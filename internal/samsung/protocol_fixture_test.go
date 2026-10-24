package samsung

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const (
	testProtocolDeleteImage  = "delete_image_list"
	testProtocolGetSlideshow = "get_slideshow_status"
)

type d2dCapture struct {
	header  map[string]any
	payload []byte
}

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
	imageAddedError int
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
				imageAdded := map[string]any{
					"event": "image_added", keyContentID: "uploaded-art",
				}
				f.stateMu.Lock()
				if f.imageAddedError != 0 {
					imageAdded["error_code"] = f.imageAddedError
				}
				f.stateMu.Unlock()
				_ = writeProtocolD2DResponse(ctx, conn, imageAdded)
			case err := <-f.d2dErr:
				// Return the transfer error to the test that waits for it.
				f.d2dErr <- err
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

// ProtocolTVFixture is the test-only local Samsung protocol server used by
// production-boundary integration tests.
type ProtocolTVFixture = protocolTVFixture

// NewProtocolTVFixture starts the shared local Samsung protocol fixture.
func NewProtocolTVFixture(t *testing.T) *ProtocolTVFixture {
	t.Helper()
	return newProtocolTVFixture(t)
}

// Address returns the fixture's TLS WebSocket and device-info address.
func (f *protocolTVFixture) Address(t *testing.T) string {
	t.Helper()
	return f.address(t)
}

// DeleteMutations returns successful delete commands observed on the wire.
func (f *protocolTVFixture) DeleteMutations() []string {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return append([]string(nil), f.deleteMutations...)
}

// Inventory returns the fixture's current user-art content IDs.
func (f *protocolTVFixture) Inventory() []string {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return append([]string(nil), f.inventory...)
}

// SlideshowSetting returns the fixture's current typed slideshow state.
func (f *protocolTVFixture) SlideshowSetting() SlideshowSetting {
	return f.slideshowSetting()
}

// SlideshowMutations returns typed slideshow writes observed on the wire.
func (f *protocolTVFixture) SlideshowMutations() []SlideshowSetting {
	return f.slideshowMutations()
}
