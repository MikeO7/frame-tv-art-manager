## 2026-06-08 - Pointer Indirection Overhead in Tight Loops
**Learning:** In highly repetitive image processing loops (like `processBMSThreshold` which runs per-pixel), directly accessing fields of a pointer struct (e.g., `src.Pix[idx]`) introduces hidden pointer indirection overhead on every single iteration, even if the struct itself is not modified.
**Action:** Always extract slice properties from pointer structs (e.g., `pix := src.Pix`) into local variables before entering tight inner loops to enforce direct memory access and bypass continuous dereferencing penalties.
