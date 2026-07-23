# 16 — Production: one live host, Firecracker-backed, public API

Depends on: 14, 15; 12 for the demo-against-prod check in the definition of done.

## Summary

Put Quickspin on the public internet: one Hetzner Cloud host running the control plane
and the Firecracker backend, state in Neon, TLS on a real domain
(`api.quickspin.<yourdomain>`), managed by systemd, deployed by a script in the repo.
The SDKs gain a base-URL knob so the plan 12 demo harness runs unchanged against prod
— an agent on your Mac driving a microVM in a datacenter. Local Lima remains the dev
environment; this plan adds prod beside it, not instead of it.

## Industry context

This is a platform's minimum viable production shape, and it is deliberately boring:
binary + systemd + Caddy + managed Postgres is how many real single-team services run
before Kubernetes is justified. The Firecracker requirement drives the one non-obvious
choice: most cloud VMs don't expose `/dev/kvm`, so the industry runs bare metal (E2B,
Fly) or nitro `.metal` (AWS). Hetzner Cloud is the solo-scale loophole — ordinary
cheap VMs with KVM enabled — which is why it hosts half the indie Firecracker projects
on the internet. Everything else here (TLS termination in front of the plane, secrets
in an env file with tight permissions, deploy-by-artifact) is standard practice scaled
down to one machine.

## What you'll learn

- Linux server operation for real: systemd units (`Restart=`, `After=`, journal
  logging), ufw firewalling (443 + SSH only — guest ports and the plane's plaintext
  port must not be public), unattended-upgrades.
- TLS and DNS in practice: an A record, Caddy as reverse proxy with automatic
  Let's Encrypt, and why the plane itself stays on localhost.
- Release engineering in miniature: `make release` (cross-compile linux/amd64 — note
  the arch flip from your arm64 Mac), `hack/deploy.sh` (rsync artifact, migrate via
  startup, restart unit, health-check, roll back on failure).
- Environment/config separation: the same binary, different env — `QUICKSPIN_DB`,
  backend default, base URL — twelve-factor style.
- Firecracker on x86_64: rebuilding the rootfs/kernel artifacts for amd64 (plan 14's
  scripts parameterized by arch).

## Design and interfaces

```text
Host: Hetzner CX-class (x86_64, /dev/kvm), Ubuntu LTS.
Provisioning: hack/cloud-init.yaml checked into the repo — creates a deploy user,
  installs Caddy + firecracker, writes systemd units, ufw rules. The host must be
  reproducible from this file alone; no hand-configured snowflake.
Processes: quickspin serve (localhost:8080) <- Caddy (:443, TLS) ; firecracker VMs
  as children of the plane as in plan 14.
Secrets: /etc/quickspin/env (mode 600) — Neon URL, nothing else in the repo.
SDKs: baseUrl option + QUICKSPIN_BASE_URL env var (default stays localhost).
New endpoint: GET /v1/status (unauthenticated) — version, uptime, backend, DB ping.
```

Committed decisions: single host, no HA, no CDN, no container packaging (a static Go
binary makes Docker-for-deploy pure ceremony); deploys are brief-downtime restarts
(seconds — acceptable and stated); the Lima path keeps working with SQLite so local
dev never needs the internet.

## Tasks

1. Parameterize plan 14's kernel/rootfs scripts for amd64; `make release`.
2. Write `hack/cloud-init.yaml`; create the server with it via Hetzner console/CLI.
3. DNS record + Caddyfile; confirm TLS.
4. `hack/deploy.sh` with health-check gate and previous-binary rollback.
5. Add `/v1/status` and the SDK base-URL option (both SDKs).
6. Mint a prod tenant/key over SSH with the admin CLI.
7. `hack/validate-16.sh` + run the plan 12 demo against prod.

## Definition of done

`hack/validate-16.sh` runs **from the Mac** against the public URL, exits 0, checking:

- `/v1/status` over valid TLS reports the expected version and `db: ok`.
- Full lifecycle with `backend: firecracker`: create → running, exec streams, file
  round-trip, destroy — all via the public API.
- Honest capability boundary: `save` on a Firecracker-backed sandbox returns a typed
  `snapshot_unsupported` error (plan 11's snapshots are docker-commit-based and plan 14
  excludes VM snapshotting). The API must say so, not 500 — and the plane should also
  run the docker backend as a second registered option on this host so save/restore
  can be validated in prod on docker-backed sandboxes.
- Auth is enforced (no key ⇒ 401) and the plane's plaintext port and a guest port are
  **not** reachable from the internet (probe and assert connection refused/filtered).
- Reboot resilience: `ssh reboot`, then within a bounded window `/v1/status` is green
  and a new sandbox can be created (systemd + reconciler did their jobs).
- Deploy safety: `hack/deploy.sh` with a deliberately broken binary rolls back and the
  API stays serving the old version.

Plus: the plan 12 demo harness completes one task against prod with only env-var
changes, and its latency table gains a prod column (Mac→Hetzner RTT now included).
Because Firecracker sandboxes cannot restore from snapshots, the harness needs a
config switch: snapshot-restore on docker backends, plain-image create (slower first
step, stated in the latency table) on firecracker. Add that switch as a plan 12 task
if 16 is known to be coming — it is one flag, and it keeps the demo honest on both
backends.

Deliberately untested: sustained load, disk-full behavior, Hetzner outages, multiple
concurrent deploys.

## Solo-developer tradeoffs

No Kubernetes, no Terraform, no CI/CD pipeline, no blue-green: for one host, each of
those adds more failure modes than it removes, and cloud-init + a deploy script keeps
the whole prod story readable in two files — itself a resume-legible judgment call.
The real risks accepted and written down: single point of failure (fine for a demo),
brief deploy downtime, and secrets managed by file permissions rather than a vault.
What is *not* compromised: TLS-only exposure, a firewalled plane, hashed keys, and
rollback-capable deploys — the floor below which "demo" becomes "liability."
