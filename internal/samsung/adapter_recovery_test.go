package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

func TestAdapterUploadSendsOnlyMattesTheTVOffers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		matte         string
		portraitMatte string
		matteErr      error
		want          [2]string
	}{
		{name: "offered matte reused for portrait", matte: "shadowbox_polar", want: [2]string{"shadowbox_polar", "shadowbox_polar"}},
		{name: "separate portrait matte", matte: "shadowbox_polar", portraitMatte: "none", want: [2]string{"shadowbox_polar", "none"}},
		{name: "unknown style", matte: "flexible_polar", want: [2]string{"none", "none"}},
		{name: "unknown portrait color", matte: "none", portraitMatte: "shadowbox_black", want: [2]string{"none", "none"}},
		{
			name: "matte list unavailable", matte: "flexible_polar", matteErr: errors.New("get_matte_list rejected"),
			want: [2]string{"flexible_polar", ""},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, command := testUploadCommand(t)
			command.Matte, command.PortraitMatte = test.matte, test.portraitMatte
			transport := mutationTransportForInventory(`[]`)
			transport.inventories = []json.RawMessage{
				json.RawMessage(`[]`), json.RawMessage(`[]`),
				json.RawMessage(`[{"content_id":"new-id","category_id":"MY-C0002"}]`),
			}
			transport.uploadContentID = "new-id"
			transport.matteErr = test.matteErr
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
			observation := observeForCommand(t, adapter, CapabilityImageUpload)
			if _, err := adapter.Apply(context.Background(), observation.Authorization, command); err != nil {
				t.Fatalf("Apply(upload) error = %v", err)
			}
			if transport.uploadMattes != test.want {
				t.Fatalf("uploaded mattes = %q, want %q", transport.uploadMattes, test.want)
			}
		})
	}
}

func TestAdapterOptionalSettingFailureKeepsObservationUsable(t *testing.T) {
	t.Parallel()
	brightness := CapabilityBrightnessRead | CapabilityBrightnessWrite
	tests := []struct {
		name     string
		required CapabilitySet
		optional CapabilitySet
		wantErr  bool
	}{
		{name: "optional brightness rejected", required: CapabilityImageUpload, optional: brightness},
		{name: "required brightness rejected", required: brightness, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			// The brightness script is empty, so every brightness read fails.
			transport := mutationTransportForInventory(`[]`)
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
			observation, err := adapter.Observe(context.Background(), ObserveRequest{
				CycleID: "cycle", CollectionGeneration: "generation",
				Required: test.required, Optional: test.optional,
			})
			if test.wantErr {
				if err == nil || !unauthorized(observation.Authorization) {
					t.Fatalf("Observe() = %+v, %v; want unauthorized failure", observation, err)
				}
				return
			}
			if err != nil || unauthorized(observation.Authorization) {
				t.Fatalf("Observe() = %+v, %v; want authorized observation", observation, err)
			}
			if observation.Capabilities.BrightnessRead != SupportUnsupported || observation.Brightness.Known {
				t.Fatalf("brightness = %+v / %v", observation.Brightness, observation.Capabilities.BrightnessRead)
			}
		})
	}
}

func TestAdapterWakesTVWhoseNetworkIsDark(t *testing.T) {
	t.Parallel()
	dark := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	tests := []struct {
		name        string
		required    CapabilitySet
		mac         bool
		deviceErr   error
		wantPresume bool
	}{
		{name: "wake request with dark REST", required: CapabilityRemotePower, mac: true, deviceErr: dark, wantPresume: true},
		{name: "artwork request with dark REST", required: CapabilityImageUpload, mac: true, deviceErr: dark},
		{name: "wake request without MAC", required: CapabilityRemotePower, deviceErr: dark},
		{name: "wake request with refused REST", required: CapabilityRemotePower, mac: true, deviceErr: errors.New("HTTP 500")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transport := mutationTransportForInventory(`[]`)
			transport.deviceErr = test.deviceErr
			transport.wakeTurnsOn = true
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
			if test.mac {
				adapter.config.MAC = []byte{0, 1, 2, 3, 4, 5}
			}
			observation, observeErr := adapter.Observe(context.Background(), ObserveRequest{
				CycleID: "cycle", CollectionGeneration: "generation", Required: test.required,
			})
			if observation.PresumedStandby != test.wantPresume {
				t.Fatalf("Observe() = %+v, %v; presumed standby want %t", observation, observeErr, test.wantPresume)
			}
			if observation.Inventory.Known || observation.Power != PowerStateUnknown {
				t.Fatalf("dark TV observation claimed state: %+v", observation)
			}
			receipt, err := adapter.Apply(context.Background(), observation.Authorization, Wake{})
			if !test.wantPresume {
				if !errors.Is(err, ErrNotAuthorized) || transport.wakeCalls != 0 {
					t.Fatalf("Apply(wake) = %+v, %v; calls %d, want refusal", receipt, err, transport.wakeCalls)
				}
				return
			}
			if err != nil || receipt.Outcome != OutcomeApplied || transport.wakeCalls != 1 {
				t.Fatalf("Apply(wake) = %+v, %v; calls %d", receipt, err, transport.wakeCalls)
			}
		})
	}
}

func TestAdapterSelectIsVerifiedByCurrentArtwork(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		pinCurrent string
		want       Outcome
	}{
		{name: "TV shows the selection", want: OutcomeApplied},
		{name: "TV keeps showing another artwork", pinCurrent: "id-2", want: OutcomeNotApplied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			inventory := `[{"content_id":"id-1","category_id":"MY-C0002"},{"content_id":"id-2","category_id":"MY-C0002"}]`
			transport := mutationTransportForInventory(inventory)
			transport.current = "id-2"
			adapter := newTestAdapter(t, &fakeClock{now: time.Unix(100, 0)}, transport)
			adapter.config.RequestTimeout = 50 * time.Millisecond
			observation := observeForCommand(t, adapter, CapabilityImageSelection)
			if observation.CurrentArtwork != "id-2" {
				t.Fatalf("observed current artwork = %q, want id-2", observation.CurrentArtwork)
			}
			if test.pinCurrent != "" {
				transport.pinnedCurrent = test.pinCurrent
			}
			receipt, err := adapter.Apply(context.Background(), observation.Authorization, Select{ContentID: "id-1"})
			if receipt.Outcome != test.want || (err == nil) != (test.want == OutcomeApplied) {
				t.Fatalf("Apply(select) = %+v, %v; want %v", receipt, err, test.want)
			}
		})
	}
}
