# 02 — Sandbox runtime: lifecycle

Depends on: 01.

## Summary

Build the first real Go package: `internal/runtime`, which owns the create → inspect →
destroy lifecycle of a single sandbox backed by a Docker container. Exercise it through
a `quickspin sandbox` CLI subcommand, not HTTP. Everything later (exec, files, the
control plane, other backends) hangs off the interface defined here.

## Industry context

Every platform has this layer. In E2B it's the code behind `Sandbox.create()`; in
Daytona, the workspace provisioner; in Kubernetes, the kubelet's container-runtime
calls. The industry pattern worth copying is the **backend-neutral interface**: the
caller speaks in sandbox specs and sandbox IDs, never in Docker terms. That is what
lets plan 14 slide Firecracker underneath without rewriting the control plane — exactly
how Daytona supports multiple providers and how Kubernetes swapped Docker for
containerd behind the CRI.

## What you'll learn

- The Docker Engine API from Go (`github.com/docker/docker/client`) — what `docker run`
  actually does (create + start are separate calls).
- Designing a Go interface for a resource lifecycle: constructors, contexts everywhere,
  error taxonomy with `errors.Is`.
- Idempotency as a design property, not an afterthought.
- Labels as the mechanism for "which containers are mine?" — the seed of reconciliation.
- The repo's error and logging conventions applied for the first time (see
  `docs/reference/error-handling-and-logging.md`): a `RuntimeError` struct that carries
  the sentinels, `E` at the Docker-SDK boundary, and a `slog` child logger injected
  through the constructor.

## Design and interfaces

You implement this interface; it is the spine of the whole project:

```go
package runtime

// Spec describes what to create. Fields expand in later plans.
type Spec struct {
    Image string
    Env   map[string]string
}

type State string

const (
    StateRunning State = "running"
    StateStopped State = "stopped"
)

type Info struct {
    ID        string // quickspin's ID, not the container ID
    State     State
    CreatedAt time.Time
}

// Sentinel errors; callers test with errors.Is.
var (
    ErrNotFound     = errors.New("sandbox not found")
    ErrImageMissing = errors.New("image not available")
)

type Runtime interface {
    Create(ctx context.Context, spec Spec) (Info, error)
    Inspect(ctx context.Context, id string) (Info, error)
    List(ctx context.Context) ([]Info, error)
    // Destroy of an unknown id returns nil: cleanup must be retry-safe.
    Destroy(ctx context.Context, id string) error
}
```

Decisions the plan commits to:

- Quickspin generates its own sandbox IDs (e.g. `sbx_` + random suffix) and stores them
  on containers as labels (`quickspin.id=...`, `quickspin.managed=true`). Container IDs
  never escape the package (log them for correlation; never join on them).
- Per the error-handling reference: `internal/runtime/errors.go` defines `RuntimeError`
  (`Op`, `Message`, `Err`, `Stack`) with `E`/`Wrap` constructors. Sentinels ride in
  `Err` so `errors.Is(err, ErrNotFound)` works through the chain; the sentinels remain
  the public contract, the struct is the diagnostic envelope.
- Per the code-structure reference: ID generation/validation and the managed-label
  filter are pure helpers (no receiver, no I/O) with their own table-driven tests —
  they are the first entries in what will become each package's decision layer.
- `NewDockerRuntime(client, logger)` takes a `*slog.Logger`; no package-level logging.
- Containers run a long-lived no-op entrypoint (e.g. `sleep infinity`); commands come
  later (plan 03).
- CLI surface: `quickspin sandbox create|list|inspect|destroy`.

## Tasks

1. Define the interface and error values in `internal/runtime`.
2. Implement `dockerRuntime` against the Lima Docker socket.
3. Wire the CLI subcommands.
4. Write the tests below (they hit real Docker — see Definition of done).
5. Add `make test-docker` for tests that need the daemon, tagged separately from pure
   unit tests so `make test` stays fast.

## Definition of done

Pure-helper unit tests (`make test`), table-driven per the code-structure reference:

- `TestNewSandboxIDHasPrefixAndIsUnique` / `TestNewSandboxIDIsNeverEmpty`.
- `TestManagedLabelFilterMatchesOnlyOurs`.
- `TestRuntimeErrorPreservesSentinel` — `errors.Is` finds `ErrNotFound` through an
  `E` + `Wrap` chain (pins the convention itself).

TDD against the real daemon (an integration test here is worth more than a mocked
client). Required tests, all passing via `make test-docker`:

- `TestCreateThenInspect` — create returns a `sbx_` ID; inspect reports running.
- `TestCreateWithMissingImage` — unknown image yields `ErrImageMissing`, and no
  half-created container is left behind (verify by listing labeled containers).
- `TestDestroyIsIdempotent` — destroy twice; second call returns nil.
- `TestInspectUnknownID` — returns `ErrNotFound`.
- `TestListOnlySeesManagedContainers` — a manually-run unlabeled container is invisible.
- Every test cleans up in `t.Cleanup`; a final `TestNoLeakedContainers` (or a shared
  check) asserts zero `quickspin.managed` containers remain.

Deliberately untested: concurrent creates, daemon restarts mid-create, non-Docker
backends.

## Solo-developer tradeoffs

Commercial platforms start at containerd or a microVM for isolation and density; Docker
is a fat dependency with a root daemon. You start with Docker because its API is the
best-documented on-ramp and the interface above quarantines the choice. Also deferred:
image distribution (you pull public images by hand), and any notion of pools or reuse —
one create equals one cold container, measured honestly, until plan 11.
