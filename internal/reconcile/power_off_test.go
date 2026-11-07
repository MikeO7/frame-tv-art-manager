package reconcile

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestScheduledPowerOffRunsAfterAnEarlierCommandFails(t *testing.T) {
	t.Parallel()
	uploadErr := errors.New("upload rejected")
	tests := []struct {
		name          string
		uploadOutcome samsung.Outcome
		finalState    samsung.Observation
		wantApplied   []CommandKind
	}{
		{
			name: "rejected upload", uploadOutcome: samsung.OutcomeNotApplied,
			finalState: knownObservation(samsung.PowerStateOn), wantApplied: []CommandKind{CommandPowerOff},
		},
		{
			name: "unknown upload outcome", uploadOutcome: samsung.OutcomeUnknown,
			finalState: knownObservation(samsung.PowerStateOn),
		},
		{
			name: "TV already off", uploadOutcome: samsung.OutcomeNotApplied,
			finalState: knownObservation(samsung.PowerStateOff),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			on := knownObservation(samsung.PowerStateOn)
			adapter := &fakeAdapter{
				observations: []samsung.Observation{on, on, test.finalState},
				receipts: []samsung.Receipt{
					{CommandID: "upload", Outcome: test.uploadOutcome, CompletedAt: testTime},
					{CommandID: "power", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
				},
				applyErrs: []error{uploadErr, nil},
			}
			service := newTestService(t, filepath.Join(t.TempDir(), "state"), Policy{Power: PowerOff})
			result, err := service.Run(context.Background(), Request{
				CycleID: "night", TV: adapter, Snapshot: snapshot(artworkItem("art.jpg", "art")),
			})
			if !errors.Is(err, uploadErr) {
				t.Fatalf("Run() error = %v, want the upload failure reported", err)
			}
			if !slices.Equal(result.AppliedCommands, test.wantApplied) {
				t.Fatalf("applied = %v, want %v", result.AppliedCommands, test.wantApplied)
			}
		})
	}
}
