# 03 — Exec, resource limits, and cancellation

Depends on: 02.

## Summary

Make sandboxes useful: run commands inside them with separated stdout/stderr and a real
exit code, enforce CPU / memory / process-count limits at creation, control outbound
network access, and make `context.Context` cancellation actually kill the process in the
container — not just abandon the Go call.

## Industry context

This is the workhorse endpoint of every agent platform — E2B's `sandbox.commands.run`,
Vercel Sandbox's `runCommand`, Modal's function exec. Two industry behaviors are worth
copying exactly: **exit codes and streams are sacred** (agents branch on them; merging
stdout/stderr or faking exit codes breaks real harnesses), and **cancellation must reach
the process** (a platform that lets a `while true` spin after the client gave up leaks
money; that is why every serious platform enforces deadlines server-side). Resource
limits are the first line of multi-tenant defense: cgroups, which is what Docker's
flags configure, are the same mechanism Kubernetes and Firecracker jailers use.

## What you'll learn

- Docker exec semantics and the stream-demultiplexing wire format (stdout/stderr arrive
  multiplexed on one connection; you split them).
- What `--memory`, `--cpus`, and `--pids-limit` actually set in cgroup v2, verified
  from inside the VM (`/sys/fs/cgroup/...`), not taken on faith.
- Context plumbing under pressure: cancelling a blocked read vs killing the remote
  process are different problems.
- Why a fork bomb is the canonical test of `PidsLimit`.

## Design and interfaces

Extend `internal/runtime`:

```go
type Spec struct {
    Image        string
    Env          map[string]string
    CPULimit     float64 // cores, e.g. 0.5
    MemoryLimit  int64   // bytes
    PidsLimit    int64
    AllowNetwork bool    // false => no outbound network (Docker "none" network)
}

type ExecResult struct {
    ExitCode int
    Stdout   []byte // buffered in this plan; streaming is a later, explicit change
    Stderr   []byte
}

var ErrExecTimeout = errors.New("exec deadline exceeded")

type Runtime interface {
    // ...plan 02 methods...
    // Exec runs cmd in the sandbox. When ctx is cancelled or its deadline passes,
    // the process inside the container is killed and ErrExecTimeout (or ctx.Err())
    // is returned.
    Exec(ctx context.Context, id string, cmd []string, opts ExecOpts) (ExecResult, error)
}

type ExecOpts struct {
    Env     map[string]string
    WorkDir string
    Timeout time.Duration // 0 => a documented default, not "forever"
}
```

Committed decision: output is **buffered** with a size cap (e.g. 1 MiB per stream,
truncation flagged in the result). Streaming is deliberately deferred to the guest-API
plan (07) so it is built once, in the right place.

## Tasks

1. Extend `Spec`, map limits to Docker `HostConfig`, and reject nonsense values. Per
   the code-structure reference, both halves are pure functions with table tests:
   `validateSpec(Spec) error` and `specToHostConfig(Spec) container.HostConfig` — the
   field-by-field translation is exactly the "struct-to-struct merge that silently
   drops a field" bug class the reference warns about, and the daemon is not needed to
   pin it.
2. Implement `Exec` with stream demultiplexing and exit-code retrieval.
3. Implement kill-on-cancel: watch `ctx.Done()`, kill the exec'd process, reap.
4. Wire `quickspin sandbox exec <id> -- <cmd...>` with `--timeout`.
5. Write the tests below.

## Definition of done

Pure-function tests under `make test`:

- `TestValidateSpecRejectsNonsense` — negative/zero limits, empty image (table).
- `TestSpecToHostConfigMapsEveryLimit` — each `Spec` field lands in the right
  `HostConfig` field; a case with all fields set pins the full list.

Required tests under `make test-docker`:

- `TestExecSeparatesStreamsAndExitCode` — a command writing to both streams and exiting
  17 comes back intact.
- `TestExecKillsProcessOnContextCancel` — cancel a `sleep 300`; the call returns
  promptly AND `ps` inside the container shows the sleep is gone (assert both).
- `TestExecTimeout` — `Timeout: 1s` on a long command returns `ErrExecTimeout`.
- `TestMemoryLimitEnforced` — a process allocating past the limit is OOM-killed
  (exit 137), the sandbox survives.
- `TestPidsLimitStopsForkBomb` — a fork bomb in a `PidsLimit: 64` sandbox fails to
  spawn and the VM stays responsive.
- `TestNetworkDenied` — with `AllowNetwork: false`, an HTTP request from inside fails.
- `TestOutputTruncation` — output beyond the cap is truncated and flagged.

Plus one manual verification recorded in the plan when closing it: read the cgroup
files for a limited sandbox inside the VM and confirm the values match the Spec.

Deliberately untested: interactive/TTY sessions, stdin, disk I/O limits, streaming.

## Solo-developer tradeoffs

Commercial platforms stream output over WebSocket/gRPC from day one and meter
CPU-seconds for billing. Buffered-with-cap is far simpler, honest about its limits, and
sufficient for an agent loop that inspects results between steps. Network policy here
is binary (on/off); real platforms do egress allowlists, DNS filtering, and metered
bandwidth — that nuance is deferred to the isolation capstones.
