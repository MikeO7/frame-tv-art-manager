## 2024-05-01 - Parallelizing Image Filters
**Learning:** For heavy, independent per-pixel math across large images (like 4K images on a Frame TV), the work can be heavily parallelized. Unrolling the inner pixel loop over RGBA and calculating offsets explicitly speeds things up even more.
**Action:** Unroll loops over colors and compute 1D offsets directly. Use `sync.WaitGroup` to chunk the image vertically to use all CPUs.
## 2024-05-01 - Parallelizing Image Filters
**Learning:** For heavy, independent per-pixel math across large images (like 4K images on a Frame TV), the work can be heavily parallelized. Unrolling the inner pixel loop over RGBA and calculating offsets explicitly speeds things up even more.
**Action:** Unroll loops over colors and compute 1D offsets directly. Use `sync.WaitGroup` to chunk the image vertically to use all CPUs.
## 2026-05-03 - Unrolling image filters
**Learning:** For heavy, independent per-pixel math across large images (like 4K images on a Frame TV), unrolling the inner pixel loop over RGBA and calculating offsets explicitly speeds things up even more.
**Action:** Unroll loops over colors and compute 1D offsets directly. Use `sync.WaitGroup` to chunk the image vertically to use all CPUs.

## 2026-05-04 - Eliminate PRNG bottleneck in tight pixel loops
**Learning:** Performing heavy, independent per-pixel operations sequentially across 4K images (like dithering with `rand.Intn` for 8.2 million pixels) is a massive performance bottleneck. The global PRNG synchronization overhead kills speed, taking ~235ms per image.
**Action:** Always parallelize heavy per-pixel mathematical operations using `sync.WaitGroup` to divide the workload vertically. Replace heavy math like global `rand.Intn` with lightweight, thread-local equivalents (like a simple Xorshift32 algorithm) and unroll inner RGBA loops to avoid bounds-checking overhead. This reduced Dither execution time from ~235ms to ~21ms.
## 2024-05-23 - Optimizing GalleryMasterPolish
**Learning:** `rand.Float64()` causes significant lock contention when used globally inside highly concurrent per-pixel loop operations. Using standard lib floating point math along with type casting (`float64`, `math.Max`, `math.Min`) adds to the bottleneck.
**Action:** Replace `rand.Float64()` with a fast thread-local Xorshift32 PRNG and cast to `float32`. Manually inline clamps instead of using `math.Min` and `math.Max`. Finally, parallelize the image processing using `sync.WaitGroup` to chunk the image vertically.
