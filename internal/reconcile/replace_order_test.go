package reconcile

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// reencodedLibrary models a collection whose every picture was prepared again
// (for example after an encoder or mat change): the TV holds the old copies,
// which are now obsolete, and each replacement is waiting to upload.
func reencodedLibrary(t *testing.T, count int, inventory ...string) (collection.Snapshot, samsung.Observation, State) {
	t.Helper()
	items := make([]collection.Item, 0, count)
	oldIDs := make([]string, 0, count)
	for index := range count {
		name := string(rune('a'+index)) + ".jpg"
		items = append(items, artworkItem(name, "new-"+name))
		oldIDs = append(oldIDs, "old-"+name)
	}
	snap := snapshot(items...)
	if inventory == nil {
		inventory = oldIDs
	}
	observation := knownObservation(samsung.PowerStateOn, inventory...)
	identity, err := identityFromObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	state := initialState(identity)
	for _, contentID := range oldIDs {
		digest := digestHex(contentID)
		state.Bindings[digest] = Binding{
			Digest: digest, ContentID: contentID, Name: contentID[len("old-"):],
			CollectionGeneration: digestHex("previous-generation"), ConfirmedAt: testTime,
		}
	}
	return snap, observation, state
}

func commandKinds(commands []commandIntent) []CommandKind {
	kinds := make([]CommandKind, len(commands))
	for index, command := range commands {
		kinds[index] = command.Kind
	}
	return kinds
}

func TestBuildPlanUploadsReplacementsBeforeRemovingOutdatedArt(t *testing.T) {
	tests := []struct {
		name      string
		capacity  capacityEvidence
		inventory []string
		want      []CommandKind
	}{
		{
			// Until a replacement is bound, the last outdated picture stays.
			name: "unknown capacity uploads every replacement first",
			want: []CommandKind{
				CommandUpload, CommandUpload, CommandUpload,
				CommandDeleteOwned, CommandDeleteOwned,
			},
		},
		{
			name:     "free slot is filled before any removal",
			capacity: capacityEvidence{Known: true, Maximum: 4, ObservedAt: testTime},
			want: []CommandKind{
				CommandUpload,
				CommandDeleteOwned, CommandDeleteOwned,
			},
		},
		{
			name:     "full TV removes outdated art only to make room",
			capacity: capacityEvidence{Known: true, Maximum: 3, ObservedAt: testTime},
			want:     []CommandKind{CommandDeleteOwned, CommandDeleteOwned},
		},
		{
			name:      "slot freed by a removal is refilled next",
			capacity:  capacityEvidence{Known: true, Maximum: 3, ObservedAt: testTime},
			inventory: []string{"old-a.jpg", "old-b.jpg"},
			want:      []CommandKind{CommandUpload, CommandDeleteOwned},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snap, observation, state := reencodedLibrary(t, 3, test.inventory...)
			state.Capacity = test.capacity
			plan, err := buildPlan(snap, observation, state, Policy{})
			if err != nil {
				t.Fatal(err)
			}
			if got := commandKinds(plan.Commands); !slices.Equal(got, test.want) {
				t.Fatalf("commands = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRunFailedReplacementUploadKeepsOutdatedArtOnTheTV(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	snap, observation, state := reencodedLibrary(t, 3)
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatalf("seed reconciliation state: %v", err)
	}
	adapter := &fakeAdapter{
		observations: []samsung.Observation{observation},
		receipts:     []samsung.Receipt{{CommandID: "upload", Outcome: samsung.OutcomeNotApplied, CompletedAt: testTime}},
		applyErrs:    []error{errors.New("upload rejected")},
	}
	service := newTestService(t, directory, Policy{})
	result, err := service.Run(context.Background(), Request{CycleID: "reencoded", TV: adapter, Snapshot: snap})
	if err == nil {
		t.Fatal("Run() succeeded after a rejected upload")
	}
	if !slices.Equal(adapter.commands, []string{"samsung.Upload"}) {
		t.Fatalf("applied commands = %v, want only the failed upload", adapter.commands)
	}
	for _, contentID := range []string{"old-a.jpg", "old-b.jpg", "old-c.jpg"} {
		if binding, ok := result.State.Bindings[digestHex(contentID)]; !ok || binding.ContentID != contentID {
			t.Fatalf("outdated art %q lost its binding after a failed upload: %+v", contentID, result.State.Bindings)
		}
	}
	for _, item := range snap.Items {
		if _, bound := result.State.Bindings[hex.EncodeToString(item.Digest[:])]; bound {
			t.Fatalf("replacement %q bound without an applied upload", item.Name)
		}
	}
}
