package optimize

import (
	"sync/atomic"
	"testing"
)

func TestRunPixelTasksBoundsActiveWorkers(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() {
		runPixelTasks(2, func(int) {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				prior := maximum.Load()
				if current <= prior || maximum.CompareAndSwap(prior, current) {
					break
				}
			}
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
		})
		close(done)
	}()
	<-started
	<-started
	if got := maximum.Load(); got != 2 {
		t.Fatalf("active pixel workers = %d, want 2", got)
	}
	close(release)
	<-done
}
