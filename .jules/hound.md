## 2026-05-20 - Prevent Denial of Service in JSON Decoding
**Learning:** Decoding JSON directly from HTTP responses using `json.NewDecoder(resp.Body).Decode()` exposes the application to resource exhaustion (DoS) vulnerabilities if the server returns an excessively large payload, as the decoder will continue allocating memory.
**Action:** Always wrap `resp.Body` with `http.MaxBytesReader` before passing it to `json.NewDecoder` to enforce a hard limit on memory allocation during payload parsing.
## 2026-05-21 - Prevent Silent Failures in Samsung Art API
**Learning:** Samsung Art API WebSocket responses include an `error_code` field within the JSON envelope. Previously, operations like `GetContentList` or `SendImage` only validated the WebSocket transport success, completely ignoring application-level API errors returned by the TV. This allowed invalid requests to silently fail while the client falsely reported success, breaking state synchronization.
**Action:** Always parse the base `artResponse` envelope and explicitly validate the `ErrorCode` field (e.g., using a `checkArtError` helper) before processing specific endpoint data or assuming the API request succeeded.
## 2026-06-02 - Redundant Lock Calls Masking Synchronization Bugs
**Learning:** Sequential lock-unlock-lock sequences on the same mutex (e.g., c.mu.Lock(); c.mu.Unlock(); c.mu.Lock()) represent fundamental architectural flaws in synchronization rather than clever workarounds. The linter directive '//nolint:staticcheck // intended unlock pattern' falsely masked this critical flaw, highlighting that suppressing static analysis tools often hides underlying logical hazards and race windows.
**Action:** Always scrutinize suppressed linters, especially around concurrency primitives. Removing the redundancy natively resolves the deadlock/race risk without compromising functionality.

## 2026-06-05 - Polymorphic JSON parsing in Samsung Art API
**Learning:** Samsung TVs return polymorphic JSON types for fields like `conn_info` and `content_list` within the `d2d.service.message.event` websocket payload. Older TVs return escaped strings (`"{\"ip\":\"...\"}"`), while 2024+ TVs return raw JSON objects (`{"ip":"..."}`). Using strongly-typed structs (like `string`) causes `json.Unmarshal` to crash.
**Action:** When interacting with the Samsung Art API, always unmarshal polymorphic fields as `json.RawMessage` and resolve them defensively using string extraction fallbacks to prevent runtime decoding panics across different TV generations.
## 2026-06-11 - Goroutine Leaks from select loop returns
**Learning:** Using `return` directly inside a `select` loop handling concurrent `go` functions causes any previously launched goroutines to leak because it skips the `wg.Wait()` step. This can also lead to subsequent data races or logic flaws as the leaked goroutines continue processing shared structures unpredictably.
**Action:** When iterating over inputs to spawn goroutines with a top-level cancellation check (`ctx.Done()`), always `break` out of the loop and call `wg.Wait()` before returning to ensure all active goroutines finish correctly.
