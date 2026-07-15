package samsung

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type d2dCapture struct {
	header  map[string]any
	payload []byte
}

const (
	testProtocolDeleteImage   = "delete_image_list"
	testProtocolGetSlideshow  = "get_slideshow_status"
	testProtocolJPEGExtension = ".jpg"
)

type protocolTVFixture struct {
	server          *httptest.Server
	d2dListener     net.Listener
	d2dCapture      chan d2dCapture
	d2dComplete     chan struct{}
	d2dErr          chan error
	remoteCommands  chan string
	stateMu         sync.Mutex
	inventory       []string
	deleteMutations []string
	slideshow       SlideshowSetting
	slideshowWrites []SlideshowSetting
}

func newProtocolTVFixture(t *testing.T) *protocolTVFixture {
	t.Helper()

	d2dListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for D2D upload: %v", err)
	}
	fixture := &protocolTVFixture{
		d2dListener:    d2dListener,
		d2dCapture:     make(chan d2dCapture, 1),
		d2dComplete:    make(chan struct{}),
		d2dErr:         make(chan error, 1),
		remoteCommands: make(chan string, 2),
		inventory:      []string{"art-1"},
		slideshow:      SlideshowSetting{Interval: 30, Kind: SlideshowShuffle},
	}
	go fixture.captureD2DUpload()

	fixture.server = httptest.NewTLSServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(func() {
		fixture.server.Close()
		_ = fixture.d2dListener.Close()
	})
	return fixture
}

func (f *protocolTVFixture) address(t *testing.T) string {
	t.Helper()
	parsed, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatalf("parse fixture URL: %v", err)
	}
	return parsed.Host
}

func (f *protocolTVFixture) captureD2DUpload() {
	conn, err := f.d2dListener.Accept()
	if err != nil {
		f.d2dErr <- fmt.Errorf("accept D2D upload: %w", err)
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	lengthBytes := make([]byte, 4)
	if _, err := io.ReadFull(conn, lengthBytes); err != nil {
		f.d2dErr <- fmt.Errorf("read D2D header length: %w", err)
		return
	}
	headerLength := binary.BigEndian.Uint32(lengthBytes)
	if headerLength == 0 || headerLength > 1<<20 {
		f.d2dErr <- fmt.Errorf("invalid D2D header length %d", headerLength)
		return
	}
	headerBytes := make([]byte, headerLength)
	if _, err := io.ReadFull(conn, headerBytes); err != nil {
		f.d2dErr <- fmt.Errorf("read D2D header: %w", err)
		return
	}
	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		f.d2dErr <- fmt.Errorf("parse D2D header: %w", err)
		return
	}
	fileLength, ok := header["fileLength"].(float64)
	if !ok || fileLength < 0 || fileLength > 1<<20 {
		f.d2dErr <- fmt.Errorf("invalid D2D file length %v", header["fileLength"])
		return
	}
	payload := make([]byte, int(fileLength))
	if _, err := io.ReadFull(conn, payload); err != nil {
		f.d2dErr <- fmt.Errorf("read D2D payload: %w", err)
		return
	}
	f.d2dCapture <- d2dCapture{header: header, payload: payload}
	close(f.d2dComplete)
}

func (f *protocolTVFixture) serveHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/api/v2/" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"device":{"modelName":"QN55LS03D","firmwareVersion":"1622","FrameTVSupport":"true","PowerState":"on"}}`)
		return
	}

	conn, err := websocket.Accept(w, request, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	if err := writeProtocolFixtureJSON(request.Context(), conn, wsResponse{
		Event: EventChannelConnect,
		Data:  json.RawMessage(`{"token":"fixture-token"}`),
	}); err != nil {
		return
	}
	if strings.Contains(request.URL.Path, endpointRemoteControl) {
		f.serveRemoteControl(request.Context(), conn)
		return
	}
	if err := writeProtocolFixtureJSON(request.Context(), conn, wsResponse{
		Event: EventChannelReady,
		Data:  json.RawMessage(`{}`),
	}); err != nil {
		return
	}
	f.serveArtApp(request.Context(), conn)
}

func (f *protocolTVFixture) serveRemoteControl(ctx context.Context, conn *websocket.Conn) {
	for range 2 {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var envelope struct {
			Params struct {
				Command string `json:"Cmd"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &envelope) == nil {
			f.remoteCommands <- envelope.Params.Command
		}
	}
}

func (f *protocolTVFixture) serveArtApp(ctx context.Context, conn *websocket.Conn) {
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		request, err := parseProtocolFixtureRequest(raw)
		if err != nil {
			return
		}
		response := map[string]any{
			"request_id": request[keyRequestID],
			"id":         request["id"],
		}
		switch request[keyRequest] {
		case keyGetArtModeStatus:
			response[keyValue] = stringOn
		case keyGetContentList:
			response["content_list"] = f.contentList()
		case testProtocolGetSlideshow:
			setting := f.slideshowSetting()
			response[keyValue] = strconv.Itoa(setting.Interval)
			if setting.Interval == 0 {
				response[keyValue] = stringOff
			}
			response["type"] = string(setting.Kind)
		case requestSetSlideshowStatus:
			setting, settingErr := parseSlideshowSetting(
				fmt.Sprint(request[keyValue]), fmt.Sprint(request["type"]),
			)
			if settingErr != nil {
				response["error_code"] = 400
			} else {
				f.setSlideshow(setting)
			}
		case "get_brightness":
			response[keyValue] = 42
		case testProtocolDeleteImage:
			if strings.Contains(fmt.Sprint(request["content_id_list"]), "storage-full") {
				response["error_code"] = 507
			} else {
				f.deleteContent(protocolFixtureContentIDs(request["content_id_list"]))
			}
		case "select_image":
			if request[keyContentID] == "api-error" {
				response["error_code"] = 911
			}
		case requestSendImage:
			connInfo, ok := request["conn_info"].(map[string]any)
			if !ok || fmt.Sprint(request["id"]) != fmt.Sprint(connInfo["id"]) ||
				fmt.Sprint(request[keyRequestID]) != fmt.Sprint(connInfo["id"]) {
				response["error_code"] = 400
				break
			}
			port := f.d2dListener.Addr().(*net.TCPAddr).Port
			response["conn_info"] = map[string]any{
				"ip": "127.0.0.1", "port": port, "key": "fixture-key", "secured": false,
			}
		}
		if err := writeProtocolD2DResponse(ctx, conn, response); err != nil {
			return
		}
		if request[keyRequest] == requestSendImage {
			select {
			case <-f.d2dComplete:
				_ = writeProtocolD2DResponse(ctx, conn, map[string]any{
					"event": "image_added", keyContentID: "uploaded-art",
				})
			case <-f.d2dErr:
				return
			case <-ctx.Done():
				return
			}
		}
	}
}

func (f *protocolTVFixture) slideshowSetting() SlideshowSetting {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return f.slideshow
}

func (f *protocolTVFixture) setSlideshow(setting SlideshowSetting) {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	f.slideshow = setting
	f.slideshowWrites = append(f.slideshowWrites, setting)
}

func (f *protocolTVFixture) slideshowMutations() []SlideshowSetting {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return append([]SlideshowSetting(nil), f.slideshowWrites...)
}

func (f *protocolTVFixture) contentList() []map[string]string {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	content := make([]map[string]string, 0, len(f.inventory))
	for _, contentID := range f.inventory {
		content = append(content, map[string]string{
			keyContentID: contentID, keyCategoryID: userArtCategory,
		})
	}
	return content
}

func (f *protocolTVFixture) deleteContent(contentIDs []string) {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	for _, contentID := range contentIDs {
		f.deleteMutations = append(f.deleteMutations, contentID)
		kept := f.inventory[:0]
		for _, existing := range f.inventory {
			if existing != contentID {
				kept = append(kept, existing)
			}
		}
		f.inventory = kept
	}
}

func protocolFixtureContentIDs(value any) []string {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var entries []map[string]string
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	contentIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if contentID := strings.TrimSpace(entry[keyContentID]); contentID != "" {
			contentIDs = append(contentIDs, contentID)
		}
	}
	return contentIDs
}

func parseProtocolFixtureRequest(raw []byte) (map[string]any, error) {
	var envelope struct {
		Params struct {
			Data string `json:"data"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	var request map[string]any
	if err := json.Unmarshal([]byte(envelope.Params.Data), &request); err != nil {
		return nil, err
	}
	return request, nil
}

func writeProtocolFixtureJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, raw)
}

func writeProtocolD2DResponse(ctx context.Context, conn *websocket.Conn, response map[string]any) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return writeProtocolFixtureJSON(ctx, conn, wsResponse{
		Event: EventD2DServiceMessage,
		Data:  json.RawMessage(raw),
	})
}

func protocolErrorOutcome(err error) Outcome {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Outcome
	}
	return OutcomeNotAttempted
}

func TestProtocolTransportObservesAndMutatesFrameTV(t *testing.T) {
	fixture := newProtocolTVFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "auth", "token")
	transport := newProtocolTransport(Config{
		Address: fixture.address(t), ClientName: "protocol-fixture", TokenPath: tokenPath,
		ConnectTimeout: 2 * time.Second, RequestTimeout: 2 * time.Second,
	}, Dependencies{Logger: slog.New(slog.DiscardHandler)}).(*protocolTransport)
	ctx := context.Background()

	if err := transport.Connect(ctx, false); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := transport.Connect(ctx, false); err != nil {
		t.Fatalf("second Connect() error = %v", err)
	}
	info, err := transport.DeviceInfo(ctx)
	if err != nil {
		t.Fatalf("DeviceInfo() error = %v", err)
	}
	if info.ModelName != "QN55LS03D" || !info.IsFrameTV() || !info.IsOn() {
		t.Fatalf("DeviceInfo() = %+v", info)
	}
	if mode, err := transport.ArtMode(ctx); err != nil || mode != stringOn {
		t.Fatalf("ArtMode() = %q, %v", mode, err)
	}
	inventory, err := transport.Inventory(ctx)
	if err != nil || !strings.Contains(string(inventory), "art-1") {
		t.Fatalf("Inventory() = %s, %v", inventory, err)
	}
	if err := transport.Delete(ctx, "art-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := transport.Select(ctx, "art-1"); err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	wantObservedSlideshow := SlideshowSetting{Interval: 30, Kind: SlideshowShuffle}
	if value, err := transport.Slideshow(ctx); err != nil || value != wantObservedSlideshow {
		t.Fatalf("Slideshow() = %+v, %v", value, err)
	}
	disabled := SlideshowSetting{Kind: SlideshowShuffle}
	if err := transport.ConfigureSlideshow(ctx, disabled); err != nil {
		t.Fatalf("ConfigureSlideshow(off) error = %v", err)
	}
	sequential := SlideshowSetting{Interval: 15, Kind: SlideshowSequential}
	if err := transport.ConfigureSlideshow(ctx, sequential); err != nil {
		t.Fatalf("ConfigureSlideshow(15) error = %v", err)
	}
	if got := fixture.slideshowSetting(); got != sequential {
		t.Fatalf("wire slideshow setting = %+v, want %+v", got, sequential)
	}
	if got := fixture.slideshowMutations(); len(got) != 2 || got[0] != disabled || got[1] != sequential {
		t.Fatalf("wire slideshow mutations = %+v", got)
	}
	if value, err := transport.Brightness(ctx); err != nil || value != 42 {
		t.Fatalf("Brightness() = %d, %v", value, err)
	}
	if err := transport.ConfigureBrightness(ctx, 61); err != nil {
		t.Fatalf("ConfigureBrightness() error = %v", err)
	}

	imagePath := filepath.Join(t.TempDir(), "upload.jpg")
	if err := os.WriteFile(imagePath, []byte("image-data"), 0o600); err != nil {
		t.Fatalf("write upload fixture: %v", err)
	}
	file, err := os.Open(imagePath)
	if err != nil {
		t.Fatalf("open upload fixture: %v", err)
	}
	defer func() { _ = file.Close() }()
	fileType := strings.TrimPrefix(testProtocolJPEGExtension, ".")
	contentID, err := transport.Upload(ctx, preparedUpload{
		file: file, fileType: fileType, matte: "none", size: int64(len("image-data")),
		digest: sha256.Sum256([]byte("image-data")),
	})
	if err != nil || contentID != "uploaded-art" {
		t.Fatalf("Upload() = %q, %v", contentID, err)
	}
	select {
	case capture := <-fixture.d2dCapture:
		if string(capture.payload) != "image-data" || capture.header["secKey"] != "fixture-key" || capture.header["fileType"] != fileType {
			t.Fatalf("D2D capture = %+v, payload %q", capture.header, capture.payload)
		}
	case err := <-fixture.d2dErr:
		t.Fatalf("D2D capture error = %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for D2D capture")
	}

	if err := transport.Delete(ctx, "storage-full"); !errors.Is(err, ErrStorageFull) || protocolErrorOutcome(err) != OutcomeNotApplied {
		t.Fatalf("Delete(storage-full) error = %v, outcome = %d", err, protocolErrorOutcome(err))
	}
	if err := transport.Select(ctx, "api-error"); !errors.Is(err, ErrArtAPIError) || protocolErrorOutcome(err) != OutcomeNotApplied {
		t.Fatalf("Select(api-error) error = %v, outcome = %d", err, protocolErrorOutcome(err))
	}

	powerCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := transport.PowerOff(powerCtx); err != nil {
		t.Fatalf("PowerOff() error = %v", err)
	}
	for _, want := range []string{"Press", "Release"} {
		select {
		case got := <-fixture.remoteCommands:
			if got != want {
				t.Fatalf("remote command = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for remote command %q", want)
		}
	}

	if err := transport.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := transport.Close(ctx); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := transport.ArtMode(ctx); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("ArtMode() after close error = %v, want ErrNotConnected", err)
	}
	if token, err := os.ReadFile(tokenPath); err != nil || string(token) != "fixture-token" {
		t.Fatalf("persisted token = %q, %v", token, err)
	}
	if mode, err := os.Stat(tokenPath); err != nil || mode.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %v, %v", mode, err)
	}
}

func TestProtocolTransportRejectsUnsafeOrInvalidCommands(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "missing-token")
	transport := &protocolTransport{
		config: Config{Address: "127.0.0.1:1", TokenPath: tokenPath, ConnectTimeout: time.Millisecond},
		logger: slog.New(slog.DiscardHandler),
	}
	if err := transport.Connect(context.Background(), true); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("dry-run Connect() error = %v, want ErrUnauthorized", err)
	}
	if _, err := transport.Brightness(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Brightness() error = %v, want ErrNotConnected", err)
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := transport.Wake(canceledCtx); !errors.Is(err, context.Canceled) || protocolErrorOutcome(err) != OutcomeNotAttempted {
		t.Fatalf("Wake(canceled) error = %v, outcome = %d", err, protocolErrorOutcome(err))
	}

	for _, test := range []struct {
		address  string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{address: "frame-tv.local", wantHost: "frame-tv.local", wantPort: portArtWSS},
		{address: "127.0.0.1:9443", wantHost: "127.0.0.1", wantPort: 9443},
		{address: " ", wantErr: true},
		{address: "127.0.0.1:99999", wantErr: true},
	} {
		host, port, err := observationAddress(test.address)
		if (err != nil) != test.wantErr || host != test.wantHost || port != test.wantPort {
			t.Fatalf("observationAddress(%q) = %q, %d, %v", test.address, host, port, err)
		}
	}

	unknown := tokenPersistenceError(errors.New("disk failed"))
	if protocolErrorOutcome(unknown) != OutcomeNotAttempted {
		t.Fatalf("tokenPersistenceError() outcome = %d", protocolErrorOutcome(unknown))
	}
}
