package reconcile

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func unresolvedUploadState(t *testing.T, before samsung.Observation, receipt *ReceiptSummary, preparedAt time.Time) State {
	t.Helper()
	identity, err := identityFromObservation(before)
	if err != nil {
		t.Fatal(err)
	}
	state := initialState(identity)
	state.UnresolvedUpload = &pending{
		OperationID: "operation", CycleID: "cycle", CollectionGen: digestHex("generation"),
		PolicyFingerprint: sha256.Sum256([]byte("policy")),
		InventoryBefore:   inventoryFingerprint{Digest: before.Inventory.Fingerprint},
		Command: commandIntent{
			Kind: CommandUpload, Digest: digestHex("art"), Name: "art.jpg", Path: "/art/art.jpg",
			FileType: collection.FileTypeJPEG, Size: 3, Matte: defaultMatte,
		},
		Phase: phaseOutcomeUnknown, Receipt: receipt, PreparedAt: &preparedAt,
		InventoryBeforeIDs: slices.Clone(before.Inventory.ContentIDs),
	}
	return state
}

func TestResolveUnresolvedUploadProvesOutcomeFromInventory(t *testing.T) {
	t.Parallel()
	before := knownObservation(samsung.PowerStateOn, "other")
	recent, settled := testTime.Add(-time.Minute), testTime.Add(-uploadSettleTime)
	unexplained := knownObservation(samsung.PowerStateOn, "other", "new")
	unknownWithID := &ReceiptSummary{CommandID: "upload", Outcome: samsung.OutcomeUnknown, ContentID: "MY_F0009", CompletedAt: recent}
	tests := []struct {
		name         string
		receipt      *ReceiptSummary
		preparedAt   time.Time
		noPreparedAt bool
		observation  samsung.Observation
		want         uploadSettlement
		wantBound    bool
	}{
		{
			name: "receipt ID landed", receipt: unknownWithID, preparedAt: recent,
			observation: knownObservation(samsung.PowerStateOn, "other", "MY_F0009"), want: uploadSettled, wantBound: true,
		},
		{
			name: "receipt ID absent", receipt: unknownWithID, preparedAt: recent,
			observation: before, want: uploadSettled,
		},
		{name: "nothing new after settle time", preparedAt: settled, observation: before, want: uploadSettled},
		{
			name: "other art deleted after settle time", preparedAt: settled,
			observation: knownObservation(samsung.PowerStateOn), want: uploadSettled,
		},
		{name: "nothing new but still settling", preparedAt: recent, observation: before},
		{name: "unexplained new art", preparedAt: settled, observation: unexplained},
		{
			name: "unexplained new art just inside the park limit", preparedAt: testTime.Add(-uploadParkLimit + time.Second),
			observation: unexplained,
		},
		{
			name: "unexplained new art at the park limit", preparedAt: testTime.Add(-uploadParkLimit),
			observation: unexplained, want: uploadParkExpired,
		},
		{
			name: "unexplained new art with no prepared time", noPreparedAt: true,
			observation: unexplained, want: uploadParkExpired,
		},
		{
			name: "park limit passed while the TV is off", preparedAt: testTime.Add(-uploadParkLimit),
			observation: samsung.Observation{},
		},
		{name: "inventory unknown", preparedAt: settled, observation: samsung.Observation{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := unresolvedUploadState(t, before, test.receipt, test.preparedAt)
			if test.noPreparedAt {
				state.UnresolvedUpload.PreparedAt = nil
			}
			got, settlement := resolveUnresolvedUpload(state, test.observation, testTime)
			if settlement != test.want || (got.UnresolvedUpload == nil) != (test.want != uploadStillParked) {
				t.Fatalf("settlement = %d, parked = %+v; want %d", settlement, got.UnresolvedUpload, test.want)
			}
			binding, bound := got.Bindings[digestHex("art")]
			if bound != test.wantBound || (bound && binding.ContentID != "MY_F0009") {
				t.Fatalf("binding = %+v, bound %t; want bound %t", binding, bound, test.wantBound)
			}
		})
	}
}

func TestRunWithUnresolvedUploadStillConvergesSettings(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "state")
	before := knownObservation(samsung.PowerStateOn, "other")
	observation := knownObservation(samsung.PowerStateOn, "other", "new")
	observation.Brightness = samsung.SettingObservation{Value: 2, Max: 10, Known: true, ObservedAt: testTime}
	observation.Capabilities.BrightnessRead = samsung.SupportSupported
	observation.Capabilities.BrightnessWrite = samsung.SupportSupported
	state := unresolvedUploadState(t, before, nil, testTime.Add(-uploadSettleTime))
	store := newStateStore(directory)
	if err := store.save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{
		observations: []samsung.Observation{observation, observation},
		receipts:     []samsung.Receipt{{CommandID: "brightness", Outcome: samsung.OutcomeApplied, CompletedAt: testTime}},
	}
	service := newTestService(t, directory, Policy{Brightness: SettingPolicy{Mode: PolicySet, Value: 6}})
	result, err := service.Run(context.Background(), Request{
		CycleID: "parked", TV: adapter, Snapshot: snapshot(artworkItem("art.jpg", "art")),
	})
	if !errors.Is(err, ErrRecoveryRequired) || result.Status != StatusRecoveryRequired {
		t.Fatalf("Run() = %+v, %v; want recovery required", result.Status, err)
	}
	if !slices.Equal(result.AppliedCommands, []CommandKind{CommandBrightness}) {
		t.Fatalf("applied = %v, want only brightness while the upload is unproven", result.AppliedCommands)
	}
	if result.State.UnresolvedUpload == nil {
		t.Fatal("unresolved upload was dropped without proof")
	}
}

// An upload parked behind artwork that never becomes explained (the image
// landed under an unreported ID, or the owner added a picture) must not end
// every later cycle in recovery. After the park limit the upload is given up:
// the unexplained artwork is left alone as unknown art and planning resumes.
func TestRunExpiresUnprovableParkedUpload(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "state")
	before := knownObservation(samsung.PowerStateOn, "other")
	observation := knownObservation(samsung.PowerStateOn, "new", "other")
	state := unresolvedUploadState(t, before, nil, testTime.Add(-uploadParkLimit))
	if err := newStateStore(directory).save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{
		observations: []samsung.Observation{observation},
		receipts: []samsung.Receipt{{
			CommandID: "upload", Outcome: samsung.OutcomeApplied, ContentID: "uploaded", CompletedAt: testTime,
		}},
	}
	item := artworkItem("art.jpg", "art")
	service := newTestService(t, directory, Policy{})
	result, err := service.Run(context.Background(), Request{
		CycleID: "expired", TV: adapter, Snapshot: snapshot(item),
	})
	if err != nil || result.Status != StatusComplete {
		t.Fatalf("Run() = %v, %v; want a complete cycle", result.Status, err)
	}
	if result.State.UnresolvedUpload != nil {
		t.Fatal("expired upload is still parked")
	}
	if !slices.Equal(adapter.commands, []string{"samsung.Upload"}) {
		t.Fatalf("commands = %v, want the artwork uploaded again and nothing deleted", adapter.commands)
	}
	for digest, binding := range result.State.Bindings {
		if binding.ContentID == "new" {
			t.Fatalf("unexplained artwork was bound to %s", digest)
		}
	}
	stored, exists, err := newStateStore(directory).load(context.Background(), result.State.TV)
	if err != nil || !exists || stored.UnresolvedUpload != nil {
		t.Fatalf("stored state parked = %+v, exists %t, error %v", stored.UnresolvedUpload, exists, err)
	}
}
