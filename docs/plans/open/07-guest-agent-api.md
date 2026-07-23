# 07 — Guest agent: the in-sandbox API

Depends on: 06.

## Summary

Build `quickspin-guest`: a small static Go binary injected into every sandbox at
creation, exposing an HTTP API *from inside* the sandbox — exec (now with streaming
output), file read/write/list, and health. The control plane proxies
`/v1/sandboxes/{id}/...` workload calls to the guest instead of using Docker exec and
the tar copy API. The `Runtime` interface keeps lifecycle (create/destroy); workload
operations move to the guest client. This is the architectural move that makes plan 14
(Firecracker) possible, because a microVM has no `docker exec`.

## Industry context

This is exactly how E2B works: their `envd` daemon runs inside every sandbox and serves
the exec/filesystem API; Daytona runs an in-workspace agent; Fly Machines run init +
agent inside the VM. The pattern exists because runtime-mediated exec is a Docker-ism —
the moment you want microVMs, bare metal, or lower latency, the only universal
interface is "a process inside the workload listening on a socket." You are also
building your first two-binary distributed system: control plane and guest version-skew
is a real design constraint from here on.

## What you'll learn

- Designing a minimal internal API and owning both sides of it.
- Streaming HTTP in Go: chunked responses, `http.Flusher`, framing stdout/stderr/exit
  as typed lines (simplest: ndjson events `{"stream":"stdout","data":...}`,
  `{"exit":17}`).
- Process supervision inside a container: the guest as PID 1's child, zombie reaping,
  signal forwarding.
- Readiness semantics: "container running" vs "guest answering" are different states —
  the control plane's `running` now means the latter.
- Static binaries and injection via bind-mount.

## Design and interfaces

Guest HTTP surface (listens inside the sandbox, e.g. `:8321`):

```text
GET  /healthz                    liveness + guest version
POST /exec                       body: {cmd, env, workdir, timeout_s}
                                 response: ndjson stream of stdout/stderr/exit events
PUT  /files?path=&mode=          write (body = content)
GET  /files?path=                read
GET  /dir?path=                  list
```

Committed decisions:

- Injection: bind-mount the cross-compiled guest binary read-only into the container
  and make it the entrypoint (replacing `sleep infinity`). No custom images required —
  any base image the user picks still works.
- Reachability: publish the guest port to the Lima VM / localhost via Docker port
  mapping; the control plane records the mapped address per sandbox. (Firecracker will
  swap this for vsock/tap behind the same client interface.)
- The control plane talks to guests through a `guest.Client` Go interface mirroring the
  surface above; handlers depend on that interface, not on `Runtime`, for workload ops.
- First application of the error reference's process-boundary rule: a guest-side Go
  error never crosses the wire. The guest serializes the shared `httpapi.ErrorResponse`
  DTO; `guest.Client` logs what the DTO said and mints a fresh error of its own package
  type rather than pretending to wrap an error it does not have. Guest and plane both
  reuse `internal/httpapi` — the DTO is wire format owned by neither side.
- Public API change: `POST /v1/sandboxes/{id}/exec` gains `?stream=true` returning the
  ndjson stream; the buffered mode remains for SDK simplicity.
- `/healthz` reports the guest's version; the control plane logs a warning on skew with
  itself.

## Tasks

1. `cmd/quickspin-guest`: HTTP server, exec with streaming + kill-on-disconnect,
   files endpoints reusing plan 04's path-validation helpers.
2. Runtime change: bind-mount + entrypoint + port mapping; store guest address.
3. Readiness: after container start, poll `/healthz` before marking `running`
   (reconciler-driven, with a bounded window before `failed`).
4. `guest.Client` in the control plane; switch workload handlers to it.
5. Streaming passthrough on the public exec endpoint.
6. Delete the now-dead Docker exec/copy paths from the runtime (report the removal).

## Definition of done

Guest tests, no Docker needed — run the guest as a local process against `httptest`
(`make test`):

- `TestGuestExecStreamsInOrder` — interleaved stdout/stderr arrive as ordered events
  ending with an exit event.
- `TestGuestExecKillsOnClientDisconnect` — closing the response body kills the child.
- `TestGuestFilesRoundTrip`, `TestGuestRejectsTraversal` (reused table).

Integration (`make test-docker`):

- `TestSandboxRunningMeansGuestHealthy` — `running` state implies `/healthz` was seen.
- `TestGuestNeverReadyMarksFailed` — break injection deliberately (e.g. bad entrypoint);
  sandbox lands in `failed` within the readiness window, container cleaned up.
- `TestStreamedExecThroughControlPlane` — end-to-end: public API `?stream=true`
  delivers events live (assert first event arrives before the command finishes).
- `TestEndToEndStillPassesWithGuestBackend` — plan 05's validate script (create, exec,
  files, destroy) passes unmodified against the guest-backed plane, proving the public
  contract survived an internal rewrite.

Deliberately untested: guest auto-upgrade, mutual auth between plane and guest (noted
in plan 08's threat model), stdin/PTY.

## Solo-developer tradeoffs

E2B's envd speaks gRPC with generated clients and supports PTYs, port forwarding, and
file watching; you ship one ndjson-over-HTTP binary because it is debuggable with curl
and has zero codegen overhead. The plane→guest link is unauthenticated plaintext on a
local interface — acceptable on a single trusted host, indefensible multi-tenant; plan
08 records this in the threat model rather than fixing it, which is itself the
commercial-vs-learning tradeoff made explicit.
