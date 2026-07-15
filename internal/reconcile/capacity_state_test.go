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

func TestStorageFullPersistsFreshCapacityEvidenceAndClearsPending(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	full := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2")
	adapter := &fakeAdapter{
		observations: []samsung.Observation{full, full},
		receipts:     []samsung.Receipt{{Outcome: samsung.OutcomeNotApplied}},
		applyErrs:    []error{fmt.Errorf("upload rejected: %w", samsung.ErrStorageFull)},
	}
	service := newTestService(t, directory, Policy{})

	result, err := service.Run(context.Background(), Request{
		CycleID: "capacity-detection", TV: adapter,
		Snapshot: snapshot(artworkItem("new.jpg", "new")),
	})
	if !errors.Is(err, samsung.ErrStorageFull) {
		t.Fatalf("Run() error = %v, want ErrStorageFull", err)
	}
	if result.State.Pending != nil {
		t.Fatalf("storage-full state retained pending operation: %+v", result.State.Pending)
	}
	want := CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime}
	if result.State.Capacity != want {
		t.Fatalf("capacity evidence = %+v, want %+v", result.State.Capacity, want)
	}
	if adapter.applyCalls != 1 {
		t.Fatalf("Apply() calls = %d, want 1", adapter.applyCalls)
	}
}

func TestKnownCapacityCountsUnknownArtworkAndCapsUploadsDeterministically(t *testing.T) {
	items := []collection.Item{
		artworkItem("c.jpg", "third"),
		artworkItem("a.jpg", "first"),
		artworkItem("b.jpg", "second"),
	}
	observation := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2")
	identity, err := identityFromObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	state := initialState(identity)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 3, ObservedAt: testTime}

	plan, err := BuildPlan(snapshot(items...), observation, state, Policy{})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Commands) != 1 || plan.Commands[0].Kind != CommandUpload {
		t.Fatalf("commands = %+v, want one upload in the only available slot", plan.Commands)
	}
	wantDigests := make([]string, 0, len(items))
	for _, item := range items {
		wantDigests = append(wantDigests, hex.EncodeToString(item.Digest[:]))
	}
	slices.Sort(wantDigests)
	if plan.Commands[0].Digest != wantDigests[0] {
		t.Fatalf("admitted digest = %s, want deterministic first digest %s", plan.Commands[0].Digest, wantDigests[0])
	}
}

func TestCapacityEvidenceSurvivesRestartAndPreventsAnotherFullUpload(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	full := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2")
	failingAdapter := &fakeAdapter{
		observations: []samsung.Observation{full, full},
		receipts:     []samsung.Receipt{{Outcome: samsung.OutcomeNotApplied}},
		applyErrs:    []error{fmt.Errorf("upload rejected: %w", samsung.ErrStorageFull)},
	}
	firstService := newTestService(t, directory, Policy{})
	firstSnapshot := snapshot(artworkItem("first.jpg", "first"))
	if _, err := firstService.Run(context.Background(), Request{
		CycleID: "detect-full", TV: failingAdapter, Snapshot: firstSnapshot,
	}); !errors.Is(err, samsung.ErrStorageFull) {
		t.Fatalf("capacity detection error = %v, want ErrStorageFull", err)
	}

	restartedAdapter := &fakeAdapter{observations: []samsung.Observation{full}}
	restartedService := newTestService(t, directory, Policy{})
	result, err := restartedService.Run(context.Background(), Request{
		CycleID: "after-restart", TV: restartedAdapter,
		Snapshot: snapshot(artworkItem("first.jpg", "first"), artworkItem("second.jpg", "second")),
	})
	if !errors.Is(err, samsung.ErrStorageFull) || result.Status != StatusStorageFull ||
		result.Applied != 0 || restartedAdapter.applyCalls != 0 {
		t.Fatalf("restarted result = %+v, apply calls = %d", result, restartedAdapter.applyCalls)
	}
	if !result.State.Capacity.Known || result.State.Capacity.Maximum != 2 || result.State.Capacity.ObservedAt != testTime {
		t.Fatalf("restarted capacity evidence = %+v", result.State.Capacity)
	}
}

func TestStorageFullFromNonUploadDoesNotPersistCapacityEvidence(t *testing.T) {
	observation := knownObservation(samsung.PowerStateOn, "unknown-1")
	adapter := &fakeAdapter{
		observations: []samsung.Observation{observation, observation},
		receipts:     []samsung.Receipt{{Outcome: samsung.OutcomeNotApplied}},
		applyErrs:    []error{fmt.Errorf("delete rejected: %w", samsung.ErrStorageFull)},
	}
	service := newTestService(t, filepath.Join(t.TempDir(), "state"), Policy{RemoveUnknown: true})

	result, err := service.Run(context.Background(), Request{
		CycleID: "non-upload-storage-error", TV: adapter, Snapshot: snapshot(),
	})
	if !errors.Is(err, samsung.ErrStorageFull) || result.State.Capacity.Known {
		t.Fatalf("Run() = %+v, %v; want storage error without capacity evidence", result, err)
	}
}

func TestCapacityNeverDeletesLastKnownGoodArtworkToCreateAnUploadSlot(t *testing.T) {
	observation := knownObservation(samsung.PowerStateOn, "last-known-good")
	identity, err := identityFromObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	state := initialState(identity)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 1, ObservedAt: testTime}
	obsoleteDigest := digestHex("obsolete")
	state.Bindings[obsoleteDigest] = Binding{
		Digest: obsoleteDigest, ContentID: "last-known-good", Name: "old.jpg",
		CollectionGeneration: digestHex("old-generation"), ConfirmedAt: testTime,
	}

	plan, err := BuildPlan(snapshot(artworkItem("replacement.jpg", "replacement")), observation, state, Policy{})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Commands) != 0 {
		t.Fatalf("capacity plan = %+v, want last-known-good artwork preserved", plan.Commands)
	}
}

func TestVerifiedObsoleteDeletionFreesCapacityForUploadOnReplan(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	wanted := artworkItem("wanted.jpg", "wanted")
	missing := artworkItem("missing.jpg", "missing")
	snap := snapshot(wanted, missing)
	full := knownObservation(samsung.PowerStateOn, "obsolete-id", "wanted-id")
	afterDelete := knownObservation(samsung.PowerStateOn, "wanted-id")
	identity, err := identityFromObservation(full)
	if err != nil {
		t.Fatal(err)
	}
	wantedDigest := hex.EncodeToString(wanted.Digest[:])
	obsoleteDigest := digestHex("obsolete")
	state := initialState(identity)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime}
	state.Bindings[wantedDigest] = Binding{
		Digest: wantedDigest, ContentID: "wanted-id", Name: wanted.Name,
		CollectionGeneration: snap.Generation, ConfirmedAt: testTime,
	}
	state.Bindings[obsoleteDigest] = Binding{
		Digest: obsoleteDigest, ContentID: "obsolete-id", Name: "obsolete.jpg",
		CollectionGeneration: digestHex("old-generation"), ConfirmedAt: testTime,
	}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatalf("seed reconciliation state: %v", err)
	}

	adapter := &fakeAdapter{
		observations: []samsung.Observation{full, full, afterDelete},
		receipts: []samsung.Receipt{
			{CommandID: "delete", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
			{CommandID: "upload", Outcome: samsung.OutcomeApplied, ContentID: "new-id", CompletedAt: testTime},
		},
	}
	service := newTestService(t, directory, Policy{})
	result, err := service.Run(context.Background(), Request{
		CycleID: "delete-then-upload", TV: adapter, Snapshot: snap,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Plan.Commands) != 1 || result.Plan.Commands[0].Kind != CommandDeleteOwned ||
		result.Plan.Commands[0].ContentID != "obsolete-id" {
		t.Fatalf("initial capacity plan = %+v, want verified obsolete deletion only", result.Plan.Commands)
	}
	if !slices.Equal(adapter.commands, []string{"samsung.Delete", "samsung.Upload"}) {
		t.Fatalf("applied commands = %v, want deletion followed by upload after replan", adapter.commands)
	}
	missingDigest := hex.EncodeToString(missing.Digest[:])
	if result.Status != StatusComplete || result.Applied != 2 || result.State.Pending != nil ||
		result.State.Bindings[wantedDigest].ContentID != "wanted-id" ||
		result.State.Bindings[missingDigest].ContentID != "new-id" {
		t.Fatalf("final reconciliation result = %+v", result)
	}
	if _, exists := result.State.Bindings[obsoleteDigest]; exists {
		t.Fatalf("obsolete binding survived verified deletion: %+v", result.State.Bindings[obsoleteDigest])
	}
}

func TestSuccessfulCapacityProbeClearsEvidenceAndResumesAllUploads(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	full := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2")
	identity, err := identityFromObservation(full)
	if err != nil {
		t.Fatal(err)
	}
	state := initialState(identity)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	probeTime := testTime.Add(time.Hour)
	afterProbe := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2", "probe-id")
	adapter := &fakeAdapter{
		observations: []samsung.Observation{full, full, afterProbe},
		receipts: []samsung.Receipt{
			{CommandID: "probe", Outcome: samsung.OutcomeApplied, ContentID: "probe-id", CompletedAt: probeTime},
			{CommandID: "remaining", Outcome: samsung.OutcomeApplied, ContentID: "remaining-id", CompletedAt: probeTime},
		},
	}
	service, err := New(Config{StateDirectory: directory}, Dependencies{
		Clock: fixedClock{probeTime}, IDs: &sequenceIDs{},
	}, WithCapacityEvidenceTTL(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Run(context.Background(), Request{
		CycleID: "expired-capacity-probe", TV: adapter,
		Snapshot: snapshot(artworkItem("first.jpg", "first"), artworkItem("second.jpg", "second")),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != StatusComplete || result.Applied != 2 || adapter.applyCalls != 2 {
		t.Fatalf("Run() = %+v, apply calls = %d", result, adapter.applyCalls)
	}
	if len(result.Plan.Commands) != 1 || result.Plan.Commands[0].Kind != CommandUpload {
		t.Fatalf("probe plan = %+v, want exactly one upload", result.Plan.Commands)
	}
	if result.State.Capacity != (CapacityEvidence{}) {
		t.Fatalf("capacity after successful probe = %+v, want cleared evidence", result.State.Capacity)
	}
	if len(result.State.Bindings) != 2 {
		t.Fatalf("bindings after resumed uploads = %+v", result.State.Bindings)
	}
}

func TestCapacityEvidenceDoesNotProbeBeforeExpiryAfterRestart(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	full := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2")
	identity, _ := identityFromObservation(full)
	state := initialState(identity)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{observations: []samsung.Observation{full}}
	service, err := New(Config{StateDirectory: directory}, Dependencies{
		Clock: fixedClock{testTime.Add(time.Hour - time.Nanosecond)}, IDs: &sequenceIDs{},
	}, WithCapacityEvidenceTTL(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Run(context.Background(), Request{
		CycleID: "capacity-still-fresh", TV: adapter,
		Snapshot: snapshot(artworkItem("first.jpg", "first"), artworkItem("second.jpg", "second")),
	})
	if !errors.Is(err, samsung.ErrStorageFull) || result.Status != StatusStorageFull ||
		result.Applied != 0 || adapter.applyCalls != 0 {
		t.Fatalf("Run() = %+v, %v; apply calls = %d", result, err, adapter.applyCalls)
	}
	want := CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime}
	if result.State.Capacity != want {
		t.Fatalf("capacity evidence = %+v, want unchanged %+v", result.State.Capacity, want)
	}
}

func TestCapacityProbeRecalibratesAfterInventoryChangeAndResumesAllUploads(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	initial := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2")
	beforeApply := knownObservation(samsung.PowerStateOn, "unknown-1")
	identity, _ := identityFromObservation(initial)
	state := initialState(identity)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	probeTime := testTime.Add(time.Hour)
	afterFirst := knownObservation(samsung.PowerStateOn, "unknown-1", "probe-id")
	afterSecond := knownObservation(samsung.PowerStateOn, "unknown-1", "probe-id", "second-id")
	adapter := &fakeAdapter{
		observations: []samsung.Observation{initial, beforeApply, afterFirst, afterSecond},
		receipts: []samsung.Receipt{
			{CommandID: "probe", Outcome: samsung.OutcomeApplied, ContentID: "probe-id", CompletedAt: probeTime},
			{CommandID: "second", Outcome: samsung.OutcomeApplied, ContentID: "second-id", CompletedAt: probeTime},
			{CommandID: "third", Outcome: samsung.OutcomeApplied, ContentID: "third-id", CompletedAt: probeTime},
		},
	}
	service, err := New(Config{StateDirectory: directory}, Dependencies{
		Clock: fixedClock{probeTime}, IDs: &sequenceIDs{},
	}, WithCapacityEvidenceTTL(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Run(context.Background(), Request{
		CycleID: "capacity-probe-reobserve", TV: adapter,
		Snapshot: snapshot(
			artworkItem("first.jpg", "first"), artworkItem("second.jpg", "second"), artworkItem("third.jpg", "third"),
		),
	})
	if err != nil || result.Status != StatusComplete || result.Applied != 3 || adapter.applyCalls != 3 {
		t.Fatalf("Run() = %+v, %v; apply calls = %d", result, err, adapter.applyCalls)
	}
	if result.State.Capacity != (CapacityEvidence{}) || len(result.State.Bindings) != 3 {
		t.Fatalf("state after resumed uploads = %+v", result.State)
	}
}

func TestRepeatedStorageFullProbeRefreshesExpiryAndAllowsNoEarlyRetry(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	full := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2")
	identity, _ := identityFromObservation(full)
	state := initialState(identity)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	ttl := time.Hour
	firstProbeAt := testTime.Add(ttl)
	failing := &fakeAdapter{
		observations: []samsung.Observation{full, full},
		receipts:     []samsung.Receipt{{Outcome: samsung.OutcomeNotApplied}},
		applyErrs:    []error{fmt.Errorf("probe rejected: %w", samsung.ErrStorageFull)},
	}
	first, err := New(Config{StateDirectory: directory}, Dependencies{
		Clock: fixedClock{firstProbeAt}, IDs: &sequenceIDs{},
	}, WithCapacityEvidenceTTL(ttl))
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		CycleID: "first-full-probe", TV: failing,
		Snapshot: snapshot(artworkItem("first.jpg", "first"), artworkItem("second.jpg", "second")),
	}
	result, runErr := first.Run(context.Background(), request)
	if !errors.Is(runErr, samsung.ErrStorageFull) || failing.applyCalls != 1 {
		t.Fatalf("first probe = %+v, %v; apply calls = %d", result, runErr, failing.applyCalls)
	}
	wantFirst := CapacityEvidence{Known: true, Maximum: 2, ObservedAt: firstProbeAt}
	if result.State.Capacity != wantFirst {
		t.Fatalf("first refreshed evidence = %+v, want %+v", result.State.Capacity, wantFirst)
	}

	earlyAdapter := &fakeAdapter{observations: []samsung.Observation{full}}
	early, err := New(Config{StateDirectory: directory}, Dependencies{
		Clock: fixedClock{firstProbeAt.Add(ttl - time.Nanosecond)}, IDs: &sequenceIDs{},
	}, WithCapacityEvidenceTTL(ttl))
	if err != nil {
		t.Fatal(err)
	}
	request.CycleID, request.TV = "early-repeat", earlyAdapter
	earlyResult, runErr := early.Run(context.Background(), request)
	if !errors.Is(runErr, samsung.ErrStorageFull) || earlyResult.Status != StatusStorageFull ||
		earlyResult.Applied != 0 || earlyAdapter.applyCalls != 0 {
		t.Fatalf("early restart = %+v, %v; apply calls = %d", earlyResult, runErr, earlyAdapter.applyCalls)
	}

	secondProbeAt := firstProbeAt.Add(ttl)
	secondFailing := &fakeAdapter{
		observations: []samsung.Observation{full, full},
		receipts:     []samsung.Receipt{{Outcome: samsung.OutcomeNotApplied}},
		applyErrs:    []error{fmt.Errorf("probe rejected again: %w", samsung.ErrStorageFull)},
	}
	second, err := New(Config{StateDirectory: directory}, Dependencies{
		Clock: fixedClock{secondProbeAt}, IDs: &sequenceIDs{},
	}, WithCapacityEvidenceTTL(ttl))
	if err != nil {
		t.Fatal(err)
	}
	request.CycleID, request.TV = "second-full-probe", secondFailing
	secondResult, runErr := second.Run(context.Background(), request)
	if !errors.Is(runErr, samsung.ErrStorageFull) || secondFailing.applyCalls != 1 {
		t.Fatalf("second probe = %+v, %v; apply calls = %d", secondResult, runErr, secondFailing.applyCalls)
	}
	wantSecond := CapacityEvidence{Known: true, Maximum: 2, ObservedAt: secondProbeAt}
	if secondResult.State.Capacity != wantSecond {
		t.Fatalf("second refreshed evidence = %+v, want %+v", secondResult.State.Capacity, wantSecond)
	}
}

func TestNonStorageProbeRejectionDoesNotRefreshCapacityEvidence(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	full := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2")
	identity, _ := identityFromObservation(full)
	state := initialState(identity)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 2, ObservedAt: testTime}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	probeTime := testTime.Add(time.Hour)
	adapter := &fakeAdapter{
		observations: []samsung.Observation{full, full},
		receipts:     []samsung.Receipt{{Outcome: samsung.OutcomeNotApplied}},
		applyErrs:    []error{fmt.Errorf("upload rejected: %w", samsung.ErrArtAPIError)},
	}
	service, err := New(Config{StateDirectory: directory}, Dependencies{
		Clock: fixedClock{probeTime}, IDs: &sequenceIDs{},
	}, WithCapacityEvidenceTTL(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	result, runErr := service.Run(context.Background(), Request{
		CycleID: "non-storage-probe-rejection", TV: adapter,
		Snapshot: snapshot(artworkItem("first.jpg", "first")),
	})
	if !errors.Is(runErr, samsung.ErrArtAPIError) || result.State.Capacity != (CapacityEvidence{}) {
		t.Fatalf("Run() = %+v, %v; want original error and cleared capacity evidence", result, runErr)
	}
}

func TestExpiredCapacityProbePreservesLastKnownGoodArtwork(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	observation := knownObservation(samsung.PowerStateOn, "last-known-good")
	identity, _ := identityFromObservation(observation)
	state := initialState(identity)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 1, ObservedAt: testTime}
	oldDigest := digestHex("old")
	state.Bindings[oldDigest] = Binding{
		Digest: oldDigest, ContentID: "last-known-good", Name: "old.jpg",
		CollectionGeneration: digestHex("old-generation"), ConfirmedAt: testTime,
	}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	probeTime := testTime.Add(time.Hour)
	adapter := &fakeAdapter{
		observations: []samsung.Observation{observation, observation},
		receipts:     []samsung.Receipt{{Outcome: samsung.OutcomeNotApplied}},
		applyErrs:    []error{fmt.Errorf("probe rejected: %w", samsung.ErrStorageFull)},
	}
	service, err := New(Config{StateDirectory: directory}, Dependencies{
		Clock: fixedClock{probeTime}, IDs: &sequenceIDs{},
	}, WithCapacityEvidenceTTL(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background(), Request{
		CycleID: "preserve-last-good-probe", TV: adapter,
		Snapshot: snapshot(artworkItem("replacement.jpg", "replacement")),
	})
	if !errors.Is(err, samsung.ErrStorageFull) || result.Applied != 0 {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	if !slices.Equal(adapter.commands, []string{"samsung.Upload"}) {
		t.Fatalf("commands = %v, want upload only", adapter.commands)
	}
	if _, exists := result.State.Bindings[oldDigest]; !exists {
		t.Fatalf("last-known-good binding was removed: %+v", result.State.Bindings)
	}
}

func TestExpiredCapacityDryRunPlansProbeWithoutPersistingLease(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	full := knownObservation(samsung.PowerStateOn, "unknown-1")
	identity, _ := identityFromObservation(full)
	state := initialState(identity)
	state.Capacity = CapacityEvidence{Known: true, Maximum: 1, ObservedAt: testTime}
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{observations: []samsung.Observation{full}}
	service, err := New(Config{StateDirectory: directory}, Dependencies{
		Clock: fixedClock{testTime.Add(time.Hour)}, IDs: &sequenceIDs{},
	}, WithCapacityEvidenceTTL(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background(), Request{
		CycleID: "dry-run-capacity-probe", TV: adapter, DryRun: true,
		Snapshot: snapshot(artworkItem("new.jpg", "new")),
	})
	if err != nil || result.Status != StatusIncompleteDryRun || adapter.applyCalls != 0 {
		t.Fatalf("Run() = %+v, %v; apply calls = %d", result, err, adapter.applyCalls)
	}
	if len(result.Plan.Commands) != 1 || result.Plan.Commands[0].Kind != CommandUpload || result.State.Capacity != state.Capacity {
		t.Fatalf("dry-run result = %+v", result)
	}
	persisted, exists, err := newStateStore(directory).load(context.Background(), identity)
	if err != nil || !exists || persisted.Capacity != state.Capacity {
		t.Fatalf("persisted dry-run state = %+v, exists=%v, error=%v", persisted, exists, err)
	}
}
