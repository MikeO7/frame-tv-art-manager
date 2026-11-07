package reconcile

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestStateFollowsTheTVByDeviceID(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "state")
	store := newStateStore(directory)
	legacy := TVIdentity{Address: "192.0.2.10", Model: "QN55LS03"}
	state := initialState(legacy)
	state.Bindings[digestHex("art")] = Binding{
		Digest: digestHex("art"), ContentID: "MY_F0001", Name: "art.jpg",
		CollectionGeneration: digestHex("generation"), ConfirmedAt: testTime,
	}
	if err := store.save(context.Background(), state); err != nil {
		t.Fatal(err)
	}

	// The first run that sees a device ID migrates the address-keyed record.
	withID := legacy
	withID.DeviceID = "uuid:frame"
	loaded, exists, err := store.load(context.Background(), withID)
	if err != nil || !exists || len(loaded.Bindings) != 1 {
		t.Fatalf("migrated load = %+v, %t, %v", loaded, exists, err)
	}
	loaded.TV = withID
	loaded.Revision++
	if err := store.save(context.Background(), loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.path(legacy)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("address-keyed record still present: %v", err)
	}

	tests := []struct {
		name     string
		identity TVIdentity
		wantKept bool
	}{
		{name: "new address, same TV", identity: TVIdentity{Address: "192.0.2.77", Model: "QN55LS03", DeviceID: "uuid:frame"}, wantKept: true},
		{name: "same address, another TV", identity: TVIdentity{Address: "192.0.2.10", Model: "QN55LS03", DeviceID: "uuid:other"}},
	}
	for _, test := range tests {
		got, exists, err := store.load(context.Background(), test.identity)
		if err != nil || exists != test.wantKept || (exists && len(got.Bindings) != 1) {
			t.Fatalf("%s: load = %+v, %t, %v; want kept %t", test.name, got, exists, err, test.wantKept)
		}
	}
}

func TestIdentityFromObservationCarriesDeviceID(t *testing.T) {
	t.Parallel()
	observation := knownObservation(samsung.PowerStateOn)
	observation.TV.DeviceID = " uuid:frame "
	identity, err := identityFromObservation(observation)
	if err != nil || identity.DeviceID != "uuid:frame" {
		t.Fatalf("identity = %+v, %v", identity, err)
	}
}
