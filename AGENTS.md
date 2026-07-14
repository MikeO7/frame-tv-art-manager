# Repository Instructions

This is the single, vendor-neutral instruction file for humans and AI coding
assistants working in this repository. Do not add tool-specific rule files that
duplicate it.

## Project

`frame-tv-art-manager` is a production Go application for Samsung Frame TVs
running Tizen 8.0 or newer. The target artwork resolution is 3840x2160. Keep
the core engine dependency-light and prefer the Go standard library.

## Engineering rules

- Make surgical, complete changes. Never leave placeholders, truncated code,
  empty implementations, or `TODO`/`FIXME` comments.
- Keep modules deep and interfaces small. Prefer locality over shallow helper
  packages and avoid speculative abstractions.
- Accept `context.Context` first in functions that perform I/O and honor
  cancellation.
- Handle every error explicitly and wrap it with `%w` when adding context.
- Use `slog` with structured key-value fields.
- Pass dependencies through constructors rather than adding global state.
- Close acquired resources promptly with `defer` and preserve errors that
  affect correctness or durability.
- Treat authentication tokens and sensitive state as `0600`; their directories
  must be `0700`.
- Artwork files may be `0644` where SMB/NFS readability requires it.
- Add regression tests for behavior changes. Prefer table-driven tests and
  `httptest` for network protocols.
- Keep aggregate test coverage at or above 90 percent.
- Update `README.md` when configuration or operator-visible behavior changes.
- Preserve unrelated worktree changes.

## Data-safety invariants

- A transient provider or network failure must not delete last-known-good art.
- Dry runs perform no durable local mutation and no TV mutation.
- Persisted files are written transactionally; interrupted writes must not
  expose truncated state.
- Unknown TV state cannot authorize destructive or display-changing work.
- Persistence failures must reach the caller and health reporting.
- Control files in the artwork directory are never cataloged, renamed,
  optimized, deduplicated, or uploaded.

## Verification

Before declaring any change complete, run:

```bash
make agent-fix
```

If it fails, fix every reported problem and rerun it until it exits zero with
no warnings. The command covers formatting, GitHub Actions validation, linting,
pre-commit checks, tests, coverage, vulnerability scanning, and anti-slop rules.

## Skills

Reusable agent skills live in [`.agents/skills`](.agents/skills). An assistant
that supports repository skills should discover `*/SKILL.md` files there. Other
assistants may read the relevant skill directly and follow it as a workflow.
Skill-specific capabilities such as sub-agents are optional: when unavailable,
perform the same steps sequentially without weakening verification or safety.

### Issue tracker

Issues, specs, and wayfinding maps live in GitHub Issues. See
[`docs/agents/issue-tracker.md`](docs/agents/issue-tracker.md).

### Triage labels

Use the canonical triage-role label vocabulary. See
[`docs/agents/triage-labels.md`](docs/agents/triage-labels.md).

### Domain docs

This is a single-context repository. See
[`docs/agents/domain.md`](docs/agents/domain.md).
