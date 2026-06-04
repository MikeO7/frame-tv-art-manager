💡 Before
The `ExecuteSyncPlan` function inside `internal/sync/execution.go` was a massive, 80+ line monolith that directly handled upload retries, sleep cycles, batch deletion loops, mapping synchronization, and error handling. This deep nesting, mixed with heavy pointer manipulation and complex control flow (`select`, channels, loops), resulted in an extremely high cognitive load that made the sync lifecycle hard to read.

✨ After
The heavy loop logic has been neatly extracted out of the main orchestrator.
1. `executeUploads()` cleanly handles the upload retry, rate-limiting, dry-run evaluation, and mapping storage.
2. `executeDeletes()` isolates the batch deletion of both tracked and unknown files.
The main `ExecuteSyncPlan` is now a crisp, highly readable top-down orchestration method that clearly defines the stages of synchronization.

📉 Reductions
- Pulled 60+ lines of raw execution logic out of the core orchestrator.
- Dropped the cyclomatic complexity of `ExecuteSyncPlan` significantly, allowing the removal of its broad `//nolint:gocognit,nestif,gocyclo,funlen` directive.
