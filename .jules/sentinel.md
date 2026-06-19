## 2026-05-19 - [High] Prevent Denial of Service (DoS) via Oversized Files
**Vulnerability:** External image downloads were reading `http.Response.Body` directly into `io.Copy`. This makes the application vulnerable to resource exhaustion (e.g., ZIP bombs or endlessly streaming malicious servers) where the declared `ContentLength` might be spoofed or missing.
**Learning:** Checking `ContentLength` is insufficient to prevent DoS attacks when streaming from untrusted external sources, as it can be bypassed.
**Prevention:** Always wrap `http.Response.Body` using `http.MaxBytesReader` before passing it to `io.Copy`. Avoid `io.LimitReader` as it silently truncates the data by returning EOF instead of an error.
## 2025-02-26 - [Medium] Fix Potential Slowloris DOS Attack
**Vulnerability:** Go `http.Server` configured without `ReadHeaderTimeout`, leading to potential Slowloris Denail of Service (DoS) attacks.
**Learning:** Default Go HTTP server configurations do not enforce timeouts, which is a common security pitfall.
**Prevention:** Always configure standard timeout fields (like `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`) when initializing `http.Server`.

## 2025-02-26 - [Medium] Fix Potential Slowloris DOS Attack
**Vulnerability:** Go `http.Server` in `internal/health/server.go` configured without `ReadTimeout`, `WriteTimeout`, and `IdleTimeout`, leading to potential Slowloris Denial of Service (DoS) attacks and resource exhaustion.
**Learning:** Default Go HTTP server configurations do not enforce read, write, or idle timeouts. This is a common security pitfall.
**Prevention:** Always configure standard timeout fields (like `ReadTimeout`, `WriteTimeout`, `IdleTimeout`) along with `ReadHeaderTimeout` when initializing `http.Server`.

## 2025-02-26 - [High] Insecure File Permissions for Authentication Tokens
**Vulnerability:** Authentication tokens and related metadata were being written to disk with overly permissive file permissions (`0644`) and their parent directories with `0755`. This could allow other users on the system to read sensitive access tokens.
**Learning:** Default permissions in Go (`0644` for files, `0755` for directories) are not suitable for sensitive credentials, even inside container environments, as it violates the principle of least privilege.
**Prevention:** Always enforce restrictive permissions (`0600` for files via `os.WriteFile` and `0700` for directories via `os.MkdirAll`) when saving sensitive files such as authentication tokens or secrets to disk.

## 2025-05-05 - [High] Prevent SSRF and Secret Leakage in Download Tracking
**Vulnerability:** The Unsplash client's `TrackDownload` method accepted an arbitrary URL (`downloadLocation`) from the API response and appended the application's `Authorization` header to the outbound request without validation. This could lead to API key leakage if the API is spoofed or manipulated to return a malicious domain.
**Learning:** Always validate that dynamically supplied URLs from external APIs point to trusted domains before appending authentication headers or executing requests to prevent Server-Side Request Forgery (SSRF) and credential leakage.
**Prevention:** Validate the URL prefix (e.g., `strings.HasPrefix(downloadLocation, c.BaseURL)`) before issuing requests with sensitive headers.
## 2026-05-11 - [High] Use Secure File Permissions
**Vulnerability:** Files and directories were being created with overly permissive file permissions ( for files,  for directories), potentially exposing sensitive data or state to other users on the system.
**Learning:** The principle of least privilege dictates that files should only be readable and writable by the owner unless strictly necessary to be shared.
**Prevention:** Always use restrictive permissions ( for files via  and  for directories via  or ) for application state, metadata, and cached data, not just explicitly sensitive tokens.
## 2025-05-15 - [High] Use Secure File Permissions
**Vulnerability:** Files and directories were being created with overly permissive file permissions (`0644` for files, `0755` for directories), potentially exposing sensitive data or state to other users on the system.
**Learning:** The principle of least privilege dictates that files should only be readable and writable by the owner unless strictly necessary to be shared.
**Prevention:** Always use restrictive permissions (`0600` for files via `os.WriteFile` and `0700` for directories via `os.MkdirAll` or `os.Chmod`) for application state, metadata, and cached data, not just explicitly sensitive tokens.
## 2025-05-15 - [High] Prevent SSRF and Query Parameter Injection in External APIs
**Vulnerability:** External APIs (NASA, Pexels, Artic) constructed search URLs directly using `fmt.Sprintf` with unescaped user-provided search queries (e.g., `url := fmt.Sprintf(".../search?q=%s", query)`). This allows an attacker to inject arbitrary query parameters (e.g., `&limit=10000` or API key overrides).
**Learning:** Even internal or backend-to-external API communications are vulnerable to parameter injection if user input is not properly encoded.
**Prevention:** Always use `url.QueryEscape(query)` from the `net/url` package when dynamically appending search queries or parameters to an external API URL.
## 2025-05-15 - [High] Prevent SSRF and Query Parameter Injection in External APIs
**Vulnerability:** External APIs constructed URLs using `fmt.Sprintf` with unescaped user-provided parameters (e.g., `url := fmt.Sprintf(".../photos/%s", photoID)`). This allows an attacker to manipulate the URL structure (Path Traversal) or inject query parameters.
**Learning:** Variables embedded in URL paths must be escaped to prevent them from modifying the URL structure.
**Prevention:** Always use `url.PathEscape` from the `net/url` package when dynamically appending parameters to URL paths, and `url.QueryEscape` when appending to query strings.

## $(date +%Y-%m-%d) - Prevent Denial of Service in Image Downloader
**Vulnerability:** The application was downloading external image sources using `io.Copy(out, resp.Body)` without any byte limit, making it vulnerable to resource exhaustion or "ZIP bomb" style attacks if a malicious server returned a massive payload.
**Learning:** `io.LimitReader` is not sufficient for securing HTTP responses because it silently truncates the stream by returning `EOF` when the limit is reached, which `io.Copy` interprets as a successful, complete download. This leads to silent corruption.
**Prevention:** Always use `http.MaxBytesReader(nil, resp.Body, maxBytes)` for HTTP downloads. It actively returns an error (`"http: request body too large"`) when the limit is breached, allowing the application to detect the attack and properly clean up temporary files.

## 2026-05-20 - [Medium] Prevent Denial of Service (DoS) in API Response Parsing
**Vulnerability:** The REST API client (`FetchDeviceInfo`) was reading HTTP responses directly into memory using `io.ReadAll(resp.Body)` without enforcing any size limits. This allowed a malicious or compromised endpoint to exhaust application memory by returning a massive payload.
**Learning:** Similar to `io.Copy`, reading directly into memory with functions like `io.ReadAll` or decoding with `json.NewDecoder` directly from an HTTP response exposes the application to resource exhaustion vulnerabilities.
**Prevention:** Always wrap `http.Response.Body` with `http.MaxBytesReader(nil, resp.Body, maxBytes)` before passing it to `io.ReadAll`, `json.NewDecoder`, or similar parsers.

## 2024-05-20 - Unbounded JSON Response Read DoS Prevention
**Vulnerability:** Denial of Service (DoS) and memory exhaustion vectors caused by decoding JSON payloads from external HTTP API responses without memory bounds using `json.NewDecoder(resp.Body)`.
**Learning:** The application architecture lacked centralized boundary limits when integrating with third-party APIs (NASA, Pixabay, Unsplash, Artic, Pexels). While timeouts existed, large or infinite payload injections from compromised or malfunctioning endpoints could exhaust local system memory before completion.
**Prevention:** Always wrap `resp.Body` with `http.MaxBytesReader` configured with a generous but firm upper limit (e.g., 5MB) before passing the stream to `json.NewDecoder`. This terminates malicious/oversized reads defensively without interrupting legitimate traffic.

## 2026-06-04 - Fix Standard Library Vulnerabilities
**Vulnerability:** The application was using an older version of Go (`1.26.3`) which contained standard library vulnerabilities in `net/textproto` (GO-2026-5039) and `crypto/x509` (GO-2026-5037). These vulnerabilities were flagged during `govulncheck` execution.
**Learning:** Standard library vulnerabilities can be detected by `govulncheck`, even if no third-party module vulnerabilities are present.
**Prevention:** Always bump the Go compiler version uniformly across `go.mod`, GitHub Actions workflows (`.github/workflows/*.yml`), and `Dockerfile` to the latest patch release (e.g., `1.26.4`) when standard library vulnerabilities are disclosed.

## 2026-06-01 - [High] Prevent SSRF and Scheme Downgrade in URL Validation
**Vulnerability:** The application was validating a dynamically provided download URL by only comparing the `Host` portion to a trusted base URL. This allowed an attacker to bypass the validation by supplying a matching host but with an unintended scheme (e.g., changing `https://` to `http://` or `ftp://`), leading to potential credential leakage in plain text or SSRF scheme confusion.
**Learning:** Checking only the `Host` property of a parsed URL is insufficient when validating redirect or tracking URLs before appending sensitive authentication headers. Scheme downgrades can silently expose API keys.
**Prevention:** Always validate both the `Host` and `Scheme` properties when ensuring an externally supplied URL belongs to a trusted destination.

## 2026-06-10 - Strict URL Scheme Validation for Direct Providers
**Vulnerability:** Server-Side Request Forgery (SSRF) and localized path traversal via unbounded URL inputs.
**Learning:** The `direct` custom source provider accepted raw URL strings and directly initialized an `http.NewRequest` with them. Because Go's standard library can sometimes be tricked by exotic schemes, failing to explicitly lock the URL scheme at the ingestion boundary could allow local file access (`file://`), loopback requests (`http://localhost`), or internal network enumerations if the application logic evolves to use custom transports.
**Prevention:** Always validate external URL boundaries explicitly using `url.Parse` and verify the `Scheme` is precisely restricted to `"http"` or `"https"` before allocating an `http.Request`.

## 2026-06-19 - [High] Insecure File Permissions on Uploaded Data
**Vulnerability:** The web uploader endpoint (`/upload`) in `internal/health/server.go` saved uploaded artwork files using `os.WriteFile` with `0o644` permissions, making sensitive user images readable by any local system user.
**Learning:** Default permissions in code snippets often favor convenience (`0644`) over security. File upload processors must default to the principle of least privilege, restricting access solely to the user running the application.
**Prevention:** Always enforce restrictive file permissions (like `0600`) when writing dynamically uploaded user data or artifacts to disk.
