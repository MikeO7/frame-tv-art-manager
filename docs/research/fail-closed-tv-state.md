# Fail-closed TV State semantics

Research date: 2026-07-12

## Decision

Replace the current collection of connection, state, inventory, display, and
backoff methods with one deep Samsung adapter. A Sync Cycle first asks the
adapter for an immutable `Observation`. The adapter returns an opaque
`Authorization` only when power, Art Mode, TV Inventory, device identity, and
the capabilities needed by the proposed Reconciliation are all known. Every TV
mutation must pass that authorization back to the adapter; the adapter
serializes a fresh preflight observation and the mutation on the same live
connection. Unknown or stale TV State can therefore never authorize upload,
delete, selection, slideshow, brightness, wake, or power-off work.

This is an application safety contract, not a claim about undocumented Samsung
firmware. Samsung's art protocol is private and the repository is the primary
source for observed behavior. The adapter must normalize only responses it has
actually received. It must not infer support or state from model year, model
name, firmware strings, an open socket, a timeout, or an absent response.

The change is urgent. `IsInArtMode` currently converts any request error into
`true`, explicitly treating an unknown state as safe
([client_art.go](../../internal/samsung/client_art.go#L15-L39)). `Connect`
continues when device information cannot be fetched, leaving power and device
identity unknown ([client.go](../../internal/samsung/client.go#L125-L165)). The
reconciler then turns the Boolean into authority for inventory and mutation
([session.go](../../internal/sync/session.go#L239-L262)). This directly violates
the repository invariant that unknown TV State cannot authorize destructive or
display-changing work.

The existing REST gate is not an independent safety proof. Its helper erases
request-construction and network errors into the same `false` result
([client.go](../../internal/samsung/client.go#L277-L293)), and the gate can be
disabled ([client.go](../../internal/samsung/client.go#L81-L95)). Treat it only
as a quiet-connect hint: `200` may permit opening the art WebSocket, but the
authoritative Art Mode query must still succeed before Reconciliation.

## Recommended interface

Keep protocol details and guard sequencing inside `internal/samsung`. The
Reconciliation layer should see this small surface rather than eleven shallow
methods:

```go
package samsung

type Adapter interface {
	Observe(ctx context.Context, req ObserveRequest) (Observation, error)
	Apply(ctx context.Context, auth Authorization, command Command) (Receipt, error)
	Close(ctx context.Context) error
	Runtime() RuntimeState
}

type ObserveRequest struct {
	CycleID              string
	CollectionGeneration string
	Required             CapabilitySet
	DryRun               bool
}

type Observation struct {
	TV            TVIdentity
	Connection    ConnectionState
	Power         PowerState
	ArtMode       ArtModeState
	Inventory     Inventory
	Slideshow     Fact[Slideshow]
	Brightness    Fact[int]
	Capabilities  Capabilities
	ObservedAt    time.Time
	Disposition   Disposition
	Authorization Authorization
}

type Command interface {
	isSamsungCommand()
}

type Upload struct {
	Generation string
	Name       string
	Path       string
	Digest     [sha256.Size]byte
	FileType   artwork.FileType
	Matte      string
}

type Delete struct {
	ContentIDs []string
	Reason     DeleteReason
}

type Select struct{ ContentID string }
type ConfigureSlideshow struct{ Slideshow Slideshow }
type ConfigureBrightness struct{ Value int }
type PowerOff struct{}
type Wake struct{}

type Receipt struct {
	CommandID   string
	Outcome     Outcome
	ContentID   string
	CompletedAt time.Time
	Observation ObservationSummary
}
```

`Authorization` is an opaque value: all of its fields and construction remain
private to `samsung`. Internally it binds an adapter instance, connection
generation, Cycle ID, TV identity, Inventory fingerprint, Collection Snapshot
generation, policy fingerprint, and dry-run bit. A zero, copied-from-another-TV,
expired, dry-run, or forged value is rejected with `ErrNotAuthorized` before a
protocol write. `Command` is sealed so callers cannot smuggle arbitrary wire
requests through the safety boundary.

`Observe` owns backoff admission, optional quiet gate probing, token loading,
connection establishment, authenticated handshake, device observation, Art
Mode observation, TV Inventory, capability probes, and optional slideshow and
brightness reads. It returns immutable value data with sorted, duplicate-free
content IDs and no aliases to adapter state. A known off/not-Art-Mode TV is a
successful blocked observation, not an error. A failed or malformed state query
returns the partial observation plus a typed error and no authorization.

`Apply` owns every mutation precondition and protocol exchange. Callers cannot
invoke raw upload/delete/display methods. The adapter accepts one operation at
a time per TV, refreshes its operation-specific facts, validates the opaque
authorization, sends the request, waits for a matching positive response, and
returns a typed receipt. This keeps the check and use adjacent and prevents two
local goroutines from invalidating one another's facts.

`Runtime` is a read-only snapshot for structured logging, status JSON, and
health reporting. It must never be accepted by `Apply`; reported state is not
authorization. Backoff bookkeeping becomes private to the adapter. Remove
`ShouldSkip`, `RecordFailure`, and `RecordSuccess` from Reconciliation so policy
cannot be inconsistently applied by callers.

## Normalized facts

All safety facts are explicit sum types; zero values mean unknown, never false:

```go
type PowerState uint8       // Unknown, On, Off
type ArtModeState uint8     // Unknown, On, Off
type Support uint8          // Unknown, Supported, Unsupported
type ConnectionState uint8  // Disconnected, Connecting, Ready, BackingOff, AuthRequired
type Disposition uint8      // Eligible, BlockedPowerOff, BlockedNotArtMode, BlockedBackoff, UnsafeUnknown

type Fact[T any] struct {
	Value      T
	Known      bool
	ObservedAt time.Time
}
```

An unrecognized power or Art Mode string is `Unknown` plus `KindProtocol`; it
is never coerced to off, on, or false. A successful WebSocket handshake proves
only an authenticated live connection. A successful TV Inventory response
proves only the parsed inventory at that instant. `FrameTVSupport == "true"`
is supporting evidence, not a substitute for successful Art Mode and inventory
requests. Capability support is established by a successful documented-in-code
probe or operation response; an error or absence is `Unknown` unless the TV
returns a protocol-level unsupported result that the adapter explicitly
recognizes. No model/firmware allowlist is permitted without captured first-party
protocol evidence and regression fixtures.

`Inventory` contains the user-managed category, sorted unique content IDs, an
SHA-256 fingerprint of the canonical serialized IDs, and its observation time.
An empty successful list is known-empty. A missing field, empty payload that is
not the protocol's explicit empty-list representation, malformed JSON,
duplicate ID, blank ID, wrong category, or truncated response is unknown and an
error. The current implementation treats an absent content list as an empty
inventory ([client_art.go](../../internal/samsung/client_art.go#L42-L74)); the
adapter must distinguish those wire cases before an empty inventory can
authorize uploads or mapping cleanup.

Capabilities are per behavior, not one `IsFrameTV` Boolean:

- art-state observation;
- user-art inventory;
- image upload;
- image deletion;
- image selection;
- slideshow read/write;
- brightness read/write; and
- remote power control/Wake-on-LAN.

Read and write capabilities are separate. For example, a failed slideshow read
cannot authorize preserving or overwriting slideshow state. Brightness writes
remain disabled until the adapter has a successful corresponding read or a
real capability response; the current client exposes only `set_brightness`
([client_art_slideshow.go](../../internal/samsung/client_art_slideshow.go#L105-L118)),
so support must not be invented.

## State and error matrix

`Apply` requires `Connection=Ready`, `Power=On`, `ArtMode=On`, a complete known
TV Inventory, and the command's capabilities. The following matrix is
normative:

| Observation | Result | Mutation authority | Health/backoff |
| --- | --- | --- | --- |
| Backoff deadline is in the future | Return `BlockedBackoff` and `KindBackoff`; do no network I/O | None | Degraded with next-attempt time; keep current backoff |
| Context canceled/deadline exceeded | Return wrapped context error | None | Shutdown cancellation does not degrade or back off; cycle timeout degrades but does not retry inside adapter |
| TV unreachable, TLS/WebSocket handshake fails, or response times out | Power/Art Mode/inventory unknown | None | Degraded; transient exponential backoff |
| Authorization rejected or token cannot be durably saved | `AuthRequired` or persistence error | None | Degraded; no timed retry storm; retry after token/config change or operator-triggered cycle |
| Quiet REST gate returns non-200 | Safe blocked hint; do not open art endpoint in this cycle | None | Healthy skip if an explicit busy response was received; transport failure is unknown/degraded |
| Device response explicitly says power off | `BlockedPowerOff` | None, including Wake unless a separate fresh known-off wake policy explicitly requests it | Healthy skip; no failure backoff |
| Device power missing, malformed, stale, or fetch failed | `Power=Unknown` | None | Degraded; typed observation error/backoff |
| Art query explicitly returns off | `BlockedNotArtMode` | None | Healthy skip; no failure backoff |
| Art query fails, times out, is malformed, or returns an unknown value | `ArtMode=Unknown` | None | Degraded; typed error/backoff |
| Art Mode on but inventory fails or is ambiguous | `Inventory.Known=false` | None | Degraded; typed error/backoff |
| Identity or required capability unknown | `UnsafeUnknown` | None | Degraded; no mutation; backoff only if caused by transient protocol/transport failure |
| All required facts known, no durable-state error, non-dry-run | `Eligible` with opaque authorization | Command-specific only | Healthy observation; failure count resets |
| All facts known, dry-run | `Eligible` for planning but authorization is permanently non-mutating | None | Healthy projected cycle |
| Mutation gets explicit success response | `OutcomeApplied` | Update in-memory observation from receipt; continue only if durable Reconciliation state commits | Healthy unless persistence fails |
| Mutation gets explicit rejected/error response | `OutcomeNotApplied` with typed protocol error | Authorization invalidated for that command; only safe retry policy may retry | Storage-full/unsupported do not trigger connectivity backoff; transient errors do |
| Connection fails after request may have been written or D2D transfer began, before matched success/rejection | `OutcomeUnknown`, matching `ErrOutcomeUnknown` | Invalidate all authorization; stop cycle | Degraded/backoff; re-observe and recover, never blind retry |

Known power-off and Art-Mode-off observations are not failures because skipping
is the correct safe result. By contrast, unknown is never reported as the same
status as a known safe skip; operators must be able to tell absence of authority
from a policy decision.

## Exact freshness rules

There is no time-to-live that authorizes mutation. Timestamps are telemetry,
not safety. The rules are:

1. An initial `Observation` is valid for pure planning only during its Cycle ID
   and only against the exact Collection Snapshot generation bound into its
   authorization.
2. The authorization expires on context cancellation, `Close`, connection
   reopen, connection-generation change, adapter backoff, any transport or
   protocol error, any `ErrOutcomeUnknown`, or completion of the Sync Cycle.
3. `Apply` holds the per-TV operation mutex and performs a new power and Art
   Mode observation immediately before **every** command. No unrelated local
   protocol call may interleave between this guard and the write.
4. Upload additionally reopens the committed artwork path without following a
   symlink, verifies regular-file metadata, full Artwork Digest, file type, and
   Collection Snapshot generation immediately before requesting D2D transfer.
   A path/name alone is never authority. This consumes the trustworthy
   `Snapshot.Item` contract selected for the Artwork Collection
   ([artwork-collection-transaction.md](artwork-collection-transaction.md#recommended-interface)).
5. Delete immediately refreshes TV Inventory under the same mutex and requires
   every target ID still to exist. Owned Artwork deletion additionally requires
   the Reconciliation intent to bind each content ID to its Artwork Digest;
   Unknown Artwork deletion requires the explicit removal policy and the same
   fresh inventory generation.
6. Select immediately refreshes TV Inventory and requires the target ID to be
   present. Slideshow and brightness writes immediately read their current
   values and require the matching read/write capability. No failed read is
   interpreted as “needs update.” The current code does exactly that for
   slideshow by ignoring the read error and treating `nil` as a reason to write
   ([execution.go](../../internal/sync/execution.go#L255-L276)); this must be
   removed.
7. Power-off requires a fresh explicit `Power=On` and `ArtMode=On`, then a
   successful remote-control handshake under the operation lock. Wake requires
   a fresh explicit `Power=Off`, an enabled MAC policy, and its own command.
   Observation and dry-run never send Wake-on-LAN. The current `Connect`
   unconditionally wakes first when configured
   ([client.go](../../internal/samsung/client.go#L114-L129)); connection setup
   must become side-effect-free.
8. A successful mutation invalidates the specific facts it can change. The
   adapter either updates them from a positive response or refreshes them before
   another dependent command. Upload/delete/select invalidate inventory and/or
   displayed selection; slideshow, brightness, and power commands invalidate
   their matching facts.

These rules deliberately favor an extra small state request over a stale
authorization. The resource envelope limits expensive image transforms, not
safety probes; TV operations remain serialized and bounded, consistent with
the process-wide admission decision
([resource-efficiency-envelope.md](resource-efficiency-envelope.md#decision)).

## Protocol errors and outcomes

Use one structured error type with `errors.Is` sentinels for policy:

```go
type Error struct {
	Kind       ErrorKind
	Operation  string
	RequestID  string
	Code       int
	Retryable  bool
	Outcome    Outcome
	Cause      error
}

type ErrorKind uint8 // Canceled, Backoff, Unreachable, Timeout, Unauthorized,
                     // Protocol, Unsupported, InvalidResponse, StorageFull,
                     // NotAuthorized, Persistence, OutcomeUnknown

type Outcome uint8 // NotAttempted, NotApplied, Applied, Unknown
```

Preserve `context.Canceled`/`DeadlineExceeded` and existing stable sentinels
with `%w`. Never classify unrelated Samsung codes as storage-full by folklore;
retain the raw numeric code for diagnosis and classify only codes backed by
captured protocol fixtures. The current client maps `403`, `507`, and `11001`
to storage-full in one shared art-error function
([client.go](../../internal/samsung/client.go#L179-L187)); implementation must
verify the operation context before retaining that mapping.

A request that definitely failed before any bytes were written is
`NotAttempted`. A matched explicit rejection is `NotApplied`. Only a matched,
validated positive response is `Applied`. Once a mutation request may have
reached the TV, timeout, disconnect, cancellation, malformed response, missing
upload confirmation, D2D ambiguity, or a power press without confirmed release
is `Unknown`. Unknown outcomes stop the TV's cycle and prohibit automatic
retry. The current upload can complete D2D then time out waiting for
`image_added` ([client_art.go](../../internal/samsung/client_art.go#L167-L229));
retrying that as an ordinary upload can create duplicates.

Positive TV acknowledgement is not the end of Reconciliation. The resulting
content ID or deletion must be durably committed before dependent TV mutations.
If durable mapping/journal persistence fails, surface the persistence error to
cycle health and stop. Use `durablefs` for authoritative state; an ambiguous
filesystem commit is also `ErrOutcomeUnknown` and requires recovery
([durable-file-semantics.md](durable-file-semantics.md#durable-replacement)).

## Backoff policy

The adapter owns a clock-injected, concurrency-safe retry state. For transient
unreachable, timeout, connection, and invalid-response failures, increment one
consecutive-failure count and choose full-jitter delay in
`[0, min(maxDelay, baseDelay * 2^(failures-1))]`. Keep the existing one-hour
ceiling unless configuration explicitly changes it. Report the exact
`next_attempt_at`; `Observe` returns `KindBackoff` without I/O before that time.

Reset the failure count only after a complete mutation-eligible observation,
or a complete known off/not-Art-Mode observation. Opening a socket alone is not
recovery. Do not add timed backoff for:

- known power-off or Art-Mode-off policy skips;
- storage full;
- unsupported capability;
- invalid local command or stale authorization;
- supervisor shutdown cancellation; or
- local persistence failure.

Unauthorized/token failures enter `AuthRequired` and await a token/config
change or explicit operator-triggered observation rather than repeatedly
prompting the TV. `ErrOutcomeUnknown` uses transient backoff but also sets a
`recovery_required` flag; expiry permits observation/recovery, never direct
mutation. Backoff is in-memory operational state and need not become durable
authority. Persisting it must never be required to fail closed.

## Dry-run behavior

Dry run may load committed local state and perform read-only network
observations, but it performs no durable local mutation and no TV mutation.
Specifically it must not:

- send Wake-on-LAN or a remote-control key;
- create/pair/replace a token;
- write metadata, mapping, capacity, backoff, journal, or Collection files;
- upload, delete, select, set slideshow, or set brightness; or
- call `Apply` successfully, even if a caller accidentally constructs a plan.

If an existing token permits a read-only connection, dry run can produce an
exact projection from a complete Collection Snapshot and known TV Inventory.
Without an existing token, complete TV State, or complete inventory, return an
incomplete projection with explicit unknown facts; do not assume an empty TV or
“would mutate.” The adapter's dry-run authorization bit is permanent, and
`Apply` rejects it before I/O as defense in depth. This strengthens the current
executor, which skips its planned writes but still reaches `Connect`, whose
first action may be Wake-on-LAN
([execution.go](../../internal/sync/execution.go#L41-L68),
[client.go](../../internal/samsung/client.go#L125-L129)).

## Health and lifecycle reporting

Publish one per-TV `RuntimeState` after every observation or command with:

- TV identifier and model when known;
- connection, power, Art Mode, inventory-known, capabilities, and disposition;
- observation and last-success timestamps;
- consecutive failures, next attempt, auth/recovery-required flags;
- last operation, error kind (sanitized), outcome, and request ID; and
- Sync Cycle ID plus Collection Snapshot generation.

Never include tokens, artwork bytes, D2D keys, or raw payloads. Structured logs
use the same fields and an error value rather than interpolated protocol data.

A complete eligible observation and fully durable Reconciliation is healthy. A
known off/not-Art-Mode policy skip is also healthy and clearly labeled skipped.
Unknown TV State, unknown inventory, auth required, recovery required, backoff
caused by failure, mutation failure, or persistence failure makes the latest
cycle degraded and `/health` returns 503 with the sanitized cause. This does
not make the process unready: the lifecycle decision says a transient Sync
Cycle failure degrades `/health` while `/ready` remains 200. Unresolved local
Collection recovery or authoritative persistence state does make readiness 503
([supervised-application-lifecycle.md](supervised-application-lifecycle.md#lifecycle-and-health-states)).
Liveness changes only for supervisor failure/stopping, never because a TV is
off or unreachable.

Every currently ignored display/metadata failure must reach the cycle result.
Selection, slideshow, brightness, and power errors are presently only warnings
([execution.go](../../internal/sync/execution.go#L206-L307)), while metadata
failure is debug-only ([session.go](../../internal/sync/session.go#L253-L255)).
Optional telemetry may fail without blocking TV work only if it is purely
ephemeral. Failure to durably persist authoritative mapping, journal, token, or
capacity state always stops the cycle and degrades health.

## Regression tests

Use table-driven adapter tests with fake clock/randomness and an `httptest`
REST server plus scripted WebSocket/D2D peers. Store captured, sanitized wire
fixtures for every supported response shape; do not synthesize firmware claims.
The minimum suite is:

1. Every power and Art Mode response value, including absent, blank, malformed,
   unknown, timeout, cancellation, and disconnect, maps to the exact state and
   error in the matrix. No error path returns authorization.
2. REST gate non-200, timeout, connection refusal, and disabled configurations
   remain distinguishable; no gate result alone grants Reconciliation.
3. Device-info failure after WebSocket success leaves power/identity unknown
   and blocks all commands.
4. Inventory explicit empty, absent field, malformed JSON, duplicate/blank ID,
   wrong category, oversized response, and disconnect are distinguished. Only
   explicit valid empty is known-empty.
5. Capability probes test supported, explicit unsupported, and unknown. No
   model/firmware string changes the result.
6. A forged, zero, wrong-TV, wrong-cycle, wrong-Collection-generation,
   dry-run, canceled, closed, stale-connection, and post-error authorization is
   rejected before any wire write.
7. For each command, force power/Art Mode preflight to off and to every unknown
   error; assert zero upload/delete/select/slideshow/brightness/power packets.
8. Change state between initial planning and `Apply`; the immediate preflight
   blocks the command. Concurrent calls prove the guard and write do not
   interleave.
9. Upload rejects a changed path, symlink, non-regular file, changed digest,
   changed generation, unsupported type, and canceled hash. It opens at most
   one transform/upload slot and sends nothing on rejection.
10. Delete and select refresh inventory and reject disappeared or substituted
    content IDs. Unknown Artwork deletion additionally requires its explicit
    policy binding.
11. Slideshow/brightness read failures never trigger writes. Power-off requires
    known on+Art Mode and classifies press/release ambiguity as unknown. Observe
    and dry run never Wake.
12. Every protocol phase is fault-injected before write, after partial/full
    write, after D2D transfer, before response, on wrong request ID, malformed
    response, explicit rejection, and success. Assert `Outcome` and retry rules.
13. An upload confirmation timeout and a delete response timeout produce
    `ErrOutcomeUnknown`, stop the cycle, preserve mapping, and require fresh
    inventory recovery before any retry.
14. Backoff exact bounds, cap, jitter injection, concurrent observations,
    reset rules, auth suspension, storage-full exclusion, and context behavior
    are deterministic under a fake clock.
15. Dry-run with/without token and with complete/incomplete observations makes
    zero durable writes and zero TV mutations while reporting exact projection
    completeness.
16. Positive TV mutation followed by each durablefs failure reaches cycle
    health, prevents dependent commands, and remains recoverable after restart.
17. Runtime snapshots and `slog` output contain all health fields but no token,
    payload, image bytes, or D2D secret. Race tests cover Observe/Apply/Close and
    status reads.
18. End-to-end Sync Cycle tests combine a committed Collection Snapshot,
    Artwork Digests, known/unknown TV facts, mapping recovery, shutdown
    cancellation, and the full mutation sequence. Aggregate repository coverage
    remains at least 90 percent and `make agent-fix` is clean.

## Acceptance gates

Implementation is complete only when all of these are true:

- No Boolean or zero value conflates unknown TV State with on/off, and the old
  `IsInArtMode(ctx) bool` seam no longer exists.
- Reconciliation cannot call raw Samsung mutation methods; the only path is
  `Adapter.Apply` with a valid opaque authorization.
- Connection setup and `Observe` are side-effect-free with respect to TV power;
  Wake and power-off are explicit guarded commands.
- Every mutation performs the exact immediate preflight above and unknown/stale
  state produces zero protocol mutation bytes.
- Planning consumes only a complete Collection Snapshot and known TV Inventory;
  uploads bind full Artwork Digests and generation, not filenames.
- Unknown mutation and durable outcomes stop the cycle, are never blindly
  retried, preserve last-known-good mappings/art, and require observation-driven
  recovery.
- Dry run is proven by spies/fault injection to perform no durable local or TV
  mutation, including pairing, metadata, and Wake-on-LAN.
- Backoff and error classification live solely in the adapter and are fully
  deterministic in tests.
- All authoritative persistence uses the selected `durablefs` semantics and
  every failure reaches lifecycle health.
- `/live`, `/ready`, `/health`, `/status`, reports, and structured logs represent
  known skip, unknown/degraded, backoff, auth-required, recovery-required, and
  applied/not-applied/unknown outcomes distinctly.
- The README documents operator-visible status, dry-run, backoff, and recovery
  behavior; regression tests cover the matrix; `make agent-fix` exits zero with
  no warnings and aggregate coverage remains at least 90 percent.

## Implementation order

1. Add normalized facts/errors and protocol fixture tests without changing
   Reconciliation behavior.
2. Implement side-effect-free `Observe`, adapter-owned backoff, runtime health,
   and opaque authorization; remove Boolean state authority.
3. Route upload/delete/select/slideshow/brightness/power through guarded
   `Apply`, first preserving behavior and then enabling command-specific
   freshness checks.
4. Bind upload to Collection Snapshot generation/Artwork Digest and bind
   delete/select to fresh TV Inventory plus durable mapping intent.
5. Add unknown-outcome recovery hooks required by recoverable Reconciliation
   before enabling automatic retries.
6. Remove the legacy role interfaces and ignored-error paths, update operator
   documentation, and run the complete verification gate.
