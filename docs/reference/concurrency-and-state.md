# Concurrency & State Conventions

Forward-looking reference material, not a description of current code. `internal/runtime`
as first built (plan 02) is single-goroutine and needs none of this. It becomes load-
bearing at plans 05–06 (a control plane with a reconcile loop and an HTTP API touching
shared records) and plan 17 (workers). Read it before writing either. Adapted from a
sibling learning project that guarded exactly this shape of state — "read a record, do
slow Docker I/O, write the result" — in real code.

One framing correction against the plans: the sibling project held its records in
mutex-guarded maps, while Quickspin's plans make the **database authoritative from plan
05** (SQLite, later Postgres). That moves where each rule lands — the transaction plays
the mutex's role for record state, and the map-and-mutex guidance below applies to the
in-process mutable state that genuinely remains in memory: plan 14's tap/IP allocator,
workerd's local bookkeeping (plan 17), and any future cache. The *failure modes* (rule 1,
check-then-act, the category-4 lost update) are identical in both worlds and are the
reason to read on; each section below notes its SQL translation where it differs.

Two rules carry most of the weight:

1. **Never hold the mutex across I/O.**
2. **Nothing outside `state.go` touches guarded state directly.**

The first rule is not stylistic here: a Docker image pull can take a minute, and a lock
held across one stalls the HTTP API, the reconcile loop, and every other request for its
whole duration.

---

## What gets guarded

In the plans, the fleet's records (sandboxes, events, hosts, capacity) live in the
database, guarded by transactions. What remains genuinely in-memory and mutex-guarded:

| Component | Mutex | Guarded state (illustrative) | Concurrent goroutines |
| --- | --- | --- | --- |
| firecracker backend (plan 14) | allocator `mu` | tap/IP allocations | reconciler-driven creates and destroys |
| worker (plan 17) | `Worker.mu` | local stats, in-flight operation table if one exists | run loop, heartbeat loop, HTTP API |

Fields set once at construction and never written again — the Docker client, the logger,
the reconcile interval, the backend `Runtime` — are **not** guarded. A value only ever
read needs no lock. Making any of them mutable means bringing it under the mutex.

**A component guards its own mutable state.** If the scheduler or the backend does I/O, the
control plane's lock must not extend across that call (rule 1) — push the lock down into
whatever holds the genuinely mutable state, so the caller never has to hold its lock across
someone else's network call.

## The four categories

Lock scope is bounded by I/O, not by logic. Every state helper should be one of these four
shapes.

| # | Shape | Example |
| --- | --- | --- |
| 1 | Pure read | `snapshotRecords`, `lookupSandbox`, `listWorkers` |
| 2 | Whole-value write the caller owns | `putRecord` for a brand-new sandbox, `appendEvent` |
| 3 | Read-modify-write, **no** I/O between | `assignWorker` (write several maps together), `renewLease`, `beginDestroy` |
| 4 | Read-modify-write **with** I/O between | the reconciler / worker updating a record around a Docker call |

### 1. Pure read

RLock, copy out, return. Reads return **copies**: handing back the live slice or record out
of the map lets the caller read that memory after the lock is gone. These are snapshots,
not views — the reconcile loop iterates its own snapshot and calls back into the package
while looping, which ranging the live map would not allow.

### 2. Whole-value write

Lock, write, unlock. Safe only when the caller owns the whole record and no earlier version
can conflict — in practice, a sandbox the store has never seen. Anything writing back a
value it *read* earlier is category 3 or 4, not this.

### 3. Read-modify-write with no I/O

**One helper, one lock — not a get followed by a set.** Locking each field access
individually still lets two goroutines interleave between the get and the set, which is how
check-then-act bugs survive a mutex.

In-memory, this is one helper under one lock; in the database it is one transaction —
never a SELECT in one transaction and an UPDATE in another. The plans hit this shape
twice by name: plan 08's quota admission (count and insert in the same transaction) and
plan 17's placement reservation (`FOR UPDATE` on the host row before recording the
sandbox on it).

> Do not check `hasCapacity()` and then reserve. Two goroutines can both pass the check and
> both reserve the last slot. Fold the check into the reserving helper's return value —
> or into the reserving transaction. Plans 08 and 17 each carry a concurrency test
> (`TestQuotaRaceOnlyAdmitsCap`, `TestConcurrentPlacementNeverOverfillsHost`) that fails
> if the fold is undone.

### 4. Read-modify-write with I/O in the middle

**This is the category the reconciler and the worker live in, and the reason a naive
"lock every access" does not port over.**

Every sandbox state change is the consequence of a Docker call:

```
read the record  ->  create/destroy/inspect a container  ->  write the result
```

Rule 1 forbids holding the lock across that middle step, so the lock *cannot* cover the
read and the write together. Small per-access helpers are not enough either — they make
each access atomic, which stops the data race but not the **lost update**:

```
reconcile reads sandbox X          {State: Creating}
reconcile calls Docker Create      ... seconds of I/O ...
API handler destroys X, writes     {State: Destroyed}
reconcile writes its copy back      {State: Running}   <- resurrects a destroyed sandbox
```

Every access there is locked and `-race` reports nothing, but X is Running again with no
one having asked for it. The cause is that the write carries *every* field, not just the
one that changed.

The fix is an `upsert`-style helper that moves the mutation inside the lock: it re-reads
the record, hands a callback a pointer to that fresh copy, and writes back only what the
callback touched. The Docker call still happens outside the lock; only the read-apply-write
is atomic.

```go
info, err := rt.Create(ctx, spec)   // slow Docker I/O, NO lock held
if err != nil { /* handle */ }

cp.upsertRecord(id, func(r *SandboxRecord) {
    if r.State == StateDestroyed {
        return                       // someone destroyed it during Create — leave it alone
    }
    r.ContainerID = info.ID
    r.State = StateRunning
})
```

Two constraints on the callback:

- **Touch only fields the caller owns**, and re-check any field it does *not* own before
  acting on it (the `StateDestroyed` guard above).
- **No I/O, and no calls back into the package.** The lock is held; either deadlocks.

**SQL translation — the form the plans actually use.** With the database authoritative
(plan 05 onward), the same guard becomes a conditional write that re-checks state in the
WHERE clause and touches only the columns the writer owns:

```sql
UPDATE sandboxes SET state = 'running', runtime_ref = ?, updated_at = ?
WHERE id = ? AND state = 'pending';   -- 0 rows affected => someone else moved it; stand down
```

The zero-rows-affected result is the SQL spelling of the callback's early `return`, and
the reconciler must treat it as "leave it alone," not as an error. Plan 06 mandates this
shape and pins the interleaving with `TestReconcilerDoesNotResurrectDestroyedSandbox` —
the same test this section prescribes, renamed to its home.

## The `Locked` suffix

`sync.RWMutex` is not reentrant. A helper that takes the lock again from inside
`upsertRecord` — which already holds it for writing — deadlocks the whole component. Shared
bodies therefore come in pairs:

```go
func (cp *Service) lookupSandbox(id string) (SandboxRecord, bool) {
    cp.mu.RLock()
    defer cp.mu.RUnlock()
    return cp.lookupSandboxLocked(id)
}
```

**If a method has the `Locked` suffix, the caller already holds the lock. If it does not,
it takes one.** No exceptions — that is the only thing keeping the pairs readable.

## `RWMutex`, not `Mutex`

Most goroutines touching this state are readers: the API's GET handlers, the reconcile
loop's initial listing, the inspect endpoint. A plain `Mutex` serialises them against each
other for no benefit. Write paths are unaffected.

## Push the lock down to the state that needs it

If scheduling or scoring does network I/O (fetching live stats from workers, plan 4), the
control plane's lock must **not** span it. Give that component its own small mutex over its
own cursor/state, and let the placement call run lock-free from the control plane's point
of view. A cursor that is only safe under its caller's lock forces that caller to hold the
lock across whatever else the callee does — here, a network call. Guarding it where it
lives removes the constraint.

When work fans out across candidates, prefer writing into **disjoint slice indices** over a
shared map:

- Distinct slice indices are distinct memory addresses; the backing array never
  reallocates, so parallel writes cannot interfere — no lock needed.
- A `map` keyed "one goroutine per key" is **not** safe: a Go map shares `count`, its
  bucket array, and growth across every key, and the runtime throws
  `fatal error: concurrent map writes`, which `recover` cannot catch. Unique keys buy
  nothing; unique indices buy everything.

Compact the per-index results into one slice after `wg.Wait()`, single-threaded, in
candidate order, so ties break the same way on every run rather than by completion order.

## Reading through a store

The records live behind the store interface plan 05 defines (SQLite locally, Postgres in
prod per plan 15 — the plans skip the in-memory-store stage). Keep these properties:

- **Typed reads.** A generic store returns a `SandboxRecord`, not an `any` to assert — the
  compiler rejects a mismatched write at the call site instead of panicking at read time.
- **By value, in and out.** Go copies a struct on assignment, so nothing a caller holds
  aliases what the store holds. A pointer-based store shares state silently the moment one
  code path forgets to copy.
- A missing key returns `ErrNotFound` alongside the **zero value**, not `nil`. Check the
  error anyway — it is the only thing distinguishing "absent" from "present and zero".

The store should impose **no concurrency policy of its own**. The control plane has
invariants spanning more than one statement (state transition + event append in plan 06,
quota check + insert in plan 08, capacity check + placement in plan 17), so the caller
owns the transaction that makes them atomic; a store that serialized or locked internally
would make it pay twice and still not make a read-modify-write atomic.

## State helpers mint their own op

Every state helper builds its own `Op` string (`"controlplane.upsertRecord"`,
`"worker.beginRun"`) rather than accepting one from the caller. A caller-supplied op is an
unverifiable literal that goes stale silently when the caller is renamed — and since most
of these helpers log rather than return, the caller's op never reached anyone anyway. See
[`error-handling-and-logging.md`](error-handling-and-logging.md).

## Testing

Assertions are secondary here — the value is in what `go test -race` reports. A test that
does not run the helpers *concurrently* proves nothing, so each stateful package should
have one that drives every helper from goroutines standing in for the real loops
(`TestControlPlaneStateIsRaceFree`).

Three failure modes are invisible to `-race` and need tests of their own:

- **Lost updates.** `-race` cannot see the resurrect-a-destroyed-sandbox sequence above.
  Pin it by driving the exact interleaving — in the plans this is plan 06's
  `TestReconcilerDoesNotResurrectDestroyedSandbox`.
- **Deadlock** shows up as the test binary's timeout, not a failure. A fan-out over many
  failing candidates with an under-sized error channel hangs instead of passing — write the
  test with more failing candidates than any plausible fixed buffer.
- **Choosing the wrong answer** is a correct-looking program computing the wrong result —
  e.g. an unreachable worker winning placement on an absent zero score. Cover it directly.

When adding a helper, check it **fails with the locking removed**. A race test that passes
either way is not testing anything.
