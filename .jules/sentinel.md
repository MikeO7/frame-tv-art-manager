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
