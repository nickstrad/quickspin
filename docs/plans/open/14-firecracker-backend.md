# 14 — Deep dive: a Firecracker microVM backend

Depends on: 07, 13 recommended first.

## Summary

The capstone taste of how the leaders actually run untrusted code: boot Firecracker
microVMs inside the Lima VM (nested virtualization), get the `quickspin-guest` binary
running inside them, and implement enough of a second `Runtime` backend
(`firecracker`) that a sandbox created with `--backend firecracker` supports create /
exec-via-guest / files / destroy through the same control plane and SDKs. Snapshots,
warm pools, and production hardening are explicitly out of scope — this plan buys the
mental model and the resume line, not parity with Docker.

## Industry context

Firecracker is the reference answer to "containers share the host kernel" — AWS built
it for Lambda; E2B, and Vercel Sandbox run on it; Fly runs microVMs for the same
reason. The architectural payoff of plan 07 lands here: because workload operations go
through the guest agent over the network rather than through `docker exec`, a VM that
merely (a) boots and (b) has a reachable guest is already a full Quickspin sandbox.
What changes underneath is everything plan 13 made visible: instead of namespaces
sharing your kernel, each sandbox gets its own kernel behind KVM, and the isolation
boundary moves from "syscall filtering" to "virtual hardware."

## What you'll learn

- KVM at arm's length: `/dev/kvm`, why nested virt works in Lima (Apple Silicon
  exposes it via Virtualization.framework), and checking for it.
- The Firecracker control model: an API socket per VM; configure boot source, drives,
  network; `InstanceStart` — driven from Go via its API (the SDK or plain HTTP).
- Building a guest world: an uncompressed kernel image, a rootfs ext4 built from the
  same alpine export as plan 13, and a tiny init that brings up networking and execs
  `quickspin-guest` — closing the loop on why the guest is a static binary.
- MicroVM networking: one tap device per VM, addresses, and why platforms obsess over
  this part.
- An honest boot-time measurement to place next to plan 11's numbers.

## Design and interfaces

`Spec` gains `Backend string` (`"docker"` default, `"firecracker"`); the control plane
routes create/destroy to the chosen backend and the guest client needs only an address
— unchanged from plan 07.

```go
// internal/runtime/firecracker — implements the same Runtime interface for
// lifecycle; workload ops arrive via the guest as with Docker.
// Per-VM artifacts: api socket, tap device, rootfs copy, firecracker process.
// Destroy kills the process and removes all four; idempotent like plan 02.
```

Committed decisions: the control plane runs **inside** the Lima VM for this plan
(Firecracker needs /dev/kvm and tap devices; `make build-linux` from plan 01 pays off);
rootfs is copied per-VM (no CoW — slow and honest); a fixed /30 per VM from a small
allocator — the allocator is the concurrency reference's category-3 shape (allocate and
mark in one locked helper, never check-then-take) with its own race test, since
concurrent creates are the norm once the reconciler drives this backend; exec/files/limits inside the VM are whatever the guest + kernel defaults do
— cgroup-equivalent limits inside the VM are out of scope (the VM's memory size is the
real limit and the interesting difference to write up).

## Tasks

1. `hack/setup-firecracker.sh`: verify `/dev/kvm` in Lima, fetch the firecracker
   binary + a CI kernel image, build the rootfs (with guest + init) via script. All
   artifact scripts take `ARCH` (per plan 01's convention) even though this plan only
   exercises arm64 — plan 16 reruns them for amd64 unchanged.
2. Boot one VM by hand (script, no Go) to a shell — the "hello kernel" moment.
3. Init + guest wiring: VM boots straight into a serving guest; curl it from the VM.
4. Implement the `firecracker` Runtime backend (create/inspect/list/destroy + guest
   address); tap/IP allocator.
5. Route `Backend` through plane, spec validation, and one SDK field.
6. `hack/validate-14.sh` + boot-time measurement into `docs/reference/demo-latency.md`.

## Definition of done

`hack/validate-14.sh` runs inside the Lima VM, exits 0, checking:

- Environment: `/dev/kvm` present; firecracker binary runs.
- Via the control plane API with `"backend": "firecracker"`: create reaches `running`
  (meaning guest `/healthz` answered from inside the microVM); exec returns correct
  streams/exit code; file round-trip works; destroy removes the VM process, tap
  device, socket, and rootfs copy; destroy twice succeeds.
- Isolation smoke check: `uname -r` inside the sandbox differs from the Lima kernel
  when the CI kernel version differs, and the Docker socket / control-plane DB are
  unreachable from inside.
- Crash check: `kill -9` the firecracker process; the reconciler marks the sandbox
  `failed` and cleans up the leftovers.
- Boot-to-guest-healthy time is measured and recorded (target: observe the order of
  magnitude, not hit a number).
- The plan 12 demo harness completes one task with `backend: firecracker` — the full
  stack, SDK to microVM.

Deliberately untested/out of scope: VM snapshots/resume (E2B's crown jewel — future
plan set), jailer hardening, memory ballooning, CoW rootfs (overlay/reflink), running
many VMs, egress control per VM.

## Solo-developer tradeoffs

Production Firecracker stacks run the jailer, device-model hardening, snapshot trees,
and CoW storage — multi-engineer territory. One unjailed VM with a copied rootfs is
weekend territory and still crosses the line that matters: your platform demonstrably
runs the same workload API on shared-kernel *and* separate-kernel isolation, chosen per
sandbox by a field in the spec. That sentence — with the latency table to back it — is
the strongest artifact this whole plan set produces for platform-infrastructure
interviews.
