1. **Optimize `calculateSobelEdgeSlow` in `internal/optimize/saliency.go`**
   - In `calculateSobelEdgeSlow`, we calculate pixel luminance by doing float multiplication (`0.299`, `0.587`, `0.114`).
   - We will replace this with integer multiplication (`299`, `587`, `114`) inside the closure to avoid floating point math per pixel on the edges.
   - We will shift the output float conversion out to the return of the function `calculateSobelEdgeSlow`.
   - Also, we'll extract constants like `minX`, `maxX`, `minY`, `maxY`, `stride` and `pix` out of the closure to avoid referencing from the parent function bounds.

2. **Complete pre-commit steps**
   - Complete pre-commit steps to ensure proper testing, verification, review, and reflection are done.

3. **Submit the change**
   - Once all tests pass, I will submit the change with the Bolt persona format.
