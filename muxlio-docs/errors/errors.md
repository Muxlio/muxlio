# Muxlio Error Codes

Plan diagnostics carry stable machine-readable codes, severity, phase, and repair
guidance. HTTP error envelopes contain code and message, with fix and details
when available.

## API Error Envelope

Every non-2xx HTTP response from the coordinator API includes a structured JSON body.
Coding agents should branch on `code` to detect and repair failures — messages and HTTP
status codes may be refined across minor releases, but codes are stable.

```json
{
  "code":    "MUXLIO_...",
  "message": "human-readable description",
  "fix":     "actionable repair instruction",
  "details": [
    {
      "code":     "MUXLIO_...",
      "severity": "error",
      "phase":    "plan",
      "subgraph": "root",
      "nodeId":   "step",
      "message":  "...",
      "fix":      "..."
    }
  ]
}
```

`fix` is omitted when no specific guidance is available. `details` is omitted when empty
(present only for `MUXLIO_VALIDATION_ERROR`).

## Build / Plan Validation Errors

These appear in `details[]` inside a `MUXLIO_VALIDATION_ERROR` response (HTTP 422)
when `POST /v1/runs` receives a structurally invalid plan.

### MUXLIO_MISSING_IR_VERSION
- **Phase:** plan
- **Cause:** The plan JSON has no `irVersion` field.
- **Fix:** Set `irVersion` to `"1.0"`.

### MUXLIO_UNSUPPORTED_IR_VERSION
- **Phase:** plan
- **Cause:** The plan's `irVersion` is not supported by this coordinator.
- **Fix:** Recompile the workflow with Plan IR `1.0` or upgrade the coordinator.

### MUXLIO_MISSING_NAME
- **Phase:** plan
- **Cause:** The plan has no `name`.
- **Fix:** Set a human-readable workflow name.

### MUXLIO_MISSING_ROOT_SUBGRAPH
- **Phase:** plan
- **Cause:** The subgraph named by `plan.root` (default `"root"`) does not exist.
- **Fix:** Add a subgraph with that name or set `plan.root` to an existing subgraph.

### MUXLIO_DUPLICATE_NODE_ID
- **Phase:** plan
- **Cause:** Two nodes in the same subgraph share an `id`.
- **Fix:** Node IDs must be unique within a subgraph.

### MUXLIO_MISSING_NODE_ID
- **Phase:** plan
- **Cause:** A node in the plan has no `id` field.
- **Fix:** Every node needs a stable id.

### MUXLIO_MISSING_EXECUTOR
- **Phase:** plan
- **Cause:** An `execute` node has no `executor` field.
- **Fix:** Set the executor capability, e.g. `"agent/claude"`.

### MUXLIO_MISSING_MAP_ITEMS
- **Phase:** plan
- **Cause:** A `map` node has no `items` expression.
- **Fix:** Set `items` to an expression evaluating to the collection.

### MUXLIO_MISSING_MAP_BODY
- **Phase:** plan
- **Cause:** A `map` node has no `bodyRef`.
- **Fix:** Set `bodyRef` to a subgraph name.

### MUXLIO_BAD_SUBGRAPH_REF
- **Phase:** plan
- **Cause:** A `bodyRef`, `thenRef`, or `elseRef` names a subgraph that doesn't exist.
- **Fix:** Add the referenced subgraph or fix the name.

### MUXLIO_MISSING_CONDITION
- **Phase:** plan
- **Cause:** A `branch` node has no `condition`.
- **Fix:** Set `condition` to a boolean expression.

### MUXLIO_MISSING_THEN_REF
- **Phase:** plan
- **Cause:** A `branch` node has no `thenRef`.
- **Fix:** Set `thenRef` to a subgraph name.

### MUXLIO_UNBOUNDED_LOOP
- **Phase:** plan
- **Cause:** A `loop` node has no `maxIterations`.
- **Fix:** Set `maxIterations` to a hard upper bound.

### MUXLIO_MISSING_WHILE
- **Phase:** plan
- **Cause:** A `loop` node has no `while` condition.
- **Fix:** Set `while` to a boolean expression.

### MUXLIO_MISSING_LOOP_BODY
- **Phase:** plan
- **Cause:** A `loop` node has no `bodyRef`.
- **Fix:** Set `bodyRef` to a subgraph name.

### MUXLIO_EMPTY_PROJECT
- **Phase:** plan
- **Cause:** A `project` node has no inputs.
- **Fix:** Add at least one input binding to project.

### MUXLIO_UNKNOWN_WAIT_TYPE
- **Phase:** plan
- **Cause:** A `wait` node has an unrecognized `waitFor` value.
- **Fix:** Set `waitFor` to `"timer"`, `"signal"`, or `"input"`, or leave empty for a pass-through wait.

### MUXLIO_MISSING_TIMER_SECONDS
- **Phase:** plan
- **Cause:** A `wait` node has `waitFor=timer` but no `timerSeconds`.
- **Fix:** Set `timerSeconds` to the number of seconds to wait.

### MUXLIO_MISSING_SIGNAL_NAME
- **Phase:** plan
- **Cause:** A `wait` node has `waitFor=signal` or `waitFor=input` but no `signalName`.
- **Fix:** Set `signalName` to the signal the node should wait for.

### MUXLIO_UNKNOWN_NODE_KIND
- **Phase:** plan
- **Cause:** A node has an unrecognized `kind`.
- **Fix:** Use one of: `execute`, `map`, `branch`, `loop`, `join`, `wait`, `child`, `project`.

### MUXLIO_BAD_REF
- **Phase:** plan
- **Cause:** An input binding or output references a node that doesn't exist in the subgraph.
- **Fix:** Reference an existing visible node and declared output port. Forward references
  are allowed when they form an acyclic dependency graph.

### MUXLIO_BAD_RETRY
- **Phase:** plan
- **Cause:** A retry policy has out-of-range `maxAttempts` or negative backoff durations.
- **Fix:** Set `maxAttempts` to 0–100 and backoff durations to non-negative values.

## HTTP API Errors (Request / Protocol Layer)

These are the top-level `code` values returned directly in non-2xx responses.

| Code | HTTP | Cause | Fix |
|---|---|---|---|
| `MUXLIO_BAD_REQUEST` | 400 | Generic bad request. | Check the request body and URL parameters. |
| `MUXLIO_INVALID_JSON` | 400 | Request body is not valid JSON or contains multiple values. | Send a single well-formed JSON object. |
| `MUXLIO_MISSING_RUN_ID` | 400 | No run ID in the URL path. | Include a run ID, e.g. `/v1/runs/<runId>`. |
| `MUXLIO_UNSUPPORTED_PROTOCOL` | 400 | Worker `protocolVersion` is not `"v1"`. | Set `protocolVersion` to `"v1"`. |
| `MUXLIO_INVALID_REGISTRATION` | 400 | Worker registration missing `workerId`, capacity, or has duplicate/invalid capabilities. | Provide non-empty `workerId`, positive `capacity`, and trimmed unique capability names. |
| `MUXLIO_INVALID_COMPLETION` | 400 | Worker completion missing required identity fields or terminal state. | Supply all identity fields and one of `succeeded`/`failed`/`cancelled`. |
| `MUXLIO_INVALID_ARTIFACT_DIGEST` | 400 | Artifact digest in the URL is missing or contains a slash. | Use a well-formed digest, e.g. `sha256:<64 hex chars>`. |
| `MUXLIO_BAD_EVENT_CURSOR` | 400 | The `after` cursor for event streaming is not a non-negative integer. | Set `after` to a non-negative sequence number. |
| `MUXLIO_NOT_FOUND` | 404 | Run, activation, or resource not found. | Verify the ID exists; use `GET /v1/runs` to list known runs. |
| `MUXLIO_METHOD_NOT_ALLOWED` | 405 | HTTP method not supported on this endpoint. | Consult the `Allow` response header. |
| `MUXLIO_WORKER_NOT_REGISTERED` | 409 | Worker attempted an operation before registering. | Call `POST /v1/workers/register` first. |
| `MUXLIO_STALE_ASSIGNMENT` | 409 | Worker completed with an outdated generation or coordinator epoch. | Fetch a fresh assignment via `GET /v1/workers/<workerId>/assignments`. |
| `MUXLIO_RUN_CONTROL_REJECTED` | 409/404 | Run pause/resume/cancel rejected (wrong state or not found). | Check `GET /v1/runs/<runId>` for current state. |
| `MUXLIO_VALIDATION_ERROR` | 422 | Plan has one or more structural validation errors. See `details[]`. | Correct the errors listed in `details[]` and resubmit. |
| `MUXLIO_DIGEST_MISMATCH` | 422 | Artifact header digest does not match URL digest, or plan digest is stale. | Recompute the digest and resubmit consistent data. |
| `MUXLIO_ARTIFACT_INTEGRITY` | 422 | Uploaded artifact bytes do not match the declared digest. | Recompute the digest and reupload the correct bytes. |
| `MUXLIO_STORE_ERROR` | 500 | Internal storage error. | Retry; if persistent, inspect coordinator logs. |
| `MUXLIO_RUN_ERROR` | 500 | Run creation or execution failed. | Inspect run events via `GET /v1/runs/<runId>/events`. |
| `MUXLIO_RUNNER_UNAVAILABLE` | 500 | Coordinator has no runner configured. | Ensure the server was started with a valid configuration. |
| `MUXLIO_RUN_CONTROL_UNAVAILABLE` | 503 | Run controls not supported by this coordinator build. | Start the coordinator with run control support. |
| `MUXLIO_ARTIFACT_UPLOAD_FAILED` | 400/422 | Artifact upload rejected (bad digest or integrity error). | Verify the bytes match the declared digest and retry. |
| `MUXLIO_ARTIFACT_DOWNLOAD_FAILED` | 404/400/500 | Artifact download failed (not found, bad digest, or store error). | Check the digest and ensure the artifact was uploaded. |
| `MUXLIO_STREAM_UNSUPPORTED` | 500 | Transport does not support SSE streaming. | Use `GET /v1/runs/<runId>/events` for paginated events instead. |
| `MUXLIO_COORDINATOR_ERROR` | 500 | Unexpected coordinator error. | Inspect coordinator logs. |
| `MUXLIO_WORKER_PROTOCOL_UNAVAILABLE` | 503 | Worker protocol not configured. | Ensure the coordinator is started with worker protocol support. |

## Runtime Errors

These appear in run error fields (not API response codes) for failures during plan execution.

### MUXLIO_DUPLICATE_MAP_KEY
- **Phase:** run
- **Cause:** A map collection produced a duplicate key.
- **Fix:** Ensure `item.id` is unique in this collection or specify a stable unique key expression.

### Map items exceed maxItems
- **Phase:** run
- **Cause:** The collection has more items than `maxItems` allows.
- **Fix:** Increase `maxItems` or reduce the collection size.

### Dependency cycle
- **Phase:** plan
- **Cause:** Nodes in a subgraph form a circular dependency.
- **Fix:** Break the cycle by removing or redirecting a binding.

## Diagnostic identity and repair workflow

`muxlio check plan.json --json` emits `{ "valid": false, "errors": [...] }`
and exits with status 1 for invalid plans, unreadable files, invalid JSON, and
compiler failures. Patch the field identified by `source.pointer`, then rerun
check; successful checks exit with status 0. `build --json` uses the same
validation diagnostics. MCP `check_plan`, `build`, and `test` return JSON text
with the same diagnostic fields. HTTP plan validation returns those fields in
`details[]` with status 422.

Every diagnostic has a stable `code`, `id`, `message`, and suggested `fix`.
The `diag_` identifier hashes the code, phase, subgraph, node, and JSON pointer;
file failures without a pointer include the filename. It excludes prose and
run/attempt IDs, so the same issue at the same location retains its identifier
across retries and transports. An ID identifies an issue location, not a unique
execution event. Use `runId`, `scopeId` (activation identity), and `attemptId` to
identify an occurrence.

`source.pointer` is an escaped JSON Pointer into the submitted Plan IR.
`source.line` and `source.column` are 1-based byte positions in that JSON.
Missing fields point to the field's parent object. Positions for compiled
TypeScript/JavaScript refer to the generated Plan IR, not the original source;
no original-source mapping is inferred. `source.file` is included when a local
JSON file is available. Programmatically constructed plans can provide pointers
without line/column positions.

Failed execution attempts persist `result.diagnostic`. It contains execution
identity, the source node, declared requirements, expected and actual values
when available, and a bounded `causes[]` chain. Output validation includes
`valuePath` for the invalid returned value. Inspect attempts through
`GET /v1/runs/<runId>/attempts` or MCP `get_attempts`. `GET /v1/runs/<runId>`,
CLI JSON run/test results, and MCP test results also include `diagnostics[]`.
Remote workers preserve the diagnostic contract in their completion receipts.

Additional stable codes:

| Code | Repair |
|---|---|
| `MUXLIO_ADAPTER_NOT_FOUND` | Install the adapter in the configured directory or correct the executor name. |
| `MUXLIO_INCOMPATIBLE_REQUIREMENTS` | Select a descriptor that satisfies the contract/executor versions and capabilities, or correct the requirements. `expected`, `actual`, and `requirements` describe the mismatch. |
| `MUXLIO_EXECUTION_FAILED` | Repair the reported executor failure and retry. |
| `MUXLIO_BUILD_FAILED` | Correct the source, file access, or compiler failure and rerun check. |
| `MUXLIO_WORKFLOW_TEST_FAILED` | Fix the co-located workflow test and rerun check. |
| `MUXLIO_TOOL_ERROR` | Correct the MCP tool arguments or reported failure and retry. |
| `MUXLIO_DIAGNOSTICS_TRUNCATED` | Fix the reported issues and rerun check to reveal additional issues. |

Diagnostic JSON and error envelopes are limited to 64 KiB, collections to 100
entries, and individual strings to approximately 2 KiB. Deep or oversized
values are omitted or replaced with truncation markers; diagnostic identity
and repair fields take priority over payload details. Normal successful output
and artifact responses retain their existing contracts. Known execution and
environment credentials, sensitive structured keys, authorization values, URL
credentials, and recognizable token strings are redacted before transport.
