package sync

import (
	"context"
	"errors"
)

// errTransformsBusy refuses interactive work while another transform is
// running or every transform place is taken. Its text reaches the person who
// asked, so it says what to do next.
var errTransformsBusy = errors.New("another image is being processed right now; try again shortly")

// One image transform runs at a time and two more may wait for it, which
// bounds the process's peak image memory.
const (
	transformsRunning = 1
	transformsWaiting = 2
)

// transformGate admits image transforms process-wide. Background work waits
// for a place, and then for its turn to run, until its context ends.
// Interactive work never waits: it is refused at once with errTransformsBusy
// when no place is free or a transform is already running, so a preview is
// never held behind a whole-collection optimization.
type transformGate struct {
	places  chan struct{}
	running chan struct{}
}

func newTransformGate() *transformGate {
	return &transformGate{
		places:  make(chan struct{}, transformsRunning+transformsWaiting),
		running: make(chan struct{}, transformsRunning),
	}
}

// run calls transform once it is admitted, synchronously and at most once.
func (g *transformGate) run(ctx context.Context, interactive bool, transform func(context.Context) error) error {
	if interactive {
		return g.runNow(ctx, transform)
	}
	select {
	case g.places <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-g.places }()
	select {
	case g.running <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-g.running }()
	return transform(ctx)
}

// runNow calls transform only if it can start immediately.
func (g *transformGate) runNow(ctx context.Context, transform func(context.Context) error) error {
	select {
	case g.places <- struct{}{}:
	default:
		return errTransformsBusy
	}
	defer func() { <-g.places }()
	select {
	case g.running <- struct{}{}:
	default:
		return errTransformsBusy
	}
	defer func() { <-g.running }()
	return transform(ctx)
}
