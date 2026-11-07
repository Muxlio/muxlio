# Remote worker protocol v1

## Trace correlation

Durable execute envelopes carry optional `traceContext` (W3C `traceparent` and
`tracestate`, without baggage) and `queuedAt`. The coordinator measures queue
delay at claim using its own enqueue timestamp; workers measure execution
duration separately. Workers continue the dispatched span context and supply
the current adapter invocation context in the shared adapter request's optional
`traceContext` field. Adapters can extract this context for their own spans;
provider-specific telemetry stays in the adapter. Prompt, transcript, artifact,
credential and arbitrary error content is excluded from Muxlio telemetry.
Exported activation and attempt ID attributes contain `sha256:` plus the SHA-256
hex digest of the original ID: scope paths can include input-derived map keys.
Wire and stored IDs are unchanged; coordinator and worker spans use the same digest.

## Detached remote operations

An execute node opts into asynchronous handoff with
`requiredCapabilities: {"remoteTask": "1"}`. Its adapter must support
`remoteTask` and `reconcile`. Provider-side `idempotencyKey` support is independent
and required only when a key is explicitly requested. The coordinator persists
the submission attempt identity and treats an ambiguous submission as reconcilable,
regardless of the node's retry policy. A failed submission response
never authorizes blind resubmission: a read-only `action: "reconcile"` lookup
receives the original `operationAttemptId`, remote reference, and any explicit key. Only `not-applied` authorizes
a fresh submission within the retry budget.

After submitting remote work, an adapter exits with a result such as
`{"remoteTask":{"id":"job-123","pending":true,"pollAt":"2026-11-06T17:00:00Z"}}`.
The worker persists this result before releasing the assignment and its slots.
The interpreter checkpoints the task/context identity, any explicit key, and next poll time in a
`detached` execution attempt. The wait consumes no worker subprocess slot. A
missing poll time defaults to 30 seconds. Restart resumes the stored schedule
and task identity, without submitting the job again.

Each follow-up is a separate bounded `action: "poll"` invocation with the remote
reference, any explicit key, and original operation attempt identity. Polling is read-only,
has a 30-second execution and queue timeout, and acquires no original workspace
or concurrency keys. Return another pending reference to keep waiting, or a
normal result with final outputs when complete. Failed lookups retry the lookup,
never the submission. Reconciliation can also return
`{"reconciliation":{"status":"pending"},"remoteTask":{"id":"job-123","pending":true}}`
to recover a job whose submission response was lost.

Handoff is restricted to remote work. Local sessions cannot detach. Adapter
processes are supervised until they exit, even if they emit pending JSON early;
a local CLI process that is still running continues to occupy its worker slot.

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
`muxlio.dev/isolation/bwrap=true`, `muxlio.dev/isolation/oci=true`, or
`muxlio.dev/isolation/docker-vm=true` (`macos-vm` is a legacy alias), matching
`params.isolation.runtime`. Operators must advertise only installed, enabled
backends that have passed the [boundary conformance suite](execution-security.md).
OCI and Docker VM also require an image; VM execution requires `enabled:true`.
Without an eligible worker the request
remains unadmitted; native fallback is forbidden. The executor checks actual
backend availability again before starting a process. Unimplemented backends
cannot satisfy admission.

Muxlio remote workers establish outbound HTTP connections to a coordinator.
They never connect to its database. The wire format is generic execution
infrastructure: workflow-specific meaning stays inside the opaque execute
request, capability names, JSON values, and artifact references.

## Versioning

Worker registration, heartbeats, polling, completions, logs and artifact
transfers require a bearer credential. Use HTTPS for remote coordinators; the
worker opens outbound connections only and needs no public inbound port.

The coordinator reloads worker credentials and `accessGrants` from its selected
configuration on every API request. Provision an identity-scoped credential by
writing a protected token file and adding a grant such as:

```json
{
  "accessGrants": [{
    "tokenFile": "worker-a-token",
    "workerId": "worker-a",
    "permissions": ["worker", "artifacts:read", "artifacts:write"],
    "artifactDigests": ["sha256:<64 hex digits>"],
    "expiresAt": "2027-01-01T00:00:00Z"
  }]
}
```

Token paths are relative to the configuration file. Artifact access is restricted
to the listed digests. The existing cluster worker token remains a trusted role
with access to every worker and artifact; distribute scoped credentials when
workers must have separate authorization.

Rotate by atomically replacing the token file and restarting the worker with
the new credential. For an overlap period, add a second grant for the same worker
identity before removing the old one. Revoke a grant by setting `revoked: true`
or removing it. Invalid or expired expiration timestamps deny access; a reload
failure fails closed. Revocation blocks subsequent assignment claims and lease
renewals; existing leases expire normally. Revocation does not prove an external
process stopped or authorize replay. Credential changes survive coordinator
restart because the configuration and protected token files remain the source
of authority. The cluster token can also be rotated through its configuration;
an explicit `MUXLIO_WORKER_TOKEN` environment override lasts for the server process.

The protocol version is `v1`, supplied as `protocolVersion` during registration
and heartbeat. A coordinator rejects unsupported major versions instead of
guessing. JSON uses UTF-8 and `application/json`; request bodies are limited to
4 MiB in the current HTTP handler. All error bodies have the common shape
`{"code":"...","message":"..."}`. Workers may retry 5xx and transport
failures with backoff. They must treat 4xx as a request/configuration issue
unless the error code documents a recoverable condition.

Registration may advertise `supportedProtocolVersions`, for example
`["v2", "v1"]`. The coordinator selects a supported version and returns it as
`protocolVersion`; no intersection is rejected before registration changes
identity or leases. Legacy clients may continue supplying only `protocolVersion`.
Workers verify the selected version and use it for subsequent heartbeats.

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

### Executable readiness

Adapters opt into readiness with `probeVersion: "1"` in their descriptor and
implement `<adapter> --muxlio-probe`. This mode must perform only bounded,
non-destructive checks and emit `{ "ready": boolean, "checks": [...] }`.
Each check carries a name, a passed flag, and a secret-free diagnostic message.
Provider authentication, runner version, binary and tool checks belong to the
adapter. The worker checks local permission and protocol compatibility and uses
the same configured native environment as execution. Raw CLI diagnostics are
never forwarded by the bundled probes.

Worker registration preserves these reports under each capability's `readiness`
field. Failed reports and missing reports for probe-enabled contracts prevent
matching before an assignment is leased. Legacy adapters without probe support
are unverified by current workers. Isolation requirements still require an
explicit supported backend label before matching; installing a runtime binary
alone does not advertise an enforced isolation backend.

`muxlio worker status [--adapters DIR] [--data-dir DIR] [--json]` runs local
readiness checks. `muxlio doctor` includes the same checks. Registration probes
are a startup snapshot; restarting the worker refreshes them after login or
configuration changes. Gemini's probe currently verifies configured API-key
availability; interactive login is unverified and fails closed. Readiness does
not guarantee a credential remains valid or that a remote service is reachable.

## Retry safety and uncertain external effects

An execute node's `retry.effect` classifies its operations:

| Policy | After worker loss |
| --- | --- |
| `pure` (legacy `none`) | Safe to replay, subject to deadlines and resource fencing. |
| `idempotent-with-key` (legacy `idempotency-key`) | Replay carries the same `idempotencyKey`; the adapter must advertise `idempotencyKey` revision 1 and implement external deduplication. |
| `reconcilable` (legacy `reconcile`) | Invoke adapter reconciliation before another operation can start. |
| `nonretryable` (legacy `manual`) | Hold for explicit operator resolution. |

Legacy `retryable` allows retries of reported failures, but does not establish
safe replay after worker loss. Unknown or undeclared effects fail closed after
uncertain execution. These policies do not provide universal exactly-once
semantics: a worker can disappear after an external commit and before its durable
completion checkpoint.

Keys default to the activation identity, including run and scope, and survive
attempt and run retries. An explicit `retry.idempotencyKey` must identify one
logical operation within the external system's deduplication domain. Fallback
adapters are not used for keyed operations, since they may use another domain.

An adapter can publish `{"operationRef":{"id":"external-id","url":"..."}}`
on `MUXLIO_LOG_FD` immediately after submission. Workers forward this shared
record through the fenced progress channel. The coordinator persists the
reference separately from completion; a missing reference is never evidence
that no effect occurred. Adapters may also return `remoteTask` on completion.

Adapters advertising `reconcile` revision 1 accept `action: "reconcile"` with
the original operation reference, `operationAttemptId`, and inputs. This action is a
read-only lookup and must never submit work. It returns `reconciliation` with
`status` and an optional `reason`. `completed` includes the ordinary output
result, which is validated before reuse. `not-applied` is affirmative evidence
that the original operation cannot subsequently commit, permitting redispatch.
Missing support, errors, malformed results, and `inconclusive` hold the attempt.

`GET /v1/runs/{runId}/attempts` exposes `effect`, `idempotencyKey`, `operationRef`,
and `awaitReconciliationReason`. Held attempts block run retry. An operator can
resolve one using `POST /v1/runs/{runId}/attempts/{attemptId}/resolve` with
`{"outcome":"not-applied","reason":"evidence that the old operation cannot commit"}`,
or `{"outcome":"completed","reason":"verified externally","result":{"executionSucceeded":true,"outputs":{}}}`.
Resolution fences stale workers and records an audit event. The operator may
then retry the failed run; completed operations reuse their verified outputs.

## Persistent provider sessions

One-shot calls remain the default. An execute node can opt in with
`session: {"mode":"create"}`. A successful completion returns `session` with an
opaque provider `id`, `executor`, provider `runnerVersion`, `ownerWorker`,
`ownerEnvironment`, `expiresAt` (RFC 3339), and `recoverable`. The coordinator
persists this handle in the worker result and the execution attempt's `result`.
To continue, supply `session: {"mode":"resume","handle": <returned session>}`
on a subsequent execute node. Use the exact handle; it is mutable provider
state, not a portable transcript. Bare `sessionId` remains a legacy protocol
and cannot be combined with the structured session request.

Adapters must advertise `session` capability **revision 2** for this protocol.
They create a distinct provider session only in create mode. On resume they
must verify the underlying runner version and provider state, fail explicitly
if either is missing or incompatible, and return the same handle. They must
never substitute a new conversation. The worker supplies ownership metadata;
the adapter supplies the opaque ID, executor, runner version, expiration, and
recoverability. Bundled adapters currently do not advertise session support.

Resume dispatch matches only the original worker and environment. The environment
fingerprint covers the host, local storage, adapter directory, agent home,
workspace and configured execution environment. The local checkpoint also
checks adapter revision and workspace. Recoverability permits an idle session
to survive a worker restart in that environment; it never permits migration.
Nonrecoverable handles fail after restart. Expired, missing, incompatible or
failed session state produces an error, without automatic creation or fallback.

The coordinator reserves an implicit exclusive resource for each resumed
session, including across runs and coordinator restarts. Reservations survive
worker loss until confirmed completion or explicit reconciliation. Workers
also use process-shared file locks, with cancellable waits, for direct calls
and independent runner instances. A nonmultiplexed turn removes its checkpoint
before starting and restores it only after a successful, matching response.
An interrupted turn therefore cannot silently resume from an unknown state.
Adapters may explicitly advertise `sessionMultiplex` revision 1 to permit
shared local locks; coordinator dispatch remains conservatively serialized.
Persistent session locking currently requires Unix; other platforms fail closed.

Session prompts carry mutable effects even when the task is marked pure or
idempotent. Lost session assignments require explicit resolution rather than
automatic replay, reconciliation prompts or executor fallback. Recovery of a
known idle session and recovery of an interrupted turn are distinct operations.
Session invocations are not automatically retried after execution or output
validation failure, even under a pure or idempotent retry policy. The provider
may already have advanced the conversation before the failure was observed.

A poll error accompanied by a non-pending remote reference confirms terminal
operation failure; the reference must match the polled identity. Errors without
a terminal reference describe failed lookups and leave the operation detached.
The same terminal failure with `reconciliation.status: "completed"` ends an
uncertain operation through the normal failure path.

### Completion port decoding errors

Worker completions preserve the shared result's optional `outputErrors` map
alongside `outputs`. A successful executor completion with a port decoding error
still fails output validation, including when the declared JSON Schema accepts
strings. Transporting a retained prose value must not turn it into valid JSON.
