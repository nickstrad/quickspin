# 09 — TypeScript SDK

Depends on: 07, 08.

## Summary

Create `sdk/typescript/` (`@quickspin/sdk`): a typed client for the control-plane API
with the E2B-style ergonomic surface agents expect — `Sandbox.create()`, `sbx.exec()`,
`sbx.files.*`, `sbx.kill()`. Per your direction, **Claude generates this SDK; your job
is review and contract-testing, not hand-writing TypeScript**. Your Go time in this
plan goes into the other deliverable: `GET /v1/openapi.json` served by the control
plane, making the API formally described.

## Industry context

The SDK is the actual product surface of platforms like E2B — nobody curls these APIs;
agents import `@e2b/code-interpreter` and call three methods. Conventions this SDK
copies deliberately: constructor/env-var API key resolution (`QUICKSPIN_API_KEY`),
resource-handle objects rather than bare functions, typed error subclasses
(`QuotaExceededError`, `SandboxNotFoundError`) so harnesses can branch on failure type,
and async iteration for streamed exec output. Serving an OpenAPI document is how
commercial platforms keep N SDKs honest against one API; yours will do the same work
in plan 10.

## What you'll learn

(Go-side, where your focus is:) API self-description — writing an OpenAPI 3.1 document
for your own endpoints and discovering where your API is irregular; error-envelope
stability as a compatibility contract. (Review-side:) judging an SDK you didn't write
against a spec you did.

## Design and interfaces

SDK surface (generated code must match this):

```ts
const sbx = await Sandbox.create({ image: "python:3.12-slim", ttlSeconds: 600 });
const res = await sbx.exec("python", ["-c", "print(1)"], { timeoutMs: 30_000 });
res.exitCode; res.stdout; res.stderr;               // buffered mode
for await (const ev of sbx.execStream("make", ["test"])) { ... } // streamed mode
await sbx.files.write("/app/main.py", code);
const text = await sbx.files.read("/app/main.py");
await sbx.keepalive();
await sbx.kill();                                    // idempotent, like the API
Sandbox.connect(id)                                  // reattach to an existing sandbox
```

Committed decisions: zero runtime dependencies (native `fetch`), works on Node 20+,
ndjson stream parsing in-house; errors map 1:1 from the API's `error.code` values.

## Tasks

1. (You, Go) Write `openapi.yaml` covering `/v1/*`, serve it at `/v1/openapi.json`,
   and add a CI check that it stays in sync with routes (a test enumerating the mux).
2. (Claude) Generate the SDK package: client, types, errors, stream parsing, README.
3. (You) Review the generated SDK against the OpenAPI doc and the surface above.
4. (Both) Contract test suite: Vitest tests that run against a **real** local control
   plane (started by the test script), not mocks.
5. Add `make test-sdk-ts` that boots the plane with a throwaway SQLite file, mints a
   key via the admin CLI, and runs the suite.

## Definition of done

`make test-sdk-ts` passes, including at minimum:

- `create → exec → files round-trip → kill` happy path.
- `execStream` yields output events before command completion (liveness assertion).
- Typed errors: bad key ⇒ `AuthenticationError`; over-quota ⇒ `QuotaExceededError`;
  killed sandbox exec ⇒ `SandboxNotFoundError`.
- `kill()` twice resolves both times.
- `Sandbox.connect()` reattaches and can exec.

Go-side: the route/OpenAPI sync test is in `make test`.

Deliberately untested: browser runtime, proxy environments, retry/backoff policy
(explicitly none in v0 — retries interact with idempotency keys and deserve their own
decision later).

## Solo-developer tradeoffs

Commercial SDKs are fully codegen'd (Stainless/Fern) with retries, pagination, and
telemetry baked in. A hand-shaped (if machine-written) client with zero dependencies is
easier to read end-to-end — which matters when the SDK is also a learning artifact and
a resume exhibit. The real discipline kept from industry: contract tests against a live
server, so the SDK can never silently drift from the plane.
