package samsung

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
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
	persistToken  func(context.Context, string, string) error
	httpClient    *http.Client

	mu       sync.Mutex
	conn     *websocket.Conn
	closed   atomic.Bool
	recvDone chan struct{}

	// pending tracks outstanding art API requests by request ID.
	// When a response arrives, the raw JSON is sent to the channel.
	pendingMu sync.Mutex
	pending   map[string]chan json.RawMessage
}

func (c *connection) registerPending(key string) (<-chan json.RawMessage, func(), error) {
	ch := make(chan json.RawMessage, 1)
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if _, exists := c.pending[key]; exists {
		return nil, nil, fmt.Errorf("listener %q is already registered", key)
	}
	if c.pending == nil {
		c.pending = make(map[string]chan json.RawMessage)
	}
	c.pending[key] = ch
	var once sync.Once
	cleanup := func() {
		once.Do(func() { c.unregisterPending(key, ch) })
	}
	return ch, cleanup, nil
}

func (c *connection) unregisterPending(key string, registered chan json.RawMessage) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if current := c.pending[key]; current == registered {
		delete(c.pending, key)
	}
}

// SendAndWait sends a JSON payload and waits for a response matching the
// given request ID. Returns the raw d2d event data JSON.
func (c *connection) SendAndWait(ctx context.Context, payload []byte, requestID string, timeout time.Duration) (json.RawMessage, error) {
	ch, cleanup, err := c.registerPending(requestID)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if err := c.Send(ctx, payload); err != nil {
		return nil, err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case data, ok := <-ch:
		if !ok {
			return nil, ErrNotConnected
		}
		return data, nil
	case <-timer.C:
		return nil, fmt.Errorf("%w: waiting for response %s", ErrTimeout, requestID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Send writes a JSON text message to the WebSocket.
func (c *connection) Send(ctx context.Context, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return ErrNotConnected
	}

	c.logger.Debug("sending WebSocket message", "bytes", len(payload))
	return c.conn.Write(ctx, websocket.MessageText, payload)
}

// formatURL builds the WebSocket URL for the specified endpoint.
func (c *connection) formatURL(token string) string {
	b64Name := base64.StdEncoding.EncodeToString([]byte(c.name))
	u := url.URL{
		Scheme: "wss",
		Host:   net.JoinHostPort(c.host, fmt.Sprint(c.port)),
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
func (c *connection) readToken() (string, error) {
	return loadAuthenticationToken(c.tokenFile)
}

// extractAndSaveToken pulls the token from a ms.channel.connect response and
// publishes it durably with sensitive permissions.
func (c *connection) extractAndSaveToken(ctx context.Context, data json.RawMessage) error {
	var d struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		c.logger.Debug("failed to parse token from handshake", "error", err)
		return nil
	}
	if d.Token == "" {
		c.logger.Debug("token field is empty in handshake data")
		return nil
	}
	if c.persistToken == nil {
		return errors.New("token persistence is not configured")
	}
	if err := c.persistToken(ctx, c.tokenFile, d.Token); err != nil {
		return fmt.Errorf("save authentication token: %w", err)
	}
	c.logger.Info("new authentication token saved")
	return nil
}
