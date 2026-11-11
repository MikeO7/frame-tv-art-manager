package reconcile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// These are state files as earlier releases wrote them. The tombstones field
// is inside the checksum, so it has to keep decoding and encoding unchanged
// even though nothing uses it any more.
const (
	stateFileWithTombstones = `{
  "version": 1,
  "tv": {
    "address": "192.0.2.10",
    "model": "QN55LS03",
    "firmware_version": "1"
  },
  "revision": 7,
  "bindings": {
    "d3cec99112255db9bf4963f7798562b6a36f1bd0b2016918629e007d474deb70": {
      "digest": "d3cec99112255db9bf4963f7798562b6a36f1bd0b2016918629e007d474deb70",
      "content_id": "MY_F0001",
      "name": "art.jpg",
      "collection_generation": "e661f4c935e8a5a83349afb5e347695c2e972e967b50efcd618f93b0b7b4c24b",
      "confirmed_at": "2026-01-02T03:04:05Z"
    }
  },
  "tombstones": {
    "MY_F0003": {
      "digest": "283bb9deef02e6843abfb538efa1eca70801bd8a701c3f98191e123496339247",
      "content_id": "MY_F0003",
      "recorded_at": "2026-01-02T03:04:05Z"
    }
  },
  "capacity": {
    "known": false,
    "observed_at": "0001-01-01T00:00:00Z"
  },
  "checksum": "78f59460121c932bbce2d7e2ce58f4fd7bd19c07d0fd75e8fd4c5d94ec52fa1b"
}
`
	stateFileWithEmptyTombstones = `{
  "version": 1,
  "tv": {
    "address": "192.0.2.10",
    "model": "QN55LS03",
    "firmware_version": "1"
  },
  "revision": 7,
  "bindings": {
    "d3cec99112255db9bf4963f7798562b6a36f1bd0b2016918629e007d474deb70": {
      "digest": "d3cec99112255db9bf4963f7798562b6a36f1bd0b2016918629e007d474deb70",
      "content_id": "MY_F0001",
      "name": "art.jpg",
      "collection_generation": "e661f4c935e8a5a83349afb5e347695c2e972e967b50efcd618f93b0b7b4c24b",
      "confirmed_at": "2026-01-02T03:04:05Z"
    }
  },
  "tombstones": {},
  "capacity": {
    "known": false,
    "observed_at": "0001-01-01T00:00:00Z"
  },
  "checksum": "9e3d2331b8a95de01c14547067d94c6080b3dfa3a159e861760cac31d2de7ed7"
}
`
)

func TestStateFilesWithTombstonesStillLoadAndRoundTrip(t *testing.T) {
	t.Parallel()
	identity, err := identityFromObservation(knownObservation(samsung.PowerStateOn))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name           string
		file           string
		wantTombstones int
	}{
		{name: "tombstone entries", file: stateFileWithTombstones, wantTombstones: 1},
		{name: "empty tombstones", file: stateFileWithEmptyTombstones},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			store := newStateStore(directory)
			path := store.path(identity)
			if err := os.WriteFile(path, []byte(test.file), 0o600); err != nil {
				t.Fatal(err)
			}

			state, exists, err := store.load(context.Background(), identity)
			if err != nil || !exists {
				t.Fatalf("load() exists = %t, error = %v", exists, err)
			}
			if state.Revision != 7 || len(state.Bindings) != 1 || len(state.Tombstones) != test.wantTombstones {
				t.Fatalf("loaded state = %+v", state)
			}

			// Saving what was loaded writes the same bytes, so the release that
			// wrote the file can still read it after this one touched it.
			if err := store.save(context.Background(), cloneState(state)); err != nil {
				t.Fatalf("save() error = %v", err)
			}
			written, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(written) != test.file {
				t.Fatalf("round-tripped state file changed:\n%s", written)
			}
		})
	}
}

// Earlier releases refuse a state file without a tombstones object, so a
// state built without the map is still written with an empty one.
func TestEncodeStateAlwaysWritesTombstonesObject(t *testing.T) {
	t.Parallel()
	identity, err := identityFromObservation(knownObservation(samsung.PowerStateOn))
	if err != nil {
		t.Fatal(err)
	}
	state := initialState(identity)
	state.Tombstones = nil
	data, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"tombstones": {}`) {
		t.Fatalf("encoded state omits the tombstones object:\n%s", data)
	}
	if _, err := decodeState(data, identity); err != nil {
		t.Fatalf("decodeState() error = %v", err)
	}
}

// A run that returns an error never reports StatusComplete, whichever step
// failed.
func TestRunErrorNeverReportsComplete(t *testing.T) {
	t.Parallel()
	on := knownObservation(samsung.PowerStateOn)
	off := knownObservation(samsung.PowerStateOff)
	unidentified := on
	unidentified.TV = samsung.TVIdentity{}
	failure := errors.New("TV did not answer")
	tests := []struct {
		name         string
		observations []samsung.Observation
		observeErrs  []error
	}{
		{name: "first observation fails", observations: []samsung.Observation{{}}, observeErrs: []error{failure}},
		{name: "TV identity is unknown", observations: []samsung.Observation{unidentified}},
		{
			name:         "observation before a command fails",
			observations: []samsung.Observation{on, on}, observeErrs: []error{nil, failure},
		},
		{name: "TV leaves Art Mode before a command", observations: []samsung.Observation{on, off}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			adapter := &fakeAdapter{observations: test.observations, observeErrs: test.observeErrs}
			service := newTestService(t, filepath.Join(t.TempDir(), "state"), Policy{})
			result, err := service.Run(context.Background(), Request{
				CycleID: "failing", TV: adapter, Snapshot: snapshot(artworkItem("art.jpg", "art")),
			})
			if err == nil {
				t.Fatal("Run() error = nil, want a failure")
			}
			if result.Status != StatusNotApplied {
				t.Fatalf("Run() status = %d with error %v, want StatusNotApplied", result.Status, err)
			}
			if adapter.applyCalls != 0 {
				t.Fatalf("Apply() calls = %d, want 0", adapter.applyCalls)
			}
		})
	}
}
