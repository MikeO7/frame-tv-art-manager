package reconcile

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

const testRotation = 14 * 24 * time.Hour

func itemDigest(item collection.Item) string {
	return hex.EncodeToString(item.Digest[:])
}

func boundState(t *testing.T, observation samsung.Observation, snap collection.Snapshot, bindings map[string]collection.Item, confirmed time.Time) State {
	t.Helper()
	identity, err := identityFromObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	state := initialState(identity)
	for contentID, item := range bindings {
		digest := itemDigest(item)
		state.Bindings[digest] = Binding{
			Digest: digest, ContentID: contentID, Name: item.Name,
			CollectionGeneration: snap.Generation, ConfirmedAt: confirmed,
		}
	}
	return state
}

func commandSummary(commands []CommandIntent) []string {
	summary := make([]string, 0, len(commands))
	for _, command := range commands {
		switch command.Kind {
		case CommandUpload:
			summary = append(summary, "upload:"+command.Name)
		case CommandDeleteOwned, CommandDeleteUnknown:
			summary = append(summary, string(command.Kind)+":"+command.ContentID)
		default:
			summary = append(summary, string(command.Kind))
		}
	}
	return summary
}

func TestBuildPlanRotation(t *testing.T) {
	t.Parallel()

	a, b, c, d := artworkItem("a.jpg", "a"), artworkItem("b.jpg", "b"), artworkItem("c.jpg", "c"), artworkItem("d.jpg", "d")
	snap := snapshot(a, b, c, d)
	full := knownObservation(samsung.PowerStateOn, "id-a", "id-b")
	aged := testTime.Add(-testRotation)
	tests := []struct {
		name        string
		policy      Policy
		confirmed   time.Time
		capacity    CapacityEvidence
		lastShown   map[string]time.Time
		wantCmds    []string
		wantLimited bool
		wantStatus  RotationStatus
	}{
		{
			name:      "capacity unknown uploads everything without rotation",
			policy:    Policy{Rotation: testRotation},
			confirmed: aged,
			wantCmds:  []string{"upload:" + firstByDigest(c, d).Name, "upload:" + secondByDigest(c, d).Name},
		},
		{
			name:        "rotation disabled reports storage limit",
			confirmed:   aged,
			capacity:    CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime},
			wantCmds:    []string{},
			wantLimited: true,
		},
		{
			name:      "aged artwork swaps for never shown artwork",
			policy:    Policy{Rotation: testRotation},
			confirmed: aged,
			capacity:  CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime},
			wantCmds: []string{
				"delete-owned:" + firstContentByDigest(a, b), "upload:" + firstByDigest(c, d).Name,
				"delete-owned:" + secondContentByDigest(a, b), "upload:" + secondByDigest(c, d).Name,
			},
			wantStatus: RotationStatus{Active: true, Waiting: 2, NextAt: testTime.Add(testRotation)},
		},
		{
			name:       "young artwork waits for its interval",
			policy:     Policy{Rotation: testRotation},
			confirmed:  testTime.Add(-time.Hour),
			capacity:   CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime},
			wantCmds:   []string{},
			wantStatus: RotationStatus{Active: true, Waiting: 2, NextAt: testTime.Add(testRotation - time.Hour)},
		},
		{
			name:      "recently removed artwork does not return immediately",
			policy:    Policy{Rotation: testRotation},
			confirmed: aged,
			capacity:  CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime},
			lastShown: map[string]time.Time{
				itemDigest(c): testTime.Add(-time.Hour), itemDigest(d): testTime.Add(-2 * time.Hour),
			},
			wantCmds:   []string{},
			wantStatus: RotationStatus{Active: true, Waiting: 2, NextAt: testTime.Add(testRotation - 2*time.Hour)},
		},
		{
			name:      "artwork away longest returns first",
			policy:    Policy{Rotation: testRotation},
			confirmed: aged,
			capacity:  CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime},
			lastShown: map[string]time.Time{
				itemDigest(c): testTime.Add(-testRotation), itemDigest(d): testTime.Add(-2 * testRotation),
			},
			wantCmds: []string{
				"delete-owned:" + firstContentByDigest(a, b), "upload:d.jpg",
				"delete-owned:" + secondContentByDigest(a, b), "upload:c.jpg",
			},
			wantStatus: RotationStatus{Active: true, Waiting: 2, NextAt: testTime.Add(testRotation)},
		},
		{
			name:      "free slot fills before rotating",
			policy:    Policy{Rotation: testRotation},
			confirmed: aged,
			capacity:  CapacityEvidence{Known: true, Maximum: 3, ObservedAt: testTime},
			wantCmds: []string{
				"upload:" + firstByDigest(c, d).Name,
				"delete-owned:" + firstContentByDigest(a, b), "upload:" + secondByDigest(c, d).Name,
			},
			wantStatus: RotationStatus{Active: true, Waiting: 1, NextAt: testTime.Add(testRotation)},
		},
		{
			name:        "capacity probe never rotates",
			policy:      Policy{Rotation: testRotation},
			confirmed:   aged,
			capacity:    CapacityEvidence{Known: true, Maximum: 3, ObservedAt: testTime, Probe: true},
			wantCmds:    []string{"upload:" + firstByDigest(c, d).Name},
			wantLimited: true,
		},
		{
			name:        "repeated shortfalls pause rotation",
			policy:      Policy{Rotation: testRotation},
			confirmed:   aged,
			capacity:    CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime, Shortfalls: maximumShortfalls},
			wantCmds:    []string{},
			wantLimited: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := boundState(t, full, snap, map[string]collection.Item{"id-a": a, "id-b": b}, test.confirmed)
			state.Capacity = test.capacity
			state.LastShown = test.lastShown
			plan, err := BuildPlan(snap, full, state, test.policy)
			if err != nil {
				t.Fatalf("BuildPlan() error = %v", err)
			}
			if got := commandSummary(plan.Commands); !slices.Equal(got, test.wantCmds) {
				t.Fatalf("commands = %v, want %v", got, test.wantCmds)
			}
			if plan.CapacityLimited != test.wantLimited || plan.Rotation != test.wantStatus {
				t.Fatalf("limited = %v rotation = %+v, want %v %+v",
					plan.CapacityLimited, plan.Rotation, test.wantLimited, test.wantStatus)
			}
		})
	}
}

func TestBuildPlanRotationKeepsLastOwnedArtwork(t *testing.T) {
	t.Parallel()

	a, waiting := artworkItem("a.jpg", "a"), artworkItem("w.jpg", "w")
	snap := snapshot(a, waiting)
	observation := knownObservation(samsung.PowerStateOn, "id-a", "unknown")
	state := boundState(t, observation, snap, map[string]collection.Item{"id-a": a}, testTime.Add(-testRotation))
	state.Capacity = CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime}

	plan, err := BuildPlan(snap, observation, state, Policy{Rotation: testRotation})
	if err != nil || len(plan.Commands) != 0 || !plan.CapacityLimited || plan.Rotation.Active {
		t.Fatalf("plan = %+v, err = %v; want last owned artwork preserved", plan, err)
	}
	plan, err = BuildPlan(snap, observation, state, Policy{Rotation: testRotation, AllowEmpty: true})
	if err != nil || !slices.Equal(commandSummary(plan.Commands), []string{"delete-owned:id-a", "upload:w.jpg"}) {
		t.Fatalf("allow-empty plan = %+v, err = %v; want swap", plan.Commands, err)
	}
}

func TestNextRotationWithoutPendingWork(t *testing.T) {
	t.Parallel()

	if got := nextRotation(nil, nil, false, testRotation, testTime); !got.IsZero() {
		t.Fatalf("nextRotation() = %v, want zero without owned or waiting artwork", got)
	}
	owned := []Binding{{ConfirmedAt: testTime.Add(-2 * testRotation)}}
	waiting := []waitingArtwork{{}}
	if got := nextRotation(owned, waiting, false, testRotation, testTime); !got.Equal(testTime) {
		t.Fatalf("nextRotation() = %v, want now when both sides are overdue", got)
	}
}

func TestRotationPolicyValidation(t *testing.T) {
	t.Parallel()

	for _, rotation := range []time.Duration{-time.Second, maximumRotation + time.Second} {
		if err := validatePolicy(Policy{Rotation: rotation}); err == nil {
			t.Fatalf("validatePolicy(%s) accepted an out-of-range rotation", rotation)
		}
	}
	if err := validatePolicy(Policy{Rotation: maximumRotation}); err != nil {
		t.Fatalf("validatePolicy(maximum) error = %v", err)
	}
}

func TestStateValidationRejectsInvalidRotationEvidence(t *testing.T) {
	t.Parallel()

	identity := TVIdentity{Address: "192.0.2.10", Model: "model"}
	tests := map[string]func(*State){
		"blank history time":    func(state *State) { state.LastShown = map[string]time.Time{digestHex("x"): {}} },
		"malformed history key": func(state *State) { state.LastShown = map[string]time.Time{"bad": testTime} },
		"negative shortfalls": func(state *State) {
			state.Capacity = CapacityEvidence{Known: true, ObservedAt: testTime, Shortfalls: -1}
		},
		"shortfalls without max": func(state *State) { state.Capacity = CapacityEvidence{Shortfalls: 1} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			state := initialState(identity)
			mutate(&state)
			if err := validateState(state, identity); err == nil {
				t.Fatal("validateState() accepted invalid rotation evidence")
			}
		})
	}
}

func TestRotationStatePersistsAndClones(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	identity := TVIdentity{Address: "192.0.2.10", Model: "model"}
	state := initialState(identity)
	state.LastShown = map[string]time.Time{digestHex("shown"): testTime}
	state.Capacity = CapacityEvidence{Known: true, Maximum: 3, ObservedAt: testTime, Shortfalls: 2}
	store := newStateStore(directory)
	if err := store.save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := store.load(context.Background(), identity)
	if err != nil || !exists || !loaded.LastShown[digestHex("shown")].Equal(testTime) || loaded.Capacity.Shortfalls != 2 {
		t.Fatalf("loaded = %+v, exists = %v, err = %v", loaded, exists, err)
	}
	clone := cloneState(loaded)
	clone.LastShown[digestHex("shown")] = testTime.Add(time.Hour)
	if !loaded.LastShown[digestHex("shown")].Equal(testTime) {
		t.Fatal("cloneState shares rotation history")
	}
}

func TestRunRotatesAgedArtworkAndRecordsHistory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	a, b := artworkItem("a.jpg", "a"), artworkItem("b.jpg", "b")
	snap := snapshot(a, b)
	full := knownObservation(samsung.PowerStateOn, "id-a")
	afterDelete := knownObservation(samsung.PowerStateOn)
	afterUpload := knownObservation(samsung.PowerStateOn, "id-b")
	state := boundState(t, full, snap, map[string]collection.Item{"id-a": a}, testTime.Add(-testRotation))
	state.Capacity = CapacityEvidence{Known: true, Maximum: 1, ObservedAt: testTime}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{
		observations: []samsung.Observation{full, full, afterDelete, afterUpload},
		receipts: []samsung.Receipt{
			{CommandID: "delete", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
			{CommandID: "upload", Outcome: samsung.OutcomeApplied, ContentID: "id-b", CompletedAt: testTime},
		},
	}
	service := newTestService(t, directory, Policy{Rotation: testRotation, AllowEmpty: true})

	result, err := service.Run(context.Background(), Request{CycleID: "rotate", TV: adapter, Snapshot: snap})
	if err != nil || result.Status != StatusComplete {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	if !slices.Equal(adapter.commands, []string{"samsung.Delete", "samsung.Upload"}) {
		t.Fatalf("applied commands = %v, want rotation swap", adapter.commands)
	}
	if _, bound := result.State.Bindings[itemDigest(a)]; bound || result.State.Bindings[itemDigest(b)].ContentID != "id-b" {
		t.Fatalf("bindings = %+v, want b replacing a", result.State.Bindings)
	}
	if !result.State.LastShown[itemDigest(a)].Equal(testTime) || len(result.State.LastShown) != 1 {
		t.Fatalf("rotation history = %+v, want a removed at test time", result.State.LastShown)
	}
	want := RotationStatus{Active: true, Waiting: 1, NextAt: testTime.Add(testRotation)}
	if result.Rotation != want {
		t.Fatalf("rotation status = %+v, want %+v", result.Rotation, want)
	}
}

func TestRunAbsorbsFirstStorageFullIntoRotation(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	a, b, c := artworkItem("a.jpg", "a"), artworkItem("b.jpg", "b"), artworkItem("c.jpg", "c")
	snap := snapshot(a, b, c)
	observation := knownObservation(samsung.PowerStateOn, "id-a", "id-b")
	state := boundState(t, observation, snap, map[string]collection.Item{"id-a": a, "id-b": b}, testTime)
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{
		observations: []samsung.Observation{observation},
		receipts:     []samsung.Receipt{{Outcome: samsung.OutcomeNotApplied}},
		applyErrs:    []error{fmt.Errorf("upload rejected: %w", samsung.ErrStorageFull)},
	}
	service := newTestService(t, directory, Policy{Rotation: testRotation})

	result, err := service.Run(context.Background(), Request{CycleID: "becomes-full", TV: adapter, Snapshot: snap})
	if err != nil || result.Status != StatusComplete || adapter.applyCalls != 1 {
		t.Fatalf("Run() = %+v, %v, apply calls = %d; want storage full absorbed", result, err, adapter.applyCalls)
	}
	wantCapacity := CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime}
	if result.State.Capacity != wantCapacity || !result.Rotation.Active || result.Rotation.Waiting != 1 {
		t.Fatalf("capacity = %+v rotation = %+v", result.State.Capacity, result.Rotation)
	}
}

func TestRunReportsStorageFullWhenRotationCannotHelp(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	observation := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2")
	adapter := &fakeAdapter{
		observations: []samsung.Observation{observation},
		receipts:     []samsung.Receipt{{Outcome: samsung.OutcomeNotApplied}},
		applyErrs:    []error{fmt.Errorf("upload rejected: %w", samsung.ErrStorageFull)},
	}
	service := newTestService(t, directory, Policy{Rotation: testRotation})

	result, err := service.Run(context.Background(), Request{
		CycleID: "unknown-art-fills-tv", TV: adapter, Snapshot: snapshot(artworkItem("new.jpg", "new")),
	})
	if !errors.Is(err, samsung.ErrStorageFull) || result.Status != StatusStorageFull || result.Rotation.Active {
		t.Fatalf("Run() = %+v, %v; want storage full without rotation", result, err)
	}
}

func TestStorageFullBelowCapacityCountsShortfallsUntilUploadSucceeds(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	a, b, c := artworkItem("a.jpg", "a"), artworkItem("b.jpg", "b"), artworkItem("c.jpg", "c")
	snap := snapshot(a, b, c)
	observation := knownObservation(samsung.PowerStateOn, "id-a", "id-b")
	state := boundState(t, observation, snap, map[string]collection.Item{"id-a": a, "id-b": b}, testTime)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 3, ObservedAt: testTime, Shortfalls: 1}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	failing := &fakeAdapter{
		observations: []samsung.Observation{observation},
		receipts:     []samsung.Receipt{{Outcome: samsung.OutcomeNotApplied}},
		applyErrs:    []error{fmt.Errorf("upload rejected: %w", samsung.ErrStorageFull)},
	}
	result, err := newTestService(t, directory, Policy{Rotation: testRotation}).Run(context.Background(), Request{
		CycleID: "shortfall", TV: failing, Snapshot: snap,
	})
	if err != nil || result.State.Capacity.Shortfalls != 2 || result.State.Capacity.Maximum != 2 {
		t.Fatalf("Run() = %+v, %v; want second shortfall recorded", result.State.Capacity, err)
	}

	state = result.State
	state.Capacity = CapacityEvidence{Known: true, Maximum: 3, ObservedAt: testTime, Shortfalls: 2}
	state.Revision++
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	succeeding := &fakeAdapter{
		observations: []samsung.Observation{observation, observation, knownObservation(samsung.PowerStateOn, "id-a", "id-b", "id-c")},
		receipts:     []samsung.Receipt{{CommandID: "upload", Outcome: samsung.OutcomeApplied, ContentID: "id-c", CompletedAt: testTime}},
	}
	result, err = newTestService(t, directory, Policy{Rotation: testRotation}).Run(context.Background(), Request{
		CycleID: "recovered", TV: succeeding, Snapshot: snap,
	})
	if err != nil || result.State.Capacity.Shortfalls != 0 || !result.State.Capacity.Known {
		t.Fatalf("Run() = %+v, %v; want shortfalls reset after upload", result.State.Capacity, err)
	}
}

func TestRunRecordsHistoryForArtworkRemovedOutsideTheManager(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	a, gone := artworkItem("a.jpg", "a"), artworkItem("gone.jpg", "gone")
	snap := snapshot(a, gone)
	before := knownObservation(samsung.PowerStateOn, "id-a", "id-gone")
	state := boundState(t, before, snap, map[string]collection.Item{"id-a": a, "id-gone": gone}, testTime)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 1, ObservedAt: testTime}
	stale := digestHex("removed-from-collection")
	state.LastShown = map[string]time.Time{stale: testTime}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{observations: []samsung.Observation{knownObservation(samsung.PowerStateOn, "id-a")}}

	result, err := newTestService(t, directory, Policy{Rotation: testRotation, AllowEmpty: true}).Run(context.Background(), Request{
		CycleID: "manual-removal", TV: adapter, Snapshot: snap,
	})
	if err != nil || result.Status != StatusComplete {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	if _, kept := result.State.LastShown[stale]; kept || !result.State.LastShown[itemDigest(gone)].Equal(testTime) {
		t.Fatalf("rotation history = %+v, want removed artwork recorded and stale history dropped", result.State.LastShown)
	}
}

func firstByDigest(left, right collection.Item) collection.Item {
	if itemDigest(left) < itemDigest(right) {
		return left
	}
	return right
}

func secondByDigest(left, right collection.Item) collection.Item {
	if itemDigest(left) < itemDigest(right) {
		return right
	}
	return left
}

func firstContentByDigest(a, b collection.Item) string {
	return "id-" + firstByDigest(a, b).Name[:1]
}

func secondContentByDigest(a, b collection.Item) string {
	return "id-" + secondByDigest(a, b).Name[:1]
}
