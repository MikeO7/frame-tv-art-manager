package sync

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// fillGate starts background transforms that block until release is closed,
// and returns their results. running counts the transforms inside the gate.
func fillGate(gate *transformGate, count int, release <-chan struct{}, running *atomic.Int32) <-chan error {
	results := make(chan error, count)
	for range count {
		go func() {
			results <- gate.run(context.Background(), false, func(context.Context) error {
				if running != nil {
					running.Add(1)
					defer running.Add(-1)
				}
				<-release
				return nil
			})
		}()
	}
	return results
}

func drain(t *testing.T, results <-chan error, count int) {
	t.Helper()
	for range count {
		if err := <-results; err != nil {
			t.Fatalf("admitted transform error = %v", err)
		}
	}
}

func TestTransformGateRunsOneAndRefusesInteractiveWorkWhenFull(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		gate := newTransformGate()
		release := make(chan struct{})
		var running atomic.Int32
		results := fillGate(gate, transformsRunning+transformsWaiting, release, &running)
		synctest.Wait()
		if got := running.Load(); got != transformsRunning {
			t.Fatalf("running transforms = %d, want %d", got, transformsRunning)
		}

		err := gate.run(context.Background(), true, func(context.Context) error {
			t.Error("refused interactive transform ran")
			return nil
		})
		if !errors.Is(err, errTransformsBusy) {
			t.Fatalf("interactive run on a full gate = %v, want errTransformsBusy", err)
		}
		close(release)
		drain(t, results, transformsRunning+transformsWaiting)
	})
}

func TestTransformGateBackgroundWaitEndsWithItsContext(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		gate := newTransformGate()
		release := make(chan struct{})
		results := fillGate(gate, transformsRunning+transformsWaiting, release, nil)
		synctest.Wait()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := gate.run(ctx, false, func(context.Context) error {
			t.Error("canceled transform ran")
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled background run = %v, want context.Canceled", err)
		}
		close(release)
		drain(t, results, transformsRunning+transformsWaiting)
	})
}

func TestTransformGateFreesTheWaitingPlaceOfACanceledTransform(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		gate := newTransformGate()
		release := make(chan struct{})
		results := fillGate(gate, transformsRunning+transformsWaiting-1, release, nil)
		synctest.Wait()

		ctx, cancel := context.WithCancel(context.Background())
		waiting := make(chan error, 1)
		go func() {
			waiting <- gate.run(ctx, false, func(context.Context) error {
				t.Error("canceled waiting transform ran")
				return nil
			})
		}()
		synctest.Wait()
		cancel()
		if err := <-waiting; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiting run = %v, want context.Canceled", err)
		}

		interactive := make(chan error, 1)
		go func() {
			interactive <- gate.run(context.Background(), true, func(context.Context) error { return nil })
		}()
		synctest.Wait()
		close(release)
		if err := <-interactive; err != nil {
			t.Fatalf("interactive run after a canceled wait = %v, want admission", err)
		}
		drain(t, results, transformsRunning+transformsWaiting-1)
	})
}
