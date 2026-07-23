# 12 — Capstone: agent harness on Quickspin

Depends on: 09 (or 10), 11 recommended.

## Summary

The end-to-end goal from the project's founding description: a **separate repo**
(`quickspin-demo` or similar) that imports your SDK, onboards to the platform with an
API key, and runs an LLM agent loop that develops working code inside a Quickspin
sandbox — writing files, running tests, reading failures, iterating until green, and
handing back the result. This plan is mostly integration and dogfooding; the code you
write is a small tool-loop harness, and every rough edge you hit is filed against
earlier components.

## Industry context

This is the customer's-eyes view: it is exactly what E2B's "code interpreter" quickstart
and Anthropic's tool-use loops do — the LLM never touches the host; every `write_file`
/ `run_command` tool call becomes an SDK call into an isolated sandbox. Platforms live
or die on how this loop feels: sandbox startup latency shows up as user-visible agent
latency (your plan 11 snapshots exist for exactly this), TTLs protect you from crashed
harnesses (plan 06), and typed SDK errors decide whether the agent can recover or just
dies (plans 09/10). Dogfooding the platform through a real agent is how every platform
team finds its actual API gaps.

## What you'll learn

- The consumer's view of your own API: what's awkward, what's missing, what's slow.
- A minimal agent tool loop with the Anthropic API: tool definitions
  (`write_file`, `run_command`, `read_file`, `task_done`), executing tool calls against
  the SDK, feeding results back.
- Failure handling at the harness level: sandbox died mid-task, quota hit, exec
  timeout — mapping typed SDK errors to agent-visible outcomes.
- Latency budgeting: where the seconds go in one agent step.

## Design and interfaces

Demo repo shape (TypeScript, matching your strongest language; the harness is ~200
lines and not the learning focus):

```text
quickspin-demo/
  harness.ts        agent loop: task in, Anthropic tool loop against one sandbox
  tools.ts          tool schemas + dispatch to @quickspin/sdk
  tasks/            2–3 task prompts of increasing difficulty (see DoD)
  run.ts            CLI: create sandbox (from a deps snapshot), run task, print
                    transcript + verdict, kill sandbox in finally
```

Committed decisions: the harness restores from a pre-built snapshot (language
toolchain + test runner preinstalled) so agent steps aren't dominated by installs; the
sandbox is created per-task and killed in a `finally`; TTL + keepalive is the crash
safety net; the model verifies its own work by running tests in the sandbox — the
harness's verdict is only "did the task's check command exit 0".

## Tasks

1. Scaffold the demo repo; import the SDK as a path/git dependency; onboard with a
   freshly minted key (record the onboarding steps — that friction list is a
   deliverable).
2. Build the snapshot the harness restores from (a small `setup.ts` using the SDK).
   Make the starting point a config switch — snapshot name or plain image — because
   plan 16 runs this same harness against Firecracker-backed prod, where snapshots are
   unsupported and the harness must fall back to image-based creates.
3. Implement the tool loop.
4. Author three tasks with machine-checkable success commands, e.g.:
   (a) "make this failing test pass" (repo pre-seeded in the snapshot),
   (b) "implement a CLI that does X, with tests" from an empty dir,
   (c) one task that intentionally tempts long-running commands (exercises timeouts).
5. Run all tasks; keep a friction log; file each item as a note against its plan.
6. Write `docs/reference/demo-latency.md`: per-step timing for one full task run.

## Definition of done

This plan's validation is the demo itself, scripted:

- `npm run demo -- tasks/a` (and b, c) each: creates a sandbox from snapshot, runs the
  agent loop, and exits 0 **iff** the task's check command passed inside the sandbox.
- A `--chaos` flag on one run kills the sandbox mid-task (via the admin API/CLI) and
  the harness surfaces a clean typed-error outcome, not a hang or stack trace.
- After any demo run (success or failure), `quickspin sandbox list` for the demo tenant
  is empty — nothing leaks even when the harness is killed with Ctrl-C (TTL covers the
  worst case; assert the `finally` covers the normal case).
- The friction log exists with at least the honest contents (an empty friction log
  fails review by definition).

Deliberately untested: multi-sandbox parallel agents, cost accounting of model tokens,
non-Anthropic model providers.

## Solo-developer tradeoffs

A commercial demo would be a hosted playground with streaming UI; yours is a CLI that
prints a transcript — right-sized, and closer to how platform engineers actually smoke-
test. Using your own harness rather than adopting a framework (LangChain etc.) keeps
the dependency surface tiny and makes the demo legible in an interview: every line
between "LLM decided" and "sandbox executed" is code you can explain. This plan is also
the natural stopping point for the résumé artifact — 13 and 14 are depth, not breadth.
