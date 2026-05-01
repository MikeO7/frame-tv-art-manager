## 2025-02-26 - [Medium] Fix Potential Slowloris DOS Attack
**Vulnerability:** Go `http.Server` configured without `ReadHeaderTimeout`, leading to potential Slowloris Denail of Service (DoS) attacks.
**Learning:** Default Go HTTP server configurations do not enforce timeouts, which is a common security pitfall.
**Prevention:** Always configure standard timeout fields (like `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`) when initializing `http.Server`.

## 2025-02-26 - [Medium] Fix Potential Slowloris DOS Attack
**Vulnerability:** Go `http.Server` in `internal/health/server.go` configured without `ReadTimeout`, `WriteTimeout`, and `IdleTimeout`, leading to potential Slowloris Denial of Service (DoS) attacks and resource exhaustion.
**Learning:** Default Go HTTP server configurations do not enforce read, write, or idle timeouts. This is a common security pitfall.
**Prevention:** Always configure standard timeout fields (like `ReadTimeout`, `WriteTimeout`, `IdleTimeout`) along with `ReadHeaderTimeout` when initializing `http.Server`.
## 2025-02-23 - [Insecure Token Storage Permissions]
**Vulnerability:** TV authentication tokens and related metadata were being written to disk with `0644` permissions, and the token directory was created with `0755`. This allowed any user on the system to read the sensitive authentication tokens.
**Learning:** Hardcoded tokens or authentication credentials written to disk must always be secured using restrictive permissions (`0600` for files, `0700` for directories) to prevent unauthorized access by other local users.
**Prevention:** Always use `0600` when saving secrets with `os.WriteFile` and `0700` with `os.MkdirAll` for directories containing secrets.
