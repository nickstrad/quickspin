# Documentation

This directory contains Quickspin's plans and reference material. The codebase describes
what exists today; documents may also discuss work that has not been implemented.

## Plans

Plans describe bounded implementation work. Moving a plan records its lifecycle; it does
not by itself authorize implementation.

- [`plans/open/`](plans/open/) contains proposed plans and work that is still in progress.
- [`plans/closed/`](plans/closed/) contains completed plans preserved for historical
  context.
- [`plans/AGENTS.md`](plans/AGENTS.md) defines the required structure for every plan,
  including the verifiable definition-of-done requirement.

### The platform learning path (open)

Plans 01–12 build the platform incrementally; 13–14 are isolation deep dives that can
run in parallel after their stated dependencies; 15–18 are the production track that
takes the platform to live cloud hosts.

1. [`01-linux-lab-environment.md`](plans/open/01-linux-lab-environment.md) — Lima Linux
   VM, Docker socket, Go cross-compilation, environment validation script.
2. [`02-sandbox-runtime-lifecycle.md`](plans/open/02-sandbox-runtime-lifecycle.md) —
   the `Runtime` interface and Docker-backed create/inspect/destroy via a CLI.
3. [`03-exec-limits-cancellation.md`](plans/open/03-exec-limits-cancellation.md) —
   exec with real exit codes, cgroup limits, network policy, kill-on-cancel.
4. [`04-sandbox-filesystem-api.md`](plans/open/04-sandbox-filesystem-api.md) — file
   read/write/list/remove with path validation.
5. [`05-control-plane-api.md`](plans/open/05-control-plane-api.md) — HTTP control
   plane, SQLite state machine, idempotency keys.
6. [`06-reconciler-leases-events.md`](plans/open/06-reconciler-leases-events.md) —
   reconcile loop, TTL reaping, event log, async create, crash convergence.
7. [`07-guest-agent-api.md`](plans/open/07-guest-agent-api.md) — the in-sandbox
   `quickspin-guest` binary serving exec (streaming) and files, E2B-envd style.
8. [`08-auth-tenancy-quotas.md`](plans/open/08-auth-tenancy-quotas.md) — API keys,
   tenant scoping, admission-time quotas, written threat model.
9. [`09-typescript-sdk.md`](plans/open/09-typescript-sdk.md) — generated TypeScript
   SDK gated by contract tests; OpenAPI served by the plane.
10. [`10-python-sdk.md`](plans/open/10-python-sdk.md) — generated Python SDK; API
    symmetry check across languages.
11. [`11-snapshot-save-restore.md`](plans/open/11-snapshot-save-restore.md) —
    filesystem snapshots, restore-by-create, warm-start benchmark.
12. [`12-agent-harness-demo.md`](plans/open/12-agent-harness-demo.md) — capstone:
    external repo imports the SDK and an LLM agent develops code in a sandbox.
13. [`13-mini-container-runtime.md`](plans/open/13-mini-container-runtime.md) — deep
    dive: namespaces, pivot_root, cgroups, capabilities by hand.
14. [`14-firecracker-backend.md`](plans/open/14-firecracker-backend.md) — deep dive:
    Firecracker microVM as a second `Runtime` backend behind the same API.
15. [`15-postgres-store.md`](plans/open/15-postgres-store.md) — pluggable store:
    Neon Postgres for prod behind the same store contract, one conformance suite.
16. [`16-prod-single-host.md`](plans/open/16-prod-single-host.md) — first live
    deployment: one Hetzner host, Firecracker-backed, TLS, systemd, deploy script.
17. [`17-worker-split-heartbeats.md`](plans/open/17-worker-split-heartbeats.md) —
    control-plane/worker split, host registration, heartbeats, placement.
18. [`18-host-provisioning-observability.md`](plans/open/18-host-provisioning-observability.md)
    — hosts created/retired via the Hetzner API, Prometheus metrics, Grafana
    dashboard and alerts, orphan-server cost safety.

Ordering: the spine is 01 → 02 → 03 → 04 → 05 → 06 → 07 and must be sequential (each
extends the same interfaces). After 07, work can proceed in parallel batches: {08}
alongside finishing touches on 07; then {09 → 10, 11, 14, 15} are mutually independent
(15 needs 08; 14 and 11 need only 07); 12 needs 09 and 11; 13 is independent of
everything after 01 and can be slotted anywhere as a change of pace. The production
tail 16 → 17 → 18 is sequential and needs 12, 14, and 15 complete. Each plan's
`Depends on:` header is authoritative where this summary is coarse.

## Reference

[`reference/`](reference/) contains forward-looking learning notes, architectural options,
and technical background. Reference documents are not specifications for current code.

Current reference documents:

- [`reference/sandbox-infrastructure-learning-path.md`](reference/sandbox-infrastructure-learning-path.md)
  suggests a staged path from one local sandbox to scalable sandbox infrastructure.
- [`reference/error-handling-and-logging.md`](reference/error-handling-and-logging.md)
  proposes per-package error types (`E` at the boundary / `Wrap` above it), one log per
  error, and `slog` child loggers threaded through constructors.
- [`reference/code-structure-and-testing.md`](reference/code-structure-and-testing.md)
  proposes separating decisions from Docker/HTTP plumbing, when to extract a pure
  function, and what to test at each layer.
- [`reference/concurrency-and-state.md`](reference/concurrency-and-state.md) proposes how
  the future control plane and workers should guard shared state around slow Docker I/O —
  the four helper categories, the upsert pattern for read-modify-write with I/O between,
  and how to race-test them.

These three are adapted from a sibling Go learning project and describe conventions to
adopt, not current behavior. They are intended as a lens for a second pass over the plans.

Update this index whenever documentation is added, moved, or removed.
