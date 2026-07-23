# 08 — API keys, tenants, and quotas

Depends on: 06 (07 recommended first).

## Summary

Put an identity boundary on the control plane: API keys tied to tenants, every sandbox
owned by a tenant, list/inspect/destroy scoped to the caller, and per-tenant quotas
(max concurrent sandboxes, max sandbox TTL). Write down a one-page threat model that
states what is and is not defended. This is the minimum needed before an SDK in another
repo can "onboard to the platform" the way you described.

## Industry context

Every platform's SDK constructor takes an API key (`E2B_API_KEY`, Daytona's keys) — the
key maps to a team/tenant, and the tenant is the unit of quota, billing, and blast
radius. The standard mechanics you'll copy: keys shown once at creation, stored only as
hashes (like password storage — a DB leak must not leak keys), a prefix for
identification (`qs_live_...`), and quota checks that admission-control creates before
any resource is spent. Commercial platforms layer OAuth, orgs, RBAC, and rate limiting
on top; the tenant-scoping core underneath is what you're building.

## What you'll learn

- Credential handling: generate with `crypto/rand`, store SHA-256 hashes, compare in
  constant time, never log secrets.
- Multi-tenant data scoping in SQL — every query gains a `tenant_id` predicate, and
  cross-tenant access must read as 404, not 403 (don't leak existence).
- Admission control vs reconciliation: quota is checked at accept time inside the same
  transaction that inserts the row, or two racing creates both slip under the cap.
- Writing a threat model honestly: trusted host, untrusted workload, semi-trusted SDK.

## Design and interfaces

Schema: `tenants(id, name, max_sandboxes, max_ttl_s, created_at)` and
`api_keys(id, tenant_id, prefix, sha256, created_at, revoked_at)`. `sandboxes` gains
`tenant_id`.

API surface:

```text
Authorization: Bearer qs_live_<random>       required on all /v1/* routes
401 missing/invalid key; 403 revoked; 404 other tenants' resources
POST /v1/sandboxes → 429 {"error":{"code":"quota_exceeded"}} at the cap
```

Admin operations (create tenant, mint/revoke key) are **CLI-only against the DB**
(`quickspin admin tenant create`, `quickspin admin key mint`), not HTTP — no admin API
means no admin-API auth problem yet. Keys print once at mint.

Committed decisions: auth is middleware resolving key→tenant onto the request context;
handlers below it never see the raw key. Requested TTLs clamp to the tenant cap.

## Tasks

1. Migration + `internal/auth` (mint, hash, verify, revoke) with unit tests.
2. Auth middleware; thread `tenant_id` through store queries.
3. Quota check inside the create transaction.
4. Admin CLI verbs.
5. `docs/reference/threat-model.md` — one page: assets, trust boundaries, what plans
   02–08 defend, what they explicitly do not (plane→guest link, host escape, egress).
6. Tests below.

## Definition of done

Unit (`make test`):

- `TestMintedKeyVerifies` / `TestRevokedKeyRejected` / `TestRawKeyNeverStored` (assert
  the DB contains no substring of the raw key).
- `TestQuotaRaceOnlyAdmitsCap` — N concurrent creates against cap M admit exactly M.

API tests with fake runtime (`make test`):

- `TestMissingKey401` / `TestCrossTenantInspectIs404` / `TestCrossTenantDestroyIs404AndHarmless`
- `TestQuotaExceededIs429WithCode`
- `TestTTLClampedToTenantMax`

Validation `hack/validate-08.sh`: mints two tenants' keys via the admin CLI, drives the
real service proving isolation (tenant B cannot see or destroy tenant A's sandbox) and
quota enforcement, and greps the server logs to prove no raw key was ever logged.

Deliberately untested/undefended (recorded in the threat model): plane→guest auth,
per-key rate limiting, key rotation UX, audit of admin actions.

## Solo-developer tradeoffs

No OAuth, no web console, no RBAC, no billing — one bearer-key scheme done correctly.
That is the right floor: hashing, scoping, and transactional admission are the parts
that are hard to retrofit, while console/OAuth are additive later. The threat-model doc
is the resume artifact here: interviewers at platform companies probe "what does your
sandbox *not* defend against?" and a written honest answer is rarer than the code.
