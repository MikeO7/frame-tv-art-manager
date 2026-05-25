## 2025-05-25 - Removing txt config format support
**Learning:** Legacy configuration fallback parsing functions (like `.txt` files) can often be removed safely to consolidate configuration around a more robust, standard format (like `.yaml`). Adding explicit schema error-logging during the fallback removal ensures users are properly guided.
**Action:** Always verify that simplifying inputs does not remove explicitly required user flexibility, and ensure you update `loader_test.go` and log explicitly if parsing fails to avoid mysterious runtime skips.
