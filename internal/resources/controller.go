// Package resources bounds expensive process-wide work and exposes immutable
// admission and Go runtime metrics.
package resources

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
)

// ErrOverloaded classifies work that cannot enter the bounded interactive
// queue. Callers may safely report it as retryable.
var ErrOverloaded = errors.New("resource controller overloaded")

// Config defines process-wide transform and per-transform CPU bounds.
type Config struct {
	TransformConcurrency int
	TransformQueue       int
	PixelWorkers         int
}

// DefaultConfig returns the production resource envelope.
func DefaultConfig() Config {
	return Config{
		TransformConcurrency: 1,
		TransformQueue:       2,
		PixelWorkers:         min(max(runtime.GOMAXPROCS(0), 1), 4),
	}
}

// Class controls admission behavior when all bounded queue positions are in
// use. Background work waits for capacity with context cancellation;
// interactive work returns ErrOverloaded immediately.
type Class string

const (
	Background  Class = "background"
	Interactive Class = "interactive"
)

// Request describes one transform without retaining artwork bytes in the
// controller queue.
type Request struct {
	Class       Class
	Mode        string
	InputPixels int64
	InputBytes  int64
}

// Outcome is the observed completion class of a transform request.
type Outcome string

const (
	OutcomeCompleted  Outcome = "completed"
	OutcomeFailed     Outcome = "failed"
	OutcomeCanceled   Outcome = "canceled"
	OutcomeOverloaded Outcome = "overloaded"
)

// Operation is the last completed admission observation.
type Operation struct {
	Class       Class
	Mode        string
	InputPixels int64
	InputBytes  int64
	Wait        time.Duration
	Duration    time.Duration
	Outcome     Outcome
}

// Snapshot is an immutable point-in-time view of transform admission.
type Snapshot struct {
	Active        int
	Queued        int
	Completed     uint64
	Failed        uint64
	Canceled      uint64
	Overloaded    uint64
	TotalWait     time.Duration
	TotalDuration time.Duration
	Last          Operation
}

// Controller owns process-wide transform admission and metrics.
type Controller struct {
	cfg      Config
	capacity chan struct{}
	active   chan struct{}

	mu       sync.RWMutex
	snapshot Snapshot
}

// NewController validates cfg and constructs an empty controller.
func NewController(cfg Config) (*Controller, error) {
	if cfg.TransformConcurrency <= 0 {
		return nil, fmt.Errorf("transform concurrency must be positive: %d", cfg.TransformConcurrency)
	}
	if cfg.TransformQueue < 0 {
		return nil, fmt.Errorf("transform queue must not be negative: %d", cfg.TransformQueue)
	}
	if cfg.PixelWorkers <= 0 {
		return nil, fmt.Errorf("pixel workers must be positive: %d", cfg.PixelWorkers)
	}
	return newController(cfg), nil
}

// NewDefaultController constructs the validated production resource envelope.
func NewDefaultController() *Controller {
	return newController(DefaultConfig())
}

func newController(cfg Config) *Controller {
	return &Controller{
		cfg:      cfg,
		capacity: make(chan struct{}, cfg.TransformConcurrency+cfg.TransformQueue),
		active:   make(chan struct{}, cfg.TransformConcurrency),
	}
}

// PixelWorkers returns the per-transform CPU fan-out bound.
func (c *Controller) PixelWorkers() int {
	return c.cfg.PixelWorkers
}

// Run admits and executes transform. The callback is invoked synchronously at
// most once and must honor ctx at safe transformation boundaries.
//
//nolint:gocognit,gocyclo // Admission, cancellation, and accounting form one auditable lifecycle.
func (c *Controller) Run(ctx context.Context, request Request, transform func(context.Context) error) error {
	if ctx == nil {
		return errors.New("resource request context is nil")
	}
	if transform == nil {
		return errors.New("resource transform is nil")
	}
	if request.Class != Background && request.Class != Interactive {
		return fmt.Errorf("invalid resource request class %q", request.Class)
	}
	if request.InputPixels < 0 || request.InputBytes < 0 {
		return errors.New("resource input measurements must not be negative")
	}

	waitStarted := time.Now()
	if err := c.reserve(ctx, request.Class); err != nil {
		outcome := OutcomeCanceled
		if errors.Is(err, ErrOverloaded) {
			outcome = OutcomeOverloaded
		}
		c.record(request, time.Since(waitStarted), 0, outcome)
		return err
	}

	select {
	case c.active <- struct{}{}:
		c.changeActive(1)
	default:
		c.changeQueued(1)
		select {
		case c.active <- struct{}{}:
			c.changeQueued(-1)
			c.changeActive(1)
		case <-ctx.Done():
			c.changeQueued(-1)
			<-c.capacity
			c.record(request, time.Since(waitStarted), 0, OutcomeCanceled)
			return ctx.Err()
		}
	}

	wait := time.Since(waitStarted)
	transformStarted := time.Now()
	err := transform(ctx)
	duration := time.Since(transformStarted)

	c.changeActive(-1)
	<-c.active
	<-c.capacity

	outcome := OutcomeCompleted
	if err != nil {
		outcome = OutcomeFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = OutcomeCanceled
		}
	}
	c.record(request, wait, duration, outcome)
	return err
}

func (c *Controller) reserve(ctx context.Context, class Class) error {
	if class == Interactive {
		select {
		case c.capacity <- struct{}{}:
			return nil
		default:
			return ErrOverloaded
		}
	}
	select {
	case c.capacity <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Controller) changeActive(delta int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot.Active += delta
}

func (c *Controller) changeQueued(delta int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot.Queued += delta
}

func (c *Controller) record(request Request, wait, duration time.Duration, outcome Outcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot.TotalWait += wait
	c.snapshot.TotalDuration += duration
	c.snapshot.Last = Operation{
		Class:       request.Class,
		Mode:        request.Mode,
		InputPixels: request.InputPixels,
		InputBytes:  request.InputBytes,
		Wait:        wait,
		Duration:    duration,
		Outcome:     outcome,
	}
	switch outcome {
	case OutcomeCompleted:
		c.snapshot.Completed++
	case OutcomeFailed:
		c.snapshot.Failed++
	case OutcomeCanceled:
		c.snapshot.Canceled++
	case OutcomeOverloaded:
		c.snapshot.Overloaded++
	}
}

// Snapshot returns a race-safe copy of current admission metrics.
func (c *Controller) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot
}
