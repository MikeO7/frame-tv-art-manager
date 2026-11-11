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

		// The freed place admits one more background transform; a gate that
		// leaked the canceled place would hold it out until its context ended.
		admitted := make(chan error, 1)
		go func() {
			admitted <- gate.run(context.Background(), false, func(context.Context) error { return nil })
		}()
		synctest.Wait()
		if len(gate.places) != transformsRunning+transformsWaiting {
			t.Fatalf("occupied places = %d, want the freed place to be taken again", len(gate.places))
		}
		close(release)
		if err := <-admitted; err != nil {
			t.Fatalf("background run after a canceled wait = %v, want admission", err)
		}
		drain(t, results, transformsRunning+transformsWaiting-1)
	})
}

func TestTransformGateRefusesInteractiveWorkWhileATransformRuns(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		gate := newTransformGate()
		release := make(chan struct{})
		// One long background transform, such as a whole-collection
		// optimization, with waiting places still free.
		results := fillGate(gate, transformsRunning, release, nil)
		synctest.Wait()

		refused := make(chan error, 1)
		go func() {
			refused <- gate.run(context.Background(), true, func(context.Context) error {
				t.Error("interactive transform ran beside a running transform")
				return nil
			})
		}()
		synctest.Wait()
		select {
		case err := <-refused:
			if !errors.Is(err, errTransformsBusy) {
				t.Fatalf("interactive run beside a running transform = %v, want errTransformsBusy", err)
			}
		default:
			t.Fatal("interactive transform waited behind the running transform instead of being refused")
		}
		if len(gate.places) != transformsRunning {
			t.Fatalf("occupied places = %d, want the refused transform to give its place back", len(gate.places))
		}

		close(release)
		drain(t, results, transformsRunning)
		ran := false
		if err := gate.run(context.Background(), true, func(context.Context) error {
			ran = true
			return nil
		}); err != nil || !ran {
			t.Fatalf("interactive run on an idle gate = %v, ran = %v; want it to run", err, ran)
		}
	})
}
