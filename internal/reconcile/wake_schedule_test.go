package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestScheduledWakeWakesAtMostOncePerBoundary(t *testing.T) {
	t.Parallel()
	boundary := testTime.Add(-time.Hour)
	off, on := knownObservation(samsung.PowerStateOff), knownObservation(samsung.PowerStateOn)
	dark := samsung.Observation{
		TV: samsung.TVIdentity{Address: off.TV.Address}, PresumedStandby: true,
		Capabilities: samsung.Capabilities{RemotePower: samsung.SupportSupported},
	}
	wakeReceipt := samsung.Receipt{CommandID: "wake", Outcome: samsung.OutcomeApplied, CompletedAt: testTime}
	type cycle struct {
		boundary     time.Time
		observations []samsung.Observation
		wantWake     bool
	}
	tests := []struct {
		name   string
		cycles []cycle
	}{
		{name: "off TV is woken once, then again at the next boundary", cycles: []cycle{
			{boundary: boundary, observations: []samsung.Observation{off, on}, wantWake: true},
			{boundary: boundary, observations: []samsung.Observation{off, off}},
			{boundary: boundary.Add(24 * time.Hour), observations: []samsung.Observation{off, on}, wantWake: true},
		}},
		{name: "dark network TV is woken", cycles: []cycle{
			{boundary: boundary, observations: []samsung.Observation{dark, on}, wantWake: true},
			{boundary: boundary, observations: []samsung.Observation{off}},
		}},
		{name: "TV already on uses up the boundary", cycles: []cycle{
			{boundary: boundary, observations: []samsung.Observation{on, on}},
			{boundary: boundary, observations: []samsung.Observation{off, off}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "state")
			for index, cycle := range test.cycles {
				adapter := &fakeAdapter{observations: cycle.observations, receipts: []samsung.Receipt{wakeReceipt}}
				service := newTestService(t, directory, Policy{})
				result, err := service.Run(context.Background(), Request{
					CycleID: "wake", TV: adapter, Snapshot: snapshot(),
					Policy: Policy{Power: PowerOn, WakeBoundary: cycle.boundary},
				})
				if err != nil {
					t.Fatalf("cycle %d: Run() error = %v", index, err)
				}
				woke := slices.Contains(result.AppliedCommands, CommandWake)
				if woke != cycle.wantWake || (adapter.applyCalls == 1) != cycle.wantWake {
					t.Fatalf("cycle %d: woke %t with %d apply calls, want %t", index, woke, adapter.applyCalls, cycle.wantWake)
				}
			}
			matches, err := filepath.Glob(filepath.Join(directory, "tv_*_wake.json"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("wake records = %v, %v", matches, err)
			}
			if info, err := os.Stat(matches[0]); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("wake record mode = %v, %v; want 0600", info, err)
			}
		})
	}
}

func TestScheduledWakeDryRunSendsAndRecordsNothing(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "state")
	off := knownObservation(samsung.PowerStateOff)
	adapter := &fakeAdapter{observations: []samsung.Observation{off}}
	service := newTestService(t, directory, Policy{})
	if _, err := service.Run(context.Background(), Request{
		CycleID: "wake", TV: adapter, Snapshot: snapshot(), DryRun: true,
		Policy: Policy{Power: PowerOn, WakeBoundary: testTime},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if adapter.applyCalls != 0 {
		t.Fatalf("dry run sent %d commands", adapter.applyCalls)
	}
	if matches, _ := filepath.Glob(filepath.Join(directory, "tv_*_wake.json")); len(matches) != 0 {
		t.Fatalf("dry run wrote wake records %v", matches)
	}
}

// Once a boundary is handled, the rest of the auto-on window is an ordinary
// cycle: one observation, asking for what convergence needs, so the adapter's
// backoff applies. The wake-only probe that bypasses backoff is sent only
// while the boundary is still open.
func TestScheduledWakeHandledBoundaryObservesOnce(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "state")
	on := knownObservation(samsung.PowerStateOn)
	policy := Policy{Power: PowerOn, WakeBoundary: testTime.Add(-time.Hour)}
	run := func(adapter *fakeAdapter) {
		t.Helper()
		service := newTestService(t, directory, Policy{})
		result, err := service.Run(context.Background(), Request{
			CycleID: "wake", TV: adapter, Snapshot: snapshot(), Policy: policy,
		})
		if err != nil || result.Status != StatusComplete {
			t.Fatalf("Run() = %v, %v", result.Status, err)
		}
	}
	first := &fakeAdapter{observations: []samsung.Observation{on, on}}
	run(first)
	if len(first.requests) != 2 || first.requests[0].Required != samsung.CapabilityRemotePower {
		t.Fatalf("open boundary observations = %+v, want a wake probe then convergence", first.requests)
	}
	second := &fakeAdapter{observations: []samsung.Observation{on, on}}
	run(second)
	if len(second.requests) != 1 || second.requests[0].Required != baseCapabilities {
		t.Fatalf("handled boundary observations = %+v, want one convergence observation", second.requests)
	}
}

// A wake record that cannot be read leaves it unknown whether the TV was
// already woken, so the cycle fails before the TV is contacted.
func TestScheduledWakeUnreadableRecordFailsBeforeObserving(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{observations: []samsung.Observation{knownObservation(samsung.PowerStateOff)}}
	if err := os.WriteFile(newStateStore(directory).wakePath(adapter.Address()), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, directory, Policy{})
	result, err := service.Run(context.Background(), Request{
		CycleID: "wake", TV: adapter, Snapshot: snapshot(),
		Policy: Policy{Power: PowerOn, WakeBoundary: testTime},
	})
	if err == nil || result.Status != StatusNotApplied {
		t.Fatalf("Run() = %v, %v; want a failed cycle", result.Status, err)
	}
	if adapter.observeCalls != 0 || adapter.applyCalls != 0 {
		t.Fatalf("Observe()/Apply() calls = %d/%d, want none", adapter.observeCalls, adapter.applyCalls)
	}
}
