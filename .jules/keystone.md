## 2026-06-09 - Pass-through Files Increase Cognitive Load
**Learning:** Files that act solely as a pass-through layer for methods already defined in another file (e.g., `transport.go` acting as a public interface for unexported methods in `client.go`) surprisingly increase complexity and consumption friction. They force developers to cross file boundaries to understand what a public method actually does, acting as a leaky boundary around simple method exposure.
**Action:** Consolidate these pass-through files by directly exposing the underlying methods in their original context file. Delete the pass-through file entirely to reduce fragmentation and interface-to-logic ratio.
## 2026-06-13 - Interface Assertion Requires the Correct Package Import
**Learning:** Moving a type assertion like `var _ TVTransport = (*samsung.Client)(nil)` from `transport_check.go` to `session.go` requires the `samsung` package to be imported in the target file.
**Action:** When manually appending code blocks, always verify that the target file contains all necessary imports. Run a build check or run `goimports -w <file>` immediately after modifications.
