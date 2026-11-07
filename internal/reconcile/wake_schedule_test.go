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
			{boundary: boundary, observations: []samsung.Observation{dark, off}},
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
