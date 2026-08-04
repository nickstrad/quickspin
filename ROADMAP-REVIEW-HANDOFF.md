# Roadmap review handoff

Context for a future session with a small context window. A full read of
`docs/index.mdx` and all open plans (07–25) produced the suggestions below. Nothing
has been edited yet; each item names its target files and the exact change. Items
marked **[confirm]** are judgment calls the user should approve before editing;
items marked **[mechanical]** follow directly once the user says "do the handoff
items."

Ground rules for whoever works this: these are doc edits only (no Go code exists
for any open roadmap). Keep `docs/index.mdx` in sync with any plan change. Plans
renumbering is expensive (cross-references everywhere) — prefer edits that avoid
renumbering unless told otherwise.

## 0. [decided] Direction change: gVisor replaces Firecracker in the core arc

User-approved. The core isolation upgrade becomes **gVisor (runsc)**, not
Firecracker; the microVM material (Firecracker, Kata) moves to OPTIONAL
deep dives at the end of the roadmap, alongside 25.

Concrete file moves (avoids renumbering 18–25):

- Rewrite `docs/plans/open/17-firecracker-backend.mdx` →
  `17-gvisor-backend.mdx`, a new plan authored from scratch.
- Move the current Firecracker content to a new
  `docs/plans/open/26-firecracker-backend.mdx`, header marked **optional deep
  dive** (same status as 25).
- `19-kubernetes-kata-backend.mdx` stays in place, header marked **optional
  deep dive** (this absorbs old item 7 below).
- Update `docs/index.mdx`: list entries, and rewrite the ordering paragraph's
  compute-track sentence — the compute track is now "17 (gVisor) in the core
  arc; 19, 25, 26 optional."

Shape of the new gVisor plan (from the review discussion):

- gVisor is an OCI runtime, so it slots **under the existing Docker backend**
  as a per-sandbox runtime handler (`--runtime=runsc` at container create),
  not a second `Runtime` implementation. A `Spec` field selects isolation.
- Everything keeps working unchanged: guest-agent injection, port mapping,
  exec paths, and `docker commit` snapshots — the old cutover regression
  (`snapshot_unsupported` on the default backend) never exists.
- No hardware gate: systrap platform needs no `/dev/kvm`, no nested virt, no
  M3+ requirement; runs anywhere Lima runs.
- Industry anchor: Modal, Google Cloud Run, and GKE Sandbox run gVisor — it is
  the competing production answer to Firecracker's threat model (userspace
  kernel intercepting syscalls vs. separate kernel behind KVM).
- The plan's engineering content: installing runsc into the Lima VM's Docker
  daemon (runtimes config), routing the spec field through plane/CLI,
  the compatibility boundary (syscalls the Sentry doesn't implement) as a
  tested/observed surface, and a syscall-heavy vs. IO-heavy benchmark added to
  `docs/reference/demo-latency.mdx` under roadmap 10's rules. Isolation smoke
  checks: `dmesg`/kernel identity differs inside, host resources unreachable.
- Prod note: gVisor on the Hetzner host is a config change to the same Docker
  backend — the prod "cutover" becomes flipping the default runtime handler,
  reversible, with snapshots still working. 12's Hetzner-for-KVM rationale
  softens to "cheap, and KVM available if the optional microVM track is ever
  done" — touch that wording in 12 and the index.

Ripples into items below: 1 (18's KVM story), 6 (superseded), 7 (absorbed),
9 (minimum arc names gVisor, not Firecracker).

## 1. [decided] Roadmap 18 moves to the optional tail (updated for gVisor)

`docs/plans/open/18-ec2-digitalocean-providers.mdx`, `docs/index.mdx`

18's stated purpose was providers *for Firecracker pools*, and its shaping
constraint was `/dev/kvm`. With item 0 making Firecracker optional, 18 goes
optional wholesale: mark it an optional deep dive tied to the microVM track
(19/26). gVisor needs no KVM, so the core arc's only provider is Hetzner
(built in 14's provisioning half, which item 4 moves after 12) — cheap, simple
API, and KVM-capable if the optional track is ever picked up. If 18 is ever done, reduce it to **DigitalOcean only** and
shrink the EC2 content (terminated-instance lingering, base64 user-data, metal
shape table) to a "road not taken" note; it was the 3rd/4th `Provider`
implementation and 3rd conformance-suite rep, with the worst cost profile
($4/hr metal, undocumented DO nested virt).

## 2. [decided] Split roadmap 16 — keep enrichment + usage records, defer pricing/invoices

`docs/plans/open/16-events-usage-metering.mdx`

Event enrichment (actor/reason/tenant_id) and the checkpointed usage processor are
worth keeping. Daily rollups → pricing engine → monthly invoices is billing
machinery for a platform with one tenant. Suggestion: scope the roadmap to end at
`usage_records` (wipe-and-replay derivability test still works there); move
rollups/pricing/invoice to a "recorded sequel" section, the way autoscaling is
handled in 14/15. Trim tasks 4–5, the invoice DoD lines, and the pricing concept
section accordingly.

## 3. [decided] Remove roadmap 21 for now — snapshots are the first approach to agent state

`docs/plans/open/21-agent-storage-provisioning.mdx`, `docs/index.mdx`

User-approved. Durable agent state is served first by roadmap 10's snapshots
(save/restore preserves the filesystem — installed deps, written code, local
SQLite files an agent keeps); brokered cloud databases are premature until a
real consumer outgrows that. Remove 21 from the active roadmap: either delete
the file or (cheaper, preserves the thinking) move it to the optional tail with
a header note stating snapshots-first and the trigger for revisiting ("an agent
whose state must outlive snapshots or be shared across sandboxes"). Nothing
downstream consumes it — 24 depends on 22 + 10 only — so no other plan needs
edits beyond the index. The one idea worth carrying forward regardless:
DSN-on-the-secret-rail as the test of 20's Secret abstraction, provable with a
fake provisioner if 21 ever returns.

## 4. [decided] Lima ladder replaces early prod; 12 becomes a late, cheap cloud spin-up

`docs/plans/open/12-prod-single-host.mdx`,
`docs/plans/open/13-host-registration-placement.mdx`,
`docs/plans/open/14-host-provisioning-observability.mdx`, `docs/index.mdx`.

User-approved, superseding the earlier "prod stays one host" version of this
item. The execution order becomes an all-local Lima ladder, with cloud last:

1. one Lima host (through 11, as today);
2. two statically-configured Lima hostds — build the scheduling logic
   (hosts table, pure `place`, the `FOR UPDATE` reservation race);
3. n Lima hosts — registration, heartbeats, two-threshold death detection,
   the failure suite; the static host config is deleted here, not before
   (13's "no fallback" rule applies at this step);
4. then, optionally and repeatably, one cloud host.

Do NOT renumber files. The index's ordering paragraph carries execution order
in prose: rewrite it to state the ladder and that 12 runs after 13.

Concrete edits:

- **13**: reorder tasks so placement + transactional reservation come first
  against statically seeded host rows (two Lima hostds), and
  registration/heartbeat/death detection + `hack/failure-suite.sh` come
  second. Drop the prod tasks ("Move prod to registration, then add a second
  host by hand") and the prod lines in the DoD and hint steps entirely — there
  is no prod yet at this point. Note the accepted throwaway: the static
  multi-host config built in the first half is deleted by the second half.
  13's `Depends on:` drops 12 and keeps 08, 11 (the placement race test still
  needs real Postgres `FOR UPDATE`).
- **12**: rewrite as "deploy the already-multi-host-capable plane to one cloud
  host". Its purpose changes from milestone to capability: a deliberately
  cheap, repeatable spin-up — deploy script + cloud-init bring up one Hetzner
  (or whatever) host, hostd registers with the plane exactly as the Lima
  hostds do (no static-config prod fork; registration is the only path by
  then), run the hand-driven acceptance pass, tear down. Hourly cost, not a
  standing bill; prod is a mode you can enter any time, not a gate. Keep
  12's real content — TLS, systemd, env-file secrets, deploy script,
  acceptance checklist — it all survives; only the position and the
  static-host wiring change. `Depends on:` gains 13.
- **Lima-first invariant**, written into 12 and the index: every roadmap's
  definition of done validates in Lima; the cloud host runs the identical
  registration and deploy path, so nothing later forks on "local vs prod".
  Running the prod setup logic must always work against whatever has been
  built so far, because it is the same path.
- **14** splits along its halves: the observability half (Prometheus, Grafana,
  alerts) has no cloud dependency and can run against Lima hosts any time
  after 13; the Hetzner provisioning half follows 12. The old "second host as
  a same-day demo" point is moot — the n-Lima-host step is the multi-host
  proof at zero cost.

Decision recorded (user accepted the tradeoff): this inverts the index's
"coding workloads only arrive after prod has been accepted by hand" principle —
22/24 may run against Lima before any cloud host exists. TLS/systemd/network
learning arrives late; the mitigation is that 12 stays cheap enough to run
early as a spot-check if wanted.

## 5. [decided] Insert a tracer harness after 09 (no renumbering)

User-approved. Anchor: run the harness right after 09. The workload surface it
drives (create, exec-stream, files, destroy) is complete at 08, and 09 is the
last milestone that changes the client-visible surface (auth headers, typed
auth/quota errors in the taxonomy) — everything later (10/11/13) changes
internals, not ergonomics. Running after 09 is the earliest honest point and
leaves maximum runway before 22 freezes the contract. Home for the text: a
small closing section in `docs/plans/open/09-auth-tenancy-quotas.mdx`.

24's friction log is the roadmap's only real-consumer feedback, and it arrives
after the OpenAPI contract freezes (22). Cheap insurance: ~100-line raw-HTTP
agent loop (no SDK, no separate repo) run once — create, exec-stream,
files, destroy driven by an LLM tool loop against the real plane in Lima.
Deliverable is an early friction log against streaming ergonomics and the error
taxonomy while the API is still cheap to change. Does not violate SDKs-last (no
SDK involved).

Bonus under item 4's ladder: the same script doubles as a reusable acceptance
probe — re-run it after 11's Postgres swap, after 13's placement, and against
the 12 cloud spin-up. Same script, same path: the Lima-first invariant made
testable. The 09 section should note this re-run role in one line.

## 6. [superseded by 0] Roadmap 17: don't flip the prod default backend

The concern (cutover breaks `save` on the default backend, 24 needs a fallback)
disappears under gVisor: snapshots keep working because gVisor sandboxes are
still Docker containers. The residue of this item lives in item 0's prod note.
When rewriting 17, also clean up the forward references this item named: 12's
"roadmap 17's cutover" mentions and 24's snapshot-vs-image backend switch (the
switch can stay as generic config hygiene, but it no longer has a driving case).

## 7. [absorbed into 0] Label roadmap 19 optional

Handled by item 0's file moves (19 marked optional deep dive alongside 25/26).
Keep this item's rationale in the 19 header note: most expensive plan in the
set, and single-node k3s undercuts its own delegation lesson.

## 8. [mechanical] Trim 07's `generation` framing to its honest justification

`docs/plans/open/07-guest-agent-api.mdx` (summary, "generation column counts
change" concept, key-concepts bullet).

07 sells `generation` as optimistic-concurrency/fencing groundwork for 08 and 13,
but 13 explicitly skips leases and no roadmap ever does CAS on it. Its only real
consumer is 16's event ordering (total order within a clock second, gapless
sequence). Keep the column; rewrite the rationale around event ordering and drop
the fencing promise (or demote it to one "possible future use" clause).

## 9. [mechanical] Add a "minimum complete arc" to the index

`docs/index.mdx`.

19 open roadmaps at this depth is plausibly 1.5–3 years solo, and value density
drops after 17. Add a short paragraph defining the finish line short of the full
tree, using item 4's ladder order: **07 → 08 → 09 → 10 → 11 → 13, then 17
(gVisor), 22, 24, with 12 as the repeatable cloud spin-up whenever wanted** — a
multi-host Lima platform, one isolation upgrade, one SDK, the capstone, and
prod as an on-demand mode rather than a gate. Items 1–3 above all
fall outside this arc. Also note 20's independence (depends only on 03/04) as a
change-of-pace option alongside 25.

## 10. [decided] Decide the 10 ↔ 11 order

`docs/plans/open/10-snapshot-save-restore.mdx`,
`docs/plans/open/11-postgres-store.mdx`, `docs/index.mdx`.

10-before-11 means snapshot tables are built on SQLite then immediately ported to
Postgres; 11's header already hedges ("10's snapshot tables if already
implemented"). Swapping avoids the porting tax but delays the more motivating
roadmap and forces a renumber — either order is defensible. If keeping the
current order (the cheap choice), add one line to 10 committing to
migration-portable SQL (the roadmap 05 portability rules) so the port stays small;
if swapping, renumber the two files and fix headers, cross-references, and the
index list/ordering paragraph.

## 11. [mechanical] Note 20's independence in its own header

`docs/plans/open/20-git-capable-sandboxes-secrets.mdx` (the `Depends on:` line or
summary).

20 depends only on 03/04 and can run any time as a change of pace, but only the
index's ordering paragraph says so. Add one sentence to 20 itself so the fact
survives being read in isolation — same treatment 25 gives its own independence.

## Praise — don't undo while editing

The 07/08 spine, 12's Hetzner-for-KVM choice, 13's dead-host→failed design
leaning on 10's snapshots as the recovery story, and the honest
Deliberately-untested sections throughout. Edits above should trim and re-scope,
not restructure these.
