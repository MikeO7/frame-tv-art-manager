package sync

import (
	"context"
	"fmt"
)

func acquireCycle(ctx context.Context, gate chan struct{}) error {
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return fmt.Errorf("wait for sync cycle: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for sync cycle: %w", ctx.Err())
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
