# Supervised application lifecycle

Research date: 2026-07-12

## Decision

Create one deep `internal/app` module whose context-first `Run` method owns the
application lifetime. It prepares required state, binds listeners synchronously,
starts each long-lived loop exactly once, publishes lifecycle state, propagates
terminal errors, and performs bounded shutdown. `cmd/frame-tv-art-manager`
becomes a thin process adapter for CLI arguments, signals, logging, and the sole
`os.Exit` call.

The current lifetime is not supervised:

- `main` discards the engine's returned error and waits forever for its goroutine
  without a deadline ([main.go](../../cmd/frame-tv-art-manager/main.go)).
- `main` starts `health.Server.Start` in a goroutine even though `Start` creates
  another goroutine internally ([main.go](../../cmd/frame-tv-art-manager/main.go),
  [server.go](../../internal/health/server.go)).
- Listener creation happens inside `ListenAndServe`, so a bind failure is only
  logged and the process continues without its configured health interface
  ([server.go](../../internal/health/server.go)).
- Directory setup calls `os.Exit` below `main`, while source bootstrap, engine,
  and HTTP shutdown errors are logged or discarded
  ([main.go](../../cmd/frame-tv-art-manager/main.go)).
- Shutdown uses an unbounded background context, so a stuck request or Sync
  Cycle can prevent process termination indefinitely.

Go's `http.Server.Serve` always returns a non-nil error and returns
`http.ErrServerClosed` after `Shutdown` or `Close`; `Shutdown` closes listeners,
then waits for active connections until its context expires
([official `net/http` documentation](https://pkg.go.dev/net/http#Server.Serve),
[official `Server.Shutdown` documentation](https://pkg.go.dev/net/http#Server.Shutdown)).
The supervisor must therefore own both the listener and the interpretation of
the serve result.

## External seam

Keep the application-facing interface to one operation:

```go
type Application struct { /* private dependencies and state */ }

func (a *Application) Run(ctx context.Context) error
```

Construction receives narrow dependencies: startup preparation, the Sync Cycle
runner, an optional already-configured HTTP server/listener factory, lifecycle
status, logger, and shutdown timeout. Tests replace these internal seams; callers
do not learn goroutine, channel, listener, or shutdown choreography.

The process adapter follows this shape:

1. Parse help, version, and healthcheck commands without starting the daemon.
2. Load and validate configuration and construct dependencies, returning errors
   instead of exiting.
3. Create a signal context with `signal.NotifyContext` for `SIGINT` and
   `SIGTERM` ([official `os/signal` documentation](https://pkg.go.dev/os/signal#NotifyContext)).
4. Call `Application.Run` synchronously.
5. Convert a clean return to exit code 0 and any terminal error to exit code 1.
6. Execute the sole `os.Exit` only after all deferred cleanup has run. Go
   documents that `os.Exit` terminates immediately and does not run deferred
   functions ([official `os.Exit` documentation](https://pkg.go.dev/os#Exit)).

After the first signal cancels the context, call the `NotifyContext` stop
function promptly so normal signal behavior is restored. A second signal then
terminates the process instead of waiting for another graceful-shutdown attempt.

## Startup and readiness sequence

`Run` performs these phases in order:

1. Set lifecycle state to `starting`.
2. Validate/create directories and enforce exact permissions. Sensitive
   directory permission or ownership failures are fatal; helpers return wrapped
   errors and never call `os.Exit`.
3. Bootstrap an absent sources file with exclusive, durable create-if-absent
   semantics. A configured bootstrap persistence failure is fatal.
4. Recover and verify the Artwork Collection. An unresolved or unknown durable
   outcome is fatal to this start attempt because no Collection Snapshot can be
   trusted.
5. If HTTP is enabled, call `net.Listen` synchronously before reporting startup
   success. A bind error is fatal and no child loop starts.
6. Start the HTTP `Serve` loop and Sync Cycle loop exactly once under a derived
   child context.
7. Set lifecycle state to `ready` only after both required loops have started.

Binding before the goroutine removes the current race in which the process can
appear started before discovering that the port is unavailable. The listener
factory is an internal seam; production uses `net.Listen`, while tests use a
real ephemeral listener or a deterministic failing adapter.

The first Sync Cycle does not block readiness. It may include slow source and
image work, and the health interface must remain responsive during it. A
startup recovery failure is different: it prevents readiness because unknown
local state cannot authorize work.

## Lifecycle and health states

Use explicit states rather than inferring process readiness from whether a
cycle has completed:

| State | Meaning | `/live` | `/ready` | existing `/health` |
| --- | --- | ---: | ---: | ---: |
| `starting` | Preparation or recovery is in progress | 200 once served | 503 | 503 |
| `ready` | Required loops are running and local state is trustworthy | 200 | 200 | 200 if latest cycle is healthy |
| `degraded` | Process is operational but the latest cycle failed | 200 | 200 | 503 with the cycle error |
| `stopping` | Graceful shutdown is in progress | 503 | 503 | 503 |
| `failed` | A terminal child or shutdown error occurred | 503 while reachable | 503 | 503 |

`/health` retains the operator-visible latest-cycle semantics. `/live` answers
only whether the process supervisor is functioning; `/ready` answers whether
startup completed and the process can safely accept work. TV/network/provider
failure degrades health but does not make HTTP upload or the process itself
unready. Unresolved collection recovery or persistence state does.

Health snapshots must be copied under the lock and encoded after releasing it;
slow clients must not hold the lifecycle/status mutex. Handlers remain available
during CPU-heavy image transformation under the selected p99 latency gate.

## Child-loop contract

The supervisor owns one result channel per child or one bounded aggregate
channel. Every started goroutine sends exactly one terminal result and exits.
No child closes a shared result channel.

- The Sync Cycle loop continues after transient cycle failures, records them in
  health, and returns only for parent cancellation or an unexpected terminal
  condition.
- `context.Canceled` or `context.DeadlineExceeded` caused by supervisor shutdown
  is expected and not a terminal process error.
- An HTTP serve result is expected only when shutdown has begun and the error
  matches `http.ErrServerClosed`. Any earlier or different serve error is
  terminal.
- A terminal child result cancels the shared child context, moves lifecycle to
  `failed`, shuts down the other child, and is returned to the process adapter.

Cancellation propagates through the derived context tree. Official Go guidance
states that canceling a parent cancels derived contexts and lets work stop early
([Go context documentation](https://go.dev/blog/context),
[Go cancellation guide](https://go.dev/doc/database/cancel-operations)). Context
is passed to operations; it is not stored as mutable application state.

## Bounded graceful shutdown

The default shutdown budget is 30 seconds and is operator-configurable as a
validated duration. On first signal, parent cancellation, or terminal child:

1. Set state to `stopping` unless already `failed`.
2. Cancel the child context so new source, transform, TV, and upload work stops
   at safe context boundaries.
3. Create a fresh timeout context from `context.Background`; deriving shutdown
   from the already-canceled run context would cancel it immediately.
4. Call `http.Server.Shutdown` and wait for the engine concurrently within the
   same absolute deadline.
5. Close cached TV transports and other registered resources. Every close error
   that affects correctness is joined into the result.
6. If graceful HTTP shutdown times out, call `Server.Close` to stop active
   connections, preserve both errors, and continue process termination.
7. If any child remains after the deadline, return a shutdown-timeout error.

`Server.Shutdown` does not close hijacked connections such as WebSockets, so TV
WebSocket ownership remains with the engine/client cleanup path rather than the
health server ([official `Server.Shutdown` documentation](https://pkg.go.dev/net/http#Server.Shutdown)).

A clean signal-triggered shutdown returns nil even though child loops observed
`context.Canceled`. Shutdown timeout, persistence/close failure, or unexpected
child termination returns a joined error and exit code 1.

## Exit-status contract

| Condition | Exit |
| --- | ---: |
| Help, version, successful healthcheck | 0 |
| Clean `SIGINT`/`SIGTERM` shutdown | 0 |
| Invalid configuration or startup preparation | 1 |
| Configured listener bind failure | 1 |
| Unrecoverable Artwork Collection state | 1 |
| Unexpected engine or HTTP termination | 1 |
| Shutdown timeout or correctness-affecting close failure | 1 |
| Failed standalone healthcheck | 1 |

Use portable exit codes in the 0–125 range as recommended by the Go `os`
documentation. Log a terminal error once at the process edge with structured
fields; inner modules add context with `%w` but do not duplicate terminal logs.

## Deterministic acceptance tests

### Startup

- Table-driven preflight tests cover configuration, permission, ownership,
  bootstrap, collection recovery, and listener bind failures.
- A deliberately occupied ephemeral port proves bind failure returns before the
  engine starts and yields exit code 1.
- HTTP-disabled startup runs the engine without creating a server goroutine.
- Lifecycle transitions are observed in exact order and readiness is never true
  before successful preparation and child startup.

### Supervision

- Injected engine and server loops prove each starts once and every terminal
  result is observed once.
- Unexpected engine return cancels/stops HTTP and is returned.
- Unexpected serve error cancels/stops the engine and is returned.
- Transient Sync Cycle errors change health to degraded without terminating the
  process.
- Race-enabled tests exercise simultaneous signal cancellation and child error;
  no send, close, listener, or status race occurs.

### Shutdown

- Parent cancellation proves engine context cancellation, HTTP shutdown, TV
  cleanup, and nil return when all complete.
- A blocking handler and blocking engine prove one shared deadline, forced HTTP
  close, timeout propagation, and bounded return.
- Multiple close/shutdown errors are preserved with `errors.Is` compatibility.
- Subprocess tests send first and second signals without terminating the test
  runner: the first begins graceful shutdown and the second uses restored
  default signal behavior.
- Goroutine leak checks compare stable eventual counts after repeated starts and
  stops; tests do not rely only on sleeps.

### Health and compatibility

- `/live`, `/ready`, `/health`, and `/status` are tested in every lifecycle
  state and under concurrent status updates.
- Health p99 remains below the selected 250 ms constrained-runner gate during
  the heaviest transform.
- Docker healthcheck behavior and startup grace are updated/documented for the
  explicit starting/readiness states.
- Existing help, version, healthcheck, environment, and JSON status behavior is
  preserved unless README documents the intentional change.

### Repository gate

- Aggregate coverage remains at least 90 percent.
- `go test -race ./...` passes for lifecycle/status packages.
- `make agent-fix` exits zero with no warnings.

## Implementation order

1. Make directory/source startup helpers return wrapped errors and add a
   testable process-edge function that returns an exit code.
2. Change the health server from self-starting `Start` to configured handler,
   server, and blocking `Serve(listener)` behavior; bind in the supervisor.
3. Add explicit lifecycle state and `/live`/`/ready` behavior.
4. Implement `internal/app.Application.Run` and move engine/server goroutine
   ownership into it.
5. Make engine cancellation and cached TV cleanup deterministic; remove forced
   end-of-cycle GC under the resource-efficiency gates.
6. Add bounded shutdown and second-signal behavior.
7. Replace old lifecycle tests with interface-level supervisor and subprocess
   tests, update Docker/README behavior, and run all gates.
