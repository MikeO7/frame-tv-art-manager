package samsung

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseUploadedContentRequiresExplicitValidInventory(t *testing.T) {
	tests := []struct {
		name    string
		raw     json.RawMessage
		wantIDs []string
	}{
		{name: "absent"},
		{name: "null", raw: json.RawMessage(`null`)},
		{name: "blank string", raw: json.RawMessage(`"   "`)},
		{name: "malformed", raw: json.RawMessage(`[{`)},
		{name: "object", raw: json.RawMessage(`{}`)},
		{name: "blank id", raw: json.RawMessage(`[{"content_id":" ","category_id":"MY-C0002"}]`)},
		{name: "wrong category", raw: json.RawMessage(`[{"content_id":"id-1","category_id":"SAM-C0001"}]`)},
		{name: "duplicate id", raw: json.RawMessage(`[{"content_id":"id-1","category_id":"MY-C0002"},{"content_id":"id-1","category_id":"MY-C0002"}]`)},
		{name: "explicit empty array", raw: json.RawMessage(`[]`), wantIDs: []string{}},
		{name: "string encoded array", raw: json.RawMessage(`"[{\"content_id\":\" id-1 \",\"category_id\":\"MY-C0002\"}]"`), wantIDs: []string{"id-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content, err := parseUploadedContent(test.raw)
			if test.wantIDs == nil {
				if err == nil {
					t.Fatalf("parseUploadedContent() = %+v, want error", content)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUploadedContent() error = %v", err)
			}
			if len(content) != len(test.wantIDs) {
				t.Fatalf("content = %+v, want IDs %v", content, test.wantIDs)
			}
			for index, wantID := range test.wantIDs {
				if content[index].ContentID != wantID {
					t.Fatalf("content[%d].ContentID = %q, want %q", index, content[index].ContentID, wantID)
				}
			}
		})
	}
}

func TestValidateFrameTVDeviceFailsClosed(t *testing.T) {
	valid := DeviceInfo{ModelName: "QN55LS03D", FrameTVSupport: stringTrue, PowerState: stringOn}
	tests := []struct {
		name   string
		mutate func(*DeviceInfo)
	}{
		{name: "missing model", mutate: func(info *DeviceInfo) { info.ModelName = " " }},
		{name: "support absent", mutate: func(info *DeviceInfo) { info.FrameTVSupport = "" }},
		{name: "support false", mutate: func(info *DeviceInfo) { info.FrameTVSupport = stringFalse }},
		{name: "power unknown", mutate: func(info *DeviceInfo) { info.PowerState = "sleeping" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := valid
			test.mutate(&info)
			if err := validateFrameTVDevice(info); err == nil {
				t.Fatal("validateFrameTVDevice() error = nil")
			}
		})
	}
	valid.PowerState = stringOff
	if err := validateFrameTVDevice(valid); err != nil {
		t.Fatalf("validateFrameTVDevice(power off) error = %v", err)
	}
	valid.PowerState = "standby"
	if err := validateFrameTVDevice(valid); err != nil {
		t.Fatalf("validateFrameTVDevice(standby) error = %v", err)
	}
}

func TestAdapterRejectsUnverifiedFrameTVIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DeviceInfo)
	}{
		{name: "unsupported", mutate: func(info *DeviceInfo) { info.FrameTVSupport = stringFalse }},
		{name: "missing model", mutate: func(info *DeviceInfo) { info.ModelName = "" }},
		{name: "unknown power", mutate: func(info *DeviceInfo) { info.PowerState = "unknown" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := eligibleObservationTransport(json.RawMessage(`[]`))
			test.mutate(&transport.device)
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
			observation, err := adapter.Observe(context.Background(), ObserveRequest{
				CycleID:              "cycle",
				CollectionGeneration: "generation",
			})
			if err == nil {
				t.Fatal("Observe() error = nil")
			}
			if observation.Disposition != DispositionUnsafeUnknown || !observation.Authorization.isZero() {
				t.Fatalf("observation = %+v, want unsafe and unauthorized", observation)
			}
			if transport.artModeCalls != 0 || transport.inventoryCalls != 0 {
				t.Fatalf("downstream calls = art %d inventory %d, want zero", transport.artModeCalls, transport.inventoryCalls)
			}
		})
	}
}

func TestWaitForImageAddedTimesOut(t *testing.T) {
	events := make(chan json.RawMessage)
	if _, err := waitForImageAdded(context.Background(), events, time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("wait() error = %v, want ErrTimeout", err)
	}
}

func TestProtocolTransportConcurrentSendAndClose(t *testing.T) {
	transport := &protocolTransport{
		config: Config{RequestTimeout: time.Millisecond},
		logger: slog.New(slog.DiscardHandler),
	}
	const iterations = 200
	for range iterations {
		transport.mu.Lock()
		transport.conn = newConnection(connConfig{logger: slog.New(slog.DiscardHandler)})
		transport.mu.Unlock()
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			_ = transport.Close(context.Background())
		}()
		go func() {
			defer group.Done()
			_, _ = transport.send(context.Background(), keyGetArtModeStatus, nil)
		}()
		group.Wait()
	}
}

type closeTrackingRoundTripper struct {
	closed atomic.Int32
}

func (*closeTrackingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network access is not expected")
}

func (t *closeTrackingRoundTripper) CloseIdleConnections() {
	t.closed.Add(1)
}

func TestProtocolTransportOwnsReusableHTTPClientsAndClosesIdleConnections(t *testing.T) {
	t.Parallel()

	device, gate, websocket := &closeTrackingRoundTripper{}, &closeTrackingRoundTripper{}, &closeTrackingRoundTripper{}
	transport := &protocolTransport{
		config:              Config{RequestTimeout: time.Second, GateTimeout: time.Second},
		logger:              slog.New(slog.DiscardHandler),
		deviceHTTPClient:    &http.Client{Transport: device},
		gateHTTPClient:      &http.Client{Transport: gate},
		websocketHTTPClient: &http.Client{Transport: websocket},
	}
	if first, second := transport.cachedDeviceHTTPClient(), transport.cachedDeviceHTTPClient(); first != second {
		t.Fatal("DeviceInfo HTTP client was recreated instead of reused")
	}
	if err := transport.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if device.closed.Load() != 1 || gate.closed.Load() != 1 || websocket.closed.Load() != 1 {
		t.Fatalf("idle close calls = device:%d gate:%d websocket:%d",
			device.closed.Load(), gate.closed.Load(), websocket.closed.Load())
	}
}

func TestProtocolTransportQuietGateDoesNotDeadlockWhileConnecting(t *testing.T) {
	t.Parallel()
	gate := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("blocked")),
		}, nil
	})}
	transport := &protocolTransport{
		config: Config{
			Address: "192.0.2.10", QuietGate: true, GateTimeout: time.Second,
			ConnectTimeout: time.Second, RequestTimeout: time.Second,
		},
		logger:         slog.New(slog.DiscardHandler),
		gateHTTPClient: gate,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := transport.Connect(ctx, false); !errors.Is(err, ErrGateFailed) {
		t.Fatalf("Connect() error = %v, want quiet-gate block", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestSamsungLogsOmitNetworkIdentifiers(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	transport := newProtocolTransport(Config{
		Address: "192.0.2.44",
		MAC:     []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff},
	}, Dependencies{Logger: logger}).(*protocolTransport)
	transport.logger.Info("safe event")
	if bytes.Contains(output.Bytes(), []byte("192.0.2.44")) ||
		bytes.Contains(output.Bytes(), []byte("aa:bb:cc:dd:ee:ff")) {
		t.Fatalf("Samsung log leaked a network identifier: %s", output.String())
	}
}
