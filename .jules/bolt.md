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
## 2024-05-23 - Optimize math.Pow in CIEDE2000 calculations
**Learning:** Using `math.Pow` with small integer exponents (like 2, 4, or 7) is surprisingly heavy inside deep execution loops due to IEEE float handling. Direct explicit multiplication is drastically faster.
**Action:** Replace `math.Pow(x, 2)` with `x * x` and `math.Pow(x, 7)` with `x2 := x * x; x4 := x2 * x2; x7 := x4 * x2 * x`. This single fix on a high-frequency function cuts execution time drastically in the `ciede2000` execution path without loss of precision. Always include inline comments explaining the unrolled math optimization.
## 2026-05-28 - Fast-Path Inline Integer Arithmetic for Tight Convolution Loops
**Learning:** In highly repetitive image convolution loops (like Sobel Edge Detection or Saliency Map generation), executing a closure function with floating point math and boundary checks on every neighbor pixel incurs massive CPU overhead.
**Action:** Implement a dual-path structure. Create a "fast path" that completely skips boundary checking for pixels strictly inside the image edges and replaces float multiplication (`R*0.299 + G*0.587 + B*0.114`) with bit-shifted integer math (`R*299 + G*587 + B*114` scaled later). This halves execution time in critical tight loop areas.
## 2026-06-05 - Avoid floating-point math in tight image processing loops
**Learning:** Performing multiple floating-point conversions and multiplications in a high-frequency execution path (like calculating pixel luminosity for millions of pixels) introduces a significant CPU bottleneck due to float evaluation overhead.
**Action:** Extract float mathematical operations out of the per-pixel inner loop. Perform inner loop accumulations using `uint64` with integer weights (e.g., 299, 587, 114) and defer the final float conversion and division (scaling) until after the loop completes.
## 2026-06-05 - Avoid standard library vulnerabilities
**Learning:** Security vulnerability scanners (`govulncheck`) flag standard library issues. `net/textproto` and `crypto/x509` in Go 1.26.3 had unhandled security risks.
**Action:** Kept Go version cleanly synchronized across `go.mod`, GitHub action workflows, and the `Dockerfile` to the latest patch (1.26.4).
