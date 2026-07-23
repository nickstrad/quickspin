# Sandbox Infrastructure Learning Path

This document suggests one route for learning how scalable agent sandbox infrastructure
works. It is forward-looking reference material, not a specification or an approved
implementation plan.

The path begins with a single local sandbox and progressively exposes lifecycle,
control-plane, scheduling, performance, and isolation concerns. Each stage should answer
its own questions before the next stage adds complexity.

## 1. Run one isolated sandbox locally

Build a Go runtime that manages a local Docker container through a small lifecycle:

```text
create -> inspect -> execute -> destroy
```

Start with an explicit resource and execution contract:

- An image identifies the starting filesystem and installed software.
- CPU, memory, and process limits constrain the workload.
- A network policy states whether outbound access is allowed.
- Execution preserves stdout, stderr, and the exit code separately.
- Context cancellation and deadlines terminate work predictably.
- Destroying a missing sandbox succeeds, making cleanup safe to retry.

Use a Go CLI to exercise this lifecycle before adding HTTP. Verify that failed creation,
timed-out commands, and repeated cleanup do not leak containers or child processes.

### Questions this stage should answer

- Which identity belongs to Quickspin, and which identity belongs to the container
  runtime?
- Which operations must be idempotent?
- What does cancellation guarantee?
- Which isolation properties are real, and which are only conventions?
- What evidence proves that resource limits and cleanup work?

## 2. Put a local control plane around the runtime

Expose the lifecycle through a Go HTTP service while keeping the runtime behind an
internal boundary. The control plane should allocate stable sandbox IDs instead of
returning backend-specific container IDs.

Add only enough state to learn control-plane behavior:

- Stable sandbox records and lifecycle states
- Idempotency keys for retried requests
- Expiring leases or deadlines
- A small local persistent store such as SQLite
- An event log for lifecycle transitions
- A reconciliation loop that compares desired state with actual runtime state

The reconciler is an important infrastructure pattern: API calls update desired state,
and repeated observation moves the runtime toward that state. Restarting the control
plane should reveal which facts must be durable and which can be rediscovered.

### Questions this stage should answer

- What happens when the service stops halfway through creation?
- Can a sandbox be rediscovered after a control-plane restart?
- Which state transitions are legal?
- How are abandoned sandboxes detected and removed?
- Which failures are safe to retry?

## 3. Add the TypeScript SDK

Build the SDK after the HTTP behavior is useful and observable. Keep it small enough that
it expresses remote resource semantics without duplicating control-plane policy.

An initial surface might support:

```text
sandboxes.create(...)
sandbox.inspect()
sandbox.commands.run(...)
sandbox.close()
```

Define typed errors for timeouts, unavailable capacity, invalid lifecycle transitions,
and failed commands. Decide deliberately whether command output is buffered or streamed;
do not imply streaming through a buffered implementation.

### Questions this stage should answer

- Which transport details should the SDK hide?
- How does cancellation cross the TypeScript, HTTP, Go, and runtime boundaries?
- Which operations return immediately, and which wait for readiness?
- Can clients safely retry after losing a response?

## 4. Separate the control plane from runtime workers

Move sandbox execution into one or more Go worker processes. The control plane now chooses
a worker, and workers report their health and available capacity.

Introduce the minimum distributed-system mechanisms needed to make this honest:

- Worker registration and heartbeats
- Capacity accounting for CPU and memory
- Placement decisions
- A queue with explicit backpressure
- Ownership leases so two workers do not manage the same sandbox
- Recovery when a worker disappears

Run several workers locally before choosing a cloud platform. Fault-injection tests—killed
workers, delayed heartbeats, duplicate requests, and dropped responses—are more valuable
here than adding deployment machinery early.

### Questions this stage should answer

- Who owns a sandbox when messages are delayed or duplicated?
- How is capacity reserved and released atomically?
- What happens to work assigned to a dead worker?
- When should the system reject work instead of queueing it?
- How can reconciliation repair conflicting state?

## 5. Improve startup performance and reuse

Measure sandbox creation before optimizing it. When startup cost becomes a demonstrated
constraint, explore:

- Pre-pulled images
- Copy-on-write filesystem layers
- Prepared templates
- Snapshot and restore
- Warm pools
- Pausing and resuming sandboxes

Track cold-start and warm-start latency separately. Reuse introduces security and
correctness questions: residual processes, files, credentials, network connections, and
memory must not cross sandbox boundaries unintentionally.

### Questions this stage should answer

- Which parts of startup dominate latency?
- What state is captured by a template or snapshot?
- How is reused capacity returned to a known-clean state?
- When does keeping capacity warm cost more than it saves?

## 6. Deepen isolation and security

Once lifecycle and scheduling behavior are understood, replace or augment the initial
Docker backend to learn lower-level isolation:

- containerd and OCI runtimes
- Rootless containers and user namespaces
- cgroups for resource enforcement
- seccomp and Linux capabilities
- Filesystem and network isolation
- gVisor or another userspace kernel
- Firecracker or another microVM runtime

Treat isolation as a threat model rather than a backend label. Specify what the workload
must not be able to read, modify, exhaust, impersonate, or contact, then test those
boundaries.

### Questions this stage should answer

- What is trusted on the host and inside the sandbox?
- Which kernel attack surface is shared with workloads?
- How are secrets introduced and revoked?
- What outbound network access is necessary?
- Which isolation failures can the control plane detect?

## 7. Operate a multi-tenant fleet

Only after the local multi-worker system behaves predictably should deployment expand to
multiple hosts or a cloud environment. Add production concerns in response to measured
needs:

- Tenant quotas and rate limits
- Admission control
- Fair scheduling
- Regional capacity
- Audit logs
- Metrics, traces, and alerts
- Upgrade and drain procedures
- Cost and utilization accounting

At this point, cloud APIs are placement and capacity mechanisms beneath the same sandbox
lifecycle. The earlier local stages provide the behavioral reference used to judge each
new backend.

## Suggested milestone order

```text
one local container
    -> reliable lifecycle
    -> local control plane
    -> TypeScript SDK
    -> multiple local workers
    -> reconciliation under failures
    -> snapshots and warm pools
    -> stronger isolation
    -> multi-host deployment
```

Completing a milestone means its failure behavior is understood and tested, not merely
that its happy path runs once.
