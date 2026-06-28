package samsung

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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

// checkGate probes the TV's REST gate endpoint (if enabled) to determine if it is
// in art mode and safe to sync without prompting the user.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the probe.
//
// Returns:
//   - error: ErrGateFailed if the TV is not in art mode, or nil if safe.
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

// setupToken checks for an existing token file and performs a one-time remote
// handshake to obtain a new token if it does not exist.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the handshake.
//   - tokenFile: The filesystem path where the token is or should be stored.
//
// Returns:
//   - error: Any network or filesystem error encountered during setup.
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

// checkArtError inspects the parsed art API response for error codes and
// maps them to typed application errors.
//
// Parameters:
//   - resp: The unmarshaled response from the Samsung art API.
//
// Returns:
//   - error: ErrStorageFull, ErrArtAPIError, or nil if successful.
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

// ensureToken attempts a brief connection to the remote control endpoint to trigger
// the TV's authorization prompt and save the resulting token.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the connection.
//   - tokenFile: The path where the new authentication token will be saved.
//   - port: The integer network port for the remote control endpoint.
//
// Returns:
//   - error: Any network error encountered during the connection attempt.
func (c *Client) ensureToken(ctx context.Context, tokenFile string, port int) error {
	conn := newConnection(c.remoteControlConfig(port, tokenFile))
	if err := conn.Open(ctx); err != nil {
		return err
	}
	return conn.Close()
}

// fetchDeviceInfo retrieves the TV's hardware and firmware information from the REST API.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//   - port: The integer network port to query for device information.
//
// Returns:
//   - *DeviceInfo: The parsed device capabilities and system properties.
//   - error: Any network or serialization error encountered during retrieval.
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

// checkArtModeGate executes an HTTP GET request against the TV's art mode gate
// to verify if it is currently displaying artwork.
//
// Parameters:
//   - ctx: Context to control the timeout and cancellation of the request.
//
// Returns:
//   - bool: True if the TV is in art mode (returns HTTP 200), false otherwise.
//   - error: Always nil; network errors are treated as false for safety.
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
