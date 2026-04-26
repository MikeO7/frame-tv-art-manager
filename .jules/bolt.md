## 2025-02-28 - Optimize Mutex Contention
**Learning:** Batch operations such as looping to delete multiple entries in maps protected by a lock can be bottlenecked by continuous mutex locking and unlocking overheads.
**Action:** Always prefer locking the map mutex once, mutating the map structure continuously in the loop before finally unlocking the map. I have added `DeleteBatch` to handle batch operations directly to reduce mutex lock contention.
## 2025-02-28 - Race Condition in Websocket Client
**Learning:** During test shutdown or websocket disconnects, `c.conn = nil` can be executed by `Close()` while `recvLoop()` is running in a separate goroutine reading from `c.conn`, leading to a race condition and a possible nil pointer dereference (segmentation fault) if `ReadMessage()` is invoked on a suddenly nil `conn`.
**Action:** Removed `c.conn = nil` inside `Close()` after `err := c.conn.Close()`. `c.conn.Close()` already terminates the active connection causing `ReadMessage()` in `recvLoop` to return with an error, gracefully avoiding nil pointer crashes.
