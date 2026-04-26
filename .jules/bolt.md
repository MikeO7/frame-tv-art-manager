## 2025-02-28 - Optimize Mutex Contention
**Learning:** Batch operations such as looping to delete multiple entries in maps protected by a lock can be bottlenecked by continuous mutex locking and unlocking overheads.
**Action:** Always prefer locking the map mutex once, mutating the map structure continuously in the loop before finally unlocking the map. I have added `DeleteBatch` to handle batch operations directly to reduce mutex lock contention.
