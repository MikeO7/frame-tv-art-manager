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
	state.UnresolvedUpload = &Pending{
		OperationID: "operation", CycleID: "cycle", CollectionGen: digestHex("generation"),
		PolicyFingerprint: sha256.Sum256([]byte("policy")),
		InventoryBefore:   InventoryFingerprint{Digest: before.Inventory.Fingerprint},
		Command: CommandIntent{
			Kind: CommandUpload, Digest: digestHex("art"), Name: "art.jpg", Path: "/art/art.jpg",
			FileType: collection.FileTypeJPEG, Size: 3, Matte: defaultMatte,
		},
		Phase: PhaseOutcomeUnknown, Receipt: receipt, PreparedAt: &preparedAt,
		InventoryBeforeIDs: slices.Clone(before.Inventory.ContentIDs),
	}
	return state
}

func TestResolveUnresolvedUploadProvesOutcomeFromInventory(t *testing.T) {
	t.Parallel()
	before := knownObservation(samsung.PowerStateOn, "other")
	recent, settled := testTime.Add(-time.Minute), testTime.Add(-uploadSettleTime)
	unknownWithID := &ReceiptSummary{CommandID: "upload", Outcome: samsung.OutcomeUnknown, ContentID: "MY_F0009", CompletedAt: recent}
	tests := []struct {
		name        string
		receipt     *ReceiptSummary
		preparedAt  time.Time
		observation samsung.Observation
		wantSettled bool
		wantBound   bool
	}{
		{
			name: "receipt ID landed", receipt: unknownWithID, preparedAt: recent,
			observation: knownObservation(samsung.PowerStateOn, "other", "MY_F0009"), wantSettled: true, wantBound: true,
		},
		{
			name: "receipt ID absent", receipt: unknownWithID, preparedAt: recent,
			observation: before, wantSettled: true,
		},
		{name: "nothing new after settle time", preparedAt: settled, observation: before, wantSettled: true},
		{
			name: "other art deleted after settle time", preparedAt: settled,
			observation: knownObservation(samsung.PowerStateOn), wantSettled: true,
		},
		{name: "nothing new but still settling", preparedAt: recent, observation: before},
		{name: "unexplained new art", preparedAt: settled, observation: knownObservation(samsung.PowerStateOn, "other", "new")},
		{name: "inventory unknown", preparedAt: settled, observation: samsung.Observation{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := unresolvedUploadState(t, before, test.receipt, test.preparedAt)
			got, settledNow := resolveUnresolvedUpload(state, test.observation, testTime)
			if settledNow != test.wantSettled || (got.UnresolvedUpload == nil) != test.wantSettled {
				t.Fatalf("settled = %t, parked = %+v; want settled %t", settledNow, got.UnresolvedUpload, test.wantSettled)
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
