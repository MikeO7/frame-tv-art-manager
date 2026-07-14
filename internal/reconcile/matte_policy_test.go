package reconcile

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestMatteOverridesSelectExactArtworkAndStableFingerprint(t *testing.T) {
	t.Parallel()

	first, err := NewMatteOverrides([]MatteOverride{
		{Filename: "z.jpg", Matte: "modern_black"},
		{Filename: "a.jpg", Matte: "shadowbox_warm"},
	})
	if err != nil {
		t.Fatalf("NewMatteOverrides() error = %v", err)
	}
	second, err := NewMatteOverrides([]MatteOverride{
		{Filename: "a.jpg", Matte: "shadowbox_warm"},
		{Filename: "z.jpg", Matte: "modern_black"},
	})
	if err != nil {
		t.Fatalf("NewMatteOverrides(reordered) error = %v", err)
	}
	firstPolicy := Policy{DefaultMatte: "none", MatteOverrides: first}
	secondPolicy := Policy{DefaultMatte: "none", MatteOverrides: second}
	firstFingerprint, err := fingerprintPolicy(firstPolicy)
	if err != nil {
		t.Fatalf("fingerprint first policy: %v", err)
	}
	secondFingerprint, err := fingerprintPolicy(secondPolicy)
	if err != nil {
		t.Fatalf("fingerprint reordered policy: %v", err)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatal("matte override input order changed the policy fingerprint")
	}
	changed, err := NewMatteOverrides([]MatteOverride{
		{Filename: "a.jpg", Matte: "modern_white"},
		{Filename: "z.jpg", Matte: "modern_black"},
	})
	if err != nil {
		t.Fatalf("NewMatteOverrides(changed) error = %v", err)
	}
	changedFingerprint, err := fingerprintPolicy(Policy{DefaultMatte: "none", MatteOverrides: changed})
	if err != nil {
		t.Fatalf("fingerprint changed policy: %v", err)
	}
	if firstFingerprint == changedFingerprint {
		t.Fatal("matte override value was omitted from the policy fingerprint")
	}

	a := artworkItem("a.jpg", "a")
	b := artworkItem("b.jpg", "b")
	observation := knownObservation(samsung.PowerStateOn)
	identity, err := identityFromObservation(observation)
	if err != nil {
		t.Fatalf("identityFromObservation() error = %v", err)
	}
	plan, err := BuildPlan(snapshot(a, b), observation, initialState(identity), firstPolicy)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	matteByName := make(map[string]string)
	for _, command := range plan.Commands {
		if command.Kind == CommandUpload {
			matteByName[command.Name] = command.Matte
		}
	}
	if matteByName["a.jpg"] != "shadowbox_warm" || matteByName["b.jpg"] != "none" {
		t.Fatalf("planned mattes = %v", matteByName)
	}
}

func TestMatteOverridesRejectUnsafeDuplicateOrInvalidEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []MatteOverride
	}{
		{name: "duplicate", entries: []MatteOverride{{Filename: "a.jpg", Matte: "none"}, {Filename: "a.jpg", Matte: "modern"}}},
		{name: "path", entries: []MatteOverride{{Filename: "../a.jpg", Matte: "none"}}},
		{name: "unnormalized filename", entries: []MatteOverride{{Filename: " a.jpg", Matte: "none"}}},
		{name: "blank matte", entries: []MatteOverride{{Filename: "a.jpg"}}},
		{name: "unnormalized matte", entries: []MatteOverride{{Filename: "a.jpg", Matte: " none"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewMatteOverrides(test.entries); err == nil {
				t.Fatal("NewMatteOverrides() accepted invalid entries")
			}
		})
	}
}

func TestMatteOverrideUploadRemainsIdempotentAfterRestart(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "state")
	overrides, err := NewMatteOverrides([]MatteOverride{{Filename: "art.jpg", Matte: "modern_black"}})
	if err != nil {
		t.Fatalf("NewMatteOverrides() error = %v", err)
	}
	policy := Policy{DefaultMatte: "none", MatteOverrides: overrides}
	item := artworkItem("art.jpg", "art")
	snap := snapshot(item)
	empty := knownObservation(samsung.PowerStateOn)
	withArt := knownObservation(samsung.PowerStateOn, "content")
	adapter := &fakeAdapter{
		observations: []samsung.Observation{empty, empty, withArt},
		receipts: []samsung.Receipt{{
			CommandID: "upload", Outcome: samsung.OutcomeApplied,
			ContentID: "content", CompletedAt: testTime,
		}},
	}
	first := newTestService(t, directory, policy)
	result, err := first.Run(context.Background(), Request{CycleID: "first", TV: adapter, Snapshot: snap})
	if err != nil || result.Status != StatusComplete || result.Applied != 1 {
		t.Fatalf("first Run() = %+v, %v", result, err)
	}
	if len(result.Plan.Commands) == 0 || result.Plan.Commands[0].Matte != "modern_black" {
		t.Fatalf("first plan = %#v", result.Plan.Commands)
	}

	restartedAdapter := &fakeAdapter{observations: []samsung.Observation{withArt}}
	restarted := newTestService(t, directory, policy)
	result, err = restarted.Run(context.Background(), Request{CycleID: "restart", TV: restartedAdapter, Snapshot: snap})
	if err != nil || result.Status != StatusComplete || result.Applied != 0 || len(result.Plan.Commands) != 0 {
		t.Fatalf("restart Run() = %+v, %v", result, err)
	}
	digest := hex.EncodeToString(item.Digest[:])
	if result.State.Bindings[digest].ContentID != "content" {
		t.Fatalf("restart binding = %#v", result.State.Bindings[digest])
	}
}
