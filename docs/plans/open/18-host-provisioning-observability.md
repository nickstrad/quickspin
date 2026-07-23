# 18 — Cloud host provisioning and fleet observability

Depends on: 17.

## Summary

Close the loop on "create and monitor live cloud hosts": the control plane gains a
provider integration (Hetzner Cloud API behind a Go interface) so an admin command —
`quickspin admin host create` — provisions a real server that boots via cloud-init
into a registered, heartbeating worker with no manual steps; `drain` + `delete`
retire one just as cleanly. Monitoring becomes real observability: Prometheus-format
`/metrics` on the plane and workers, scraped into Grafana Cloud's free tier, with a
small dashboard and two alerts. A cost-safety reconciler guarantees no forgotten
server ever bills you silently.

## Industry context

This is infrastructure-as-software rather than infrastructure-as-clicking: the same
pattern as Kubernetes cluster-autoscaler node groups or Nomad autoscaler plugins —
desired hosts in a database, a provider API to realize them, cloud-init to make new
metal self-joining, and reconciliation between the provider's server list and yours.
The metrics stack is the industry-standard shape at minimum size: instrument with
`prometheus/client_golang`, let a managed Grafana scrape and alert, and treat the
provider's server list as one more "actual state" to reconcile — orphan cloud servers
are the fleet-scale version of plan 02's orphan containers, except these cost money.

## What you'll learn

- Driving a cloud provider API from Go (`hcloud-go`): create/list/delete servers,
  labels as ownership markers, user-data injection — and writing it behind an
  interface with a fake for tests.
- Cloud-init as a joining mechanism: the worker token and plane URL templated into
  user-data so a booted server needs zero SSH to become fleet capacity.
- Metrics that matter: counters (creates by outcome), histograms (create-to-ready
  latency), gauges (running sandboxes, host headroom, heartbeat age) — and the
  restraint to stop there.
- Alerting on symptoms not causes: "no ready hosts" and "sandbox failure rate" page;
  everything else is a dashboard.
- Cost-safety engineering: reconciling billing-relevant external state with strict
  "delete unknowns bearing our label" policy.

## Design and interfaces

```go
// internal/provider
type Provider interface {
    CreateServer(ctx context.Context, req CreateServerRequest) (Server, error) // labels: quickspin.fleet
    ListServers(ctx context.Context) ([]Server, error)                          // ours only (by label)
    DeleteServer(ctx context.Context, id string) error                          // idempotent
}
```

```text
Admin flow: host create → provider server w/ user-data(token, plane URL, arch)
  → boots workerd → self-registers (plan 17) → ready. hosts row carries
  provider_server_id from birth; deletion refuses unless drained+empty (--force
  overrides, destroying remaining sandboxes first).
Fleet reconciler: provider server with our label but no hosts row, older than a
  grace window ⇒ delete + loud event. hosts row whose server is gone ⇒ dead
  immediately (skip the heartbeat wait).
Observability: GET /metrics on plane (auth-exempt, IP-restricted via Caddy) and
  workerd; Grafana Cloud scrapes; dashboard JSON checked into hack/dashboard.json;
  alerts: zero-ready-hosts, failure-rate.
```

Committed decisions: creation is admin-triggered, not autoscaling — an autoscaler is
a policy loop atop exactly these primitives and is left as the natural sequel; hosts
are cattle (no in-place upgrades — create new, drain old, delete: your first fleet
rollout procedure, documented in the runbook); the token in user-data is per-host and
revoked on delete.

## Tasks

1. Provider interface + hcloud implementation + fake; labels and idempotent delete.
2. Parameterize plan 16's cloud-init into user-data templating (workerd-only variant).
3. Admin verbs: `host create|delete [--force]`; wire drain-then-delete.
4. Fleet reconciler additions (orphan servers, vanished servers). Same shape as plan
   06: a pure `decideFleetAction(hostRow *Host, server *provider.Server, now time.Time)`
   carries the grace-window branching (table-tested — a wrong branch here deletes a
   paying server or leaks one), and the provider SDK is an external origin, so `E` at
   that boundary per the error reference.
5. Instrument plane + workerd; Caddy metrics exposure; Grafana Cloud scrape;
   dashboard + two alerts.
6. `docs/reference/runbook.md`: host roll procedure, alert responses, "the bill looks
   wrong" checklist.
7. `hack/validate-18.sh`.

## Definition of done

Unit tests with the fake provider (`make test`):

- `TestOrphanServerReapedAfterGrace` / `TestOrphanServerKeptDuringGrace`.
- `TestVanishedServerMarksHostDeadImmediately`.
- `TestDeleteRefusesUndrainedHost` and `--force` path destroys sandboxes first.
- `TestUserDataContainsTokenAndPlaneURL` (templating, no live API).

Live validation `hack/validate-18.sh` (spends real money — cents — and says so):

- `admin host create` → within a bounded window the hosts table shows a new ready
  host that came up with **zero SSH interventions**.
- A `backend: firecracker` sandbox is placed on the new host and passes exec/files.
- Drain → placement avoids it; delete → Hetzner API confirms the server is gone and
  the token is revoked. Final assertion: provider server list contains exactly the
  expected set —**the script fails loudly if anything labeled quickspin remains**.
- Metrics: `/metrics` on plane and worker include the required series; the
  create-to-ready histogram observed the new host's sandbox.
- Alert check (once, manually acknowledged in closing notes): stop all workerds and
  confirm the zero-ready-hosts alert fires in Grafana.

Deliberately untested: multi-region, autoscaling policy, provider rate limits/outage
handling, Grafana-is-down blindness.

## Solo-developer tradeoffs

A commercial fleet layer adds autoscaling, multi-region placement, capacity
forecasting, and its own observability pipeline (Prometheus federation, Loki, traces).
You stop at admin-triggered elasticity plus managed Grafana because the *primitives* —
provider reconciliation, self-joining hosts, drain-based rolls, symptom alerts — are
the transferable knowledge, and each omitted layer is policy atop them. The one place
this plan is deliberately stricter than a first pass needs: the orphan-server reaper.
Platforms have engineers who notice a stray $400 instance; you have a reconciler or a
surprise invoice.
