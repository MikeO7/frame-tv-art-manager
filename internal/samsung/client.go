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
//   - ip:     The IPv4 address of the target Frame TV.
//   - opts:   Configuration options for the connection, including timeouts and client identity.
//   - logger: A structured logger for emitting connection state and errors.
//
// Returns:
//   - *Client: An instantiated client ready to connect.
//
// Example:
//
//	tv := samsung.NewClient("192.168.1.150", opts, logger)
//	if err := tv.Connect(ctx); err != nil {
//	    log.Fatal("Could not connect to TV:", err)
//	}
//	defer tv.Close()
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
	if c.opts.TVMAC != "" {
		c.logger.Info("sending Wake-on-LAN", "mac", c.opts.TVMAC)
		if err := c.sendWOL(ctx, c.opts.TVMAC); err != nil {
			c.logger.Warn("WoL failed", "error", err)
		} else {
			time.Sleep(2 * time.Second)
		}
	}

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

	tokenFile := c.tokenFilePath()
	if _, err := os.Stat(tokenFile); os.IsNotExist(err) {
		c.logger.Info("no token found, performing one-time remote handshake")
		if err := os.MkdirAll(filepath.Dir(tokenFile), 0o700); err != nil {
			return fmt.Errorf("create token dir: %w", err)
		}
		if err := c.ensureToken(ctx, tokenFile, 8002); err != nil {
			c.logger.Warn("remote handshake failed (TV might be off or busy)", "error", err)
		} else {
			time.Sleep(2 * time.Second)
		}
	}

	c.artConn = newConnection(
		c.IP, 8002, "com.samsung.art-app",
		c.opts.ClientName, tokenFile,
		c.opts.ConnectionTimeout, c.opts.SkipTLSVerify, c.logger,
	)

	if err := c.artConn.Open(ctx); err != nil {
		return fmt.Errorf("connect to art endpoint: %w", err)
	}

	info, err := c.fetchDeviceInfo(ctx, 8002)
	if err != nil {
		c.logger.Warn("could not fetch device info", "error", err)
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
	c.mu.Unlock() //nolint:staticcheck // intended unlock pattern
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

// DeviceInfo returns the cached device info, or nil if not fetched.
func (c *Client) DeviceInfo() *DeviceInfo {
	return c.info
}

// tokenFilePath returns the path to the token file for this TV.
func (c *Client) tokenFilePath() string {
	safeIP := strings.ReplaceAll(c.IP, ".", "_")
	return filepath.Join(c.opts.TokenDir, fmt.Sprintf("tv_%s.txt", safeIP))
}

func (c *Client) ensureToken(ctx context.Context, tokenFile string, port int) error {
	conn := newConnection(c.IP, port, "samsung.remote.control", c.opts.ClientName, tokenFile, c.opts.ConnectionTimeout, c.opts.SkipTLSVerify, c.logger)
	if err := conn.Open(ctx); err != nil {
		return err
	}
	return conn.Close()
}

func (c *Client) turnOff(ctx context.Context) error {
	return c.turnOffTV(ctx, 8002)
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

func (c *Client) turnOffTV(ctx context.Context, port int) error {
	conn := newConnection(
		c.IP, port, "samsung.remote.control",
		c.opts.ClientName, c.tokenFilePath(),
		c.opts.ConnectionTimeout, c.opts.SkipTLSVerify, c.logger,
	)

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
