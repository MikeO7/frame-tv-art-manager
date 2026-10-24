package resources_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/resources"
)

func TestDefaultConfigBoundsTransformsAndPixelWorkers(t *testing.T) {
	t.Parallel()
	cfg := resources.DefaultConfig()
	if cfg.TransformConcurrency != 1 || cfg.TransformQueue != 2 {
		t.Fatalf("transform bounds = %d active/%d queued, want 1/2", cfg.TransformConcurrency, cfg.TransformQueue)
	}
	if cfg.PixelWorkers < 1 || cfg.PixelWorkers > 4 {
		t.Fatalf("PixelWorkers = %d, want within [1, 4]", cfg.PixelWorkers)
	}
	if got := resources.NewDefaultController().PixelWorkers(); got != cfg.PixelWorkers {
		t.Fatalf("default controller PixelWorkers() = %d, want %d", got, cfg.PixelWorkers)
	}
}

func TestRunRejectsInvalidRequestsWithoutRunningTransform(t *testing.T) {
	t.Parallel()
	transform := func(context.Context) error {
		t.Error("invalid request ran its transform")
		return nil
	}
	tests := []struct {
		name      string
		ctx       context.Context
		request   resources.Request
		transform func(context.Context) error
	}{
		{name: "nil context", request: resources.Request{Class: resources.Background}, transform: transform},
		{name: "nil transform", ctx: context.Background(), request: resources.Request{Class: resources.Background}},
		{name: "unknown class", ctx: context.Background(), request: resources.Request{Class: "batch"}, transform: transform},
		{
			name: "negative pixels", ctx: context.Background(),
			request: resources.Request{Class: resources.Interactive, InputPixels: -1}, transform: transform,
		},
		{
			name: "negative bytes", ctx: context.Background(),
			request: resources.Request{Class: resources.Interactive, InputBytes: -1}, transform: transform,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			controller := resources.NewDefaultController()
			if err := controller.Run(test.ctx, test.request, test.transform); err == nil {
				t.Fatal("Run() error = nil")
			}
			if snapshot := controller.Snapshot(); snapshot != (resources.Snapshot{}) {
				t.Fatalf("invalid request changed metrics: %+v", snapshot)
			}
		})
	}
}

func TestControllerBoundsActiveAndQueuedTransforms(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		controller := resources.NewDefaultController()
		release := make(chan struct{})
		results := make(chan error, 3)
		active := 0
		for range 3 {
			go func() {
				results <- controller.Run(context.Background(), resources.Request{
					Class: resources.Background, Mode: "ordinary",
				}, func(context.Context) error {
					active++
					if active > 1 {
						t.Error("more than one transform ran concurrently")
					}
					<-release
					active--
					return nil
				})
			}()
		}
		synctest.Wait()
		if snapshot := controller.Snapshot(); snapshot.Active != 1 || snapshot.Queued != 2 {
			t.Fatalf("admission = %+v, want one active and two queued", snapshot)
		}

		err := controller.Run(context.Background(), resources.Request{
			Class: resources.Interactive, Mode: "upload",
		}, func(context.Context) error {
			t.Error("overloaded transform ran")
			return nil
		})
		if !errors.Is(err, resources.ErrOverloaded) {
			t.Fatalf("Run() error = %v, want ErrOverloaded", err)
		}

		close(release)
		for range 3 {
			if err := <-results; err != nil {
				t.Fatalf("Run() error = %v", err)
			}
		}
		snapshot := controller.Snapshot()
		if snapshot.Completed != 3 || snapshot.Overloaded != 1 || snapshot.Active != 0 || snapshot.Queued != 0 {
			t.Fatalf("metrics = %+v, want 3 completed, 1 overloaded, and nothing admitted", snapshot)
		}
	})
}

func TestBackgroundAdmissionHonorsCancellation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		controller := resources.NewDefaultController()
		release := make(chan struct{})
		done := make(chan error, 3)
		for range 3 {
			go func() {
				done <- controller.Run(context.Background(), resources.Request{Class: resources.Background}, func(context.Context) error {
					<-release
					return nil
				})
			}()
		}
		synctest.Wait()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := controller.Run(ctx, resources.Request{Class: resources.Background}, func(context.Context) error {
			t.Error("canceled transform ran")
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}

		close(release)
		for range 3 {
			if err := <-done; err != nil {
				t.Fatalf("admitted Run() error = %v", err)
			}
		}
		if snapshot := controller.Snapshot(); snapshot.Canceled != 1 || snapshot.Completed != 3 {
			t.Fatalf("metrics = %+v, want one canceled and three completed", snapshot)
		}
	})
}

func TestQueuedCancellationReleasesCapacity(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		controller := resources.NewDefaultController()
		release := make(chan struct{})
		admittedDone := make(chan error, 3)
		run := func() {
			admittedDone <- controller.Run(context.Background(), resources.Request{Class: resources.Background}, func(context.Context) error {
				<-release
				return nil
			})
		}
		go run()
		go run()
		synctest.Wait()

		ctx, cancel := context.WithCancel(context.Background())
		queuedDone := make(chan error, 1)
		go func() {
			queuedDone <- controller.Run(ctx, resources.Request{Class: resources.Background}, func(context.Context) error {
				t.Error("canceled queued transform ran")
				return nil
			})
		}()
		synctest.Wait()
		if snapshot := controller.Snapshot(); snapshot.Active != 1 || snapshot.Queued != 2 {
			t.Fatalf("admission = %+v, want one active and two queued", snapshot)
		}
		cancel()
		if err := <-queuedDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("queued Run() error = %v, want context.Canceled", err)
		}
		if snapshot := controller.Snapshot(); snapshot.Queued != 1 {
			t.Fatalf("canceled queue entry was not released: %+v", snapshot)
		}

		go func() {
			admittedDone <- controller.Run(context.Background(), resources.Request{Class: resources.Interactive}, func(context.Context) error {
				return nil
			})
		}()
		synctest.Wait()
		if snapshot := controller.Snapshot(); snapshot.Queued != 2 || snapshot.Overloaded != 0 {
			t.Fatalf("interactive request after cancellation = %+v, want it queued in the released slot", snapshot)
		}
		close(release)
		for range 3 {
			if err := <-admittedDone; err != nil {
				t.Fatalf("admitted Run() error = %v", err)
			}
		}
	})
}

func TestControllerRecordsOperationMetrics(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		controller := resources.NewDefaultController()
		blocker := make(chan error, 1)
		go func() {
			blocker <- controller.Run(context.Background(), resources.Request{Class: resources.Background}, func(context.Context) error {
				time.Sleep(2 * time.Second)
				return nil
			})
		}()
		synctest.Wait()

		wantErr := errors.New("transform failed")
		err := controller.Run(context.Background(), resources.Request{
			Class: resources.Background, Mode: "museum", InputPixels: 8_294_400, InputBytes: 1_048_576,
		}, func(context.Context) error {
			time.Sleep(3 * time.Second)
			return wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("Run() error = %v, want %v", err, wantErr)
		}
		if err := <-blocker; err != nil {
			t.Fatalf("blocking Run() error = %v", err)
		}

		snapshot := controller.Snapshot()
		want := resources.Operation{
			Class: resources.Background, Mode: "museum", InputPixels: 8_294_400, InputBytes: 1_048_576,
			Wait: 2 * time.Second, Duration: 3 * time.Second, Outcome: resources.OutcomeFailed,
		}
		if snapshot.Last != want || snapshot.Failed != 1 || snapshot.Completed != 1 ||
			snapshot.TotalWait != 2*time.Second || snapshot.TotalDuration != 5*time.Second {
			t.Fatalf("Snapshot() = %+v, want last %+v", snapshot, want)
		}

		canceled := controller.Run(context.Background(), resources.Request{Class: resources.Interactive}, func(context.Context) error {
			return context.DeadlineExceeded
		})
		if !errors.Is(canceled, context.DeadlineExceeded) || controller.Snapshot().Last.Outcome != resources.OutcomeCanceled {
			t.Fatalf("deadline transform = %v, last %+v; want canceled outcome", canceled, controller.Snapshot().Last)
		}
	})
}
