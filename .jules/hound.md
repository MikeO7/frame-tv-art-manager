## 2026-05-20 - Prevent Denial of Service in JSON Decoding
**Learning:** Decoding JSON directly from HTTP responses using `json.NewDecoder(resp.Body).Decode()` exposes the application to resource exhaustion (DoS) vulnerabilities if the server returns an excessively large payload, as the decoder will continue allocating memory.
**Action:** Always wrap `resp.Body` with `http.MaxBytesReader` before passing it to `json.NewDecoder` to enforce a hard limit on memory allocation during payload parsing.
