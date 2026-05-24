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
	"sync"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

const (
	keyRequest   = "request"
	keyRequestID = "request_id"
)

// Client is the high-level facade for interacting with a single Samsung
// Frame TV. It composes the lower-level connection, REST, gate,
// WoL, and remote control components into a clean interface that the
// sync engine consumes.
type Client struct {
	IP     string
	opts   config.TVConnectOptions
	logger *slog.Logger

	artConn *connection
	info    *DeviceInfo

	// Persistent backoff state
	mu           sync.Mutex
	failures     int
	lastFailure  time.Time
	backoffUntil time.Time
}

// NewClient creates a new TV client. Call Connect() to establish the
// WebSocket connection.
//
// Parameters:
//   - ip: The IPv4 address of the Samsung TV (e.g., "192.168.1.100").
//   - opts: Connection options including timeouts, token directory, and the identity sent during handshakes.
//   - logger: A structured logger for emitting connection state changes and errors.
//
// Returns:
//   - *Client: An un-connected TV client instance ready for the Connect() method.
//
// Example:
//
//	tvOpts := config.TVConnectOptions{
//	    ClientName: "Living Room Frame",
//	    TokenDir:   "/data/tokens",
//	    ConnectionTimeout: 30 * time.Second,
//	}
//	client := samsung.NewClient("192.168.1.150", tvOpts, logger)
//	err := client.Connect(ctx)
//	if err != nil {
//	    log.Println("Connection failed:", err)
//	}
func NewClient(ip string, opts config.TVConnectOptions, logger *slog.Logger) *Client {
	return &Client{
		IP:     ip,
		opts:   opts,
		logger: logger.With("tv", ip),
	}
}

// connect establishes a connection to the TV with the following sequence:
//  1. Wake-on-LAN (if MAC configured)
//  2. Silent REST Gate (if enabled) → abort if TV is not in art mode
//  3. Open WSS connection to art endpoint on port 8002
//  4. Fetch device info via REST API
//
//nolint:gocognit,nestif // complexity justified for this domain-specific path
func (c *Client) connect(ctx context.Context) error {
	// Step 1: Wake-on-LAN.
	if c.opts.TVMAC != "" {
		c.logger.Info("sending Wake-on-LAN", "mac", c.opts.TVMAC)
		if err := c.sendWOL(ctx, c.opts.TVMAC); err != nil {
			c.logger.Warn("WoL failed", "error", err)
		} else {
			// Brief pause to let the TV wake up.
			time.Sleep(2 * time.Second)
		}
	}

	// Step 2: Silent REST Gate.
	if c.opts.EnableRESTGate {
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
		if err := os.MkdirAll(filepath.Dir(tokenFile), 0o700); err != nil {
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
		c.opts.ClientName, tokenFile,
		c.opts.ConnectionTimeout, c.logger,
	)

	if err := c.artConn.Open(ctx); err != nil {
		return fmt.Errorf("connect to art endpoint: %w", err)
	}

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

// ShouldSkip returns true if the TV is in a backoff window due to failures.
func (c *Client) ShouldSkip() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Now().Before(c.backoffUntil) {
		remaining := time.Until(c.backoffUntil).Round(time.Second)
		c.logger.Info("TV in backoff period, skipping",
			"failures", c.failures,
			"retry_in", remaining.String(),
		)
		return true
	}
	return false
}

// RecordFailure tracks a connection failure and calculates exponential backoff.
func (c *Client) RecordFailure(baseInterval time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failures++
	c.lastFailure = time.Now()

	maxDelay := 1 * time.Hour
	delay := baseInterval
	for i := 1; i < c.failures; i++ {
		if delay >= maxDelay {
			delay = maxDelay
			break
		}
		if delay > maxDelay/2 {
			delay = maxDelay
			break
		}
		delay *= 2
	}
	if delay > maxDelay {
		delay = maxDelay
	}

	c.backoffUntil = c.lastFailure.Add(delay)

	c.logger.Warn("TV unreachable, backing off",
		"consecutive_failures", c.failures,
		"next_retry", c.backoffUntil.Format(time.Kitchen),
		"backoff_duration", delay.Round(time.Second).String(),
	)
}

// RecordSuccess resets failure count.
func (c *Client) RecordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.failures > 0 {
		c.logger.Info("TV recovered after failures",
			"previous_failures", c.failures,
		)
	}
	c.failures = 0
	c.backoffUntil = time.Time{}
}

func checkArtError(resp *artResponse) error {
	if resp.ErrorCode != 0 {
		return fmt.Errorf("%w: code %d", ErrArtAPIError, resp.ErrorCode)
	}
	return nil
}

// isInArtMode checks if the TV is currently in art mode by querying
// the art API over the active WebSocket connection.
func (c *Client) isInArtMode(ctx context.Context) bool {
	// First check power state via REST.
	if c.info != nil && !c.info.IsOn() {
		c.logger.Debug("TV is powered off")
		return false
	}

	id := newRequestID()
	req := map[string]any{
		keyRequest:   "get_artmode_status",
		"id":         id,
		keyRequestID: id,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		c.logger.Debug("could not build get_artmode_status request, assuming safe to sync", "error", err)
		return true
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
	if err != nil {
		c.logger.Debug("could not determine art mode, assuming safe to sync", "error", err)
		return true // backward-compatible: if we can't tell, try anyway
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.logger.Debug("parse artmode_status failed, assuming safe to sync", "error", err)
		return true
	}

	if err := checkArtError(&resp); err != nil {
		c.logger.Debug("get_artmode_status error response, assuming safe to sync", "error", err)
		return true
	}

	isArt := resp.Value == "on"
	c.logger.Debug("art mode status", "value", resp.Value, "isArtMode", isArt)
	return isArt
}

// getUploadedImages returns the list of user-uploaded images on the TV
// (category MY-C0002 = "My Photos").
func (c *Client) getUploadedImages(ctx context.Context) ([]ArtContent, error) {
	id := newRequestID()
	req := map[string]any{
		keyRequest:    "get_content_list",
		"id":          id,
		keyRequestID:  id,
		"category_id": "MY-C0002",
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
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
	filtered := make([]ArtContent, 0, len(items))
	for _, item := range items {
		if item.CategoryID == "MY-C0002" {
			filtered = append(filtered, item)
		}
	}

	return filtered, nil
}

func (c *Client) registerImageAddedListener() (waitFn func(ctx context.Context, timeout time.Duration) (string, error)) {
	ch := make(chan json.RawMessage, 1)

	c.artConn.pendingMu.Lock()
	c.artConn.pending["image_added"] = ch
	c.artConn.pendingMu.Unlock()

	return func(ctx context.Context, timeout time.Duration) (string, error) {
		defer func() {
			if c.artConn != nil {
				c.artConn.pendingMu.Lock()
				delete(c.artConn.pending, "image_added")
				c.artConn.pendingMu.Unlock()
			}
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

// upload sends an image to the TV via the art API + D2D socket transfer.
//
//nolint:funlen // complexity justified for this domain-specific path
func (c *Client) upload(ctx context.Context, filePath, fileType, matte string) (string, error) {
	stat, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", filePath, err)
	}

	if matte == "" {
		matte = c.opts.MatteStyle
	}

	// Register the image_added listener BEFORE sending the upload request,
	// so we don't miss the response if it arrives quickly.
	waitForAdded := c.registerImageAddedListener()

	// Step 1: Send the upload request to get D2D connection info.
	id := newRequestID()
	artReq := map[string]any{
		keyRequest:   "send_image",
		"file_type":  fileType,
		"id":         id,
		keyRequestID: id,
		"conn_info": map[string]any{
			"d2d_mode":      "socket",
			"connection_id": time.Now().UnixNano() % (4 * 1024 * 1024 * 1024),
			"id":            id,
		},
		"image_date":        time.Now().Format("2006:01:02 15:04:05"),
		"matte_id":          matte,
		"portrait_matte_id": matte,
		"file_size":         stat.Size(),
	}

	payload, err := artAppRequest(artReq)
	if err != nil {
		return "", fmt.Errorf("build send_image request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
	if err != nil {
		return "", fmt.Errorf("send_image: %w", err)
	}

	c.logger.Debug("send_image raw response", "raw", string(raw))
	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("parse send_image response: %w", err)
	}

	if err := checkArtError(&resp); err != nil {
		return "", fmt.Errorf("send_image error: %w", err)
	}

	if resp.ConnInfo == "" {
		return "", fmt.Errorf("send_image: no conn_info in response")
	}

	c.logger.Debug("send_image conn_info string", "conn_info", resp.ConnInfo)
	var ci connInfo
	if err := json.Unmarshal([]byte(resp.ConnInfo), &ci); err != nil {
		return "", fmt.Errorf("parse conn_info: %w", err)
	}
	c.logger.Debug("send_image parsed conn_info", "ip", ci.IP, "port", ci.Port)

	// Step 2: Transfer the file over D2D socket.
	if err := uploadImageD2D(ctx, ci, filePath, fileType, c.opts.ConnectionTimeout); err != nil {
		return "", fmt.Errorf("d2d transfer: %w", err)
	}

	// Step 3: Wait for the TV to confirm the image was added.
	contentID, err := waitForAdded(ctx, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("wait for confirmation: %w", err)
	}

	return contentID, nil
}

// deleteImages removes artwork from the TV by content IDs.
func (c *Client) deleteImages(ctx context.Context, ids []string) error {
	id := newRequestID()

	contentIDList := make([]map[string]string, len(ids))
	for i, cid := range ids {
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

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
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

// selectImage sets the currently displayed artwork.
func (c *Client) selectImage(ctx context.Context, id string) error {
	reqID := newRequestID()

	req := map[string]any{
		keyRequest:   "select_image",
		"id":         reqID,
		keyRequestID: reqID,
		"content_id": id,
		"show":       true,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return fmt.Errorf("build select_image request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, reqID, c.opts.APITimeout)
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

// slideshowStatus returns the current slideshow configuration.
func (c *Client) slideshowStatus(ctx context.Context) (*SlideshowStatus, error) {
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

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
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

// setSlideshow updates the slideshow configuration.
func (c *Client) setSlideshow(ctx context.Context, s SlideshowStatus) error {
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

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
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

// setBrightness sets the art mode brightness.
func (c *Client) setBrightness(ctx context.Context, val int) error {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "set_brightness",
		"id":         id,
		keyRequestID: id,
		"value":      val,
	}

	payload, err := artAppRequest(req)
	if err != nil {
		return fmt.Errorf("build set_brightness request: %w", err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
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

// getCategories retrieves the list of all artwork categories available on the TV.
func (c *Client) getCategories(ctx context.Context) (json.RawMessage, error) {
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

	raw, err := c.artConn.SendAndWait(ctx, payload, id, c.opts.APITimeout)
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

// turnOff powers off the TV by holding KEY_POWER for 3 seconds via
// a separate remote control WebSocket connection.
func (c *Client) turnOff(ctx context.Context) error {
	return c.turnOffTV(ctx, 8002)
}

// DeviceInfo returns the cached device info, or nil if not fetched.
func (c *Client) DeviceInfo() *DeviceInfo {
	return c.info
}

// tokenFilePath returns the path to the token file for this TV.
func (c *Client) tokenFilePath() string {
	safeIP := strings.ReplaceAll(c.IP, ".", "_")
	return filepath.Join(c.opts.TokenDir, fmt.Sprintf("tv_%s.txt", safeIP))
}

// saveMetadata fetches all available system information and artwork categories,
// saving them to a JSON file in the tokens directory for auditing.
func (c *Client) saveMetadata(ctx context.Context) error {
	metadata := make(map[string]any)
	metadata["timestamp"] = time.Now().Format(time.RFC3339)

	// 1. Basic Device Info.
	if c.info != nil {
		metadata["device"] = c.info
	}

	// 2. Slideshow Status.
	if ss, err := c.slideshowStatus(ctx); err == nil {
		metadata["slideshow"] = ss
	}

	// 3. All Categories.
	if cats, err := c.getCategories(ctx); err == nil {
		var raw json.RawMessage
		if err := json.Unmarshal(cats, &raw); err == nil {
			metadata["categories"] = raw
		}
	}

	// 4. Detailed Environment (reserved for future telemetry integration).
	metadata["platform"] = "Y2025"

	b, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	safeIP := strings.ReplaceAll(c.IP, ".", "_")
	path := filepath.Join(c.opts.TokenDir, fmt.Sprintf("tv_%s_metadata.json", safeIP))

	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write metadata file: %w", err)
	}

	c.logger.Info("metadata saved", "path", path)
	return nil
}

var macSeparators = regexp.MustCompile(`[^a-fA-F0-9]`)

func (c *Client) sendWOL(ctx context.Context, macAddr string) error {
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
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "udp", "255.255.255.255:9")
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

	client := &http.Client{Timeout: c.opts.GateTimeout}

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
	conn := newConnection(c.IP, port, "samsung.remote.control", c.opts.ClientName, tokenFile, c.opts.ConnectionTimeout, c.logger)

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
		Timeout: c.opts.APITimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // lgtm[go/insecure-tls]
			},
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
	conn := newConnection(c.IP, port, "samsung.remote.control", c.opts.ClientName, c.tokenFilePath(), c.opts.ConnectionTimeout, c.logger)

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
