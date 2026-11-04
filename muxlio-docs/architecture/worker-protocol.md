# Remote worker protocol v1

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
| `POST /{workerId}/heartbeat` | Renew liveness and report active assignment IDs. | `200` with coordinator epoch, lease interval, and optional generic commands. |
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

Heartbeat body is `{"protocolVersion":"v1","activeAssignments":["as-1"]}`.
The response provides `coordinatorEpoch`, `leaseSeconds`, and optional
`commands`. Commands are generic control instructions and are not domain
statuses.

An assignment carries `assignmentId`, `runId`, `activationId`, `attemptId`,
`coordinatorEpoch`, positive `generation`, `leaseUntil` (RFC 3339), and
`request`. `request` is the generic executor envelope: executor capability,
task, JSON params and values, artifact references, declared output ports, and
limits. Workers do not need the parent graph or coordinator database.

Completion echoes assignment/run/activation/attempt identity, epoch and
generation, then reports `state` as `succeeded`, `failed`, or `cancelled`, with
optional generic `outputs`, structured `error` (`code`, `message`, optional
`retryable`), and `logCursor`. The coordinator must compare all fencing fields
with the current lease before accepting a result. A stale generation or epoch
must not overwrite current state. Completion is safe to retry: the coordinator
should acknowledge an already-recorded identical completion and reject a
conflicting or stale one with a protocol error.

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
and executes assignments sequentially using its local adapters. Adapter
stderr is returned in the completion `logs` field; stdout remains reserved for
the adapter's JSON `ExecuteResult`. A completion includes the assignment's
fencing identity and either outputs or a structured execution error.
