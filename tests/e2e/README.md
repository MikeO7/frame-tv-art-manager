# End-to-end flow inventory

`make e2e` builds the production binary and runs pinned tester-army/e2e journeys.
No model credentials are required. Each run records its revision, command,
platform, fixture description, and runner versions in `.e2e/environment.json`.
The runner writes `.e2e/report.json`, JUnit, Markdown, screenshots, and failure
traces. GitHub Actions retains these artifacts for seven days.

The app uses fresh temporary artwork and token directories. Configuration is
constructed from an allowlist rather than the operator's environment. The TV
address is loopback. Source tests use an ephemeral loopback HTTP server.
Successful shutdown removes each test's temporary state.

| User or operator flow | Runner coverage | Existing Go coverage and remaining boundary |
| --- | --- | --- |
| CLI help and version | Real binary exits successfully without state writes | `cmd/frame-tv-art-manager/main_test.go` |
| Missing TV address, malformed address, duplicate address | Real binary rejects before directory creation | `internal/config/load_environ_test.go`, `contract_test.go` |
| Invalid token, port, portrait mode, quality, slideshow interval, auto-off time | Real binary rejects before directory creation | Config suites cover remaining settings, ranges, conflicts, legacy precedence, warning fallbacks, and solar coordinates |
| Local folder and source-file bootstrap | Actual binary creates isolated artwork state and starter sources file | Main and collection suites cover symlinks, ownership, crashes, corrupt state, manifest migration, unsafe names, control files, and deduplication |
| Source download and last-known-good preservation | Actual binary imports a PNG from local HTTP; later new source returns 503 and original bytes remain | Sources suites cover all Unsplash, NASA, Art Institute, Pexels, Pixabay, direct-provider commands, pagination, authentication, truncation, malformed responses, size limits, and cleanup; live providers are not contacted |
| Raw upload for curl or iOS Shortcuts | Actual HTTP endpoint accepts PNG, preserves bytes, and deduplicates | Health suites additionally cover JPEG and iOS request format; physical Shortcuts not exercised |
| Browser file picker and multiple uploads | Chromium uploads valid and invalid images in one batch; reports Success, Deduplicated, and rejection | Real import endpoint and store are used |
| Empty, pending, loaded, failed uploader states | Chromium checks initial empty list, delayed pending upload and progress, success, invalid input, and network error | Network failure is deliberately intercepted; successful uploads are real. Phone width is checked and screenshotted |
| Authentication and wrong username/password | Real GET/POST return 401 with Basic challenge; complete filesystem hashes remain unchanged | Credentials are public local fixtures |
| Missing file field, invalid multipart, empty/truncated/non-image, oversized body | Real endpoint rejects with 400; complete filesystem hashes remain unchanged | Collection and optimization suites cover unsafe dimensions and formats |
| Cross-origin, malformed Origin, unknown route, unsupported method | Real endpoint rejects unsafe origins before import; 404 and 405/Allow contracts | Focused Go regression covers absent versus empty Origin, duplicate headers, oversized Origin, userinfo, paths, queries, fragments, scheme and host |
| Disabled uploader and dry-run | Real process returns 403; dry-run preserves every state-file hash | Reconcile and sync dry-run suites assert zero TV commands and durable writes |
| Liveness, readiness, health, detailed status | Real process stays live; failed TV cycle returns 503 readiness/health and records error status | Health contract suites cover successful, starting, stopping, and failed lifecycle states |
| Startup bind collision and SIGTERM | Real process exits on bind error; successful SIGTERM reports clean shutdown | App suites cover child supervision, preparation, cancellation, and bounded shutdown |
| Optimization, smart crop, pad/collage, color/HDR, museum effects, output limits | Covered by existing Go suites, not browser runner | `internal/optimize` tests validate pixels, dimensions, allocation limits, profiles, provider failures and output contracts |
| TV pairing and sensitive token permissions | Protocol integration fixture, not physical TV | Samsung adapter/connection/token suites and `production_pipeline_integration_test.go` |
| Art inventory, upload, delete, matte, slideshow, brightness, power, wake, capacity | Protocol fixture and application/domain suites, not physical TV | Samsung, sync and reconcile suites cover unknown-state refusal, selected/final-art preservation, idempotence, pending recovery, retry limits and storage-full behavior |
| Docker install, upgrades, release archives and healthcheck | Not run by browser runner | Existing CI build/containers and CLI healthcheck tests remain authoritative; no local container pulls |

The new Origin regression failed on the unmodified endpoint: a path-bearing
Origin received 200 and imported artwork. The endpoint now bounds the header,
rejects duplicate values, and accepts only an HTTP(S) origin without URL extras.
Missing Origin remains allowed for non-browser clients. Existing proxy-friendly
host comparison is preserved.

Real TV firmware, pairing prompts, remote buttons, displayed artwork, physical
Apple Shortcuts, and live source credentials require a separate authorized device
session. Protocol fixtures do not establish physical-device compatibility.
