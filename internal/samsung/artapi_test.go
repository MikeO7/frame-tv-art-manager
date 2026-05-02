package samsung

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"log/slog"
)

func setupMockArtServer(requestName string, artRespData map[string]any) *httptest.Server {
	upgrader := websocket.Upgrader{}
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Handshake
		resp := wsResponse{Event: "ms.channel.connect", Data: json.RawMessage(`{"token":"test-token"}`)}
		b, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, b)

		respReady := wsResponse{Event: "ms.channel.ready", Data: json.RawMessage(`{}`)}
		bReady, _ := json.Marshal(respReady)
		_ = conn.WriteMessage(websocket.TextMessage, bReady)

		// Wait for request
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var envelope struct {
			Params struct {
				Data string `json:"data"`
			} `json:"params"`
		}
		_ = json.Unmarshal(msg, &envelope)

		var innerReq map[string]any
		_ = json.Unmarshal([]byte(envelope.Params.Data), &innerReq)
		id := innerReq["id"].(string)

		artResp := map[string]any{"request": requestName, "id": id}
		for k, v := range artRespData {
			artResp[k] = v
		}
		artRespBytes, _ := json.Marshal(artResp)

		respMsg := wsResponse{
			Event: "d2d_service_message",
			Data:  json.RawMessage(artRespBytes),
		}
		respBytes, _ := json.Marshal(respMsg)
		_ = conn.WriteMessage(websocket.TextMessage, respBytes)
		time.Sleep(50 * time.Millisecond)
	}))
}

func startTestConnection(t *testing.T, server *httptest.Server) *Connection {
	u, _ := url.Parse(server.URL)
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())

	conn := NewConnection(host, port, "com.samsung.art-app", "TestClient", t.TempDir()+"/token.txt", 1*time.Second, slog.Default())
	if err := conn.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestArtAPI_GetContentList(t *testing.T) {
	contentList := `[{"content_id":"id1","category_id":"cat1"},{"content_id":"id2","category_id":"cat2"}]`
	server := setupMockArtServer("get_content_list", map[string]any{"content_list": contentList})
	defer server.Close()

	conn := startTestConnection(t, server)
	defer func() { _ = conn.Close() }()

	api := NewArtAPI(conn, 1*time.Second, slog.Default())
	list, err := api.GetContentList(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContentList failed: %v", err)
	}

	if len(list) != 2 {
		t.Errorf("expected 2 items, got %d", len(list))
	}
}

func TestArtAPI_GetContentList_Filtered(t *testing.T) {
	contentList := `[{"content_id":"id1","category_id":"MY-C0002"},{"content_id":"id2","category_id":"OTHER"}]`
	server := setupMockArtServer("get_content_list", map[string]any{"content_list": contentList})
	defer server.Close()

	conn := startTestConnection(t, server)
	defer func() { _ = conn.Close() }()

	api := NewArtAPI(conn, 1*time.Second, slog.Default())
	list, err := api.GetContentList(context.Background(), "MY-C0002")
	if err != nil {
		t.Fatal(err)
	}

	if len(list) != 1 {
		t.Errorf("expected 1 item after filtering, got %d", len(list))
	}
}

func TestArtAPI_SendImage(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Handshake
		resp := wsResponse{Event: "ms.channel.connect", Data: json.RawMessage(`{"token":"test-token"}`)}
		b, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, b)
		respReady := wsResponse{Event: "ms.channel.ready", Data: json.RawMessage(`{}`)}
		bReady, _ := json.Marshal(respReady)
		_ = conn.WriteMessage(websocket.TextMessage, bReady)

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var envelope struct {
			Params struct {
				Data string `json:"data"`
			} `json:"params"`
		}
		_ = json.Unmarshal(msg, &envelope)
		var innerReq map[string]any
		_ = json.Unmarshal([]byte(envelope.Params.Data), &innerReq)
		id := innerReq["id"].(string)

		connInfo := `{"id":"` + id + `","ip":"127.0.0.1","port":12345}`
		artResp := map[string]any{"request": "send_image", "id": id, "conn_info": connInfo}
		artRespBytes, _ := json.Marshal(artResp)
		respMsg := wsResponse{Event: "d2d_service_message", Data: json.RawMessage(artRespBytes)}
		respBytes, _ := json.Marshal(respMsg)
		_ = conn.WriteMessage(websocket.TextMessage, respBytes)
		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()

	conn := startTestConnection(t, server)
	defer func() { _ = conn.Close() }()

	api := NewArtAPI(conn, 1*time.Second, slog.Default())
	info, err := api.SendImage(context.Background(), SendImageRequest{FileType: "jpg", FileSize: 100, Matte: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if info.IP != "127.0.0.1" {
		t.Errorf("expected 127.0.0.1, got %s", info.IP)
	}
}

func TestArtAPI_DeleteImages(t *testing.T) {
	server := setupMockArtServer("delete_image_list", nil)
	defer server.Close()

	conn := startTestConnection(t, server)
	defer func() { _ = conn.Close() }()

	api := NewArtAPI(conn, 1*time.Second, slog.Default())
	err := api.DeleteImages(context.Background(), []string{"id1", "id2"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArtAPI_SelectImage(t *testing.T) {
	server := setupMockArtServer("set_art_select_image", nil)
	defer server.Close()

	conn := startTestConnection(t, server)
	defer func() { _ = conn.Close() }()

	api := NewArtAPI(conn, 1*time.Second, slog.Default())
	if err := api.SelectImage(context.Background(), "id1", false); err != nil {
		t.Fatal(err)
	}
}

func TestArtAPI_SetBrightness(t *testing.T) {
	server := setupMockArtServer("set_brightness", nil)
	defer server.Close()

	conn := startTestConnection(t, server)
	defer func() { _ = conn.Close() }()

	api := NewArtAPI(conn, 1*time.Second, slog.Default())
	if err := api.SetBrightness(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
}
