package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
)

func TestConnection_Open_Handshake(t *testing.T) {
	// Create a mock WebSocket server with TLS.
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Step 1: Send ms.channel.connect.
		resp := wsResponse{
			Event: "ms.channel.connect",
			Data:  json.RawMessage(`{"token":"test-token-123"}`),
		}
		b, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, b)

		// Step 2: For art-app, send ms.channel.ready.
		if strings.Contains(r.URL.Path, "com.samsung.art-app") {
			respReady := wsResponse{
				Event: "ms.channel.ready",
				Data:  json.RawMessage(`{}`),
			}
			bReady, _ := json.Marshal(respReady)
			_ = conn.WriteMessage(websocket.TextMessage, bReady)
		}

		// Keep alive for a bit.
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	// Extract host and port from the test server URL (e.g. https://127.0.0.1:12345).
	u, _ := url.Parse(server.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	tokenDir := t.TempDir()
	tokenFile := tokenDir + "/token.txt"

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	conn := NewConnection(host, port, "com.samsung.art-app", "TestClient", tokenFile, 2*time.Second, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conn.Open(ctx); err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if !conn.IsAlive() {
		t.Error("expected connection to be alive")
	}

	// Verify token was saved.
	savedToken, _ := os.ReadFile(tokenFile) //nolint:gosec // ReadFile safe in test
	if string(savedToken) != "test-token-123" {
		t.Errorf("expected token 'test-token-123', got %q", string(savedToken))
	}
}

func TestConnection_Unauthorized(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		resp := wsResponse{
			Event: "ms.channel.unauthorized",
		}
		b, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, b)
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	conn := NewConnection(host, port, "test", "TestClient", t.TempDir()+"/token.txt", 1*time.Second, slog.Default())
	err := conn.Open(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}
