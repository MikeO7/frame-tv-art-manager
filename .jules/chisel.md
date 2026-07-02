## 2024-05-23 - TV Sync Reconciler Simplification\n**Learning:** The `Run` method in `internal/sync/reconciler.go` is deeply nested and monolithic (196 lines), responsible for everything from connection and initialization to uploading, deleting, updating slideshows, and auto-off. This causes extremely high cognitive load and violates Single Responsibility.\n**Action:** Extract large blocks of logic (setup, upload loop, delete loop, finalization) into descriptive helper methods on the `Reconciler` struct to dramatically reduce complexity without changing the functional behavior or public contracts.

## 2026-06-13 - PlanSync Method Simplification
**Learning:** The `PlanSync` method in `internal/sync/plan.go` is complex, handling the creation of the entire sync plan. Breaking the logic into `buildUploadJobs`, `buildDeleteJobs`, and `determineSelectedID` dramatically reduced cyclomatic and cognitive complexity without altering behavior.
**Action:** Extract specific mapping logic and conditionals into small helper methods that deal with isolated map processing, instead of leaving it embedded within large declarative setups.
## 2026-06-16 - ArtworkCatalog Rebuild Simplification
**Learning:** The `Rebuild` method in `internal/sources/catalog.go` was a monolithic block handling cache checks, state resets, worker pool orchestration, and individual result processing. By cleanly separating these responsibilities into distinct helper methods (`isCacheValid`, `resetState`, `processFilesConcurrent`, and `processResult`), the function became a readable declarative pipeline, drastically reducing complexity metrics and allowing the removal of strict linter bypasses (`gocognit`, `gocyclo`, `funlen`) while maintaining identical runtime behavior.
**Action:** When extracting large, mixed-responsibility functions, isolate the concurrency mechanics (worker pools, channel coordination) from the core business logic (result parsing, state mutations) to ensure both high readability and thread safety.

## 2026-06-18 - parseExif Simplification
**Learning:** The `parseExif` method in `internal/optimize/resize.go` had high cognitive complexity due to handling byte order logic alongside parsing the EXIF orientation tags.
**Action:** Extracting the EXIF orientation tag loop into a separate `findOrientationTag` helper function significantly reduced cognitive load and cyclomatic complexity, resolving linter warnings.
## 2026-07-02 - Respecting Performance Hot-Paths
**Learning:** Extracting code for readability in performance-critical sections (like pixel processing loops marked with explicit inline/unroll warnings) violates structural boundaries and risks severe performance regressions.
**Action:** Always fully read function blocks before executing scripts, and actively look for `//` developer comments indicating deliberate loop unrolling, lack of function calls, or manual inline assignments before attempting structural extraction.
