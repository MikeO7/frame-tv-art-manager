package samsung

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRemote_EnsureToken(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, _ := upgrader.Upgrade(w, r, nil)
		defer func() { _ = conn.Close() }()

		handshakeResp := map[string]any{
			keyEvent: EventChannelConnect,
			"data": map[string]any{
				"token": "remote-token",
			},
		}
		_ = conn.WriteJSON(handshakeResp)
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	tokenFile := filepath.Join(t.TempDir(), "remote-token.txt")

	err := EnsureToken(context.Background(), u.Hostname(), port, "TestClient", tokenFile, 1*time.Second, slog.Default())
	if err != nil {
		t.Fatalf("EnsureToken failed: %v", err)
	}
}
