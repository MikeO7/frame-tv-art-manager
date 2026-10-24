package optimize

import (
	"sync/atomic"
	"testing"
)

func TestRunPixelRowsBoundsActiveWorkers(t *testing.T) {
	want := int32(defaultPixelWorkers())
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, want)
	done := make(chan struct{})
	go func() {
		runPixelRows(0, pixelPartitions, func(_, _, _ int) {
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
	for range want {
		<-started
	}
	if got := maximum.Load(); got != want {
		t.Fatalf("active pixel workers = %d, want %d", got, want)
	}
	close(release)
	<-done
}

func TestRunPixelRowsCoversEveryRowOnce(t *testing.T) {
	t.Parallel()
	for _, rows := range [][2]int{{0, 0}, {1, 0}, {0, 3}, {1, 20}, {0, 37}} {
		var seen [64]atomic.Int32
		runPixelRows(rows[0], rows[1], func(_, startY, endY int) {
			for y := startY; y < endY; y++ {
				seen[y].Add(1)
			}
		})
		for y := range seen {
			want := int32(0)
			if y >= rows[0] && y < rows[1] {
				want = 1
			}
			if got := seen[y].Load(); got != want {
				t.Fatalf("rows %v: row %d visited %d times, want %d", rows, y, got, want)
			}
		}
	}
}
