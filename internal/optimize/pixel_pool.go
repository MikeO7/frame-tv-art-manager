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

func runPixelTasks(workerLimit int, task func(int)) {
	workers := min(max(workerLimit, 1), pixelPartitions)
	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for index := range jobs {
				task(index)
			}
		}()
	}
	for index := range pixelPartitions {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
}
