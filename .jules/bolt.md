## 2024-05-15 - [Batch Disk I/O Operations]
**Learning:** In Go applications, performing disk I/O operations (like saving JSON mappings) inside a loop can severely degrade performance, especially when uploading multiple items or running in environments with slower disks.
**Action:** Always extract disk I/O operations, such as saving state files or mappings, outside of loops to batch them and improve overall performance by reducing repeated filesystem access. This is particularly important for functions that iterate over collections to perform tasks like uploads or downloads.

## 2024-04-28 - Batch Map Deletions to Avoid Lock Contention
**Learning:** Calling a method that takes a lock (`mu.Lock()`) in a loop (like `mapping.Delete(filename)`) can cause performance issues and lock contention when iterating over a large map.
**Action:** Always batch updates when modifying a mutex-protected map. Create batch methods (like `DeleteBatch`) and pass slices of keys to reduce the time spent acquiring and releasing the lock.

## 2026-04-29 - [Optimize Ambient Effects by Parallelizing Loop]
**Learning:** In Go, performing heavy independent per-pixel math (such as distances for a vignette) inside a tight nested loop across a 4K image is a CPU bottleneck. Breaking this workload up with `sync.WaitGroup` by dividing the image vertically among multiple workers drops execution time by nearly 70%. Additionally, detecting cases where effects are mathematically zero (vignette=0) avoids doing expensive `math.Sqrt` per-pixel.
**Action:** When iterating over large 2D arrays or image pixels applying complex floating-point calculations, prefer slicing the rows/columns into parallel goroutines and identifying "fast-paths" that skip computation completely.
