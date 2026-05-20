## 2025-05-20 - Consolidating Samsung network utilities
**Learning:** The samsung package has several tiny files (rest.go, gate.go, remote.go, wol.go) that are only used by the main Client struct in client.go. These are perfect candidates for structural consolidation into a deep module to eliminate shallow wrappers and reduce internal fragmentation.
**Action:** Move the contents of rest.go, gate.go, remote.go, and wol.go into client.go as private methods on the Client struct to encapsulate all TV network operations within a single deep module.
