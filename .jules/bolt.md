## 2024-05-01 - Parallelizing Image Filters
**Learning:** For heavy, independent per-pixel math across large images (like 4K images on a Frame TV), the work can be heavily parallelized. Unrolling the inner pixel loop over RGBA and calculating offsets explicitly speeds things up even more.
**Action:** Unroll loops over colors and compute 1D offsets directly. Use `sync.WaitGroup` to chunk the image vertically to use all CPUs.
## 2024-05-01 - Parallelizing Image Filters
**Learning:** For heavy, independent per-pixel math across large images (like 4K images on a Frame TV), the work can be heavily parallelized. Unrolling the inner pixel loop over RGBA and calculating offsets explicitly speeds things up even more.
**Action:** Unroll loops over colors and compute 1D offsets directly. Use `sync.WaitGroup` to chunk the image vertically to use all CPUs.
