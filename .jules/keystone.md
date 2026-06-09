## 2025-05-20 - Consolidating Samsung network utilities
**Learning:** The samsung package has several tiny files (rest.go, gate.go, remote.go, wol.go) that are only used by the main Client struct in client.go. These are perfect candidates for structural consolidation into a deep module to eliminate shallow wrappers and reduce internal fragmentation.
**Action:** Move the contents of rest.go, gate.go, remote.go, and wol.go into client.go as private methods on the Client struct to encapsulate all TV network operations within a single deep module.

## 2026-06-08 - Consolidating config module files
**Learning:** The `config` module had several fragmented files like `options.go` and `matte.go` that merely added methods and small structs to the main `Config` type. This fragmentation increased cognitive load and spread configuration logic across too many files without adding meaningful domain boundaries.
**Action:** Consolidate tiny helper files like `options.go` and `matte.go` into the primary `config.go` file (and merge their tests into `config_test.go`) to create a deeper, more unified module interface and reduce file fragmentation.
