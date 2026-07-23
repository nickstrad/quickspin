# 10 — Python SDK

Depends on: 09.

## Summary

Create `sdk/python/` (`quickspin` package): the same client surface as the TypeScript
SDK, in idiomatic Python (sync-first, context managers, `httpx`). As with plan 09,
**Claude generates it; you review and gate on contract tests**. The learning payload
for you is smaller and different: watching whether your API survives a second language
untouched — every place the Python SDK needs a workaround is an API bug to fix in Go.

## Industry context

Python is the default language of agent frameworks (LangChain, CrewAI, OpenAI/Anthropic
tool loops), so a sandbox platform without a Python SDK doesn't exist to half the
market — E2B and Daytona both lead with Python. The industry pattern exercised here is
**API-first symmetry**: both SDKs derive from the same OpenAPI contract and pass
equivalent contract suites, which is the only scalable answer to N languages. Where
TypeScript is async-native, Python platforms typically ship sync-first with an async
variant later; this plan follows that.

## What you'll learn

- Reading your API through a second language's eyes: naming (`ttlSeconds` vs
  `ttl_seconds`), error-code stability, streaming ergonomics (`for ev in
  sbx.exec_stream(...)` over generators).
- Keeping two contract suites equivalent — and noticing when one suite asserts
  something the other forgot.
- Light Python packaging (`pyproject.toml`, `uv`) as consumer, not expert.

## Design and interfaces

```python
with Sandbox.create(image="python:3.12-slim", ttl_seconds=600) as sbx:
    res = sbx.exec("python", ["-c", "print(1)"], timeout_s=30)
    res.exit_code; res.stdout; res.stderr
    for ev in sbx.exec_stream("make", ["test"]):
        ...
    sbx.files.write("/app/main.py", code)
    text = sbx.files.read("/app/main.py")
# context-manager exit kills the sandbox

Sandbox.connect(sandbox_id)   # reattach without killing on exit unless asked
```

Committed decisions: `httpx` as the only runtime dependency; typed exceptions mirror
plan 09's names (`QuotaExceededError`, `SandboxNotFoundError`, `AuthenticationError`);
key from `QUICKSPIN_API_KEY`; the context manager kills on exit for `create` but not
for `connect` (attachment ≠ ownership — document this).

## Tasks

1. (Claude) Generate the package, mirroring the TS SDK's structure and README.
2. (You) Review against `openapi.yaml`; file every friction point as either a Python
   fix or a Go API fix — keep that list, it goes in the closing notes.
3. (Both) Port the contract suite to pytest; assert the **same behaviors** as plan 09's
   Vitest suite, plus the context-manager semantics.
4. `make test-sdk-py` boots a throwaway plane and runs pytest (same harness pattern as
   `test-sdk-ts`; extract the shared boot script into `hack/testplane.sh`).

## Definition of done

`make test-sdk-py` passes with the pytest equivalents of every plan 09 contract test,
plus:

- `test_context_manager_kills_on_exit` — sandbox is gone after the `with` block, even
  when the block raises.
- `test_connect_does_not_kill_on_exit`.
- `test_stream_yields_before_completion`.

Cross-SDK parity check: a short doc table (in the plan's closing notes) mapping each TS
contract test to its pytest twin, with gaps explained — parity is asserted by review,
not tooling, and saying so is part of done.

Deliberately untested: asyncio variant, Python < 3.11, Windows.

## Solo-developer tradeoffs

Commercial platforms generate both SDKs from the spec in CI and publish to npm/PyPI on
tags with semver guarantees. You keep the packages in-repo and unpublished until the
plan 12 demo needs to import them (a git or path dependency is fine for a learning
platform and avoids premature registry ceremony). Skipping asyncio-first is deliberate:
sync code keeps the demo harness readable, and the API's streaming already proves the
hard part works.
