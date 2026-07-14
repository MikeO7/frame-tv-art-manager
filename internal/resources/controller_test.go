package resources_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/resources"
)

func TestDefaultConfigBoundsTransformsAndPixelWorkers(t *testing.T) {
	cfg := resources.DefaultConfig()
	if cfg.TransformConcurrency != 1 {
		t.Fatalf("TransformConcurrency = %d, want 1", cfg.TransformConcurrency)
	}
	if cfg.TransformQueue != 2 {
		t.Fatalf("TransformQueue = %d, want 2", cfg.TransformQueue)
	}
	if cfg.PixelWorkers < 1 || cfg.PixelWorkers > 4 {
		t.Fatalf("PixelWorkers = %d, want within [1, 4]", cfg.PixelWorkers)
	}
}

func TestNewControllerRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  resources.Config
	}{
		{name: "zero transform concurrency", cfg: resources.Config{TransformQueue: 2, PixelWorkers: 1}},
		{name: "negative queue", cfg: resources.Config{TransformConcurrency: 1, TransformQueue: -1, PixelWorkers: 1}},
		{name: "zero pixel workers", cfg: resources.Config{TransformConcurrency: 1, TransformQueue: 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resources.NewController(test.cfg); err == nil {
				t.Fatal("NewController() error = nil")
			}
		})
	}
}

func TestControllerBoundsActiveAndQueuedTransforms(t *testing.T) {
	t.Parallel()
	controller := newTestController(t, resources.Config{
		TransformConcurrency: 1,
		TransformQueue:       2,
		PixelWorkers:         1,
	})

	release := make(chan struct{})
	started := make(chan struct{}, 3)
	results := make(chan error, 3)
	var active atomic.Int32
	var maximum atomic.Int32

	transform := func(context.Context) error {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			prior := maximum.Load()
			if current <= prior || maximum.CompareAndSwap(prior, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		return nil
	}

	for range 3 {
		go func() {
			results <- controller.Run(context.Background(), resources.Request{
				Class: resources.Background,
				Mode:  "ordinary",
			}, transform)
		}()
	}

	waitForSnapshot(t, controller, func(snapshot resources.Snapshot) bool {
		return snapshot.Active == 1 && snapshot.Queued == 2
	})

	err := controller.Run(context.Background(), resources.Request{
		Class: resources.Interactive,
		Mode:  "upload",
	}, func(context.Context) error {
		t.Fatal("overloaded transform ran")
		return nil
	})
	if !errors.Is(err, resources.ErrOverloaded) {
		t.Fatalf("Run() error = %v, want ErrOverloaded", err)
	}

	close(release)
	for range 3 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for transform")
		}
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum active transforms = %d, want 1", got)
	}

	snapshot := controller.Snapshot()
	if snapshot.Completed != 3 || snapshot.Overloaded != 1 {
		t.Fatalf("metrics = %+v, want 3 completed and 1 overloaded", snapshot)
	}
}

func TestBackgroundAdmissionHonorsCancellation(t *testing.T) {
	t.Parallel()
	controller := newTestController(t, resources.Config{
		TransformConcurrency: 1,
		TransformQueue:       0,
		PixelWorkers:         1,
	})

	release := make(chan struct{})
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- controller.Run(context.Background(), resources.Request{Class: resources.Background}, func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := controller.Run(ctx, resources.Request{Class: resources.Background}, func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("canceled transform ran")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if got := controller.Snapshot().Canceled; got != 1 {
		t.Fatalf("Canceled = %d, want 1", got)
	}
}

func TestQueuedCancellationReleasesCapacity(t *testing.T) {
	t.Parallel()
	controller := newTestController(t, resources.Config{
		TransformConcurrency: 1,
		TransformQueue:       1,
		PixelWorkers:         1,
	})

	release := make(chan struct{})
	activeDone := make(chan error, 1)
	go func() {
		activeDone <- controller.Run(context.Background(), resources.Request{Class: resources.Background}, func(context.Context) error {
			<-release
			return nil
		})
	}()
	waitForSnapshot(t, controller, func(snapshot resources.Snapshot) bool { return snapshot.Active == 1 })

	ctx, cancel := context.WithCancel(context.Background())
	queuedDone := make(chan error, 1)
	go func() {
		queuedDone <- controller.Run(ctx, resources.Request{Class: resources.Background}, func(context.Context) error {
			t.Error("canceled queued transform ran")
			return nil
		})
	}()
	waitForSnapshot(t, controller, func(snapshot resources.Snapshot) bool { return snapshot.Queued == 1 })
	cancel()
	if err := <-queuedDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued Run() error = %v, want context.Canceled", err)
	}
	waitForSnapshot(t, controller, func(snapshot resources.Snapshot) bool { return snapshot.Queued == 0 })

	close(release)
	if err := <-activeDone; err != nil {
		t.Fatalf("active Run() error = %v", err)
	}
}

func TestControllerRecordsOperationMetrics(t *testing.T) {
	t.Parallel()
	controller := newTestController(t, resources.DefaultConfig())
	wantErr := errors.New("transform failed")
	err := controller.Run(context.Background(), resources.Request{
		Class:       resources.Interactive,
		Mode:        "museum",
		InputPixels: 8_294_400,
		InputBytes:  1_048_576,
	}, func(context.Context) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}

	snapshot := controller.Snapshot()
	if snapshot.Failed != 1 || snapshot.Last.Class != resources.Interactive ||
		snapshot.Last.Mode != "museum" || snapshot.Last.InputPixels != 8_294_400 ||
		snapshot.Last.InputBytes != 1_048_576 || snapshot.Last.Outcome != resources.OutcomeFailed {
		t.Fatalf("Snapshot() = %+v", snapshot)
	}
	if snapshot.Last.Wait < 0 || snapshot.Last.Duration < 0 {
		t.Fatalf("negative timing in %+v", snapshot.Last)
	}
}

func newTestController(t *testing.T, cfg resources.Config) *resources.Controller {
	t.Helper()
	controller, err := resources.NewController(cfg)
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	return controller
}

func waitForSnapshot(t *testing.T, controller *resources.Controller, ready func(resources.Snapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready(controller.Snapshot()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not reached; snapshot = %+v", controller.Snapshot())
}

func TestControllerSupportsConcurrentSnapshots(t *testing.T) {
	t.Parallel()
	controller := newTestController(t, resources.DefaultConfig())
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = controller.Snapshot()
		}()
	}
	wg.Wait()
}
