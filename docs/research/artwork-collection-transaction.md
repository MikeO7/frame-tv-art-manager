# Transactional Artwork Collection ownership

Research date: 2026-07-12

## Decision

Create one deep `internal/collection` module as the only writer of the artwork
directory and the only producer of a **Collection Snapshot** that may authorize
TV reconciliation. Source ingestion, uploads, identity, validation,
optimization, collage creation, namespace migration, deduplication, pruning,
and recovery become private stages of this module. The existing `sources`,
`optimize`, and HTTP-upload code may remain as provider/codec adapters, but they
must stop publishing, renaming, or removing collection files themselves.

This ownership boundary closes the present split-brain design:

- `localCollection.prepare` calls a source loader, an optimizer, and then a
  catalog scan, while filename changes escape through a per-TV observer
  ([collection.go](../../internal/sync/collection.go#L47-L92)).
- Catalog rebuild is not read-only: it renames non-canonical files and silently
  deletes duplicates while constructing an in-memory index
  ([catalog_index.go](../../internal/sources/catalog_index.go#L83-L139)).
- Source pruning directly removes filenames selected by a numeric-prefix
  heuristic after provider work completes
  ([loader_sync.go](../../internal/sources/loader_sync.go#L25-L105),
  [catalog.go](../../internal/sources/catalog.go#L221-L247)).
- Optimization replaces bytes, then separately renames the path
  ([resize.go](../../internal/optimize/resize.go#L54-L112),
  [resize.go](../../internal/optimize/resize.go#L137-L164),
  [resize.go](../../internal/optimize/resize.go#L234-L264)).
- Collage creation publishes one output, ignores both input-removal errors, and
  only afterward updates caches and TV mappings
  ([collage_pipeline.go](../../internal/optimize/collage_pipeline.go#L105-L170)).
- The upload endpoint writes directly into the same directory, outside the
  catalog's ownership and locking
  ([upload.go](../../internal/health/upload.go#L163-L227)).

The current in-memory catalog is not a trustworthy transaction boundary. Its
directory-modification-time cache can only report what a scan happened to see,
and its mutation methods update several maps independently of durable state
([catalog.go](../../internal/sources/catalog.go#L26-L75),
[catalog_index.go](../../internal/sources/catalog_index.go#L26-L47)). A snapshot
must instead be derived from a recovered, fully validated committed generation.

## Recommended interface

Keep the application-facing surface small and context-first:

```go
package collection

type Store interface {
	Prepare(ctx context.Context, request PrepareRequest) (Snapshot, error)
	Import(ctx context.Context, request ImportRequest) (Snapshot, error)
}

type PrepareRequest struct {
	Sources []SourceSpec
	Policy  Policy
	DryRun  bool
}

type ImportRequest struct {
	Reader   io.Reader
	Hint     string
	MaxBytes int64
	Policy   Policy
	DryRun   bool
}

type Snapshot struct {
	Generation string
	Items      []Item
	Changes    []Change
	Warnings   []string
	DryRun     bool
}

type Item struct {
	Name   string
	Path   string
	Digest [sha256.Size]byte
	Type   artwork.FileType
	Width  int
	Height int
	Origin Origin
}
```

`Prepare` is the cycle operation: recover an interrupted transaction, scan and
validate the committed collection, resolve all configured sources, stage and
transform additions, plan deterministic rename/dedupe/collage/prune effects,
commit them, then return the new snapshot. `Import` gives the HTTP handler the
same bounded staging, deduplication, validation, commit, and snapshot semantics
instead of a second writer. Both methods serialize through one process-wide
mutation lock.

Do not expose `Rename`, `Remove`, `Rebuild`, `NoteFileRename`, or mutable maps.
Those operations are implementation details whose safety depends on the whole
transaction. Read-only consumers receive a value snapshot with sorted items;
maps or slices returned to callers must not alias internal state. `Path` is
always constructed below the configured root, `Name` is a validated base name,
and every item's full SHA-256 digest was computed from the committed bytes.

A successful non-dry-run return guarantees:

1. recovery found no unresolved transaction;
2. every listed file is a regular, non-symlink, fully decodable JPEG or PNG;
3. every listed name/digest pair still matches the committed directory;
4. the collection manifest is durable;
5. no unlisted engine-owned staged file is authoritative; and
6. the snapshot generation identifies exactly this committed manifest.

If any persistence result is unknown, return an error matching
`durablefs.ErrOutcomeUnknown` and **no usable Snapshot**. The engine must not
reconcile a TV from a partial map. This follows the previously selected
durability contract, under which post-namespace-sync failures require
inspection rather than assumed rollback
([durable-file-semantics.md](durable-file-semantics.md#durable-replacement)).

## Identity and ownership model

Filename parsing is useful presentation logic, but it is not sufficient
authority for ownership or equality. The current code sometimes trusts a hash
suffix without reading bytes and classifies source-managed files solely by a
three-digit prefix ([catalog_index.go](../../internal/sources/catalog_index.go#L142-L159),
[catalog.go](../../internal/sources/catalog.go#L18-L24)). The collection module
must use three separate identities:

| Identity | Meaning | Authority |
| --- | --- | --- |
| `Digest` | Full SHA-256 of committed bytes | Deduplication and TV/local version identity |
| `OriginKey` | Stable provider plus provider-owned item ID, or upload UUID/operator origin | Source resolution and pruning ownership |
| `Name` | Collision-safe, sanitized display filename with a short digest suffix | Filesystem namespace and matte lookup only |

Persist a versioned collection manifest containing `Name`, full `Digest`,
media facts, origin, and management class. Only entries explicitly marked
`source-managed` may be pruned because a source disappeared. Operator artwork
is never inferred to be managed from its filename. Uploads are app-managed for
validation/deduplication but are not source-pruned.

The scanner admits only regular, non-symlink files with supported extensions;
the shared extension allowlist is currently JPEG/JPG/PNG
([identity.go](../../internal/artwork/identity.go#L11-L27)). It rejects every
reserved name and the entire private control directory before hashing or
decoding. This preserves the repository invariant that `mattes.json`, journals,
manifests, staging files, AppleDouble files, and future control artifacts are
never cataloged, renamed, optimized, deduplicated, uploaded, or pruned.

Validation is performed before publication and includes bounded-size reading,
content sniffing, full decode, positive dimensions, configured dimension/pixel
limits, output encoding, a second full decode of the staged output, and a full
digest. The present `ValidateImage` decodes only the configuration header, so a
truncated body can pass it ([resize.go](../../internal/optimize/resize.go#L167-L177));
it is not sufficient as the publication gate.

## Staging and journal layout

Reserve a private directory inside `ARTWORK_DIR`:

```text
.frame-tv-art-manager/          mode 0700, never scanned as artwork
  manifest.json                 mode 0600, versioned committed generation
  transaction.json              mode 0600, at most one active transaction
  staging/                      mode 0700, transaction-scoped outputs
```

The artwork root remains `0755` and committed artwork remains exactly `0644`
for SMB/NFS readability. Control state is `0600` under `0700`, consistent with
the chosen durability classes
([durable-file-semantics.md](durable-file-semantics.md#durability-classes)).
Staging is on the same filesystem as final artwork. Publication and removal use
`durablefs.Rename`/`Remove`; manifest and journal updates use
`durablefs.Replace`. Both source and destination parent directories are synced
when a staged file is published across directories.

The journal is a versioned, checksummed intent record. It contains:

- transaction ID, base generation, policy fingerprint, and phase;
- each input name plus expected full digest and management class;
- each staged output path, final name, full digest, media facts, and mode;
- an ordered list of idempotent namespace operations;
- the complete next manifest, not a patch that depends on process memory;
- completion state for each operation.

Write and sync all staged outputs first. Then durably publish the journal before
the first authoritative namespace change. After every operation,
durably advance the journal. A crash between an operation and its progress
update is expected: recovery inspects names and full digests and repeats or
records the already-completed idempotent effect. Never decide recovery from a
short hash suffix.

One transaction is active at a time. `Import` waits for or returns a typed busy
error when `Prepare` owns the mutation lock; it never bypasses the owner. A
deployment must run one writer process per artwork directory. If multi-process
writers become supported, add a real platform file-lock implementation; a
create-if-absent marker is not a safe crash-recoverable lock.

## Ordered transaction

### 1. Recover and inventory

Recovery always runs before planning. With no active journal, read the manifest
and rescan disk. Hash and fully validate every candidate so the new plan starts
from facts, not stale cache state. Unexpected supported images are adopted as
operator-owned after validation; corrupt or unreadable files are reported and
excluded but not silently deleted. Missing manifest entries are surfaced as
drift and become explicit transaction inputs, never implicit cache cleanup.

### 2. Resolve sources conservatively

Resolve every configured source under the caller context with bounded
concurrency. The current code correctly suppresses pruning if any resolution
or download path reports an error
([loader_sync.go](../../internal/sources/loader_sync.go#L42-L100)); preserve and
strengthen that rule: **any incomplete source view disables all source pruning
for the transaction**. A transient provider failure may still allow safe,
validated additions, but the snapshot carries warnings and no old managed item
is removed.

Stable `OriginKey`s, not source-line order or a global numeric index, decide
whether an item was seen. Downloads stream through a size-limited reader into a
unique exclusive staging file. This replaces the predictable truncating
`<filename>.tmp` path and ignored close/chmod errors currently used by downloads
([loader_download.go](../../internal/sources/loader_download.go#L116-L151)).

### 3. Transform under one admission controller

Collage and optimization are pure transformations from committed inputs to
staged outputs. They cannot rename or remove inputs. The process-wide transform
admission controller permits one active transform and a two-job backlog by
default; per-pixel workers use `min(GOMAXPROCS, 4)`
([resource-efficiency-envelope.md](resource-efficiency-envelope.md#decision)).
Cancellation stops admission and work at defined boundaries. A completed,
validated staged output may remain ephemeral for cleanup, but cancellation
cannot publish it without proceeding through the journaled commit.

Portrait pairs are sorted deterministically as they are today
([collage_pipeline.go](../../internal/optimize/collage_pipeline.go#L219-L260)).
The transaction records both exact input digests and the exact output digest;
recovery will not consume a same-named file whose bytes changed externally.

### 4. Build and publish intent

Plan deterministic operations in this safety order:

1. publish validated additions and replacement outputs;
2. apply namespace-only renames whose bytes are unchanged;
3. remove superseded collage inputs, duplicates, and source-pruned files only
   after their required successor is durable;
4. durably publish the complete next manifest; and
5. mark complete, remove the journal durably, and clean staging best-effort.

For collage specifically, the output is durably visible before either input is
removed. A canonical survivor is durably present before a duplicate is removed.
Source pruning begins only after the complete source view and every retained
addition are durable. No ordering may create a moment with neither the old
last-known-good artwork nor its validated successor.

The transaction journal makes intermediate disk state recoverable, and a
snapshot is withheld until cleanup and final manifest publication finish. The
journal remains the authority whenever disk and manifest temporarily disagree.

### 5. Return a fresh snapshot

After journal removal, rescan the manifest's listed files, verify their full
digests, sort by name then digest, and return a value Snapshot. The engine passes
this same immutable snapshot to every TV; individual capacity filters derive
new values rather than mutate it. This replaces the mutable map currently
shared with concurrent TV goroutines
([engine.go](../../internal/sync/engine.go#L109-L139),
[engine.go](../../internal/sync/engine.go#L181-L238)).

## Snapshot effects for Reconciliation

The collection module never reads or writes per-TV mappings. Its immutable
Snapshot and Changes expose the committed digest facts from which
Reconciliation updates each TV mapping. The mapping contract must identify the
committed local **digest**, not merely a filename. Current mappings are
`filename -> content_id`
([mapping.go](../../internal/sync/mapping.go#L13-L18)), and every optimization
rename currently carries that content ID to the new name
([collection.go](../../internal/sync/collection.go#L79-L91),
[mapping.go](../../internal/sync/mapping.go#L205-L221)). That is only correct
when the bytes are unchanged.

This is a correctness bug for optimization and collage: both create different
bytes, but the inherited content ID still names the old image already on the
TV. Reconciliation can therefore believe the new local output is uploaded when
it is not. Use these semantic effects instead:

| Local change | Mapping effect |
| --- | --- |
| Rename, same full digest | Move the active association to the new name; preserve `content_id`. |
| Replace/optimize, different digest | Keep the old remote association as a deletion tombstone; the new digest is untracked and must upload. |
| Collage `A + B -> C` | Tombstone both old remote associations; `C` starts untracked. Never transfer A's ID to C. |
| Delete/prune | Convert any active association to a deletion tombstone until TV deletion succeeds. |
| Deduplicate equal digests | Choose one deterministic active association for the canonical local item; retain every additional remote ID as a deletion tombstone. |
| New/imported item | No mapping effect until a successful TV upload returns its `content_id`. |

Version mapping state as active associations keyed by local digest/name plus
remote tombstones. Reconciliation applies those effects idempotently from the
Snapshot before TV mutation. A mapping persistence failure reaches cycle health
and prevents that TV from reconciling, but it does not make the already-committed
Artwork Collection transaction depend on every configured TV.

This preserves remote deletion intent. Simply deleting the old filename from
today's mapping before planning would lose the content ID, causing the old TV
image to become “unknown” and potentially remain forever when unknown-image
deletion is disabled. The existing executor deliberately keeps tracked mapping
entries until TV deletion succeeds
([execution.go](../../internal/sync/execution.go#L134-L160)); tombstones retain
that useful property while allowing a changed local digest to upload.

## Dry-run contract

`DryRun` performs recovery inspection and read-only inventory/source resolution
but invokes no durable local mutation, no TV mapping mutation, no provider
post-download mutation, and no TV mutation. It does not create staging files,
rewrite the journal/manifest, rename, chmod, optimize, deduplicate, prune, or
clean existing artifacts. It returns the current committed Snapshot with
`DryRun=true` and deterministic proposed `Changes`; changes requiring downloaded
bytes or transformation are explicitly marked estimates rather than represented
as committed items.

If an unfinished transaction exists, dry-run may report its recovery plan but
must not recover it, and it returns an error that prevents TV reconciliation.
This makes the repository's “no durable local mutation and no TV mutation”
invariant true for the entire cycle, not only the current TV executor, whose
dry-run checks begin after source download and optimization have already run
([engine.go](../../internal/sync/engine.go#L94-L123),
[execution.go](../../internal/sync/execution.go#L41-L60)).

## Exact crash-point invariants

| Crash point | Required on-disk state after restart | Recovery action and invariant |
| --- | --- | --- |
| Before journal publication | Old manifest and collection remain authoritative; staged files are non-authoritative. | Delete orphan staging after inspection. No mapping changed and no old art was removed. |
| During staged write/encode | Old collection is untouched; partial file exists only under reserved staging. | Remove the partial stage. It can never enter a snapshot. |
| After stage sync, before journal | Same as above; complete stage is still ephemeral. | Remove it; recomputation is safe. |
| After journal, before first operation | Journal describes every intended effect; old generation is intact. | Revalidate input and stage digests, then resume. Digest mismatch fails closed without removal. |
| After output rename, before directory sync | Publication outcome is unknown. | Return/retain `ErrOutcomeUnknown`; inspect final and staging paths by full digest, sync the applicable parents, then advance or fail closed. Never overwrite an unrelated final file. |
| After output is durable, before progress update | Both old input and new output exist. | Detect the exact output digest, mark the idempotent step complete, continue. Last-known-good art is duplicated, not lost. |
| After collage output, before either input removal | Output and both inputs exist. | Resume removals only if all recorded digests match. |
| After collage input A removal | Output and input B exist; journal retains A's expected digest/effect. | Verify output, treat absent A as completed only if no conflicting path exists, then remove B durably. At least the output remains. |
| After collage input B removal | Output exists and journal still owns recovery. | Finish manifest publication and verification; never recreate inputs from memory. |
| After canonical dedupe survivor, before duplicate removal | Both equal-digest files may exist. | Preserve canonical, retry duplicate tombstone/removal. Never remove the last copy of a digest. |
| After a duplicate removal | Canonical equal-digest file exists. | Verify canonical digest before recording completion; fail closed if it does not. |
| During source pruning | All retained/additional files are durable; journal lists every prune candidate and complete source view. | Retry individual removals idempotently. Provider failure can never reach this phase. |
| After manifest publish, before journal removal | Collection and manifest are committed. | Verify all manifest digests, then durably remove the journal and return the generation. |
| After journal removal, before response | The committed generation is authoritative. | A new call reconstructs the same Snapshot from manifest plus disk; response loss is harmless. |
| Cancellation before a namespace operation | Journal/staging may exist, old collection remains available. | Stop and return `context.Canceled`; next non-dry call recovers. |
| Cancellation after an operation starts | The syscall may have committed despite cancellation. | Classify by durablefs outcome and inspect during recovery; cancellation never claims rollback. |

At every row: control artifacts remain excluded; no truncated file is listed;
no provider/network failure authorizes pruning; and no Snapshot is returned
while recovery or a persistence failure is unresolved.

## Acceptance gates

### Ownership and API

- All artwork-directory mutations in `sources`, `optimize`, `health`, and
  catalog rebuild are replaced by calls into `internal/collection`.
- Catalog scan is read-only. There is no public mutation API or rename observer.
- Only `Prepare` and `Import` return snapshots; TV reconciliation accepts a
  Snapshot rather than a mutable filename map.
- Concurrent prepare/import tests prove one writer and immutable snapshots.

### Safety and recovery

- Fault-injection tests cover every crash row above, including failures before
  and after each file sync, rename/remove, parent sync, manifest update, journal
  progress update, and journal removal.
- Collage tests prove output-before-input-removal and recovery after each of the
  three namespace operations.
- Dedupe tests prove a canonical equal-digest file exists before any duplicate
  removal and that distinct short-prefix collisions are not deduplicated.
- Provider resolution/download error tests prove zero pruning and preservation
  of every last-known-good managed item.
- Unknown durable outcomes block snapshots and health reports the persistence
  failure until recovery succeeds.

### Identity, validation, and controls

- Full SHA-256, not filename hash text, controls equality and recovery.
- Publication tests reject truncated images that pass `DecodeConfig`, symlinks,
  non-regular files, path traversal, excessive bytes/pixels, and unsupported
  formats.
- `mattes.json`, the reserved control directory, temporary files, AppleDouble
  entries, and arbitrary non-art files are never mutated under scan, optimize,
  dedupe, prune, upload, dry-run, or recovery tests.
- Source-managed ownership comes only from the manifest; numeric filenames do
  not authorize deletion.

### Mapping correctness

- Reconciliation tests prove same-digest rename preserves an ID while
  different-digest optimization does not.
- Reconciliation tests prove collage outputs never inherit either input ID.
- Reconciliation tests prove deleted/replaced IDs remain tombstoned until
  confirmed TV deletion.
- Collection tests prove Prepare and Import never mutate TV mappings.

### Dry run and efficiency

- A whole-cycle dry-run filesystem/TV spy observes zero writes, creates,
  chmods, renames, removals, manifest/journal updates, mapping updates, provider
  hooks, or TV calls.
- Transform admission is process-wide: one active job and two pending by
  default; source/download and hash worker counts meet the selected resource
  envelope.
- Large imports and downloads stream to bounded staging; request bodies and
  image pixel counts are bounded before large allocations.

### Repository verification

- Regression and integration tests keep aggregate coverage at or above 90%.
- Operator-visible recovery, reserved-directory, dry-run, and upload behavior
  is documented in `README.md`.
- `make agent-fix` exits zero with no warnings.

## Implementation sequence

1. Implement `durablefs` and the versioned manifest/journal representations.
2. Implement read-only inventory, full validation, recovery, and Snapshot.
3. Move download and HTTP import staging/publication behind `Store`.
4. Move optimization and collage to pure staged transforms under the shared
   admission controller.
5. Move deterministic rename, dedupe, and source pruning into the transaction.
6. Change the engine/reconciler to consume immutable snapshots and introduce
   digest-aware mapping/tombstone semantics, then remove the
   old catalog mutation and rename-observer surfaces.
7. Run the crash matrix, dry-run spies, constrained resource tests, and the
   complete repository verification gate.
