# 01 — Linux lab environment

Depends on: nothing.

## Summary

Set up the Linux environment every later plan runs against: a Lima VM on macOS running
Docker, reachable from the Mac both over the Docker API socket and over SSH. Add a
validation script that proves the environment works, and a Makefile target that
cross-compiles Quickspin's Go binaries for `linux/arm64` so they can run inside the VM.

Nothing platform-shaped is built yet. The deliverable is a reproducible lab plus the
muscle memory of moving between the Mac (where you edit and build) and the Linux kernel
(where sandboxes actually live).

## Industry context

Every sandbox platform you admire — E2B, Daytona, Modal, Vercel Sandbox — ultimately
runs workloads on Linux hosts, because containers and microVMs are Linux kernel
features (namespaces, cgroups, KVM). Their engineers develop on laptops but target
fleet hosts; the Docker-socket-over-VM arrangement you build here is a one-machine
miniature of that split. Docker Desktop hides this VM from you; Lima gives you the same
thing with the hood open, which matters from plan 13 onward when you need to touch the
kernel directly.

## What you'll learn

- Where Linux actually is on a Mac dev setup, and why `DOCKER_HOST` is the seam.
- Lima basics: VM lifecycle, port/socket forwarding, `limactl shell`.
- Go cross-compilation (`GOOS`/`GOARCH`) and why static binaries matter for dropping
  tools into minimal containers/VMs.
- Writing environment checks as scripts rather than wiki pages.

## Design and interfaces

No Go interfaces. The contracts are environmental:

- A Lima instance (suggested name `quickspin`) running Ubuntu LTS with Docker installed,
  its Docker socket forwarded to the Mac.
- `DOCKER_HOST` documented in the README (or a `hack/env.sh` to source).
- New Makefile targets:
  - `make build-linux` — cross-compiles `cmd/...` to `bin/linux-$(ARCH)/`, with
    `ARCH ?= arm64`. Parameterizing the arch now is deliberate: the production host in
    plan 16 is x86_64, and every artifact script written later (guest binary, rootfs,
    Firecracker kernel) should take the arch as a variable from birth rather than being
    un-hardcoded under deadline pressure.
  - `make validate-env` — runs `hack/validate-01.sh`.

## Tasks

1. Install Lima (`brew install lima`), create the VM from a checked-in
   `lima/quickspin.yaml` template so the lab is reproducible from the repo.
2. Install Docker inside the VM (via the template's provisioning script), forward the
   socket, verify `docker ps` works from the Mac.
3. Add `make build-linux` and confirm a hello-world binary built on the Mac runs inside
   the VM via `limactl shell`.
4. Write `hack/validate-01.sh`.
5. Document the setup and daily workflow (start VM, source env, run tests) in the README.

## Definition of done

`hack/validate-01.sh` exits 0 and prints a check line for each of:

- `limactl list` shows the `quickspin` instance running.
- `docker version` from the Mac reports a Linux server.
- `docker run --rm alpine uname -s` prints `Linux`.
- A Go binary cross-compiled by `make build-linux` executes successfully inside the VM.
- SSH into the VM works non-interactively (`limactl shell quickspin true`).

Negative check: the script fails with a clear message (not a stack trace) when the VM is
stopped.

Deliberately untested: x86_64 hosts, multiple VM instances, VM survival across macOS
reboots.

## Solo-developer tradeoffs

A commercial platform provisions hosts with Terraform/cloud-init across a fleet and
never depends on one laptop VM. One Lima VM costs nothing, keeps the edit-test loop
local, and still gives you a real kernel. The cost: nothing here teaches multi-host
networking, and arm64 (Apple Silicon) means your images differ from the x86_64 most
clouds run — acceptable until plan 14, where Firecracker on arm64 works fine anyway.
