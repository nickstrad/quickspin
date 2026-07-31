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

> **Status: early.** What exists today is a local HTTP control plane backed by SQLite
> and Docker, plus a CLI client for creating, inspecting, listing, executing in, copying
> files to and from, and destroying sandboxes. Sandboxes have cgroup v2
> CPU/memory/pids limits and network off by default; file operations enforce
> absolute-path validation and bounded transfers. The guest agent, SDKs, and the
> Firecracker/Kata backends are future roadmap work. See the
> [learning roadmaps](docs/plans/) for the intended sequence.

## Architecture

```
cmd/quickspin              main: wires logger -> cobra command tree
  |
internal/cli
  |-- sandbox commands --> internal/client --> HTTP control plane
  '-- serve -------------> internal/httpapi
                               |-- internal/store    SQLite records
                               '-- internal/runtime  backend-neutral operations
                                     |-- DockerRuntime
                                     '-- runtimetest  conformance suite
```

The control plane owns sandbox records and calls `runtime.Runtime`
([internal/runtime/runtime.go](internal/runtime/runtime.go)) to realize them:

```go
type Runtime interface {
	Create(ctx context.Context, sandboxID string, spec Spec) (Info, error)
	Inspect(ctx context.Context, sandboxID string) (Info, error)
	List(ctx context.Context) ([]Info, error)
	Destroy(ctx context.Context, sandboxID string) error
	Exec(ctx context.Context, sandboxID string, cmd []string, opts ExecOpts) (ExecResult, error)
	// file operations omitted
}
```

Everything except `DockerRuntime` is backend-neutral. Only `quickspin serve` opens the
Docker runtime and SQLite store. Every `quickspin sandbox ...` command is an HTTP client,
so it can use a remote control plane without needing Docker on the client machine. That
separation also keeps future Firecracker and Kata backends behind the same server API.

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
| A Docker daemon | required on the machine running `quickspin serve`; Docker is the only runtime backend today |
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

### Starting the VM without make

`make lima-vm-create` does not just call `limactl start` — it injects the guest's
`DOCKER_HOST`:

```sh
limactl start lima/quickspin.yaml --name=quickspin \
  --set '.env.DOCKER_HOST = "unix:///run/user/'"$(id -u)"'/docker.sock"'
```

Without that flag the VM still boots and the host-side context still works, but the
Quickspin binary *inside* the VM fails with `permission denied ... /var/run/docker.sock`.
The VM runs **rootless** Docker at `/run/user/<uid>/docker.sock`; the `docker` CLI finds
it through the `rootless` context, but Go SDK clients such as Quickspin read only
`DOCKER_HOST` and otherwise fall back to the rootful socket, which does not exist here.

The value cannot live in `lima/quickspin.yaml` because Lima expands `{{.UID}}` in only
certain fields and `env` is not one of them — hence the `$(id -u)` on the command line.
Lima gives the guest user the host's UID, so the two paths match.

If you already have a VM without it, fix it in place and reboot the instance:

```sh
limactl shell quickspin -- sudo sh -c \
  "echo DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock >> /etc/environment"
limactl stop quickspin && limactl start quickspin
```

The restart is required, not cosmetic. `/etc/environment` is read by PAM once per SSH
*connection*, and Lima multiplexes every `limactl shell` over one long-lived connection
opened at boot — so a running instance keeps serving the environment it had before the
edit.

## Run the control plane and CLI

After `make env-create`, start the control plane on the host. It connects to the active
Docker context, listens on `127.0.0.1:8080`, and stores state in `control-plane.db` in
the current directory by default:

```sh
# Terminal 1
make run ARGS="serve"
```

Keep the server running and use a second terminal for client commands:

```sh
# Terminal 2
make run ARGS="sandbox list"
make run ARGS="sandbox create alpine:3.20"
```

The client defaults to `http://127.0.0.1:8080`. For another address, use `--server` for
one command or set `QUICKSPIN_SERVER` for the shell:

```sh
# Terminal 1: choose a different listener and database.
quickspin serve --port 9000 --db ./quickspin.db

# Terminal 2: either form points the client at that server.
quickspin --server http://127.0.0.1:9000 sandbox list
export QUICKSPIN_SERVER=http://127.0.0.1:9000
quickspin sandbox list
```

Stop the server with Ctrl-C. Only the server machine needs Docker; a client targeting a
remote server does not.

To run everything inside Lima, cross-compile first and open two VM shells. Lima mounts
the host home directory, so the checkout and built binary are available at the same
absolute path. Run `serve` in the first shell and client commands in the second:

```sh
make build-linux
make lima-vm-shell

# Inside Lima, shell 1:
cd /absolute/path/to/quickspin
./bin/linux-arm64/quickspin serve

# Inside Lima, shell 2:
cd /absolute/path/to/quickspin
./bin/linux-arm64/quickspin sandbox list
```

## CLI

The binary is `quickspin`; all sandbox verbs live under `quickspin sandbox`.

```
quickspin
  serve                     run the HTTP control plane
  sandbox
    create IMAGE            create a sandbox from an image
    list                    list managed sandboxes
    inspect ID              show one sandbox
    exec ID -- CMD [ARG..]  run a command inside a sandbox
    cp SOURCE DESTINATION   copy a file into or out of a sandbox
    ls ID PATH              list a path inside a sandbox
    rm ID PATH              remove a path inside a sandbox
    destroy ID              destroy a sandbox
```

Persistent flags, valid on every command:

| Flag | Values | Default | Effect |
| --- | --- | --- | --- |
| `--server` | URL | `http://127.0.0.1:8080` | control-plane address; defaults from `QUICKSPIN_SERVER` when set |
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

These examples assume `quickspin serve` is running and the client points to its
address as described above.

Create a sandbox and read back its ID:

```sh
$ quickspin sandbox create alpine:3.20
ID                                        STATE    IMAGE        CREATED AT
sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b  running  alpine:3.20  2026-07-27T10:14:02Z
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
ID                                        STATE    IMAGE        CREATED AT
sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b  running  alpine:3.20  2026-07-27T10:14:02Z
sbx_3b8d7c11-2a4e-49f0-8d31-77c9ab0e5512  running  alpine:3.20  2026-07-27T10:15:40Z
```

Machine-readable output:

```sh
$ quickspin sandbox inspect sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b -o json
{
  "id": "sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b",
  "state": "running",
  "created_at": "2026-07-27T10:14:02Z"
}

$ quickspin sandbox list -o json | jq -r '.[] | select(.state == "running") | .sandbox_id'
```

Destroy one, or sweep them all:

```sh
$ quickspin sandbox destroy sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b
ID                                        STATUS
sbx_9f1c0e3a-5c2b-4f27-9c6c-1a2d3e4f5a6b  destroyed

$ quickspin sandbox list -o json | jq -r '.[].sandbox_id' | xargs -n1 quickspin sandbox destroy
```

Destroying an unknown ID succeeds silently — cleanup is retry-safe by design, so a
crashed reaper can re-run without special-casing already-gone sandboxes.

## Development

```sh
make build              # build bin/quickspin
make run ARGS="serve"   # build and run the control plane
make run ARGS="sandbox list"  # in another terminal, run a client command
make fmt                # gofmt
make vet                # go vet
make tidy               # sync go.mod / go.sum
make test               # every Go test; needs no Docker
make clean              # remove bin/
```

`make fmt` and `make vet` should both be clean before a change is done.

### Documentation

The human-facing docs are the learning roadmaps in [`docs/plans/`](docs/plans/). A local
reader renders the open and completed roadmaps with search and navigation:

```sh
make docs         # dev server
make docs-build   # type-check and produce a static build
```

`docs/plans/open/` holds open roadmap work; `docs/plans/closed/` is completed roadmap
history. [`docs/index.mdx`](docs/index.mdx), [`docs/reader-guide.mdx`](docs/reader-guide.mdx),
and `docs/reference/` are agent-oriented authoring and architecture material, so the
reader does not include them in its navigation or search.

## Tests

There are two lanes, deliberately kept apart so the fast one stays fast.

**Daemon-free (`make test`)** — pure helpers, the Docker adapter driven against an
`httptest.Server` standing in for the daemon, control-plane handler tests with fake
stores and runtimes, and CLI tests with a fake control-plane client. Runs on a machine
with no Docker installed at all; the live suite reports itself skipped.

```sh
make test                                        # everything
go test ./internal/runtime/                      # one package
go test ./internal/runtime/ -v                   # include test and subtest names
go test ./internal/runtime/ -run TestEnvToArgs -v # one test
go test ./internal/runtime/ -run 'TestEnvToArgs/sorted' -v # one subtest
go test ./internal/runtime/ -count=1             # bypass the result cache
```

`-run` accepts a regular expression, and `/` descends into `t.Run` subtests. For a compact
list of failures from a large table-driven suite:

```sh
go test -json ./internal/runtime/ | jq -r 'select(.Action == "fail") | .Test // empty'
```

For an optional watch loop, install `entr` and run:

```sh
ls internal/runtime/*.go | entr -c go test ./internal/runtime/
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
