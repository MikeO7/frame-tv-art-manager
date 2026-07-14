package reconcile

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestPowerOnPolicyProbesPowerThenConverges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		observations []samsung.Observation
		receipts     []samsung.Receipt
		wantCommands []string
		wantApplied  int
	}{
		{
			name:         "already on",
			observations: []samsung.Observation{knownObservation(samsung.PowerStateOn), knownObservation(samsung.PowerStateOn)},
		},
		{
			name:         "known off",
			observations: []samsung.Observation{wakeObservation(samsung.SupportSupported), knownObservation(samsung.PowerStateOn)},
			receipts: []samsung.Receipt{{
				CommandID: "wake", Outcome: samsung.OutcomeApplied, CompletedAt: testTime,
			}},
			wantCommands: []string{"samsung.Wake"},
			wantApplied:  1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			adapter := &fakeAdapter{observations: test.observations, receipts: test.receipts}
			service := newTestService(t, filepath.Join(t.TempDir(), "state"), Policy{Power: PowerOn})
			result, err := service.Run(context.Background(), Request{
				CycleID: "power-on", TV: adapter, Snapshot: snapshot(),
			})
			if err != nil || result.Status != StatusComplete {
				t.Fatalf("Run() result = %#v, error = %v", result, err)
			}
			if result.Applied != test.wantApplied || !slices.Equal(adapter.commands, test.wantCommands) {
				t.Fatalf("applied/commands = %d/%v, want %d/%v", result.Applied, adapter.commands, test.wantApplied, test.wantCommands)
			}
			if len(adapter.requests) != 2 {
				t.Fatalf("Observe() requests = %d, want 2", len(adapter.requests))
			}
			if got := adapter.requests[0].Required; got != samsung.CapabilityRemotePower {
				t.Fatalf("power probe capabilities = %09b, want exactly remote power", got)
			}
			if got := adapter.requests[1].Required; got != baseCapabilities() {
				t.Fatalf("convergence capabilities = %09b, want %09b", got, baseCapabilities())
			}
		})
	}
}

func TestPowerOnPolicyRequiresKnownSupportedWake(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		observation samsung.Observation
		wantStatus  Status
	}{
		{name: "known off without MAC support", observation: wakeObservation(samsung.SupportUnknown), wantStatus: StatusUnsupported},
		{name: "unknown power", observation: func() samsung.Observation {
			observation := wakeObservation(samsung.SupportSupported)
			observation.Power = samsung.PowerStateUnknown
			observation.Disposition = samsung.DispositionUnsafeUnknown
			return observation
		}(), wantStatus: StatusNotApplied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "state")
			adapter := &fakeAdapter{observations: []samsung.Observation{test.observation}}
			service := newTestService(t, directory, Policy{Power: PowerOn})
			result, err := service.Run(context.Background(), Request{
				CycleID: "unsafe-wake", TV: adapter, Snapshot: snapshot(),
			})
			if err == nil || result.Status != test.wantStatus {
				t.Fatalf("Run() result = %#v, error = %v", result, err)
			}
			if adapter.applyCalls != 0 {
				t.Fatalf("Apply() calls = %d, want 0", adapter.applyCalls)
			}
			if _, statErr := os.Lstat(directory); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("unsafe wake created durable state: %v", statErr)
			}
		})
	}
}

func TestPowerOnDryRunPlansWakeWithoutMutationOrDurableWrites(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "state")
	adapter := &fakeAdapter{observations: []samsung.Observation{wakeObservation(samsung.SupportSupported)}}
	service := newTestService(t, directory, Policy{Power: PowerOn})
	result, err := service.Run(context.Background(), Request{
		CycleID: "dry-wake", TV: adapter, Snapshot: snapshot(), DryRun: true,
	})
	if err != nil || result.Status != StatusIncompleteDryRun {
		t.Fatalf("Run() result = %#v, error = %v", result, err)
	}
	if len(result.Plan.Commands) != 1 || result.Plan.Commands[0].Kind != CommandWake {
		t.Fatalf("dry-run plan = %#v, want one wake", result.Plan)
	}
	if adapter.applyCalls != 0 {
		t.Fatalf("Apply() calls = %d, want 0", adapter.applyCalls)
	}
	if _, statErr := os.Lstat(directory); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("dry-run wake created durable state: %v", statErr)
	}
}

func TestPowerOnUnknownOutcomePersistsAndRecoversAcrossRestart(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "state")
	firstAdapter := &fakeAdapter{
		observations: []samsung.Observation{wakeObservation(samsung.SupportSupported)},
		receipts: []samsung.Receipt{{
			CommandID: "wake", Outcome: samsung.OutcomeUnknown, CompletedAt: testTime,
		}},
		applyErrs: []error{errors.New("wake acknowledgement lost")},
	}
	first := newTestService(t, directory, Policy{Power: PowerOn})
	result, err := first.Run(context.Background(), Request{
		CycleID: "wake-unknown", TV: firstAdapter, Snapshot: snapshot(),
	})
	if !errors.Is(err, ErrRecoveryRequired) || result.Status != StatusRecoveryRequired ||
		result.State.Pending == nil || result.State.Pending.Command.Kind != CommandWake ||
		result.State.Pending.Phase != PhaseOutcomeUnknown {
		t.Fatalf("first Run() result = %#v, error = %v", result, err)
	}

	on := knownObservation(samsung.PowerStateOn)
	restartAdapter := &fakeAdapter{observations: []samsung.Observation{on, on}}
	restarted := newTestService(t, directory, Policy{Power: PowerOn})
	result, err = restarted.Run(context.Background(), Request{
		CycleID: "wake-recovery", TV: restartAdapter, Snapshot: snapshot(),
	})
	if err != nil || result.Status != StatusComplete || result.State.Pending != nil {
		t.Fatalf("restart Run() result = %#v, error = %v", result, err)
	}
	if restartAdapter.applyCalls != 0 {
		t.Fatalf("restart blindly repeated wake; Apply() calls = %d", restartAdapter.applyCalls)
	}
}

func TestPowerOnWakeThenContinuesCollectionConvergence(t *testing.T) {
	t.Parallel()

	off := wakeObservation(samsung.SupportSupported)
	on := knownObservation(samsung.PowerStateOn)
	adapter := &fakeAdapter{
		observations: []samsung.Observation{off, on, on},
		receipts: []samsung.Receipt{
			{CommandID: "wake", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
			{CommandID: "upload", Outcome: samsung.OutcomeApplied, ContentID: "uploaded", CompletedAt: testTime},
		},
	}
	service := newTestService(t, filepath.Join(t.TempDir(), "state"), Policy{Power: PowerOn})
	result, err := service.Run(context.Background(), Request{
		CycleID: "wake-and-upload", TV: adapter, Snapshot: snapshot(artworkItem("new.jpg", "new")),
	})
	if err != nil || result.Status != StatusComplete || result.Applied != 2 {
		t.Fatalf("Run() result = %#v, error = %v", result, err)
	}
	if want := []string{"samsung.Wake", "samsung.Upload"}; !slices.Equal(adapter.commands, want) {
		t.Fatalf("commands = %v, want %v", adapter.commands, want)
	}
	if want := []CommandKind{CommandWake, CommandUpload}; !slices.Equal(result.AppliedCommands, want) {
		t.Fatalf("result applied commands = %v, want %v", result.AppliedCommands, want)
	}
	if len(result.State.Bindings) != 1 || result.State.Pending != nil {
		t.Fatalf("final state = %#v", result.State)
	}
	if len(adapter.requests) != 3 || adapter.requests[0].Required != samsung.CapabilityRemotePower ||
		adapter.requests[1].Required != baseCapabilities() ||
		adapter.requests[2].Required&baseCapabilities() != baseCapabilities() {
		t.Fatalf("observation requests = %#v", adapter.requests)
	}
}

func wakeObservation(remotePower samsung.Support) samsung.Observation {
	observation := knownObservation(samsung.PowerStateOff)
	observation.Inventory = samsung.Inventory{}
	observation.Capabilities = samsung.Capabilities{RemotePower: remotePower}
	return observation
}
