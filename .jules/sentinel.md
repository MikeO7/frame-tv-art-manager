## 2025-02-26 - [Medium] Fix Potential Slowloris DOS Attack
**Vulnerability:** Go `http.Server` configured without `ReadHeaderTimeout`, leading to potential Slowloris Denail of Service (DoS) attacks.
**Learning:** Default Go HTTP server configurations do not enforce timeouts, which is a common security pitfall.
**Prevention:** Always configure standard timeout fields (like `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`) when initializing `http.Server`.

## 2025-02-26 - [Medium] Fix Potential Slowloris DOS Attack
**Vulnerability:** Go `http.Server` in `internal/health/server.go` configured without `ReadTimeout`, `WriteTimeout`, and `IdleTimeout`, leading to potential Slowloris Denial of Service (DoS) attacks and resource exhaustion.
**Learning:** Default Go HTTP server configurations do not enforce read, write, or idle timeouts. This is a common security pitfall.
**Prevention:** Always configure standard timeout fields (like `ReadTimeout`, `WriteTimeout`, `IdleTimeout`) along with `ReadHeaderTimeout` when initializing `http.Server`.

## 2025-02-26 - [High] Fix Insecure Permissions on Authentication Tokens
**Vulnerability:** Saved TV authentication tokens and their parent directory were created with over-permissive world-readable permissions (0644 for files, 0755 for directory), allowing local unprivileged users to extract tokens and take over TV connections.
**Learning:** Hardcoding permissions without considering context (like storing secrets) can leak sensitive credentials locally. `//nolint:gosec` was inappropriately used to suppress legitimate security warnings about over-permissive file access.
**Prevention:** Always enforce restrictive permissions (e.g., `0600` for files via `os.WriteFile` and `0700` for directories via `os.MkdirAll`) when storing secrets or authentication tokens.
