## 2025-05-25 - Removing gopkg.in/yaml.v3
**Learning:** `gopkg.in/yaml.v3` can be cleanly removed if the config loader can natively fallback to `.txt` lists. Removing it required migrating `sources.yaml` to `sources.txt` in tests and configuration documentation without side-effects on tree shaking or system functionality.
**Action:** When evaluating unused packages, check if simple fallback formats (like txt) can naturally deprecate complex parsing dependencies (like YAML) to eliminate third-party code without losing user functionality.
