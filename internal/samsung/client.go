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
	keyRequest          = "request"
	keyRequestID        = "request_id"
	keyGetArtModeStatus = "get_artmode_status"
	keyGetContentList   = "get_content_list"
	keyContentID        = "content_id"
)

// WebSocket endpoint names exposed by Samsung Frame TVs.
const (
	endpointArtApp        = "com.samsung.art-app"
	endpointRemoteControl = "samsung.remote.control"
)

// Samsung Frame TV network ports and protocol limits.
const (
	portArtWSS         = 8002 // WSS art-app + remote-control endpoint
	portRESTGate       = 8001 // REST art-mode gate probe
	wolBroadcastPort   = 9    // Wake-on-LAN discard port
	maxBackoffDelay    = 1 * time.Hour
	maxDeviceInfoBytes = 1 << 20 // 1 MiB cap on the device-info response

	// powerKeyHold is how long KEY_POWER is held between the press and release
	// events to trigger a power toggle on the TV's remote-control endpoint.
	powerKeyHold = 3 * time.Second
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

	// powerHold is the KEY_POWER press-to-release duration. It defaults to
	// powerKeyHold and is overridable in tests to avoid real-time waits.
	powerHold time.Duration

	// Persistent backoff state
	mu           sync.Mutex
	failures     int
	lastFailure  time.Time
	backoffUntil time.Time
}

// NewClient creates a Samsung TV client for the given IP and options. It only
// initializes client state; call Connect to open the WebSocket connection.
func NewClient(ip string, opts config.TVConnectOptions, logger *slog.Logger) *Client {
	return &Client{
		IP:        ip,
		opts:      opts,
		logger:    logger.With("tv", ip),
		powerHold: powerKeyHold,
	}
}

func (c *Client) checkGate(ctx context.Context) error {
	if !c.opts.EnableRESTGate {
		return nil
	}
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
	return nil
}

func (c *Client) setupToken(ctx context.Context, tokenFile string) error {
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		return nil
	}
	c.logger.Info("no token found, performing one-time remote handshake")
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0o700); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	if err := c.ensureToken(ctx, tokenFile, portArtWSS); err != nil {
		c.logger.Warn("remote handshake failed (TV might be off or busy)", "error", err)
	} else {
		time.Sleep(2 * time.Second)
	}
	return nil
}

// Connect establishes a connection to the TV with the following sequence:
//  1. Wake-on-LAN (if MAC configured)
//  2. Silent REST Gate (if enabled) -> abort if TV is not in art mode
//  3. Open WSS connection to art endpoint on port 8002
//  4. Fetch device info via REST API
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the connection.
//
// Returns:
//   - error: Any network or authentication error encountered during handshake.
func (c *Client) Connect(ctx context.Context) error {
	c.wakeTV(ctx)

	if err := c.checkGate(ctx); err != nil {
		return err
	}

	tokenFile := c.tokenFilePath()
	if err := c.setupToken(ctx, tokenFile); err != nil {
		return err
	}

	c.artConn = newConnection(connConfig{
		host:          c.IP,
		port:          portArtWSS,
		endpoint:      endpointArtApp,
		name:          c.opts.ClientName,
		tokenFile:     tokenFile,
		timeout:       c.opts.ConnectionTimeout,
		skipTLSVerify: c.opts.SkipTLSVerify,
		logger:        c.logger,
	})

	if err := c.artConn.Open(ctx); err != nil {
		return fmt.Errorf("connect to art endpoint: %w", err)
	}

	info, err := c.fetchDeviceInfo(ctx, portArtWSS)
	if err != nil {
		c.logger.Warn("could not fetch device info", "error", err)
	} else {
		c.info = info
		c.logger.Info(
			"connected",
			"model", info.ModelName,
			"firmware", info.FirmwareVersion,
			"frameTVSupport", info.FrameTVSupport,
		)
	}

	return nil
}

// Close shuts down the WebSocket connection.
//
// Returns:
//   - error: Any network error encountered during disconnection.
func (c *Client) Close() error {
	if c.artConn != nil {
		return c.artConn.Close()
	}
	return nil
}

func checkArtError(resp *artResponse) error {
	if resp.ErrorCode != 0 {
		// 11001 is sometimes returned by Samsung's art app for out-of-storage.
		if resp.ErrorCode == 403 || resp.ErrorCode == 507 || resp.ErrorCode == 11001 {
			return fmt.Errorf("%w: code %d", ErrStorageFull, resp.ErrorCode)
		}
		return fmt.Errorf("%w: code %d", ErrArtAPIError, resp.ErrorCode)
	}
	return nil
}

// Model returns the connected TV model name, if known.
//
// Returns:
//   - string: The model string (e.g., "QN65LS03AAFXZA"), or an empty string if unknown.
func (c *Client) Model() string {
	if c.info != nil {
		return c.info.ModelName
	}
	return ""
}

// DeviceInfo returns the cached device info, or nil if not fetched.
//
// Returns:
//   - *DeviceInfo: The system capabilities and hardware identifiers of the TV.
func (c *Client) DeviceInfo() *DeviceInfo {
	return c.info
}

// tokenFilePath returns the path to the token file for this TV.
func (c *Client) tokenFilePath() string {
	safeIP := strings.ReplaceAll(c.IP, ".", "_")
	return filepath.Join(c.opts.TokenDir, fmt.Sprintf("tv_%s.txt", safeIP))
}

// remoteControlConfig builds the connConfig for the TV's remote-control
// endpoint at the given port, using the supplied token file.
func (c *Client) remoteControlConfig(port int, tokenFile string) connConfig {
	return connConfig{
		host:          c.IP,
		port:          port,
		endpoint:      endpointRemoteControl,
		name:          c.opts.ClientName,
		tokenFile:     tokenFile,
		timeout:       c.opts.ConnectionTimeout,
		skipTLSVerify: c.opts.SkipTLSVerify,
		logger:        c.logger,
	}
}

func (c *Client) ensureToken(ctx context.Context, tokenFile string, port int) error {
	conn := newConnection(c.remoteControlConfig(port, tokenFile))
	if err := conn.Open(ctx); err != nil {
		return err
	}
	return conn.Close()
}

func (c *Client) fetchDeviceInfo(ctx context.Context, port int) (*DeviceInfo, error) {
	url := fmt.Sprintf("https://%s:%d/api/v2/", c.IP, port)

	client := &http.Client{
		Timeout: c.opts.APITimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: c.opts.SkipTLSVerify, //nolint:gosec // configurable TLS verification
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

	// Prevent DoS / resource exhaustion by enforcing a maximum read size.
	reader := http.MaxBytesReader(nil, resp.Body, maxDeviceInfoBytes)

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

func (c *Client) checkArtModeGate(ctx context.Context) (bool, error) {
	url := fmt.Sprintf("http://%s:%d/ms/art", c.IP, portRESTGate)

	client := &http.Client{Timeout: c.opts.GateTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, nil
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == http.StatusOK, nil
}

// imageAddedTimeout bounds how long Upload waits for the TV to confirm an
// uploaded image via the "image_added" event after the D2D transfer completes.
const imageAddedTimeout = 30 * time.Second

// IsInArtMode reports whether the TV is currently in art mode by querying
// the art API over the active WebSocket connection. On query failure it
// returns true, treating the TV as safe to sync.
func (c *Client) IsInArtMode(ctx context.Context) bool {
	if c.info != nil && !c.info.IsOn() {
		c.logger.Debug("TV is powered off")
		return false
	}

	id := newRequestID()
	req := map[string]any{
		keyRequest:   keyGetArtModeStatus,
		"id":         id,
		keyRequestID: id,
	}

	resp, _, err := c.sendArtRequest(ctx, req)
	if err != nil {
		c.logger.Debug("could not determine art mode, assuming safe to sync", "error", err)
		return true
	}

	isArt := resp.Value == "on"
	c.logger.Debug("art mode status", "value", resp.Value, "isArtMode", isArt)
	return isArt
}

// ListUploaded returns user-uploaded artwork on the TV (category MY-C0002 = "My Photos").
func (c *Client) ListUploaded(ctx context.Context) ([]ArtContent, error) {
	id := newRequestID()
	req := map[string]any{
		keyRequest:    keyGetContentList,
		"id":          id,
		keyRequestID:  id,
		"category_id": "MY-C0002",
	}

	resp, _, err := c.sendArtRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	contentListStr := resp.ContentList()
	if contentListStr == "" {
		return nil, nil
	}

	var items []ArtContent
	if err := json.Unmarshal([]byte(contentListStr), &items); err != nil {
		return nil, fmt.Errorf("parse content_list: %w", err)
	}

	filtered := make([]ArtContent, 0, len(items))
	for _, item := range items {
		if item.CategoryID == "MY-C0002" {
			filtered = append(filtered, item)
		}
	}

	return filtered, nil
}

// DeleteImages removes artwork from the TV by content IDs.
func (c *Client) DeleteImages(ctx context.Context, ids []string) error {
	id := newRequestID()

	contentIDList := make([]map[string]string, len(ids))
	for i, cid := range ids {
		contentIDList[i] = map[string]string{keyContentID: cid}
	}

	req := map[string]any{
		keyRequest:        "delete_image_list",
		"id":              id,
		keyRequestID:      id,
		"content_id_list": contentIDList,
	}

	_, _, err := c.sendArtRequest(ctx, req)
	return err
}

// SelectImage sets the currently displayed artwork by content ID.
func (c *Client) SelectImage(ctx context.Context, id string) error {
	reqID := newRequestID()

	req := map[string]any{
		keyRequest:   "select_image",
		"id":         reqID,
		keyRequestID: reqID,
		keyContentID: id,
		"show":       true,
	}

	_, _, err := c.sendArtRequest(ctx, req)
	return err
}

// getCategories retrieves the raw JSON list of artwork categories from the TV.
func (c *Client) getCategories(ctx context.Context) (json.RawMessage, error) {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "get_categories",
		"id":         id,
		keyRequestID: id,
	}

	_, raw, err := c.sendArtRequest(ctx, req)
	return raw, err
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

// Upload sends an image to the TV via the art API and D2D socket transfer with
// the given matte style, returning the TV-assigned content ID. An empty matte
// falls back to the configured default.
func (c *Client) Upload(ctx context.Context, filePath, fileType, matte string) (string, error) {
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
	resp, raw, err := c.sendArtRequest(ctx, buildSendImageRequest(id, fileType, matte, stat.Size()))
	if err != nil {
		return "", err
	}

	c.logger.Debug("send_image raw response", "raw", string(raw))

	connInfoStr := resp.ConnInfo()
	if connInfoStr == "" {
		return "", fmt.Errorf("send_image: no conn_info in response")
	}

	c.logger.Debug("send_image conn_info string", "conn_info", connInfoStr)
	var ci connInfo
	if err := json.Unmarshal([]byte(connInfoStr), &ci); err != nil {
		return "", fmt.Errorf("parse conn_info: %w", err)
	}
	c.logger.Debug("send_image parsed conn_info", "ip", ci.IP, "port", ci.Port)

	// Step 2: Transfer the file over D2D socket.
	if err := uploadImageD2D(ctx, d2dUpload{
		info:          ci,
		filePath:      filePath,
		fileType:      fileType,
		timeout:       c.opts.ConnectionTimeout,
		skipTLSVerify: c.opts.SkipTLSVerify,
	}); err != nil {
		return "", fmt.Errorf("d2d transfer: %w", err)
	}

	// Step 3: Wait for the TV to confirm the image was added.
	contentID, err := waitForAdded(ctx, imageAddedTimeout)
	if err != nil {
		return "", fmt.Errorf("wait for confirmation: %w", err)
	}

	return contentID, nil
}

// buildSendImageRequest constructs the art-API "send_image" payload that
// announces a pending D2D socket transfer of the given size and matte.
func buildSendImageRequest(id, fileType, matte string, fileSize int64) map[string]any {
	// connectionIDModulus bounds the generated connection_id to 4 GiB, the
	// range the TV's D2D handshake expects.
	const connectionIDModulus = 4 * 1024 * 1024 * 1024

	return map[string]any{
		keyRequest:   "send_image",
		"file_type":  fileType,
		"id":         id,
		keyRequestID: id,
		"conn_info": map[string]any{
			"d2d_mode":      "socket",
			"connection_id": time.Now().UnixNano() % connectionIDModulus,
			"id":            id,
		},
		"image_date":        time.Now().Format("2006:01:02 15:04:05"),
		"matte_id":          matte,
		"portrait_matte_id": matte,
		"file_size":         fileSize,
	}
}

// sendArtRequest wraps req in the art-app envelope, sends it, and waits for the
// matching response, returning the parsed artResponse and the raw JSON payload.
func (c *Client) sendArtRequest(ctx context.Context, req map[string]any) (*artResponse, json.RawMessage, error) {
	name := fmt.Sprint(req[keyRequest])
	reqID := fmt.Sprint(req[keyRequestID])

	payload, err := artAppRequest(req)
	if err != nil {
		return nil, nil, fmt.Errorf("build %s request: %w", name, err)
	}

	raw, err := c.artConn.SendAndWait(ctx, payload, reqID, c.opts.APITimeout)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", name, err)
	}

	var resp artResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, nil, fmt.Errorf("parse %s response: %w", name, err)
	}

	if err := checkArtError(&resp); err != nil {
		return nil, nil, fmt.Errorf("%s error: %w", name, err)
	}

	return &resp, raw, nil
}

// SaveMetadata fetches device info, slideshow status, and artwork categories,
// writing them to a per-TV JSON file in the tokens directory for auditing.
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

// SlideshowStatus returns the TV's current slideshow configuration.
func (c *Client) SlideshowStatus(ctx context.Context) (*SlideshowStatus, error) {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "get_slideshow_status",
		"id":         id,
		keyRequestID: id,
	}

	_, raw, err := c.sendArtRequest(ctx, req)
	if err != nil {
		return nil, err
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

// SetSlideshow updates the TV's slideshow configuration.
func (c *Client) SetSlideshow(ctx context.Context, s SlideshowStatus) error {
	id := newRequestID()

	req := map[string]any{
		keyRequest:    "set_slideshow_status",
		"id":          id,
		keyRequestID:  id,
		"value":       s.Value,
		"category_id": s.CategoryID,
		"type":        s.Type,
	}

	_, _, err := c.sendArtRequest(ctx, req)
	return err
}

// SetBrightness sets the art-mode brightness value.
func (c *Client) SetBrightness(ctx context.Context, val int) error {
	id := newRequestID()

	req := map[string]any{
		keyRequest:   "set_brightness",
		"id":         id,
		keyRequestID: id,
		"value":      val,
	}

	_, _, err := c.sendArtRequest(ctx, req)
	return err
}

// ShouldSkip returns true if the TV is in a backoff window due to failures.
//
// Returns:
//   - bool: True if the client is still waiting out a failure timeout period.
func (c *Client) ShouldSkip() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Now().Before(c.backoffUntil) {
		remaining := time.Until(c.backoffUntil).Round(time.Second)
		c.logger.Info(
			"TV in backoff period, skipping",
			"failures", c.failures,
			"retry_in", remaining.String(),
		)
		return true
	}
	return false
}

// RecordFailure tracks a connection failure and calculates exponential backoff.
//
// Parameters:
//   - baseInterval: The initial time duration to wait before the next retry.
func (c *Client) RecordFailure(baseInterval time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failures++
	c.lastFailure = time.Now()

	// Exponential backoff: baseInterval doubled per consecutive failure, capped.
	delay := baseInterval
	for i := 1; i < c.failures && delay < maxBackoffDelay; i++ {
		delay *= 2
	}
	if delay > maxBackoffDelay {
		delay = maxBackoffDelay
	}

	c.backoffUntil = c.lastFailure.Add(delay)

	c.logger.Warn(
		"TV unreachable, backing off",
		"consecutive_failures", c.failures,
		"next_retry", c.backoffUntil.Format(time.Kitchen),
		"backoff_duration", delay.Round(time.Second).String(),
	)
}

// RecordSuccess resets failure count.
//
// Calling this method clears the backoff window completely.
func (c *Client) RecordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.failures > 0 {
		c.logger.Info(
			"TV recovered after failures",
			"previous_failures", c.failures,
		)
	}
	c.failures = 0
	c.backoffUntil = time.Time{}
}

// wakeTV sends a Wake-on-LAN magic packet to the TV to wake it up if it's sleeping.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
func (c *Client) wakeTV(ctx context.Context) {
	if c.opts.TVMAC == "" {
		return
	}
	c.logger.Info("sending Wake-on-LAN", "mac", c.opts.TVMAC)
	if err := c.sendWOL(ctx, c.opts.TVMAC); err != nil {
		c.logger.Warn("WoL failed", "error", err)
	} else {
		time.Sleep(2 * time.Second)
	}
}

var macSeparators = regexp.MustCompile(`[^a-fA-F0-9]`)

// sendWOL broadcasts a Wake-on-LAN magic packet to wake up the TV on the local network.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the network request.
//   - macAddr: The MAC address string (e.g., "AA:BB:CC:DD:EE:FF") of the TV.
//
// Returns:
//   - error: Any formatting error or network failure encountered while broadcasting.
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
	conn, err := dialer.DialContext(ctx, "udp", fmt.Sprintf("255.255.255.255:%d", wolBroadcastPort))
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

// TurnOff powers off the TV via the remote control API.
func (c *Client) TurnOff(ctx context.Context) error {
	return c.turnOffTV(ctx, portArtWSS)
}

func (c *Client) turnOffTV(ctx context.Context, port int) error {
	conn := newConnection(c.remoteControlConfig(port, c.tokenFilePath()))

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

	if err := conn.Send(ctx, pressPayload); err != nil {
		return fmt.Errorf("send press: %w", err)
	}

	// Hold KEY_POWER before releasing to trigger the power toggle.
	hold := c.powerHold
	if hold <= 0 {
		hold = powerKeyHold
	}
	select {
	case <-time.After(hold):
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

	if err := conn.Send(ctx, releasePayload); err != nil {
		return fmt.Errorf("send release: %w", err)
	}

	return nil
}
