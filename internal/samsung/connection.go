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

	"github.com/gorilla/websocket"
)

// connection manages a single WSS connection to a Samsung Frame TV endpoint.
type connection struct {
	host          string
	port          int
	endpoint      string
	name          string // client identity sent in WebSocket URL
	tokenFile     string
	timeout       time.Duration
	skipTLSVerify bool
	logger        *slog.Logger

	mu       sync.Mutex
	conn     *websocket.Conn
	closed   atomic.Bool
	recvDone chan struct{}

	// pending tracks outstanding art API requests by request ID.
	// When a response arrives, the raw JSON is sent to the channel.
	pendingMu sync.Mutex
	pending   map[string]chan json.RawMessage
}

// newConnection creates a new WebSocket connection manager.
//

func newConnection(
	host string, port int, endpoint, name, tokenFile string,
	timeout time.Duration, skipTLSVerify bool, logger *slog.Logger,
) *connection {
	return &connection{
		host:          host,
		port:          port,
		endpoint:      endpoint,
		name:          name,
		tokenFile:     tokenFile,
		timeout:       timeout,
		skipTLSVerify: skipTLSVerify,
		logger:        logger,
		pending:       make(map[string]chan json.RawMessage),
	}
}

// Open establishes the WSS connection, performs the handshake, and starts
// the background receive loop.
//
//nolint:funlen // Connection handshake sequence is inherently complex
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
		//nolint:gosec // Samsung TVs use self-signed certs for local WSS.
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: c.skipTLSVerify},
		HandshakeTimeout: c.timeout,
	}

	conn, httpResp, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	if httpResp != nil && httpResp.Body != nil {
		defer func() { _ = httpResp.Body.Close() }()
	}

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

		if readyResp.Event != EventChannelReady {
			_ = conn.Close()
			return fmt.Errorf("%w: expected ms.channel.ready, got %q", ErrConnectionFailure, readyResp.Event)
		}
	}

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

	if c.recvDone != nil {
		select {
		case <-c.recvDone:
		case <-time.After(500 * time.Millisecond):
			c.logger.Debug("recv loop did not exit quickly, continuing", "endpoint", c.endpoint)
		}
	}

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
	if err := os.WriteFile(c.tokenFile, []byte(d.Token), 0o600); err != nil {
		c.logger.Error("failed to save token", "error", err, "file", c.tokenFile)
	}
}
