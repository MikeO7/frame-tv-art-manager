package app

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

const defaultShutdownTimeout = 30 * time.Second

var (
	// ErrAlreadyRun reports an attempt to reuse an Application. Each instance
	// deliberately owns exactly one child lifetime.
	ErrAlreadyRun = errors.New("application has already run")
	// ErrChildExited reports a child that stopped without a terminal error before
	// shutdown began.
	ErrChildExited = errors.New("application child exited unexpectedly")
	// ErrShutdownTimeout reports children that did not stop within the shared
	// shutdown budget.
	ErrShutdownTimeout = errors.New("application shutdown timed out")
)

// State describes the process lifecycle independently of Sync Cycle health.
type State string

const (
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

// HTTPServer is an already-bound server owned by Application for one Run.
type HTTPServer interface {
	Serve() error
	Shutdown(context.Context) error
	Close() error
}

// ResourceCloser releases an application-owned resource within the shared
// supervisor shutdown budget.
type ResourceCloser interface {
	Close(context.Context) error
}

// Options provides the narrow boundaries supervised by an Application.
type Options struct {
	Prepare         func(context.Context) error
	RunCycle        func(context.Context) error
	BindHTTP        func(context.Context) (HTTPServer, error)
	SetState        func(State)
	Closers         []ResourceCloser
	ShutdownTimeout time.Duration
}

// Application owns one application lifetime.
type Application struct {
	prepare         func(context.Context) error
	runCycle        func(context.Context) error
	bindHTTP        func(context.Context) (HTTPServer, error)
	setState        func(State)
	closers         []ResourceCloser
	shutdownTimeout time.Duration
	run             atomic.Bool
}
