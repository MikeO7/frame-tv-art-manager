## 2026-06-11 - [Dead Export Discovery]
**Learning:** Exported methods acting as reverse-lookups in data mapping structs (like `GetFilename` returning a key for a value) are often written "just in case" but never actually consumed by production application logic, serving purely as test artifacts.
**Action:** Audit public API boundaries of data structures using AST or text analysis tools like `grep` to ensure methods have active callers outside of their own test files before considering them vital.
## 2026-06-29 - [JSON Unmarshal Safe Pruning]
**Learning:** In Go, removing unused fields from structs exclusively used to unmarshal JSON responses is a safe dead-code optimization, as the `encoding/json` package silently ignores unmapped JSON keys without breaking the unmarshal process.
**Action:** When pruning for dead code, always check JSON unmarshaling structs for unused fields as they are safe targets.
