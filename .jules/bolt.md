## 2026-06-08 - Pointer Indirection Overhead in Tight Loops
**Learning:** In highly repetitive image processing loops (like `processBMSThreshold` which runs per-pixel), directly accessing fields of a pointer struct (e.g., `src.Pix[idx]`) introduces hidden pointer indirection overhead on every single iteration, even if the struct itself is not modified.
**Action:** Always extract slice properties from pointer structs (e.g., `pix := src.Pix`) into local variables before entering tight inner loops to enforce direct memory access and bypass continuous dereferencing penalties.
## 2026-06-11 - Math Function Overheads in Tight Loops
**Learning:** Functions like `ciede2000` that perform heavy math inside tight image pixel processing loops suffer heavily from repetitive degree-to-radian conversions (`* math.Pi / 180.0`). Additionally, `math.Pow(x, 7)` is significantly slower than explicit multiplication (`x*x*x*x*x*x*x`).
**Action:** When optimizing tight inner loops involving math functions, always structure code to use radians natively to avoid conversions and replace small integer powers with explicit multiplication.
## 2026-06-13 - Extracting Math from 2D Loops
**Learning:** In tight O(W*H) nested image processing loops, floating-point calculations like `math.Exp` or `math.Sqrt` that depend on only one coordinate (e.g., `x` or `y` individually) create massive overhead when computed repeatedly.
**Action:** Always pre-calculate coordinate-dependent aesthetic or mathematical factors into 1D arrays outside the loop, replacing expensive function calls with simple slice lookups in the hot path.
## 2026-06-16 - Precomputing Base Data for Concurrent Loops
**Learning:** When concurrent loops process identical base data across different configurations (like parallel execution per threshold or channel), recalculating shared properties (like luminance) inside each goroutine introduces redundant CPU overhead that scales with the number of parallel workers.
**Action:** Always precompute shared properties into a slice sequentially prior to spawning goroutines to significantly reduce redundant CPU operations and memory allocations across workers.
## 2026-06-21 - Reusing Linear Loop Variables in Grid Processing
**Learning:** In tight 1D loops iterating over a grid (like flood fill queues in BMS processing), repeatedly converting the 1D index (`curr`) back into 2D coordinates (`cx`, `cy`) and then immediately recalculating the adjacent 1D indexes via `cy * width + cx +/- 1` introduces massive redundant multiplication and addition overhead on every pixel iteration.
**Action:** When scanning neighbors in a flat 1D slice representation of a grid, always calculate adjacent indices through direct addition/subtraction from the current 1D index (e.g., `curr - 1`, `curr + width`) to entirely eliminate coordinate conversions in the hot path.
