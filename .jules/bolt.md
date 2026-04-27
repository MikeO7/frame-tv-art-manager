## 2024-05-15 - [Batch Disk I/O Operations]
**Learning:** In Go applications, performing disk I/O operations (like saving JSON mappings) inside a loop can severely degrade performance, especially when uploading multiple items or running in environments with slower disks.
**Action:** Always extract disk I/O operations, such as saving state files or mappings, outside of loops to batch them and improve overall performance by reducing repeated filesystem access. This is particularly important for functions that iterate over collections to perform tasks like uploads or downloads.
## 2026-04-27 - [Batch Mutex Lock Operations]
**Learning:** Repeatedly locking and unlocking a mutex in a tight loop to modify shared state (such as maps) introduces significant lock contention overhead and latency.
**Action:** Extract map modifications (e.g., deletions) that happen in loops into dedicated batch methods like `DeleteBatch` that acquire the lock only once for the entire batch operation.
