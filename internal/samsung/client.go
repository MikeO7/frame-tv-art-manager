package samsung

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

// Client is the high-level facade for interacting with a single Samsung
// Frame TV. It composes the lower-level connection, art API, REST, gate,
// WoL, and remote control components into a clean interface that the
// sync engine consumes.
type Client struct {
	IP     string
	cfg    *config.Config
	logger *slog.Logger

	artConn *connection
	artAPI  *artAPI
	info    *DeviceInfo
}

// NewClient creates a new TV client. Call Connect() to establish the
// WebSocket connection.
func NewClient(ip string, cfg *config.Config, logger *slog.Logger) *Client {
	return &Client{
		IP:     ip,
		cfg:    cfg,
		logger: logger.With("tv", ip),
	}
}

// Connect establishes a connection to the TV with the following sequence:
//  1. Wake-on-LAN (if MAC configured)
//  2. Silent REST Gate (if enabled) → abort if TV is not in art mode
//  3. Open WSS connection to art endpoint on port 8002
//  4. Fetch device info via REST API
func (c *Client) Connect(ctx context.Context) error {
	// Step 1: Wake-on-LAN.
	if c.cfg.TVMAC != "" {
		c.logger.Info("sending Wake-on-LAN", "mac", c.cfg.TVMAC)
		if err := c.sendWOL(c.cfg.TVMAC); err != nil {
			c.logger.Warn("WoL failed", "error", err)
		} else {
			// Brief pause to let the TV wake up.
			time.Sleep(2 * time.Second)
		}
	}

	// Step 2: Silent REST Gate.
	if c.cfg.EnableRESTGate {
		c.logger.Debug("checking REST gate")
		inArtMode, err := c.checkArtModeGate(ctx)
		if err != nil {
			c.logger.Warn("REST gate error", "error", err)
		}
		if !inArtMode {
			c.logger.Info("REST gate: TV not in art mode, skipping to prevent popup")
			return ErrGateFailed
		}
		c.logger.Debug("REST gate: TV is in art mode")
	}

	// Step 3: Ensure we have a token (Smart Handshake for 2024 models).
	// On 2024 models, the art-app endpoint doesn't issue tokens, but the
	// remote.control endpoint does. We fetch it once to ensure persistence.
	tokenFile := c.tokenFilePath()
	if _, err := os.Stat(tokenFile); os.IsNotExist(err) {
		c.logger.Info("no token found, performing one-time remote handshake")
		// Ensure directory exists for token.
		if err := os.MkdirAll(filepath.Dir(tokenFile), 0700); err != nil { //nolint:gosec // Required token directory permissions
			return fmt.Errorf("create token dir: %w", err)
		}
		if err := c.ensureToken(ctx, tokenFile, 8002); err != nil {
			c.logger.Warn("remote handshake failed (TV might be off or busy)", "error", err)
		} else {
			// Brief pause to let the TV stabilize after authorization.
			time.Sleep(2 * time.Second)
		}
	}

	// Step 4: Open WSS connection to art endpoint.
	c.artConn = newConnection(
		c.IP, 8002, "com.samsung.art-app",
		c.cfg.ClientName, tokenFile,
		c.cfg.ConnectionTimeout, c.logger,
	)

	if err := c.artConn.Open(ctx); err != nil {
		return fmt.Errorf("connect to art endpoint: %w", err)
	}

	c.artAPI = newArtAPI(c.artConn, c.cfg.APITimeout, c.logger)

	// Step 4: Fetch device info.
	info, err := c.fetchDeviceInfo(ctx, 8002)
	if err != nil {
		c.logger.Warn("could not fetch device info", "error", err)
		// Non-fatal — we can still sync without device info.
	} else {
		c.info = info
		c.logger.Info("connected",
			"model", info.ModelName,
			"firmware", info.FirmwareVersion,
			"frameTVSupport", info.FrameTVSupport,
		)
	}

	return nil
}

// Close shuts down the WebSocket connection.
func (c *Client) Close() error {
	if c.artConn != nil {
		return c.artConn.Close()
	}
	return nil
}

// IsInArtMode checks if the TV is currently in art mode by querying
// the art API over the active WebSocket connection.
func (c *Client) IsInArtMode(ctx context.Context) bool {
	// First check power state via REST.
	if c.info != nil && !c.info.IsOn() {
		c.logger.Debug("TV is powered off")
		return false
	}

	// Then check art mode via WebSocket.
	status, err := c.artAPI.GetArtModeStatus(ctx)
	if err != nil {
		c.logger.Debug("could not determine art mode, assuming safe to sync", "error", err)
		return true // backward-compatible: if we can't tell, try anyway
	}

	isArt := status == "on"
	c.logger.Debug("art mode status", "value", status, "isArtMode", isArt)
	return isArt
}

// GetUploadedImages returns the list of user-uploaded images on the TV
// (category MY-C0002 = "My Photos").
func (c *Client) GetUploadedImages(ctx context.Context) ([]ArtContent, error) {
	return c.artAPI.GetContentList(ctx, "MY-C0002")
}

// Upload sends an image to the TV via the art API + D2D socket transfer.
// Returns the content_id assigned by the TV.
func (c *Client) Upload(ctx context.Context, filePath, fileType string) (string, error) {
	stat, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", filePath, err)
	}

	matte := c.cfg.MatteStyle

	// Register the image_added listener BEFORE sending the upload request,
	// so we don't miss the response if it arrives quickly.
	waitForAdded := c.artAPI.RegisterImageAddedListener()

	// Step 1: Send the upload request to get D2D connection info.
	connInfo, err := c.artAPI.SendImage(ctx, SendImageRequest{
		FilePath: filePath,
		FileType: fileType,
		FileSize: stat.Size(),
		Matte:    matte,
	})
	if err != nil {
		return "", fmt.Errorf("send image request: %w", err)
	}

	// Step 2: Transfer the file over D2D socket.
	if err := uploadImageD2D(ctx, *connInfo, filePath, fileType, c.cfg.ConnectionTimeout); err != nil {
		return "", fmt.Errorf("d2d transfer: %w", err)
	}

	// Step 3: Wait for the TV to confirm the image was added.
	contentID, err := waitForAdded(ctx, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("wait for confirmation: %w", err)
	}

	return contentID, nil
}

// DeleteImages removes artwork from the TV by content IDs.
func (c *Client) DeleteImages(ctx context.Context, ids []string) error {
	return c.artAPI.DeleteImages(ctx, ids)
}

// SelectImage sets the currently displayed artwork.
func (c *Client) SelectImage(ctx context.Context, id string) error {
	return c.artAPI.SelectImage(ctx, id, true)
}

// SlideshowStatus returns the current slideshow configuration.
func (c *Client) SlideshowStatus(ctx context.Context) (*SlideshowStatus, error) {
	return c.artAPI.GetSlideshowStatus(ctx)
}

// SetSlideshow updates the slideshow configuration.
func (c *Client) SetSlideshow(ctx context.Context, s SlideshowStatus) error {
	return c.artAPI.SetSlideshowStatus(ctx, s)
}

// SetBrightness sets the art mode brightness.
func (c *Client) SetBrightness(ctx context.Context, val int) error {
	return c.artAPI.SetBrightness(ctx, val)
}

// TurnOff powers off the TV by holding KEY_POWER for 3 seconds via
// a separate remote control WebSocket connection.
func (c *Client) TurnOff(ctx context.Context) error {
	return c.turnOffTV(ctx, 8002)
}

// DeviceInfo returns the cached device info, or nil if not fetched.
func (c *Client) DeviceInfo() *DeviceInfo {
	return c.info
}

// tokenFilePath returns the path to the token file for this TV.
func (c *Client) tokenFilePath() string {
	safeIP := strings.ReplaceAll(c.IP, ".", "_")
	return filepath.Join(c.cfg.TokenDir, fmt.Sprintf("tv_%s.txt", safeIP))
}

// SaveMetadata fetches all available system information and artwork categories,
// saving them to a JSON file in the tokens directory for auditing.
func (c *Client) SaveMetadata(ctx context.Context) error {
	metadata := make(map[string]any)
	metadata["timestamp"] = time.Now().Format(time.RFC3339)

	// 1. Basic Device Info.
	if c.info != nil {
		metadata["device"] = c.info
	}

	// 2. Slideshow Status.
	if ss, err := c.SlideshowStatus(ctx); err == nil {
		metadata["slideshow"] = ss
	}

	// 3. All Categories.
	if cats, err := c.artAPI.GetCategories(ctx); err == nil {
		var raw json.RawMessage
		if err := json.Unmarshal(cats, &raw); err == nil {
			metadata["categories"] = raw
		}
	}

	// 4. Detailed Environment (Placeholder for future sensors).
	metadata["platform"] = "Y2025"

	b, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	safeIP := strings.ReplaceAll(c.IP, ".", "_")
	path := filepath.Join(c.cfg.TokenDir, fmt.Sprintf("tv_%s_metadata.json", safeIP))

	if err := os.WriteFile(path, b, 0600); err != nil { //nolint:gosec // Standard file permissions
		return fmt.Errorf("write metadata file: %w", err)
	}

	c.logger.Info("metadata saved", "path", path)
	return nil
}

var macSeparators = regexp.MustCompile(`[^a-fA-F0-9]`)

func (c *Client) sendWOL(macAddr string) error {
	if macAddr == "" {
		return nil
	}

	// Strip separators and validate length.
	clean := macSeparators.ReplaceAllString(macAddr, "")
	clean = strings.ToLower(clean)
	if len(clean) != 12 {
		return fmt.Errorf("invalid MAC address %q: expected 12 hex chars, got %d", macAddr, len(clean))
	}

	// Parse hex bytes.
	mac := make([]byte, 6)
	for i := 0; i < 6; i++ {
		_, err := fmt.Sscanf(clean[i*2:i*2+2], "%02x", &mac[i])
		if err != nil {
			return fmt.Errorf("invalid MAC address %q: %w", macAddr, err)
		}
	}

	// Build magic packet: 6 bytes of 0xFF followed by MAC repeated 16 times.
	packet := make([]byte, 6+16*6)
	for i := 0; i < 6; i++ {
		packet[i] = 0xFF
	}
	for i := 0; i < 16; i++ {
		copy(packet[6+i*6:], mac)
	}

	// Send to broadcast address.
	conn, err := net.Dial("udp", "255.255.255.255:9")
	if err != nil {
		return fmt.Errorf("dial broadcast: %w", err)
	}
	defer func() { _ = conn.Close() }()

	_, err = conn.Write(packet)
	if err != nil {
		return fmt.Errorf("send magic packet: %w", err)
	}

	return nil
}

func (c *Client) checkArtModeGate(ctx context.Context) (bool, error) {
	url := fmt.Sprintf("http://%s:8001/ms/art", c.IP)

	client := &http.Client{Timeout: c.cfg.GateTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, nil // not a fatal error, just can't check
	}

	resp, err := client.Do(req)
	if err != nil {
		// Why: If we return an error here, the entire sync process aborts for all TVs.
		// By returning (false, nil), we gracefully skip this specific TV for the current cycle
		// as if it's merely "not ready", rather than throwing a hard fatal error.
		// Timeout or connection refused typically just means the TV is off or busy.
		return false, nil
	}
	defer func() { _ = resp.Body.Close() }()

	// Only 200 OK means the TV is definitively in Art Mode.
	return resp.StatusCode == http.StatusOK, nil
}

func (c *Client) ensureToken(ctx context.Context, tokenFile string, port int) error {
	conn := newConnection(c.IP, port, "samsung.remote.control", c.cfg.ClientName, tokenFile, c.cfg.ConnectionTimeout, c.logger)

	// newConnection.Open() handles the handshake and automatically saves
	// the token to tokenFile if it's received in the ms.channel.connect event.
	if err := conn.Open(ctx); err != nil {
		return fmt.Errorf("remote handshake failed: %w", err)
	}
	defer func() { _ = conn.Close() }()

	return nil
}

func (c *Client) fetchDeviceInfo(ctx context.Context, port int) (*DeviceInfo, error) {
	url := fmt.Sprintf("https://%s:%d/api/v2/", c.IP, port)

	client := &http.Client{
		Timeout: c.cfg.APITimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // lgtm[go/insecure-tls]
			}, //nolint:gosec // Required: Samsung TVs use self-signed certs for local REST; verification would prevent connection.
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Prevent DoS / resource exhaustion by enforcing a 1MB maximum read size
	maxBytes := int64(1 * 1024 * 1024)
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var envelope deviceInfoResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse device info: %w", err)
	}

	return &envelope.Device, nil
}

func (c *Client) turnOffTV(ctx context.Context, port int) error {
	conn := newConnection(c.IP, port, "samsung.remote.control", c.cfg.ClientName, c.tokenFilePath(), c.cfg.ConnectionTimeout, c.logger)

	if err := conn.Open(ctx); err != nil {
		return fmt.Errorf("open remote control connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Send KEY_POWER press.
	press := map[string]any{
		keyMethod: methodRemoteControl,
		keyParams: map[string]any{
			"Cmd":          "Press",
			"DataOfCmd":    "KEY_POWER",
			"Option":       stringFalse,
			"TypeOfRemote": "SendRemoteKey",
		},
	}

	pressPayload, err := json.Marshal(press)
	if err != nil {
		return fmt.Errorf("marshal press command: %w", err)
	}

	if err := conn.Send(pressPayload); err != nil {
		return fmt.Errorf("send press: %w", err)
	}

	// Hold for 3 seconds.
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}

	// Send KEY_POWER release.
	release := map[string]any{
		keyMethod: methodRemoteControl,
		keyParams: map[string]any{
			"Cmd":          "Release",
			"DataOfCmd":    "KEY_POWER",
			"Option":       stringFalse,
			"TypeOfRemote": "SendRemoteKey",
		},
	}

	releasePayload, err := json.Marshal(release)
	if err != nil {
		return fmt.Errorf("marshal release command: %w", err)
	}

	if err := conn.Send(releasePayload); err != nil {
		return fmt.Errorf("send release: %w", err)
	}

	return nil
}
