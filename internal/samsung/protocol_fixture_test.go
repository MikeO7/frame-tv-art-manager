package samsung

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The fake Frame TV below reproduces wire behavior captured from real TVs by
// NickWaterton/samsung-tv-ws-api (tests/fixtures, devices/2021_frame_QN55LS03A
// and devices/2022_frame_QN43LS03B): string error codes in "event":"error"
// envelopes, unsolicited startup events, a numeric pairing token, inventories
// that mix every category, 0-10 brightness with min/max, conn_info sent as an
// encoded string with a string port, and secured D2D sockets on 2022 models.

const (
	testProtocolDeleteImage  = "delete_image_list"
	testProtocolGetSlideshow = "get_slideshow_status"
	fixtureToken             = "123456789"
	fixtureFavorites         = "MY-C0004"
	fixtureStore             = "MY-C0008"
	fixtureErrorUnsupported  = "-9"
	fixtureErrorFailed       = "-1"
)

// Delete replies differ by model: some echo the request, some broadcast an
// uncorrelated image_deleted event, and some send nothing.
const (
	deleteReplyEvent = "event"
	deleteReplyEcho  = "echo"
	deleteReplyNone  = "none"
)

type d2dCapture struct {
	header  map[string]any
	payload []byte
}

type protocolTVFixture struct {
	server         *httptest.Server
	d2dListener    net.Listener
	d2dCapture     chan d2dCapture
	d2dDone        chan error
	d2dErr         chan error
	remoteCommands chan string

	stateMu sync.Mutex
	// legacyAPI makes the fixture behave like art API 2.03 (2021 Frames).
	legacyAPI       bool
	securedD2D      bool
	powerState      string
	restDown        bool
	artMode         string
	inventory       []string
	current         string
	deleteMutations []string
	deleteReply     string
	slideshow       SlideshowSetting
	slideshowWrites []SlideshowSetting
	brightness      int
	brightnessSets  []string
	imageAddedError string
	foreignAdded    bool
	nextUpload      int
	pairings        int
	requests        []map[string]any
	powerOffEffect  func(*protocolTVFixture)
}

func newProtocolTVFixture(t *testing.T) *protocolTVFixture {
	t.Helper()
	return newConfiguredTVFixture(t, func(*protocolTVFixture) {})
}

// newConfiguredTVFixture starts the fake TV after configure has set the
// behavior that must be fixed before the first connection.
func newConfiguredTVFixture(t *testing.T, configure func(*protocolTVFixture)) *protocolTVFixture {
	t.Helper()
	fixture := &protocolTVFixture{
		d2dCapture:     make(chan d2dCapture, 8),
		d2dDone:        make(chan error, 8),
		d2dErr:         make(chan error, 8),
		remoteCommands: make(chan string, 8),
		powerState:     stringOn,
		artMode:        stringOn,
		inventory:      []string{"art-1"},
		current:        "art-1",
		deleteReply:    deleteReplyEvent,
		slideshow:      SlideshowSetting{Interval: 30, Kind: SlideshowShuffle, Category: userArtCategory},
		brightness:     4,
		nextUpload:     20,
		powerOffEffect: func(f *protocolTVFixture) { f.powerState = stringStandby; f.artMode = stringOff },
	}
	configure(fixture)
	fixture.server = httptest.NewUnstartedServer(http.HandlerFunc(fixture.serveHTTP))
	fixture.server.StartTLS()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for D2D upload: %v", err)
	}
	if fixture.securedD2D {
		listener = tls.NewListener(listener, &tls.Config{
			Certificates: fixture.server.TLS.Certificates, MinVersion: tls.VersionTLS12,
		})
	}
	fixture.d2dListener = listener
	go fixture.acceptD2DUploads()
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

func (f *protocolTVFixture) acceptD2DUploads() {
	for {
		conn, err := f.d2dListener.Accept()
		if err != nil {
			return
		}
		capture, err := readD2DUpload(conn)
		_ = conn.Close()
		if err != nil {
			f.d2dErr <- err
			f.d2dDone <- err
			continue
		}
		f.d2dCapture <- capture
		f.d2dDone <- nil
	}
}

func readD2DUpload(conn net.Conn) (d2dCapture, error) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	lengthBytes := make([]byte, 4)
	if _, err := io.ReadFull(conn, lengthBytes); err != nil {
		return d2dCapture{}, fmt.Errorf("read D2D header length: %w", err)
	}
	headerLength := binary.BigEndian.Uint32(lengthBytes)
	if headerLength == 0 || headerLength > 1<<20 {
		return d2dCapture{}, fmt.Errorf("invalid D2D header length %d", headerLength)
	}
	headerBytes := make([]byte, headerLength)
	if _, err := io.ReadFull(conn, headerBytes); err != nil {
		return d2dCapture{}, fmt.Errorf("read D2D header: %w", err)
	}
	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return d2dCapture{}, fmt.Errorf("parse D2D header: %w", err)
	}
	fileLength, ok := header["fileLength"].(float64)
	if !ok || fileLength < 0 || fileLength > 1<<20 {
		return d2dCapture{}, fmt.Errorf("invalid D2D file length %v", header["fileLength"])
	}
	payload := make([]byte, int(fileLength))
	if _, err := io.ReadFull(conn, payload); err != nil {
		return d2dCapture{}, fmt.Errorf("read D2D payload: %w", err)
	}
	return d2dCapture{header: header, payload: payload}, nil
}

func (f *protocolTVFixture) serveHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/api/v2/" {
		f.serveDeviceInfo(w)
		return
	}
	f.stateMu.Lock()
	power := f.powerState
	f.stateMu.Unlock()
	if power != stringOn {
		// Network standby keeps REST up but refuses the websocket channels.
		http.Error(w, "standby", http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(w, request, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	ctx := request.Context()
	// Real TVs interleave broadcasts with the handshake.
	if writeProtocolFixtureJSON(ctx, conn, map[string]any{
		keyEvent: "ed.edenTV.update", keyData: map[string]string{"update_type": "ed.edenApp.update"},
	}) != nil {
		return
	}
	if writeProtocolFixtureJSON(ctx, conn, map[string]any{
		keyEvent: eventChannelConnect, keyData: map[string]any{"token": json.Number(fixtureToken)},
	}) != nil {
		return
	}
	if strings.Contains(request.URL.Path, endpointRemoteControl) {
		f.stateMu.Lock()
		f.pairings++
		f.stateMu.Unlock()
		f.serveRemoteControl(ctx, conn)
		return
	}
	if writeProtocolFixtureJSON(ctx, conn, map[string]any{keyEvent: "ms.voiceApp.hide"}) != nil {
		return
	}
	if writeProtocolFixtureJSON(ctx, conn, map[string]any{keyEvent: eventChannelReady, keyData: map[string]any{}}) != nil {
		return
	}
	f.serveArtApp(ctx, conn)
}

func (f *protocolTVFixture) serveDeviceInfo(w http.ResponseWriter) {
	f.stateMu.Lock()
	power, down := f.powerState, f.restDown
	f.stateMu.Unlock()
	if down {
		// Deep standby: the TV stops answering, which the client sees as a
		// dropped connection.
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"device": map[string]string{
			"FrameTVSupport": stringTrue, "PowerState": power, "duid": "uuid:fixture-frame",
			"firmwareVersion": "Unknown", "id": "uuid:fixture-frame", "model": "22_PONTUSM_FTV",
			"modelName": "QN43LS03BAFXZC", "wifiMac": "aa:bb:cc:dd:ee:ff",
		},
		"isSupport": `{"FrameTVSupport": "true"}`, "type": "Samsung SmartTV", "version": "2.0.25",
	})
}

func (f *protocolTVFixture) serveRemoteControl(ctx context.Context, conn *websocket.Conn) {
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var envelope struct {
			Params struct {
				Command string `json:"Cmd"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			continue
		}
		f.remoteCommands <- envelope.Params.Command
		if envelope.Params.Command == "Release" {
			f.stateMu.Lock()
			f.powerOffEffect(f)
			f.stateMu.Unlock()
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
		responses, upload := f.answer(request)
		for _, response := range responses {
			if writeProtocolD2DResponse(ctx, conn, response) != nil {
				return
			}
		}
		if upload && !f.finishUpload(ctx, conn, request) {
			return
		}
	}
}

// answer returns the replies to one art request, or none when the TV ignores
// it, and whether a D2D upload follows.
//
//nolint:gocyclo // One flat switch mirrors the art API surface.
func (f *protocolTVFixture) answer(request map[string]any) ([]map[string]any, bool) {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	f.requests = append(f.requests, request)
	name := fmt.Sprint(request[keyRequest])
	reply := f.reply(request, name)
	fail := func(code string) []map[string]any { return []map[string]any{f.errorReply(request, code)} }
	if f.powerState != stringOn && name != keyGetArtModeStatus {
		return nil, false
	}
	switch name {
	case keyGetArtModeStatus:
		reply[keyValue] = f.artMode
	case requestAPIVersion:
		if f.legacyAPI {
			return fail(fixtureErrorUnsupported), false
		}
		reply["version"] = "4.3.4.0"
	case requestLegacyAPIVersion:
		if !f.legacyAPI {
			return fail(fixtureErrorUnsupported), false
		}
		reply["version"] = "2.03"
	case keyGetContentList:
		reply["content_list"] = f.contentList()
	case requestCurrentArtwork:
		reply[keyContentID] = f.current
		reply[keyCategoryID] = userArtCategory
	case "select_image":
		contentID := fmt.Sprint(request[keyContentID])
		if !slices.Contains(f.inventory, contentID) {
			return fail(fixtureErrorFailed), false
		}
		f.current = contentID
	case requestArtModeSettings:
		if f.legacyAPI {
			return fail(fixtureErrorUnsupported), false
		}
		settings, _ := json.Marshal([]map[string]string{
			{"item": "brightness", "value": strconv.Itoa(f.brightness), "min": "0", "max": "10"},
			{"item": "color_temperature", "value": "3", "min": "-5", "max": "5"},
			{"item": "brightness_sensor_setting", "value": stringOff},
		})
		reply[keyData] = string(settings)
	case requestGetBrightness:
		if !f.legacyAPI {
			// Art API 4.x only answers get_artmode_settings.
			return nil, false
		}
		reply[keyValue], reply["min"], reply["max"] = strconv.Itoa(f.brightness), "0", "10"
	case "set_brightness":
		value, isString := request[keyValue].(string)
		level, err := strconv.Atoi(value)
		if !isString || err != nil || level < 0 || level > 10 {
			return fail(fixtureErrorFailed), false
		}
		f.brightness = level
		f.brightnessSets = append(f.brightnessSets, value)
	case testProtocolGetSlideshow, "get_auto_rotation_status":
		if (name == testProtocolGetSlideshow) == f.legacyAPI {
			return fail(fixtureErrorUnsupported), false
		}
		reply[keyValue] = strconv.Itoa(f.slideshow.Interval)
		if f.slideshow.Interval == 0 {
			reply[keyValue] = stringOff
		}
		reply["type"] = string(f.slideshow.Kind)
		reply[keyCategoryID] = f.slideshow.Category
	case "set_slideshow_status", "set_auto_rotation_status":
		if (name == "set_slideshow_status") == f.legacyAPI {
			return fail(fixtureErrorUnsupported), false
		}
		setting, err := parseSlideshowSetting(fmt.Sprint(request[keyValue]), fmt.Sprint(request["type"]),
			fmt.Sprint(request[keyCategoryID]))
		if err != nil {
			return fail(fixtureErrorFailed), false
		}
		f.slideshow = setting
		f.slideshowWrites = append(f.slideshowWrites, setting)
	case testProtocolDeleteImage:
		contentIDs := protocolFixtureContentIDs(request["content_id_list"])
		if slices.Contains(contentIDs, "storage-full") {
			return fail("507"), false
		}
		f.deleteContent(contentIDs)
		switch f.deleteReply {
		case deleteReplyNone:
			return nil, false
		case deleteReplyEvent:
			return []map[string]any{{keyEvent: eventImageDeleted, "content_id": contentIDs}}, false
		}
	case requestMatteList:
		reply["matte_type_list"] = `[{"matte_type": "none"}, {"matte_type": "modern"}, {"matte_type": "shadowbox"}]`
		reply["matte_color_list"] = `[{"color": "black", "R": 34, "G": 34, "B": 33}, {"color": "polar", "R": 232, "G": 230, "B": 231}]`
	case requestSendImage:
		connInfo, ok := request["conn_info"].(map[string]any)
		if !ok || fmt.Sprint(request["id"]) != fmt.Sprint(connInfo["id"]) ||
			fmt.Sprint(request[keyRequestID]) != fmt.Sprint(connInfo["id"]) {
			return fail(fixtureErrorFailed), false
		}
		port := f.d2dListener.Addr().(*net.TCPAddr).Port
		encoded, _ := json.Marshal(map[string]any{
			"ip": "127.0.0.1", "port": strconv.Itoa(port), "key": "fixture-key",
			"stat": "ok", "mode": "socket", "secured": f.securedD2D,
		})
		reply["conn_info"] = string(encoded)
		return []map[string]any{reply}, true
	default:
		return fail(fixtureErrorUnsupported), false
	}
	return []map[string]any{reply}, false
}

// reply starts a correlated answer in the request's art API generation.
func (f *protocolTVFixture) reply(request map[string]any, name string) map[string]any {
	if f.legacyAPI {
		return map[string]any{
			"id": request["id"], keyEvent: strings.TrimPrefix(name, "get_"), "target_client_id": "fixture-client",
		}
	}
	return map[string]any{keyRequestID: request[keyRequestID], keyEvent: name}
}

// errorReply is the real-TV error envelope: a string code and the failed
// request echoed as an encoded string, correlated only by id.
func (f *protocolTVFixture) errorReply(request map[string]any, code string) map[string]any {
	echoed, _ := json.Marshal(request)
	return map[string]any{
		"id": request["id"], keyEvent: "error", "request_data": string(echoed),
		"error_code": code, "target_client_id": "fixture-client",
	}
}

func (f *protocolTVFixture) finishUpload(ctx context.Context, conn *websocket.Conn, request map[string]any) bool {
	select {
	case err := <-f.d2dDone:
		if err != nil {
			return true
		}
	case <-ctx.Done():
		return false
	}
	f.stateMu.Lock()
	events := make([]map[string]any, 0, 2)
	if f.foreignAdded {
		// Another client's upload finishing at the same moment.
		foreign := map[string]any{keyEvent: eventImageAdded, keyContentID: "MY_F9999", "id": "another-client"}
		if !f.legacyAPI {
			foreign = map[string]any{keyEvent: eventImageAdded, keyContentID: f.inventory[0]}
		}
		events = append(events, foreign)
	}
	added := map[string]any{keyEvent: eventImageAdded}
	if f.legacyAPI {
		added["id"] = request["id"]
	}
	if f.imageAddedError != "" {
		added["error_code"] = f.imageAddedError
	} else {
		contentID := fmt.Sprintf("MY_F%04d", f.nextUpload)
		f.nextUpload++
		f.inventory = append(f.inventory, contentID)
		added[keyContentID] = contentID
	}
	events = append(events, added)
	f.stateMu.Unlock()
	for _, event := range events {
		if writeProtocolD2DResponse(ctx, conn, event) != nil {
			return false
		}
	}
	return true
}

// contentList returns user art mixed with favorites, which repeat user
// content IDs, and Art Store items, as real TVs do.
func (f *protocolTVFixture) contentList() string {
	content := make([]map[string]any, 0, 2*len(f.inventory)+1)
	for _, contentID := range f.inventory {
		content = append(content, map[string]any{
			keyCategoryID: userArtCategory, keyContentID: contentID, "matte_id": "none", "width": 3840, "height": 2160,
		})
	}
	for _, contentID := range f.inventory {
		content = append(content, map[string]any{keyCategoryID: fixtureFavorites, keyContentID: contentID})
	}
	content = append(content, map[string]any{keyCategoryID: fixtureStore, keyContentID: "SAM-S0397"})
	encoded, _ := json.Marshal(content)
	return string(encoded)
}

func (f *protocolTVFixture) deleteContent(contentIDs []string) {
	for _, contentID := range contentIDs {
		f.deleteMutations = append(f.deleteMutations, contentID)
		f.inventory = slices.DeleteFunc(f.inventory, func(existing string) bool { return existing == contentID })
	}
}

func (f *protocolTVFixture) slideshowMutations() []SlideshowSetting {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return slices.Clone(f.slideshowWrites)
}

func (f *protocolTVFixture) requestsNamed(name string) []map[string]any {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	var matched []map[string]any
	for _, request := range f.requests {
		if request[keyRequest] == name {
			matched = append(matched, request)
		}
	}
	return matched
}

func (f *protocolTVFixture) pairingCount() int {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return f.pairings
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

// writeProtocolD2DResponse wraps a reply the way real TVs do: the inner data
// is a JSON-encoded string inside a d2d_service_message event.
func writeProtocolD2DResponse(ctx context.Context, conn *websocket.Conn, response map[string]any) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return writeProtocolFixtureJSON(ctx, conn, map[string]any{
		keyEvent: eventD2DServiceMessage, keyData: string(raw), "from": "fixture-host",
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
	return slices.Clone(f.deleteMutations)
}

// Inventory returns the fixture's current user-art content IDs.
func (f *protocolTVFixture) Inventory() []string {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return slices.Clone(f.inventory)
}

// SlideshowSetting returns the fixture's current typed slideshow state.
func (f *protocolTVFixture) SlideshowSetting() SlideshowSetting {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return f.slideshow
}

// SlideshowMutations returns typed slideshow writes observed on the wire.
func (f *protocolTVFixture) SlideshowMutations() []SlideshowSetting {
	return f.slideshowMutations()
}
