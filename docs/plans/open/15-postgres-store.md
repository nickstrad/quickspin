# 15 — Postgres store (Neon) for production

Depends on: 08 (store shape final through tenants/quotas); 11's snapshot tables if
already implemented.

## Summary

Make the control plane's store backend pluggable in fact, not just in interface: the
same store contract runs on SQLite (local dev, unchanged) or Postgres, selected by
config (`QUICKSPIN_DB=sqlite:path` vs a `postgres://` URL). Production uses Neon —
serverless Postgres with a free tier and instant database branching, which becomes the
test isolation mechanism. Every store behavior you've built (state machine,
idempotency keys, admission-time quotas, events) must pass the same test suite on both
backends before this plan closes.

## Industry context

Every commercial control plane runs on Postgres or something stronger; SQLite's
single-writer model dies the moment the plane has two instances or a worker split
(plan 17). Neon specifically is what a solo developer would pick in 2026: scale-to-zero
pricing, pooled connection strings via PgBouncer, and branch-per-test-run — the same
"copy the prod schema in seconds" trick platform teams build internally with snapshots.
The porting exercise itself is an industry rite: the bugs live exactly where SQLite was
forgiving (type affinity, `datetime` as text, implicit transaction behavior) and
Postgres is not.

## What you'll learn

- Writing Go SQL against two engines: `pgx` vs `database/sql`+sqlite, placeholder
  styles (`?` vs `$1`), and where to stop abstracting (two small store
  implementations beat one query-builder).
- Real transaction semantics: Postgres isolation levels, `SELECT ... FOR UPDATE` for
  the quota admission race (plan 08's `TestQuotaRaceOnlyAdmitsCap` gets interesting —
  SQLite serialized writes for you; Postgres will not).
- Migrations at startup under a Postgres advisory lock (`pg_advisory_lock`) so two
  racing planes cannot double-migrate — the files themselves exist since plan 05; what
  is new is dialect porting and the lock.
- Operating a managed DB: connection pooling (pooled vs direct Neon strings), TLS
  (`sslmode=require`), secrets outside the repo.

## Design and interfaces

The `store` interface from plan 05 does not change — that is the point. Additions:

```go
// store.Open(cfg) picks the backend from the URL scheme.
// migrations/ is embedded (go:embed) and applied on startup under an advisory
// lock (pg_advisory_lock) so two racing planes can't double-migrate.
```

Committed decisions:

- Time columns become `timestamptz`; IDs stay app-generated strings (no serial PKs to
  keep backends symmetric).
- Quota admission uses `SELECT count(*) ... FOR UPDATE` on the tenant row in Postgres;
  the store test suite gains a cross-backend concurrency test that would have caught
  the difference.
- The shared suite already lives as `storetest.Run(t, factory)` (plan 05 wrote it that
  way for exactly this moment); this plan adds the `TestPostgres` caller and extends
  the suite with the guard-clause write test: an UPDATE conditioned on current state
  (plan 06's don't-resurrect rule) must behave identically on both engines.
- CI/local Postgres tests run against a Neon **branch** created and deleted by
  `hack/testdb.sh` (fallback: a local `docker run postgres` in the Lima VM if offline).

## Tasks

1. Port `migrations/` to a Postgres-compatible dialect (plan 05's portability rules
   should make this small; every divergence found goes in the closing notes).
2. Teach the migrator the advisory-lock dance on Postgres.
3. Implement the Postgres store with `pgx`.
4. `hack/testdb.sh`: create Neon branch via `neonctl`/API, export URL, delete on exit.
5. Fix everything the shared suite finds (expect datetime and race findings).
6. Config plumbing + README section on Neon setup (project, roles, pooled string).

## Definition of done

- `make test` — SQLite suite still green, untouched behavior.
- `make test-store-pg` — the identical `storetest.Run` suite green against a Neon
  branch (or local Postgres), including:
  - `TestQuotaRaceOnlyAdmitsCap` at meaningful concurrency (this is the test that
    proves Postgres-correctness, not just Postgres-compatibility).
  - `TestIdempotencyKeyReturnsSameSandbox` under concurrent duplicate creates.
  - `TestMigrationsAreIdempotent` — two planes starting simultaneously against one
    fresh database both come up healthy (advisory lock works).
- `hack/validate-15.sh`: boots the full plane against a Neon branch, runs the plan 05
  end-to-end curl flow, kills and restarts the plane, confirms state survived in Neon.
- Closing notes must list every behavior difference found between backends.

Deliberately untested: failover/read replicas, connection-storm behavior, Neon
scale-to-zero cold-start latency under load (worth *measuring* once and noting).

## Solo-developer tradeoffs

A commercial platform would run HA Postgres with PITR, schema-change review, and a
data-access layer team. You get managed HA from Neon for free-tier money and keep two
hand-written store implementations, accepting the double-maintenance cost because the
SQLite path is what keeps local dev and `make test` instant and offline. The discipline
kept from industry: one conformance suite, versioned migrations, and no ORM — the SQL
you debug at 2am is the SQL you wrote.
