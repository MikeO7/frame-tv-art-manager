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
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// newWebSocketTV starts a TLS WebSocket server that writes the handshake
// frames, runs serve, and then holds the socket until the client closes it so
// tests never depend on sleeps for message delivery.
func newWebSocketTV(t *testing.T, handshake [][]byte, serve func(context.Context, *websocket.Conn)) (string, int) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for _, frame := range handshake {
			if err := conn.Write(r.Context(), websocket.MessageText, frame); err != nil {
				return
			}
		}
		if serve != nil {
			serve(r.Context(), conn)
		}
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return parsed.Hostname(), port
}

func wsFrame(t *testing.T, event string, data any) []byte {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("encode frame data: %v", err)
	}
	frame, err := json.Marshal(wsResponse{Event: event, Data: raw})
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	return frame
}

func newTestConnection(t *testing.T, host string, port int, endpoint string) *connection {
	t.Helper()
	return newConnection(connConfig{
		host: host, port: port, endpoint: endpoint, name: "TestClient",
		tokenFile: filepath.Join(secureTokenDirectory(t), "token.txt"),
		timeout:   2 * time.Second, skipTLSVerify: true, logger: slog.New(slog.DiscardHandler),
	})
}

func TestConnectionOpenPersistsTokenAndCorrelatesResponses(t *testing.T) {
	t.Parallel()
	handshake := [][]byte{
		wsFrame(t, eventChannelConnect, map[string]string{"token": "test-token-123"}),
		wsFrame(t, eventChannelReady, map[string]string{}),
	}
	host, port := newWebSocketTV(t, handshake, func(ctx context.Context, conn *websocket.Conn) {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		request, err := parseProtocolFixtureRequest(raw)
		if err != nil {
			t.Errorf("parse art request: %v", err)
			return
		}
		inner, err := json.Marshal(map[string]any{"request_id": request[keyRequestID], keyValue: stringOn})
		if err != nil {
			t.Errorf("encode response: %v", err)
			return
		}
		frames := [][]byte{
			[]byte("not json"),
			wsFrame(t, eventD2DServiceMessage, map[string]string{"request_id": "someone-else", keyValue: stringOff}),
			wsFrame(t, eventD2DServiceMessageEvent, string(inner)),
		}
		for _, frame := range frames {
			if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
				return
			}
		}
	})
	conn := newTestConnection(t, host, port, endpointArtApp)
	ctx := context.Background()

	if err := conn.Open(ctx); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := conn.Open(ctx); err != nil || !conn.IsAlive() {
		t.Fatalf("second Open() = %v, alive %t; want idempotent live connection", err, conn.IsAlive())
	}
	token, err := os.ReadFile(conn.tokenFile)
	if err != nil || string(token) != "test-token-123" {
		t.Fatalf("persisted token = %q, %v", token, err)
	}
	if info, err := os.Stat(conn.tokenFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %v, %v; want 0600", info, err)
	}

	requestID := newRequestID()
	payload, err := artAppRequest(map[string]any{keyRequest: keyGetArtModeStatus, keyRequestID: requestID})
	if err != nil {
		t.Fatalf("artAppRequest() error = %v", err)
	}
	response, err := conn.SendAndWait(ctx, payload, requestID, 2*time.Second)
	if err != nil {
		t.Fatalf("SendAndWait() error = %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(response, &decoded); err != nil || decoded[keyValue] != stringOn || decoded[keyRequestID] != requestID {
		t.Fatalf("SendAndWait() = %s, %v; want the string-encoded response for %s", response, err, requestID)
	}

	if err := conn.CloseContext(ctx); err != nil {
		t.Fatalf("CloseContext() error = %v", err)
	}
	if conn.IsAlive() {
		t.Fatal("connection is alive after CloseContext()")
	}
	if err := conn.Send(ctx, payload); !errors.Is(err, errNotConnected) {
		t.Fatalf("Send() after close error = %v, want ErrNotConnected", err)
	}
	if err := conn.CloseContext(ctx); err != nil {
		t.Fatalf("second CloseContext() error = %v", err)
	}
}

func unrelatedEvents(t *testing.T, count int) [][]byte {
	t.Helper()
	frames := make([][]byte, 0, count)
	for index := range count {
		event := "ed.edenTV.update"
		if index%2 == 1 {
			event = "ms.voiceApp.hide"
		}
		frames = append(frames, wsFrame(t, event, map[string]string{}))
	}
	return frames
}

func TestConnectionOpenSkipsUnrelatedHandshakeEvents(t *testing.T) {
	t.Parallel()
	handshake := append(unrelatedEvents(t, 2), wsFrame(t, eventChannelConnect, map[string]any{"token": 123456789}))
	handshake = append(handshake, unrelatedEvents(t, 3)...)
	handshake = append(handshake, wsFrame(t, eventChannelReady, map[string]string{}))
	host, port := newWebSocketTV(t, handshake, nil)
	conn := newTestConnection(t, host, port, endpointArtApp)
	if err := conn.Open(context.Background()); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseContext(context.Background()) })
	token, err := loadAuthenticationToken(conn.tokenFile)
	if err != nil || token != "123456789" {
		t.Fatalf("persisted token = %q, %v; want numeric token as text", token, err)
	}
}

func TestConnectionOpenRejectsInvalidHandshakes(t *testing.T) {
	t.Parallel()
	connect := func(t *testing.T) []byte {
		return wsFrame(t, eventChannelConnect, map[string]string{})
	}
	tests := []struct {
		name      string
		endpoint  string
		handshake func(*testing.T) [][]byte
		wantErr   error
		wantText  string
	}{
		{
			name: "unauthorized", endpoint: endpointRemoteControl,
			handshake: func(t *testing.T) [][]byte { return [][]byte{wsFrame(t, "ms.channel.unauthorized", nil)} },
			wantErr:   errUnauthorized,
		},
		{
			name: "pairing timeout", endpoint: endpointRemoteControl,
			handshake: func(t *testing.T) [][]byte { return [][]byte{wsFrame(t, "ms.channel.timeOut", nil)} },
			wantErr:   errTimeout,
		},
		{
			name: "only unrelated events", endpoint: endpointRemoteControl,
			handshake: func(t *testing.T) [][]byte { return unrelatedEvents(t, maxHandshakeEvents) },
			wantErr:   errConnectionFailure,
		},
		{
			name: "malformed handshake", endpoint: endpointRemoteControl,
			handshake: func(*testing.T) [][]byte { return [][]byte{[]byte("{")} },
			wantText:  "parse handshake",
		},
		{
			name: "art app without ready", endpoint: endpointArtApp,
			handshake: func(t *testing.T) [][]byte {
				return append([][]byte{connect(t)}, unrelatedEvents(t, maxHandshakeEvents)...)
			},
			wantErr: errConnectionFailure,
		},
		{
			name: "art app malformed ready", endpoint: endpointArtApp,
			handshake: func(t *testing.T) [][]byte { return [][]byte{connect(t), []byte("[")} },
			wantText:  "parse channel ready",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			host, port := newWebSocketTV(t, test.handshake(t), nil)
			conn := newTestConnection(t, host, port, test.endpoint)
			err := conn.Open(context.Background())
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("Open() error = %v, want %v", err, test.wantErr)
			}
			if test.wantText != "" && (err == nil || !strings.Contains(err.Error(), test.wantText)) {
				t.Fatalf("Open() error = %v, want %q", err, test.wantText)
			}
			if conn.IsAlive() {
				t.Fatal("rejected handshake left the connection alive")
			}
			if _, err := os.Stat(conn.tokenFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected handshake persisted a token: %v", err)
			}
		})
	}
}

func TestConnectionOpenFailsWhenTVUnreachable(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	conn := newTestConnection(t, "127.0.0.1", port, endpointArtApp)
	if err := conn.Open(context.Background()); err == nil || conn.IsAlive() {
		t.Fatalf("Open() to closed port = %v, alive %t", err, conn.IsAlive())
	}
}

func TestConnectionPendingRequestsFailWhenUnansweredOrClosed(t *testing.T) {
	t.Parallel()
	handshake := [][]byte{wsFrame(t, eventChannelConnect, map[string]string{})}
	host, port := newWebSocketTV(t, handshake, nil)
	conn := newTestConnection(t, host, port, endpointRemoteControl)
	if err := conn.Open(context.Background()); err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if _, err := conn.SendAndWait(context.Background(), []byte(`{}`), "unanswered", 10*time.Millisecond); !errors.Is(err, errTimeout) {
		t.Fatalf("SendAndWait() error = %v, want ErrTimeout", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conn.SendAndWait(canceled, []byte(`{}`), "canceled", time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("SendAndWait(canceled) error = %v, want context.Canceled", err)
	}

	// A canceled write closes its socket, so the listener checks use a
	// connection of their own.
	conn = newTestConnection(t, host, port, endpointRemoteControl)
	if err := conn.Open(context.Background()); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	pending, cleanup, err := conn.registerPending("in-flight")
	if err != nil {
		t.Fatalf("registerPending() error = %v", err)
	}
	defer cleanup()
	if _, _, err := conn.registerPending("in-flight"); err == nil {
		t.Fatal("registerPending() accepted a duplicate listener")
	}
	if err := conn.CloseContext(context.Background()); err != nil {
		t.Fatalf("CloseContext() error = %v", err)
	}
	if _, open := <-pending; open {
		t.Fatal("CloseContext() left a pending listener open")
	}
}

func TestDialFailureDoesNotExposeTheToken(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	connection := newConnection(connConfig{timeout: time.Second, logger: slog.New(slog.DiscardHandler)})
	_, err = connection.dial(context.Background(), "ws://"+address+"/api/v2/channels/art?token=paired-secret")
	if err == nil {
		t.Fatal("dial to a closed port succeeded")
	}
	if strings.Contains(err.Error(), "paired-secret") || strings.Contains(err.Error(), "token=") {
		t.Fatalf("dial error exposes the token: %v", err)
	}
	if !unreachableError(err) {
		t.Fatalf("dial error = %v, want it still classified as unreachable", err)
	}
}

func TestConnectionFormatURLBracketsIPv6Host(t *testing.T) {
	t.Parallel()
	connection := newConnection(connConfig{
		host: "2001:db8::1", port: portArtWSS, endpoint: endpointArtApp, name: "Frame Manager",
	})
	parsed, err := url.Parse(connection.formatURL("token"))
	if err != nil {
		t.Fatalf("parse formatted URL: %v", err)
	}
	if parsed.Host != "[2001:db8::1]:8002" || parsed.Hostname() != "2001:db8::1" ||
		parsed.Path != "/api/v2/channels/"+endpointArtApp || parsed.Query().Get("token") != "token" {
		t.Fatalf("formatted URL = %s", parsed)
	}
}

func TestArtAppRequestWrapsJSONEncodedPayload(t *testing.T) {
	t.Parallel()
	b, err := artAppRequest(map[string]any{"hello": "world"})
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Method string `json:"method"`
		Params struct {
			Event string `json:"event"`
			To    string `json:"to"`
			Data  string `json:"data"`
		} `json:"params"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Method != "ms.channel.emit" || envelope.Params.Event != "art_app_request" ||
		envelope.Params.To != "host" || envelope.Params.Data != `{"hello":"world"}` {
		t.Fatalf("art request envelope = %+v", envelope)
	}
}

func TestRequestIDsAreUniqueVersionFourUUIDs(t *testing.T) {
	t.Parallel()
	ids := []string{newRequestID(), newRequestID(), requestIDFrom(errorReader{}), requestIDFrom(errorReader{})}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if len(id) != 36 || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
			t.Fatalf("request ID %q is not a version 4 UUID", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate request ID %q in %v", id, ids)
		}
		seen[id] = struct{}{}
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

func TestExtractAndSaveTokenIgnoresEmptyTokensAndReportsPersistenceFailure(t *testing.T) {
	t.Parallel()
	c := newConnection(connConfig{tokenFile: filepath.Join(t.TempDir(), "token.txt"), logger: slog.New(slog.DiscardHandler)})
	for _, payload := range []string{`{`, `{"token":""}`} {
		if err := c.extractAndSaveToken(context.Background(), json.RawMessage(payload)); err != nil {
			t.Fatalf("extractAndSaveToken(%s) error = %v", payload, err)
		}
	}
	if _, err := os.Stat(c.tokenFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid or empty tokens created a file: %v", err)
	}

	blocked := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.tokenFile = filepath.Join(blocked, "token.txt")
	if err := c.extractAndSaveToken(context.Background(), json.RawMessage(`{"token":"abcdefghijk"}`)); err == nil {
		t.Fatal("blocked token path error = nil")
	}
}

func TestConnectionDeadSocketFailsWaitersAndReportsDead(t *testing.T) {
	t.Parallel()
	handshake := [][]byte{
		wsFrame(t, eventChannelConnect, map[string]string{"token": "token"}),
		wsFrame(t, eventChannelReady, map[string]string{}),
	}
	drop := make(chan struct{})
	host, port := newWebSocketTV(t, handshake, func(_ context.Context, conn *websocket.Conn) {
		<-drop
		_ = conn.CloseNow()
	})
	conn := newTestConnection(t, host, port, endpointArtApp)
	if err := conn.Open(context.Background()); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseContext(context.Background()) })
	waiter, cleanup, err := conn.registerPending("request-1")
	if err != nil {
		t.Fatalf("registerPending() error = %v", err)
	}
	defer cleanup()
	close(drop)
	select {
	case _, ok := <-waiter:
		if ok {
			t.Fatal("waiter received a reply from a dead socket")
		}
	case <-time.After(time.Second):
		t.Fatal("waiter was not failed when the socket died")
	}
	if conn.IsAlive() {
		t.Fatal("dead socket still reported alive")
	}
	if _, _, err := conn.registerPending("request-2"); !errors.Is(err, errNotConnected) {
		t.Fatalf("registerPending() after death error = %v, want ErrNotConnected", err)
	}
}

// TestRouteD2DEventKeepsEventsQueuedBeforeTheWaiterReads covers a foreign
// image_added arriving just before ours while the upload is still sending
// bytes: both must reach the waiter so ours is not dropped.
func TestRouteD2DEventKeepsEventsQueuedBeforeTheWaiterReads(t *testing.T) {
	t.Parallel()
	conn := &connection{connConfig: connConfig{logger: slog.New(slog.DiscardHandler)}}
	events, cleanup, err := conn.registerPending(eventImageAdded)
	if err != nil {
		t.Fatalf("registerPending() error = %v", err)
	}
	defer cleanup()
	for _, contentID := range []string{"MY_F9999", "MY_F0020"} {
		data, err := json.Marshal(map[string]string{"event": eventImageAdded, "content_id": contentID})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		conn.routeD2DEvent(data)
	}
	for _, want := range []string{"MY_F9999", "MY_F0020"} {
		select {
		case data := <-events:
			if !strings.Contains(string(data), want) {
				t.Fatalf("event = %s, want content ID %s", data, want)
			}
		default:
			t.Fatalf("event for %s was dropped", want)
		}
	}
}
