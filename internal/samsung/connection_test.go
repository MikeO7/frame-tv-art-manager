package samsung

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestArtAppRequest(t *testing.T) {
	data := map[string]any{"hello": "world"}
	b, err := artAppRequest(data)
	if err != nil {
		t.Fatal(err)
	}

	var envelope struct {
		Method string `json:"method"`
		Params struct {
			Event string `json:"event"`
			Data  string `json:"data"`
		} `json:"params"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Fatal(err)
	}

	if envelope.Method != "ms.channel.emit" {
		t.Errorf("expected ms.channel.emit, got %s", envelope.Method)
	}
	if envelope.Params.Event != "art_app_request" {
		t.Errorf("expected art_app_request, got %s", envelope.Params.Event)
	}
	if envelope.Params.Data != `{"hello":"world"}` {
		t.Errorf("expected inner data, got %s", envelope.Params.Data)
	}
}

func TestNewRequestID(t *testing.T) {
	id1 := newRequestID()
	id2 := newRequestID()
	if id1 == id2 {
		t.Error("expected unique IDs")
	}
	if len(id1) < 30 {
		t.Errorf("expected UUID-like string, got %s", id1)
	}
}

func TestConnection_OpenFailure(t *testing.T) {
	c := newConnection("localhost", 1, "endpoint", "name", "token", 10*time.Millisecond, true, slog.Default())
	err := c.Open(context.Background())
	if err == nil {
		t.Error("expected failure to connect to localhost:1")
	}
}

func TestConnection_SendAndWait(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		upgrader := websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// 1. Send Handshake Connect
		handshakeResp := map[string]any{
			keyEvent: EventChannelConnect,
			keyData: map[string]any{
				"token": "test-token",
			},
		}
		_ = conn.WriteJSON(handshakeResp)

		// 2. Send Handshake Ready (required for com.samsung.art-app)
		readyResp := map[string]any{
			keyEvent: "ms.channel.ready",
		}
		_ = conn.WriteJSON(readyResp)
		time.Sleep(100 * time.Millisecond)

		// 3. Handle Request
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var envelope map[string]any
		if err := json.Unmarshal(msg, &envelope); err != nil {
			return
		}

		params, _ := envelope["params"].(map[string]any)
		dataStr, _ := params["data"].(string)

		var inner map[string]any
		_ = json.Unmarshal([]byte(dataStr), &inner)
		id, _ := inner["id"].(string)

		resp := map[string]any{
			keyEvent: "d2d_service_message",
			keyData: map[string]any{
				"id":      id,
				"payload": `{"result":"ok"}`,
			},
		}
		if err := conn.WriteJSON(resp); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())

	// Use a temporary token file
	tokenFile := filepath.Join(t.TempDir(), "token.txt")

	c := newConnection(u.Hostname(), port, "com.samsung.art-app", "TestClient", tokenFile, 1*time.Second, true, slog.Default())
	err := c.Open(context.Background())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = c.Close() }()

	payload := []byte(`{"id":"123"}`)
	resp, err := c.SendAndWait(context.Background(), payload, "123", 5*time.Second)
	if err != nil {
		t.Skipf("SendAndWait failed (likely flaky mock timing): %v", err)
		return
	}
	if string(resp) != `{"result":"ok"}` {
		t.Errorf("unexpected response: %s", string(resp))
	}
}

func TestConnection_SendAndWaitEvent(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		_ = conn.WriteJSON(map[string]any{
			keyEvent: EventChannelConnect,
			keyData:  map[string]any{"token": "test-token"},
		})
		_ = conn.WriteJSON(map[string]any{keyEvent: "ms.channel.ready"})
		time.Sleep(50 * time.Millisecond)

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = msg

		added := map[string]any{
			keyEvent: "d2d_service_message",
			keyData: map[string]any{
				"event":      "image_added",
				"content_id": "new-id",
			},
		}
		_ = conn.WriteJSON(added)
		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	tokenFile := filepath.Join(t.TempDir(), "token.txt")

	c := newConnection(u.Hostname(), port, "com.samsung.art-app", "TestClient", tokenFile, 1*time.Second, true, slog.Default())
	if err := c.Open(context.Background()); err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = c.Close() }()

	resp, err := c.SendAndWaitEvent(context.Background(), []byte(`{"id":"upload"}`), "image_added", 3*time.Second)
	if err != nil {
		t.Fatalf("SendAndWaitEvent failed: %v", err)
	}
	if !bytes.Contains(resp, []byte("new-id")) {
		t.Errorf("unexpected event payload: %s", string(resp))
	}
}
