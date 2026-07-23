# Code Structure & Testing Conventions

Forward-looking reference material, not a description of current code. It proposes how to
split logic in Quickspin so it can be tested, and what to test once it is split. Adapted
from a sibling learning project; examples are re-cast in Quickspin's domain
(`internal/runtime`, the Docker backend, a future control plane and reconciler).

One rule carries most of the weight:

**Separate the decision from the plumbing.** Pull the logic that computes a value or picks
an outcome out of the code that talks to HTTP, Docker, the clock, or a logger. Test the
logic directly; keep the plumbing thin enough to read.

This matters more here than in a CRUD app, because Quickspin's plumbing is a live Docker
daemon. If the only way to exercise a decision is to start a container, most decisions go
untested.

---

## Three kinds of code

| Kind | What it does | Where it lives | How it is tested |
| --- | --- | --- | --- |
| **Pure functions** | Values in, values out. No receiver, no I/O, no clock, no logging, no globals. | `pure.go` in the package, or beside the type they serve | Directly, table-driven. No fixtures, no fakes, no daemon. |
| **Business logic** | Composes pure functions and mutates the package's own state (sandbox records, worker registry). | Methods on the control plane / worker / reconciler type | Construct the struct, seed its maps, call the method, assert on the state it owns. |
| **Framework plumbing** | HTTP handlers, Docker SDK calls, `time.Tick` reconcile loops, `slog` lines. | `api.go`, `docker.go`, the loop bodies | Sparingly — `httptest` at the boundary, one integration test against real Docker, or not at all. |

The point of the split is that most of what can actually be *wrong* lives in the first two
rows, and neither needs a Docker daemon or a live worker to exercise. Plan 02's
`make test` / `make test-docker` split is the same idea at the Makefile level: keep the
daemon-free tests fast and separate.

## Extracting a pure function

The signal is a block inside a method that computes something without touching the
receiver. It is buried, so nothing can test it, and it will drift.

Imagine the reconciler deciding what to do about one sandbox, inline:

```go
// buried inside reconcileOnce, mid-loop, holding observed + desired state
if desired == nil && observed != nil {
    _ = rt.Destroy(ctx, observed.ID)      // an orphan: destroy it
} else if desired != nil && observed == nil {
    _, _ = rt.Create(ctx, desired.Spec)   // missing: create it
} // ... and the "both exist but state differs" case, easy to forget
```

The decision — *given desired and observed, what should happen* — is tangled with the
Docker calls that carry it out. You cannot test "an orphan is destroyed" without a daemon,
and the missing fourth case is invisible.

Pulled out, it is a function you can just call:

```go
type ReconcileAction int

const (
    ActionNone ReconcileAction = iota
    ActionCreate
    ActionDestroy
    ActionMarkDrifted
)

// decideReconcileAction compares what the control plane wants against what the runtime
// reports and returns the single action that closes the gap. No I/O: the caller performs
// the action, so every branch is one table row.
func decideReconcileAction(desired *SandboxRecord, observed *runtime.Info) ReconcileAction {
    switch {
    case desired == nil && observed == nil:
        return ActionNone
    case desired == nil && observed != nil:
        return ActionDestroy
    case desired != nil && observed == nil:
        return ActionCreate
    default:
        if desired.State != observed.State {
            return ActionMarkDrifted
        }
        return ActionNone
    }
}
```

Three things the extraction buys:

- **The edge cases become visible.** The "both nil" and "both present but drifted" cases
  are now branches you can see and name, not gaps in an `if/else` chain.
- **The comment has somewhere to live.** Why an orphan is destroyed rather than adopted is
  the part that gets lost; on a named function it sits at the top.
- **Regressions get pinned.** A test asserts each (desired, observed) pair maps to the
  exact action, so a reordered branch can't silently start destroying live sandboxes.

Good candidates, in rough priority:

1. **String / ID / label construction** — generating and validating `sbx_` IDs, building
   the `quickspin.managed=true` label filter, turning a container name into a sandbox ID.
2. **Struct-to-struct translation and merges** — `Spec` → Docker container config; merging
   an observed `runtime.Info` into a persisted `SandboxRecord` without clobbering
   control-plane-owned fields. A field-copy block silently forgetting a field is a classic
   bug; a test pins the list.
3. **Branching that picks an outcome** — `decideReconcileAction`, legal-state-transition
   checks (`canTransition(from, to State) bool`). Return an enum or bool describing *what
   to do*, and let the caller do it.
4. **Filters and predicates** — "is this container one of mine?", "is this lease expired?"
   over a slice or map.

Not everything belongs in `pure.go`. If a helper only makes sense next to one type, keep
it in that type's file. The file is a convenience, not a rule.

## Keep the error style

Pure functions have no `Op` of their own, so they return plain `fmt.Errorf` with `%w` (or
a bare sentinel). The calling method wraps at the boundary, per
[`error-handling-and-logging.md`](error-handling-and-logging.md):

```go
id, err := newSandboxID()
if err != nil {
    return Info{}, E("runtime.dockerRuntime.Create", "generating sandbox id", err)
}
```

## What to test

Test **both** layers. The pure functions catch the arithmetic; the business logic catches
the wiring — a correct helper called with the wrong arguments still produces a bug.

### Pure functions

Table-driven, standard library only, no testify. Cover the happy path, each rejected
input, and any regression worth pinning:

```go
func TestDecideReconcileAction(t *testing.T) {
    running := &runtime.Info{State: runtime.StateRunning}
    tests := []struct {
        name     string
        desired  *SandboxRecord
        observed *runtime.Info
        want     ReconcileAction
    }{
        {name: "orphan container is destroyed", desired: nil, observed: running, want: ActionDestroy},
        {name: "missing sandbox is created", desired: &SandboxRecord{}, observed: nil, want: ActionCreate},
        {name: "nothing to do when both absent", desired: nil, observed: nil, want: ActionNone},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            if got := decideReconcileAction(tt.desired, tt.observed); got != tt.want {
                t.Errorf("decideReconcileAction() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

Name each case after the behavior it describes, not `case1`/`case2` — the name is what you
read when it fails. Failure messages follow the standard Go form: `got X, want Y`, with the
inputs included.

Two habits worth keeping:

- **Pin real bugs by name.** When a bug ships and you fix it, add a test named after it
  (`TestNewSandboxIDIsNeverEmpty`) asserting against the exact broken value. A test named
  after the bug explains itself.
- **Assert what must *not* change.** A merge test should check that an observed
  `runtime.Info` cannot overwrite a control-plane-owned field (the lease, the desired
  state) — the runtime doesn't track those and would send zero, so a careless merge would
  silently erase them. For value-semantics helpers, also assert the arguments were not
  mutated.

### Business logic

Methods that compose pure functions and own state are testable without a network or a
daemon: build the struct, seed its state, call the method, assert on the state. In the
plans the control plane's state sits behind the store interface (plan 05), so "seed its
maps" becomes "seed a `:memory:` SQLite store" — same shape, same speed:

```go
st := storetest.NewMemoryStore(t)                       // :memory: SQLite from plan 05
st.Put(persisted)
cp := &Service{store: st, logger: discardLogger()}
cp.applyObservation(id, observed)
if got := mustGet(t, st, id).State; got != want { /* ... */ }
```

When a method mixes decision and I/O, that is the signal to extract — the reconcile loop
becomes testable exactly once `decideReconcileAction` carries the branching and the loop
just dispatches.

### Framework plumbing

Test at the boundary or not at all. Use `httptest` for handlers. The reconcile ticker and
the Docker SDK calls are left to the real-daemon integration tests plan 02 already
mandates (`make test-docker`) and to `hack/validate-NN.sh` scripts. Do not build elaborate
Docker fakes — if a bug needs one to reproduce, the logic it lives in probably wants
extracting instead. One honest integration test against the real daemon is worth more than
a mock that encodes your assumptions back at you; plan 02 says the same.

## Running tests

```sh
make test                  # daemon-free unit tests, fast
make test-docker           # integration tests that need the Lima Docker socket
make fmt                   # gofmt; must leave nothing to change
make vet                   # go vet; must be clean
```

`make build` and `make vet` should both be clean before you call a change done.
