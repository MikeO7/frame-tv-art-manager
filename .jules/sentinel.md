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
