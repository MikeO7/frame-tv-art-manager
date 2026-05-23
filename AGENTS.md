# Agent Instructions

This repository uses [AI.md](AI.md) as the canonical engineering and anti-slop rule set.

Before completing any task:

1. **Read & Follow AI Rules**: Strictly read and follow [AI.md](AI.md).
2. **Self-Correction Verification**: Always run `make agent-fix` (or `make check`) before declaring any task complete. This runs the full testing, linting, formatting, vulnerability, and anti-slop pipelines.
3. **Loop Until Clean**: If any check fails, you MUST analyze the linter or test failures, correct the code, and re-run `make agent-fix` until it passes with zero warnings (exit code 0).
4. **Zero Slop**: Do not write any placeholders, truncated code, or TODO/FIXME comments. Any placeholder will instantly fail the pre-commit anti-slop check.
