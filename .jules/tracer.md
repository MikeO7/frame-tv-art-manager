## 2024-03-01 - [Mocking Samsung HTTP interactions]
**Learning:** Functions like `checkArtModeGate` make an HTTP call using `http.Client`. To test these properly without making actual network requests to a TV, we need to spin up a mock HTTP server using `httptest.NewServer` or `httptest.NewTLSServer`.
**Action:** Use `httptest.NewServer` in `internal/samsung/client_test.go` to simulate the TV's REST responses.
