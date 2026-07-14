# Domain docs

This is a single-context repository. Engineering skills consume its domain
documentation using the rules below.

## Before exploring

- Read `CONTEXT.md` at the repository root when it exists.
- Read relevant decisions under `docs/adr/` when that directory exists.
- If either is absent, proceed silently. Domain-modeling workflows create them
  lazily when terminology or a durable architectural decision is resolved.

## Language

Use the canonical terms defined in `CONTEXT.md` in issue titles, refactor
proposals, hypotheses, tests, and documentation. Do not substitute synonyms
that the glossary explicitly rejects.

When a needed domain concept is absent, reconsider whether it belongs in the
domain language. Add it through the domain-modeling workflow only when it is a
real project-specific concept.

## Decisions

Surface any conflict with an existing ADR explicitly. Do not silently override
a recorded decision.
