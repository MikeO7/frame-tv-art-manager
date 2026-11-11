package samsung

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

func fixtureTransport(t *testing.T, fixture *protocolTVFixture, tokenPath string) *protocolTransport {
	t.Helper()
	transport := newProtocolTransport(Config{
		Address: fixture.address(t), ClientName: "protocol-fixture", TokenPath: tokenPath,
		ConnectTimeout: 2 * time.Second, RequestTimeout: 2 * time.Second,
	}, Dependencies{Logger: slog.New(slog.DiscardHandler)}).(*protocolTransport)
	t.Cleanup(func() { _ = transport.Close(context.Background()) })
	return transport
}

func connectFixtureTransport(t *testing.T, fixture *protocolTVFixture, tokenPath string) *protocolTransport {
	t.Helper()
	transport := fixtureTransport(t, fixture, tokenPath)
	if err := transport.Connect(context.Background(), false); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	return transport
}

func uploadFixtureImage(t *testing.T, transport *protocolTransport, data []byte, existing ...string) (string, error) {
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
	known := make(map[string]struct{}, len(existing))
	for _, contentID := range existing {
		known[contentID] = struct{}{}
	}
	return transport.Upload(context.Background(), preparedUpload{
		file: file, fileType: fileTypeJPEG, matte: "none", size: int64(len(data)), digest: sha256.Sum256(data),
		existing: known,
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
	if info.ModelName != "QN43LS03BAFXZC" || validateFrameTVDevice(info) != nil || info.PowerState != stringOn ||
		info.stableDeviceID() != "uuid:fixture-frame" {
		t.Fatalf("DeviceInfo() = %+v", info)
	}
	if mode, err := transport.ArtMode(ctx); err != nil || mode != stringOn {
		t.Fatalf("ArtMode() = %q, %v", mode, err)
	}
	raw, err := transport.Inventory(ctx)
	if err != nil {
		t.Fatalf("Inventory() error = %v", err)
	}
	inventory, err := normalizeInventory(raw, time.Now())
	if err != nil || !reflect.DeepEqual(inventory.ContentIDs, []string{"art-1"}) {
		t.Fatalf("mixed-category inventory = %+v, %v", inventory, err)
	}
	listRequest := fixture.requestsNamed(keyGetContentList)[0]
	if listRequest[keyCategory] != userArtCategory || listRequest[keyCategoryID] != nil {
		t.Fatalf("content list request = %v, want category %s", listRequest, userArtCategory)
	}
	if current, err := transport.CurrentArtwork(ctx); err != nil || current != "art-1" {
		t.Fatalf("CurrentArtwork() = %q, %v", current, err)
	}

	wantObservedSlideshow := SlideshowSetting{Interval: 30, Kind: SlideshowShuffle, Category: userArtCategory}
	if value, err := transport.Slideshow(ctx); err != nil || value != wantObservedSlideshow {
		t.Fatalf("Slideshow() = %+v, %v", value, err)
	}
	disabled := SlideshowSetting{Kind: SlideshowShuffle, Category: fixtureFavorites}
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
	wantWrites := []SlideshowSetting{disabled, {Interval: 15, Kind: SlideshowSequential, Category: userArtCategory}}
	if got := fixture.slideshowMutations(); !reflect.DeepEqual(got, wantWrites) {
		t.Fatalf("wire slideshow mutations = %+v, want %+v", got, wantWrites)
	}

	if reading, err := transport.Brightness(ctx); err != nil || reading != (brightnessReading{Value: 4, Min: 0, Max: 10}) {
		t.Fatalf("Brightness() = %+v, %v", reading, err)
	}
	if err := transport.ConfigureBrightness(ctx, 7); err != nil {
		t.Fatalf("ConfigureBrightness() error = %v", err)
	}
	if err := transport.ConfigureBrightness(ctx, 61); !errors.Is(err, ErrArtAPIError) || protocolErrorOutcome(err) != OutcomeNotApplied {
		t.Fatalf("ConfigureBrightness(out of range) error = %v, want explicit rejection", err)
	}
	if len(fixture.requestsNamed(requestGetBrightness)) != 0 {
		t.Fatal("art API 4.x transport sent get_brightness instead of get_artmode_settings")
	}

	catalog, err := transport.MatteCatalog(ctx)
	if err != nil || !catalog.accepts("shadowbox_polar") || catalog.accepts("flexible_polar") || !catalog.accepts(matteNone) {
		t.Fatalf("MatteCatalog() = %+v, %v", catalog, err)
	}

	contentID, err := uploadFixtureImage(t, transport, []byte("image-data"), "art-1")
	if err != nil || contentID != "MY_F0020" {
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
	if err := transport.Select(ctx, contentID); err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if current, err := transport.CurrentArtwork(ctx); err != nil || current != contentID {
		t.Fatalf("CurrentArtwork() after select = %q, %v", current, err)
	}
	if err := transport.Select(ctx, "missing"); !errors.Is(err, ErrArtAPIError) || protocolErrorOutcome(err) != OutcomeNotApplied {
		t.Fatalf("Select(missing) error = %v, outcome = %d", err, protocolErrorOutcome(err))
	}

	if err := transport.Delete(ctx, "art-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got := fixture.DeleteMutations(); len(got) != 1 || got[0] != "art-1" || slices.Contains(fixture.Inventory(), "art-1") {
		t.Fatalf("wire deletes = %v, inventory = %v", got, fixture.Inventory())
	}
	if err := transport.Delete(ctx, "storage-full"); !errors.Is(err, ErrStorageFull) || protocolErrorOutcome(err) != OutcomeNotApplied {
		t.Fatalf("Delete(storage-full) error = %v, outcome = %d", err, protocolErrorOutcome(err))
	}

	if err := transport.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := transport.Close(ctx); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := transport.ArtMode(ctx); !errors.Is(err, errNotConnected) {
		t.Fatalf("ArtMode() after close error = %v, want ErrNotConnected", err)
	}
	if err := transport.Delete(ctx, "art-1"); !errors.Is(err, errNotConnected) || protocolErrorOutcome(err) != OutcomeNotAttempted {
		t.Fatalf("Delete() after close error = %v, want not attempted", err)
	}
	if token, err := os.ReadFile(tokenPath); err != nil || string(token) != fixtureToken {
		t.Fatalf("persisted numeric token = %q, %v", token, err)
	}
	if mode, err := os.Stat(tokenPath); err != nil || mode.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %v, %v", mode, err)
	}
}

func TestProtocolTransportUsesLegacyArtAPIRequestNames(t *testing.T) {
	t.Parallel()
	fixture := newConfiguredTVFixture(t, func(f *protocolTVFixture) { f.legacyAPI = true })
	transport := connectFixtureTransport(t, fixture, filepath.Join(t.TempDir(), "auth", "token"))
	ctx := context.Background()

	if reading, err := transport.Brightness(ctx); err != nil || reading != (brightnessReading{Value: 4, Max: 10}) {
		t.Fatalf("legacy Brightness() = %+v, %v", reading, err)
	}
	if len(fixture.requestsNamed(requestArtModeSettings)) != 0 {
		t.Fatal("art API 2.x transport asked for get_artmode_settings")
	}
	setting, err := transport.Slideshow(ctx)
	if err != nil || setting.Interval != 30 {
		t.Fatalf("legacy Slideshow() = %+v, %v", setting, err)
	}
	if err := transport.ConfigureSlideshow(ctx, SlideshowSetting{Interval: 60, Kind: SlideshowSequential}); err != nil {
		t.Fatalf("legacy ConfigureSlideshow() error = %v", err)
	}
	if len(fixture.requestsNamed("set_auto_rotation_status")) != 1 || len(fixture.requestsNamed("set_slideshow_status")) != 0 {
		t.Fatal("legacy slideshow write used the wrong request name")
	}
	contentID, err := uploadFixtureImage(t, transport, []byte("legacy-image"), "art-1")
	if err != nil || contentID != "MY_F0020" {
		t.Fatalf("legacy Upload() = %q, %v", contentID, err)
	}
}

func TestProtocolUploadSkipsImageAddedFromOtherClients(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{false, true} {
		fixture := newConfiguredTVFixture(t, func(f *protocolTVFixture) { f.legacyAPI, f.foreignAdded = legacy, true })
		transport := connectFixtureTransport(t, fixture, filepath.Join(t.TempDir(), "auth", "token"))
		contentID, err := uploadFixtureImage(t, transport, []byte("image-data"), "art-1")
		if err != nil || contentID != "MY_F0020" {
			t.Fatalf("legacy=%t Upload() = %q, %v; a foreign image_added was bound", legacy, contentID, err)
		}
	}
}

func TestProtocolUploadOverSecuredD2DWaitsBeforeConnecting(t *testing.T) {
	t.Parallel()
	fixture := newConfiguredTVFixture(t, func(f *protocolTVFixture) { f.securedD2D = true })
	transport := connectFixtureTransport(t, fixture, filepath.Join(t.TempDir(), "auth", "token"))
	started := time.Now()
	contentID, err := uploadFixtureImage(t, transport, []byte("secured-image"), "art-1")
	if err != nil || contentID == "" {
		t.Fatalf("secured Upload() = %q, %v", contentID, err)
	}
	if elapsed := time.Since(started); elapsed < d2dSecuredConnectDelay {
		t.Fatalf("secured upload connected after %s, want at least %s", elapsed, d2dSecuredConnectDelay)
	}
}

func TestProtocolDeleteDoesNotWaitForUncorrelatedReplies(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{deleteReplyEvent, deleteReplyEcho, deleteReplyNone} {
		fixture := newConfiguredTVFixture(t, func(f *protocolTVFixture) { f.deleteReply = reply })
		transport := connectFixtureTransport(t, fixture, filepath.Join(t.TempDir(), "auth", "token"))
		started := time.Now()
		if err := transport.Delete(context.Background(), "art-1"); err != nil {
			t.Fatalf("reply %s: Delete() error = %v", reply, err)
		}
		if elapsed := time.Since(started); elapsed > transport.probeTimeout()+time.Second {
			t.Fatalf("reply %s: Delete() took %s", reply, elapsed)
		}
		if len(fixture.Inventory()) != 0 {
			t.Fatalf("reply %s: inventory = %v", reply, fixture.Inventory())
		}
	}
}

func TestProtocolTransportPairsOnRemoteControlBeforeArtChannel(t *testing.T) {
	t.Parallel()
	fixture := newProtocolTVFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "auth", "token")
	connectFixtureTransport(t, fixture, tokenPath)
	if fixture.pairingCount() != 1 {
		t.Fatalf("remote-control pairings = %d, want 1", fixture.pairingCount())
	}
	if token, err := os.ReadFile(tokenPath); err != nil || string(token) != fixtureToken {
		t.Fatalf("paired token = %q, %v", token, err)
	}
	connectFixtureTransport(t, fixture, tokenPath)
	if fixture.pairingCount() != 1 {
		t.Fatalf("remote-control pairings with an existing token = %d, want 1", fixture.pairingCount())
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
	fixture := newConfiguredTVFixture(t, func(f *protocolTVFixture) { f.legacyAPI, f.imageAddedError = true, "507" })
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
	if err := transport.Connect(context.Background(), true); !errors.Is(err, errUnauthorized) {
		t.Fatalf("dry-run Connect() error = %v, want ErrUnauthorized", err)
	}
	if _, err := transport.Brightness(context.Background()); !errors.Is(err, errNotConnected) {
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

func TestParseImageAddedCorrelation(t *testing.T) {
	t.Parallel()
	match := imageAddedMatch{requestID: "mine", existing: map[string]struct{}{"MY_F0001": {}}}
	tests := []struct {
		name     string
		raw      string
		wantID   string
		wantMine bool
		wantErr  error
	}{
		{name: "echoed request", raw: `{"event":"image_added","id":"mine","content_id":"MY_F0002"}`, wantID: "MY_F0002", wantMine: true},
		{name: "other request", raw: `{"event":"image_added","request_id":"theirs","content_id":"MY_F0002"}`},
		{name: "new uncorrelated ID", raw: `{"event":"image_added","content_id":"MY_F0003"}`, wantID: "MY_F0003", wantMine: true},
		{name: "existing uncorrelated ID", raw: `{"event":"image_added","content_id":"MY_F0001"}`},
		{name: "blank uncorrelated ID", raw: `{"event":"image_added"}`},
		{name: "correlated error", raw: `{"event":"image_added","id":"mine","error_code":"507"}`, wantMine: true, wantErr: ErrStorageFull},
		{name: "uncorrelated error", raw: `{"event":"image_added","error_code":"-1"}`},
		{name: "malformed", raw: `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contentID, mine, err := parseImageAdded([]byte(test.raw), match)
			// A skipped event may carry the reason it was skipped.
			if !mine {
				err = nil
			}
			if contentID != test.wantID || mine != test.wantMine || !errors.Is(err, test.wantErr) {
				t.Fatalf("parseImageAdded() = %q, %t, %v", contentID, mine, err)
			}
		})
	}
}

// A TV that answers neither slideshow request is asked once per connection;
// asking again on every observation would cost both timeouts each cycle.
func TestProtocolTransportRemembersUnansweredSlideshowUntilReconnect(t *testing.T) {
	t.Parallel()
	fixture := newConfiguredTVFixture(t, func(f *protocolTVFixture) {
		f.unsupported = []string{testProtocolGetSlideshow, "get_auto_rotation_status"}
	})
	transport := connectFixtureTransport(t, fixture, filepath.Join(t.TempDir(), "auth", "token"))
	ctx := context.Background()

	for range 3 {
		if _, err := transport.Slideshow(ctx); !errors.Is(err, ErrArtAPIError) {
			t.Fatalf("Slideshow() error = %v, want the TV's rejection", err)
		}
	}
	for _, name := range []string{testProtocolGetSlideshow, "get_auto_rotation_status", requestAPIVersion} {
		if got := len(fixture.requestsNamed(name)); got != 1 {
			t.Fatalf("%s sent %d times on one connection, want 1", name, got)
		}
	}

	if err := transport.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := transport.Connect(ctx, false); err != nil {
		t.Fatalf("reconnect error = %v", err)
	}
	if _, err := transport.Slideshow(ctx); !errors.Is(err, ErrArtAPIError) {
		t.Fatalf("Slideshow() after reconnect error = %v", err)
	}
	if got := len(fixture.requestsNamed(testProtocolGetSlideshow)); got != 2 {
		t.Fatalf("slideshow probes after reconnect = %d, want 2", got)
	}
}

// A TV that answers no brightness request is asked once per connection.
func TestProtocolTransportRemembersUnansweredBrightnessUntilReconnect(t *testing.T) {
	t.Parallel()
	names := []string{requestAPIVersion, requestLegacyAPIVersion, requestArtModeSettings, requestGetBrightness}
	fixture := newConfiguredTVFixture(t, func(f *protocolTVFixture) { f.unsupported = names })
	transport := connectFixtureTransport(t, fixture, filepath.Join(t.TempDir(), "auth", "token"))
	ctx := context.Background()

	for range 3 {
		if _, err := transport.Brightness(ctx); !errors.Is(err, ErrArtAPIError) {
			t.Fatalf("Brightness() error = %v, want the TV's rejection", err)
		}
	}
	for _, name := range names {
		if got := len(fixture.requestsNamed(name)); got != 1 {
			t.Fatalf("%s sent %d times on one connection, want 1", name, got)
		}
	}
}

// A setting the TV has answered on this connection is a managed setting: a
// later failure is asked again instead of being remembered as unsupported.
func TestProtocolTransportRetriesSettingThatAnsweredBefore(t *testing.T) {
	t.Parallel()
	fixture := newProtocolTVFixture(t)
	transport := connectFixtureTransport(t, fixture, filepath.Join(t.TempDir(), "auth", "token"))
	ctx := context.Background()
	if _, err := transport.Slideshow(ctx); err != nil {
		t.Fatalf("Slideshow() error = %v", err)
	}

	fixture.stopAnswering(testProtocolGetSlideshow, "get_auto_rotation_status")
	for range 2 {
		if _, err := transport.Slideshow(ctx); !errors.Is(err, ErrArtAPIError) {
			t.Fatalf("Slideshow() error = %v, want the TV's rejection", err)
		}
	}
	if got := len(fixture.requestsNamed(testProtocolGetSlideshow)); got != 3 {
		t.Fatalf("slideshow reads = %d, want one per call", got)
	}
}

// A TV that answers neither version request is unversioned for the life of the
// connection: the version probes are not repeated on every brightness read.
func TestProtocolTransportProbesArtAPIVersionOncePerConnection(t *testing.T) {
	t.Parallel()
	fixture := newConfiguredTVFixture(t, func(f *protocolTVFixture) {
		f.unsupported = []string{requestAPIVersion, requestLegacyAPIVersion}
	})
	transport := connectFixtureTransport(t, fixture, filepath.Join(t.TempDir(), "auth", "token"))
	ctx := context.Background()

	for range 3 {
		if reading, err := transport.Brightness(ctx); err != nil || reading.Value != 4 {
			t.Fatalf("Brightness() = %+v, %v", reading, err)
		}
	}
	for _, name := range []string{requestAPIVersion, requestLegacyAPIVersion} {
		if got := len(fixture.requestsNamed(name)); got != 1 {
			t.Fatalf("%s sent %d times on one connection, want 1", name, got)
		}
	}
}
