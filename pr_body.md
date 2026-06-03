💡 **Before:**
The `ExecuteSyncPlan` function in `internal/sync/execution.go` was overly complex and suffered from high cognitive load. It was responsible for orchestrating the entire execution pipeline, including nested retry logic for multiple uploads, processing complex batch deletions (for tracked and unknown images), and finalizing the TV's state. This made the function long, hard to read, and difficult to test in isolation.

✨ **After:**
The logic has been structurally simplified by extracting the upload and deletion loops into two distinct, private helper methods (`processUploads` and `processDeletions`). `ExecuteSyncPlan` now reads as a clear, high-level orchestrator that sequentially delegates tasks to these targeted helpers.

📉 **Reductions:**
- Abstracted over 60 lines of complex control flow and nested conditions from the main execution path.
- Removed obsolete `//nolint:gocognit,nestif,gocyclo,funlen` directives, as the top-level function is now clean enough to pass the team's standard cyclomatic complexity and function length linting rules.
