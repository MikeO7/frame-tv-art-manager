## 2024-03-01 - [Mocking Samsung HTTP interactions]
**Learning:** Functions like `checkArtModeGate` make an HTTP call using `http.Client`. To test these properly without making actual network requests to a TV, we need to spin up a mock HTTP server using `httptest.NewServer` or `httptest.NewTLSServer`.
**Action:** Use `httptest.NewServer` in `internal/samsung/client_test.go` to simulate the TV's REST responses.
## 2026-05-26 - Void Function Logging Assertions
**Learning:** Functions that return nothing but log warnings internally (e.g., Unsplash TrackDownload) cannot be verified simply by forcing execution of the error paths. Doing so creates a "ghost assertion".
**Action:** Always inject a custom `slog` logger backed by a `bytes.Buffer` when testing void logging paths, and assert against the captured buffer contents using `bytes.Contains` to ensure actual execution behavior is validated.
## 2026-05-31 - Safe Goroutine Log Assertions
**Learning:** Testing asynchronous logging behaviors in background goroutines by capturing `slog` output into a standard `bytes.Buffer` causes data race failures (`go test -race`).
**Action:** Always wrap the buffer in a thread-safe struct with a `sync.Mutex`, or use channels to safely verify log execution paths. Avoid hardcoded `time.Sleep()` synchronization to prevent flaky tests.
