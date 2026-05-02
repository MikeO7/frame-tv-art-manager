package samsung

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Connection manages a single WSS connection to a Samsung Frame TV endpoint.
//
// The TV uses two WebSocket endpoints:
//   - "com.samsung.art-app"       — for art management (upload, list, select, etc.)
//   - "samsung.remote.control"    — for remote key commands (power off)
//
// Each endpoint requires its own Connection instance, but they share the
// same token file for authentication.
type Connection struct {
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

// NewConnection creates a new WebSocket connection manager. It does not
// connect automatically — call Open() to establish the connection.
func NewConnection(host string, port int, endpoint, name, tokenFile string, timeout time.Duration, logger *slog.Logger) *Connection {
	return &Connection{
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
func (c *Connection) Open(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil // already connected
	}

	token := c.readToken()
	wsURL := c.formatURL(token)
	c.logger.Debug("dialing WebSocket", "url", wsURL)

	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // Required: Samsung TVs use self-signed certs for local WSS; verification would prevent connection.
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
	case "ms.channel.connect":
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
func (c *Connection) Close() error {
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
func (c *Connection) IsAlive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && !c.closed.Load()
}

// SendAndWait sends a JSON payload and waits for a response matching the
// given request ID. Returns the raw d2d event data JSON.
func (c *Connection) SendAndWait(ctx context.Context, payload []byte, requestID string, timeout time.Duration) (json.RawMessage, error) {
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
func (c *Connection) SendAndWaitEvent(ctx context.Context, payload []byte, eventName string, timeout time.Duration) (json.RawMessage, error) {
	return c.SendAndWait(ctx, payload, eventName, timeout)
}

// Send writes a JSON text message to the WebSocket.
func (c *Connection) Send(payload []byte) error {
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
func (c *Connection) recvLoop() {
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
		if resp.Event == "d2d.service.message.event" || resp.Event == "d2d_service_message" {
			c.routeD2DEvent(resp.Data)
		}
	}
}

func (c *Connection) routeD2DEvent(dataRaw json.RawMessage) {
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
func (c *Connection) formatURL(token string) string {
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
func (c *Connection) readToken() string {
	data, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// extractAndSaveToken pulls the token from a ms.channel.connect response
// and writes it to the token file.
func (c *Connection) extractAndSaveToken(data json.RawMessage) {
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
	if err := os.WriteFile(c.tokenFile, []byte(d.Token), 0600); err != nil {
		c.logger.Error("failed to save token", "error", err, "file", c.tokenFile)
	}
}

// wsResponse is the top-level WebSocket message envelope from the TV.
type wsResponse struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// ArtAppRequest builds the outer WebSocket message for an art API request.
func ArtAppRequest(data map[string]any) ([]byte, error) {
	inner, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	outer := map[string]any{
		"method": "ms.channel.emit",
		"params": map[string]any{
			"event": "art_app_request",
			"to":    "host",
			"data":  string(inner),
		},
	}
	return json.Marshal(outer)
}

// NewRequestID generates a new UUID string for art API request correlation.
func NewRequestID() string {
	return uuid.New().String()
}
