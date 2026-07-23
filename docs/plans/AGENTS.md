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
6. **Hint steps** — a progressive set of rough implementation outlines the user can
   reveal when stuck. See the guidance below.
7. **Definition of done** — see below. This section is mandatory and must be verifiable.
8. **Solo-developer tradeoffs** — what a commercial platform would do differently and
   why this plan deliberately does less (or different), so shortcuts are informed
   decisions rather than accidents.
9. **Go deeper** — external resources (official docs, articles, books, talks, papers)
   for studying the plan's concepts beyond what the plan itself teaches. Scale the
   section to the plan's conceptual depth: a setup plan may list two doc links; a
   deep-dive plan deserves books and talks. Use the `Resources`/`Resource` MDX
   components. Every linked URL must be real and verified — a resource with no
   trustworthy URL is listed by title/author/venue without a link. Each entry needs a
   one-line note saying what specifically it adds beyond the plan.

## Use MDX to teach, not merely to decorate

Plans use MDX so they can be more useful than static task lists. Make each plan a
self-contained study guide that explains the concepts needed to implement it:

- Include concise conceptual writeups near the task that uses the concept. Explain why
  the mechanism exists, how it behaves, and which failure mode makes it relevant.
- Use diagrams when a relationship, lifecycle, state machine, request path, ownership
  boundary, or event sequence is easier to understand visually than as prose.
- Use tables for exact comparisons, mappings, invariants, and tradeoffs.
- Use callouts for warnings, design commitments, Linux-specific behavior, and places
  where the learning implementation deliberately differs from a commercial system.
- Use interactive study components when they help the user test their understanding or
  progressively reveal help. The components available to plans are documented in
  [`docs/reader-guide.mdx`](../reader-guide.mdx).
- Keep the prose technically substantial. Visual treatment should clarify useful
  information, not pad the document or turn every paragraph into a component.
- A plan may describe interfaces and show small illustrative snippets, but it must not
  contain the completed implementation the user is meant to write.

## Hint steps must guide without solving

Every plan must contain a `## Hint steps` section after `## Tasks`. It exists for the
user to consult only when blocked.

- Mirror the task order and provide a rough path that would ultimately complete the
  plan.
- Make hints progressive: begin with the next question to answer or file to inspect,
  then name the relevant API or system mechanism, and only then outline the likely
  code shape.
- Explain what observable result should appear after each hint so the user can tell
  whether they are back on track.
- Point to relevant Go, Linux, protocol, or library documentation and name useful
  commands when appropriate.
- Include warnings about the most likely conceptual traps and failure cases.
- Do not provide full function bodies, copy-paste-ready solutions, or a sequence so
  detailed that implementation becomes transcription.
- Prefer collapsible or progressive-reveal MDX components so the main plan remains
  readable before the user asks for help.
- End with a diagnostic hint for the plan's negative test or most likely failure mode;
  success-path hints alone are insufficient.

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

- File names: `NN-short-slug.mdx`, numbered in intended order. Later plans may depend on
  earlier ones; state dependencies explicitly at the top (`Depends on: 03, 05`).
- Plans define interfaces; the user writes the Go implementations. SDK plans (TypeScript,
  Python) are the exception: there the agent may generate client code and the user's job
  is review plus making the contract tests pass.
- When a plan completes, move it to `closed/`, note the completion date and any
  deviations from the plan at the top, and update `docs/index.mdx`.
- Plans are MDX documents. Ordinary Markdown is valid, but use MDX deliberately to make
  concepts, diagrams, and progressive hints easier to study.
