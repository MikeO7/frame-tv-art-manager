package reconcile

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestResultReportsOrderedAppliedCommandsOnPartialFailure(t *testing.T) {
	full := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2")
	afterFirst := knownObservation(samsung.PowerStateOn, "unknown-2")
	rejected := errors.New("second delete rejected")
	adapter := &fakeAdapter{
		observations: []samsung.Observation{full, full, afterFirst},
		receipts: []samsung.Receipt{
			{CommandID: "delete-1", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
			{Outcome: samsung.OutcomeNotApplied},
		},
		applyErrs: []error{nil, rejected},
	}
	service := newTestService(t, filepath.Join(t.TempDir(), "state"), Policy{RemoveUnknown: true})

	result, err := service.Run(context.Background(), Request{
		CycleID: "partial-applied-commands", TV: adapter, Snapshot: snapshot(),
	})
	if !errors.Is(err, rejected) || result.Status != StatusNotApplied || result.Applied != 1 {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	if !slices.Equal(result.AppliedCommands, []CommandKind{CommandDeleteUnknown}) {
		t.Fatalf("applied commands = %v, want one completed delete", result.AppliedCommands)
	}
}
