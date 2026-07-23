# Plan documents

This directory holds Quickspin's implementation plans. `open/` contains proposed or
in-progress plans; `closed/` contains completed plans kept as history. Moving a file
records lifecycle only — a plan is implemented when the user asks, not because it exists.

## Required structure

Every plan in this directory must contain these sections, in this order:

1. **Summary** — what will be built, in current/future tense that never implies it
   already exists.
2. **Industry context** — how commercial platforms (E2B, Daytona, Modal, Vercel Sandbox,
   Fly Machines, etc.) solve the same problem, and how this plan's piece integrates with
   the platform being built here.
3. **What you'll learn** — the concrete Linux, Go, distributed-systems, or API-design
   concepts the plan is designed to teach. This is a learning project; a plan that
   builds something without teaching something is mis-scoped.
4. **Design and interfaces** — the Go interfaces, types, HTTP contracts, or schemas the
   plan commits to. Interfaces may be fully specified in the plan (the user implements
   them); implementations must not be.
5. **Tasks** — an ordered list of small steps, each independently checkable.
6. **Definition of done** — see below. This section is mandatory and must be verifiable.
7. **Solo-developer tradeoffs** — what a commercial platform would do differently and
   why this plan deliberately does less (or different), so shortcuts are informed
   decisions rather than accidents.

## Definition of done must be verifiable

Every plan must define completion as something a machine can check, not a feeling:

- Prefer **TDD**: the plan lists the test names/behaviors to write first (e.g.
  `TestDestroyIsIdempotent`, `TestExecKillsProcessOnContextCancel`) and done means
  `make test` passes with those tests present and meaningful.
- Where unit tests cannot capture the behavior (environment setup, real containers,
  cross-process behavior), the plan must specify a **validation script** under `hack/`
  (e.g. `hack/validate-05.sh`) that exits 0 only when the plan's observable guarantees
  hold, and prints what it checked.
- Failure behavior counts: a plan whose happy path runs once is not done. Each plan
  should include at least one negative/failure check (timeout, crash, retry, leak).
- The definition of done must also state what remains **deliberately untested** so the
  gap is recorded rather than forgotten.

## Conventions

- File names: `NN-short-slug.md`, numbered in intended order. Later plans may depend on
  earlier ones; state dependencies explicitly at the top (`Depends on: 03, 05`).
- Plans define interfaces; the user writes the Go implementations. SDK plans (TypeScript,
  Python) are the exception: there the agent may generate client code and the user's job
  is review plus making the contract tests pass.
- When a plan completes, move it to `closed/`, note the completion date and any
  deviations from the plan at the top, and update `docs/index.md`.
