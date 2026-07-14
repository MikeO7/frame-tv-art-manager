# Recoverable Reconciliation mutations

Research date: 2026-07-12

## Decision

Create one deep `internal/reconcile` module that owns a complete Sync Cycle for
one TV: recovery, observation, pure planning, durable intent, guarded execution,
postcondition observation, durable commit, and health outcome. Reconciliation
must not attempt distributed rollback across the process and TV. It converges by
recording enough local intent before each external mutation, classifying the
adapter receipt, and resolving every uncertain result from a fresh trustworthy
TV observation before another mutation is allowed.

Use a single durable per-TV state file, not separate mapping, capacity, and
progress files. It contains digest-based ownership plus at most one pending
operation. Replacing this file is the local commit boundary and uses the chosen
`durablefs` contract. One pending operation keeps recovery finite, makes the
state machine inspectable, and matches the Samsung adapter's serialized command
contract ([durable-file-semantics.md](durable-file-semantics.md#decision),
[fail-closed-tv-state.md](fail-closed-tv-state.md#recommended-interface)).

The current executor has no journal or outcome model. It retries any upload
error, even after D2D transfer may have completed; performs TV deletion before
mapping persistence; and discards display, brightness, slideshow, power, and
capacity persistence failures ([execution.go](../../internal/sync/execution.go#L16-L70),
[execution.go](../../internal/sync/execution.go#L83-L204),
[execution.go](../../internal/sync/execution.go#L206-L307),
[session.go](../../internal/sync/session.go#L187-L218)). Those paths cannot
distinguish not-applied from applied or unknown and therefore cannot safely
retry after interruption.

## Module boundary

The application-facing surface should remain context-first and small:

```go
package reconcile

type Service interface {
	Run(ctx context.Context, request Request) (Result, error)
	Recover(ctx context.Context, request RecoveryRequest) (RecoveryResult, error)
}

type Request struct {
	CycleID  string
	TV       samsung.Adapter
	Snapshot collection.Snapshot
	Policy   Policy
	DryRun   bool
}
```

`Run` always invokes recovery first. `Recover` is public only so startup and
readiness can recover all configured TVs before normal cycles; it is not a
second execution API. The module privately owns its state store, planner,
clock, command-ID generator, and structured logger through its constructor.
The planner is a pure function over a committed Collection Snapshot, a known TV
Inventory, committed per-TV state, and immutable policy.

Reconciliation consumes only `collection.Snapshot` values whose generation and
full Artwork Digests were verified after collection recovery. Filename is
display metadata, not identity. This fixes the existing filename-to-content-ID
mapping, which can incorrectly transfer an old TV identity after optimization
or collage changes the bytes
([artwork-collection-transaction.md](artwork-collection-transaction.md#snapshot-effects-for-reconciliation),
[mapping.go](../../internal/sync/mapping.go#L13-L18)).

## Durable state

Store `TOKEN_DIR/tv_<stable-id>_reconciliation.json` with mode `0600` under a
`0700` directory. The stable key must include observed TV identity; an IP
address alone may be reused by a different device. A representative schema is:

```go
type State struct {
	Version              int
	TV                    TVIdentity
	Revision              uint64
	Bindings              map[artwork.Digest]Binding
	Tombstones            map[string]Tombstone
	Capacity              CapacityEvidence
	Pending               *Pending
	LastCompleteCycleID   string
	LastCollectionGen     string
}

type Binding struct {
	Digest      artwork.Digest
	ContentID   string
	Name        string
	ConfirmedAt time.Time
}

type Pending struct {
	OperationID        string
	CycleID            string
	CollectionGen      string
	PolicyFingerprint  [sha256.Size]byte
	InventoryBefore    InventoryFingerprint
	Command            CommandIntent
	Phase              Phase
	Receipt            *ReceiptSummary
}

type Phase uint8 // Prepared, OutcomeUnknown, Applied
```

The file is the journal: never maintain a second progress file whose commit can
diverge. Before a TV command, persist `Prepared` with exact preconditions and
command intent. After a positive receipt, persist `Applied` plus the receipt.
Then fold its semantic effect into bindings/tombstones/capacity and clear
`Pending` in one durable replacement. If either post-TV write fails, the cycle
stops degraded and the journal remains recoverable. If a `durablefs` result is
unknown, reload and validate the state file before deciding which revision is
authoritative.

Persisting `Prepared` does not prove a request was sent. On restart, every
`Prepared`, `OutcomeUnknown`, or `Applied` entry is recovered through fresh TV
observation; replay is never the first recovery action. The state validates
version, TV identity, unique nonblank content IDs, digest encodings, revisions,
phase/command compatibility, and Collection generation. A malformed primary
may use a separately validated prior revision only when doing so cannot erase a
newer pending intent; otherwise readiness is 503 and all TV mutation is blocked.

## Operation protocol

For each planned command:

1. Reconfirm that the exact Collection generation and policy fingerprint still
   match the plan.
2. Durably persist one `Prepared` intent. A failure sends zero TV mutation
   bytes and reaches health.
3. Call `samsung.Adapter.Apply` with its opaque authorization. The adapter
   performs immediate command-specific preflight and serializes the guard and
   write.
4. Classify the receipt as `NotAttempted`, `NotApplied`, `Applied`, or
   `Unknown`. Only `NotAttempted` and explicitly retryable `NotApplied` may be
   retried within the same cycle, and only after a new observation and a fresh
   authorization. `Unknown` is never retried.
5. For `Applied`, durably record the receipt before dependent commands. For
   `Unknown`, durably change the phase to `OutcomeUnknown` when possible; if
   that write fails, the existing `Prepared` intent is already sufficient to
   force recovery.
6. Resolve the command's postcondition from the positive receipt or a fresh
   observation, fold the effect into durable state, clear the intent, and only
   then continue.

There is no generic compensation. A compensating TV mutation has the same
failure modes as the original and can destroy operator state. Recovery either
adopts an observed safe result, proves not-applied and replans, or remains
blocked. Never delete an unexpected upload merely because it resembles a
failed attempt; Samsung inventory does not currently expose a trustworthy
Artwork Digest for arbitrary TV content.

## Command recovery matrix

| Command | Durable intent | Applied proof | Recovery rule |
| --- | --- | --- | --- |
| Upload | Digest, committed path/name, size/type/matte, Collection generation, inventory fingerprint | Matched positive receipt with nonblank new content ID, followed by inventory containing that ID | If receipt was lost, refresh inventory. Adopt only when the adapter can correlate a unique command/transfer result to that ID. Without such proof, remain recovery-required and require operator resolution; never upload again blindly. |
| Delete Owned Artwork | Exact content ID and bound Artwork Digest tombstone | Positive receipt and refreshed inventory omitting the ID | Missing ID means applied and the binding/tombstone can be cleared. Present ID means not applied and may be replanned. Unknown inventory remains blocked. |
| Delete Unknown Artwork | Exact content ID, explicit policy fingerprint, inventory generation | Positive receipt and refreshed inventory omitting the ID | Same presence/absence rule, but any policy change cancels further deletion. Never infer ownership or delete a replacement ID. |
| Select | Exact content ID and inventory generation | Positive receipt or authoritative selected-content observation | If selection cannot be observed and acknowledgement was lost, report unknown; do not select another image. Selection is deterministic, never Go map iteration. |
| Slideshow | Fully known previous value and exact desired value | Positive receipt followed by matching read | Fresh matching read adopts applied; previous value proves not applied. Any third value is external change/unknown and blocks overwrite until a new plan. |
| Brightness | Fully known previous value, desired value, and read/write capabilities | Positive receipt followed by matching read | Same three-way rule as slideshow. Until a real read capability exists, automatic brightness writes remain disabled. |
| Power-off/Wake | Explicit prior power/Art Mode facts and policy | Positive receipt plus authoritative power observation where available | Unknown outcome ends the cycle. Never repeat a remote key or Wake packet merely because acknowledgement is absent. |

Batch deletion must be removed from the Reconciliation seam. Issue one content
ID per durable intent so partial application has one recoverable postcondition.
The adapter may optimize wire framing only if it can return an independently
classified result for every ID; otherwise batching is unsafe. The current
single `delete_image_list` call cannot establish which members applied after a
lost response ([client_art.go](../../internal/samsung/client_art.go#L76-L96)).

## Ordering and convergence

Use deterministic ordering by digest then content ID. The default cycle order
is:

1. recover any pending operation;
2. remove locally stale bindings only after known inventory proves the content
   ID absent;
3. delete obsolete Owned Artwork with durable tombstones;
4. upload missing desired digests;
5. optionally delete Unknown Artwork under the exact removal policy;
6. select a deterministic desired content ID when required;
7. update slideshow, then brightness;
8. power off last; and
9. durably record a completed cycle and capacity evidence.

Deleting obsolete Owned Artwork before upload safely frees planned space, but
must not remove the last-known-good displayed art unless a durable successor is
already bound or policy explicitly permits an empty managed collection. A
transient provider failure never yields a new Collection Snapshot, so it cannot
create deletion intents.

Each successful command invalidates its affected authorization facts. The next
command obtains a new observation/authorization from the Samsung adapter. A
Collection generation or policy change discards the remaining in-memory plan
and starts a new cycle after pending recovery; already applied operations are
not rolled back.

## Capacity and auxiliary state

Capacity is advisory evidence, not mutation authority. Derive its bound from a
fresh known inventory and an explicit storage-full `NotApplied` upload result.
Persist it inside the same per-TV state revision so mapping and capacity cannot
diverge. A storage-full cycle is degraded-but-recoverable, performs no later
uploads, and must not increment a success streak. Successful complete cycles
may cautiously probe growth; dry runs never update evidence.

Any authoritative persistence failure is terminal for that TV's current cycle
and reaches health. Metadata snapshots are optional telemetry only if their
failure cannot affect planning or authority; otherwise they belong in the same
error path. The current implementation logs and ignores capacity load/save and
success-streak failures, including a dry-run path that may write capacity state
([session.go](../../internal/sync/session.go#L187-L218),
[session.go](../../internal/sync/session.go#L139-L153)).

## Dry run, cancellation, and health

Dry run executes recovery inspection, read-only observation, and pure planning
without creating or replacing the state file. It cannot pair, persist a token
or metadata, update capacity/backoff, write a journal, mutate the collection,
Wake, upload, delete, select, change display settings, or power off. Return an
explicit incomplete projection when state or inventory is unknown.

Cancellation before adapter write is `NotAttempted`. Cancellation after a
request may have been written is `Unknown`; preserve the pending intent and
stop. Supervisor shutdown waits within its bounded deadline for an in-progress
state replacement, but does not claim that cancellation rolled back a TV
command. Startup recovery runs before readiness becomes 200.

Health distinguishes complete, known skip, incomplete dry-run, storage-full,
not-applied failure, recovery-required, and persistence-unknown. A fully
durable successful cycle is healthy. Any pending intent, unknown TV state,
unknown outcome, mapping/state failure, or failed required display command is
degraded and `/health` returns 503. An unreadable or unrecoverable authoritative
state file also makes `/ready` return 503. Logs and status include operation ID,
cycle ID, phase, sanitized error kind, and outcome, never tokens or artwork
bytes.

## Crash-point and regression tests

Use a table-driven state-machine harness with a fake durable store and scripted
Samsung adapter. Restart a new Service instance at every injected point:

1. before and after intent temp creation, write, file sync, rename, and parent
   sync;
2. after `Prepared` commit but before adapter call;
3. before request write, after partial/full request write, after TV application,
   before acknowledgement, and after positive/rejected acknowledgement;
4. before and after `Applied` receipt commit;
5. before and after folding the effect and clearing `Pending`;
6. before every subsequent dependent command; and
7. during supervisor cancellation at every phase.

For upload, cover D2D completion with lost `image_added`, duplicate-looking
inventory, mapping persistence failure, digest/path substitution, and restart.
Assert zero blind retry and zero automatic orphan deletion. For each single-ID
delete, cover present, absent, replaced, malformed inventory, and lost response;
assert incomplete inventory never authorizes cleanup. For selection, slideshow,
brightness, power, and Wake, cover positive, explicit rejection, lost response,
failed verification read, third-party value change, and restart.

Additional required tests:

- filename unchanged with changed digest uploads the new bytes and tombstones
  the old binding; same-digest rename preserves the binding;
- collage and optimization never transfer content IDs across changed digests;
- mapping rename collision cannot discard either association;
- storage-full evidence is based on refreshed inventory, does not count as a
  successful cycle, and persistence failure degrades health;
- provider/network failure retains the last Collection Snapshot and schedules
  no destructive TV work;
- exhaustive dry-run spies and before/after filesystem snapshots prove zero
  local and TV mutation;
- randomized/property tests crash and resume arbitrary command sequences and
  establish: at most one pending operation, no unknown outcome is replayed,
  bindings never claim an unproved digest/content pair, and every completed
  state equals observed postconditions;
- race tests cover concurrent status reads and one serialized cycle per TV;
  aggregate coverage remains at least 90 percent.

## Implementation order

1. Introduce versioned reconciliation state and pure digest-based planner using
   the existing durable mapping tests as migration fixtures. Read legacy
   filename mappings but do not delete them until the new state is durably
   verified.
2. Add the one-operation journal state machine and crash harness with a fake
   adapter; initially route no production TV writes through it.
3. Implement fail-closed Samsung observation/outcome classification and migrate
   upload first. Remove blind `uploadWithRetry` before production journaled
   upload is enabled.
4. Migrate single-ID Owned/Unknown deletion and inventory-driven recovery;
   remove batch deletion from the Reconciliation interface.
5. Migrate deterministic selection, slideshow, brightness, and power commands,
   making every required error health-visible.
6. Fold capacity into reconciliation state, enforce the separate dry-run path,
   and connect recovery/readiness to the application supervisor.
7. Remove legacy `Mapping`, `CapacityManager`, `TVTransport`, rename observers,
   and ignored-error paths only after migration and restart tests pass. Update
   README recovery/status/configuration behavior and run `make agent-fix`.

## Acceptance gates

- Every TV mutation has durable intent before transmission and a classified
  outcome; unknown is never blindly retried.
- Recovery begins with trustworthy observation and converges or stays safely
  blocked; it never guesses or performs generic compensation.
- Ownership binds full Artwork Digest, content ID, TV identity, and Collection
  generation. Filename alone cannot authorize mutation.
- Exactly one per-TV durable state owns bindings, tombstones, pending progress,
  and capacity evidence, using `durablefs` and sensitive permissions.
- All display and authoritative persistence failures reach the Sync Cycle and
  health; no warning-only mutation failures remain.
- Dry run has mechanically proven zero durable local and TV mutation.
- Startup resolves or reports pending work before readiness; unknown state or
  outcome cannot authorize destructive or display-changing work.
- Crash tests cover every local/TV boundary for every command; repository
  coverage remains at least 90 percent and `make agent-fix` exits zero without
  warnings.
