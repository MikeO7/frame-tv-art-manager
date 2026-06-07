## 2025-02-12 - Update Environment Map for Image Sources & System
**Learning:** `ARTWORK_SOURCES_FILE` was undocumented in `.env.example`.
**Action:** Update `.env.example` with the missing config schemas, specifically adding sections for "Image Sources & APIs", "Image Optimization & Processing" and "Advanced System Settings".
## 2026-05-24 - Context alignment for Public Utility Modules\n**Learning:** Public utility functions need explicit parameter breakdowns and real-world examples to prevent contextual decay.\n**Action:** Add comprehensive GoDocs to `OptimizeFile`, `NewClient`, and `LoadMapping` to ensure clarity.
## 2026-05-28 - Sync Environment Configurations in .env.example
**Learning:** The .env.example file was missing VERIFY_TLS and required solar brightness dependencies (LOCATION_LATITUDE, LOCATION_LONGITUDE) were not clearly annotated.
**Action:** Update .env.example with missing variables and clear contextual examples to prevent configuration confusion.
## 2026-06-04 - Documentation sync for HEALTH_PORT
**Learning:** The application's `HEALTH_PORT` defaults to `8080` (as defined in `internal/config/load.go`), but `.env.example` incorrectly listed it as `0`, which disables the health server entirely.
**Action:** Always verify `.env.example` defaults against the actual code values defined in `internal/config/load.go`.
## 2026-06-05 - Clean Up Temporary Scripts Before Committing
**Learning:** Using temporary Python or Bash scripts to procedurally edit large markdown files is effective, but leaving them behind pollutes the workspace and gets flagged during code review.
**Action:** Always append a cleanup step (`rm patch_readme.py`) when running workspace modification scripts to prevent tracked workspace pollution.
## 2026-06-05 - CI Security Workflow Fix for Standard Library Vulnerabilities
**Learning:** During the automated GitHub workflow execution (`govulncheck`), the security scanner identified two vulnerabilities `GO-2026-5039` and `GO-2026-5037` inherently tied to the Go 1.26.3 standard library, forcing a bump.
**Action:** Bumping the Go toolchain version across `go.mod`, `.github/workflows/*.yml`, and `Dockerfile` successfully eliminated standard library vulnerability false flags during CI validation.
## 2026-06-07 - Sync Environment Configurations in README.md
**Learning:** The `README.md` file was missing several environment variables documented in `internal/config/load.go` (e.g. `UNSPLASH_APP_ID`, `ENABLE_REST_GATE`, `SLIDESHOW_ENABLED`, `TV_MAC`, `PEXELS_API_KEY`, etc.), leading to undocumented configuration options.
**Action:** Always verify `README.md` against the actual code values defined in `internal/config/load.go`.
