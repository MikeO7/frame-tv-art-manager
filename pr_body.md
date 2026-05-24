**Target**: Removed the unreferenced `GetStage()` method from the `Status` struct in `internal/health/server.go`. This method was identified as dead code using `deadcode -test ./...`.
**Drop**: Deleted 7 lines of code, reducing structural footprint in the health tracking package.
**Proof**: Clean execution of `go test ./...` and `go build ./cmd/frame-tv-art-manager` confirms that this method was completely unreferenced and safe to drop.
