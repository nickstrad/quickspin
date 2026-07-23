# 06 — Reconciler, TTLs, and the event log

Depends on: 05.

## Summary

Make the control plane survive reality: a background reconciler compares desired state
(SQLite) with actual state (the runtime) and repairs drift; every sandbox gets a TTL so
abandoned sandboxes are reaped; every lifecycle transition is appended to an event log.
Create becomes asynchronous — `POST /v1/sandboxes` returns `pending` immediately and
the reconciler drives it to `running`. This is the plan where Quickspin stops being a
CRUD app and starts being infrastructure.

## Industry context

Reconciliation is *the* control-plane pattern: Kubernetes controllers, Nomad, and every
sandbox platform's janitor loops work this way — write desired state, then repeatedly
observe and converge, so crashes anywhere leave a repairable system rather than a
corrupt one. TTL reaping is universal in agent platforms (E2B kills sandboxes after a
default timeout) because agents crash and forget to clean up, and orphaned compute is
the fastest way to burn money. Event logs are how platforms answer "what happened to my
sandbox?" — and later become billing and audit records.

## What you'll learn

- The reconcile loop pattern: level-triggered ("observe full state, fix it") vs
  edge-triggered ("react to events"), and why level-triggered survives crashes.
- Crash-consistency thinking: enumerate the half-done states (row without container,
  container without row, both present but disagreeing) and write code that heals each.
- Long-running goroutine hygiene: tickers, jitter, shutdown, not overlapping runs — and
  the error-handling rule for top-of-goroutine loops: the reconciler has no caller to
  return to, so it logs each failure once and continues; it never aborts a pass because
  one sandbox misbehaved.
- The **lost-update** failure mode from the concurrency reference, in its exact home:
  the reconciler reads a record, does seconds of Docker I/O with no transaction held,
  then writes back. A whole-record write can resurrect a sandbox the API destroyed
  mid-create. The fix is the reference's category-4 shape translated to SQL: writes
  re-check ownership in the WHERE clause (`UPDATE ... SET state='running' WHERE id=?
  AND state='pending'`) and touch only the columns the reconciler owns.
- Append-only event modeling.

## Design and interfaces

```go
// Reconciler runs one converge pass; the serve loop calls it on a ticker.
// A pass must be safe to run concurrently with API traffic and safe to repeat.
type Reconciler struct { /* store, runtime, clock, logger */ }

func (r *Reconciler) ReconcileOnce(ctx context.Context) (Actions, error)

// Actions summarizes what a pass did — createdN, destroyedN, orphansAdopted,
// expiredReaped — so tests and logs can assert on behavior.
```

Schema additions: `sandboxes.expires_at`, `sandboxes.desired_state`, and
`events(id, sandbox_id, at, from_state, to_state, reason)`. New API surface:
`GET /v1/sandboxes/{id}/events`, and `POST /v1/sandboxes/{id}/keepalive` to extend the
TTL (default e.g. 15 minutes, cap enforced).

Committed decisions:

- Desired vs observed state are separate columns; the reconciler is the **only**
  component that transitions observed state based on runtime facts.
- The what-to-do branching is a pure function, straight from the code-structure
  reference: `decideReconcileAction(desired *SandboxRecord, observed *runtime.Info,
  now time.Time) ReconcileAction`. Every (desired, observed) pairing — including both
  nil, and present-but-drifted — is a named table row; `ReconcileOnce` only dispatches
  actions and performs I/O. This is the single highest-leverage extraction in the
  project: a reordered branch here destroys live sandboxes.
- Orphan policy: containers labeled `quickspin.managed` with no DB row are destroyed
  and logged (the DB is authoritative). The label from plan 02 is what makes this safe.
- The clock is injected (`func() time.Time`) so TTL tests don't sleep.

## Tasks

1. Schema migration; make create async (insert `pending` + return 202; reconciler
   creates the container).
2. Implement `ReconcileOnce` handling: pending→create, expired→destroy, orphan→adopt
   or destroy, missing-container-for-running-row→mark failed.
3. Ticker wiring with jitter and single-flight (skip a pass if the previous is live).
4. Event append inside the same transaction as each state change.
5. Keepalive endpoint.
6. Tests and crash script below.

## Definition of done

Unit tests with fake runtime + injected clock (`make test`):

- `TestReconcileCreatesPendingSandbox`
- `TestReconcileReapsExpiredSandbox` — advance the fake clock past `expires_at`.
- `TestReconcileDestroysOrphanContainer`
- `TestReconcileMarksVanishedContainerFailed`
- `TestReconcileOncePassIsIdempotent` — two passes over the same state: second is a
  no-op (assert on `Actions`).
- `TestKeepaliveExtendsTTLUpToCap`
- `TestEveryTransitionEmitsEvent` — event log replays to the current state.
- `TestDecideReconcileAction` — pure table over every (desired, observed) pairing,
  named by behavior ("orphan container is destroyed", not "case 3").
- `TestReconcilerDoesNotResurrectDestroyedSandbox` — drive the exact interleaving:
  reconciler reads `pending`, API deletes the sandbox during the (fake) create, the
  reconciler's write-back must not flip it out of its destroyed/stopping state. `-race`
  cannot catch this; only this test does.

Crash validation `hack/validate-06.sh` (real Docker): creates sandboxes, `kill -9`s the
server at three points (after row insert / after container create / during destroy),
restarts it, and asserts the system converges — every DB row matches a container state
and vice versa, with zero leaks — within one reconcile interval. Also creates a
container with the quickspin label by hand and confirms the reconciler removes it.

Deliberately untested: two control-plane instances racing (single-instance is a stated
assumption until plan-set two), clock skew, event-log compaction.

## Solo-developer tradeoffs

Kubernetes-grade reconcilers use informers, work queues, and rate-limited retries per
object; you run one full-scan loop on a ticker, which is O(sandboxes) per pass and
entirely fine below thousands of rows. Commercial platforms also emit events to a
message bus for other services to consume; your events are rows in SQLite, queryable
with one endpoint — same concept, one binary. The non-negotiable kept: after any crash,
convergence, provable by script.
