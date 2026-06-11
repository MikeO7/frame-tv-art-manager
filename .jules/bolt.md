## 2026-06-08 - Pointer Indirection Overhead in Tight Loops
**Learning:** In highly repetitive image processing loops (like `processBMSThreshold` which runs per-pixel), directly accessing fields of a pointer struct (e.g., `src.Pix[idx]`) introduces hidden pointer indirection overhead on every single iteration, even if the struct itself is not modified.
**Action:** Always extract slice properties from pointer structs (e.g., `pix := src.Pix`) into local variables before entering tight inner loops to enforce direct memory access and bypass continuous dereferencing penalties.
## 2026-06-11 - Math Function Overheads in Tight Loops
**Learning:** Functions like `ciede2000` that perform heavy math inside tight image pixel processing loops suffer heavily from repetitive degree-to-radian conversions (`* math.Pi / 180.0`). Additionally, `math.Pow(x, 7)` is significantly slower than explicit multiplication (`x*x*x*x*x*x*x`).
**Action:** When optimizing tight inner loops involving math functions, always structure code to use radians natively to avoid conversions and replace small integer powers with explicit multiplication.
