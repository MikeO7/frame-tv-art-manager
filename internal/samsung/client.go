package samsung

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
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
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // lgtm[go/disabled-certificate-check] Required: Samsung TVs use self-signed certs for local REST; verification would prevent connection.
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

// ArtAPI provides typed methods for Samsung Frame TV art channel operations.
// All communication happens over the WebSocket connection to the
// "com.samsung.art-app" endpoint.
const keyRequest = "request"
const keyRequestID = "request_id"

type artAPI struct {
	conn    *connection
	timeout time.Duration
	logger  *slog.Logger
}

// newArtAPI wraps an existing art-endpoint Connection with typed API methods.
func newArtAPI(conn *connection, timeout time.Duration, logger *slog.Logger) *artAPI {
	return &artAPI{
		conn:    conn,
		timeout: timeout,
		logger:  logger,
	}
}

func checkArtError(resp *artResponse) error {
	if resp.ErrorCode != 0 {
		return fmt.Errorf("%w: code %d", ErrArtAPIError, resp.ErrorCode)
	}
	return nil
}

// GetContentList retrieves the list of artwork on the TV, optionally
// filtered by category. Use "MY-C0002" for user-uploaded photos.
func (a *artAPI) GetContentList(ctx context.Context, category string) ([]ArtContent, error) {
	id := newRequestID()
	req := map[string]any{
		keyRequest:   "get_content_list",
		"id":         id,
		keyRequestID: id,
	}
	if category != "" {
		req["category_id"] = category
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return nil, fmt.Errorf("get_content_list: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return nil, fmt.Errorf("get_content_list error: %w", err)
	}

	if resp.ContentList == "" {
		return nil, nil
	}

	var items []ArtContent
	if err := json.Unmarshal([]byte(resp.ContentList), &items); err != nil {
		return nil, fmt.Errorf("parse content_list: %w", err)
	}

	// Filter by category if specified.
	if category != "" {
		filtered := make([]ArtContent, 0, len(items))
		for _, item := range items {
			if item.CategoryID == category {
				filtered = append(filtered, item)
			}
		}
		return filtered, nil
	}

	return items, nil
}

// SendImage sends an image upload request to the TV. The TV responds with
// D2D connection info that must be used to transfer the actual file bytes
// via UploadImageD2D.
func (a *artAPI) SendImage(ctx context.Context, req SendImageRequest) (*connInfo, error) {
	id := newRequestID()

	artReq := map[string]any{
		keyRequest:   "send_image",
		"file_type":  req.FileType,
		"id":         id,
		keyRequestID: id,
		"conn_info": map[string]any{
			"d2d_mode":      "socket",
			"connection_id": time.Now().UnixNano() % (4 * 1024 * 1024 * 1024),
			"id":            id,
		},
		"image_date":        time.Now().Format("2006:01:02 15:04:05"),
		"matte_id":          req.Matte,
		"portrait_matte_id": req.Matte,
		"file_size":         req.FileSize,
	}

	payload, err := artAppRequest(artReq)
	if err != nil {
		return nil, fmt.Errorf("build send_image request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return nil, fmt.Errorf("send_image: %w", err)
	}

	a.logger.Debug("send_image raw response", "raw", string(raw))
	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse send_image response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return nil, fmt.Errorf("send_image error: %w", err)
	}

	if resp.ConnInfo == "" {
		return nil, fmt.Errorf("send_image: no conn_info in response")
	}

	a.logger.Debug("send_image conn_info string", "conn_info", resp.ConnInfo)
	var ci connInfo
	if err := json.Unmarshal([]byte(resp.ConnInfo), &ci); err != nil {
		return nil, fmt.Errorf("parse conn_info: %w", err)
	}
	a.logger.Debug("send_image parsed conn_info", "ip", ci.IP, "port", ci.Port)

	return &ci, nil
}

// WaitForImageAdded blocks until the TV confirms the image was added,
// returning the assigned content_id.
func (a *artAPI) WaitForImageAdded(ctx context.Context, timeout time.Duration) (string, error) {
	// The TV sends an "image_added" event (not correlated to a request_id).
	raw, err := a.conn.SendAndWaitEvent(ctx, nil, "image_added", timeout)
	if err != nil {
		// If we sent nil payload, we just need to listen — re-register the
		// pending entry without sending anything.
		return "", fmt.Errorf("wait for image_added: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("parse image_added: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return "", fmt.Errorf("image_added error: %w", err)
	}

	return resp.ContentID, nil
}

// RegisterImageAddedListener registers a listener for the "image_added" event
// before sending the upload, so we don't miss the response.
func (a *artAPI) RegisterImageAddedListener() (waitFn func(ctx context.Context, timeout time.Duration) (string, error)) {
	ch := make(chan json.RawMessage, 1)

	a.conn.pendingMu.Lock()
	a.conn.pending["image_added"] = ch
	a.conn.pendingMu.Unlock()

	return func(ctx context.Context, timeout time.Duration) (string, error) {
		defer func() {
			a.conn.pendingMu.Lock()
			delete(a.conn.pending, "image_added")
			a.conn.pendingMu.Unlock()
		}()

		select {
		case data, ok := <-ch:
			if !ok {
				return "", ErrNotConnected
			}
			var resp artResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				return "", fmt.Errorf("parse image_added: %w", err)
			}
			if err := checkArtError(&resp); err != nil {
				return "", fmt.Errorf("image_added error: %w", err)
			}
			return resp.ContentID, nil
		case <-time.After(timeout):
			return "", fmt.Errorf("%w: waiting for image_added", ErrTimeout)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// DeleteImages removes artwork from the TV by their content IDs.
func (a *artAPI) DeleteImages(ctx context.Context, contentIDs []string) error {
	id := newRequestID()

	contentIDList := make([]map[string]string, len(contentIDs))
	for i, cid := range contentIDs {
		contentIDList[i] = map[string]string{"content_id": cid}
	}

	req := map[string]any{
		keyRequest:        "delete_image_list",
		"id":              id,
		keyRequestID:      id,
		"content_id_list": contentIDList,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return fmt.Errorf("delete_image_list: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("parse delete_image_list response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return fmt.Errorf("delete_image_list error: %w", err)
	}

	return nil
}

// SelectImage sets the currently displayed artwork on the TV.
func (a *artAPI) SelectImage(ctx context.Context, contentID string, show bool) error {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "select_image",
		"id":         id,
		keyRequestID: id,
		"content_id": contentID,
		"show":       show,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return fmt.Errorf("build select_image request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return fmt.Errorf("select_image: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("parse select_image response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return fmt.Errorf("select_image error: %w", err)
	}

	return nil
}

// GetArtModeStatus returns "on" if the TV is in art mode, "off" otherwise.
func (a *artAPI) GetArtModeStatus(ctx context.Context) (string, error) {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "get_artmode_status",
		"id":         id,
		keyRequestID: id,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return "", fmt.Errorf("build get_artmode_status request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return "", fmt.Errorf("get_artmode_status: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("parse artmode_status: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return "", fmt.Errorf("artmode_status error: %w", err)
	}

	return resp.Value, nil
}

// GetSlideshowStatus retrieves the current slideshow configuration.
func (a *artAPI) GetSlideshowStatus(ctx context.Context) (*SlideshowStatus, error) {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "get_slideshow_status",
		"id":         id,
		keyRequestID: id,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build get_slideshow_status request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return nil, fmt.Errorf("get_slideshow_status: %w", err)
	}

	var artResp artResponse
	if err := json.Unmarshal(raw, &artResp); err != nil {
		return nil, fmt.Errorf("parse get_slideshow_status response: %w", err)
	}

	if err := checkArtError(&artResp); err != nil {
		return nil, fmt.Errorf("get_slideshow_status error: %w", err)
	}

	var resp struct {
		Value      string `json:"value"`
		Type       string `json:"type"`
		CategoryID string `json:"category_id"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse slideshow_status: %w", err)
	}

	return &SlideshowStatus{
		Value:      resp.Value,
		Type:       resp.Type,
		CategoryID: resp.CategoryID,
	}, nil
}

// SetSlideshowStatus updates the slideshow configuration on the TV.
func (a *artAPI) SetSlideshowStatus(ctx context.Context, s SlideshowStatus) error {
	id := newRequestID()

	req := map[string]any{
		keyRequest:    "set_slideshow_status",
		"id":          id,
		keyRequestID:  id,
		"value":       s.Value,
		"category_id": s.CategoryID,
		"type":        s.Type,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return fmt.Errorf("build set_slideshow_status request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return fmt.Errorf("set_slideshow_status: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("parse set_slideshow_status response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return fmt.Errorf("set_slideshow_status error: %w", err)
	}

	return nil
}

// SetBrightness sets the art mode brightness (typically 0–10 or 0–50
// depending on TV model).
func (a *artAPI) SetBrightness(ctx context.Context, value int) error {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "set_brightness",
		"id":         id,
		keyRequestID: id,
		"value":      value,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return fmt.Errorf("build set_brightness request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return fmt.Errorf("set_brightness: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("parse set_brightness response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return fmt.Errorf("set_brightness error: %w", err)
	}

	return nil
}

// GetCategories retrieves the list of all artwork categories available on the TV.
// Example: [{"category_id":"MY-C0002","category_name":"My Photos"}]
func (a *artAPI) GetCategories(ctx context.Context) (json.RawMessage, error) {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "get_categories",
		"id":         id,
		keyRequestID: id,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build get_categories request: %w", err)
	}

	raw, err := a.conn.SendAndWait(ctx, payload, id, a.timeout)
	if err != nil {
		return nil, fmt.Errorf("get_categories: %w", err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse get_categories response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return nil, fmt.Errorf("get_categories error: %w", err)
	}

	return raw, nil
}

// connection manages a single WSS connection to a Samsung Frame TV endpoint.
//
// The TV uses two WebSocket endpoints:
//   - "com.samsung.art-app"       — for art management (upload, list, select, etc.)
//   - "samsung.remote.control"    — for remote key commands (power off)
//
// Each endpoint requires its own connection instance, but they share the
// same token file for authentication.
type connection struct {
	host      string
	port      int
	endpoint  string
	name      string // client identity sent in WebSocket URL
	tokenFile string
	timeout   time.Duration
	logger    *slog.Logger

	mu       sync.Mutex
	conn     *websocket.Conn
	closed   atomic.Bool
	recvDone chan struct{}

	// pending tracks outstanding art API requests by request ID.
	// When a response arrives, the raw JSON is sent to the channel.
	pendingMu sync.Mutex
	pending   map[string]chan json.RawMessage
}

// newConnection creates a new WebSocket connection manager. It does not
// connect automatically — call Open() to establish the connection.
func newConnection(host string, port int, endpoint, name, tokenFile string, timeout time.Duration, logger *slog.Logger) *connection {
	return &connection{
		host:      host,
		port:      port,
		endpoint:  endpoint,
		name:      name,
		tokenFile: tokenFile,
		timeout:   timeout,
		logger:    logger,
		pending:   make(map[string]chan json.RawMessage),
	}
}

// Open establishes the WSS connection, performs the handshake, and starts
// the background receive loop. On first connection, the TV will show an
// Allow/Deny prompt — the user must accept within the timeout period.
//
// The handshake sequence for the art endpoint is:
//  1. Dial wss://<host>:8002/api/v2/channels/<endpoint>?name=<b64>&token=<tok>
//  2. Receive ms.channel.connect event → extract and save token
//  3. Receive ms.channel.ready event → connection is live
//
// For the remote control endpoint, only step 1-2 is needed.
//
//nolint:gocyclo // Connection handshake sequence is inherently complex
func (c *connection) Open(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil // already connected
	}

	token := c.readToken()
	wsURL := c.formatURL(token)
	c.logger.Debug("dialing WebSocket", "url", wsURL)

	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // lgtm[go/disabled-certificate-check] Required: Samsung TVs use self-signed certs for local WSS; verification would prevent connection.
		HandshakeTimeout: c.timeout,
	}

	conn, httpResp, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	if httpResp != nil && httpResp.Body != nil {
		defer func() { _ = httpResp.Body.Close() }()
	}

	// Read the first message — expect ms.channel.connect.
	if err := conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("set read deadline: %w", err)
	}

	_, msg, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("read handshake: %w", err)
	}

	c.logger.Debug("handshake message received", "msg", string(msg))
	var resp wsResponse
	if err := json.Unmarshal(msg, &resp); err != nil {
		_ = conn.Close()
		return fmt.Errorf("parse handshake: %w", err)
	}

	switch resp.Event {
	case EventChannelConnect:
		c.extractAndSaveToken(resp.Data)
	case "ms.channel.unauthorized":
		_ = conn.Close()
		return ErrUnauthorized
	case "ms.channel.timeOut":
		_ = conn.Close()
		return ErrTimeout
	default:
		_ = conn.Close()
		return fmt.Errorf("%w: unexpected event %q", ErrConnectionFailure, resp.Event)
	}

	// For the art endpoint, also wait for ms.channel.ready.
	if c.endpoint == "com.samsung.art-app" {
		if err := conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
			_ = conn.Close()
			return fmt.Errorf("set read deadline: %w", err)
		}

		_, msg, err = conn.ReadMessage()
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("read channel ready: %w", err)
		}

		var readyResp wsResponse
		if err := json.Unmarshal(msg, &readyResp); err != nil {
			_ = conn.Close()
			return fmt.Errorf("parse channel ready: %w", err)
		}

		if readyResp.Event != "ms.channel.ready" {
			_ = conn.Close()
			return fmt.Errorf("%w: expected ms.channel.ready, got %q", ErrConnectionFailure, readyResp.Event)
		}
	}

	// Clear the read deadline for the recv loop.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("clear read deadline: %w", err)
	}

	c.conn = conn
	c.closed.Store(false)
	c.recvDone = make(chan struct{})
	go c.recvLoop()

	c.logger.Info("WebSocket connected", "endpoint", c.endpoint, "host", c.host)
	return nil
}

// Close shuts down the WebSocket connection and waits for the recv loop to exit.
func (c *connection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil
	}

	c.closed.Store(true)
	err := c.conn.Close()
	c.conn = nil

	// Wait for recv loop to finish with a timeout.
	if c.recvDone != nil {
		select {
		case <-c.recvDone:
		case <-time.After(500 * time.Millisecond):
			c.logger.Debug("recv loop did not exit quickly, continuing", "endpoint", c.endpoint)
		}
	}

	// Cancel all pending requests.
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()

	return err
}

// IsAlive returns true if the connection is open and not closed.
func (c *connection) IsAlive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && !c.closed.Load()
}

// SendAndWait sends a JSON payload and waits for a response matching the
// given request ID. Returns the raw d2d event data JSON.
func (c *connection) SendAndWait(ctx context.Context, payload []byte, requestID string, timeout time.Duration) (json.RawMessage, error) {
	ch := make(chan json.RawMessage, 1)

	c.pendingMu.Lock()
	c.pending[requestID] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, requestID)
		c.pendingMu.Unlock()
	}()

	if err := c.Send(payload); err != nil {
		return nil, err
	}

	select {
	case data, ok := <-ch:
		if !ok {
			return nil, ErrNotConnected
		}
		return data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("%w: waiting for response %s", ErrTimeout, requestID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SendAndWaitEvent sends a payload and waits for a response matching
// a specific event name (e.g. "image_added") instead of a request ID.
func (c *connection) SendAndWaitEvent(ctx context.Context, payload []byte, eventName string, timeout time.Duration) (json.RawMessage, error) {
	return c.SendAndWait(ctx, payload, eventName, timeout)
}

// Send writes a JSON text message to the WebSocket.
func (c *connection) Send(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return ErrNotConnected
	}

	c.logger.Debug("WS SEND", "payload", string(payload))
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	return c.conn.WriteMessage(websocket.TextMessage, payload)
}

// --- internal ---

// recvLoop reads messages from the WebSocket and routes them to pending
// request channels based on request_id or event name.
func (c *connection) recvLoop() {
	defer close(c.recvDone)

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return
	}

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if !c.closed.Load() {
				c.logger.Debug("recv loop error", "error", err)
			}
			return
		}

		c.logger.Debug("WS RECV", "payload", string(msg))

		var resp wsResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			c.logger.Debug("recv: unparseable message", "error", err)
			continue
		}

		// Route d2d service messages to pending requests.
		// Support both dot-notation and underscore-notation used by different models.
		if resp.Event == EventD2DServiceMessageEvent || resp.Event == EventD2DServiceMessage {
			c.routeD2DEvent(resp.Data)
		}
	}
}

func (c *connection) routeD2DEvent(dataRaw json.RawMessage) {
	// Some TVs (like the 2024 model) send 'data' as a JSON-encoded string.
	// Others send it as a raw JSON object. We try to handle both.
	var dataToParse []byte = dataRaw

	var dataStr string
	if err := json.Unmarshal(dataRaw, &dataStr); err == nil {
		// It was a string! Use the unwrapped string content for parsing.
		dataToParse = []byte(dataStr)
	}

	var inner struct {
		RequestID string `json:"request_id"`
		ID        string `json:"id"`
		Event     string `json:"event"`
	}
	if err := json.Unmarshal(dataToParse, &inner); err != nil {
		c.logger.Debug("d2d event: parse failed", "error", err, "raw", string(dataRaw))
		return
	}

	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	// Try matching by request_id first, then event name.
	keys := []string{inner.RequestID, inner.ID, inner.Event}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if ch, ok := c.pending[key]; ok {
			select {
			case ch <- dataToParse:
			default:
			}
			return
		}
	}
}

// formatURL builds the WebSocket URL for the specified endpoint.
func (c *connection) formatURL(token string) string {
	b64Name := base64.StdEncoding.EncodeToString([]byte(c.name))
	u := url.URL{
		Scheme: "wss",
		Host:   fmt.Sprintf("%s:%d", c.host, c.port),
		Path:   fmt.Sprintf("/api/v2/channels/%s", c.endpoint),
	}
	q := u.Query()
	q.Set("name", b64Name)
	if token != "" {
		q.Set("token", token)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// readToken reads the saved auth token from the token file.
// Returns empty string if the file doesn't exist yet.
func (c *connection) readToken() string {
	data, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// extractAndSaveToken pulls the token from a ms.channel.connect response
// and writes it to the token file.
func (c *connection) extractAndSaveToken(data json.RawMessage) {
	var d struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		c.logger.Debug("failed to parse token from data", "error", err, "data", string(data))
		return
	}
	if d.Token == "" {
		c.logger.Debug("token field is empty in handshake data", "data", string(data))
		return
	}

	c.logger.Info("new auth token received", "token", d.Token[:min(len(d.Token), 8)]+"...")
	if err := os.WriteFile(c.tokenFile, []byte(d.Token), 0600); err != nil { //nolint:gosec // Safe token write
		c.logger.Error("failed to save token", "error", err, "file", c.tokenFile)
	}
}

// wsResponse is the top-level WebSocket message envelope from the TV.
type wsResponse struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// artAppRequest builds the outer WebSocket message for an art API request.
const keyMethod = "method"
const keyParams = "params"
const keyEvent = "event"
const keyData = "data"

func artAppRequest(data map[string]any) ([]byte, error) {
	inner, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	outer := map[string]any{
		keyMethod: "ms.channel.emit",
		keyParams: map[string]any{
			keyEvent: "art_app_request",
			"to":     "host",
			keyData:  string(inner),
		},
	}
	return json.Marshal(outer)
}

// newRequestID generates a new UUID string for art API request correlation.
func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

const d2dChunkSize = 64 * 1024 // 64KB chunks for image transfer

// UploadImageD2D transfers an image file to the TV via a direct TCP/TLS
// socket connection. This is the "Device-to-Device" transfer protocol
// used by Samsung Frame TVs for high-resolution image uploads.
//
// Protocol:
//  1. Connect to info.IP:info.Port (TLS if info.Secured)
//  2. Send 4-byte big-endian header length
//  3. Send JSON header with file metadata and security key
//  4. Send raw image bytes in 64KB chunks
//  5. Close socket
//
// The caller must separately wait for the "image_added" event on the
// WebSocket to confirm the upload succeeded and get the content_id.
func uploadImageD2D(ctx context.Context, info connInfo, filePath string, fileType string, timeout time.Duration) error {
	// Open the image file.
	f, err := os.Open(filepath.Clean(filePath)) //nolint:gosec // Path is verified
	if err != nil {
		return fmt.Errorf("open image file: %w", err)
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat image file: %w", err)
	}
	fileSize := stat.Size()

	// Build the D2D header.
	header := map[string]any{
		"num":        0,
		"total":      1,
		"fileLength": fileSize,
		"fileName":   "dummy",
		"fileType":   fileType,
		"secKey":     info.Key,
		"version":    "0.0.1",
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("marshal d2d header: %w", err)
	}

	// Connect to the TV's D2D socket.
	addr := fmt.Sprintf("%s:%s", info.IP, info.Port)
	dialer := net.Dialer{Timeout: timeout}

	var conn net.Conn
	if info.Secured {
		tlsConf := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // lgtm[go/disabled-certificate-check] Samsung self-signed cert
		conn, err = tls.DialWithDialer(&dialer, "tcp", addr, tlsConf)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("dial d2d socket %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	// Set a write deadline for the entire transfer.
	if err := conn.SetWriteDeadline(time.Now().Add(timeout + time.Duration(fileSize/d2dChunkSize)*100*time.Millisecond)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}

	// Send: [4-byte header length][header JSON][file bytes]
	headerLen := make([]byte, 4)
	binary.BigEndian.PutUint32(headerLen, uint32(len(headerJSON))) //nolint:gosec // JSON header length is small

	if _, err := conn.Write(headerLen); err != nil {
		return fmt.Errorf("write header length: %w", err)
	}

	if _, err := conn.Write(headerJSON); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	// Stream file in chunks.
	buf := make([]byte, d2dChunkSize)
	var totalWritten int64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			if _, writeErr := conn.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("write image data at offset %d: %w", totalWritten, writeErr)
			}
			totalWritten += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read image file: %w", readErr)
		}
	}

	if totalWritten != fileSize {
		return fmt.Errorf("incomplete transfer: wrote %d of %d bytes", totalWritten, fileSize)
	}

	return nil
}

// SyncRequest defines the dynamic state and configurations for a single TV sync run.
type SyncRequest struct {
	LocalFiles        map[string]struct{}
	Mapping           map[string]string // filename -> contentID
	MatteOverrides    map[string]string // filename -> matte style
	DesiredBrightness *int
	Slideshow         *SlideshowStatus
	TriggerAutoOff    bool
}

// SyncResult returns the synchronization outcome, including details for updating the mapping cache.
type SyncResult struct {
	Model        string
	Status       string
	ArtMode      bool
	Uploaded     int
	Deleted      int
	TotalImages  int
	Brightness   string
	Slideshow    string
	ErrorMessage string

	// Mapping database updates
	NewUploads   map[string]string // filename -> contentID
	DeletedFiles []string          // list of filenames deleted
}

// Sync performs the complete synchronization cycle for the TV, completely encapsulating
// WOL, connection, art mode checks, inventory diffing, uploads, deletes, and auto-off.
func (c *Client) Sync(ctx context.Context, req SyncRequest) (SyncResult, error) {
	result := SyncResult{
		NewUploads: make(map[string]string),
	}

	// 1. Connect to the TV.
	if err := c.Connect(ctx); err != nil {
		if errors.Is(err, ErrGateFailed) {
			c.logger.Info("skipping — REST gate says TV is busy")
			result.Status = "skipped (gate)"
			return result, nil
		}
		result.Status = "error"
		return result, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = c.Close() }()

	// 2. Fetch basic device info.
	if c.info != nil {
		result.Model = c.info.ModelName
	}

	// 3. Check art mode.
	if !c.IsInArtMode(ctx) {
		c.logger.Info("skipping — TV not in art mode")
		result.Status = "skipped (not art mode)"
		return result, nil
	}
	result.ArtMode = true

	// 4. Background metadata save (handled by caller if throttled, or just run inline here)
	// We can save metadata inline since it is fast.
	if err := c.SaveMetadata(ctx); err != nil {
		c.logger.Debug("could not save metadata", "error", err)
	}

	// 5. Query currently uploaded images.
	tvContent, err := c.GetUploadedImages(ctx)
	if err != nil {
		result.Status = "error"
		return result, fmt.Errorf("get TV images: %w", err)
	}

	// 6. Reconciliation: split TV content into tracked vs unknown.
	trackedFiles := make(map[string]struct{})
	unknownIDs := make(map[string]struct{})

	// Reverse map
	reverseMap := make(map[string]string, len(req.Mapping))
	for filename, cid := range req.Mapping {
		reverseMap[cid] = filename
	}

	// Reconcile database entries: find stale mappings that are no longer on the TV.
	liveIDs := make(map[string]bool)
	for _, item := range tvContent {
		liveIDs[item.ContentID] = true
	}

	for filename, contentID := range req.Mapping {
		if !liveIDs[contentID] {
			c.logger.Debug("purging stale mapping (not on TV)", "file", filename, "id", contentID)
			result.DeletedFiles = append(result.DeletedFiles, filename)
		}
	}

	for _, item := range tvContent {
		if filename, ok := reverseMap[item.ContentID]; ok {
			trackedFiles[filename] = struct{}{}
		} else {
			unknownIDs[item.ContentID] = struct{}{}
		}
	}

	c.logger.Info("TV inventory",
		"tracked", len(trackedFiles),
		"unknown", len(unknownIDs),
	)

	// 7. Diff: determine uploads and deletes.
	toUpload := diffSets(req.LocalFiles, trackedFiles)
	toDelete := diffSets(trackedFiles, req.LocalFiles)

	if len(unknownIDs) > 0 {
		if c.cfg.RemoveUnknownImages {
			c.logger.Info("will remove unknown images", "count", len(unknownIDs))
		} else {
			c.logger.Warn("unknown images on TV (set REMOVE_UNKNOWN_IMAGES=true to remove)",
				"count", len(unknownIDs))
		}
	}

	c.logger.Info("sync plan",
		"to_upload", len(toUpload),
		"to_delete", len(toDelete),
		"unknown_to_delete", boolCount(c.cfg.RemoveUnknownImages, len(unknownIDs)),
	)

	// 8. Capture slideshow settings.
	var preserveSlideshow *SlideshowStatus
	hasChanges := len(toUpload) > 0 || len(toDelete) > 0 || (c.cfg.RemoveUnknownImages && len(unknownIDs) > 0)
	if hasChanges && !c.cfg.SlideshowOverride {
		preserveSlideshow, _ = c.SlideshowStatus(ctx)
	}

	// 9. Upload new images.
	for filename := range toUpload {
		if c.cfg.DryRun {
			c.logger.Info("[DRY RUN] would upload", "file", filename)
			result.Uploaded++
			continue
		}

		filePath := filepath.Join(c.cfg.ArtworkDir, filename)
		fileType := fileTypeFromExt(filename)

		// Determine matte style
		matte := c.cfg.MatteStyle
		if customMatte, ok := req.MatteOverrides[filename]; ok {
			matte = customMatte
		}

		c.logger.Info("uploading", "file", filename, "matte", matte)

		contentID, err := c.Upload(ctx, filePath, fileType)
		if err != nil {
			c.logger.Error("upload failed", "file", filename, "error", err)
			time.Sleep(c.cfg.UploadDelay * 2)
			continue
		}

		result.NewUploads[filename] = contentID
		c.logger.Info("uploaded", "file", filename, "content_id", contentID)
		result.Uploaded++

		time.Sleep(c.cfg.UploadDelay)
	}

	// 10. Delete tracked images.
	if len(toDelete) > 0 {
		var idsToDelete []string
		var filesToDelete []string
		for filename := range toDelete {
			if cid, ok := req.Mapping[filename]; ok {
				idsToDelete = append(idsToDelete, cid)
				filesToDelete = append(filesToDelete, filename)
			}
		}

		if len(idsToDelete) > 0 {
			if c.cfg.DryRun {
				c.logger.Info("[DRY RUN] would delete tracked images", "count", len(idsToDelete))
			} else {
				c.logger.Info("deleting tracked images", "count", len(idsToDelete))
				if err := c.DeleteImages(ctx, idsToDelete); err != nil {
					c.logger.Error("batch delete failed", "error", err)
				} else {
					result.DeletedFiles = append(result.DeletedFiles, filesToDelete...)
					c.logger.Info("deleted tracked images", "count", len(idsToDelete))
				}
			}
			result.Deleted = len(idsToDelete)
		}
	}

	// 11. Delete unknown images.
	if c.cfg.RemoveUnknownImages && len(unknownIDs) > 0 {
		ids := setToSlice(unknownIDs)
		if c.cfg.DryRun {
			c.logger.Info("[DRY RUN] would delete unknown images", "count", len(ids))
		} else {
			c.logger.Info("deleting unknown images", "count", len(ids))
			if err := c.DeleteImages(ctx, ids); err != nil {
				c.logger.Error("delete unknown images failed", "error", err)
			}
		}
	}

	// 12. Select image and restore/apply slideshow.
	var finalMapping = make(map[string]string)
	for k, v := range req.Mapping {
		finalMapping[k] = v
	}
	for _, f := range result.DeletedFiles {
		delete(finalMapping, f)
	}
	for k, v := range result.NewUploads {
		finalMapping[k] = v
	}

	if hasChanges && len(req.LocalFiles) > 0 {
		if len(finalMapping) > 0 {
			var selectedID string

			settingsForMode := req.Slideshow
			if settingsForMode == nil {
				settingsForMode = preserveSlideshow
			}

			if settingsForMode != nil && settingsForMode.Type == "shuffleslideshow" {
				values := mapValues(finalMapping)
				selectedID = values[mathrand.IntN(len(values))] //nolint:gosec // Shuffle selection does not require cryptographically secure rand
				c.logger.Info("selecting random image for shuffle mode")
			} else if len(finalMapping) > 0 {
				for _, id := range finalMapping {
					selectedID = id
					break
				}
				c.logger.Info("selecting first image")
			}

			if selectedID != "" && !c.cfg.DryRun {
				if err := c.SelectImage(ctx, selectedID); err != nil {
					c.logger.Warn("failed to select image", "error", err)
				}
			}

			if preserveSlideshow != nil && !c.cfg.DryRun {
				if err := c.SetSlideshow(ctx, *preserveSlideshow); err != nil {
					c.logger.Warn("failed to restore slideshow", "error", err)
				}
			}
		}
	}

	// Apply slideshow override.
	if req.Slideshow != nil && !c.cfg.DryRun {
		current, _ := c.SlideshowStatus(ctx)
		needsUpdate := current == nil ||
			current.Value != req.Slideshow.Value ||
			current.Type != req.Slideshow.Type

		if needsUpdate {
			c.logger.Info("updating slideshow settings",
				"interval", req.Slideshow.Value,
				"type", req.Slideshow.Type,
			)
			if err := c.SetSlideshow(ctx, *req.Slideshow); err != nil {
				c.logger.Warn("failed to set slideshow", "error", err)
			}
		}
		result.Slideshow = fmt.Sprintf("%s every %s min", req.Slideshow.Type, req.Slideshow.Value)
	}

	// 13. Apply brightness.
	if req.DesiredBrightness != nil && !c.cfg.DryRun {
		if err := c.SetBrightness(ctx, *req.DesiredBrightness); err != nil {
			c.logger.Warn("failed to set brightness", "error", err)
		}
		result.Brightness = fmt.Sprintf("%d", *req.DesiredBrightness)
	}

	// 14. Auto-off trigger.
	if req.TriggerAutoOff {
		c.logger.Info("within auto-off window, turning off TV")
		if !c.cfg.DryRun {
			if err := c.TurnOff(ctx); err != nil {
				c.logger.Warn("failed to turn off TV", "error", err)
			} else {
				c.logger.Info("TV turned off")
			}
		}
	}

	// 15. Final stats.
	result.TotalImages = len(trackedFiles) + result.Uploaded - result.Deleted
	result.Status = "ok"

	c.logger.Info("sync completed")
	return result, nil
}

// --- Internal Helper Functions ---

func fileTypeFromExt(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".png" {
		return "png"
	}
	return "jpg"
}

func diffSets(a, b map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{})
	for k := range a {
		if _, ok := b[k]; !ok {
			result[k] = struct{}{}
		}
	}
	return result
}

func setToSlice(s map[string]struct{}) []string {
	result := make([]string, 0, len(s))
	for k := range s {
		result = append(result, k)
	}
	return result
}

func mapValues(m map[string]string) []string {
	result := make([]string, 0, len(m))
	for _, v := range m {
		result = append(result, v)
	}
	return result
}

func boolCount(cond bool, count int) int {
	if cond {
		return count
	}
	return 0
}
