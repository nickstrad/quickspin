# 13 — Deep dive: a mini container runtime by hand

Depends on: 01 (independent of 05–12; can run in parallel with them).

## Summary

Build `quickspin-mini`: a few hundred lines of Go that create a "container" with no
Docker involved — clone a process into new namespaces, set up a root filesystem with
`pivot_root`, mount `/proc`, apply cgroup v2 limits, drop capabilities, and run a
command inside. It runs only inside the Lima VM, is never wired into the platform, and
exists purely to replace "Docker does isolation" with "I know which five kernel
features Docker composes, because I called them."

## Industry context

This is the layer where platform companies actually differentiate and hire: runc is
"just" these syscalls productionized; gVisor and Firecracker are answers to the
question "which of these boundaries is too weak?" — a question you can only evaluate
after building the weak version. The exercise is a rite of passage with public
ancestry (Liz Rice's "containers from scratch"); the twist here is tying each syscall
back to behavior you already observed through Docker in plans 02–03 (e.g. the
`PidsLimit` fork-bomb test becomes *your* `pids.max` write).

## What you'll learn

- Namespaces concretely: `CLONE_NEWPID/NEWNS/NEWUTS/NEWIPC/NEWNET`, and what each one
  actually hides (PID 1 illusion, mount table privacy, hostname, no network).
- Filesystem containment: why `pivot_root` (not `chroot`), unpacking an image rootfs
  (`docker export` of alpine) into a directory.
- cgroup v2 by hand: create the group, write `memory.max` / `pids.max` / `cpu.max`,
  put the child's PID in `cgroup.procs`, watch the kernel enforce it.
- Capabilities: what root inside the sandbox can still do, and dropping the set.
- Go-specific mechanics: `syscall.SysProcAttr`, the re-exec pattern (`/proc/self/exe`)
  for the parent/child split.

## Design and interfaces

```text
quickspin-mini run \
  --rootfs /tmp/alpine-rootfs \
  --memory 64m --pids 32 --cpu 0.5 \
  --hostname sandbox \
  -- /bin/sh -c 'command'
```

Committed decisions: cgroup v2 only (what the Lima Ubuntu runs); no network setup —
new netns left empty, which *is* the `AllowNetwork: false` behavior from plan 03; no
image pulling — a `hack/make-rootfs.sh` produces the rootfs via `docker export`; runs
as root inside the VM (rootless/userns is stretch, noted below).

## Tasks

1. `hack/make-rootfs.sh` + the re-exec skeleton (parent clones with namespace flags,
   child sets up and execs).
2. Hostname, mount namespace, `pivot_root`, fresh `/proc` mount.
3. cgroup setup/teardown (create dir, write limits, add PID, remove on exit).
4. Capability dropping.
5. `hack/validate-13.sh` (this plan is validated by script, not `go test` — the
   behaviors are cross-process and root-only).
6. Write `docs/reference/what-docker-does.md`: a table mapping each plan 02–03 Docker
   behavior to the syscall(s) you now invoke yourself.

## Definition of done

`hack/validate-13.sh` runs inside the VM as root, exits 0, printing a check per claim:

- Inside: `ps aux` shows the command as PID 1 with no host processes; `hostname` is
  `sandbox`; `/` is the alpine rootfs; host paths (e.g. the real `/home`) unreachable.
- Outside: host mount table is unchanged after exit (mount ns didn't leak).
- Network: `ping -c1 1.1.1.1` fails inside (empty netns).
- Memory: allocating past `--memory 64m` gets the child killed; the VM stays healthy.
- Pids: fork bomb caps at `--pids 32`.
- Cleanup: after exit, the cgroup directory is removed and no stray processes remain.
- Failure path: a bogus `--rootfs` produces a clean error, not a half-set-up mess.

Deliberately untested/out of scope: rootless mode (user namespaces + uid maps — listed
as the stretch goal), seccomp filters, veth/bridge networking, overlayfs layering.
Each is one more rung on the same ladder; note in the closing which you attempted.

## Solo-developer tradeoffs

runc handles dozens of edge cases per syscall (mount propagation, console handling,
checkpoint hooks); your mini-runtime handles the happy path of five features and that
is the point — it's a flashlight, not a product, and it is deliberately never plugged
into the control plane (Docker/Firecracker remain the real backends). The payoff is
disproportionate for interviews at E2B/Daytona/Modal-type companies: "walk me through
what happens when a container starts" answered from personal syscalls beats any amount
of Docker-flag fluency.
