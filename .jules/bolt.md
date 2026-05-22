## 2026-05-12 - Fast CIE Lab Conversion and Precomputed LUT
**Learning:** In tight image processing loops (like Smart Crop's `rgbToLab`), executing multiple mathematical power functions (e.g., `math.Pow(..., 2.2)` and `math.Pow(..., 1.0/3.0)`) per pixel acts as a major CPU bottleneck. Also, Go's native `math.Cbrt` is specifically optimized and outperforms `math.Pow` with a `1/3` exponent.
**Action:** When converting 0-255 uint8 RGB inputs using a non-linear exponent like 2.2, precalculate all 256 possible outcomes into a global `sync.Once` initialized Lookup Table (LUT). Always replace `math.Pow(x, 1.0/3.0)` with `math.Cbrt(x)` for better performance.
## 2026-05-13 - Inline Clamping for Pixel Math
**Learning:** `math.Min` and `math.Max` in Go are implemented for IEEE 754 floats and handle edge cases like `NaN` and `Inf`. In a tight per-pixel loop like `applySoftLight` inside `ApplyCanvasTexture` handling 1080p images, the function call overhead and complex float handling become a significant bottleneck.
**Action:** Replace `math.Min` and `math.Max` bounded return values with inline conditional checks (`if v < 0 { return 0 }` etc.) when you know the input range is well-bounded. This reduced execution time by approximately 35%.
## 2026-05-19 - [Performance Optimization] Precomputing calculateWeave in ApplyCanvasTexture
**Learning:** `calculateWeave(x, y)` has a math-heavy 20x20 repeating pattern. Precomputing this in a global LUT eliminates duplicate computation.
**Action:** Changed `calculateWeave` to use a global LUT populated via `sync.Once`.
## 2026-05-20 - Impasto Calculation Optimization
**Learning:** In tight per-pixel loops calculating normal mapping differences (`calculateBipolarImpasto`), performing multiple `float64` conversions and division operations across channels is computationally expensive.
**Action:** Re-order equations to perform subtraction/addition using bit-shifted integer operations (`x << 1`), and replace final divisions with a single precomputed multiplication constant.
## 2024-05-21 - Optimize RGB to CIE Lab Math Overhead
**Learning:** In Go, calling an anonymous function inside a tight loop like image processing (millions of pixels) adds measurable call stack overhead. Also, floating-point divisions (`16.0 / 116.0`) in per-pixel math, even if constant, can prevent compiler loop unrolling or fast-math paths compared to precomputing the reciprocal/constant.
**Action:** When performing complex pixel transformations (like converting RGB to CIE Lab), strictly avoid closures within the evaluation loop. Inline calculations and precalculate any static divisions into constants to reduce nanoseconds per operation, which scales heavily in 4K resolution processing.
## 2026-05-22 - Fast Math Pow Replacement in Tight Loops
**Learning:** In extremely tight math loops evaluated on a per-pixel basis (like CIEDE2000 calculations), standard library functions like `math.Pow(x, 2)` and `math.Pow(x, 7)` have significant invocation and floating-point logic overhead.
**Action:** Replace bounded, integer-exponent `math.Pow` calls with explicit multiplication operations (e.g. `x * x` or computing `x^7` using successive squaring `x^2, x^4, x^4 * x^2 * x`). This cut the benchmark time of CIEDE2000 roughly in half.
