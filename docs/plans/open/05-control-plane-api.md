# 05 — Control plane: HTTP API and durable state

Depends on: 04.

## Summary

Wrap the runtime in a long-running Go HTTP service — the control plane. It owns sandbox
records in SQLite, exposes a versioned JSON API (`/v1/sandboxes`...), assigns lifecycle
states, and supports idempotent creation via client-supplied idempotency keys. The CLI
becomes a client of the API instead of calling the runtime directly. From this plan on,
Quickspin is a service, not a tool.

## Industry context

This is the piece users literally point their SDKs at: E2B's and Daytona's public REST
APIs look strikingly like the surface below (create with a spec, poll state, exec,
files, delete). Two industry patterns are load-bearing here. First, **desired state
lives in a database, actual state lives in the runtime**, and they are allowed to
disagree — reconciling them is plan 06's job; this plan makes the disagreement
representable. Second, **idempotency keys** (as popularized by Stripe's API) are how
every serious platform makes "the client retried a create after a network blip" not
produce two billed sandboxes.

## What you'll learn

- Production-shaped Go HTTP services: `net/http` with Go 1.22+ method routing,
  middleware, graceful shutdown, structured logging (`log/slog`), request IDs.
- SQLite from Go: schema migrations, transactions, and why "state machine in a
  database" is a transactional problem.
- The error/logging conventions at a second layer: `ControlPlaneError` with `Wrap`
  above the runtime's errors, one-log-per-error (the handler that converts an error to
  an HTTP response is the propagation stop, so it logs), child loggers per component.
- Designing a lifecycle state machine and rejecting illegal transitions.
- API error design: stable machine-readable error codes vs prose.

## Design and interfaces

Lifecycle states and legal transitions:

```text
pending -> running -> stopping -> stopped
pending -> failed
running -> failed
```

HTTP surface (JSON bodies; errors as `{"error": {"code": "...", "message": "..."}}`):

```text
POST   /v1/sandboxes              create (honors Idempotency-Key header)
GET    /v1/sandboxes              list
GET    /v1/sandboxes/{id}         inspect
DELETE /v1/sandboxes/{id}         destroy (idempotent: 204 even if already gone)
POST   /v1/sandboxes/{id}/exec    run a command (buffered result)
PUT    /v1/sandboxes/{id}/files?path=/abs/path    write (body = content)
GET    /v1/sandboxes/{id}/files?path=/abs/path    read
GET    /v1/sandboxes/{id}/dir?path=/abs/path      list
```

SQLite schema (minimum): `sandboxes(id, state, spec_json, runtime_ref, created_at,
updated_at)` and `idempotency_keys(key, sandbox_id, created_at)`.

Committed decisions:

- Create is **synchronous** in this plan (request returns when the sandbox is running
  or failed). Plan 06 revisits this honestly; note it now as a known simplification.
- The store is behind a small Go interface so tests can use `:memory:` SQLite. Per the
  concurrency reference's store rules: typed reads, records by value in and out,
  `ErrNotFound` + zero value for missing keys, and **no locking or transaction policy
  of its own** — invariants that span calls (state transition + event append, quota
  check + insert in plan 08) are the caller's transaction.
- Now that plan 15 commits prod to Postgres, two of its costs move here where they are
  nearly free: the schema ships as **versioned migration files** (`migrations/NNNN_*.sql`,
  embedded, applied at startup) rather than create-if-missing, and it avoids
  SQLite-isms Postgres will reject (app-generated string IDs, no serial PKs, ISO-8601
  UTC text timestamps mapping cleanly to `timestamptz`). The store test suite is
  written as `storetest.Run(t, factory)` from day one so plan 15 adds a second caller
  instead of an extraction refactor.
- State transitions happen in one place (a `transition(id, from, to)` helper inside a
  transaction) — never as scattered UPDATEs. The legality check itself is a pure
  function, `canTransition(from, to State) bool`, with a table test enumerating the
  full matrix (code-structure reference, candidate #3).
- `internal/httpapi` owns the response dance (`WriteJSON`, `WriteError`) and the
  `ErrorResponse` DTO; handlers never hand-roll status-plus-JSON, `WriteError` sets the
  body's code from the status it writes, and 204 writes no body. This package is shared
  wire format — the SDKs (09/10) and the worker client (17) will consume the same DTO,
  which is why it lives in neither server nor client.

## Tasks

1. Schema + migration on startup; store package with `:memory:` tests.
2. HTTP server skeleton: routing, middleware (request ID, logging, panic recovery),
   graceful shutdown on SIGTERM.
3. Handlers for the surface above, mapping runtime sentinel errors to HTTP codes
   (`ErrNotFound`→404, `ErrImageMissing`→422, illegal transition→409).
4. Idempotency-key handling for create.
5. Convert the CLI to an HTTP client (`quickspin serve` starts the plane).
6. Tests below.

## Definition of done

Store unit tests (`make test`), authored inside `storetest.Run`:

- `TestIllegalTransitionRejected` — e.g. stopped→running fails atomically.
- `TestIdempotencyKeyReturnsSameSandbox`.
- `TestCanTransitionMatrix` — pure table over every (from, to) pair.
- `TestStoreReturnsCopies` — mutating a returned record does not change a re-read.

API tests with `httptest` and a fake runtime (`make test`):

- `TestCreateInspectDestroyFlow` — JSON contract, status codes, error envelope shape.
- `TestDeleteIsIdempotent` — second DELETE also 204.
- `TestRuntimeFailureMarksSandboxFailed` — fake runtime error ⇒ record in `failed`,
  429/5xx taxonomy correct.

End-to-end validation script `hack/validate-05.sh` against the real service + Docker:
starts `quickspin serve`, drives create→exec→file write/read→destroy with `curl`,
asserts on JSON with `jq`, kills the server mid-run and confirms a restart still lists
the surviving sandbox record. Exits 0 only if all checks pass and no labeled containers
leak.

Deliberately untested: concurrent creates racing one idempotency key, auth (plan 08),
TLS, request body size limits.

## Solo-developer tradeoffs

Commercial planes are Postgres-backed, horizontally scaled, and async-first (create
returns `pending` immediately; you poll or subscribe). SQLite plus synchronous create
keeps this a single deployable binary with zero infra — the right call for one
developer — and the store interface plus the state machine mean the Postgres/async
migration later is mechanical, not architectural. The one habit not to defer: never let
an HTTP handler touch Docker without recording intent in the database first. That
ordering is what plan 06 builds on.
