# Durable file semantics

Research date: 2026-07-12

## Decision

Adopt one crash-consistent filesystem boundary for every application-owned
artifact. A successful operation must mean more than "the bytes reached a Go
writer": it must mean the content, intended permission bits, and containing
directory entry were synchronized, or the caller received an error.

The smallest useful deep module is `internal/durablefs`. Its public surface
should stay limited to semantic operations:

```go
func Replace(ctx context.Context, path string, mode fs.FileMode, write func(io.Writer) error) error
func Rename(ctx context.Context, oldPath, newPath string) error
func Remove(ctx context.Context, path string) error
```

`Replace` owns same-directory temporary-file creation, exact mode setting,
writer execution, file sync, close, rename, parent-directory sync, cleanup,
and contextual errors. `Rename` and `Remove` own the necessary directory sync
(both parents if a future rename crosses directories). Byte-slice convenience
belongs as a small wrapper only if repeated call sites justify it. The module
must not own JSON, images, backups, catalog policy, or TV reconciliation.
Operations honor cancellation before work and immediately before publishing a
namespace mutation. Filesystem syscalls already in progress are not
interruptible through Go's `os` package, so cancellation never pretends to
roll back a completed rename or removal.

This module is necessary but not sufficient for collage replacement. Creating
one collage and deleting two source images is a three-path transaction; it
needs a collection-level transaction/recovery record or an operation ordering
whose intermediate states are explicitly recoverable. Hiding that policy in a
generic file helper would make the helper shallow and the failure semantics
invisible.

## Required contracts

### Durable replacement

For a single target on Linux and macOS:

1. Create a uniquely named temporary file in the target's directory. This
   keeps the rename on one filesystem; Linux rejects cross-mount renames with
   `EXDEV`, and Apple likewise requires both names to be on the same filesystem
   ([Linux `rename(2)`](https://man7.org/linux/man-pages/man2/rename.2.html),
   [Apple `rename(2)`](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/rename.2.html)).
2. Write the complete representation and propagate encoder/write errors.
3. Apply the final mode to the temporary inode before publishing it. Creation
   modes are filtered by the process umask
   ([Go `os.WriteFile`](https://pkg.go.dev/os#WriteFile),
   [Linux `open(2)`](https://man7.org/linux/man-pages/man2/open.2.html),
   [Apple `open(2)`](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/open.2.html));
   `Chmod` sets the intended Unix permission bits directly
   ([Go `os.Chmod`](https://pkg.go.dev/os#Chmod)).
4. Sync and close the temporary file, checking both errors. Go defines
   `File.Sync` as committing file contents to stable storage
   ([Go `File.Sync`](https://pkg.go.dev/os#File.Sync)).
5. Rename the temporary path over the target. On Unix this gives atomic name
   visibility: readers see an old or new instance, not a missing destination
   ([Go `os.Rename`](https://pkg.go.dev/os#Rename),
   [Linux `rename(2)`](https://man7.org/linux/man-pages/man2/rename.2.html),
   [Apple `rename(2)`](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/rename.2.html)).
6. Open and sync the containing directory, then close it and check both errors.
   Linux explicitly states that syncing the file does not persist its directory
   entry and that the directory needs a separate sync
   ([Linux `fsync(2)`](https://man7.org/linux/man-pages/man2/fsync.2.html)).

If any step before rename fails, the old target remains authoritative. If
rename succeeds but directory sync fails, return an error and classify the
durable result as unknown; do not claim rollback. Temporary cleanup is best
effort and must not hide the primary error.

The module must expose this distinction through an error classification usable
with `errors.Is`, such as `ErrOutcomeUnknown`. Callers may retry an operation
that is known not to have committed; after an unknown result they must inspect
and reconcile the destination instead of assuming either the old or new state.

`os.WriteFile` is not a replacement primitive: Go explicitly warns that it
uses multiple system calls and can leave a partially written file after a
mid-operation failure ([Go `os.WriteFile`](https://pkg.go.dev/os#WriteFile)).
Atomic rename is also not, by itself, proof that the new directory entry is
crash-durable.

### Rename and removal

After a successful `Rename`, sync the destination parent and, when the parents
differ, the source parent. After a successful `Remove`, sync its parent before
reporting durable success. A rename or removal that changes only cached
in-memory indexes after the syscall but before directory sync must not be
reported as committed.

On NFS and similar remote filesystems, the server can perform a rename and
then fail before acknowledging it; Linux documents that a failed rename may
therefore have happened. Operations and retries must be idempotent and inspect
the resulting paths before deciding what occurred
([Linux `rename(2)`](https://man7.org/linux/man-pages/man2/rename.2.html)).
SMB/NFS guarantees ultimately depend on the mounted filesystem and server
honoring flush requests.

### macOS scope

The common contract is Go `File.Sync` plus directory sync, which is appropriate
for the Linux/Raspberry Pi production target and normal macOS development.
Apple documents that ordinary `fsync` can leave data in a drive cache and that
`F_FULLFSYNC` asks supported devices to flush that cache
([Apple `fsync(2)`](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/fsync.2.html),
[Apple `fcntl(2)`](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/fcntl.2.html)).
`F_FULLFSYNC` is slower, filesystem/device-dependent, and not a high-level Go
`os` operation. It should be a documented optional stronger policy only if
power-loss-grade macOS guarantees become a requirement, not silently mixed
into the default helper.

## Durability classes

| Class | Meaning | Success contract | Permissions |
| --- | --- | --- | --- |
| S1: sensitive authoritative state | Loss or corruption can make reconciliation unsafe or prevent authentication | Atomic replacement, file sync, close, parent sync; failures reach caller and health; recover or fail closed | File `0600`, directory `0700` |
| S2: durable desired artwork | The file is part of the desired collection and may be shown or used to authorize TV work | Validate before publish; atomic publication/replacement; file and directory sync; rename/removal directory sync; collection transactions recover after interruption | File `0644`, directory `0755` for SMB/NFS |
| S3: replaceable snapshot | Useful audit/derived information that can be regenerated and must never appear truncated | Atomic replacement and error propagation; sync before claiming persistence | File `0600`, directory `0700` |
| S4: operator-owned control | Configuration is authored outside the engine and is never cataloged or mutated by normal cycles | Read only; bootstrap is create-if-absent without overwrite races and is atomically published | `0600` by application default; preserve operator-selected mode on existing files |
| E: ephemeral | Temporary staging or a writability probe, never authoritative | Unique name, bounded lifetime, best-effort cleanup; never catalog | Least privilege while staged |

## Persisted-artifact matrix

| Artifact | Class | Current semantics | Gap / required semantics |
| --- | --- | --- | --- |
| `TOKEN_DIR/tv_<ip>.txt` pairing token | S1 | Direct `os.WriteFile(..., 0600)`; a write error is logged inside a callback and not returned ([connection.go](../../internal/samsung/connection.go#L118-L136)). Startup creates/chmods the token directory `0700` ([main.go](../../cmd/frame-tv-art-manager/main.go#L178-L208)). | Can truncate an existing token and cannot establish durable success. Use durable replacement; return the persistence error through connection setup and health. Never log token content. |
| `TOKEN_DIR/tv_<ip>_mapping.json` and `.bak` | S1 | Best current implementation: same-directory unique temp, `0600`, file sync, checked close, rename, directory sync; old primary is copied through the same path to `.bak`. Load falls back to backup on malformed primary ([mapping.go](../../internal/sync/mapping.go#L43-L136)). Mutation rolls memory back if save fails. | Move the mechanism into `durablefs`; preserve backup policy in the mapping owner. Define the post-rename/directory-sync-failure result as unknown. Consider validating backup bytes before accepting them; do not weaken current behavior. |
| `TOKEN_DIR/tv_<ip>_capacity.json` and `.bak` | S1 | Reuses mapping's `atomicWriteWithBackup`; load falls back to backup and reports malformed state ([capacity.go](../../internal/sync/capacity.go#L39-L88), [capacity.go](../../internal/sync/capacity.go#L115-L132)). | Same consolidation as mapping. Capacity persistence failures currently have callers that only warn/log in parts of reconciliation; S1 failures must reach cycle result and health. |
| `TOKEN_DIR/tv_<ip>_metadata.json` | S3 | JSON is regenerated and written with direct truncating `os.WriteFile(..., 0600)` ([client_art_slideshow.go](../../internal/samsung/client_art_slideshow.go#L13-L53)). | Use durable replacement to prevent a truncated audit snapshot. The immediate function returns errors, but the reconciliation caller must surface them according to the persistence-health policy. |
| `ARTWORK_SOURCES_FILE` when bootstrapped | S4 | A stat-then-`os.WriteFile(..., 0600)` sequence creates an example only when the path appears absent; errors are warnings ([main.go](../../cmd/frame-tv-art-manager/main.go#L211-L215), [main.go](../../cmd/frame-tv-art-manager/main.go#L245-L257)). Normal operation reads but does not rewrite it. | The stat/write pair races with an operator creating the file and can overwrite that content. Publish with exclusive create-if-absent semantics; do not replace an existing operator file. Startup should report a bootstrap persistence failure without treating an existing file as an error. |
| `ARTWORK_DIR/mattes.json` | S4 | Read-only control file; missing and malformed content silently become empty configuration ([config_matte.go](../../internal/config/config_matte.go#L16-L31)). | No durable writer is needed. Preserve the invariant that catalog, optimizer, deduper, and source pruning never mutate this control file. Separately decide whether malformed operator configuration should be surfaced instead of ignored. |
| Existing operator artwork | S2 | Catalog rebuild may rename files to content-hash names and remove duplicates; rename/chmod/remove failures are ignored or only conditionally observed ([catalog_index.go](../../internal/sources/catalog_index.go#L85-L119)). Optimizer renames filenames with no directory sync ([resize.go](../../internal/optimize/resize.go#L137-L164)). | Route every engine-owned rename/remove through durable operations and propagate errors. Update caches/mappings only after the disk commit; on ambiguous remote-filesystem results, inspect/reconcile. Control files must be excluded before any mutation. |
| Download staging `<identity>.<ext>.tmp` | E transitioning to S2 | Predictable path is opened with `O_TRUNC`; concurrent attempts can share/truncate it. Close and chmod errors are ignored; file is not synced ([loader_download.go](../../internal/sources/loader_download.go#L125-L151)). | Use a unique same-directory temp opened exclusively, exact `0644`, checked close/sync, and cleanup. Do not let a staging name enter the catalog. |
| Downloaded final artwork | S2 | After hashing, staging is renamed to a content-addressed final path; post-rename chmod errors and directory durability are ignored ([catalog.go](../../internal/sources/catalog.go#L162-L208)). | Set mode and sync before publication, then rename and sync the artwork directory. Only then update in-memory indexes and report a new download. Idempotently handle an already-existing hash target. |
| HTTP-uploaded artwork | S2 | Strong file staging: random same-directory temp, `0644`, checked file sync and close, then rename. Parent directory is not synced ([upload.go](../../internal/health/upload.go#L163-L227)). | Reuse `durablefs.Replace`/create semantics and sync the parent before returning HTTP success. The content-addressed target makes retry/deduplication naturally idempotent. |
| Optimized in-place image bytes | S2 | Random same-directory temp, encoding, `0644`, file sync, checked close, validation, then replacement rename ([resize.go](../../internal/optimize/resize.go#L225-L267)). | Add parent sync through `durablefs.Replace`. Treat validation as part of the writer/publisher policy, before rename. |
| Optimized filename migration | S2 | A second plain rename follows the content replacement and lacks directory sync ([resize.go](../../internal/optimize/resize.go#L137-L164)). | Durable rename before catalog/mapping notification. Prefer a single publication directly to the final name when it does not obscure collision and recovery behavior. |
| Collage output plus two input removals | S2 multi-file transaction | Output temp is validated and file-synced, then renamed without directory sync; both source removals ignore errors; catalog and mapping observers run afterward ([collage_pipeline.go](../../internal/optimize/collage_pipeline.go#L115-L170)). | Highest artwork durability risk. Define a recoverable three-path transaction. Never delete inputs until the output is durably published. Record enough intent to resume/finish after interruption, propagate removal/sync errors, and make recovery idempotent. |
| Source-pruning removals | S2 | Removal errors are returned and pruning is skipped after any provider-resolution error, protecting last-known-good art ([loader_sync.go](../../internal/sources/loader_sync.go#L87-L103)). Directory removal is not synced. | Preserve the conservative provider-failure gate. Use durable removal and propagate directory-sync failures; update catalog only after commit. |
| Catalog duplicate removals | S2 | `os.Remove` errors are discarded during rebuild ([catalog_index.go](../../internal/sources/catalog_index.go#L111-L119)). | Use the collection mutation owner and report failures. A read/rebuild operation should not silently perform non-durable destructive cleanup; planning and committing cleanup should be explicit. |
| `ARTWORK_DIR` and `TOKEN_DIR` | S2 container / S1 container | Startup creates them as `0755` and `0700`, then explicitly chmods them; chmod/chown failures only warn. A transient `.write_test` is created and removed ([main.go](../../cmd/frame-tv-art-manager/main.go#L178-L208)). | Existing-directory chmod is useful because Go's `MkdirAll` does nothing to an existing directory and creation modes are before umask ([Go `os.MkdirAll`](https://pkg.go.dev/os#MkdirAll)). For sensitive state, inability to enforce `0700` should fail startup rather than warn. The writability probe remains E and its cleanup error is not a persistence failure. |

There are no durable on-disk catalog indexes: `hashIndex`, `prefixMap`,
`visited`, and `catalog` are process memory rebuilt from the artwork directory.
The remote TV collection is external state, not a local persisted artifact; its
coordination with mapping files belongs to reconciliation transaction design.

## Implementation order

1. Extract the already-tested mapping `atomicReplace` behavior into
   `internal/durablefs`, preserving fault-injection coverage and adding durable
   rename/remove tests.
2. Migrate token, metadata, upload, download publication, and optimizer
   replacement paths. Make all persistence errors reach their operation/cycle
   caller and health state.
3. Route catalog renames/removals and source pruning through a single artwork
   collection mutation owner. Stop mutating during an implicit cache rebuild.
4. Design and test collage recovery as a collection-level transaction, with
   crash points after output publication and after each input removal.
5. Add Linux integration tests for old-or-new visibility, exact modes under a
   restrictive umask, cleanup, directory-sync failure, and idempotent recovery.
   Keep unit fault seams local to `durablefs`; callers should test semantic
   error propagation rather than reimplement filesystem mocks.

## Acceptance criteria

- No application-owned durable artifact is written with truncating
  `os.WriteFile` or an unsynchronized rename/remove.
- A success return means the declared class contract completed; a failure after
  namespace mutation is reported as an unknown durable result and reconciled.
- State/auth files are exactly `0600` in directories that are exactly `0700`;
  artwork is `0644` in a traversable `0755` directory.
- Dry runs do not invoke any durable mutation.
- Persistence errors are returned to the cycle/lifecycle owner and reflected in
  health reporting.
- Recovery tests prove interruption cannot expose truncated state or delete the
  only last-known-good artwork.
