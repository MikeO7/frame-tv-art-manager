package samsung

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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
		resp := wsResponse{Event: EventChannelConnect, Data: json.RawMessage(`{"token":"test-token"}`)}
		b, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, b)

		respReady := wsResponse{Event: EventChannelReady, Data: json.RawMessage(`{}`)}
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
			Event: EventD2DServiceMessage,
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
		resp := wsResponse{Event: EventChannelConnect, Data: json.RawMessage(`{"token":"test-token"}`)}
		b, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, b)
		respReady := wsResponse{Event: EventChannelReady, Data: json.RawMessage(`{}`)}
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
		respMsg := wsResponse{Event: EventD2DServiceMessage, Data: json.RawMessage(artRespBytes)}
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

const testValue = "value"
const testCat1 = "cat1"
const testContentID = "content_id"

func TestArtAPI_GetArtModeStatus(t *testing.T) {
	server := setupMockArtServer("get_artmode_status", map[string]any{testValue: "on"})
	defer server.Close()

	conn := startTestConnection(t, server)
	defer func() { _ = conn.Close() }()

	api := NewArtAPI(conn, 1*time.Second, slog.Default())
	status, err := api.GetArtModeStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != "on" {
		t.Errorf("expected on, got %s", status)
	}
}

func TestArtAPI_GetSlideshowStatus(t *testing.T) {
	server := setupMockArtServer("get_slideshow_status", map[string]any{
		testValue:     "3",
		"type":        "slideshow",
		"category_id": testCat1,
	})
	defer server.Close()

	conn := startTestConnection(t, server)
	defer func() { _ = conn.Close() }()

	api := NewArtAPI(conn, 1*time.Second, slog.Default())
	ss, err := api.GetSlideshowStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ss.Value != "3" || ss.Type != "slideshow" || ss.CategoryID != "cat1" {
		t.Errorf("unexpected slideshow status: %+v", ss)
	}
}

func TestArtAPI_SetSlideshowStatus(t *testing.T) {
	server := setupMockArtServer("set_slideshow_status", nil)
	defer server.Close()

	conn := startTestConnection(t, server)
	defer func() { _ = conn.Close() }()

	api := NewArtAPI(conn, 1*time.Second, slog.Default())
	err := api.SetSlideshowStatus(context.Background(), SlideshowStatus{Value: "15", Type: "shuffle", CategoryID: "cat1"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArtAPI_GetCategories(t *testing.T) {
	cats := `{"categories":[{"id":"cat1","name":"Category 1"}]}`
	server := setupMockArtServer("get_categories", map[string]any{"categories": cats})
	defer server.Close()

	conn := startTestConnection(t, server)
	defer func() { _ = conn.Close() }()

	api := NewArtAPI(conn, 1*time.Second, slog.Default())
	raw, err := api.GetCategories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), "cat1") {
		t.Errorf("expected cat1 in response, got %s", string(raw))
	}
}

func TestArtAPI_WaitForImageAdded(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Handshake
		resp := wsResponse{Event: EventChannelConnect, Data: json.RawMessage(`{"token":"test-token"}`)}
		b, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, b)
		respReady := wsResponse{Event: EventChannelReady, Data: json.RawMessage(`{}`)}
		bReady, _ := json.Marshal(respReady)
		_ = conn.WriteMessage(websocket.TextMessage, bReady)

		// Send event after a short delay
		time.Sleep(100 * time.Millisecond)
		artResp := map[string]any{keyEvent: "image_added", testContentID: "new-id"}
		artRespBytes, _ := json.Marshal(artResp)
		respMsg := wsResponse{Event: EventD2DServiceMessage, Data: json.RawMessage(artRespBytes)}
		respBytes, _ := json.Marshal(respMsg)
		_ = conn.WriteMessage(websocket.TextMessage, respBytes)
		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()

	conn := startTestConnection(t, server)
	defer func() { _ = conn.Close() }()

	api := NewArtAPI(conn, 1*time.Second, slog.Default())
	id, err := api.WaitForImageAdded(context.Background(), 1*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if id != "new-id" {
		t.Errorf("expected new-id, got %s", id)
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
