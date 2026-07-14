# Resource-efficiency envelope

Status: decision-ready research, 2026-07-12

## Decision

The application should support a **512 MiB reserved-memory / 1 CPU deployment
class** for its normal synchronization workload. Image transformation is the
dominant resource consumer and must have one process-wide admission controller,
with one transform active by default. The Go soft memory limit is a safety rail,
not the admission controller.

When the runtime has an explicit memory reservation, set `GOMEMLIMIT` to 85–90%
of that reservation. Do not bake an absolute `GOMEMLIMIT` into the binary when
the available memory is unknown. The Go GC guide recommends a further 5–10% of
headroom for memory the runtime does not know about; the slightly larger margin
here also covers the executable, thread stacks, network buffers, and image
codec/runtime variance ([Go GC guide, memory-limit advice][go-gc]).

Use the Go runtime's default `GOMAXPROCS`. Go 1.25 and newer derive that default
from a container CPU limit when it is lower than the machine's logical CPU
count, and update it if the limit changes. An explicit `GOMAXPROCS` remains an
operator override ([Go container-aware `GOMAXPROCS`][go-gomaxprocs]). This
repository builds with Go 1.26.5, so the container-aware behavior applies.

Recommended initial controls:

| Resource | Default | Reason |
| --- | ---: | --- |
| Active image transforms | 1 process-wide | A single current transform reaches about 395 MiB RSS; parallel transforms multiply the live image set. |
| Pending transform jobs | 2 | Bounds retained job metadata and makes overload visible quickly. |
| Per-pixel workers | `min(GOMAXPROCS, 4)` | Avoids today's fixed eight runnable workers per kernel on small CPU reservations. |
| Source-line resolution/download | 2 | Downloads stream to disk; two preserves I/O overlap without allowing five simultaneous post-download transforms. |
| Catalog hashing | `min(GOMAXPROCS, 4)` | Hashing is streaming and CPU/disk bound; sixteen workers add contention without helping constrained deployments. |
| TV reconciliation | `min(number of TVs, max(1, GOMAXPROCS))` | Bounds sockets and goroutines while preserving parallelism. |

These are defaults, not promises that more parallelism is faster. Any increase
must pass the gates below on the project's pinned benchmark runners.

## Current evidence

### Measured development baseline

A one-iteration, end-to-end `OptimizeFile` benchmark was run on Apple M1 Pro,
Darwin/arm64, with `GOMAXPROCS=4`, transforming a 4096×2304 JPEG to
3840×2160:

| Setting | Allocated bytes/op | Peak RSS | Time/op |
| --- | ---: | ---: | ---: |
| `GOMEMLIMIT=384MiB` | 402,127,912 B | about 404,448 KiB | about 718 ms |
| `GOMEMLIMIT=256MiB` | not separately retained | about 404,320 KiB | not a valid cross-platform comparison |

The lower soft limit did not lower peak RSS. This is expected behavior when the
program's live working set cannot fit below the limit: `SetMemoryLimit` is a
soft limit, and the runtime may exceed it rather than continuously thrash. It
accounts for runtime-managed memory, not every byte in process RSS
([`runtime/debug.SetMemoryLimit`][go-memory-limit], [Go GC guide][go-gc]). A
memory limit therefore cannot make several simultaneously live 4K buffers fit
into an undersized reservation.

The same development host produced these one-iteration allocation results:

| Benchmark | Bytes/op | Allocations/op |
| --- | ---: | ---: |
| 256×144 saliency map | 4,901,864 B | 65 |
| 3840×2160 canvas texture | 33,181,968 B | 28 |
| 3840×2160 gallery polish | 4,000 B | 25 |

A stripped Linux/arm64 binary is about 7.4 MB. These numbers are useful as an
allocation and binary-size baseline. **The Apple timing is not a performance
claim for Linux, another CPU, or a constrained container.** Timing gates must
run on pinned target runners.

### Allocation model

One 3840×2160 `image.RGBA` pixel buffer is exactly 33,177,600 bytes (31.64 MiB),
before its small Go object header. The current transform path:

1. decodes the complete source image;
2. commonly converts it to another complete RGBA buffer;
3. allocates a complete target buffer for crop/scale;
4. allocates another complete target buffer for sharpen;
5. may allocate another complete buffer for canvas texture; and
6. retains the final buffer while JPEG encoding.

That structure explains why total allocation is roughly 383.5 MiB per ordinary
end-to-end operation even though not all allocated bytes are live at once. The
large `golang.org/x/image/draw` Catmull-Rom scale and successive full-frame
buffers are the first places to profile and deepen; do not trade away output
correctness without fixture-based visual comparisons.

## Concurrency inventory and risks

The current controls are local rather than process-wide:

- Catalog optimization forces at least four and at most sixteen simultaneous
  files based on `runtime.NumCPU` ([`internal/optimize/pipeline.go`](../../internal/optimize/pipeline.go)).
  Four current transforms could approach four times the single-transform live
  set.
- Every pixel kernel starts eight goroutines regardless of the runtime CPU
  allowance ([`internal/optimize/effects.go`](../../internal/optimize/effects.go)).
  Four transforms can therefore make dozens of CPU-bound goroutines runnable.
- Source synchronization permits five source lines concurrently, and each line
  can call `OptimizeFile` immediately after a download
  ([`internal/sources/loader_sync.go`](../../internal/sources/loader_sync.go),
  [`internal/sources/loader_download.go`](../../internal/sources/loader_download.go)).
  Reducing only the catalog worker pool would leave this second transform path
  uncontrolled.
- Catalog rebuild can hash with sixteen workers
  ([`internal/sources/catalog_index.go`](../../internal/sources/catalog_index.go)).
- TV reconciliation starts one goroutine per configured TV with no admission
  bound ([`internal/sync/engine.go`](../../internal/sync/engine.go)).
- Collage creation is serial, but deliberately retains two decoded/rotated
  sources, two panel crops, a 4K canvas, and a sharpen destination during part
  of the operation ([`internal/optimize/collage_pipeline.go`](../../internal/optimize/collage_pipeline.go)).
- The engine forces `runtime.GC` and `debug.FreeOSMemory` after every cycle.
  This can reduce idle RSS, but it should be retained only if target profiles
  show that its CPU/latency cost is justified; Go's normal pacer should own GC
  frequency ([`internal/sync/engine.go`](../../internal/sync/engine.go)).

`runtime.NumCPU` is not an admission-control policy. CPU count, scheduler
parallelism, memory reservation, and per-operation live memory are distinct.
The transform semaphore must wrap every entry point to transformation, including
catalog work, post-download work, collages, and uploads.

## Overload behavior

Resource pressure must degrade throughput, not correctness:

- Admit at most one transform and keep at most two pending jobs by default.
- Scheduled/background work waits with context cancellation. It never creates
  an unbounded goroutine or in-memory queue.
- An interactive request that cannot enter the bounded queue promptly returns a
  retryable overload response (`429` plus `Retry-After`) before promising that
  work was accepted. If the upload was already durably committed, report that
  storage succeeded and optimization is deferred; do not delete it.
- Cancellation before admission performs no mutation. Cancellation during a
  transform stops at the next safe stage boundary, removes only its temporary
  output, and preserves the last-known-good artwork.
- A memory-limit or queue-pressure event marks health as degraded and records
  structured queue depth, wait duration, active transforms, and the operation
  class. Liveness remains responsive.
- Never respond to overload by pruning artwork, skipping transactional writes,
  weakening image validation, or authorizing TV mutation from unknown state.

## Required observability

Publish or log at least:

- active and queued transforms, admission wait, duration, outcome, input pixel
  count, input bytes, and mode;
- process RSS from the host/container boundary and Go runtime memory classes;
- GC cycles, GC CPU fraction, heap goal, and configured memory limit;
- source, catalog, and TV worker counts; and
- health-handler latency during transformation.

Use `runtime/metrics` for stable runtime counters and heap classes, and capture
heap/alloc-space profiles with `pprof` when a gate moves
([`runtime/metrics`][go-runtime-metrics], [Go diagnostics][go-diagnostics]).
Do not infer RSS from Go heap metrics.

## Portable regression gates

Run correctness gates on every architecture. Run resource and timing gates in
a pinned Linux container on dedicated amd64 and arm64 runners; compare only a
runner to its own stored baseline.

### Hard acceptance gates

1. A container with a 512 MiB memory reservation and `GOMEMLIMIT=448MiB`
   completes every fixture below with transform concurrency one, no OOM kill,
   no corrupted/truncated file, and peak container memory below 480 MiB.
2. A 4096×2304-to-3840×2160 ordinary transform allocates no more than
   268,435,456 bytes/op (256 MiB). This is a deliberate improvement gate from
   the current 402,127,912 bytes/op, not a description of current compliance.
3. Peak memory with a ten-file backlog is no more than 10% above peak memory
   with one file. Queue depth must not multiply decoded image memory.
4. At most one transform is observed active across catalog, download, collage,
   and upload entry points under a race-enabled integration test.
5. Health requests during the heaviest transform have p99 latency below 250 ms
   and zero failures on the pinned 1-CPU runner.
6. Idle container memory, after a completed cycle and two normal GC cycles, is
   below 96 MiB. The test must not call `FreeOSMemory` solely to pass.
7. No benchmark has a median duration regression greater than 10% against the
   previous accepted baseline on the same pinned runner unless the change
   documents an intentional correctness/quality trade-off.
8. The stripped `linux/amd64` and `linux/arm64` binaries each remain below
   12 MiB.

### Fixture matrix

Use committed, license-safe fixtures with recorded SHA-256 digests:

| Case | Input | Mode exercised |
| --- | --- | --- |
| Display-size landscape | 3840×2160 JPEG | Decode, sharpen, dither, encode |
| Oversize landscape | 4096×2304 and 6000×4000 JPEG | Crop/scale and high input-pixel pressure |
| Portrait | 3024×4032 JPEG with EXIF orientations 1 and 6 | Rotation plus crop and padded modes |
| Smart crop | 6000×4000 JPEG | Saliency allocations and output stability |
| Museum | 3840×2160 JPEG | Canvas texture and polish |
| Collage | Two 3024×4032 JPEGs | Maximum multi-image live set |
| Invalid/hostile | Truncated JPEG, oversized dimensions, body above limit | Early rejection without durable damage |
| Backlog | Ten mixed images | Queue bound and non-multiplying memory |

For transforms, assert pixel dimensions, decode success, naming, transactional
replacement, and a checked-in perceptual/output checksum policy. Resource gains
do not excuse silently changing crop composition or image effects.

### Measurement protocol

1. Record commit, Go version, OS image digest, architecture, CPU reservation,
   memory reservation, `GOMAXPROCS`, `GOMEMLIMIT`, fixture digest, and all image
   options.
2. Build once with the production flags. Do not include compilation or fixture
   generation in results.
3. Run one cold iteration, then at least ten measured warm iterations. Report
   median and p95 duration, bytes/op, allocs/op, peak container memory, and GC
   CPU fraction; retain raw output as a CI artifact.
4. Run each fixture alone, then the backlog. Run normal, race, and memory-limit
   suites separately because instrumentation changes timing and memory.
5. Take alloc-space and in-use-space profiles for any allocation/RSS regression.
6. Never compare wall-clock numbers across different hosts as if they measured
   the same performance. Allocation counts may also change with Go/library
   versions, so update a baseline only with an explained toolchain change.

Go's benchmark API reports allocations with `-benchmem`, while profiles show
which allocation sites matter; the official GC guide recommends alloc-space for
allocation-rate hot spots ([Go `testing` benchmark docs][go-testing], [Go GC
optimization guide][go-gc-optimization]).

## Implementation sequence

1. Add one injected resource controller owning transform admission, queue
   capacity, and metrics; route every transform entry point through it.
2. Make optimization context-aware at safe stage boundaries and change local
   worker counts to respect runtime parallelism.
3. Add the fixture benchmark harness and pin baselines before changing kernels.
4. Reduce the transform live set: release decoded/intermediate images earlier,
   reuse or process buffers in place where output is identical, and profile the
   Catmull-Rom path. Reach the 256 MiB allocation gate.
5. Bound source, catalog, and TV fan-out and implement explicit overload
   responses.
6. Add deployment examples that pair a memory reservation with an 85–90%
   `GOMEMLIMIT`; leave the limit unset when no reservation is known.
7. Remove forced end-of-cycle GC only after the idle-RSS gate passes without it.

This sequence first prevents multiplicative memory use, then makes individual
operations cheaper. It avoids relying on GC tuning to compensate for an
unbounded application-level live set.

[go-gc]: https://go.dev/doc/gc-guide#Memory_limit
[go-memory-limit]: https://pkg.go.dev/runtime/debug#SetMemoryLimit
[go-gomaxprocs]: https://go.dev/blog/container-aware-gomaxprocs
[go-runtime-metrics]: https://pkg.go.dev/runtime/metrics
[go-diagnostics]: https://go.dev/doc/diagnostics
[go-testing]: https://pkg.go.dev/testing#hdr-Benchmarks
[go-gc-optimization]: https://go.dev/doc/gc-guide#Optimization_guide
