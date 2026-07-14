package samsung

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// recvLoopShutdownGrace bounds how long Close waits for the background reader
// to exit before proceeding, so shutdown can't hang on a stuck socket read.
const recvLoopShutdownGrace = 500 * time.Millisecond

// connConfig groups the parameters required to construct a connection.
type connConfig struct {
	host          string
	port          int
	endpoint      string
	name          string
	tokenFile     string
	timeout       time.Duration
	skipTLSVerify bool
	logger        *slog.Logger
	persistToken  func(context.Context, string, string) error
	httpClient    *http.Client
}

// newConnection creates a new WebSocket connection manager.
func newConnection(cfg connConfig) *connection {
	persistToken := cfg.persistToken
	if persistToken == nil {
		persistToken = persistAuthenticationToken
	}
	return &connection{
		host:          cfg.host,
		port:          cfg.port,
		endpoint:      cfg.endpoint,
		name:          cfg.name,
		tokenFile:     cfg.tokenFile,
		timeout:       cfg.timeout,
		skipTLSVerify: cfg.skipTLSVerify,
		logger:        cfg.logger,
		persistToken:  persistToken,
		httpClient:    cfg.httpClient,
		pending:       make(map[string]chan json.RawMessage),
	}
}

// maxMessageSize is the maximum size in bytes of a message read from the TV WebSocket.
// The default in coder/websocket is 32 KB, which is too small for large content lists.
const maxMessageSize = 16 * 1024 * 1024 // 16 MB

func (c *connection) dial(ctx context.Context, wsURL string) (*websocket.Conn, error) {
	client := c.httpClient
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				//nolint:gosec // Samsung TVs use self-signed certs for local WSS.
				TLSClientConfig: &tls.Config{InsecureSkipVerify: c.skipTLSVerify},
			},
		}
		defer client.CloseIdleConnections()
	}

	dialCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	conn, httpResp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPClient: client,
	})
	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}
	if httpResp != nil && httpResp.Body != nil {
		_ = httpResp.Body.Close()
	}
	conn.SetReadLimit(maxMessageSize)
	return conn, nil
}

func (c *connection) readHandshake(ctx context.Context, conn *websocket.Conn) error {
	readCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	_, msg, err := conn.Read(readCtx)
	if err != nil {
		return fmt.Errorf("read handshake: %w", err)
	}

	var resp wsResponse
	if err := json.Unmarshal(msg, &resp); err != nil {
		return fmt.Errorf("parse handshake: %w", err)
	}

	switch resp.Event {
	case EventChannelConnect:
		if err := c.extractAndSaveToken(ctx, resp.Data); err != nil {
			return err
		}
	case "ms.channel.unauthorized":
		return ErrUnauthorized
	case "ms.channel.timeOut":
		return ErrTimeout
	default:
		return fmt.Errorf("unexpected event %q: %w", resp.Event, ErrConnectionFailure)
	}
	return nil
}

func (c *connection) waitForChannelReady(ctx context.Context, conn *websocket.Conn) error {
	readCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	_, msg, err := conn.Read(readCtx)
	if err != nil {
		return fmt.Errorf("read channel ready: %w", err)
	}

	var readyResp wsResponse
	if err := json.Unmarshal(msg, &readyResp); err != nil {
		return fmt.Errorf("parse channel ready: %w", err)
	}

	if readyResp.Event != EventChannelReady {
		return fmt.Errorf("expected ms.channel.ready, got %q: %w", readyResp.Event, ErrConnectionFailure)
	}
	return nil
}

func (c *connection) Open(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil // already connected
	}

	token, err := c.readToken()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("load authentication token: %w", err)
	}
	wsURL := c.formatURL(token)
	c.logger.Debug("dialing WebSocket", "endpoint", c.endpoint, "port", c.port)

	conn, err := c.dial(ctx, wsURL)
	if err != nil {
		return err
	}

	if err := c.readHandshake(ctx, conn); err != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return err
	}

	if c.endpoint == endpointArtApp {
		if err := c.waitForChannelReady(ctx, conn); err != nil {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return err
		}
	}

	c.conn = conn
	c.closed.Store(false)
	c.recvDone = make(chan struct{})
	//nolint:contextcheck,gosec // background reader goroutine intentionally uses its own long-lived context, not Open's transient handshake context
	go c.recvLoop()

	c.logger.Info("WebSocket connected", "endpoint", c.endpoint)
	return nil
}

// CloseContext shuts down the socket and bounds the background-reader join by
// both the caller's deadline and the protocol shutdown grace period.
func (c *connection) CloseContext(ctx context.Context) error {
	c.mu.Lock()
	if c.conn == nil {
		c.mu.Unlock()
		return nil
	}
	c.closed.Store(true)
	conn := c.conn
	recvDone := c.recvDone
	c.conn = nil
	c.mu.Unlock()

	err := conn.Close(websocket.StatusNormalClosure, "")

	if recvDone != nil {
		timer := time.NewTimer(recvLoopShutdownGrace)
		defer timer.Stop()
		select {
		case <-recvDone:
		case <-timer.C:
			c.logger.Debug("recv loop did not exit quickly, continuing", "endpoint", c.endpoint)
		case <-ctx.Done():
			err = errors.Join(err, ctx.Err())
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
