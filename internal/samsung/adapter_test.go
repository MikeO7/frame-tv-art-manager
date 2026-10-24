package samsung

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

type scriptedObservationTransport struct {
	connectErr     error
	device         DeviceInfo
	deviceErr      error
	artMode        string
	artModes       []string
	artModeErr     error
	inventory      json.RawMessage
	inventoryErr   error
	connectCalls   int
	artModeCalls   int
	inventoryCalls int
	closeCalls     int
}

func (t *scriptedObservationTransport) Connect(context.Context, bool) error {
	t.connectCalls++
	return t.connectErr
}

func (t *scriptedObservationTransport) DeviceInfo(context.Context) (DeviceInfo, error) {
	return t.device, t.deviceErr
}

func (t *scriptedObservationTransport) ArtMode(context.Context) (string, error) {
	t.artModeCalls++
	if len(t.artModes) > 0 {
		value := t.artModes[0]
		t.artModes = t.artModes[1:]
		return value, t.artModeErr
	}
	return t.artMode, t.artModeErr
}

func (t *scriptedObservationTransport) Inventory(context.Context) (json.RawMessage, error) {
	t.inventoryCalls++
	return t.inventory, t.inventoryErr
}

func (t *scriptedObservationTransport) Close(context.Context) error {
	t.closeCalls++
	return nil
}

func TestAdapterObserveExplicitEmptyInventoryIsEligible(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.July, 12, 10, 0, 0, 0, time.UTC)}
	transport := eligibleObservationTransport(json.RawMessage(`[]`))
	adapter := newTestAdapter(t, clock, transport)

	observation, err := adapter.Observe(context.Background(), ObserveRequest{
		CycleID:              "cycle-17",
		CollectionGeneration: "collection-4",
		Required:             CapabilityArtStateObservation | CapabilityUserArtInventory,
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observation.Power != PowerStateOn || observation.ArtMode != ArtModeOn {
		t.Fatalf("TV state = power %v, art mode %v", observation.Power, observation.ArtMode)
	}
	if !observation.Inventory.Known || len(observation.Inventory.ContentIDs) != 0 {
		t.Fatalf("inventory = %+v, want known empty", observation.Inventory)
	}
	if observation.Inventory.Fingerprint != sha256.Sum256([]byte("[]")) {
		t.Fatalf("inventory fingerprint = %x", observation.Inventory.Fingerprint)
	}
	if observation.Capabilities.ArtStateObservation != SupportSupported ||
		observation.Capabilities.UserArtInventory != SupportSupported {
		t.Fatalf("capabilities = %+v", observation.Capabilities)
	}
	if observation.Disposition != DispositionEligible || unauthorized(observation.Authorization) {
		t.Fatalf("disposition/auth = %v/%+v", observation.Disposition, observation.Authorization)
	}
	if observation.ObservedAt != clock.now || observation.Inventory.ObservedAt != clock.now {
		t.Fatalf("observation timestamps = %v / %v", observation.ObservedAt, observation.Inventory.ObservedAt)
	}
}

func TestAdapterObserveCanonicalizesInventory(t *testing.T) {
	transport := eligibleObservationTransport(json.RawMessage(`"[{\"content_id\":\"MY_F0002\",\"category_id\":\"MY-C0002\"},{\"content_id\":\"MY_F0001\",\"category_id\":\"MY-C0002\"}]"`))
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)

	observation, err := adapter.Observe(context.Background(), ObserveRequest{CycleID: "cycle", CollectionGeneration: "generation"})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	want := []string{"MY_F0001", "MY_F0002"}
	if !reflect.DeepEqual(observation.Inventory.ContentIDs, want) {
		t.Fatalf("content IDs = %v, want %v", observation.Inventory.ContentIDs, want)
	}
	if observation.Inventory.Fingerprint != sha256.Sum256([]byte(`["MY_F0001","MY_F0002"]`)) {
		t.Fatalf("inventory fingerprint = %x", observation.Inventory.Fingerprint)
	}
}

func TestAdapterObserveNeverInfersArtModeOnFromErrors(t *testing.T) {
	tests := []struct {
		name  string
		power string
		value string
		err   error
	}{
		{name: "standby request error", power: stringStandby, err: errors.New("connection lost")},
		{name: "blank response"},
		{name: "standby unknown response", power: stringStandby, value: "ambient"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := eligibleObservationTransport(json.RawMessage(`[]`))
			if test.power != "" {
				transport.device.PowerState = test.power
			}
			transport.artMode = test.value
			transport.artModeErr = test.err
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)

			observation, err := adapter.Observe(context.Background(), ObserveRequest{CycleID: "cycle", CollectionGeneration: "generation"})
			if err == nil {
				t.Fatal("Observe() error = nil")
			}
			if observation.ArtMode != ArtModeUnknown || !unauthorized(observation.Authorization) {
				t.Fatalf("art mode/auth = %v/%+v", observation.ArtMode, observation.Authorization)
			}
			if transport.inventoryCalls != 0 {
				t.Fatalf("inventory calls = %d, want zero", transport.inventoryCalls)
			}
		})
	}
}

func TestAdapterObserveStandbyArtModeIsOperational(t *testing.T) {
	transport := eligibleObservationTransport(json.RawMessage(`[{"content_id":"MY_F0001","category_id":"MY-C0002"}]`))
	transport.device.PowerState = stringStandby
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)

	observation, err := adapter.Observe(context.Background(), ObserveRequest{
		CycleID:              "cycle",
		CollectionGeneration: "generation",
		Required:             CapabilityUserArtInventory,
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observation.Power != PowerStateOn || observation.ArtMode != ArtModeOn ||
		observation.Disposition != DispositionEligible || unauthorized(observation.Authorization) {
		t.Fatalf("standby Art Mode observation = %#v", observation)
	}
	if !observation.Inventory.Known || !reflect.DeepEqual(observation.Inventory.ContentIDs, []string{"MY_F0001"}) {
		t.Fatalf("standby Art Mode inventory = %#v", observation.Inventory)
	}
}

func TestAdapterObserveKnownOffStatesAreSafeSkips(t *testing.T) {
	tests := []struct {
		name             string
		power            string
		artMode          string
		wantDisposition  Disposition
		wantArtModeCalls int
	}{
		{name: "power off", power: "off", artMode: "on", wantDisposition: DispositionBlockedPowerOff},
		{
			name: "standby outside Art Mode", power: "standby", artMode: "off",
			wantDisposition: DispositionBlockedPowerOff, wantArtModeCalls: 1,
		},
		{name: "art mode off", power: "on", artMode: "off", wantDisposition: DispositionBlockedNotArtMode, wantArtModeCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := eligibleObservationTransport(json.RawMessage(`[]`))
			transport.device.PowerState = test.power
			transport.artMode = test.artMode
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)

			observation, err := adapter.Observe(context.Background(), ObserveRequest{CycleID: "cycle", CollectionGeneration: "generation"})
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if observation.Disposition != test.wantDisposition || !unauthorized(observation.Authorization) {
				t.Fatalf("disposition/auth = %v/%+v", observation.Disposition, observation.Authorization)
			}
			if transport.artModeCalls != test.wantArtModeCalls || transport.inventoryCalls != 0 {
				t.Fatalf("read calls = art %d, inventory %d", transport.artModeCalls, transport.inventoryCalls)
			}
		})
	}
}

func TestAdapterKnownOffAuthorizesOnlyExactSupportedWake(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		power      string
		artMode    string
		withMAC    bool
		required   CapabilitySet
		wantRemote Support
	}{
		{
			name: "configured MAC and exact remote-power request", withMAC: true,
			required: CapabilityRemotePower, wantRemote: SupportSupported,
		},
		{
			name: "standby outside Art Mode with configured MAC", power: stringStandby, artMode: stringOff, withMAC: true,
			required: CapabilityRemotePower, wantRemote: SupportSupported,
		},
		{
			name: "missing MAC", required: CapabilityRemotePower,
			wantRemote: SupportUnknown,
		},
		{
			name: "request includes unrelated capabilities", withMAC: true,
			required:   CapabilityRemotePower | CapabilityUserArtInventory,
			wantRemote: SupportUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transport := mutationTransportForInventory(`[]`)
			transport.device.PowerState = test.power
			if test.power == "" {
				transport.device.PowerState = stringOff
			}
			if test.artMode != "" {
				transport.artMode = test.artMode
			}
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
			if test.withMAC {
				adapter.config.MAC = []byte{0, 1, 2, 3, 4, 5}
			}
			// The scripted TV never wakes, so bound the postcondition poll tightly.
			adapter.config.RequestTimeout = 50 * time.Millisecond

			observation, err := adapter.Observe(context.Background(), ObserveRequest{
				CycleID: "wake", CollectionGeneration: "generation", Required: test.required,
			})
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if observation.Power != PowerStateOff || observation.Disposition != DispositionBlockedPowerOff ||
				observation.Capabilities.RemotePower != test.wantRemote {
				t.Fatalf("off observation = %#v", observation)
			}
			receipt, applyErr := adapter.Apply(context.Background(), observation.Authorization, Wake{})
			if test.wantRemote == SupportSupported {
				// The authorization was minted. The scripted device remains off, so
				// the adapter must attempt wake and fail its postcondition safely.
				if applyErr == nil || transport.wakeCalls != 1 || receipt.Outcome == OutcomeApplied {
					t.Fatalf("supported wake receipt/error/calls = %#v/%v/%d", receipt, applyErr, transport.wakeCalls)
				}
				return
			}
			if !errors.Is(applyErr, ErrNotAuthorized) || receipt.Outcome != OutcomeNotAttempted || transport.wakeCalls != 0 {
				t.Fatalf("unsupported wake receipt/error/calls = %#v/%v/%d", receipt, applyErr, transport.wakeCalls)
			}
		})
	}
}

func TestAdapterObserveRejectsAmbiguousInventory(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "absent"},
		{name: "null", raw: json.RawMessage(`null`)},
		{name: "blank string", raw: json.RawMessage(`""`)},
		{name: "malformed", raw: json.RawMessage(`[{`)},
		{name: "duplicate", raw: json.RawMessage(`[{"content_id":"MY_F0001","category_id":"MY-C0002"},{"content_id":"MY_F0001","category_id":"MY-C0002"}]`)},
		{name: "blank ID", raw: json.RawMessage(`[{"content_id":"","category_id":"MY-C0002"}]`)},
		{name: "wrong category", raw: json.RawMessage(`[{"content_id":"MY_F0001","category_id":"OTHER"}]`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, eligibleObservationTransport(test.raw))
			observation, err := adapter.Observe(context.Background(), ObserveRequest{CycleID: "cycle", CollectionGeneration: "generation"})
			if err == nil {
				t.Fatal("Observe() error = nil")
			}
			if observation.Inventory.Known || !unauthorized(observation.Authorization) || observation.Disposition != DispositionUnsafeUnknown {
				t.Fatalf("unsafe inventory observation = %+v", observation)
			}
			if observation.ArtMode == ArtModeOn {
				t.Fatalf("failed observation retained Art Mode on: %+v", observation)
			}
			var samsungErr *Error
			if !errors.As(err, &samsungErr) || samsungErr.Kind != ErrorKindInvalidResponse {
				t.Fatalf("Observe() error = %v, want invalid response", err)
			}
		})
	}
}

func TestAdapterObserveRequiredUnknownCapabilityWithholdsAuthorization(t *testing.T) {
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, eligibleObservationTransport(json.RawMessage(`[]`)))

	observation, err := adapter.Observe(context.Background(), ObserveRequest{
		CycleID:              "cycle",
		CollectionGeneration: "generation",
		Required:             CapabilityImageUpload,
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observation.Capabilities.ImageUpload != SupportUnknown ||
		observation.Disposition != DispositionUnsafeUnknown ||
		!unauthorized(observation.Authorization) {
		t.Fatalf("observation = %+v", observation)
	}
}

func TestAdapterApplyAlwaysRejectsWithoutIO(t *testing.T) {
	transport := eligibleObservationTransport(json.RawMessage(`[]`))
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	observation, err := adapter.Observe(context.Background(), ObserveRequest{
		CycleID:              "cycle",
		CollectionGeneration: "generation",
		DryRun:               true,
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	connectCalls := transport.connectCalls

	receipt, err := adapter.Apply(context.Background(), observation.Authorization, Wake{})
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("Apply() error = %v, want not authorized", err)
	}
	if receipt.Outcome != OutcomeNotAttempted || transport.connectCalls != connectCalls {
		t.Fatalf("receipt/connect calls = %+v/%d", receipt, transport.connectCalls)
	}
}

func TestAdapterCloseInvalidatesObservationAuthority(t *testing.T) {
	transport := eligibleObservationTransport(json.RawMessage(`[]`))
	adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
	observation, err := adapter.Observe(context.Background(), ObserveRequest{CycleID: "cycle", CollectionGeneration: "generation"})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if err := adapter.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if transport.closeCalls != 1 {
		t.Fatalf("close calls = %d", transport.closeCalls)
	}
	if _, err := adapter.Apply(context.Background(), observation.Authorization, Wake{}); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("Apply() after close error = %v", err)
	}
	if _, err := adapter.Observe(context.Background(), ObserveRequest{}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Observe() after close error = %v", err)
	}
}

func eligibleObservationTransport(inventory json.RawMessage) *scriptedObservationTransport {
	return &scriptedObservationTransport{
		device: DeviceInfo{
			ModelName:      "QN55LS03D",
			FrameTVSupport: stringTrue,
			PowerState:     "on",
		},
		artMode:   "on",
		inventory: inventory,
	}
}

func newTestAdapter(t *testing.T, clock Clock, transport observationTransport) *adapter {
	t.Helper()
	tokenPath := filepath.Join(t.TempDir(), "token.txt")
	adapter, err := newAdapter(Config{
		Address:        "192.0.2.10",
		ClientName:     "contract-test",
		TokenPath:      tokenPath,
		ConnectTimeout: time.Second,
		RequestTimeout: time.Second,
		GateTimeout:    time.Second,
		BackoffBase:    time.Minute,
		BackoffMaximum: time.Hour,
	}, Dependencies{
		Clock:  clock,
		Random: bytes.NewReader(bytes.Repeat([]byte{0xff}, 64)),
		Logger: slog.New(slog.DiscardHandler),
	}, transport)
	if err != nil {
		t.Fatalf("newAdapter() error = %v", err)
	}
	return adapter
}

// unauthorized reports whether an observation withheld mutation authority.
func unauthorized(authorization Authorization) bool {
	return authorization.adapter == nil
}
