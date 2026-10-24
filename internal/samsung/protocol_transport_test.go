package samsung

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func protocolErrorOutcome(err error) Outcome {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Outcome
	}
	return OutcomeNotAttempted
}

func connectFixtureTransport(t *testing.T, fixture *protocolTVFixture, tokenPath string) *protocolTransport {
	t.Helper()
	transport := newProtocolTransport(Config{
		Address: fixture.address(t), ClientName: "protocol-fixture", TokenPath: tokenPath,
		ConnectTimeout: 2 * time.Second, RequestTimeout: 2 * time.Second,
	}, Dependencies{Logger: slog.New(slog.DiscardHandler)}).(*protocolTransport)
	if err := transport.Connect(context.Background(), false); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = transport.Close(context.Background()) })
	return transport
}

func uploadFixtureImage(t *testing.T, transport *protocolTransport, data []byte) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upload.jpg")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write upload fixture: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open upload fixture: %v", err)
	}
	defer func() { _ = file.Close() }()
	return transport.Upload(context.Background(), preparedUpload{
		file: file, fileType: fileTypeJPEG, matte: "none", size: int64(len(data)), digest: sha256.Sum256(data),
	})
}

func TestProtocolTransportObservesAndMutatesFrameTV(t *testing.T) {
	t.Parallel()
	fixture := newProtocolTVFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "auth", "token")
	transport := connectFixtureTransport(t, fixture, tokenPath)
	ctx := context.Background()

	if err := transport.Connect(ctx, false); err != nil {
		t.Fatalf("second Connect() error = %v", err)
	}
	info, err := transport.DeviceInfo(ctx)
	if err != nil {
		t.Fatalf("DeviceInfo() error = %v", err)
	}
	if info.ModelName != "QN55LS03D" || validateFrameTVDevice(info) != nil || info.PowerState != stringOn {
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
	if got := fixture.DeleteMutations(); len(got) != 1 || got[0] != "art-1" || len(fixture.Inventory()) != 0 {
		t.Fatalf("wire deletes = %v, inventory = %v", got, fixture.Inventory())
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
	if err := transport.ConfigureSlideshow(ctx, SlideshowSetting{Interval: -1, Kind: SlideshowSequential}); err == nil ||
		protocolErrorOutcome(err) != OutcomeNotAttempted {
		t.Fatalf("ConfigureSlideshow(invalid) error = %v, want not attempted", err)
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

	contentID, err := uploadFixtureImage(t, transport, []byte("image-data"))
	if err != nil || contentID != "uploaded-art" {
		t.Fatalf("Upload() = %q, %v", contentID, err)
	}
	select {
	case capture := <-fixture.d2dCapture:
		if string(capture.payload) != "image-data" || capture.header["secKey"] != "fixture-key" || capture.header["fileType"] != fileTypeJPEG {
			t.Fatalf("D2D capture = %+v, payload %q", capture.header, capture.payload)
		}
	case err := <-fixture.d2dErr:
		t.Fatalf("D2D capture error = %v", err)
	}

	if err := transport.Delete(ctx, "storage-full"); !errors.Is(err, ErrStorageFull) || protocolErrorOutcome(err) != OutcomeNotApplied {
		t.Fatalf("Delete(storage-full) error = %v, outcome = %d", err, protocolErrorOutcome(err))
	}
	if err := transport.Select(ctx, "api-error"); !errors.Is(err, ErrArtAPIError) || protocolErrorOutcome(err) != OutcomeNotApplied {
		t.Fatalf("Select(api-error) error = %v, outcome = %d", err, protocolErrorOutcome(err))
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
	if err := transport.Delete(ctx, "art-1"); !errors.Is(err, ErrNotConnected) || protocolErrorOutcome(err) != OutcomeNotAttempted {
		t.Fatalf("Delete() after close error = %v, want not attempted", err)
	}
	if token, err := os.ReadFile(tokenPath); err != nil || string(token) != "fixture-token" {
		t.Fatalf("persisted token = %q, %v", token, err)
	}
	if mode, err := os.Stat(tokenPath); err != nil || mode.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %v, %v", mode, err)
	}
}

// TestProtocolTransportPowerOffHoldsPowerKey runs in parallel because the
// production power-key hold is a fixed three seconds.
func TestProtocolTransportPowerOffHoldsPowerKey(t *testing.T) {
	t.Parallel()
	fixture := newProtocolTVFixture(t)
	transport := connectFixtureTransport(t, fixture, filepath.Join(t.TempDir(), "auth", "token"))

	started := time.Now()
	if err := transport.PowerOff(context.Background()); err != nil {
		t.Fatalf("PowerOff() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < powerKeyHold {
		t.Fatalf("PowerOff() released after %s, want at least %s", elapsed, powerKeyHold)
	}
	for _, want := range []string{"Press", "Release"} {
		if got := <-fixture.remoteCommands; got != want {
			t.Fatalf("remote command = %q, want %q", got, want)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := transport.PowerOff(canceled); err == nil || protocolErrorOutcome(err) == OutcomeApplied {
		t.Fatalf("PowerOff(canceled) error = %v, want failure", err)
	}
}

func TestProtocolUploadClassifiesImageAddedStorageFullAsNotApplied(t *testing.T) {
	t.Parallel()
	fixture := newProtocolTVFixture(t)
	fixture.stateMu.Lock()
	fixture.imageAddedError = 507
	fixture.stateMu.Unlock()
	transport := connectFixtureTransport(t, fixture, filepath.Join(t.TempDir(), "auth", "token"))

	_, err := uploadFixtureImage(t, transport, []byte("image-data"))
	if !errors.Is(err, ErrStorageFull) || protocolErrorOutcome(err) != OutcomeNotApplied {
		t.Fatalf("Upload() error = %v, outcome = %d; want storage full/not applied", err, protocolErrorOutcome(err))
	}
}

func TestProtocolTransportRejectsUnsafeOrInvalidCommands(t *testing.T) {
	t.Parallel()
	transport := &protocolTransport{
		config: Config{Address: "127.0.0.1:1", TokenPath: filepath.Join(t.TempDir(), "missing-token"), ConnectTimeout: time.Millisecond},
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
	if err := transport.PowerOff(context.Background()); err == nil || protocolErrorOutcome(err) != OutcomeNotAttempted {
		t.Fatalf("PowerOff(unreachable) error = %v, want not attempted", err)
	}
}

func TestObservationAddressDefaultsAndValidatesPort(t *testing.T) {
	t.Parallel()
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
			t.Errorf("observationAddress(%q) = %q, %d, %v", test.address, host, port, err)
		}
	}
}
