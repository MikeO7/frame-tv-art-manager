package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/coder/websocket"
)

// roundTripFunc is defined in client_test.go so we can just use it

func TestClient_CheckGate(t *testing.T) {
	c := NewClient("127.0.0.1", (&config.Config{}).TVConnectOptions(), slog.Default())

	// Default EnableRESTGate is false
	err := c.checkGate(context.Background())
	if err != nil {
		t.Errorf("expected no error when EnableRESTGate is false, got %v", err)
	}

	c.opts.EnableRESTGate = true
	// Not in art mode (default roundtripper will fail to connect)
	err = c.checkGate(context.Background())
	if !errors.Is(err, ErrGateFailed) {
		t.Errorf("expected ErrGateFailed, got %v", err)
	}

	// Mock HTTP transport to return 200 OK
	originalTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = originalTransport }()

	http.DefaultTransport = roundTripFunc(func(req *http.Request) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
		}
	})

	err = c.checkGate(context.Background())
	if err != nil {
		t.Errorf("expected no error when in art mode, got %v", err)
	}
}

func TestClient_SetupToken(t *testing.T) {
	c := NewClient("127.0.0.1", (&config.Config{
		ConnectionTimeout: 50 * time.Millisecond,
	}).TVConnectOptions(), slog.Default())

	tmpDir := t.TempDir()
	tokenFile := filepath.Join(tmpDir, "test_token.txt")

	// 1. File doesn't exist, mkdir fails
	err := c.setupToken(context.Background(), "/invalid/path/that/fails/token.txt")
	if err == nil {
		t.Errorf("expected error when mkdir fails")
	}

	// 2. File exists
	_ = os.WriteFile(tokenFile, []byte("token"), 0o600)
	err = c.setupToken(context.Background(), tokenFile)
	if err != nil {
		t.Errorf("expected no error when token file exists, got %v", err)
	}

	// 3. File doesn't exist, ensureToken fails but we just warn and proceed
	os.Remove(tokenFile)
	err = c.setupToken(context.Background(), tokenFile)
	if err != nil {
		t.Errorf("expected no error even if ensureToken fails, got %v", err)
	}
}

func TestClient_Connect(t *testing.T) {
	wsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

		writeJSON := func(ctx context.Context, v any) error {
			b, err := json.Marshal(v)
			if err != nil {
				return err
			}
			return conn.Write(ctx, websocket.MessageText, b)
		}

		resp := wsResponse{Event: EventChannelConnect, Data: json.RawMessage(`{"token":"saved-token"}`)}
		_ = writeJSON(r.Context(), resp)

		respReady := wsResponse{Event: EventChannelReady, Data: json.RawMessage(`{}`)}
		_ = writeJSON(r.Context(), respReady)

		time.Sleep(50 * time.Millisecond)
	}))
	defer wsServer.Close()

	u, _ := url.Parse(wsServer.URL)
	host := u.Hostname()
	_, _ = strconv.Atoi(u.Port())

	// We need to override the port used by Connect, but it's hardcoded to 8002.
	// Since we can't change the port without changing the code, we'll test error paths
	// or mock network if needed, but since we cannot easily change portArtWSS which is a const,
	// maybe we just test the error paths of Connect.

	c := NewClient(host, (&config.Config{
		TokenDir:          t.TempDir(),
		ConnectionTimeout: 10 * time.Millisecond,
		EnableRESTGate:    true,
	}).TVConnectOptions(), slog.Default())

	// checkGate will fail
	err := c.Connect(context.Background())
	if !errors.Is(err, ErrGateFailed) {
		t.Errorf("expected ErrGateFailed, got %v", err)
	}

	c.opts.EnableRESTGate = false
	// checkGate skipped, token fails / connect fails
	err = c.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "connect to art endpoint") {
		t.Errorf("expected connect error, got %v", err)
	}
}
