# quickspin

Quickspin is an **agent sandbox platform**: infrastructure that gives an AI coding
agent a disposable, isolated machine to run code in.

An agent that writes code needs somewhere to run it. Running agent-authored commands on
the host is unsafe and unreproducible, so the sandbox model — an ephemeral,
resource-capped, network-restricted environment per task — has become the standard,
used by platforms like E2B, Modal, and Vercel Sandbox. Quickspin implements that model
around a backend-neutral runtime contract: mandatory cgroup limits, network off by
default, idempotent lifecycle operations, and a conformance suite every backend must
pass, so container and microVM isolation sit behind the same API.

> **Status: early.** What exists today is the sandbox runtime layer and a CLI over it —
> create, inspect, list, exec, copy files into and out of, list paths in, remove paths
> from, and destroy Docker-backed sandboxes. Sandboxes have cgroup v2 CPU/memory/pids
> limits and network off by default; file operations enforce absolute-path validation
> and bounded transfers. The control plane, guest agent, SDKs, and the Firecracker/Kata
> backends are planned, not built. See [Roadmap](#roadmap) below.

## Architecture

```
cmd/quickspin           main: wires logger -> runtime -> cobra command tree
  |
internal/cli            cobra commands, table/json/yaml rendering
  |
internal/runtime        Runtime interface (backend-neutral)
  |-- DockerRuntime     the one implementation today (moby client)
  '-- runtimetest       Fake runtime + conformance suite every backend must pass
```

The central abstraction is `runtime.Runtime` ([internal/runtime/runtime.go](internal/runtime/runtime.go)):

```go
type Runtime interface {
	Create(ctx context.Context, spec Spec) (Info, error)
	Inspect(ctx context.Context, id string) (Info, error)
	List(ctx context.Context) ([]Info, error)
	Destroy(ctx context.Context, id string) error // idempotent: unknown id returns nil
	Exec(ctx context.Context, id string, cmd []string, opts ExecOpts) (ExecResult, error)
}
```

Everything except `DockerRuntime` is backend-neutral. The CLI never imports the Docker client;
it is handed a `Runtime` and knows nothing about how a sandbox is isolated. That is what
makes the planned Firecracker and Kata backends drop-in replacements rather than rewrites.

- **Sandbox IDs are Quickspin's, not Docker's.** A sandbox is `sbx_<uuid>`, stored as a
  container label; the Docker container ID never leaks into the CLI surface.
- **Ownership is label-based.** Only containers carrying `quickspin.managed=true` are
  visible to `list` or eligible for `destroy`, so Quickspin can share a daemon with
  anything else without touching it.
- **Limits are mandatory, not optional.** `Spec.Validate` rejects a zero CPU, memory, or
  pids limit rather than reading it as "unlimited", because Docker reads zero as
  unlimited and a forgotten field would silently produce an uncapped sandbox. The CLI
  supplies defaults so every sandbox lands in a real cgroup.
- **Exec output is buffered and capped** at 1 MiB per stream, with per-stream truncation
  flags — an unbounded write from inside the sandbox must not exhaust the host through
  the very call meant to contain it. Streaming is deferred to the guest-agent plan.

## Requirements

| Tool | Why |
| --- | --- |
| Go 1.26+ | building and testing the module (`go.mod` pins 1.26.4) |
| A Docker daemon | the only runtime backend that exists today |
| [Lima](https://lima-vm.io/) (`limactl`) | macOS: provides the Linux VM the daemon runs in |
| `docker` CLI | managing the Docker context that points at the VM |
| `jq` | used by `make test-docker` |
| Node.js + npm | only for the local docs reader (`make docs`) |

## Install

```sh
git clone https://github.com/nickstrad/quickspin.git
cd quickspin
make build          # -> bin/quickspin
```

Or install straight onto your `PATH`:

```sh
go install github.com/nickstrad/quickspin/cmd/quickspin@latest
```

`make build-linux` cross-compiles a static `linux/arm64` binary into
`bin/linux-arm64/` (override with `LINUX_ARCH=amd64`) — useful for running the binary
inside the VM rather than on the host.

## Set up a Docker daemon (macOS)

Quickspin talks to a daemon over the standard `DOCKER_HOST`/context resolution, so any
daemon works. The repo ships a Lima VM definition so the development daemon is disposable
and isolated from whatever else you run:

```sh
make env-create     # start the `quickspin` Lima VM, create the docker context, select it
make env-validate   # verify the VM, the daemon, and a cross-compiled binary all work
make env-cleanup    # stop/delete the VM and remove the docker context
```

`make lima-vm-shell` drops you inside the VM.

## CLI

The binary is `quickspin`; all sandbox verbs live under `quickspin sandbox`.

```
quickspin
  sandbox
    create IMAGE            create a sandbox from an image
    list                    list managed sandboxes
    inspect ID              show one sandbox
    exec ID -- CMD [ARG..]  run a command inside a sandbox
    destroy ID              destroy a sandbox
```

Persistent flags, valid on every command:

| Flag | Values | Default | Effect |
| --- | --- | --- | --- |
| `-o`, `--output` | `table`, `json`, `yaml` | `table` | output format on stdout |
| `--log-level` | `debug`, `info`, `warn`, `error` | `info` | structured log verbosity on stderr |

Logs go to **stderr** and command output to **stdout**, so `-o json` stays pipeable into
`jq` at any log level.

`sandbox create` flags:

| Flag | Default | Effect |
| --- | --- | --- |
| `-e`, `--env KEY=VALUE` | — | environment variable baked into the container; repeatable |
| `--cpus` | `1.0` | CPU cores, fractional allowed (`cpu.max`) |
| `-m`, `--memory` | `512m` | memory limit, binary suffixes `b`/`k`/`m`/`g` (`memory.max`) |
| `--pids-limit` | `256` | maximum processes (`pids.max`) |
| `--allow-network` | `false` | sandboxes get **no network** unless this is set |

`sandbox exec` flags:

| Flag | Default | Effect |
| --- | --- | --- |
| `-e`, `--env KEY=VALUE` | — | environment for this command only, layered over the container's |
| `-w`, `--workdir` | image's own | working directory for this command |
| `--timeout` | `30s` | how long the command may run before it is cancelled |

### Examples

Create a sandbox and read back its ID:

```sh
$ quickspin sandbox create alpine:3.20
ID                                        STATE    CREATED AT
sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b  running  2026-07-27T10:14:02Z
```

Create with environment variables and explicit limits, network allowed:

```sh
quickspin sandbox create python:3.12-slim \
  -e WORKSPACE=/srv/agent \
  --cpus 0.5 -m 256m --pids-limit 128 \
  --allow-network
```

Run a command inside it. Everything after `--` belongs to the sandbox, so its own flags
are passed through rather than parsed by Quickspin:

```sh
$ quickspin sandbox exec sbx_9f1c... -- python -c 'print(2 + 2)'
4

$ quickspin sandbox exec sbx_9f1c... -w /srv/agent -- ls -la
$ quickspin sandbox exec sbx_9f1c... --timeout 5m -- pytest -q
```

The limits are real kernel cgroup v2 files, and `exec` is how you see that:

```sh
$ quickspin sandbox exec sbx_9f1c... -- cat /sys/fs/cgroup/memory.max
268435456
```

A non-zero exit from the sandbox command is reported as an error. It is **not** yet
propagated as Quickspin's own exit status — `$?` is `1` for any failure, so a command
that exited 137 (OOM kill) is currently indistinguishable from a Quickspin failure.

List sandboxes, oldest first:

```sh
$ quickspin sandbox list
ID                                        STATE    CREATED AT
sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b  running  2026-07-27T10:14:02Z
sbx_3b8d7c11-2a4e-49f0-8d31-77c9ab0e5512  running  2026-07-27T10:15:40Z
```

Machine-readable output:

```sh
$ quickspin sandbox inspect sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b -o json
{
  "id": "sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b",
  "state": "running",
  "created_at": "2026-07-27T10:14:02Z"
}

$ quickspin sandbox list -o json | jq -r '.[] | select(.state == "running") | .id'
```

Destroy one, or sweep them all:

```sh
$ quickspin sandbox destroy sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b
ID                                        STATUS
sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b  destroyed

$ quickspin sandbox list -o json | jq -r '.[].id' | xargs -n1 quickspin sandbox destroy
```

Destroying an unknown ID succeeds silently — cleanup is retry-safe by design, so a
crashed reaper can re-run without special-casing already-gone sandboxes.

## Development

```sh
make build      # build bin/quickspin
make run ARGS="sandbox list"  # build and run in one step
make fmt        # gofmt
make vet        # go vet
make tidy       # sync go.mod / go.sum
make test       # every Go test; needs no Docker
make clean      # remove bin/
```

`make fmt` and `make vet` should both be clean before a change is done.

### Documentation

All project documentation lives in [`docs/`](docs/) as MDX, starting at
[`docs/index.mdx`](docs/index.mdx). A local reader renders it with search and navigation:

```sh
make docs         # dev server
make docs-build   # type-check and produce a static build
```

`docs/plans/open/` holds proposed and in-progress work; `docs/plans/closed/` is history;
`docs/reference/` is forward-looking architecture and design notes, not a spec for
current behavior.

## Tests

There are two lanes, deliberately kept apart so the fast one stays fast.

**Daemon-free (`make test`)** — pure helpers, the Docker adapter driven against an
`httptest.Server` standing in for the daemon, and CLI tests driven by
`runtimetest.Fake`. Runs on a machine with no Docker installed at all; the live suite
reports itself skipped.

```sh
make test                                        # everything
go test ./internal/runtime/                      # one package
go test ./internal/runtime/ -run TestEnvToArgs -v # one test
go test ./internal/runtime/ -count=1             # bypass the result cache
```

**Live Docker (`make test-docker`)** — the suite that needs a real daemon. It owns a
*separate* `quickspin-runtime-test` Lima VM, passes that VM's socket as `DOCKER_HOST`,
and never touches your active Docker context or the development `quickspin` VM. It also
runs a CLI smoke test against the built binary.

```sh
make test-docker            # setup (if needed) + run the live suite
make test-docker-setup      # create the test VM only
make test-docker-clean      # remove containers a failed run leaked
make test-docker-teardown   # delete the test VM
```

The live suite refuses to start if the test VM already holds Quickspin containers: on a
daemon only tests use, leftovers mean a previous run leaked, and quietly cleaning them
would erase the signal. `make test-docker-clean` is the explicit remedy.

`internal/runtime/runtimetest` holds the **conformance suite** — the behavioral contract
(ID format, idempotent destroy, error classification) that every `Runtime`
implementation must satisfy. Both `DockerRuntime` and the in-memory `Fake` run against
it, which is how a future Firecracker or Kata backend proves it behaves the same.
Background: [`docs/reference/runtime-backend-testing.mdx`](docs/reference/runtime-backend-testing.mdx)
and [`docs/reference/docker-test-architecture-explained.mdx`](docs/reference/docker-test-architecture-explained.mdx).

## Roadmap

The plans in [`docs/plans/open/`](docs/plans/open/) sequence the platform. Abbreviated:

| Plans | Track |
| --- | --- |
| 01–02 | Lima lab environment; the `Runtime` interface and Docker backend *(done)* |
| 03–04 | exec with real exit codes, cgroup limits, network policy; filesystem API *(done)* |
| 05–08 | HTTP control plane, reconciler and leases, in-sandbox guest agent, auth/tenancy/quotas |
| 09–12 | TypeScript and Python SDKs, snapshots, an agent-harness capstone demo |
| 15–18 | production: Postgres store, live Docker-backed host, control-plane/worker split with heartbeats and a failure-injection suite, fleet provisioning and observability |
| 13–14 | isolation internals: a minimal container runtime on raw kernel primitives; Firecracker microVMs as a second backend, ending with the prod cutover |
| 19–21 | compute pools, EC2/DigitalOcean providers, Kubernetes + Kata as a third backend |
| 22–23 | agent workflows: git-capable sandboxes with secret injection, per-agent storage |

Each plan states its own dependencies; a plan existing in `open/` does not mean it is
being worked on.
