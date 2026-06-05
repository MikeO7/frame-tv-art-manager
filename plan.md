1. **Increase test coverage in `internal/sync/execution.go`**
   - Implement comprehensive tests for `updateBrightnessPlan`, `updateSlideshowPlan`, `handleAutoOffPlan`, `applySelectionAndSlideshowPlan`, `uploadWithRetry`, and `ExecuteSyncPlan` functions to hit the remaining untested paths.
   - Using the `mockTVTransportExecution` helper I will inject the correct failure modes, empty states, and expected results to cover all missing paths.

2. **Complete pre-commit steps to ensure proper testing, verification, review, and reflection are done.**
   - Run `make coverage-check` and `make test` to ensure coverage is strictly maintained and there are no regressions.
   - Run formatting and linters.
   - Document any critical learnings in the `.jules/tracer.md` file.

3. **Submit the PR**
   - Create the pull request using `gh pr create` with the 'Tracer' persona, summarizing the coverage boost.
