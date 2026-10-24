package sync

import "context"

// acquireGate takes the single slot in gate unless ctx ends first. A slot won
// in a race with cancellation is released so callers never proceed canceled.
func acquireGate(ctx context.Context, gate chan struct{}) error {
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func acquireTVWorker(ctx context.Context, workers chan<- struct{}) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case workers <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}
