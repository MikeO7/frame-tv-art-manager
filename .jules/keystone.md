## 2025-05-20 - Consolidating Samsung network utilities
**Learning:** The samsung package has several tiny files (rest.go, gate.go, remote.go, wol.go) that are only used by the main Client struct in client.go. These are perfect candidates for structural consolidation into a deep module to eliminate shallow wrappers and reduce internal fragmentation.
**Action:** Move the contents of rest.go, gate.go, remote.go, and wol.go into client.go as private methods on the Client struct to encapsulate all TV network operations within a single deep module.

## 2025-05-20 - Consolidating Samsung REST and Remote Control
**Learning:** Found that `internal/samsung` had multiple single-function, shallow files for basic network requests (`client_rest.go`, `client_remote.go`, `client_wol.go`), unnecessarily distributing core `Client` behaviors across the directory structure and making the `Client` logic harder to follow in one place.
**Action:** Consolidating simple utility functions into `client.go` ensures a deep abstraction where all core `Client` network bindings are localized and self-contained, simplifying maintainability.
