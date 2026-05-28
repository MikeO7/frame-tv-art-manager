## 2025-02-12 - Update Environment Map for Image Sources & System
**Learning:** `ARTWORK_SOURCES_FILE` was undocumented in `.env.example`.
**Action:** Update `.env.example` with the missing config schemas, specifically adding sections for "Image Sources & APIs", "Image Optimization & Processing" and "Advanced System Settings".
## 2026-05-24 - Context alignment for Public Utility Modules\n**Learning:** Public utility functions need explicit parameter breakdowns and real-world examples to prevent contextual decay.\n**Action:** Add comprehensive GoDocs to `OptimizeFile`, `NewClient`, and `LoadMapping` to ensure clarity.
## 2026-05-28 - Sync Environment Configurations in .env.example
**Learning:** The .env.example file was missing VERIFY_TLS and required solar brightness dependencies (LOCATION_LATITUDE, LOCATION_LONGITUDE) were not clearly annotated.
**Action:** Update .env.example with missing variables and clear contextual examples to prevent configuration confusion.
