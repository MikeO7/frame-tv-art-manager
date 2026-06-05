## 2024-03-01 - [Mocking Samsung HTTP interactions]
**Learning:** Functions like `checkArtModeGate` make an HTTP call using `http.Client`. To test these properly without making actual network requests to a TV, we need to spin up a mock HTTP server using `httptest.NewServer` or `httptest.NewTLSServer`.
**Action:** Use `httptest.NewServer` in `internal/samsung/client_test.go` to simulate the TV's REST responses.
## 2026-05-26 - Void Function Logging Assertions
**Learning:** Functions that return nothing but log warnings internally (e.g., Unsplash TrackDownload) cannot be verified simply by forcing execution of the error paths. Doing so creates a "ghost assertion".
**Action:** Always inject a custom `slog` logger backed by a `bytes.Buffer` when testing void logging paths, and assert against the captured buffer contents using `bytes.Contains` to ensure actual execution behavior is validated.
## 2026-06-02 - Testing sync.TVReconciler using stub structs to bypass cycle imports
**Learning:** When trying to test components like `sync.TVReconciler` that depend heavily on complex struct boundaries from other packages (like `samsung.SlideshowStatus`) but also suffer from circular dependency loops (if testing inside `sync_test.go` and using actual structures vs dummy packages), it's easiest to define a local mock of the interface directly inside `execution_test.go` and add explicit boolean or value trackers to make solid AAA-pattern assertions.
**Action:** When adding missing coverage for side-effect-heavy utility classes, always add explicit boolean tracker properties (`somethingCalled bool`) to your local mock structs. This avoids ghost assertions and ensures you don't over-mock.
