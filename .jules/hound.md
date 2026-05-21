## 2026-05-20 - Prevent Denial of Service in JSON Decoding
**Learning:** Decoding JSON directly from HTTP responses using `json.NewDecoder(resp.Body).Decode()` exposes the application to resource exhaustion (DoS) vulnerabilities if the server returns an excessively large payload, as the decoder will continue allocating memory.
**Action:** Always wrap `resp.Body` with `http.MaxBytesReader` before passing it to `json.NewDecoder` to enforce a hard limit on memory allocation during payload parsing.
## 2026-05-21 - Prevent Silent Failures in Samsung Art API
**Learning:** Samsung Art API WebSocket responses include an `error_code` field within the JSON envelope. Previously, operations like `GetContentList` or `SendImage` only validated the WebSocket transport success, completely ignoring application-level API errors returned by the TV. This allowed invalid requests to silently fail while the client falsely reported success, breaking state synchronization.
**Action:** Always parse the base `artResponse` envelope and explicitly validate the `ErrorCode` field (e.g., using a `checkArtError` helper) before processing specific endpoint data or assuming the API request succeeded.
