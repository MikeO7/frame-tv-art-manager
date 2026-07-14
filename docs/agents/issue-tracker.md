# Issue tracker: GitHub

Issues and PRDs for this repository live as GitHub issues. Use the `gh` CLI for
all operations and infer the repository from the current clone.

## Conventions

- Create issues with `gh issue create`.
- Read an issue and its discussion with `gh issue view <number> --comments`.
- List issues with `gh issue list` and request JSON fields for structured work.
- Add discussion with `gh issue comment <number>`.
- Apply or remove labels with `gh issue edit`.
- Close issues with `gh issue close` after recording the resolution.

## Pull requests as a triage surface

**PRs as a request surface: no.**

GitHub shares one number space across issues and pull requests. Resolve an
ambiguous number with `gh pr view` and fall back to `gh issue view`.

## Publishing and fetching

When a skill says to publish to the issue tracker, create a GitHub issue. When a
skill says to fetch a ticket, read the full issue and its comments.

## Wayfinding operations

- **Map**: a single issue labelled `wayfinder:map` containing the destination,
  notes, decisions, fog, and scope.
- **Child ticket**: an issue linked to the map as a GitHub sub-issue and labelled
  `wayfinder:research`, `wayfinder:prototype`, `wayfinder:grilling`, or
  `wayfinder:task`.
- **Blocking**: use GitHub's native issue dependencies. If unavailable, put a
  `Blocked by:` line at the top of the child body.
- **Frontier**: the map's first open, unblocked, unassigned child.
- **Claim**: assign the ticket to the driving developer before work begins.
- **Resolve**: comment with the answer, close the ticket, and append a linked
  one-line gist to the map's Decisions-so-far section.

Create issues before wiring their sub-issue and blocking relationships because
those relationships require issue identifiers.
