package optimize

import (
	"runtime"
	"sync"
)

// Keep eight deterministic row partitions so seeded effects and floating-point
// accumulation retain their established output. A smaller worker pool drains
// those partitions without changing their boundaries.
const pixelPartitions = 8

func defaultPixelWorkers() int {
	return min(max(runtime.GOMAXPROCS(0), 1), 4)
}

// runPixelRows splits rows [first, end) into pixelPartitions contiguous bands
// and calls rows for every non-empty band on defaultPixelWorkers goroutines.
func runPixelRows(first, end int, rows func(partition, startY, endY int)) {
	chunk := (end - first + pixelPartitions - 1) / pixelPartitions
	workers := defaultPixelWorkers()
	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for partition := range jobs {
				startY := first + partition*chunk
				if endY := min(startY+chunk, end); startY < endY {
					rows(partition, startY, endY)
				}
			}
		}()
	}
	for partition := range pixelPartitions {
		jobs <- partition
	}
	close(jobs)
	wg.Wait()
}
