package samsung

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/gorilla/websocket"
)

func TestClient_New(t *testing.T) {
	cfg := &config.Config{TokenDir: "/tmp"}
	c := NewClient("192.168.1.10", cfg, slog.Default())
	if c.IP != "192.168.1.10" {
		t.Errorf("expected IP 192.168.1.10, got %s", c.IP)
	}
}

func TestClient_TokenFilePath(t *testing.T) {
	cfg := &config.Config{TokenDir: "/data/tokens"}
	c := NewClient("1.2.3.4", cfg, slog.Default())
	path := c.tokenFilePath()

	expected := filepath.Join("/data/tokens", "tv_1_2_3_4.txt")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestDeviceInfo_IsFrameTV(t *testing.T) {
	d := &DeviceInfo{FrameTVSupport: stringTrue}
	if !d.IsFrameTV() {
		t.Error("expected true")
	}
	d.FrameTVSupport = "false"
	if d.IsFrameTV() {
		t.Error("expected false")
	}
}

func TestDeviceInfo_IsOn(t *testing.T) {
	d := &DeviceInfo{PowerState: "on"}
	if !d.IsOn() {
		t.Error("expected true")
	}
	d.PowerState = "off"
	if d.IsOn() {
		t.Error("expected false")
	}
}

func TestArtResponse_ConnInfoParsing(t *testing.T) {
	// Test the parsing logic we saw in ArtAPI.SendImage
	rawJSON := `{"ip":"127.0.0.1","port":12345}`
	var ci ConnInfo
	if err := json.Unmarshal([]byte(rawJSON), &ci); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if ci.IP != "127.0.0.1" || ci.Port.String() != "12345" {
		t.Errorf("incorrect parsing: %+v", ci)
	}
}

func TestEnsureToken(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		resp := wsResponse{Event: EventChannelConnect, Data: json.RawMessage(`{"token":"new-token"}`)}
		b, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, b)
	}))
	defer server.Close()

	u, _ := neturl.Parse(server.URL)
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	tokenFile := filepath.Join(t.TempDir(), "token.txt")

	err := EnsureToken(context.Background(), host, port, "Test", tokenFile, 1*time.Second, slog.Default())
	if err != nil {
		t.Fatalf("EnsureToken failed: %v", err)
	}

	data, _ := os.ReadFile(filepath.Clean(tokenFile))
	if string(data) != "new-token" {
		t.Errorf("expected new-token, got %s", string(data))
	}
}
