**🎯 Target:**
Removed duplicate and dead unexported helper functions (`diffSets`, `setToSlice`, `mapValues`, `boolCount`) from `internal/sync/engine.go` and their associated tests from `internal/sync/sync_test.go`. These helpers were actively used in `internal/samsung/sync.go` but left orphaned in `engine.go` after a previous refactor.

**📉 Drop:**
Deleted ~34 lines of dead runtime code and ~40 lines of test code.

**🔬 Proof:**
Ran `make check` and `make build`. All tests pass cleanly and test coverage remains unaffected since the removed code was essentially dead weight.
