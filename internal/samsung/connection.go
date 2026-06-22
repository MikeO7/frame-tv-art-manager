package samsung

import (
	"context"
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

	"github.com/coder/websocket"
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

	if err := c.Send(ctx, payload); err != nil {
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
func (c *connection) Send(ctx context.Context, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return ErrNotConnected
	}

	c.logger.Debug("WS SEND", "payload", string(payload))
	return c.conn.Write(ctx, websocket.MessageText, payload)
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
