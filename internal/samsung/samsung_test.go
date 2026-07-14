package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestConnection_Open_Handshake(t *testing.T) {
	// Create a mock WebSocket server with TLS.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

		// Step 1: Send ms.channel.connect.
		resp := wsResponse{
			Event: EventChannelConnect,
			Data:  json.RawMessage(`{"token":"test-token-123"}`),
		}
		b, _ := json.Marshal(resp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)

		// Step 2: For art-app, send ms.channel.ready.
		if strings.Contains(r.URL.Path, "com.samsung.art-app") {
			respReady := wsResponse{
				Event: EventChannelReady,
				Data:  json.RawMessage(`{}`),
			}
			bReady, _ := json.Marshal(respReady)
			_ = conn.Write(r.Context(), websocket.MessageText, bReady)
		}

		// Keep alive for a bit.
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	// Extract host and port from the test server URL (e.g. https://127.0.0.1:12345).
	u, _ := url.Parse(server.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	tokenDir := secureTokenDirectory(t)
	tokenFile := tokenDir + "/token.txt"

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	conn := newConnection(connConfig{
		host:          host,
		port:          port,
		endpoint:      "com.samsung.art-app",
		name:          "TestClient",
		tokenFile:     tokenFile,
		timeout:       2 * time.Second,
		skipTLSVerify: true,
		logger:        logger,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conn.Open(ctx); err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = conn.CloseContext(context.Background()) }()

	if !conn.IsAlive() {
		t.Error("expected connection to be alive")
	}

	// Verify token was saved.
	savedToken, _ := os.ReadFile(tokenFile)
	if string(savedToken) != "test-token-123" {
		t.Errorf("expected token 'test-token-123', got %q", string(savedToken))
	}
}

func TestConnection_Unauthorized(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

		resp := wsResponse{
			Event: "ms.channel.unauthorized",
		}
		b, _ := json.Marshal(resp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	conn := newConnection(connConfig{
		host:          host,
		port:          port,
		endpoint:      "test",
		name:          "TestClient",
		tokenFile:     secureTokenDirectory(t) + "/token.txt",
		timeout:       1 * time.Second,
		skipTLSVerify: true,
		logger:        slog.Default(),
	})
	err := conn.Open(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}
