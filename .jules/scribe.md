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
