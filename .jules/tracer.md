## 2024-03-01 - [Mocking Samsung HTTP interactions]
**Learning:** Functions like `checkArtModeGate` make an HTTP call using `http.Client`. To test these properly without making actual network requests to a TV, we need to spin up a mock HTTP server using `httptest.NewServer` or `httptest.NewTLSServer`.
**Action:** Use `httptest.NewServer` in `internal/samsung/client_test.go` to simulate the TV's REST responses.
## 2026-05-26 - Void Function Logging Assertions
**Learning:** Functions that return nothing but log warnings internally (e.g., Unsplash TrackDownload) cannot be verified simply by forcing execution of the error paths. Doing so creates a "ghost assertion".
**Action:** Always inject a custom `slog` logger backed by a `bytes.Buffer` when testing void logging paths, and assert against the captured buffer contents using `bytes.Contains` to ensure actual execution behavior is validated.
## 2026-05-31 - Safe Goroutine Log Assertions
**Learning:** Testing asynchronous logging behaviors in background goroutines by capturing `slog` output into a standard `bytes.Buffer` causes data race failures (`go test -race`).
**Action:** Always wrap the buffer in a thread-safe struct with a `sync.Mutex`, or use channels to safely verify log execution paths. Avoid hardcoded `time.Sleep()` synchronization to prevent flaky tests.

## 2026-06-02 - Testing sync.TVReconciler using stub structs to bypass cycle imports
**Learning:** When trying to test components like `sync.TVReconciler` that depend heavily on complex struct boundaries from other packages (like `samsung.SlideshowStatus`) but also suffer from circular dependency loops (if testing inside `sync_test.go` and using actual structures vs dummy packages), it's easiest to define a local mock of the interface directly inside `execution_test.go` and add explicit boolean or value trackers to make solid AAA-pattern assertions.
**Action:** When adding missing coverage for side-effect-heavy utility classes, always add explicit boolean tracker properties (`somethingCalled bool`) to your local mock structs. This avoids ghost assertions and ensures you don't over-mock.

## 2026-06-06 - Test Coverage for Unsplash Provider Error Paths
**Learning:** To satisfy the 'errcheck' linter when writing dummy data to `http.ResponseWriter` in test servers, ensure both return values are explicitly assigned to the blank identifier: `_, _ = w.Write([]byte("..."))`. Also, to satisfy 'staticcheck' rule SA1012, never pass a `nil` context to force an error path in tests. Instead, initialize and explicitly cancel a context: `ctx, cancel := context.WithCancel(context.Background()); cancel()`.
**Action:** Always assign both return values of `w.Write` to blank identifiers in test servers and use explicitly canceled contexts instead of `nil` when testing context-dependent error paths.
