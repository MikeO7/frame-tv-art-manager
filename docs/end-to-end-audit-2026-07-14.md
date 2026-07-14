# End-to-End Codebase Audit — 2026-07-14

## Scope and completion standard

This audit covers every production Go package, the executable composition
root, tests, container configuration, GitHub Actions, operator documentation,
and repository engineering controls. The review used package-by-package static
inspection, call-site searches, architecture tracing, focused regression tests,
the race detector, linting, coverage analysis, workflow validation, dependency
tidying, and vulnerability scanning.

A finding is complete only when its implementation and regression tests are in
the repository and `make agent-fix` passes. "Verified" means the final gate also
passed the full `go test -race -count=1 -shuffle=on ./...` run and a production
binary build. Coverage is a risk control, not a claim that testing can prove the
absence of every possible defect.

## Findings register

| ID | Severity | Finding | Resolution and evidence | Status |
|---|---|---|---|---|
| F-01 | Critical | Two overlapping Samsung/sync implementations made mutation ordering and ownership ambiguous. | Replaced the legacy client/session/plan stack with the Samsung Adapter, Reconciliation, Convergence Engine, and Managed Engine modules. Removed the obsolete implementation and tests. | Resolved |
| F-02 | Critical | TV mutations could not be authorized from a single fresh, trustworthy observation. | Added typed observations, capability-scoped single-use authorization, inventory fingerprints, and fail-closed dispositions. Unknown power, Art Mode, inventory, or postcondition state cannot authorize display-changing or destructive work. | Resolved |
| F-03 | Critical | Interrupted TV mutations lacked durable recovery evidence. | Added reconciliation state, pending mutation records, receipts, recovery validation, and unknown-outcome handling. Persistence failures reach callers and health status. | Resolved |
| F-04 | Critical | Collection preparation did not consistently enforce item limits, unique digests, manifest facts, or returned Snapshot validity. | Centralized manifest validation and Snapshot materialization; `Store.Prepare` validates the projected and committed Snapshot before return and rejects unsafe inventory before manifest publication. | Resolved |
| F-05 | Critical | HTTP uploads had a second file writer outside the authoritative Artwork Collection transaction. | Removed the health-package persistence path. Uploads require and call the transactional `collection.Store`; startup fails fast when upload is enabled without that dependency. | Resolved |
| F-06 | Critical | Some final artwork moves could replace an operator-owned destination. | Added durable no-replace publication with `durablefs.MoveExclusive` and collision tests for downloads, optimizer renames, and collages. Existing destinations and sources are preserved on collision. | Resolved |
| F-07 | High | Source synchronization and transformation occurred before authoritative collection reconciliation, and provider Origin Keys were not durably represented. | Downloads remain private until `Store.Import` journals artwork and manifest together. Optimization runs in an isolated verified workspace; `Collection.Apply` commits additions, manifest state, and digest-guarded deletions through a recoverable batch journal. Source Origin Keys are durable and projected through renames. | Resolved |
| F-08 | High | Configuration was converted `Config -> Settings -> Config` at startup, duplicating the schema and creating a shallow compatibility module. | Startup now parses directly into one canonical `Config`, which owns runtime shutdown timing. Removed `Settings`, all nested duplicate types, `LegacyConfig`, `LoadSettings`, and both conversion directions. | Resolved |
| F-09 | High | Application HTTP and cycle goroutines were not fully owned by one supervised lifecycle. | Added the `app.Application` module with bind-before-ready startup, coordinated cancellation, bounded shutdown, resource cleanup, early-exit handling, and lifecycle health projection. | Resolved |
| F-10 | High | Predictable source temporary names could follow and truncate a hostile pre-existing symlink. | Source downloads now use randomized same-directory temporary files, explicit modes, sync/close checks, and no-clobber publication. Regression tests verify the former predictable path is untouched. | Resolved |
| F-11 | High | Manifest, transaction journal, reconciliation state, matte configuration, and source-manifest reads could allocate from unbounded control files. | Added bounded stable reads, regular-file and same-file checks, explicit maximum sizes, cancellation, and oversized sparse-file tests. | Resolved |
| F-12 | High | `MAX_ARTWORK_IMAGES` counted only files visited during one source cycle and was not enforced by collection preparation. | Capacity now uses the complete catalog, including operator files, and `Store.Prepare` independently enforces the collection limit. | Resolved |
| F-13 | High | A transient or partial provider failure risked turning an incomplete provider view into destructive intent. | Source failures retain last-known-good artwork. Source pruning remains fail-closed unless ownership is durably proven. | Resolved |
| F-14 | High | Dry-run paths could accidentally share mutating preparation behavior. | Dry runs bypass downloads, transforms, directory creation, transaction recovery writes, TV mutations, and upload enablement. Read-only validation and regression tests cover the contract. | Resolved |
| F-15 | High | File replacement durability and post-publication error classification varied by caller. | Added the `durablefs` module for atomic replacement, exclusive creation, exclusive moves, directory synchronization, and unknown-outcome classification. Transactional control files use restrictive modes. | Resolved |
| F-16 | Medium | Unsupported image responses such as WebP were relabeled as JPEG and failed late after reading the body. | Source downloads now validate supported MIME types before consuming the body and close rejected responses. | Resolved |
| F-17 | Medium | Steady-state cycles repeatedly rebuilt, staged, and decoded the same complete artwork tree. | Reused the source catalog rebuild, added cache invalidation on mutation, avoided the second collection scan when the manifest is unchanged, and added a verified preflight that skips staging and collection application when every item is already in its final form. Raw portrait pairing and uncertain metadata still take the conservative staging path. | Resolved |
| F-18 | Medium | YAML provider-map iteration produced nondeterministic fresh-install download ordering. | Provider keys are sorted before flattening; exact-order tests protect reproducibility. | Resolved |
| F-19 | Medium | Image work could scale CPU, memory, and goroutines with inputs rather than a process-wide envelope. | Added resource admission, transform concurrency and queue limits, bounded pixel workers, input byte/pixel accounting, cancellation, and deterministic output-contract tests. | Resolved |
| F-20 | Medium | The Samsung Adapter retained a broad runtime projection with inventory identifiers that no production caller used. | Removed the public runtime projection and reduced private retained state to authorization guard facts and backoff state. | Resolved |
| F-21 | Medium | Several test-only production wrappers and speculative interfaces increased the maintenance and testing surface. | Removed the speculative optimizer `Transformer`, runtime/GC snapshot wrapper, standalone reconciliation recovery operation, contextless Samsung close/event aliases, nondeterministic brightness wrappers, and obsolete artwork file-type helpers. | Resolved |
| F-22 | Medium | Security-sensitive configuration and control-file handling lacked one consistently validated contract. | Added cross-field validation, upload-token requirements, normalized paths, symlink rejection, duplicate-key/UTF-8 checks, safe token modes (`0600`) and directories (`0700`), and exact environment contract tests. | Resolved |
| F-23 | Medium | Health readiness, liveness, cycle failure, and upload behavior were not cleanly separated. | Added explicit lifecycle state, `/live`, readiness/health behavior, structured status projection, upload authentication/origin checks, and supervised HTTP lifecycle tests. | Resolved |
| F-24 | Medium | Mutation verification did not exhaustively distinguish applied, not-applied, unknown, canceled, unauthorized, timeout, and unreachable outcomes. | Added typed outcomes/errors and postcondition checks for inventory, settings, slideshow, selection, upload, deletion, power, and Wake-on-LAN operations. | Resolved |
| F-25 | Medium | Multi-TV Wake-on-LAN configuration could apply one ambiguous MAC address to several TVs. | Automatic Wake is disabled for ambiguous multi-TV configuration and validated per TV. | Resolved |
| F-26 | Low | Dead convenience functions, duplicate tests, coverage-only shims, and legacy protocol helpers obscured the supported interface. | Removed obsolete production and test surfaces after call-site verification. Retained deterministic brightness functions, admission metrics, context-aware connection shutdown, and private automatic recovery because they have active production roles. | Resolved |
| F-27 | Low | CI did not consistently exercise the race detector and the repository lacked one mandatory, reproducible completion gate. | Added/updated CI and race workflows. `make agent-fix` now covers formatting, workflow lint, Go lint, pre-commit checks, shuffled tests, aggregate coverage, vulnerability scanning, and anti-slop checks. | Resolved |
| F-28 | Low | Operator documentation and examples could drift from validated configuration and lifecycle behavior. | Updated the README, `.env.example`, Compose configuration, domain language, engineering rules, and focused architecture research documents. | Resolved |
| F-29 | High | Control-file readers duplicated incomplete stability checks, and source TXT parsing was contextless and unbounded. | Added the shared context-aware `durablefs.ReadStable` primitive with a positive byte cap, optional exact mode, regular/non-symlink enforcement, before/open/after identity checks, cancellation-aware reads, and close-error preservation. Migrated collection, reconciliation, matte, TXT, and YAML readers; both source formats are capped at 4 MiB and cached by content digest. | Resolved |
| F-30 | Medium | The health server depended on the full collection store even though uploads need only one operation. | Introduced the one-method `health.ArtworkImporter` seam and removed the Managed Engine forwarding method. Composition still injects the authoritative collection store without exposing unrelated collection operations. | Resolved |
| F-31 | High | Concurrent source processing made provider order, global numbering, and capacity admission scheduling-dependent, with a check-then-act race around the artwork limit. | Source synchronization now processes the already sorted manifest sequentially. Provider resolution, global numbering, and capacity admission are deterministic, and the authoritative collection transaction still revalidates the final limit. | Resolved |
| F-32 | Medium | The isolated optimizer transaction was correct but copied and decoded every file even when a cycle had no transformation work. | Added `optimize.RequiresStage`, driven by verified Snapshot metadata. It bypasses staging and `Collection.Apply` for finalized landscape JPEG/PNG inputs, reads only the minimum JPEG metadata needed to classify raw portraits, preserves portrait pairing behavior, and fails conservatively on uncertainty. | Resolved |
| F-33 | Low | A final set of test-only and pass-through production APIs remained after the architecture rewrite. | Removed the optimizer `Transformer`/file-request facade, environment test helpers, source-provider accessor, read-only catalog alias, direct download publication, test-only catalog mutation methods, Managed Engine `RunOnce`/`Apply` aliases, and contextless optimizer wrappers. Tests now exercise the supported seams or package-private implementation directly. | Resolved |

## Architecture closure

All architecture findings above are resolved. The final collection mutation
interface is `Prepare`, `Import`, and `Apply`:

- `Prepare` recovers durable intent and returns a verified Collection Snapshot.
- `Import` validates private input and transactionally publishes one item with
  its Origin Key.
- `Apply` validates a complete isolated staging directory and commits a
  multi-effect replacement using journaled additions, manifest publication,
  and digest-guarded deletions.

The optimizer receives only verified immutable Snapshot inputs, copies them
into a private `0700` workspace as independent `0644` files, performs all
transformations there, and returns rename evidence. It rejects symlinks,
non-regular files, traversal names, duplicate identities, and digest changes.
Cancellation or failure removes the workspace without touching committed art.
Recovery tests cover cancellation with retained intent, predecessor
preservation, successful replay, orphan cleanup, and no-clobber publication.

An independent Standards review and an independent Spec review were run after
the main implementation. They identified F-29 through F-33 and the expanded
steady-state concern in F-17. Every actionable review finding was implemented
and regression-tested before the final verification record below.

## Verification record

All commands below ran successfully against the final implementation on
2026-07-14:

| Verification | Result |
|---|---|
| `make agent-fix` | Passed on the first correction-loop run: Go formatting, `actionlint`, `golangci-lint` with 0 issues, all pre-commit hooks, anti-slop checks, shuffled package tests, aggregate coverage enforcement, and `govulncheck`. |
| Aggregate statement coverage | The two final shuffled gates covered 6,593–6,594 of 7,306 statements (**90.24%–90.25%**); the repository minimum is 90%. The conservative rounded result is **90.2%**. |
| `go test -race -count=1 -shuffle=on ./...` | Passed for every package. The race-instrumented optimizer suite completed in 149.579 seconds; no data races or test failures were reported. |
| `go build -mod=readonly -trimpath -o /tmp/frame-tv-art-manager ./cmd/frame-tv-art-manager` | Passed without warnings. |
| `/tmp/frame-tv-art-manager --version` | Passed and printed the expected development build metadata. |
| `go mod tidy -diff` | Passed with no module-file diff. |
| `git diff --check` | Passed with no whitespace errors. |
| Residual architecture API scan | No matches for the removed configuration, catalog, Managed Engine, provider, optimizer, runtime, or Samsung compatibility surfaces. |
| Placeholder and abnormal-termination scan | No `TODO`, `FIXME`, `XXX`, or `HACK` markers. Remaining `panic` calls are test failure/sentinel paths; remaining `os.Exit` calls are executable CLI boundaries. |

No finding in this register remains open, deferred, or accepted as debt. This
record captures the implementation and verification state immediately before
publication; repository history is the authority for commit and push details.
