# 17 — Control plane / worker split and host heartbeats

Depends on: 15, 16 (07's guest architecture assumed).

## Summary

Break the monolith: sandbox execution moves into `quickspin-workerd`, a daemon that
runs on each host, exposes the `Runtime` operations over an authenticated internal
HTTP API, registers itself with the control plane, and heartbeats. The plane —
which can now run anywhere, since it no longer needs `/dev/kvm` — keeps a `hosts`
table, places new sandboxes on a healthy host with capacity, and (via the reconciler)
declares hosts dead when heartbeats stop, failing their sandboxes. This is the plan
that makes "monitor live cloud hosts" a property of the system rather than of your
SSH habits.

## Industry context

This is the canonical control-plane/data-plane split: Kubernetes' apiserver/kubelet,
Nomad's server/client, and (per their engineering posts) how E2B and Fly place microVMs
on host fleets. The mechanisms you're building are the textbook trio — registration,
heartbeats with a dead-host threshold, and capacity-aware placement — plus the rule
that makes distributed state survivable: the database's desired state is authoritative,
workers hold no durable truth, and a dead worker's sandboxes are *failed, not
migrated* (sandboxes are stateful; honest platforms kill them and let clients recreate
from snapshots — exactly why plan 11 exists).

## What you'll learn

- Designing an internal service-to-service API and its auth (static bearer token per
  host from the plane's admin CLI; mTLS noted as the industry answer and deferred).
- Failure detection theory made concrete: heartbeat interval vs death threshold,
  why "no heartbeat" means *unknown* not *dead*, and what a flapping host does to
  naive logic.
- Placement as a pure function: `place(spec, []Host) (hostID, error)` — trivially
  testable, no I/O (the code-structure reference doc's pattern applied for real). But
  the pure function only *chooses*; the *reservation* is a check-then-act hazard — two
  concurrent creates can both pick a host's last slot. Committing the placement re-checks
  capacity inside the insert's transaction (`FOR UPDATE` on the host row), the same
  fold-the-check-into-the-reserve rule as plan 08's quota, one layer down.
- Both process boundaries follow the error reference: workerd serves the shared
  `httpapi.ErrorResponse`; the plane's worker client logs the DTO and mints a
  `ControlPlaneError` (never a fake wrap), and `internal/worker` gets its own
  `WorkerError` type with the standard 30 lines.
- The reconciler generalized across two layers: converging sandboxes *and* hosts.

## Design and interfaces

```text
quickspin-workerd (per host):
  POST /register     at boot: host name, arch, backend(s), capacity (max sandboxes)
  Serves the Runtime surface over HTTP: create/inspect/list/destroy per plan 02/14
  (workload ops still go plane -> guest directly, unchanged from plan 07)
  POST /heartbeat    every 10s to the plane: running count, disk/mem headroom

Control plane:
  hosts(id, addr, arch, backend, capacity, state{ready,unready,draining,dead},
        last_heartbeat_at, registered_at)
  Placement: filter ready+backend+capacity, pick least-loaded; no host ⇒ create
  fails fast with a typed "no_capacity" error (backpressure, not queueing — queueing
  is future work and saying so is the design decision).
  Reconciler additions: no heartbeat > 30s ⇒ unready (no new placements);
  > 2m ⇒ dead ⇒ its sandboxes -> failed, events emitted.
  Admin: quickspin admin host list|drain|token ; GET /v1/status gains host summary.
  sandboxes gains host_id.
```

Committed decisions: the plane never SSHes to workers — everything flows through the
two HTTP surfaces; worker tokens are minted per host and revocable; `drain` stops
placement but leaves running sandboxes to finish or expire (the graceful-maintenance
primitive plan 18 automates); local dev runs plane + one workerd in Lima so the split
is exercised daily, not only in prod.

## Tasks

1. Extract workerd (thin HTTP shell over the existing runtime packages) + token auth.
2. Hosts table, registration, heartbeat endpoints; migration.
3. Placement function + wiring into async create; `no_capacity` error through the
   API and both SDKs.
4. Reconciler: unready/dead transitions, sandbox failing, host events.
5. Admin CLI verbs; `/v1/status` host summary.
6. Prod cutover: run workerd on the Hetzner host beside the plane (still one machine —
   plan 18 adds more); update cloud-init and deploy script for two units.

## Definition of done

Unit tests (`make test`, fake clock, fake worker client):

- `TestPlacementPrefersLeastLoaded`, `TestPlacementFiltersArchAndBackend`,
  `TestPlacementNoCapacityIsTypedError` (pure-function table tests).
- `TestMissedHeartbeatsMarkUnreadyThenDead` — clock-driven, both thresholds.
- `TestDeadHostFailsItsSandboxes` — with events, and no effect on other hosts'
  sandboxes.
- `TestFlappingHostRecovers` — heartbeat resumes before the dead threshold ⇒ ready
  again, nothing was killed.
- `TestDrainStopsPlacementKeepsRunning`.
- `TestConcurrentPlacementNeverOverfillsHost` — N concurrent creates against one
  host with capacity M admit exactly M (the placement twin of plan 08's quota-race
  test; must fail if the transactional re-check is removed).

Integration `hack/validate-17.sh` (Lima, real plane + real workerd):

- Boot both; workerd registers; sandbox lands on it and works end-to-end.
- `kill -9` workerd: within the thresholds the host goes dead, its sandbox is
  `failed` with an event, and creates now fail fast with `no_capacity`.
- Restart workerd: re-registers, placement resumes; the killed sandbox's container
  is garbage-collected (the runtime-level orphan sweep from plan 06 still works
  through the worker).

Deliberately untested: two workerds racing one sandbox ID (single-owner-by-placement
is asserted by design, not by lease — recorded as the known gap vs the reference
doc's ownership-lease stage), mTLS, placement under thousands of hosts.

## Solo-developer tradeoffs

Nomad and Kubernetes solve this with raft-replicated servers, ownership leases, and
work queues; you run one plane instance (Neon is the only HA piece) and skip leases
because single-plane placement can't double-assign. That is a real correctness
dependency on "exactly one control plane" — written down, and the first thing to fix
if this ever became multi-plane. Failing dead hosts' sandboxes instead of migrating
them is not a shortcut, though: it is what the big platforms do, and snapshots are
the recovery story.
