# Remote worker protocol v1

## Attempt workspace and mutable resources

Execute nodes and envelopes accept `workspaceMode` and `concurrencyKeys`.
Both SDK authoring APIs preserve these declarations.

* `snapshot` (default): the script executor copies regular files from the selected
  worker workspace into an attempt-owned writable directory. With no source it
  creates an empty workspace. No Git repository is required. Git metadata is
  excluded; symbolic links and special files fail preparation. Workspace inputs
  are rebound to the private copy. The source remains untouched.
* `read-only`: mounts the source read-only through a required isolation backend.
  Native execution rejects this mode because it cannot enforce it.
* `shared-write`: explicitly exposes the source as writable. Requires at least
  one exclusive key. Every task accessing the same mutable tree or external
  resource must declare the same key, including tasks in different runs.

For example, `{ "workspaceMode": "shared-write", "concurrencyKeys":
["repository:project", "deployment:staging"] }` reserves both resources with
limit one. Keys are opaque and global within the coordinator. They are checked
atomically with dispatch admission and survive coordinator restarts. Completion
releases them. Lost holders stay uncertain until reconciliation, including
requests declaring `effect: "none"`. Cancelled holders retain their keys until the worker confirms completion;
lease expiry alone does not prove that a process has stopped.

Keyed execution requires coordinator worker admission. Direct script execution
(including local `muxlio run --adapters`) rejects concurrency keys because it
cannot enforce the coordinator's durable reservations. Submit keyed workflows
to a coordinator with a registered worker instead.

Attempt scratch directories are allocated exclusively beneath the attempt ID;
restarting an attempt creates a fresh execution directory. Scratch must be outside
the snapshot source. Snapshots are independent copies, not OS security
boundaries: untrusted code must request `params.isolation.mode: "required"`.

Required isolation is eligible for admission only on workers advertising
`muxlio.dev/isolation/bwrap=true` or `muxlio.dev/isolation/oci=true`, matching
`params.isolation.runtime`. Operators must advertise only installed, enabled
backends. OCI also requires an image. Without an eligible worker the request
remains unadmitted; native fallback is forbidden. The executor checks actual
backend availability again before starting a process. Unimplemented backends
cannot satisfy admission.

Muxlio remote workers establish outbound HTTP connections to a coordinator.
They never connect to its database. The wire format is generic execution
infrastructure: workflow-specific meaning stays inside the opaque execute
request, capability names, JSON values, and artifact references.

## Versioning

The worker protocol has no authentication. Run it only on a trusted network.

The protocol version is `v1`, supplied as `protocolVersion` during registration
and heartbeat. A coordinator rejects unsupported major versions instead of
guessing. JSON uses UTF-8 and `application/json`; request bodies are limited to
4 MiB in the current HTTP handler. All error bodies have the common shape
`{"code":"...","message":"..."}`. Workers may retry 5xx and transport
failures with backoff. They must treat 4xx as a request/configuration issue
unless the error code documents a recoverable condition.

## Endpoints

All paths are rooted at `/v1/workers`.

| Method and path | Purpose | Success |
|---|---|---|
| `POST /register` | Register identity, protocol version, runtime, capacity, capability/version pairs, and optional labels. Capability names must be unique. | `200` with coordinator epoch and heartbeat interval. |
| `POST /{workerId}/heartbeat` | Renew liveness and individual fenced slot leases; report drain mode. | `200` with coordinator epoch, lease interval, and optional generic commands. |
| `GET /{workerId}/assignments` | Long-poll or poll for the next eligible assignment. | `200` with assignment; `204` when none is available. |
| `POST /{workerId}/assignments/complete` | Report a terminal result for an assignment. | `202` when accepted for reconciliation. |

The current handler returns immediately when no assignment is available; a
worker chooses its polling interval using the heartbeat interval. A later
transport may add streaming without changing the assignment envelope.

## Messages

Registration:

```json
{
  "protocolVersion": "v1",
  "workerId": "worker-a",
  "os": "linux",
  "architecture": "arm64",
  "capacity": 2,
  "capabilities": [{"name": "agent/claude", "version": "1.2.0"}],
  "labels": {"region": "eu-west"}
}
```

Heartbeat body is
`{"protocolVersion":"v1","instanceId":"process-1","activeAssignments":["as-1"],"leases":[{"assignmentId":"as-1","generation":1,"coordinatorEpoch":2}],"capacity":4,"availableSlots":3,"draining":false}`.
Each entry in `leases` renews only that assignment's current generation and
coordinator epoch. Assignment IDs alone do not renew leases. Expired leases
cannot be resurrected. A slot missing from the heartbeat expires independently.
`capacity` is an optional live admission limit; zero drains the worker.
Lowering it preserves admitted attempts and blocks new claims until the active
count falls below the limit. `availableSlots` advertises the worker's remaining
local slots. The coordinator independently enforces capacity atomically when
claiming dispatches.
The response provides `coordinatorEpoch`, `leaseSeconds`, and optional
`commands`. Commands are generic control instructions and are not domain
statuses.

An assignment carries `assignmentId`, `runId`, `activationId`, `attemptId`,
`coordinatorEpoch`, positive `generation`, `leaseUntil` (RFC 3339), and
`request`. `request` is the generic executor envelope: executor capability,
task, JSON params and values, artifact references, declared output ports, and
limits. Workers do not need the parent graph or coordinator database.

Workers supervise up to their configured capacity concurrently across all
advertised executor kinds (the default remains one). Each child owns a runner,
cancellation context, process group, attempt scratch directory, and log sink.
Heartbeats renew every active child, including children awaiting completion
acknowledgement. Cancelling a child only cancels that child's context. Shutdown
on SIGINT or SIGTERM first enters drain mode: it stops polling, advertises
`draining: true`, and continues heartbeats and completion delivery for up to
30 seconds. At the deadline it cancels and reaps remaining children.
Embedded workers can use `RemoteWorkerConfig.Drain` to drain explicitly and
`ShutdownGracePeriod` to configure cancellation grace (zero cancels immediately).
Drain prevents new claims even when advertised capacity is positive.

Registration may include `instanceId`, a random identity generated once per
worker process. Re-registering the same instance is idempotent. A replacement
instance for an existing worker ID fences all of the previous instance's active
assignments by advancing their generations and marking their dispatches
uncertain. Late results may be retained as diagnostics, but cannot become
successful workflow results or trigger automatic replay of uncertain effects.
Workers send the same `instanceId` in heartbeat bodies and the assignment
poll query string. The coordinator rejects mismatched or omitted identities
with HTTP 403 (`MUXLIO_WORKER_INSTANCE_MISMATCH`) after an identified process
registers. A replaced worker stops without re-registering. Older registrations
without an instance identity retain lease-based fencing; switching between an
identified process and a legacy process also fences existing assignments.

Completion echoes assignment/run/activation/attempt identity, epoch and
generation, then reports `state` as `succeeded`, `failed`, or `cancelled`, with
optional generic `outputs`, structured `error` (`code`, `message`, optional
`retryable`), and `logCursor`. The coordinator must compare all fencing fields
with the current lease before accepting a result. A stale generation or epoch
must not overwrite current state. Completion is safe to retry: the coordinator
should acknowledge an already-recorded identical completion and reject a
conflicting or stale one with a protocol error.

## Lease loss and recovery

Assignment generations and coordinator epochs are persisted with claims.
Completion and log publication validate the full identity and unexpired lease
atomically with the write. A coordinator restart fences previous leases; a
worker process replacement fences that process's assignments. Identical
committed completions remain durable across restarts.

Lease expiry, coordinator restart, and worker replacement release reservations.
Only requests declaring `effect: "none"` are automatically eligible for replay
under a newer generation. The interpreter propagates the execute node's
`retry.effect` policy into this envelope. Missing or other effect policies
remain uncertain and require explicit reconciliation. A stale completion may
be retained as a diagnostic; it cannot reconcile uncertainty or authorize an
unsafe replay. Workers cancel a slot locally when its lease expires, even if
heartbeat I/O is blocked.

## Errors

Errors use a stable machine-readable `code` and human-readable `message`.
Implemented HTTP cases include `unauthorized` (401), `worker_identity_mismatch`
(403), `invalid_json`, `invalid_registration`, `unsupported_protocol`,
`invalid_completion` (400), `not_found` (404), `method_not_allowed` (405),
`worker_protocol_unavailable` (503), and `coordinator_error` (500). An
assignment that violates the response envelope is treated as an internal
`coordinator_error` (500). Clients should branch on `code`, not message text.
Backends may provide a typed protocol error with an HTTP status and code;
invalid backend status or empty error fields are converted to the generic 500
response. Internal errors are deliberately returned without implementation
details.

## Security and semantics

Capability matching is based on advertised names and versions, runtime
attributes, capacity, and placement labels. A capability is an executor
contract, not a business category. Registration does not itself grant access
to arbitrary secrets; secret references and authorization are assignment
policy concerns. Artifact transfer, streamed logs, cancellation, and
reconciliation details will be versioned extensions. Until those transports
are configured, workers should only process the JSON execution request they
receive and return portable JSON/artifact references.

## Running a worker

Install adapter executables in a directory (the default is
`~/.muxlio/adapters`); executor names map `/` to `-`, matching the local
`ScriptExecutor` contract. Start a worker with:

```sh
muxlio worker start \
  --coordinator http://127.0.0.1:7600 \
  --worker-id worker-a \
  --workspace /path/to/workspace \
  --worker-token-file ~/.muxlio/worker-token \
  --capabilities agent/claude,agent/codex,agent/cursor,agent/gemini-cli,agent/kiro,command \
  --capacity 2 \
  --adapters ~/.muxlio/adapters
```

The worker makes only outbound HTTP requests, registers once, heartbeats and polls until interrupted,
and executes assignments concurrently up to capacity using its local adapters. Adapter
stderr is returned in the completion `logs` field; stdout remains reserved for
the adapter's JSON `ExecuteResult`. A completion includes the assignment's
fencing identity and either outputs or a structured execution error.


## Admission budgets and fairness

The coordinator's service config accepts an `admission` object. Limits are
shared across workers and stored with dispatch state. All concurrency caps count
slots; each executor costs one slot unless `slots` is configured. Zero or omitted
caps mean unlimited, except worker capacity zero drains that worker.

```json
{
  "admission": {
    "globalCap": 16,
    "perRunCap": 4,
    "executorCaps": {"agent-pool": 8},
    "keyCaps": {"team-a": 3},
    "providerCaps": {"external-service": 6},
    "providerRates": {"external-service": {"burst": 2, "perSecond": 1}},
    "executors": {
      "agent/example": {
        "slots": 2,
        "executorKey": "agent-pool",
        "operatorKey": "team-a",
        "providerKey": "external-service"
      }
    }
  }
}
```

Resource keys are explicit opaque names; adapter/provider protocols remain in
adapters. `executorKey` defaults to the executor capability and can group multiple
capabilities into one adapter budget. Executors can also share an operator or
external provider key. Provider rate tokens count dispatch starts, independently
of slot weights and reported model-token usage. Completing or cancelling work
releases concurrency capacity without refunding rate tokens. Buckets and
reservations survive coordinator restarts.

Admission checks and reservations commit in the same transaction as the claim.
Worker polls claim at most one assignment, with no speculative batch prefetch.
Ready runs rotate by least-recently-served turn, preserving queue order among
eligible requests within a run. Blocked runs yield to eligible peers. A newly
arriving run gets a turn before a previously served fan-out run when capacity
next opens. Timer and input waits create no dispatch and consume no execution
capacity. Paused and terminal runs are ineligible.

Activation responses include `queueReason` for pending dispatches. The inspector
shows the latest observed budget constraint (worker, global, run, executor,
operator key, provider concurrency, or provider rate); before a matching worker
polls, it shows "Waiting for worker". Reasons update on subsequent worker polls
and disappear once the request is claimed.
