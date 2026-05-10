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

## 2025-05-10 - [High] Prevent Query Parameter Injection and SSRF in API Clients
**Vulnerability:** External API clients (NASA, Pexels, Art Institute of Chicago) were constructing search URLs by directly interpolating unescaped user-supplied query strings using `fmt.Sprintf` without URL encoding. This could allow an attacker to inject arbitrary query parameters or manipulate the URL structure, leading to Query Parameter Injection and potential Server-Side Request Forgery (SSRF).
**Learning:** Always validate and sanitize user-supplied input before using it to construct external API URLs. Direct string interpolation of search queries into URLs bypasses URL encoding, allowing attackers to inject structural characters like `&` and `=` to modify the API request.
**Prevention:** When constructing external API URLs with user-supplied search queries in Go, always use `url.QueryEscape(query)` from the `net/url` package within `fmt.Sprintf` to safely encode the input and prevent Query Parameter Injection.
