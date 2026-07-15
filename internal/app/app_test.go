package app_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/app"
)

func TestApplicationPreparationFailurePreventsChildrenFromStarting(t *testing.T) {
	t.Parallel()

	prepareErr := errors.New("collection recovery failed")
	cycleStarted := false
	states := make([]app.State, 0, 2)
	application, err := app.New(app.Options{
		Prepare: func(context.Context) error { return prepareErr },
		RunCycle: func(context.Context) error {
			cycleStarted = true
			return nil
		},
		SetState: func(state app.State) { states = append(states, state) },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = application.Run(context.Background())
	if !errors.Is(err, prepareErr) {
		t.Fatalf("Run() error = %v, want preparation error", err)
	}
	if cycleStarted {
		t.Fatal("RunCycle started after preparation failed")
	}
	wantStates := []app.State{app.StateStarting, app.StateFailed}
	if !equalStates(states, wantStates) {
		t.Fatalf("states = %v, want %v", states, wantStates)
	}
}

func TestApplicationPreparationFailureClosesOwnedResources(t *testing.T) {
	t.Parallel()

	prepareErr := errors.New("prepare failed")
	closed := make(chan struct{})
	application, err := app.New(app.Options{
		Prepare:  func(context.Context) error { return prepareErr },
		RunCycle: func(context.Context) error { return nil },
		Closers: []app.ResourceCloser{closerFunc(func(context.Context) error {
			close(closed)
			return nil
		})},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := application.Run(context.Background()); !errors.Is(err, prepareErr) {
		t.Fatalf("Run() error = %v, want prepare error", err)
	}
	waitClosed(t, closed, "resource cleanup")
}

func TestApplicationServesLivenessWhilePreparationRuns(t *testing.T) {
	t.Parallel()

	server := newBlockingHTTPServer()
	allowPrepare := make(chan struct{})
	prepareStarted := make(chan struct{})
	cycleStarted := make(chan struct{})
	application, err := app.New(app.Options{
		Prepare: func(ctx context.Context) error {
			close(prepareStarted)
			select {
			case <-allowPrepare:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		RunCycle: func(ctx context.Context) error {
			close(cycleStarted)
			<-ctx.Done()
			return ctx.Err()
		},
		BindHTTP: func(context.Context) (app.HTTPServer, error) { return server, nil },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- application.Run(ctx) }()
	waitClosed(t, prepareStarted, "preparation start")
	waitClosed(t, server.serveStarted, "HTTP serve during preparation")
	select {
	case <-cycleStarted:
		t.Fatal("Sync Cycle started before preparation completed")
	default:
	}
	close(allowPrepare)
	waitClosed(t, cycleStarted, "Sync Cycle start")
	cancel()
	close(server.allowShutdown)
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
}

func TestApplicationPreparationFailureShutsDownStartedHTTP(t *testing.T) {
	t.Parallel()

	prepareErr := errors.New("inventory failed")
	server := newBlockingHTTPServer()
	close(server.allowShutdown)
	application, err := app.New(app.Options{
		Prepare:  func(context.Context) error { return prepareErr },
		RunCycle: func(context.Context) error { return nil },
		BindHTTP: func(context.Context) (app.HTTPServer, error) { return server, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Run(context.Background()); !errors.Is(err, prepareErr) {
		t.Fatalf("Run() error = %v, want preparation error", err)
	}
	waitClosed(t, server.serveStarted, "HTTP serve start")
	waitClosed(t, server.shutdownStarted, "HTTP shutdown")
	waitClosed(t, server.serveStopped, "HTTP serve stop")
}

func TestApplicationBindFailurePreventsChildrenFromStarting(t *testing.T) {
	t.Parallel()

	bindErr := errors.New("address already in use")
	cycleStarted := false
	states := make([]app.State, 0, 2)
	application, err := app.New(app.Options{
		RunCycle: func(context.Context) error {
			cycleStarted = true
			return nil
		},
		BindHTTP: func(context.Context) (app.HTTPServer, error) {
			return nil, bindErr
		},
		SetState: func(state app.State) { states = append(states, state) },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = application.Run(context.Background())
	if !errors.Is(err, bindErr) {
		t.Fatalf("Run() error = %v, want bind error", err)
	}
	if cycleStarted {
		t.Fatal("RunCycle started after HTTP bind failed")
	}
	wantStates := []app.State{app.StateStarting, app.StateFailed}
	if !equalStates(states, wantStates) {
		t.Fatalf("states = %v, want %v", states, wantStates)
	}
}

func TestApplicationCancellationStopsCycleCleanly(t *testing.T) {
	t.Parallel()

	cycleStarted := make(chan struct{})
	cycleStopped := make(chan struct{})
	var states stateRecorder
	application, err := app.New(app.Options{
		RunCycle: func(ctx context.Context) error {
			close(cycleStarted)
			<-ctx.Done()
			close(cycleStopped)
			return ctx.Err()
		},
		SetState: states.set,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- application.Run(ctx) }()

	waitClosed(t, cycleStarted, "Sync Cycle start")
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not return after cancellation")
	}
	waitClosed(t, cycleStopped, "Sync Cycle stop")
	wantStates := []app.State{app.StateStarting, app.StateReady, app.StateStopping}
	if got := states.snapshot(); !equalStates(got, wantStates) {
		t.Fatalf("states = %v, want %v", got, wantStates)
	}
}

func TestApplicationOwnsHTTPAndCycleUntilShutdownCompletes(t *testing.T) {
	t.Parallel()

	server := newBlockingHTTPServer()
	cycleStarted := make(chan struct{})
	cycleStopped := make(chan struct{})
	var cycleStarts atomic.Int32
	application, err := app.New(app.Options{
		RunCycle: func(ctx context.Context) error {
			cycleStarts.Add(1)
			close(cycleStarted)
			<-ctx.Done()
			close(cycleStopped)
			return ctx.Err()
		},
		BindHTTP: func(context.Context) (app.HTTPServer, error) { return server, nil },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- application.Run(ctx) }()
	waitClosed(t, cycleStarted, "Sync Cycle start")
	waitClosed(t, server.serveStarted, "HTTP serve start")

	cancel()
	waitClosed(t, server.shutdownStarted, "HTTP shutdown start")
	waitClosed(t, cycleStopped, "Sync Cycle stop")
	waitClosed(t, server.serveStopped, "HTTP serve stop")
	select {
	case err := <-result:
		t.Fatalf("Run() returned before HTTP shutdown completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(server.allowShutdown)

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not return after HTTP shutdown completed")
	}
	if got := cycleStarts.Load(); got != 1 {
		t.Fatalf("Sync Cycle starts = %d, want 1", got)
	}
	if got := server.serveCalls.Load(); got != 1 {
		t.Fatalf("HTTP Serve calls = %d, want 1", got)
	}
	if got := server.shutdownCalls.Load(); got != 1 {
		t.Fatalf("HTTP Shutdown calls = %d, want 1", got)
	}
	if got := server.closeCalls.Load(); got != 0 {
		t.Fatalf("HTTP Close calls = %d, want 0", got)
	}
}

func TestApplicationReturnsUnexpectedCycleErrorAndStopsHTTP(t *testing.T) {
	t.Parallel()

	cycleErr := errors.New("reconciliation journal corrupt")
	releaseCycle := make(chan struct{})
	server := newBlockingHTTPServer()
	close(server.allowShutdown)
	var states stateRecorder
	application, err := app.New(app.Options{
		RunCycle: func(context.Context) error {
			<-releaseCycle
			return cycleErr
		},
		BindHTTP: func(context.Context) (app.HTTPServer, error) { return server, nil },
		SetState: states.set,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result := make(chan error, 1)
	go func() { result <- application.Run(context.Background()) }()
	waitClosed(t, server.serveStarted, "HTTP serve start")
	close(releaseCycle)

	select {
	case err := <-result:
		if !errors.Is(err, cycleErr) {
			t.Fatalf("Run() error = %v, want Sync Cycle error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not return after Sync Cycle terminated")
	}
	waitClosed(t, server.shutdownStarted, "HTTP shutdown")
	waitClosed(t, server.serveStopped, "HTTP serve stop")
	wantStates := []app.State{app.StateStarting, app.StateReady, app.StateFailed}
	if got := states.snapshot(); !equalStates(got, wantStates) {
		t.Fatalf("states = %v, want %v", got, wantStates)
	}
}

func TestApplicationReturnsUnexpectedHTTPErrorAndStopsCycle(t *testing.T) {
	t.Parallel()

	serveErr := errors.New("accept failed")
	server := &failingHTTPServer{
		serveStarted: make(chan struct{}),
		releaseServe: make(chan struct{}),
	}
	cycleStopped := make(chan struct{})
	application, err := app.New(app.Options{
		RunCycle: func(ctx context.Context) error {
			<-ctx.Done()
			close(cycleStopped)
			return ctx.Err()
		},
		BindHTTP: func(context.Context) (app.HTTPServer, error) { return server, nil },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result := make(chan error, 1)
	go func() { result <- application.Run(context.Background()) }()
	waitClosed(t, server.serveStarted, "HTTP serve start")
	server.serveErr = serveErr
	close(server.releaseServe)

	select {
	case err := <-result:
		if !errors.Is(err, serveErr) {
			t.Fatalf("Run() error = %v, want HTTP serve error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not return after HTTP server terminated")
	}
	waitClosed(t, cycleStopped, "Sync Cycle stop")
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options app.Options
	}{
		{name: "missing Sync Cycle"},
		{
			name: "negative shutdown timeout",
			options: app.Options{
				RunCycle:        func(context.Context) error { return nil },
				ShutdownTimeout: -time.Second,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := app.New(test.options); err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
		})
	}
}

func TestApplicationRejectsReuse(t *testing.T) {
	t.Parallel()

	application, err := app.New(app.Options{
		RunCycle: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := application.Run(ctx); err != nil {
		t.Fatalf("first Run() error = %v, want nil", err)
	}
	if err := application.Run(context.Background()); !errors.Is(err, app.ErrAlreadyRun) {
		t.Fatalf("second Run() error = %v, want ErrAlreadyRun", err)
	}
}

func TestApplicationRejectsNilBoundServer(t *testing.T) {
	t.Parallel()

	application, err := app.New(app.Options{
		RunCycle: func(context.Context) error { return nil },
		BindHTTP: func(context.Context) (app.HTTPServer, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := application.Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want nil-server error")
	}
}

func TestApplicationTreatsEarlyCleanChildExitAsTerminal(t *testing.T) {
	t.Parallel()

	application, err := app.New(app.Options{
		RunCycle: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := application.Run(context.Background()); !errors.Is(err, app.ErrChildExited) {
		t.Fatalf("Run() error = %v, want ErrChildExited", err)
	}
}

func TestApplicationPreservesShutdownAndCleanupErrors(t *testing.T) {
	t.Parallel()

	shutdownErr := errors.New("listener durability failure")
	cleanupErr := errors.New("TV transport close failure")
	server := newErrorHTTPServer(shutdownErr)
	application, err := app.New(app.Options{
		RunCycle: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		BindHTTP: func(context.Context) (app.HTTPServer, error) { return server, nil },
		Closers: []app.ResourceCloser{
			nil,
			closerFunc(func(context.Context) error { return cleanupErr }),
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- application.Run(ctx) }()
	waitClosed(t, server.started, "HTTP serve start")
	cancel()
	err = <-result
	if !errors.Is(err, shutdownErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Run() error = %v, want joined shutdown and cleanup errors", err)
	}
}

func TestApplicationForcesHTTPClosedAtSharedDeadline(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("forced connection close failed")
	server := newTimeoutHTTPServer(closeErr)
	application, err := app.New(app.Options{
		RunCycle: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		BindHTTP:        func(context.Context) (app.HTTPServer, error) { return server, nil },
		ShutdownTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- application.Run(ctx) }()
	waitClosed(t, server.started, "HTTP serve start")
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, app.ErrShutdownTimeout) || !errors.Is(err, closeErr) {
			t.Fatalf("Run() error = %v, want timeout and forced-close errors", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() exceeded bounded shutdown deadline")
	}
	if got := server.closeCalls.Load(); got != 1 {
		t.Fatalf("HTTP Close calls = %d, want 1", got)
	}
}

func TestApplicationBoundsBlockingResourceCleanup(t *testing.T) {
	t.Parallel()

	unblock := make(chan struct{})
	defer close(unblock)
	application, err := app.New(app.Options{
		RunCycle: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		Closers: []app.ResourceCloser{closerFunc(func(ctx context.Context) error {
			select {
			case <-unblock:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})},
		ShutdownTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- application.Run(ctx) }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, app.ErrShutdownTimeout) {
			t.Fatalf("Run() error = %v, want ErrShutdownTimeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() exceeded cleanup deadline")
	}
}

func equalStates(got, want []app.State) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type stateRecorder struct {
	mu     sync.Mutex
	states []app.State
}

type closerFunc func(context.Context) error

func (fn closerFunc) Close(ctx context.Context) error { return fn(ctx) }

func (r *stateRecorder) set(state app.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, state)
}

func (r *stateRecorder) snapshot() []app.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]app.State(nil), r.states...)
}

func waitClosed(t *testing.T, channel <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

type blockingHTTPServer struct {
	serveStarted    chan struct{}
	serveStopped    chan struct{}
	shutdownStarted chan struct{}
	allowShutdown   chan struct{}
	stopServe       chan struct{}
	serveCalls      atomic.Int32
	shutdownCalls   atomic.Int32
	closeCalls      atomic.Int32
	stopOnce        sync.Once
}

type failingHTTPServer struct {
	serveStarted chan struct{}
	releaseServe chan struct{}
	serveErr     error
}

func (s *failingHTTPServer) Serve() error {
	close(s.serveStarted)
	<-s.releaseServe
	return s.serveErr
}

func (*failingHTTPServer) Shutdown(context.Context) error { return nil }

func (*failingHTTPServer) Close() error { return nil }

type errorHTTPServer struct {
	started     chan struct{}
	stop        chan struct{}
	shutdownErr error
}

func newErrorHTTPServer(shutdownErr error) *errorHTTPServer {
	return &errorHTTPServer{
		started:     make(chan struct{}),
		stop:        make(chan struct{}),
		shutdownErr: shutdownErr,
	}
}

func (s *errorHTTPServer) Serve() error {
	close(s.started)
	<-s.stop
	return http.ErrServerClosed
}

func (s *errorHTTPServer) Shutdown(context.Context) error {
	close(s.stop)
	return s.shutdownErr
}

func (*errorHTTPServer) Close() error { return nil }

type timeoutHTTPServer struct {
	started    chan struct{}
	stop       chan struct{}
	closeErr   error
	closeCalls atomic.Int32
	stopOnce   sync.Once
}

func newTimeoutHTTPServer(closeErr error) *timeoutHTTPServer {
	return &timeoutHTTPServer{
		started:  make(chan struct{}),
		stop:     make(chan struct{}),
		closeErr: closeErr,
	}
}

func (s *timeoutHTTPServer) Serve() error {
	close(s.started)
	<-s.stop
	return http.ErrServerClosed
}

func (*timeoutHTTPServer) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *timeoutHTTPServer) Close() error {
	s.closeCalls.Add(1)
	s.stopOnce.Do(func() { close(s.stop) })
	return s.closeErr
}

func newBlockingHTTPServer() *blockingHTTPServer {
	return &blockingHTTPServer{
		serveStarted:    make(chan struct{}),
		serveStopped:    make(chan struct{}),
		shutdownStarted: make(chan struct{}),
		allowShutdown:   make(chan struct{}),
		stopServe:       make(chan struct{}),
	}
}

func (s *blockingHTTPServer) Serve() error {
	s.serveCalls.Add(1)
	close(s.serveStarted)
	<-s.stopServe
	close(s.serveStopped)
	return http.ErrServerClosed
}

func (s *blockingHTTPServer) Shutdown(context.Context) error {
	s.shutdownCalls.Add(1)
	close(s.shutdownStarted)
	s.stopOnce.Do(func() { close(s.stopServe) })
	<-s.allowShutdown
	return nil
}

func (s *blockingHTTPServer) Close() error {
	s.closeCalls.Add(1)
	s.stopOnce.Do(func() { close(s.stopServe) })
	return nil
}
