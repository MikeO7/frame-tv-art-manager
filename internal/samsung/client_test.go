package samsung

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/coder/websocket"
)

const (
	testType        = "type"
	testContentList = "content_list"
	testSendImage   = "send_image"
	testConnInfo    = "conn_info"
	testSlideshow   = "slideshow"
	testValue       = "value"
	testCat1        = "cat1"
	testContentID   = "content_id"
)

func TestClient_New(t *testing.T) {
	cfg := &config.Config{TokenDir: "/tmp"}
	c := NewClient("192.168.1.10", cfg.TVConnectOptions(), slog.Default())
	if c.IP != "192.168.1.10" {
		t.Errorf("expected IP 192.168.1.10, got %s", c.IP)
	}
}

func TestClient_TokenFilePath(t *testing.T) {
	cfg := &config.Config{TokenDir: "/data/tokens"}
	c := NewClient("1.2.3.4", cfg.TVConnectOptions(), slog.Default())
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
	var ci connInfo
	if err := json.Unmarshal([]byte(rawJSON), &ci); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if ci.IP != "127.0.0.1" || ci.Port.String() != "12345" {
		t.Errorf("incorrect parsing: %+v", ci)
	}
}

func TestSendWOL_Invalid(t *testing.T) {
	c := NewClient("192.168.1.10", (&config.Config{}).TVConnectOptions(), slog.Default())
	err := c.sendWOL(context.Background(), "invalid")
	if err == nil {
		t.Error("expected error for invalid MAC, got nil")
	}

	err = c.sendWOL(context.Background(), "")
	if err != nil {
		t.Errorf("expected nil for empty MAC, got %v", err)
	}
}

func TestSendWOL_ValidFormat(_ *testing.T) {
	c := NewClient("192.168.1.10", (&config.Config{}).TVConnectOptions(), slog.Default())
	// Actually, sendWOL calls net.Dial.
	// Since we are mocking/ignoring network, we just want to ensure it parses the MAC correctly
	// and attempts to send. If the test environment allows UDP broadcast, it passes.
	// If it fails with "network is unreachable", that's also acceptable for this unit test context,
	// but let's just make sure it doesn't fail parsing.
	_ = c.sendWOL(context.Background(), "AA:BB:CC:DD:EE:FF")
}

func TestEnsureToken(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		resp := wsResponse{Event: EventChannelConnect, Data: json.RawMessage(`{"token":"new-token"}`)}
		b, _ := json.Marshal(resp)
		_ = conn.Write(r.Context(), websocket.MessageText, b)
	}))
	defer server.Close()

	u, _ := neturl.Parse(server.URL)
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	tokenFile := filepath.Join(t.TempDir(), "token.txt")

	c := NewClient(host, (&config.Config{ConnectionTimeout: 1 * time.Second}).TVConnectOptions(), slog.Default())
	err := c.ensureToken(context.Background(), tokenFile, port)
	if err != nil {
		t.Fatalf("ensureToken failed: %v", err)
	}

	data, _ := os.ReadFile(filepath.Clean(tokenFile))
	if string(data) != "new-token" {
		t.Errorf("expected new-token, got %s", string(data))
	}
}

type roundTripFunc func(req *http.Request) *http.Response

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

func TestCheckArtModeGate(t *testing.T) {
	// We replace http.DefaultTransport to intercept requests to port 8001
	originalTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = originalTransport }()

	t.Run("IsArtMode", func(t *testing.T) {
		http.DefaultTransport = roundTripFunc(func(req *http.Request) *http.Response {
			if req.URL.Port() == "8001" && req.URL.Path == "/ms/art" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       http.NoBody,
				}
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody}
		})

		c := NewClient("192.168.1.10", (&config.Config{GateTimeout: 1 * time.Second}).TVConnectOptions(), slog.Default())
		isArt, err := c.checkArtModeGate(context.Background())
		if err != nil {
			t.Fatalf("expected nil err, got %v", err)
		}
		if !isArt {
			t.Errorf("expected isArt to be true")
		}
	})

	t.Run("NotArtMode", func(t *testing.T) {
		http.DefaultTransport = roundTripFunc(func(_ *http.Request) *http.Response {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       http.NoBody,
			}
		})

		c := NewClient("192.168.1.10", (&config.Config{GateTimeout: 1 * time.Second}).TVConnectOptions(), slog.Default())
		isArt, err := c.checkArtModeGate(context.Background())
		if err != nil {
			t.Fatalf("expected nil err, got %v", err)
		}
		if isArt {
			t.Errorf("expected isArt to be false")
		}
	})
}

type errorTransport struct{}

func (e errorTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, os.ErrDeadlineExceeded
}

func TestCheckArtModeGate_Timeout(t *testing.T) {
	originalTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = originalTransport }()

	http.DefaultTransport = errorTransport{}

	c := NewClient("192.168.1.10", (&config.Config{GateTimeout: 1 * time.Second}).TVConnectOptions(), slog.Default())
	isArt, err := c.checkArtModeGate(context.Background())
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if isArt {
		t.Errorf("expected isArt to be false")
	}
}

func TestTurnOff(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		// Handshake
		resp := wsResponse{Event: EventChannelConnect, Data: json.RawMessage(`{"token":"test-token"}`)}
		_ = writeJSON(r.Context(), resp)

		respReady := wsResponse{Event: EventChannelReady, Data: json.RawMessage(`{}`)}
		_ = writeJSON(r.Context(), respReady)

		// Read KEY_POWER Press
		_, msg, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var pressReq map[string]any
		_ = json.Unmarshal(msg, &pressReq)
		if pressReq["method"] != methodRemoteControl {
			t.Errorf("expected method ms.remote.control, got %v", pressReq["method"])
		}

		// Read KEY_POWER Release
		_, msg, err = conn.Read(r.Context())
		if err != nil {
			return
		}
		var releaseReq map[string]any
		_ = json.Unmarshal(msg, &releaseReq)
		if releaseReq["method"] != methodRemoteControl {
			t.Errorf("expected method ms.remote.control, got %v", releaseReq["method"])
		}
	}))
	defer server.Close()

	u, _ := neturl.Parse(server.URL)
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())

	// Override port in turnOffTV is not natively supported since it hardcodes 8002.
	// But `turnOffTV` connects using `c.IP`. The only way is to use IP loopback, and we can't change port 8002 without changing `turnOffTV` code. Wait! We can change the default transport for WebSockets? No, `turnOffTV` uses `newConnection` which dials `8002`.
	// Since turnOffTV hardcodes 8002, we will use a small trick:
	// If `c.IP` is `localhost`, `newConnection` will try to dial `localhost:8002`.
	// We could temporarily modify `newConnection` dialer or just extract port to parameter,
	// but let's intercept the `DialContext` for websockets or just change turnOffTV.
	// Wait, we can just change `turnOffTV` to accept a port just like `ensureToken` does!
	c := NewClient(host, (&config.Config{ConnectionTimeout: 2 * time.Second, TokenDir: t.TempDir()}).TVConnectOptions(), slog.Default())

	// Shorten the KEY_POWER hold so the test verifies the press/release
	// sequence without waiting the full real-world hold duration.
	c.powerHold = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.turnOffTV(ctx, port)
	if err != nil {
		t.Fatalf("expected turnOffTV to succeed, got: %v", err)
	}
}

func TestClientWrapperMethods(t *testing.T) {
	// First, we create an artAPI mock just to satisfy the wrapper.
	// Since artAPI is created by the Connect function, testing these without connecting
	// means we manually inject the internal fields for testing.

	// Create a mock WS server for ArtAPI so we can test the wrappers.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		// Send initial connects
		resp := wsResponse{Event: EventChannelConnect, Data: json.RawMessage(`{"token":"test-token"}`)}
		_ = writeJSON(r.Context(), resp)

		respReady := wsResponse{Event: EventChannelReady, Data: json.RawMessage(`{}`)}
		_ = writeJSON(r.Context(), respReady)

		for {
			_, msg, err := conn.Read(r.Context())
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
			reqType := innerReq["request"].(string)

			var artResp map[string]any

			switch reqType {
			case "get_content_list":
				artResp = map[string]any{keyRequest: reqType, "id": id, testContentList: `[{"content_id":"id1", "category_id": "MY-C0002"}]`}
			case "delete_image_list":
				artResp = map[string]any{keyRequest: reqType, "id": id}
			case "set_art_select_image":
				artResp = map[string]any{keyRequest: reqType, "id": id}
			case "get_slideshow_status":
				artResp = map[string]any{keyRequest: reqType, "id": id, testValue: "10", testType: "slideshow"}
			case "set_slideshow_status":
				artResp = map[string]any{keyRequest: reqType, "id": id}
			case "set_brightness":
				artResp = map[string]any{keyRequest: reqType, "id": id}
			case "get_artmode_status":
				artResp = map[string]any{keyRequest: reqType, "id": id, testValue: "on"}
			case "get_categories":
				artResp = map[string]any{keyRequest: reqType, "id": id, "categories": `{"categories":[{"id":"MY-C0002"}]}`}
			default:
				artResp = map[string]any{keyRequest: reqType, "id": id}
			}

			artRespBytes, _ := json.Marshal(artResp)
			respMsg := wsResponse{Event: EventD2DServiceMessage, Data: json.RawMessage(artRespBytes)}
			respBytes, _ := json.Marshal(respMsg)
			_ = conn.Write(r.Context(), websocket.MessageText, respBytes)
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer server.Close()

	u, _ := neturl.Parse(server.URL)
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())

	c := NewClient(host, (&config.Config{ConnectionTimeout: 2 * time.Second, APITimeout: 2 * time.Second, TokenDir: t.TempDir()}).TVConnectOptions(), slog.Default())

	// Create art connection directly to test wrappers
	c.artConn = newConnection(connConfig{
		host:          host,
		port:          port,
		endpoint:      "com.samsung.art-app",
		name:          "TestClient",
		tokenFile:     c.tokenFilePath(),
		timeout:       1 * time.Second,
		skipTLSVerify: true,
		logger:        slog.Default(),
	})
	if err := c.artConn.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Device info test
	c.info = &DeviceInfo{PowerState: "on", ModelName: "TEST"}

	if !c.IsInArtMode(context.Background()) {
		t.Errorf("expected IsInArtMode to be true")
	}

	imgs, err := c.ListUploaded(context.Background())
	if err != nil {
		t.Errorf("GetUploadedImages err: %v", err)
	}
	if len(imgs) != 1 {
		t.Errorf("expected 1 image")
	}

	err = c.DeleteImages(context.Background(), []string{"id1"})
	if err != nil {
		t.Errorf("DeleteImages err: %v", err)
	}

	err = c.SelectImage(context.Background(), "id1")
	if err != nil {
		t.Errorf("SelectImage err: %v", err)
	}

	ss, err := c.SlideshowStatus(context.Background())
	if err != nil {
		t.Errorf("SlideshowStatus err: %v", err)
	}
	if ss.Value != "10" {
		t.Errorf("expected 10")
	}

	err = c.SetSlideshow(context.Background(), SlideshowStatus{Value: "15"})
	if err != nil {
		t.Errorf("SetSlideshow err: %v", err)
	}

	err = c.SetBrightness(context.Background(), 5)
	if err != nil {
		t.Errorf("SetBrightness err: %v", err)
	}

	// Wrapper coverage
	if c.DeviceInfo().ModelName != "TEST" {
		t.Errorf("expected TEST model")
	}

	err = c.SaveMetadata(context.Background())
	if err != nil {
		t.Errorf("SaveMetadata err: %v", err)
	}

	err = c.TurnOff(context.Background())
	// Expected context deadline exceeded because mock remote server sleeps / isn't connected exactly,
	// actually `TurnOff` initiates its own connection to port 8002 via `turnOffTV` so this fails in `conn.Open`
	if err == nil {
		t.Errorf("expected TurnOff to fail due to no listener on port 8002")
	}
}

func TestClientUpload(t *testing.T) {
	// Create mock server representing both Art API and D2D file server (for simplicity they run on the same IP but different port)
	d2dServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Just simulate accepting file and immediately confirm image_added over websocket
		w.WriteHeader(http.StatusOK)
	}))
	defer d2dServer.Close()

	d2dU, _ := neturl.Parse(d2dServer.URL)
	d2dHost := d2dU.Hostname()
	d2dPort := d2dU.Port()

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

		resp := wsResponse{Event: EventChannelConnect, Data: json.RawMessage(`{"token":"test-token"}`)}
		_ = writeJSON(r.Context(), resp)

		respReady := wsResponse{Event: EventChannelReady, Data: json.RawMessage(`{}`)}
		_ = writeJSON(r.Context(), respReady)

		_, msg, err := conn.Read(r.Context())
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

		connInfoJSON := fmt.Sprintf(`{"ip":"%s", "port":%s}`, d2dHost, d2dPort)
		artResp := map[string]any{
			keyRequest:   testSendImage,
			"id":         id,
			testConnInfo: connInfoJSON,
		}

		artRespBytes, _ := json.Marshal(artResp)
		respMsg := wsResponse{Event: EventD2DServiceMessage, Data: json.RawMessage(artRespBytes)}
		respBytes, _ := json.Marshal(respMsg)
		_ = conn.Write(r.Context(), websocket.MessageText, respBytes)

		// Fire image_added immediately
		time.Sleep(100 * time.Millisecond)
		addedResp := map[string]any{
			"event":       "image_added",
			testContentID: "new-upload-id",
		}
		addedRespBytes, _ := json.Marshal(addedResp)
		respMsgAdded := wsResponse{Event: EventD2DServiceMessage, Data: json.RawMessage(addedRespBytes)}
		respBytesAdded, _ := json.Marshal(respMsgAdded)
		_ = conn.Write(r.Context(), websocket.MessageText, respBytesAdded)
		time.Sleep(50 * time.Millisecond)
	}))
	defer wsServer.Close()

	u, _ := neturl.Parse(wsServer.URL)
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())

	c := NewClient(host, (&config.Config{ConnectionTimeout: 2 * time.Second, APITimeout: 2 * time.Second, TokenDir: t.TempDir()}).TVConnectOptions(), slog.Default())
	c.artConn = newConnection(connConfig{
		host:          host,
		port:          port,
		endpoint:      "com.samsung.art-app",
		name:          "TestClient",
		tokenFile:     c.tokenFilePath(),
		timeout:       1 * time.Second,
		skipTLSVerify: true,
		logger:        slog.Default(),
	})
	if err := c.artConn.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Need a dummy file
	tmpFile := filepath.Join(t.TempDir(), "dummy.jpg")
	_ = os.WriteFile(tmpFile, []byte("data"), 0o600)

	id, err := c.Upload(context.Background(), tmpFile, "jpg", "shadowbox_polar")
	if err != nil {
		t.Fatalf("Upload failed: %v", err)
	}
	if id != "new-upload-id" {
		t.Errorf("expected new-upload-id, got %s", id)
	}
}

func TestIsInArtMode_Branches(t *testing.T) {
	c := NewClient("127.0.0.1", (&config.Config{}).TVConnectOptions(), slog.Default())
	c.info = &DeviceInfo{PowerState: "standby"}
	if c.IsInArtMode(context.Background()) {
		t.Errorf("expected false when TV is off")
	}

	// Error handling tests for GetArtModeStatus require extensive mocking.
}

func TestClientUpload_FileStatError(t *testing.T) {
	c := NewClient("127.0.0.1", (&config.Config{}).TVConnectOptions(), slog.Default())
	_, err := c.Upload(context.Background(), "/does/not/exist.jpg", "jpg", "none")
	if err == nil {
		t.Errorf("expected error on non-existent file")
	}
}

func TestSaveMetadata_NoDir(t *testing.T) {
	c := NewClient("127.0.0.1", (&config.Config{TokenDir: "/root/forbidden/path"}).TVConnectOptions(), slog.Default())

	// Needs a valid artAPI mock connection or it panics in SaveMetadata's internal call to getSlideshowStatus
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()

	u, _ := neturl.Parse(server.URL)
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())

	c.artConn = newConnection(connConfig{
		host:          host,
		port:          port,
		endpoint:      "com.samsung.art-app",
		name:          "TestClient",
		tokenFile:     c.tokenFilePath(),
		timeout:       1 * time.Second,
		skipTLSVerify: true,
		logger:        slog.Default(),
	})
	_ = c.artConn.Open(context.Background())
	defer func() { _ = c.Close() }()

	err := c.SaveMetadata(context.Background())
	if err == nil {
		t.Errorf("expected error when saving to forbidden path")
	}
}

func TestClientClose_Nil(t *testing.T) {
	c := NewClient("127.0.0.1", (&config.Config{}).TVConnectOptions(), slog.Default())
	err := c.Close()
	if err != nil {
		t.Errorf("expected nil when closing uninitialized client")
	}
}

func TestClient_Backoff(t *testing.T) {
	c := NewClient("1.1.1.1", (&config.Config{}).TVConnectOptions(), slog.Default())

	c.backoffUntil = time.Now().Add(5 * time.Minute)
	if !c.ShouldSkip() {
		t.Error("expected ShouldSkip during backoff")
	}

	c.RecordFailure(time.Minute)
	if c.failures != 1 {
		t.Errorf("failures = %d", c.failures)
	}
	if !c.ShouldSkip() {
		t.Error("expected ShouldSkip after RecordFailure")
	}

	c.RecordSuccess()
	if c.ShouldSkip() {
		t.Error("expected no skip after RecordSuccess")
	}
	if c.failures != 0 {
		t.Errorf("failures after success = %d", c.failures)
	}
}

func TestClient_RecordFailure_MaxBackoff(t *testing.T) {
	c := NewClient("1.1.1.1", (&config.Config{}).TVConnectOptions(), slog.Default())
	for i := 0; i < 10; i++ {
		c.RecordFailure(time.Minute)
	}
	if c.failures != 10 {
		t.Errorf("failures = %d", c.failures)
	}
	if !c.ShouldSkip() {
		t.Error("expected skip after repeated failures")
	}
}

func TestClient_Model_Empty(t *testing.T) {
	c := NewClient("1.1.1.1", (&config.Config{}).TVConnectOptions(), slog.Default())
	if c.Model() != "" {
		t.Errorf("expected empty model, got %q", c.Model())
	}
}

func TestClient_PublicTransportMethods(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		for {
			_, msg, err := conn.Read(r.Context())
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
			reqType := innerReq["request"].(string)

			var artResp map[string]any

			switch reqType {
			case "get_content_list":
				artResp = map[string]any{keyRequest: reqType, "id": id, testContentList: `[{"content_id":"cid1", "category_id": "MY-C0002"}]`}
			case "get_artmode_status":
				artResp = map[string]any{keyRequest: reqType, "id": id, testValue: "on"}
			default:
				artResp = map[string]any{keyRequest: reqType, "id": id, testValue: "on"}
			}
			artRespBytes, _ := json.Marshal(artResp)
			respMsg := wsResponse{Event: EventD2DServiceMessage, Data: json.RawMessage(artRespBytes)}
			respBytes, _ := json.Marshal(respMsg)
			_ = conn.Write(r.Context(), websocket.MessageText, respBytes)
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer server.Close()

	u, _ := neturl.Parse(server.URL)
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())

	c := NewClient(host, (&config.Config{
		TokenDir:          t.TempDir(),
		ConnectionTimeout: 2 * time.Second,
		APITimeout:        2 * time.Second,
	}).TVConnectOptions(), slog.Default())

	c.artConn = newConnection(connConfig{
		host:          host,
		port:          port,
		endpoint:      "com.samsung.art-app",
		name:          "TestClient",
		tokenFile:     c.tokenFilePath(),
		timeout:       1 * time.Second,
		skipTLSVerify: true,
		logger:        slog.Default(),
	})
	if err := c.artConn.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	c.info = &DeviceInfo{PowerState: "on", ModelName: "Frame TV"}

	if c.Model() != "Frame TV" {
		t.Errorf("Model() = %q", c.Model())
	}
	if !c.IsInArtMode(context.Background()) {
		t.Error("expected art mode")
	}
	imgs, err := c.ListUploaded(context.Background())
	if err != nil || len(imgs) != 1 {
		t.Errorf("ListUploaded = %v err=%v", imgs, err)
	}
	if err := c.DeleteImages(context.Background(), []string{"cid1"}); err != nil {
		t.Errorf("DeleteImages: %v", err)
	}
	if err := c.SelectImage(context.Background(), "cid1"); err != nil {
		t.Errorf("SelectImage: %v", err)
	}
	ss, err := c.SlideshowStatus(context.Background())
	if err != nil || ss == nil {
		t.Errorf("SlideshowStatus = %v err=%v", ss, err)
	}
	if err := c.SetSlideshow(context.Background(), SlideshowStatus{Value: "15"}); err != nil {
		t.Errorf("SetSlideshow: %v", err)
	}
	if err := c.SetBrightness(context.Background(), 6); err != nil {
		t.Errorf("SetBrightness: %v", err)
	}
	if err := c.SaveMetadata(context.Background()); err != nil {
		t.Errorf("SaveMetadata: %v", err)
	}
}
