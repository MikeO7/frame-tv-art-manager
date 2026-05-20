---
name: deep-module-refactor
description: |
  Expert software architecture skill based on John Ousterhout's "A Philosophy of Software Design."
  Eliminates software entropy by detecting shallow modules and refactoring them into deep modules.

  A shallow module has a high interface-to-logic ratio — it exposes too many configuration details,
  internal types, or multiple tiny functions that do very little heavy lifting on their own.
  A deep module abstracts massive internal complexity behind an exceptionally simple, stable interface.

  Trigger when the user asks to:
  - Refactor or simplify a module, package, or file cluster
  - Reduce coupling or eliminate leaky abstractions
  - Apply "deep module" or Ousterhout principles
  - Improve architecture without changing external behavior
  - Audit a codebase for shallow wrappers or fragmented utilities

  DO NOT trigger for:
  - Simple one-file bug fixes with no architectural impact
  - Pure performance optimizations with no interface changes
  - Adding new features from scratch
---

# Deep Module Refactor

A two-phase skill that detects and eliminates shallow modules, replacing them with deep modules
following the principles in John Ousterhout's *A Philosophy of Software Design*.

---

## Phase 1: Analysis & Architectural Mapping

**Do not modify, create, or delete any files during this phase.**

Perform a thorough scan of the target codebase or module cluster. Your goal is to identify
fragmented, tightly coupled, or shallow-wrapper file groups that violate deep module principles.

### Step 1 — Scan the workspace

Use directory listing and file reading tools to map the target area. Look for:

- **High interface-to-logic ratio**: Functions or exported symbols that do very little work on their own.
- **Leaky abstractions**: Internal types, error variants, or config structs that are forced onto callers.
- **Shallow wrappers**: Files or packages that exist only to delegate to another single file/package.
- **Fragmentation**: Logic that logically belongs together but is split across many small files with thin interfaces.
- **Tight coupling**: Consumers that import many internal sub-packages when they only need one concept.

### Step 2 — Generate an Architectural Proposal

Present a structured plan with these four sections:

#### 1. Shallow Module Detection
List specific files or packages where interface complexity outweighs implementation.
Call out leaky abstractions by name. Quote specific exported symbols that reveal internals.

#### 2. The Target Boundary
Propose the new, singular "Deep Module" boundary.
Define the clean, simplified public API (function signatures, types, or interface) that will
replace the fragmented surface. The interface must be dead-simple — hide everything that
doesn't need to be a decision for the caller.

#### 3. Internal Consolidation Plan
Explain how internal sub-components, private helpers, and utility files will be encapsulated
inside the new boundary. Name which files become private/unexported and which are merged.

#### 4. Impact Assessment
List every file or consumer outside the module that will need updated imports, call sites, or
type references. Estimate the blast radius.

### Step 3 — Stop and await approval

Present the Architectural Proposal to the user as an implementation plan artifact.
**Do not proceed to Phase 2 until the user explicitly approves the proposal.**

---

## Phase 2: Refactoring Execution (Post-Approval Only)

Execute the refactoring after receiving explicit user approval. Follow these strict constraints:

### Constraint 1 — Preserve Interface Stability
The new public interface must remain dead-simple after the refactor.
- Do not expose internal mechanisms, implementation-specific error types, or config edge cases unless absolutely necessary.
- The caller should need to know as little as possible to use the module correctly.

### Constraint 2 — Encapsulate Complexity
Move shallow utilities, internal data models, and helpers into the deep module's private scope:
- Make internal files unexported (lowercase package names, unexported symbols).
- Merge thin single-function files into cohesive internal files grouped by responsibility.
- Delete files that become empty or redundant after consolidation.

### Constraint 3 — Verify and Test
Before modifying internal structure:
1. Confirm existing tests cover the public interface. If coverage is insufficient, **write tests first** that exercise the module strictly via its public API.
2. After the refactor, run the full test suite to verify no regressions.
3. Update any tests that previously tested internal details — redirect them to test via the new public interface.
4. Run `make check` (or the equivalent verification command for this repo) before declaring completion.

### Constraint 4 — Enforce Type Safety
- Maintain total type safety across all refactored boundaries.
- Remove dead code: unexported functions never called internally, orphaned exports no longer part of the public API.
- Eliminate redundant type aliases or wrapper types that existed only to bridge the old shallow interfaces.

### Step-by-step execution order

1. **Write missing tests** (if any) against the current public interface before touching internals.
2. **Create the new deep module file(s)** with the approved public API signatures.
3. **Move internal logic** into the new module's private scope.
4. **Update all call sites** identified in the Impact Assessment.
5. **Delete orphaned files** that are now fully encapsulated or replaced.
6. **Run tests and linting** — fix any failures before moving on.
7. **Update documentation** (comments, README sections, AI.md if applicable) to reflect the new interface.

---

## Guidelines

- Always complete Phase 1 in full before writing a single line of production code.
- The proposal artifact is the contract — do not deviate from the approved boundary without re-presenting it.
- Prefer fewer, more powerful functions over many small ones. A single `Process(ctx, input)` that does the right thing is better than five `Validate`, `Transform`, `Serialize`, `Write`, `Flush` calls the caller must orchestrate.
- Deep modules should be boring to use: obvious defaults, minimal required arguments, no surprises.
- If a refactor would require exposing internal complexity to preserve backward compatibility, flag it explicitly in the proposal and discuss the trade-off with the user.
