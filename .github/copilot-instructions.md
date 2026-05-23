# GitHub Copilot System Instructions

This repository follows strict production-grade code hygiene. Any assistant modifying this codebase must adhere to the rules in `AI.md` and `AGENTS.md`.

## Core Verification Commands
* Run complete check: `make check`
* Auto-repair formatting/imports: `make fix`
* Agent Self-Correction Loop: `make agent-fix` (Iterate until it exits with status 0)

## AI Assistant Constraints
1. **Never write stubs or placeholders** (like `// TODO: implement this`, `// ...`, or empty mock blocks).
2. **Always verify your code** by running `make agent-fix` before declaring a task complete.
3. **Iterate on errors**: If `make agent-fix` returns a non-zero exit code, analyze the output, edit the code, and run it again in a loop until it passes with exit code 0.
4. **Pragmatic Go Idioms**: Check every error explicitly, close resources immediately via `defer`, and never introduce global state.
