# 11 — Save, restore, and warm starts

Depends on: 07 (06's reconciler assumed).

## Summary

Add the "save sandboxes" half of your platform description: stop a sandbox while
preserving its filesystem as a named snapshot, create new sandboxes *from* a snapshot,
and measure what this does to startup latency. Mechanically: `docker commit`-based
snapshots owned by tenants, new lifecycle verbs (`save`, restore-by-create), and a
first honest latency benchmark (cold create vs snapshot create).

## Industry context

Persistence and fast resume are where sandbox platforms differentiate hardest. E2B
pauses sandboxes and resumes them with **memory state intact** (Firecracker VM
snapshots); Daytona persists workspaces; Modal builds images from snapshotted layers.
Your Docker-commit version preserves **filesystem only, not memory or running
processes** — a real and useful capability (an agent's installed deps and written code
survive), but a different promise than E2B's, and the plan requires naming that
difference in your API docs rather than blurring it. Filesystem-vs-memory snapshotting
is precisely the boundary where microVMs earn their keep, which sets up plan 14.

## What you'll learn

- Docker image mechanics for real: layers, commit, what balloons image size, why
  writable-layer growth matters.
- Lifecycle design when states multiply: what is `saved`? (This plan: a snapshot is an
  artifact, and the sandbox that produced it is destroyed on save — no paused state.)
- Benchmarking honestly: percentiles over means, measuring from API call to guest
  `/healthz`, separating pull/create/start/ready phases.
- Cleanliness of restored state: what leaks through a snapshot (files in `/tmp`,
  credentials the agent wrote) — a small threat-model addendum.

## Design and interfaces

```text
POST /v1/sandboxes/{id}/save   body: {"name": "deps-installed"}
     → 202; sandbox transitions stopping→saved-and-destroyed; snapshot recorded
GET  /v1/snapshots             list (tenant-scoped)
DELETE /v1/snapshots/{id}
POST /v1/sandboxes             spec gains {"snapshot": "deps-installed"} as an
                               alternative to {"image": ...} (exactly one required)
```

Schema: `snapshots(id, tenant_id, name, image_ref, size_bytes, created_at,
source_sandbox_id)`. Runtime interface gains `Snapshot(ctx, id) (imageRef, error)`;
`Spec` gains `Snapshot string`.

Committed decisions: save destroys the source sandbox (a snapshot is a checkpoint you
resume *from*, not a paused thing you resume — E2B-style pause is out of scope and the
API name says `save`, not `pause`); snapshots count against a per-tenant total-bytes
quota; guest binary is re-injected on restore so snapshots never bake in a stale guest.

## Tasks

1. Runtime `Snapshot` via commit; strip the bind-mount/entrypoint so images stay
   backend-portable.
2. Schema + endpoints + tenant scoping + byte quota (admission-time, like plan 08).
3. Reconciler: unreferenced/over-quota snapshot images get garbage-collected.
4. SDK additions (both languages, generated): `sbx.save(name)`,
   `Sandbox.create({snapshot})`, `snapshots.list/delete`.
5. `hack/bench-11.sh`: 20 iterations each of cold create vs snapshot create (with a
   heavyweight setup step, e.g. `pip install` of a few packages), reporting p50/p95
   per phase to a CSV checked into the closing notes.

## Definition of done

Integration tests (`make test-docker`):

- `TestSaveThenRestorePreservesFiles` — write file, install a package, save, create
  from snapshot, both survive; source sandbox is gone.
- `TestRestoredSandboxHasFreshGuest` — guest healthy and version-current after restore.
- `TestSaveIsRecordedBeforeDestroy` — kill the plane between commit and destroy;
  reconciler converges without losing the snapshot (crash point in `validate-06`
  style).
- `TestSnapshotTenantScoping` — tenant B cannot restore from A's snapshot (404).
- `TestSnapshotByteQuota` — over-quota save rejected with a typed code.
- `TestSnapshotGC` — deleting the DB row leads the reconciler to remove the image.

Benchmark: `hack/bench-11.sh` runs green and its CSV shows snapshot-create p50
meaningfully below cold-create-with-setup p50 (the number itself is the artifact).

Deliberately untested: memory/process state (explicitly not preserved), concurrent
saves of one sandbox, snapshot export off-host.

## Solo-developer tradeoffs

E2B's pause/resume snapshots RAM in hundreds of milliseconds via Firecracker; that is
a company-scale feature sitting on a custom hypervisor stack. `docker commit` gives you
80% of the agent-workflow value (durable environments, warm dependency caches) with one
API call, and — more importantly for your goals — forces you to articulate the
filesystem/memory distinction that makes the microVM pitch legible in interviews. The
byte quota exists because snapshots are the first thing in this system that silently
consumes disk forever; commercial platforms meter and bill it, you cap it.
