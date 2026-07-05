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
## 2026-07-05 - Avoid Redundant Slice Allocations inside Parallel Loops
**Learning:** In tight, parallelized inner operations (like Boolean Map Saliency processing across multiple thresholds), dynamically allocating temporary arrays (like `boolMap := make([]bool, w*h)`) inside each goroutine introduces severe heap allocation pressure and garbage collection overhead. Furthermore, transforming basic mathematical state evaluation into memory access causes cache misses.
**Action:** When translating boolean states against thresholds, evaluate the state strictly on-the-fly (`s.lumMap[idx] <= s.t`) directly from pre-computed shared memory slices rather than pre-allocating an intermediate boolean mapping structure.

## 2026-07-05 - 1D Arithmetic Trumps 2D Conversion in Hot Paths
**Learning:** Converting a 1D slice index (`curr`) back into 2D Cartesian coordinates (`cx := curr % w; cy := curr / w`) inside an extremely tight flood-fill processing loop wastes CPU cycles doing division and modulo operations for every single neighbor calculation.
**Action:** Always maintain strict 1D index offsets (`curr - 1`, `curr + w`) in hot execution paths to handle spatial neighbors, skipping coordinate space transitions entirely to minimize arithmetic load.
