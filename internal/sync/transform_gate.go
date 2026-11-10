package sync

import (
	"context"
	"errors"
)

// errTransformsBusy refuses interactive work while every transform place is
// taken. Callers may report it as retryable.
var errTransformsBusy = errors.New("image transforms are busy")

// One image transform runs at a time and two more may wait for it, which
// bounds the process's peak image memory.
const (
	transformsRunning = 1
	transformsWaiting = 2
)

// transformGate admits image transforms process-wide. Background work waits
// for a place until its context ends; interactive work is refused at once
// with errTransformsBusy when no place is free, so a request never queues
// behind a whole collection.
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
		select {
		case g.places <- struct{}{}:
		default:
			return errTransformsBusy
		}
	} else {
		select {
		case g.places <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { <-g.places }()
	select {
	case g.running <- struct{}{}:
	default:
		select {
		case g.running <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { <-g.running }()
	return transform(ctx)
}
