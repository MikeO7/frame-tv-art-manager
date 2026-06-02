📏 Architectural Shift
Before this change, the `internal/samsung` package suffered from internal fragmentation, with several tiny, shallow wrappers (`client_network.go`) existing solely to provide networking logic strictly bound to the `Client` struct in `client.go`. This created an unnecessary cognitive burden and exposed internal abstraction boundaries that did not align with the overarching architecture. By folding these components directly into the primary `Client` module, we established a deep module pattern, effectively encapsulating all TV network operations within a single, cohesive file.

🛠️ Changes
- Relocated `fetchDeviceInfo`, `sendWOL`, and `turnOffTV` directly into `client.go`.
- Relocated the `checkArtModeGate` method from `client_art.go` into `client.go` to centralize basic HTTP REST gate operations.
- Deleted `client_network.go`, eliminating the artificial internal file coupling.
- Cleaned up imports and unused code block linting directives.

🔬 Verification
- `make check` passes, confirming all format, linter (`golangci-lint`), and codebase metrics are satisfied.
- The `go test ./...` test suite passed successfully without disruption to downstream dependents.
