package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

type blockingMutationWaiter struct {
	calls   chan time.Duration
	release chan error
}

func TestTimerMutationWaiterHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (timerMutationWaiter{}).Wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
}

func newBlockingMutationWaiter() *blockingMutationWaiter {
	return &blockingMutationWaiter{calls: make(chan time.Duration, 4), release: make(chan error, 4)}
}

func (w *blockingMutationWaiter) Wait(ctx context.Context, delay time.Duration) error {
	select {
	case w.calls <- delay:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-w.release:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestMutationPacingPersistsNextPendingBeforeWaitingAndNeverWaitsAfterLast(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	full := knownObservation(samsung.PowerStateOn, "unknown-1", "unknown-2")
	afterFirst := knownObservation(samsung.PowerStateOn, "unknown-2")
	adapter := &fakeAdapter{
		observations: []samsung.Observation{full, full, afterFirst},
		receipts: []samsung.Receipt{
			{CommandID: "delete-1", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
			{CommandID: "delete-2", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
		},
	}
	waiter := newBlockingMutationWaiter()
	service, err := New(Config{
		StateDirectory: directory, Policy: Policy{RemoveUnknown: true},
	}, Dependencies{
		Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler),
	}, WithMutationPacing(time.Second), withMutationWaiter(waiter))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	type runResult struct {
		result Result
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		result, runErr := service.Run(context.Background(), Request{
			CycleID: "paced", TV: adapter, Snapshot: snapshot(),
		})
		done <- runResult{result: result, err: runErr}
	}()

	select {
	case delay := <-waiter.calls:
		if delay != time.Second {
			t.Fatalf("pacing delay = %s, want 1s", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("second mutation did not enter pacing wait")
	}
	identity, identityErr := identityFromObservation(full)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	persisted, exists, loadErr := newStateStore(directory).load(context.Background(), identity)
	if loadErr != nil || !exists {
		t.Fatalf("load state during pacing = %+v, %v, exists=%v", persisted, loadErr, exists)
	}
	if persisted.Pending == nil || persisted.Pending.Command.Kind != CommandDeleteUnknown ||
		persisted.Pending.Command.ContentID != "unknown-2" || persisted.Pending.Phase != PhasePrepared {
		t.Fatalf("persisted state during pacing = %+v", persisted)
	}
	adapter.mu.Lock()
	applyCalls := adapter.applyCalls
	adapter.mu.Unlock()
	if applyCalls != 1 {
		t.Fatalf("Apply() calls before pacing release = %d, want 1", applyCalls)
	}

	waiter.release <- nil
	select {
	case completed := <-done:
		if completed.err != nil || completed.result.Status != StatusComplete || completed.result.Applied != 2 {
			t.Fatalf("paced Run() = %+v, %v", completed.result, completed.err)
		}
	case <-time.After(time.Second):
		t.Fatal("paced reconciliation did not complete")
	}
	select {
	case delay := <-waiter.calls:
		t.Fatalf("unexpected pacing wait after final mutation: %s", delay)
	default:
	}
	if !slices.Equal(adapter.commands, []string{"samsung.Delete", "samsung.Delete"}) {
		t.Fatalf("applied commands = %v", adapter.commands)
	}
}

func TestMutationPacingCancellationClearsUnattemptedUploadAndRestartContinues(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	empty := knownObservation(samsung.PowerStateOn)
	afterFirst := knownObservation(samsung.PowerStateOn, "content-1")
	collection := snapshot(artworkItem("first.jpg", "first"), artworkItem("second.jpg", "second"))
	adapter := &fakeAdapter{
		observations: []samsung.Observation{empty, empty, afterFirst},
		receipts: []samsung.Receipt{
			{CommandID: "upload-1", ContentID: "content-1", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
		},
	}
	waiter := newBlockingMutationWaiter()
	service, err := New(Config{StateDirectory: directory}, Dependencies{
		Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler),
	}, WithMutationPacing(time.Second), withMutationWaiter(waiter))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, runErr := service.Run(ctx, Request{CycleID: "cancel-paced", TV: adapter, Snapshot: collection})
		done <- runErr
	}()
	select {
	case <-waiter.calls:
	case <-time.After(time.Second):
		t.Fatal("second mutation did not enter pacing wait")
	}
	cancel()
	select {
	case runErr := <-done:
		if !errors.Is(runErr, context.Canceled) || errors.Is(runErr, ErrRecoveryRequired) {
			t.Fatalf("Run() error = %v, want cancellation without recovery requirement", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("pacing cancellation did not unblock Run")
	}
	adapter.mu.Lock()
	applyCalls := adapter.applyCalls
	adapter.mu.Unlock()
	if applyCalls != 1 {
		t.Fatalf("Apply() calls after pacing cancellation = %d, want 1", applyCalls)
	}
	identity, _ := identityFromObservation(empty)
	persisted, exists, loadErr := newStateStore(directory).load(context.Background(), identity)
	if loadErr != nil || !exists || persisted.Pending != nil || len(persisted.Bindings) != 1 {
		t.Fatalf("durable canceled pacing state = %+v, exists=%v, error=%v", persisted, exists, loadErr)
	}

	restartedAdapter := &fakeAdapter{
		observations: []samsung.Observation{afterFirst, afterFirst},
		receipts: []samsung.Receipt{
			{CommandID: "upload-2", ContentID: "content-2", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
		},
	}
	result, err := service.Run(context.Background(), Request{
		CycleID: "restart-after-cancel", TV: restartedAdapter, Snapshot: collection,
	})
	if err != nil || result.Status != StatusComplete || result.Applied != 1 || len(result.State.Bindings) != 2 {
		t.Fatalf("restart Run() = %+v, %v", result, err)
	}
}

func TestMutationPacingCancellationPropagatesPendingCleanupFailure(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	savedDirectory := directory + "-saved"
	empty := knownObservation(samsung.PowerStateOn)
	afterFirst := knownObservation(samsung.PowerStateOn, "content-1")
	collection := snapshot(artworkItem("first.jpg", "first"), artworkItem("second.jpg", "second"))
	adapter := &fakeAdapter{
		observations: []samsung.Observation{empty, empty, afterFirst},
		receipts: []samsung.Receipt{
			{CommandID: "upload-1", ContentID: "content-1", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
		},
	}
	waiter := newBlockingMutationWaiter()
	service, err := New(Config{StateDirectory: directory}, Dependencies{
		Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler),
	}, WithMutationPacing(time.Second), withMutationWaiter(waiter))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, runErr := service.Run(ctx, Request{CycleID: "cleanup-failure", TV: adapter, Snapshot: collection})
		done <- runErr
	}()
	select {
	case <-waiter.calls:
	case <-time.After(time.Second):
		t.Fatal("second upload did not enter pacing wait")
	}
	if err := os.Rename(directory, savedDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(directory, []byte("blocks state directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(directory)
		_ = os.Rename(savedDirectory, directory)
	})
	cancel()
	select {
	case runErr := <-done:
		if !errors.Is(runErr, context.Canceled) || !strings.Contains(runErr.Error(), "state directory") {
			t.Fatalf("Run() error = %v, want cancellation joined with persistence failure", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("pacing cancellation did not unblock Run")
	}
	identity, _ := identityFromObservation(empty)
	persisted, exists, loadErr := newStateStore(savedDirectory).load(context.Background(), identity)
	if loadErr != nil || !exists || persisted.Pending == nil || persisted.Pending.Command.Kind != CommandUpload {
		t.Fatalf("state after cleanup failure = %+v, exists=%v, error=%v", persisted, exists, loadErr)
	}
}

func TestMutationRetryReobservesAfterDefiniteTransientNotAttempted(t *testing.T) {
	observation := knownObservation(samsung.PowerStateOn, "unknown-1")
	adapter := &fakeAdapter{
		observations: []samsung.Observation{observation, observation, observation},
		receipts: []samsung.Receipt{
			{Outcome: samsung.OutcomeNotAttempted},
			{CommandID: "delete", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
		},
		applyErrs: []error{&samsung.Error{
			Kind: samsung.ErrorKindInvalidResponse, Operation: "delete", Retryable: true,
			Outcome: samsung.OutcomeNotAttempted, Cause: errors.New("transient response failure"),
		}},
	}
	waiter := newBlockingMutationWaiter()
	waiter.release <- nil
	service, err := New(Config{
		StateDirectory: filepath.Join(t.TempDir(), "state"), Policy: Policy{RemoveUnknown: true},
	}, Dependencies{
		Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler),
	}, WithMutationAttempts(2), WithMutationPacing(time.Second), withMutationWaiter(waiter))
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Run(context.Background(), Request{
		CycleID: "retry-not-attempted", TV: adapter, Snapshot: snapshot(),
	})
	if err != nil || result.Status != StatusComplete || result.Applied != 1 {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	adapter.mu.Lock()
	observeCalls, applyCalls := adapter.observeCalls, adapter.applyCalls
	commands := slices.Clone(adapter.commands)
	adapter.mu.Unlock()
	if observeCalls != 3 || applyCalls != 2 {
		t.Fatalf("calls: Observe=%d Apply=%d, want Observe=3 Apply=2", observeCalls, applyCalls)
	}
	if !slices.Equal(commands, []string{"samsung.Delete", "samsung.Delete"}) {
		t.Fatalf("applied commands = %v", commands)
	}
	select {
	case delay := <-waiter.calls:
		if delay != time.Second {
			t.Fatalf("retry pacing delay = %s, want 1s", delay)
		}
	default:
		t.Fatal("retry did not pass through mutation pacing")
	}
	select {
	case delay := <-waiter.calls:
		t.Fatalf("unexpected additional pacing wait: %s", delay)
	default:
	}
}

func TestMutationRetryRejectsUnsafeOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		receipt    samsung.Receipt
		applyError error
		wantStatus Status
		wantError  bool
	}{
		{
			name:    "ambiguous outcome",
			receipt: samsung.Receipt{Outcome: samsung.OutcomeUnknown},
			applyError: &samsung.Error{
				Kind: samsung.ErrorKindInvalidResponse, Operation: "delete", Retryable: true,
				Outcome: samsung.OutcomeUnknown, Cause: errors.New("connection lost"),
			},
			wantStatus: StatusRecoveryRequired,
			wantError:  true,
		},
		{
			name:    "definitely not applied",
			receipt: samsung.Receipt{Outcome: samsung.OutcomeNotApplied},
			applyError: &samsung.Error{
				Kind: samsung.ErrorKindInvalidResponse, Operation: "delete", Retryable: true,
				Outcome: samsung.OutcomeNotApplied, Cause: errors.New("rejected"),
			},
			wantStatus: StatusNotApplied,
			wantError:  true,
		},
		{
			name:    "not attempted but non-retryable",
			receipt: samsung.Receipt{Outcome: samsung.OutcomeNotAttempted},
			applyError: &samsung.Error{
				Kind: samsung.ErrorKindInvalidResponse, Operation: "delete", Retryable: false,
				Outcome: samsung.OutcomeNotAttempted, Cause: errors.New("invalid request"),
			},
			wantStatus: StatusNotApplied,
			wantError:  true,
		},
		{
			name:       "applied receipt with transport error",
			receipt:    samsung.Receipt{CommandID: "delete", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
			applyError: errors.New("late connection close"),
			wantStatus: StatusComplete,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := knownObservation(samsung.PowerStateOn, "unknown-1")
			adapter := &fakeAdapter{
				observations: []samsung.Observation{observation, observation, observation},
				receipts:     []samsung.Receipt{test.receipt},
				applyErrs:    []error{test.applyError},
			}
			service, err := New(Config{
				StateDirectory: filepath.Join(t.TempDir(), "state"), Policy: Policy{RemoveUnknown: true},
			}, Dependencies{
				Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler),
			}, WithMutationAttempts(3))
			if err != nil {
				t.Fatal(err)
			}

			result, runErr := service.Run(context.Background(), Request{
				CycleID: "unsafe-retry", TV: adapter, Snapshot: snapshot(),
			})
			if (runErr != nil) != test.wantError || result.Status != test.wantStatus {
				t.Fatalf("Run() = %+v, %v; want status %q error=%v", result, runErr, test.wantStatus, test.wantError)
			}
			adapter.mu.Lock()
			observeCalls, applyCalls := adapter.observeCalls, adapter.applyCalls
			adapter.mu.Unlock()
			if observeCalls != 2 || applyCalls != 1 {
				t.Fatalf("calls: Observe=%d Apply=%d, want Observe=2 Apply=1", observeCalls, applyCalls)
			}
		})
	}
}

func TestMutationRetryStopsAtConfiguredAttemptLimit(t *testing.T) {
	observation := knownObservation(samsung.PowerStateOn, "unknown-1")
	transient := func(message string) error {
		return &samsung.Error{
			Kind: samsung.ErrorKindInvalidResponse, Operation: "delete", Retryable: true,
			Outcome: samsung.OutcomeNotAttempted, Cause: errors.New(message),
		}
	}
	adapter := &fakeAdapter{
		observations: []samsung.Observation{observation, observation, observation},
		receipts: []samsung.Receipt{
			{Outcome: samsung.OutcomeNotAttempted},
			{Outcome: samsung.OutcomeNotAttempted},
		},
		applyErrs: []error{transient("first transient"), transient("second transient")},
	}
	service, err := New(Config{
		StateDirectory: filepath.Join(t.TempDir(), "state"), Policy: Policy{RemoveUnknown: true},
	}, Dependencies{
		Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler),
	}, WithMutationAttempts(2))
	if err != nil {
		t.Fatal(err)
	}

	result, runErr := service.Run(context.Background(), Request{
		CycleID: "retry-limit", TV: adapter, Snapshot: snapshot(),
	})
	if runErr == nil || result.Status != StatusNotApplied || result.State.Pending != nil {
		t.Fatalf("Run() = %+v, %v", result, runErr)
	}
	adapter.mu.Lock()
	observeCalls, applyCalls := adapter.observeCalls, adapter.applyCalls
	adapter.mu.Unlock()
	if observeCalls != 3 || applyCalls != 2 {
		t.Fatalf("calls: Observe=%d Apply=%d, want Observe=3 Apply=2", observeCalls, applyCalls)
	}
}

func TestMutationExecutionOptions(t *testing.T) {
	tests := []struct {
		name   string
		option Option
	}{
		{name: "negative pacing", option: WithMutationPacing(-time.Nanosecond)},
		{name: "excessive pacing", option: WithMutationPacing(maximumMutationPacing + time.Nanosecond)},
		{name: "zero attempts", option: WithMutationAttempts(0)},
		{name: "excessive attempts", option: WithMutationAttempts(maximumMutationAttempts + 1)},
		{name: "zero capacity TTL", option: WithCapacityEvidenceTTL(0)},
		{name: "excessive capacity TTL", option: WithCapacityEvidenceTTL(maximumCapacityTTL + time.Nanosecond)},
		{name: "nil option", option: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(Config{StateDirectory: filepath.Join(t.TempDir(), "state")}, Dependencies{}, test.option)
			if err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestMutationPacingDoesNotWaitForZeroDelayOrDryRun(t *testing.T) {
	tests := []struct {
		name    string
		delay   time.Duration
		dryRun  bool
		wantRun Status
	}{
		{name: "zero delay", wantRun: StatusComplete},
		{name: "dry run", delay: time.Second, dryRun: true, wantRun: StatusIncompleteDryRun},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := knownObservation(samsung.PowerStateOn, "unknown-1")
			adapter := &fakeAdapter{
				observations: []samsung.Observation{observation, observation},
				receipts: []samsung.Receipt{
					{CommandID: "delete", Outcome: samsung.OutcomeApplied, CompletedAt: testTime},
				},
			}
			waiter := newBlockingMutationWaiter()
			service, err := New(Config{
				StateDirectory: filepath.Join(t.TempDir(), "state"), Policy: Policy{RemoveUnknown: true},
			}, Dependencies{
				Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler),
			}, WithMutationPacing(test.delay), withMutationWaiter(waiter))
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Run(context.Background(), Request{
				CycleID: "no-pacing", TV: adapter, Snapshot: snapshot(), DryRun: test.dryRun,
			})
			if err != nil || result.Status != test.wantRun {
				t.Fatalf("Run() = %+v, %v", result, err)
			}
			select {
			case delay := <-waiter.calls:
				t.Fatalf("unexpected pacing wait: %s", delay)
			default:
			}
		})
	}
}

var _ mutationWaiter = (*blockingMutationWaiter)(nil)
