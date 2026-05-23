# Security Policy

The Frame TV Art Manager team is committed to delivering a robust, production-grade tool to keep your Samsung Frame TV in sync with high-res artwork safely and securely. This document outlines our security policies, vulnerability reporting procedure, and the architectural hardening guidelines we enforce in this codebase.

## Supported Versions

We actively maintain and support the following versions of the Frame TV Art Manager:

| Version | Supported | Notes |
| :--- | :--- | :--- |
| **v1.x** | ✅ Yes | Latest active development version. |
| **v0.x** | ❌ No | Please upgrade to the latest v1.x release. |

## Reporting a Vulnerability

We take the security of this application and your integration seriously. If you discover a vulnerability, please report it immediately and responsibly.

### How to Report

1. **Private Vulnerability Reporting**: The preferred method is to use GitHub's private vulnerability reporting feature on our repository page. Go to **Security** -> **Advisories** and click **Report a vulnerability**. This keeps the details confidential until we publish a patch.
2. **Email**: If you prefer, or if the private reporting tool is unavailable, you can email security reports directly to the maintainer's primary contact address.

Please include the following information in your report:
- A detailed description of the vulnerability.
- Step-by-step instructions (or a Proof of Concept script) to reproduce the issue.
- The potential impact of the vulnerability.
- Any suggested mitigations or fixes.

We will acknowledge receipt of your vulnerability report within 48 hours and work with you to coordinate a secure and prompt disclosure.

---

## Security Architecture & Hardening

This repository enforces proactive, defense-in-depth security principles. Every contribution is held to these strict implementation standards to ensure user configurations and network integrations remain secure.

### 1. Restrictive File Permissions (Least Privilege)
We strictly enforce the principle of least privilege for filesystem operations. Sensitive information, including Samsung TV authentication tokens, state configurations, and cached files, must be saved with restrictive permissions:
- Files must be created with `0600` permissions (owner read/write only) via `os.WriteFile` or explicit `os.Chmod`.
- Parent directories must be created with `0700` permissions (owner read/write/execute only) via `os.MkdirAll`.

### 2. Resource-Bounded HTTP Readers (DoS Prevention)
To prevent Denial of Service (DoS) and memory exhaustion attacks (e.g., zip bombs or endless data streams from compromised or malicious external providers), the application restricts HTTP read bounds:
- All external HTTP response bodies must be wrapped using `http.MaxBytesReader` configured with a strict upper limit (typically `5MB`) before passing the stream to `io.Copy`, `io.ReadAll`, or `json.NewDecoder`.
- We avoid `io.LimitReader` for HTTP responses because it silently truncates data upon reaching the limit, which can lead to silent data corruption instead of raising a proper error.

### 3. Server Timeout Hardening (Slowloris Mitigation)
All Go `http.Server` instances running inside the application (such as health check and metadata endpoints) must explicitly configure request and response timeouts. Standard server initializations must define:
- `ReadHeaderTimeout`
- `ReadTimeout`
- `WriteTimeout`
- `IdleTimeout`

Default configurations that allow unbounded timeouts are strictly banned to prevent Slowloris-style connection exhaustion attacks.

### 4. Safe Parameter Validation (SSRF & Injection Prevention)
Integrations with third-party APIs (NASA, Unsplash, Pexels, Art Institute of Chicago, etc.) must guarantee URL integrity and prevent Server-Side Request Forgery (SSRF) and query parameter injections:
- All dynamic query parameters must be properly escaped using `url.QueryEscape`.
- All dynamic path variables must be properly escaped using `url.PathEscape`.
- Dynamic download URLs returned by third-party APIs (e.g., Unsplash download location trackers) must be validated against a list of trusted domain prefixes before appending authentication headers or executing requests.

---

## Automated Security Pipelines

To maintain high code quality and secure operation, the following automated security scans run on every pull request and commit:

- **Secret Scanning & Push Protection**: Managed via GitHub CLI and repository settings to block commits containing private credentials.
- **GitLeaks**: A pre-commit and CI hook that detects secrets, tokens, and private keys.
- **GoVulnCheck**: An automated vulnerability analyzer (`govulncheck`) that inspects Go package dependencies for known CVEs.
- **Makefile Audits**: The validation suite can be run locally at any time using:
  ```bash
  make check
  ```
