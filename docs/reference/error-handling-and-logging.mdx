# Error Handling & Logging Conventions

Forward-looking reference material, not a description of current code. It proposes how
every package in Quickspin should report failures and emit logs once there is more than a
greeting to report on. It is adapted from a sibling learning project that carried these
conventions in real code; the examples here are re-cast in Quickspin's domain
(`internal/runtime`, a future control plane, future workers).

Two rules carry most of the weight:

1. **`E` at the boundary with the outside world, `Wrap` everywhere above it.**
2. **One log per error** — the function that stops propagating an error logs it.

---

## Errors

Each package owns its error type, named after the package, in that package's `errors.go`:

| Package                  | Type                | Exists?         |
| ------------------------ | ------------------- | --------------- |
| `internal/runtime`       | `RuntimeError`      | plan 02         |
| `internal/controlplane`  | `ControlPlaneError` | future (plan 05) |
| `internal/worker`        | `WorkerError`       | future (plan 17) |

They are deliberately near-identical — about 30 lines each, differing only in the name.
A shared `errkit` package would exist for one line (`debug.Stack()`), and the duplication
keeps each package self-contained and its errors self-identifying.

```go
type RuntimeError struct {
    Op      string // e.g. "runtime.dockerRuntime.Create" — package.Type.Method
    Message string // user-friendly, no %v of the cause and no trailing newline
    Err     error  // wrapped cause (nil at an origin with no cause)
    Stack   string // captured ONLY at the origin
}
```

`Error()` renders `op: message: cause`, and `Unwrap()` makes `errors.Is` / `errors.As`
work through the whole chain.

### Composing with the sentinel errors

Plan 02 commits `internal/runtime` to sentinel errors (`ErrNotFound`, `ErrImageMissing`)
that callers test with `errors.Is`. The struct type does not replace those — it carries
them. Put the sentinel in `Err` at the origin, and because `Unwrap()` walks the chain,
`errors.Is(err, runtime.ErrNotFound)` still succeeds through any number of `Wrap`s above:

```go
// origin, inside the Docker backend: classify the daemon's "no such image" as our sentinel,
// and attach the operational detail the caller cannot reconstruct.
return Info{}, E("runtime.dockerRuntime.Create",
    fmt.Sprintf("pulling image %s", spec.Image), ErrImageMissing)
```

A caller three layers up still writes `if errors.Is(err, runtime.ErrImageMissing)`, and
also gets an `Op` trail and a stack for the log. The sentinel answers *what kind* of
failure; the struct answers *where* and *while doing what*.

### `E` vs `Wrap`

- **`E(op, message, err)` — origin.** The cause is a sentinel of this package, `nil`, or
  something from outside this codebase: the Docker SDK, `net/http`, `encoding/json`.
  Captures the stack.
- **`Wrap(op, message, err)` — everything above.** The error already carries a stack, so
  this does not capture a second one.

```go
// origin: the Docker SDK is outside our code
return Info{}, E("runtime.dockerRuntime.Inspect",
    fmt.Sprintf("inspecting container for sandbox %s", id), err)

// above it: the error already carries a RuntimeError with a stack
return Wrap("controlplane.Service.CreateSandbox",
    fmt.Sprintf("creating sandbox from spec %s", spec.Image), err)
```

Put the cause in `Err`, never formatted into `Message` — `Error()` appends it for you.

### Wrap vs log-and-replace at a boundary

- **Your caller can act on it** → wrap and return. (`runtime.Create` → the CLI command, or
  → `controlplane.CreateSandbox`.)
- **You are a top-of-goroutine loop, or the error crossed an HTTP process boundary** →
  log the underlying error once, then continue or mint a fresh error of your own
  package's type.

The HTTP case arrives earlier than the worker split: the guest agent (plan 07) is the
first separate process, and the worker daemon (plan 17) is the second. Either way, a Go
error never crosses the wire — the remote side serializes the shared error DTO, and the
client (`guest.Client`, the plane's worker client) logs what the DTO said and mints a
fresh error of its own package's type rather than pretending to wrap an error it does not
have. The reconciler loop (plan 06) is the other case — a top-of-goroutine loop that logs
and continues rather than returning to a caller that isn't there.

## Logging

Stdlib `log/slog` only. **No `log.Printf`, no `fmt.Println` for diagnostics** — those
carry no identity, which is the problem this replaces.

`main.go` builds the root logger once and hands child loggers down through constructors:

```go
logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
rtLogger := logger.With("component", "runtime", "backend", "docker")
rt := runtime.NewDockerRuntime(dockerClient, rtLogger)
```

- Components that own an identity (`runtime`, control plane, worker) hold a `logger` field
  and log.
- APIs reuse their owner's logger with `.With("subcomponent", "api")`.
- **Leaf libraries are silent** — they only *return* errors, never log. Components log.
  A container's stdout/stderr streamed back to the caller is passthrough, not logging, and
  is fine.

Accept a logger, don't reach for a global. `slog.Default()` and package-level `slog.Info`
are back doors that undo the identity threading.

### Attribute keys

`component`, `subcomponent`, `sandboxID` (the `sbx_` id), `containerID`, `image`, `state`,
`worker` (name), `err`, `url`, `code` (HTTP status), `backend`.

`sandboxID` is the routing key — it is Quickspin's own identity for a sandbox and what the
control plane's records are keyed by. The backend's `containerID` is an implementation
detail that should not escape `internal/runtime`; log it for correlation, never join on it
across components.

### Levels

| Level   | Use for                                                                  |
| ------- | ------------------------------------------------------------------------ |
| `Debug` | periodic loop chatter — reconcile ticks, polling, "listing managed sandboxes" |
| `Info`  | lifecycle — listening, sandbox created, container started/destroyed        |
| `Warn`  | recoverable or retried — reconcile drift repaired, worker heartbeat late    |
| `Error` | failed and dropped or gave up                                            |

## HTTP responses (once there is an HTTP surface)

When the control plane grows an HTTP API (plan 05) and later clients (the guest client
in plan 07, the worker client in plan 17), a shared `internal/httpapi` helper should own
the status-plus-JSON dance rather than each handler hand-rolling it. An `ErrorResponse`
DTO is a wire format shared by the server and the client, so it belongs to neither —
plan 05 creates the package for exactly this reason.

```go
httpapi.WriteError(w, http.StatusNotFound, "sandbox not found")  // body Code == 404, always
httpapi.WriteJSON(w, http.StatusCreated, info)
```

`WriteError` should set the body's `Code` from the status it just wrote, so the two cannot
disagree. A `204` must write no body at all: `w.WriteHeader(http.StatusNoContent)` and
nothing else.

## Known gaps to decide deliberately

- `Stack` is captured at every origin but nothing reads it unless a log site emits it.
  Either surface it at `Debug` (a `LogValue()` on each error type) or drop the field; it
  costs a `debug.Stack()` per error for no payoff until something reads it.
- `Wrap` may be unused in a package that is always at one end of the chain. Keeping it so
  every package presents the same API is a reasonable call, but it is a call.
- Sentinel vs struct is resolved in the plans (see plan 02): the sentinels are the public
  contract callers switch on; the struct is the diagnostic envelope carrying them in
  `Err`. Plan 02 pins the composition with a test
  (`TestRuntimeErrorPreservesSentinel`) so the error type can never silently swallow a
  sentinel it was handed.
