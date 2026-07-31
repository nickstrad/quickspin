# Roadmap documents

This directory holds Quickspin's implementation roadmaps. `open/` contains proposed or
in-progress roadmaps; `closed/` contains completed roadmaps kept as history. Moving a file
records lifecycle only — a roadmap is implemented when the user asks, not because it exists.

## Required structure

Every roadmap in this directory must contain these sections, in this order:

1. **Summary** — what will be built, in current/future tense that never implies it
   already exists.
2. **Industry context** — how commercial platforms (E2B, Daytona, Modal, Vercel Sandbox,
   Fly Machines, etc.) solve the same problem, and how this roadmap's piece integrates with
   the platform being built here.
3. **What you'll learn** — the concrete Linux, Go, distributed-systems, or API-design
   concepts the roadmap is designed to teach. This is a learning project; a roadmap that
   builds something without teaching something is mis-scoped.
4. **Design and interfaces** — the Go interfaces, types, HTTP contracts, or schemas the
   roadmap commits to. Interfaces may be fully specified in the roadmap (the user implements
   them); implementations must not be.
5. **Tasks** — an ordered list of small steps, each independently checkable.
6. **Hint steps** — a progressive set of rough implementation outlines the user can
   reveal when stuck. See the guidance below.
7. **Definition of done** — see below. This section is mandatory and must be verifiable.
8. **Solo-developer tradeoffs** — what a commercial platform would do differently and
   why this roadmap deliberately does less (or different), so shortcuts are informed
   decisions rather than accidents.
9. **Go deeper** — external resources (official docs, articles, books, talks, papers)
   for studying the roadmap's concepts beyond what the roadmap itself teaches. Scale the
   section to the roadmap's conceptual depth: a setup roadmap may list two doc links; a
   deep-dive roadmap deserves books and talks. Use the `Resources`/`Resource` MDX
   components. Every linked URL must be real and verified — a resource with no
   trustworthy URL is listed by title/author/venue without a link. Each entry needs a
   one-line note saying what specifically it adds beyond the roadmap.

## Use MDX to teach, not merely to decorate

Roadmaps use MDX so they can be more useful than static task lists. Make each roadmap a
self-contained study guide that explains the concepts needed to implement it:

- Include concise conceptual writeups near the task that uses the concept. Explain why
  the mechanism exists, how it behaves, and which failure mode makes it relevant.
- Use diagrams when a relationship, lifecycle, state machine, request path, ownership
  boundary, or event sequence is easier to understand visually than as prose.
- Use tables for exact comparisons, mappings, invariants, and tradeoffs.
- Use callouts for warnings, design commitments, Linux-specific behavior, and places
  where the learning implementation deliberately differs from a commercial system.
- Use interactive study components when they help the user test their understanding or
  progressively reveal help. The components available to roadmaps are documented in
  [`docs/reader-guide.mdx`](../reader-guide.mdx).
- Keep the prose technically substantial. Visual treatment should clarify useful
  information, not pad the document or turn every paragraph into a component.
- A roadmap may describe interfaces and show small illustrative snippets, but it must not
  contain the completed implementation the user is meant to write.

## Hint steps must guide without solving

Every roadmap must contain a `## Hint steps` section after `## Tasks`. It exists for the
user to consult only when blocked.

- Mirror the task order and provide a rough path that would ultimately complete the
  roadmap.
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
- Prefer collapsible or progressive-reveal MDX components so the main roadmap remains
  readable before the user asks for help.
- End with a diagnostic hint for the roadmap's negative test or most likely failure mode;
  success-path hints alone are insufficient.

## Definition of done must be verifiable

Every roadmap must define completion as something a machine can check, not a feeling:

- Prefer **TDD**: the roadmap lists the test names/behaviors to write first (e.g.
  `TestDestroyIsIdempotent`, `TestExecKillsProcessOnContextCancel`) and done means
  `make test` passes with those tests present and meaningful.
- Where unit tests cannot capture the behavior (environment setup, real containers,
  cross-process behavior), the roadmap must specify a **validation script** under `hack/`
  (e.g. `hack/validate-05.sh`) that exits 0 only when the roadmap's observable guarantees
  hold, and prints what it checked.
- Failure behavior counts: a roadmap whose happy path runs once is not done. Each roadmap
  should include at least one negative/failure check (timeout, crash, retry, leak).
- The definition of done must also state what remains **deliberately untested** so the
  gap is recorded rather than forgotten.

## Conventions

- File names: `NN-short-slug.mdx`, numbered in intended order. Later roadmaps may depend on
  earlier ones; state dependencies explicitly at the top (`Depends on: 03, 05`).
- Roadmaps define interfaces; the user writes the Go implementations. SDK roadmaps (TypeScript,
  Python) are the exception: there the agent may generate client code and the user's job
  is review plus making the contract tests pass.
- When a roadmap completes, move it to `closed/`, add
  `{/* Completed YYYY-MM-DD. <deviation note> */}` below its dependency line, and update
  `docs/index.mdx`. The reader uses that note as completion metadata.
- Roadmaps are MDX documents. Ordinary Markdown is valid, but use MDX deliberately to make
  concepts, diagrams, and progressive hints easier to study.
