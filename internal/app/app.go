// Package app supervises the application's startup, child loops, and shutdown.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// New validates and captures the dependencies required for one application
// lifetime.
func New(options Options) (*Application, error) {
	if options.RunCycle == nil {
		return nil, errors.New("run Sync Cycle function is required")
	}
	if options.ShutdownTimeout < 0 {
		return nil, errors.New("shutdown timeout must not be negative")
	}
	if options.Prepare == nil {
		options.Prepare = func(context.Context) error { return nil }
	}
	if options.SetState == nil {
		options.SetState = func(State) {}
	}
	if options.ShutdownTimeout == 0 {
		options.ShutdownTimeout = defaultShutdownTimeout
	}

	return &Application{
		prepare:         options.Prepare,
		runCycle:        options.RunCycle,
		bindHTTP:        options.BindHTTP,
		setState:        options.SetState,
		closers:         append([]ResourceCloser(nil), options.Closers...),
		shutdownTimeout: options.ShutdownTimeout,
	}, nil
}

// Run starts and supervises the application until cancellation or a terminal
// child error.
//
//nolint:funlen // Early HTTP liveness, preparation, child startup, and shared shutdown form one lifecycle.
func (a *Application) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("run context is required")
	}
	if !a.run.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}

	a.setState(StateStarting)
	var server HTTPServer
	if a.bindHTTP != nil {
		var err error
		server, err = a.bindServer(ctx)
		if err != nil {
			a.setState(StateFailed)
			return a.startupFailure(ctx, err)
		}
	}

	childCtx, cancelChildren := context.WithCancel(ctx)
	defer cancelChildren()
	results := make(chan childResult, 2)
	childCount := 0
	if server != nil {
		childCount++
		go func() {
			results <- childResult{kind: childHTTP, err: server.Serve()}
		}()
	}
	if err := a.prepareStart(ctx); err != nil {
		a.setState(StateFailed)
		cancelChildren()
		if server == nil {
			return a.startupFailure(ctx, err)
		}
		shutdownErr := a.shutdown(ctx, server, results, childCount, make(map[childKind]error, childCount))
		return errors.Join(err, shutdownErr)
	}
	childCount++
	go func() {
		results <- childResult{kind: childCycle, err: a.runCycle(childCtx)}
	}()
	a.setState(StateReady)

	completed := make(map[childKind]error, childCount)
	terminal, failed := a.waitForStop(ctx, results, completed)

	cancelChildren()
	shutdownErr := a.shutdown(ctx, server, results, childCount, completed)
	result := errors.Join(terminal, shutdownErr)
	if result != nil && !failed {
		a.setState(StateFailed)
	}
	return result
}

func (a *Application) prepareStart(ctx context.Context) error {
	if err := a.prepare(ctx); err != nil {
		return fmt.Errorf("prepare application: %w", err)
	}
	return nil
}

func (a *Application) bindServer(ctx context.Context) (HTTPServer, error) {
	server, err := a.bindHTTP(ctx)
	if err != nil {
		return nil, fmt.Errorf("bind HTTP server: %w", err)
	}
	if server == nil {
		return nil, errors.New("bind HTTP server: returned a nil server")
	}
	return server, nil
}

func (a *Application) waitForStop(
	ctx context.Context,
	results <-chan childResult,
	completed map[childKind]error,
) (error, bool) {
	select {
	case <-ctx.Done():
		a.setState(StateStopping)
		return nil, false
	case result := <-results:
		completed[result.kind] = result.err
		select {
		case <-ctx.Done():
			a.setState(StateStopping)
			return nil, false
		default:
		}
		terminal := unexpectedChildError(result)
		completed[result.kind] = expectedChildStop(result.kind)
		a.setState(StateFailed)
		return terminal, true
	}
}

func expectedChildStop(kind childKind) error {
	if kind == childHTTP {
		return http.ErrServerClosed
	}
	return context.Canceled
}

type childKind uint8

const (
	childCycle childKind = iota
	childHTTP
)

type childResult struct {
	kind childKind
	err  error
}

func (a *Application) shutdown(
	runCtx context.Context,
	server HTTPServer,
	results <-chan childResult,
	childCount int,
	completed map[childKind]error,
) error {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(runCtx), a.shutdownTimeout)
	defer cancel()

	var shutdownResult <-chan error
	shutdownDone := server == nil
	if server != nil {
		channel := make(chan error, 1)
		shutdownResult = channel
		go func() { channel <- server.Shutdown(shutdownCtx) }()
	}

	var shutdownErr error
	for len(completed) < childCount || !shutdownDone {
		select {
		case result := <-results:
			completed[result.kind] = result.err
		case shutdownErr = <-shutdownResult:
			shutdownDone = true
			shutdownResult = nil
		case <-shutdownCtx.Done():
			return a.finishShutdown(shutdownCtx, server, completed, shutdownErr, true)
		}
	}

	return a.finishShutdown(shutdownCtx, server, completed, shutdownErr, false)
}

func (a *Application) finishShutdown(
	ctx context.Context,
	server HTTPServer,
	completed map[childKind]error,
	shutdownErr error,
	timedOut bool,
) error {
	errs := childShutdownErrors(completed)
	errs = append(errs, serverShutdownErrors(server, shutdownErr, timedOut)...)
	if timedOut {
		errs = append(errs, ErrShutdownTimeout)
	}
	errs = append(errs, a.closeResources(ctx)...)
	return errors.Join(errs...)
}

func childShutdownErrors(completed map[childKind]error) []error {
	var errs []error
	if err, exists := completed[childCycle]; exists && !isExpectedCancellation(err) {
		errs = append(errs, fmt.Errorf("stop Sync Cycle: %w", err))
	}
	if err, exists := completed[childHTTP]; exists && !errors.Is(err, http.ErrServerClosed) {
		errs = append(errs, fmt.Errorf("stop HTTP server: %w", childExitError(err)))
	}
	return errs
}

func serverShutdownErrors(server HTTPServer, shutdownErr error, timedOut bool) []error {
	var errs []error
	if shutdownErr != nil {
		errs = append(errs, fmt.Errorf("shutdown HTTP server: %w", shutdownErr))
	}
	if server == nil || (!timedOut && !errors.Is(shutdownErr, context.DeadlineExceeded)) {
		return errs
	}
	if err := server.Close(); err != nil {
		errs = append(errs, fmt.Errorf("force close HTTP server: %w", err))
	}
	return errs
}

func (a *Application) startupFailure(runCtx context.Context, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(runCtx), a.shutdownTimeout)
	defer cancel()
	return errors.Join(cause, errors.Join(a.closeResources(ctx)...))
}

func (a *Application) closeResources(ctx context.Context) []error {
	var errs []error
	for index, closer := range a.closers {
		if closer == nil {
			continue
		}
		if err := closer.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close resource %d: %w", index, err))
		}
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("close resource %d: %w", index, errors.Join(ErrShutdownTimeout, err)))
			return errs
		}
	}
	return errs
}

func unexpectedChildError(result childResult) error {
	name := "Sync Cycle"
	if result.kind == childHTTP {
		name = "HTTP server"
	}
	return fmt.Errorf("%s terminated: %w", name, childExitError(result.err))
}

func childExitError(err error) error {
	if err == nil {
		return ErrChildExited
	}
	return err
}

func isExpectedCancellation(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
