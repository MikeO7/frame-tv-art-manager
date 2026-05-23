# Claude Engineering Guidelines

This repository enforces strict production-grade code hygiene. Any agent working on this codebase must adhere to the canonical rules in `AI.md` and `AGENTS.md`.

## Core Verification Guardrails
Before completing any task, you **MUST** run the verification pipeline:
* **Run checks**: `make check`
* **Auto-fix formatting/imports**: `make fix`
* **Agent Self-Correction Loop**: `make agent-fix` (Iterate until it exits with status 0)

## Code Hygiene Rules
1. **Zero Slop**: Never write placeholders, `// TODO`, `// ...`, or stubs. Every line of code must be complete, compiling, and functional.
2. **Self-Correction Loop**: If a verification step fails, analyze the compiler, linter, or test failures, correct the code, and re-run `make agent-fix` until clean.
3. **No Over-Engineering**: Prefer Go standard library; avoid shallow abstractions or new external packages. Keep modules deep and interfaces small.
