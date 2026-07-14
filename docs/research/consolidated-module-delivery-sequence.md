# Consolidated module and delivery sequence

Research date: 2026-07-12

## Decision

Replace the current configuration-wide dependency graph and mutation-sharing
packages with six deep runtime modules and one process composition root:

1. `internal/durablefs` owns crash-consistent local namespace operations.
2. `internal/resources` owns bounded transform admission and resource metrics.
3. `internal/collection` is the only writer of the Artwork Collection and the
   only producer of a trustworthy Collection Snapshot.
4. `internal/samsung` owns Samsung protocol state, authorization, outcomes,
   backoff, token persistence, and transport lifetime for one TV.
5. `internal/reconcile` owns recovery and convergence for one TV, including its
   single authoritative durable state.
6. `internal/health` owns immutable lifecycle/cycle/status projections and the
   HTTP surface; uploads enter the Collection through `Import`.
7. `internal/app` composes those modules, supervises their lifetimes, schedules
   Sync Cycles, bounds per-TV fan-out, and performs shutdown.

`cmd/frame-tv-art-manager` remains the only process adapter. It parses CLI
commands, loads environment configuration, installs logging/signals, builds
package-owned options, calls `Application.Run`, and translates its result into
the sole `os.Exit` call.

This consolidates the six preceding decisions rather than introducing another
architecture:

- durable operations and outcome-unknown semantics come from
  [durable-file-semantics.md](durable-file-semantics.md);
- collection ownership and digest identity come from
  [artwork-collection-transaction.md](artwork-collection-transaction.md);
- bounded transforms and measurement gates come from
  [resource-efficiency-envelope.md](resource-efficiency-envelope.md);
- TV observations and guarded commands come from
  [fail-closed-tv-state.md](fail-closed-tv-state.md);
- per-TV intent and recovery come from
  [recoverable-reconciliation-mutations.md](recoverable-reconciliation-mutations.md);
- lifetime, readiness, and bounded shutdown come from
  [supervised-application-lifecycle.md](supervised-application-lifecycle.md).

The current graph passes a `*config.Config` into `sync`, `sources`, `samsung`,
and `health`, while `config` itself imports `optimize`
([config_options.go](../../internal/config/config_options.go),
[engine.go](../../internal/sync/engine.go),
[loader.go](../../internal/sources/loader.go),
[client.go](../../internal/samsung/client.go),
[server.go](../../internal/health/server.go)). This makes configuration a
service locator and lets packages consume settings outside their ownership.
The target graph passes immutable, package-owned value options through
constructors. No runtime package imports `internal/config`.

## Target dependency graph and ownership

```text
cmd/frame-tv-art-manager
  -> config              environment schema and validation only
  -> app                 composition and lifetime

app
  -> collection          one committed Collection Snapshot per cycle
  -> reconcile           one serialized Service per configured TV
  -> health              lifecycle/cycle/status publication and HTTP Serve
  -> resources           shared admission/metrics owner

collection
  -> durablefs           manifest, journal, artwork publication/removal
  -> resources           transformation admission
  -> optimize            pure decode/transform/encode adapter
  -> source adapters     read-only provider resolution and bounded download

reconcile
  -> durablefs           one per-TV state revision
  -> collection          immutable Snapshot values
  -> samsung             Observe/Apply/Close/Runtime only

samsung
  -> durablefs           authentication token persistence

health
  -> collection          Import only; no filesystem writer

durablefs, resources, optimize
  -> Go standard library, except optimize may retain golang.org/x/image
```

The dependency direction is acyclic. In particular, `config` does not import a
runtime module; `collection` never reads or writes per-TV state; `samsung`
never decides desired collection policy; `reconcile` never calls raw protocol
methods; and `health` never writes artwork. The current `internal/sync` package
is dissolved after cutover. Its scheduling moves to `app`, collection work to
`collection`, and per-TV state machine to `reconcile`. The current
`internal/sources` code becomes collection-private provider adapters. The
current `brightness` calculation becomes pure Reconciliation policy code.
Filename parsing and naming in `internal/artwork` becomes collection-private;
cross-module values are explicit `collection.Digest`, Snapshot, and command
values rather than a general helper package.

`internal/optimize` remains only if it becomes a pure transformation module. It
must not rename, remove, catalog, map, or publish files. Its filesystem-mutating
API (`OptimizeFile`, `OptimizeCatalog`, rename observer) is removed after the
Collection Store owns publication
([resize.go](../../internal/optimize/resize.go),
[pipeline.go](../../internal/optimize/pipeline.go)).

## Exact construction configuration

These are constructor inputs, not a second operator configuration format. The
environment loader returns one validated `config.Settings`; the composition
root explicitly converts it into these package-owned values. Every slice and
map is defensively copied during construction.

### Process and application

```go
package app

type Config struct {
	SyncInterval   time.Duration
	ShutdownTimeout time.Duration
	MaxTVWorkers   int
}

type Dependencies struct {
	Collection collection.Store
	TVs        []TV
	Health     *health.Server
	Status     *health.Status
	Resources  *resources.Controller
	Logger     *slog.Logger
}

type TV struct {
	Address    string
	Adapter    samsung.Adapter
	Reconciler reconcile.Service
}
```

`SyncInterval` must be positive, `ShutdownTimeout` defaults to 30 seconds, and
`MaxTVWorkers` defaults to `min(len(TVs), 4)` with a minimum of one. App owns no
Samsung or collection policy fields.

### Filesystem durability

```go
package durablefs

type Policy struct {
	SyncDirectories bool
}

func Replace(ctx context.Context, path string, mode fs.FileMode,
	write func(io.Writer) error) error
func Rename(ctx context.Context, oldPath, newPath string) error
func Remove(ctx context.Context, path string) error
```

The production policy always synchronizes directories. A policy injection is
allowed only behind a constructor used by fault tests; callers do not select
weaker production durability. This preserves the exact success and
`ErrOutcomeUnknown` contract already selected.

### Resource admission

```go
package resources

type Config struct {
	TransformConcurrency int
	TransformQueue        int
	PixelWorkers          int
}
```

Production defaults are one active transform, two queued transforms, and
`min(GOMAXPROCS, 4)` pixel workers. Values must be positive except a queue may
be zero. The Controller owns waiting, cancellation, overload classification,
and runtime metrics. It never owns transformation or collection policy.

### Artwork Collection

```go
package collection

type Config struct {
	Root           string
	MaxItems       int
	MaxImportBytes int64
	Transform      TransformPolicy
}

type TransformPolicy struct {
	Enabled         bool
	Width           int
	Height          int
	JPEGQuality     int
	Portrait        PortraitMode
	SmartCrop       bool
	Museum          bool
	MuseumIntensity int
}

type SourceConfig struct {
	ManifestPath string
	Resolvers    []Resolver
}

type Dependencies struct {
	Admission   *resources.Controller
	Transformer Transformer
	Sources     SourceConfig
	Logger      *slog.Logger
}
```

`Root` is absolute after load-time normalization; `MaxItems == 0` means no
configured cap; byte, dimension, pixel, quality, and intensity ranges are
validated before construction. Provider credentials are captured inside the
specific resolver at construction and are never retained in Snapshot, status,
or logs. `mattes.json` is loaded by Collection as read-only operator control.
`Store.Prepare` and `Store.Import` remain the only mutation entry points from
the prior Collection decision.

### Samsung adapter

```go
package samsung

type Config struct {
	Address           string
	MAC               net.HardwareAddr
	ClientName        string
	TokenPath         string
	VerifyTLS         bool
	QuietGate         bool
	ConnectTimeout    time.Duration
	RequestTimeout    time.Duration
	GateTimeout       time.Duration
	BackoffBase       time.Duration
	BackoffMaximum    time.Duration
}

type Dependencies struct {
	Clock  Clock
	Random io.Reader
	Logger *slog.Logger
}
```

Address and token path are validated and normalized at startup. Timeouts are
positive. The maximum backoff remains one hour. A configured MAC enables only
an explicit guarded `Wake` command; construction and `Observe` never wake. TLS
verification retains current environment semantics during compatibility
migration. Test clocks/randomness are constructor dependencies, not package
globals.

### Reconciliation

```go
package reconcile

type Config struct {
	StatePath string
	Policy    Policy
}

type Policy struct {
	RemoveUnknown bool
	DefaultMatte  string
	Slideshow     SlideshowPolicy
	Brightness    BrightnessPolicy
	AutoOff       AutoOffPolicy
	UploadDelay   time.Duration
}

type Dependencies struct {
	Adapter samsung.Adapter
	Clock   Clock
	IDs     IDSource
	Logger  *slog.Logger
}
```

Slideshow, brightness, and auto-off are explicit sum types with `Preserve`,
`Disabled`, and `Set` variants rather than pointer/Boolean combinations.
`UploadAttempts` is intentionally absent: a generic retry count cannot be safe
across unknown TV outcomes. The service may retry only a newly authorized,
explicitly `NotAttempted` or retryable `NotApplied` command. `StatePath` is
derived from an observed stable TV identity after provisional IP-based state
has been validated and migrated.

Collection, Samsung, and Reconciliation call the semantic `durablefs`
operations directly; they do not receive a broad filesystem interface. Their
tests inject package-private persistence functions at the owning module's
internal seam and use real temporary directories for cross-module behavior.
This keeps the external `durablefs` interface aligned with the selected three
semantic operations instead of exporting a shallow syscall-shaped adapter.

### Health and HTTP

```go
package health

type Config struct {
	ListenAddress     string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	UploadEnabled     bool
	MaxUploadBytes    int64
}

type Dependencies struct {
	Status   *Status
	Importer collection.Importer
	Logger   *slog.Logger
}
```

The HTTP server receives no root `config.Config` and no artwork directory.
Upload parses a bounded stream and calls `Importer.Import`; it does not decode
and persist through a second writer. Listener creation belongs to `app` so bind
failure is synchronous. Status readers receive immutable snapshots encoded
after locks are released.

## Operator configuration and compatibility

The first delivery preserves every existing environment variable, default,
and precedence rule documented in [`.env.example`](../../.env.example) and
[README.md](../../README.md). `TV_IPS` remains required; `SKIP_TLS_VERIFY=true`
continues to override `VERIFY_TLS`; the presence of any `SLIDESHOW_*` variable
continues to opt into override; default paths and image settings do not change.

Compatibility is implemented at one edge:

```go
package config

func LoadEnviron(environ []string) (Settings, []Warning, error)
```

The loader uses presence-aware parsing so unset, explicitly empty, malformed,
and valid values remain distinguishable. `Settings` contains nested immutable
operator concepts (`Runtime`, `Storage`, `Collection`, `TVs`, `Reconciliation`,
`HTTP`, `Logging`) but no values from runtime packages. It validates all ranges,
paths, addresses, time zones, and cross-field rules before any durable or
network action. Go's `os.LookupEnv` provides the required presence distinction,
while `strconv` parsing returns errors that can be wrapped rather than erased
([official `os.LookupEnv`](https://pkg.go.dev/os#LookupEnv),
[official `strconv`](https://pkg.go.dev/strconv)).

Malformed numeric/Boolean values currently fall back silently
([config_env.go](../../internal/config/config_env.go)); changing that without a
transition could surprise deployments. The compatibility sequence is:

1. Preserve the current fallback for one release, but emit one structured
   startup warning naming the variable and fallback without printing secrets.
2. Document that explicit malformed values will become startup errors in the
   next major release. Add exact-value table tests for every variable now.
3. In the next major release, reject every malformed explicitly set value.
   Missing values still receive documented defaults.
4. Accept legacy names for at least one major release after any rename, warn
   once, and reject simultaneous legacy/new names when values conflict.

No environment name needs to be renamed for this refactor. Add only
`SHUTDOWN_TIMEOUT_SECONDS` (default 30) because bounded shutdown is new
operator behavior. Resource-controller defaults are initially fixed and
measured; expose tuning variables only if operational evidence demonstrates a
need. `UPLOAD_ATTEMPTS` becomes deprecated when journaled upload cuts over:
retain parsing and warn that only proven-not-applied attempts are eligible for
retry, then remove it in the next major release.

`TV_MAC` remains compatible for a single TV. For multiple TVs it is ambiguous;
the loader warns and disables automatic Wake rather than applying one MAC to
every address. A later structured multi-TV operator format is outside this
refactor unless separately specified.

## State migration, recovery, and rollback

Migration is conservative, restartable, and performed before readiness. Every
new file is versioned and written with `durablefs`; legacy files remain
read-only until all configured TVs have completed at least one durable cycle.

### Collection migration

When no collection manifest exists, scan and fully validate the current
artwork directory without renaming or deleting. Adopt supported regular files
as operator-owned entries with full digests. A legacy numeric filename prefix
is not sufficient to mark an item source-managed. A later complete source
resolution may associate an exact digest with an Origin Key; until then it is
not source-prunable. Publish generation one only after every admitted item and
control exclusion is verified. Existing invalid files are reported and left
untouched; no Collection Snapshot authorizes TV work until migration commits.

### Per-TV migration

Read legacy mapping, backup, capacity, token, and current TV Inventory. Convert
an active mapping only when the legacy filename resolves to exactly one
committed Artwork Digest and the mapped content ID is present in a known
inventory for the observed TV identity. Import validated capacity as advisory
evidence. A legacy mapping whose local file is absent or ambiguous is retained
in a quarantined migration section and cannot authorize deletion. Never infer
a digest from a filename suffix. Token bytes keep their path and are durably
replaced only when an authenticated protocol result supplies a token.

Write the complete new reconciliation state, reload it, verify revision and TV
identity, and only then enable the new service for that TV. Do not delete or
rewrite legacy files during this transaction. Metadata is telemetry and may be
regenerated; it is never migration authority.

### Cutover and rollback

Each slice has a single mutation owner. Shadow readers and planners may compare
results, but production never dual-writes two authoritative state formats. A
TV remains on the legacy path until its new state is durably verified; after
its first new journaled TV mutation, forward recovery is the only automatic
safe path. Running an older binary after that point can ignore pending intent
and is prohibited. Release notes require a stopped-process backup of
`TOKEN_DIR` before upgrade and describe restoring that backup only when no new
TV mutation occurred. If a cutover fails, leave both legacy input and new
journal intact, report readiness/health according to the established rules,
and resume the same migration or recovery on restart.

Collection transactions similarly roll forward from their journal. They never
restore old bytes from memory or treat an uncertain namespace operation as
rolled back. Unknown local or TV outcomes retain their intent and block later
mutation until inspection resolves them.

## Dependency-ordered tracer-bullet delivery

Every ticket below ends with `make agent-fix` clean, aggregate coverage at
least 90 percent, and no unbounded compatibility branch. A tracer bullet must
ship one exercised end-to-end path, not only types or placeholders.

| ID | Deliverable | Blocked by | Exit condition |
| --- | --- | --- | --- |
| T01 | Characterization and safety fixtures | none | Current env/defaults, status JSON, mappings/capacity, protocol responses, dry run, collection names, and transform outputs are captured as deterministic tests. |
| T02 | `durablefs` semantic operations | T01 | Replace/rename/remove fault tests cover every boundary, exact modes, directory sync, cancellation, cleanup, and `ErrOutcomeUnknown`; migrate one S1 state write and one S2 upload write. |
| T03 | Configuration edge and explicit composition | T01 | Presence-aware loader passes the complete env matrix; package-owned option conversion exists; no new runtime code accepts root config; warning compatibility is documented. |
| T04 | Resource controller and portable benchmark harness | T01, T03 | One-active/two-queued controller guards an existing transform path; committed fixtures and constrained-runner metrics establish baselines without output changes. |
| T05 | Collection Import tracer | T02, T03, T04 | HTTP upload delegates to `collection.Import`; only Collection writes the artwork directory; duplicate, overload, dry-run, crash, and concurrent Prepare/Import behavior pass. |
| T06 | Transactional Collection Prepare | T05 | Source resolution, validation, optimization, collage, rename/dedupe/prune, manifest, journal, recovery, controls, immutable Snapshot, and digest effects satisfy the Collection crash matrix. Legacy catalog writers are disconnected. |
| T07 | Samsung read-only Observe adapter | T02, T03 | Tri-state facts, explicit empty inventory, capabilities, side-effect-free connect, token durability, adapter backoff/runtime, and authorization rejection pass fixtures; no error returns Art Mode on. |
| T08 | Reconciliation state migration and pure planner | T06, T07 | Legacy fixtures migrate conservatively; digest bindings/tombstones/capacity and one-pending schema validate; dry-run and shadow plan compare without TV writes. |
| T09 | Journaled upload vertical slice | T08 | Prepared intent precedes write; digest/generation preflight, receipts, lost D2D acknowledgement, persistence failure, restart, and no-blind-retry tests pass; legacy upload retry is removed. |
| T10 | Recoverable deletion vertical slice | T09 | Owned and explicit Unknown deletion use single-ID intents and observed postconditions; batch deletion and unsafe mapping cleanup are unreachable. |
| T11 | Guarded display/power convergence | T10 | Deterministic selection, slideshow read/write, supported brightness read/write, Wake, and power-off use immediate preflight; every required failure reaches health. Unsupported brightness remains disabled. |
| T12 | Supervised Application and health semantics | T06, T11 | Synchronous bind, startup recovery, one child per loop, `/live`/`/ready`/`/health`, bounded shutdown, adapter cleanup, second-signal behavior, and sole process exit satisfy lifecycle tests. |
| T13 | Legacy removal and operator cutover | T12 | `internal/sync` and mutation APIs in old catalog/optimizer/health are gone; no runtime imports `config`; README/env/container docs and migration/recovery runbook match behavior; full crash/race/resource/security gates pass. |

T05 and T07 may proceed in parallel after their prerequisites. T06 and T07 are
independent until T08. T12 may be prototyped against legacy children after T03,
but its ticket cannot close until it supervises final Collection,
Reconciliation, and Samsung cleanup. T13 is the only removal ticket; earlier
tickets preserve a working fallback and do not strand half-migrated state.

## Operator-visible documentation changes

Update README, `.env.example`, Compose, Docker healthcheck guidance, and release
notes in the ticket that changes the behavior. The final documentation must
cover:

- dry run as zero durable local and zero TV mutation, including no bootstrap,
  pairing, token, metadata, capacity, journal, Wake, or transform publication;
- Collection control directory, recovery behavior, single-writer requirement,
  and exact file/directory permissions;
- strict versus compatibility configuration parsing and deprecations;
- known skip versus unknown/degraded, auth-required, backoff,
  recovery-required, storage-full, and outcome-unknown states;
- `/live`, `/ready`, `/health`, `/status`, startup grace, and the effect of
  `HEALTH_PORT=0` on the container healthcheck;
- 30-second default shutdown and the new override;
- pre-upgrade state backup, per-TV migration, forward-only cutover boundary,
  and recovery steps;
- upload authentication limitation and bounded/overload response;
- TLS behavior, token secrecy, and per-TV Wake limitations; and
- measurement defaults and optional deployment `GOMEMLIMIT` guidance without
  promising Raspberry Pi support or cross-machine timing.

README claims that dry run still mutates local data and that `TV_MAC` wakes
before connection currently reflect legacy behavior
([README.md](../../README.md#tv-behavior)); those statements change only when
the corresponding implementation ticket lands.

## Regression, crash, security, and resource gates

### Correctness and crash safety

- Run the durablefs, Collection, Samsung, Reconciliation, lifecycle, and
  migration crash matrices at every defined pre/post namespace and protocol
  boundary.
- Property tests establish at most one pending operation, no replay after an
  unknown outcome, no unproved digest/content binding, no source pruning from
  an incomplete provider view, and deterministic convergence from every
  restart point.
- Exhaustive spies plus before/after filesystem snapshots prove dry run invokes
  no durable local or TV mutation.
- Race tests cover status snapshots, Prepare/Import serialization, resource
  admission, adapter Observe/Apply/Close/Runtime, per-TV cycle serialization,
  signals, and shutdown.
- Existing image output dimensions and approved perceptual/checksum fixtures
  remain stable unless a separately documented quality change is accepted.

### Security

- Tokens, manifests, journals, Reconciliation state, and backups are `0600`
  under `0700`; committed artwork is `0644` under the traversable `0755`
  collection root.
- Paths reject traversal, symlinks, non-regular files, reserved control names,
  and digest substitution before publication or upload.
- HTTP/provider/TV reads are context-bound and size-bounded; status and logs
  never expose credentials, token bytes, D2D keys, artwork bytes, or raw secret
  protocol payloads.
- Keep `govulncheck`, pinned Actions, pre-commit secret detection, actionlint,
  and the existing lint policy blocking. New dependencies require a written
  reason; standard library remains preferred.

### Resource and benchmark gates

The hard gates from the resource decision are release criteria: one active
transform/two queued; a 512 MiB reservation with `GOMEMLIMIT=448MiB` completes
the fixture matrix below 480 MiB; an ordinary 4096x2304-to-3840x2160 transform
allocates no more than 256 MiB/op; ten queued files raise peak memory no more
than 10 percent over one; health p99 is below 250 ms on the pinned one-CPU
runner; post-cycle idle memory is below 96 MiB without forced `FreeOSMemory`;
same-runner median benchmark regression is at most 10 percent unless explained;
and stripped amd64/arm64 binaries remain below 12 MiB. Record raw benchmark,
runtime metrics, RSS, Go version, image/runner digest, CPU/memory limits,
architecture, and fixture digests as artifacts. Go's benchmark and runtime
metrics APIs are the primary measurement interfaces
([official benchmark documentation](https://pkg.go.dev/testing#hdr-Benchmarks),
[official runtime metrics](https://pkg.go.dev/runtime/metrics)).

## Final exit criteria

The refactor is complete only when all of the following are simultaneously
true:

1. The target dependency graph is enforced by tests or dependency rules; no
   runtime package imports `internal/config`, and `internal/sync` is removed.
2. All application-owned persistence uses `durablefs`; success includes the
   selected durability contract and ambiguous results are recoverable.
3. Collection is the only artwork writer, produces verified immutable
   digest-based Snapshots, preserves controls, and recovers every transaction.
4. Resource admission covers every decode/transform path and all hard resource
   gates pass on pinned amd64 and arm64 runners.
5. Samsung exposes only Observe/Apply/Close/Runtime to Reconciliation; unknown
   state authorizes no mutation, connect is power-side-effect-free, and all
   command outcomes are classified.
6. Reconciliation has one authoritative per-TV state, one pending operation,
   no blind retry/compensation, deterministic recovery, and health-visible
   persistence/display failures.
7. Dry run is mechanically proven to cause zero durable local and TV mutation.
8. Application startup, binding, loops, readiness, degradation, cleanup,
   signals, exit status, and shutdown are supervised and bounded.
9. Legacy collection, mapping, capacity, token, and environment fixtures
   migrate or fail closed exactly as documented; no legacy writer remains
   reachable after cutover.
10. README, `.env.example`, Compose, Docker, security, status schema, migration,
    recovery, and release notes describe the shipped behavior without stale
    claims.
11. `go test -race ./...` passes, aggregate coverage is at least 90 percent,
    every resource/security/crash gate passes, and `make agent-fix` exits zero
    with no warnings.

Passing the current verification suite alone does not satisfy these criteria:
it establishes that a delivery slice is regression-clean, not that later
architecture tickets have been implemented. Completion requires T01 through
T13 and removal of every legacy mutation path.
