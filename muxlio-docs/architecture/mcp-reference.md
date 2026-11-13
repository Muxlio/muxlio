# MCP protocol and tool reference

The `muxlio-mcp` binary exposes Muxlio operations as tools over the
[Model Context Protocol](https://spec.modelcontextprotocol.io/) (MCP).
Any MCP-capable assistant — Codex, Cursor, Claude Code, or another — can call
these tools to build, check, run, and inspect Muxlio workflows without leaving
its chat interface.

The MCP server is an adapter to the existing HTTP API. It does not execute
runs itself; the Muxlio coordinator must be running separately.

Offline authoring tools include `list_workflow_templates` (`{}`),
`get_workflow_template` (`{name, version}`), and `instantiate_workflow_template`
(`{name, version, parameters}`). All three are read-only, deterministic operations.
Discovery and inspection return JSON Schemas; instantiation returns an editable
ordinary `plan` with its `digest` and template identity. See the
[workflow template catalog](../workflow-templates.md) for contracts and examples.
Templates are reusable authoring recipes; `register_workflow_version` and
`list_workflow_versions` store and expose pinned compiled plans separately.

---

## Prerequisites

1. Build or install Muxlio. After `bash scripts/build-release.sh PREFIX`, the
   binary lives at `PREFIX/bin/muxlio-mcp`.
2. Have a reachable coordinator and any credentials it requires. It can run on
   another machine; no local coordinator or worker is required on the MCP host.
   See the [service guide](../service.md) to deploy one if needed.
3. Configure its URL or saved profile, then call `server_health` and `get_workers`
   to check connectivity and execution capacity.

---

## Configuration

### Coordinator URL

The server uses `--server`, then `MUXLIO_SERVER`, then `defaultServer` in
`~/.muxlio/config.json`. One must be set. Both explicit
HTTP(S) URLs and saved `servers` / `profiles` names are supported:

```sh
muxlio-mcp --server coordinator
```

`muxlio init` creates a `servers.local` profile and selects it when no default
is set. Its loopback URL uses `credentialRef: "root"` to read the configured
top-level `token` / `tokenFile`. Repeat init migrates existing local installations
and preserves existing defaults and profiles. It does not start the service;
run `muxlio service start` before using MCP.

Profiles may be URL strings or objects with `url`, `token`, and/or `tokenFile`.
`MUXLIO_TOKEN` overrides configured tokens. Profiles never inherit top-level
credentials implicitly. The `root` credential reference is restricted to loopback;
remote profiles require their own scoped credentials and HTTPS. Relative token
file paths resolve against the config directory.

Run `muxlio integrations doctor` to diagnose coordinator configuration,
connectivity, TLS, authentication and workers read permission. Use `--server NAME`
to check a particular profile. `--config-only` checks resolution and credential
availability without connecting. For a registered authoring client, supply
`--client NAME --scope project|user`; doctor checks its saved stdio endpoint
before asking the client to invoke `server_health` and `get_workers`.
For old local installs, rerun `muxlio init`; use `integrations repair` with the
client, scope and `--profile local` (or the selected saved profile name) to update
an unchanged managed registration to a profile name.

An explicit URL supplied through `--server` or `MUXLIO_SERVER` uses
`MUXLIO_TOKEN` for authentication. It does not search profiles by URL or infer
credentials from local addresses, ports, or network interfaces. Select a profile
by name to use its saved credentials.

`MUXLIO_CONFIG` selects a configuration file; `MUXLIO_HOME` changes the default
configuration directory. Keep credentials out of workflow inputs and chat.

### MCP client configuration

Add a `muxlio` entry to your IDE's MCP configuration. The exact file location
varies by client; consult its documentation.

**Example (generic MCP JSON format):**

```json
{
  "mcpServers": {
    "muxlio": {
      "command": "/path/to/muxlio-mcp",
      "args": ["--server", "coordinator"]
    }
  }
}
```

**Using `MUXLIO_SERVER` instead of `--args`:**

```json
{
  "mcpServers": {
    "muxlio": {
      "command": "/path/to/muxlio-mcp",
      "env": {
        "MUXLIO_SERVER": "coordinator"
      }
    }
  }
}
```

**Kiro (`~/.kiro/settings/mcp.json`):**

```json
{
  "mcpServers": {
    "muxlio": {
      "command": "muxlio-mcp",
      "args": ["--server", "coordinator"]
    }
  }
}
```

**Cursor (`~/.cursor/mcp.json`):**

```json
{
  "mcpServers": {
    "muxlio": {
      "command": "muxlio-mcp",
      "args": ["--server", "coordinator"]
    }
  }
}
```

**Claude Code / claude_desktop_config.json:**

```json
{
  "mcpServers": {
    "muxlio": {
      "command": "muxlio-mcp",
      "args": ["--server", "coordinator"]
    }
  }
}
```

### Network transport (Streamable HTTP)

`muxlio init` configures an authenticated MCP HTTP listener at
`127.0.0.1:7602` and creates persistent client and worker tokens in the protected
runtime config (existing tokens and listener settings are preserved).
`muxlio service start` then starts the coordinator, worker, UI, and MCP listener
in one process. Stop and restart manage them together. No separate MCP daemon or
saved coordinator profile is needed for this listener; it connects to the
service's local coordinator using its effective client token.

For a network listener on `coordinator`, run:

```sh
muxlio init --mcp-listen 0.0.0.0:7602
muxlio service restart
```

Run init with the same `--data-dir` used by your service. New setup defaults to
loopback; the example explicitly exposes the listener to the trusted network.
The relevant service configuration is:

```json
{
  "mcp": {
    "listen": "0.0.0.0:7602"
  }
}
```

Optional `mcp` fields are `tokenFile`, `tlsCert`, `tlsKey`, and `allowOrigins`
(an array of exact browser origins). Relative file paths resolve beside the
service config. For HTTPS, set both MCP TLS fields. Omit `mcp` or set it to
`null` to disable the listener; init adds it when missing and preserves `null`
unless `--mcp-listen` is explicitly supplied. Listener startup errors, including
an occupied port or unreadable TLS/token files, fail service startup. Logs go
to the service log.

The standalone binary remains available for independent hosting:

```sh
~/.muxlio/bin/muxlio-mcp --server coordinator --listen 0.0.0.0:7602
```

Every MCP request requires a bearer token. By default the listener uses the
effective coordinator token. For a separate listener credential in service mode,
set `mcp.tokenFile`, or set `MUXLIO_MCP_HTTP_TOKEN` in the service environment.
For the standalone command, use
`--http-token-file /absolute/path/to/token` or `MUXLIO_MCP_HTTP_TOKEN`. The file
wins over the environment, which wins over the coordinator token. An empty
listener token is rejected at startup. The listener token never replaces the
upstream coordinator credential.

For service HTTPS, set `mcp.tlsCert` and `mcp.tlsKey`. The standalone equivalent
is `--tls-cert /absolute/path/to/cert.pem --tls-key /absolute/path/to/key.pem`.
Alternatively, terminate TLS at a trusted reverse proxy. Plain HTTP is intended
for trusted networks; its bearer token travels without encryption. The same
flags work with a specific interface address instead of `0.0.0.0`.

Browser requests containing an `Origin` header are rejected by default. To
permit a browser-based inspector, add its exact origin to `mcp.allowOrigins`,
for example `["http://localhost:6274"]`. The standalone command accepts
`--allow-origin http://localhost:6274` (repeat for additional origins). Native
clients without an Origin header need no origin setting. Origins do not replace
authentication; every tool call still requires a bearer token.

### Connecting an MCP client

For a network connection, configure a client that supports Streamable HTTP with
`http://<host>:7602/mcp` (or the configured HTTPS URL) and the listener's bearer
token. The client must be able to reach that host. Store credentials in the
client's protected configuration, outside workflow inputs and chat.

For stdio, configure the absolute path to `muxlio-mcp` with arguments
`--server <profile-or-URL>`. The client launches the MCP binary locally; the
coordinator may run on another machine. Client configuration formats and menu
locations depend on the client and version.

Call `server_health` and `get_workers` first. `registeredWorkers` contains live
workers and `workers` contains active assignments. Muxlio's HTTP listener provides
fixed bearer authentication; it does not implement an OAuth authorization server.
A client requiring OAuth needs a separately configured compatible gateway.

---

## Protocol

Both transports support legacy MCP 2025-11-25 and per-request MCP 2026-07-28.
Stdio uses newline-delimited JSON-RPC 2.0. Startup diagnostics go to stderr
in both modes.

HTTP implements the stateless JSON-response form of
[Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports):
POST `/mcp` accepts one JSON-RPC message and returns a JSON response, or HTTP 202
with no body for notifications and client responses. Send `Content-Type:
application/json` and `Accept: application/json, text/event-stream`. Legacy
POSTs after initialization must include `MCP-Protocol-Version: 2025-11-25`.
The initial `initialize` request can omit that header; its response advertises
2025-11-25. Missing subsequent headers and unsupported versions are rejected.
Requests are limited to 4 MiB. Batch messages are rejected.

No session IDs or server-push SSE streams are created. GET and DELETE return
HTTP 405, as permitted for servers without those features. Disconnection does
not cancel submitted workflows; use `cancel_run` to stop one.

Legacy methods: `initialize`, `tools/list`, `tools/call`, `ping`.
Notifications (`notifications/initialized`) are accepted without response bodies.

### Negotiated Tasks extension (2026-07-28)

Modern clients use `server/discover` and include these fields in every request's
`params._meta` (HTTP also uses `MCP-Protocol-Version: 2026-07-28`):

```json
{
  "io.modelcontextprotocol/protocolVersion": "2026-07-28",
  "io.modelcontextprotocol/clientInfo": {"name": "example", "version": "1"},
  "io.modelcontextprotocol/clientCapabilities": {
    "extensions": {"io.modelcontextprotocol/tasks": {}},
    "elicitation": {"form": {}}
  }
}
```

Modern HTTP requests must also include `Mcp-Method` matching the body method,
and `tools/call` must include `Mcp-Name` matching the tool name. Missing or
conflicting routing headers return HTTP 400 with JSON-RPC code `-32020`
before execution. Unknown modern methods return HTTP 404 with code `-32601`.

Discovery advertises `io.modelcontextprotocol/tasks`. With that extension on
the calling request, `start_run` can return a flat
`{resultType: "task", taskId, status, createdAt, lastUpdatedAt, ttlMs,
pollIntervalMs}` result after the coordinator commits the run and read-back
succeeds. The opaque handle identifies a run invocation, not a Muxlio task
definition. It survives MCP restarts while the run remains retained.

Poll `tasks/get` with `{taskId, _meta}`. It returns `resultType: "complete"`
and task state. A successful or failed workflow becomes `completed` with a
final tool `result`; failed workflows have `result.isError: true`. Cancelled
runs become `cancelled`. Created, running, and paused runs remain `working`
unless outstanding input makes them `input_required`.

Outstanding input appears as an `inputRequests` map keyed by durable request
IDs, with `elicitation/create` form requests. Form capability must be declared
on requests that retrieve or answer outstanding input. Each form has a `json`
string field containing JSON text, preserving arbitrary input schemas within
MCP's flat form schema. Its description supplies the original JSON Schema.
Respond through `tasks/update`:

```json
{
  "taskId": "<returned handle>",
  "inputResponses": {
    "<input request ID>": {"action": "accept", "content": {"json": "false"}}
  },
  "_meta": {"...": "same per-request metadata as above"}
}
```

Updates use the coordinator's authenticated input endpoint, schema validation,
request-specific permissions, and idempotent delivery. Partial responses leave
other requests outstanding. Unknown or already answered keys are ignored;
declined or cancelled forms leave the input outstanding. `tasks/cancel` with
`{taskId, _meta}` requests run cancellation without requiring form capability.
Update and cancel return `{resultType: "complete"}` acknowledgements; poll
again to observe state. On HTTP, send `Mcp-Method` with the method name and
`Mcp-Name` with the task handle for these three operations.

All task operations recheck coordinator authorization for the run and operation
using the configured upstream credential. HTTP listener authentication also
applies to every request; task handles grant no permissions. No task list,
blocking result method, or 2025 experimental Tasks wire format is implemented.
Clients without the per-request extension, including legacy clients, keep
receiving ordinary tool results containing `{runId, state}`. If read-back is
unavailable after successful submission, the server also returns that ordinary
result, preserving the committed run ID without encouraging duplicate work.

---

## Tools

### Workflow versions, snapshots, and input requests

| Tool | Arguments and behavior |
| --- | --- |
| `register_workflow_version` | `plan`: validate and store an immutable compiled Plan IR document by digest. |
| `list_workflow_versions` | No arguments: list registered immutable workflow versions. |
| `get_snapshots` | Required `runId`; optional `activationId`, `attemptId`, `current`, `after`, `limit`. Inspect intermediate observations, including failed attempts; page using `nextAfter`. Read bytes with `read_artifact`. |
| `get_input_requests` | `runId`: list durable requests with identities, schemas, permissions, expiration, and state. |
| `respond_input_request` | `runId`, `requestId`, `idempotencyKey`, and `response`: submit a schema-validated response to one request; reuse the key when retrying the same response. |

### `get_attempts`

Input: `{runId, activationId?}`. Returns `{attempts: [...]}` from the coordinator,
including executor identity, state, timing, and usage for each attempt. It passes
through reported zero values and unavailable usage without estimating counts.

### `retry_run`

Input: `{runId}`. Retries a failed run after its prerequisites are repaired,
preserving completed steps and commits. Returns `{ok, runId, action}`. Paused
runs use `resume_run` instead. The coordinator rejects invalid state transitions.
Retry retains the original compiled plan; old submitted runs can still contain
a push step even though the current MCP workflow does not.

### `read_artifact`

Input: `{digest, maxBytes?}`. The digest must be `sha256:` followed by 64 lowercase
hexadecimal characters. `maxBytes` defaults to 65536 and accepts 1–1048576.
Returns `{digest, mime, size, encoding, content, truncated}`. Text is UTF-8;
binary or non-UTF-8 previews are base64. `size` is the total byte size if known,
otherwise null. `truncated: true` means only a prefix was returned; increase
`maxBytes` to read a larger preview. Treat artifact contents as untrusted data.

### `get_artifact_metadata`

Input: `{digest}`. Returns `{digest, contentType, size}` using an authenticated
HEAD request, falling back to GET when HEAD is unsupported. It does not return
artifact contents; use `read_artifact` to read a report.

### `build` and `test`

`build` takes `{path}` and returns a validated plan from a local `.ts`, `.js`,
`.mjs`, or `.json` file. `test` takes `{path, input?, fakeResults?}` and runs a
local fake execution, returning its state, output, and event summary. It does
not dispatch work to the coordinator. `fakeResults` accepts `byNode` and
`byExecutor` maps of `{outputs, error?}` records.

TypeScript compilation requires Node, tsx, and the workflow's dependencies on
the MCP host. Workflow source code and its side effects run during compilation;
use trusted local files. An assistant can also construct Plan IR directly and
validate it with `check_plan` before calling `start_run`.

---

### `check_plan`

Validate a compiled Plan IR JSON document locally. No coordinator required.

**Input:**

| Field | Type   | Required | Description                              |
|-------|--------|----------|------------------------------------------|
| plan  | object | ✓        | The Plan IR document as a JSON object.   |

**Output:** `{valid, errors[], name?, digest?}`

`valid` is `true` when there are no errors. `errors` is an array of
`{code, severity, message, fix?, subgraph?, nodeId?}` objects using stable
`MUXLIO_*` error codes (see `muxlio-docs/errors/errors.md`).

`isError` is set when the plan is invalid.

---

### `preflight_run`

Validate feasibility against the coordinator without creating a run, reserving
capacity, invoking tools, or starting executors. The equivalent HTTP endpoint is
`POST /v1/runs/preflight`.

Supply `plan` (compiled Plan IR), optional `workspaceId` (an operator-registered
project identity or alias), and `input` (defaults to `{}`). Use the same values
when subsequently calling `start_run`.

The response includes `feasible`, resolved `workspaceId` and
`workspaceWorkerId`, `inputSchema`, plan/input/workspace diagnostics, and an
`executors` entry for each execute node, identified by `subgraph` and `nodeIds`.
Each entry includes required capabilities, isolation and placement constraints,
declared `executionPolicy`, compatible `matchingWorkers`, and `workerEvidence`
containing advertised executor contracts and cached installation/authentication
probe checks. Failed checks retain the worker's diagnostic messages, including
missing executables or companion dependencies.

`verified` names validations performed; consult the diagnostics for failures.
`unverified` and `deferred` explicitly identify checks this response cannot prove.
Worker operator tool policies, current tool grants, authentication, quota,
dynamic input bindings, secret resolution, and actual workspace/environment
compatibility remain subject to execution-time checks. Missing probe categories
are unverified. A passing probe is only a snapshot, and positive feasibility
does not guarantee successful execution or reserve worker capacity.

`isError` is set when `feasible` is false. Fix the reported issues and repeat
preflight before calling `start_run`; preflight never submits automatically.

---

### `start_run`

Submit a compiled Plan IR or an immutable registered version to the coordinator
and start a workflow run.

**Input:**

| Field | Type   | Required | Description                                                    |
|-------|--------|----------|----------------------------------------------------------------|
| plan  | object | Either this or `planDigest` | The Plan IR document. |
| planDigest | string | Either this or `plan` | Content digest returned by `register_workflow_version` or `list_workflow_versions`. The coordinator loads the stored definition. |
| input | object | ✓        | Typed JSON input matching the plan's `inputSchema`. Use `{}` when the plan requires no input. |
| workspaceId | string | | Operator-authorized target workspace. Defaults to the plan's workspace or the configured coordinator default. |
| originLabel | string | | Optional opaque, user-visible submission origin (for example, a client or project label). Stored separately from workflow and run state. |
| idempotencyKey | string | | Stable caller-generated submission key; reuse it on retries. |

**Output:** `{runId, state}` — the new run ID and initial state (`running`).

Supply exactly one of `plan` or `planDigest`. Full plans are validated locally;
registered versions are fetched and validated inside the coordinator. A reference
submission pins exactly the registered bytes and digest, with the authorized
workspace stored separately. A workspace fixed in the plan cannot be overridden.
Missing versions return 404, retired versions return 410, and inputs that do not
match the stored input schema are rejected before creating a run. `isError` is set
if validation fails or the coordinator rejects the request.

Register once, then submit from any authenticated client without resending the
definition:

```json
{"planDigest":"sha256:<registered digest>","workspaceId":"project","input":{"request":"Weekly report"},"idempotencyKey":"weekly-report-1"}
```

Retries with the same key and matching definition, exact input bytes, caller,
and authorized workspace return the original run ID. Changed bindings within
that scope return a conflict. The HTTP equivalent is `POST /v1/runs` with
`Idempotency-Key` as a header instead of a body field. See
[registered workflows](../mcp.md#start-a-registered-workflow) for registration
and retirement details.

---

### `list_runs`

Discover runs without a run ID, including active work and retained history.

All inputs are optional. `workspaceId` matches the authorized workspace identity
stored at submission; configured aliases resolve to that identity. `planDigest`
and `state` match exactly. Stored workspace identities remain searchable after
their execution bindings are removed; unknown identities return no matches.
`createdAfter` (inclusive) and `createdBefore`
(exclusive) accept RFC3339 creation timestamps. `callerId: "me"` selects the
current authenticated caller; selecting another caller ID requires administrator
authorization. Filters narrow the caller's existing authorization.

```json
{"workspaceId":"project","limit":25,"offset":0}
```

Open another client with access to the same coordinator and use the same workspace
selector to find relevant active and recent runs. A different workspace is excluded.
Do not automatically reuse a run based only on a workflow name or origin label.
These labels are opaque metadata, not execution states or filesystem permissions.

The HTTP equivalent is `GET /v1/runs?workspaceId=project&limit=25&offset=0`.
Results expose `workspaceId`, `originLabel`, `callerId` and `planDigest` when known.
Supplying `limit` enables bounded pages (maximum 200) with `offset`, `nextOffset`,
`hasMore`, `total` and scoped `states` counts. All filters apply before pagination;
creation-time ties use run ID for stable ordering. Omit filters for generic
discovery, including old runs without metadata. Discovery does not pin runs or
extend retention. Idempotent retries retain the original origin label.

**Output without pagination:** `{runs: [{id, workflowName, state, workspaceId?, originLabel?, callerId?, planDigest?, createdAt, updatedAt, error?, output?}]}`

---

### `get_run_summary`

Retrieve the same compact outcome and links from Claude, Cursor, Codex or Kiro.
Input: `{runId}`. This read-only tool forwards `GET /v1/runs/:id/summary`.

The response includes persisted `state`, canonical `outcome`, `terminal`, workspace
identity, workflow/plan identity, nullable observed `startedAt`/`finishedAt`, and
creation/update times. Outcomes are `succeeded`, `failed`, `cancelled`, `paused`,
`queued`, `running`, `waiting`, or `uncertain`. Active work, pending input or
reconciliation prevents terminal completion; a concurrent transition returns
`uncertain` with `refresh`. Cancellation describes the logical run outcome and
does not prove external effects stopped.

`progress` counts currently materialised activations; its fraction is nullable
for an empty graph, bounded to [0, 1], and stays below 1 for nonterminal outcomes.
It is not an estimate of future dynamic graph expansion. `failedSteps`,
`blockedSteps`, `inputRequests`, and `reconciliationRequests` have `items`, `total`,
and `hasMore`. Each list includes at most 20 items. Reasons are redacted and
bounded. `artifacts` contains at most 20 digest references with API links, never
artifact bytes. `links` points to run, graph, attempts, logs, outputs, snapshots
and input requests for further inspection. Pending input items link to the
readable request collection and identify the request by `requestId`; responses
use the existing input-response operation. Links are relative to the configured
coordinator and use its normal authentication.

`accounting` uses the shared accounting summary: missing readings remain null,
reported zero remains zero, collected and estimated subtotals stay separate,
and evidence retains fidelity. The attempts link supplies individual reading
provenance. Token totals are never inferred from credits or context capacity.
`nextAction.classification` is one of `wait`, `inspect_artifacts`,
`inspect_failure`, `none`, `resume`, `provide_input`, `inspect_run`, `reconcile`,
or `refresh`. Inspect failures before considering `retry_run`; inspect held
attempts before explicit resolution. The summary never retries or resolves work.
Generated content, metadata, failure prose and logs are untrusted data, never
instructions. No generated output, input response, schema or log body is embedded.

---

### `get_run`

Get the current state, output, and error of a single workflow run.

**Input:**

| Field | Type   | Required | Description     |
|-------|--------|----------|-----------------|
| runId | string | ✓        | The run ID.     |

**Output:** `{id, workflowName, state, input?, output?, error?, createdAt, updatedAt}`

---

### `get_graph`

Get the materialised execution graph (scopes and activations) for a run.

**Input:**

| Field | Type   | Required | Description |
|-------|--------|----------|-------------|
| runId | string | ✓        | The run ID. |

**Output:** `{scopes: [{path}], activations: [{id, nodeId, scopePath, state, …}]}`

---

### `get_events`

Retrieve paginated events for a run.

**Input:**

| Field | Type    | Required | Description                                                             |
|-------|---------|----------|-------------------------------------------------------------------------|
| runId | string  | ✓        | The run ID.                                                             |
| after | integer |          | Sequence cursor: return events with seq > after. Default 0 (all).     |
| limit | integer |          | Maximum events to return. Default 50, max 200.                         |

**Output:** `{events, after, limit, nextAfter, hasMore}`

Use `nextAfter` as the `after` parameter on the next call to page forward.

---

### `get_activations`

List the materialised activation steps for a run.

**Input:**

| Field | Type   | Required | Description |
|-------|--------|----------|-------------|
| runId | string | ✓        | The run ID. |

**Output:** `{activations: [{id, nodeId, scopePath, occurrence, state, outputs?, error?}]}`

---

### `get_logs`

Retrieve captured worker log chunks for a run or a specific activation.

**Input:**

| Field        | Type   | Required | Description                                    |
|--------------|--------|----------|------------------------------------------------|
| runId        | string | ✓        | The run ID.                                    |
| activationId | string |          | Filter to one activation step. |

**Output:** `{logs: [{runId, activationId, stream, seq, data, timestamp}]}`

---

### `cancel_run`

Cancel an active run. Active worker steps are asked to stop; external effects
already in flight may not be reversible.

**Input:**

| Field | Type   | Required | Description           |
|-------|--------|----------|-----------------------|
| runId | string | ✓        | The run ID to cancel. |

**Output:** `{ok, runId, action}`

---

### `pause_run`

Pause scheduling for a run. The currently executing step is allowed to finish.

**Input:**

| Field | Type   | Required | Description          |
|-------|--------|----------|----------------------|
| runId | string | ✓        | The run ID to pause. |

**Output:** `{ok, runId, action}`

---

### `resume_run`

Resume a paused run.

**Input:**

| Field | Type   | Required | Description           |
|-------|--------|----------|-----------------------|
| runId | string | ✓        | The run ID to resume. |

**Output:** `{ok, runId, action}`

---

### `send_signal`

Deliver a typed signal to a run that is waiting for it (a `wait` node with
`waitFor: "signal"`).

**Input:**

| Field      | Type   | Required | Description                                   |
|------------|--------|----------|-----------------------------------------------|
| runId      | string | ✓        | The run ID.                                   |
| signalName | string | ✓        | The signal name the run is waiting for.       |
| payload    | object |          | Optional JSON payload. Use `{}` for a bare signal. |

**Output:** `{ok, runId, signal}`

---

### `list_executors` and `explain_capability`

Start with `list_executors({})` to choose from live registered executors. These
read-only tools aggregate the existing `get_workers` snapshot without launching
adapters or contacting providers. Executor names, protocol capabilities, and
versions come from worker registrations and adapter-owned descriptors. There are
no built-in assumptions that a CLI, ACP, A2A, or command executor supports sessions,
remote tasks, typed results, or usage.

Both tools accept an optional `request` containing shared execute-envelope
constraints. `explain_capability` requires the exact `executor` name:

```json
{
  "executor": "generic/task",
  "request": {
    "contractVersion": "1",
    "requiredCapabilities": {"session": "2", "usage": "1"},
    "session": {"mode": "create"},
    "placementLabels": {"region": "local"}
  }
}
```

Each `executors` entry includes:

- `variants`: separate executor version and descriptor combinations, with the
  original `contract` (including environment pins and `requiresHostExecution`),
  normalized declared `capabilityVersions`, shared-contract `operations`, worker
  counts, and `isolationWorkerCounts` for operator-enabled worker backends.
  Capabilities from different variants are never combined. Missing descriptors
  provide baseline execution only; absent or false optional features are unsupported.
- `eligibleWorkerCount`: workers passing the same dispatch matcher, including
  readiness, exact versions, environment, placement, workspace and session ownership,
  and isolation, with positive capacity and no draining flag.
- `availableWorkerCount`: estimated eligible workers with headroom, assuming
  one slot per active assignment and one slot for the requested executor.
  `queueReason` is `no_eligible_workers`, `worker_full`, or the empty string.
  The response's `capacityEstimate` identifies this assumption and marks weighted
  admission as unverified: the worker snapshot contains neither reserved slot
  weights nor executor slot costs. A worker counted as available can still be
  blocked by weighted worker capacity. These estimates also do not predict
  global, run, key, rate, fairness, or resource admission queues.
- `blockingReasons`: stable reason codes when no worker is eligible.
  `explain_capability` also returns sorted `workers` with cached readiness,
  placement/isolation labels, eligibility, availability, and a `reason` containing
  a machine-readable `code` and actionable `message`. Each worker reports its first
  blocking constraint; correcting it may reveal another. Unknown executors return
  `executor_not_registered` rather than an inferred implementation.

Operations describe contract support: `execute`, `session.legacy`,
`session.create`, `session.resume`, `session.steer`, `remote.submit`, `remote.poll`,
and `reconcile` appear only when their complete capability set and revisions are
advertised. For example, `remoteTask` alone does not establish remote submission
or polling support without `reconcile`. Actual operations still require valid
request fields, provider state, and authorization.

The response includes the worker snapshot's `serverTime`, heartbeat window, and
`unverified` limits. An advertised `usage` capability does not establish that a
particular run will report usage; missing readings remain unavailable. Cached
readiness does not establish current authentication, provider/model functionality,
quota, or tool permissions, and discovery reserves no capacity. Use
`preflight_run` for whole-plan validation and workspace authorization, and inspect
activation queue reasons after submission for actual admission status.

---

### `get_workers`

List currently connected workers and their active assignments.

**Input:** (no fields required)

**Output:** `{workers: [{assignmentId, runId, activationId, workerId, state, leaseUntil, …}]}`

---

### `server_health`

Check coordinator liveness.

**Input:** (no fields required)

**Output:** `{status: "ok"}` — `isError` is set when the coordinator is
unreachable.

---

## Error shape

All tool errors use `isError: true` and return a plain-text explanation in the
`content[0].text` field. Structured validation errors from `check_plan` and
`start_run` use stable `MUXLIO_*` codes (see `muxlio-docs/errors/errors.md`).

HTTP errors from the coordinator include the status code:

```json
{
  "content": [{"type": "text", "text": "HTTP 404: {\"code\":\"not_found\",\"message\":\"run not found\"}"}],
  "isError": true
}
```

---

## Example session

```
> check_plan {"irVersion":"1.0","name":"batch","subgraphs":{"root":{"nodes":[…]}}}
{"valid":true,"name":"batch","digest":"sha256:…","errors":[]}

> start_run plan=<above> input={"items":[{"id":"a","title":"Topic A"}]}
{"runId":"run_abc123","state":"running"}

> get_run run_abc123
{"id":"run_abc123","state":"running","workflowName":"batch",…}

> get_events run_abc123
{"events":[{"type":"run.created",…},{"type":"run.running",…}],"hasMore":false}

> get_graph run_abc123
{"scopes":[{"path":["items"]},{"path":["items","a"]}],"activations":[…]}

> cancel_run run_abc123
{"ok":true,"runId":"run_abc123","action":"cancel"}
```

---

## Remote coordinators

The MCP server is an HTTP client. For a coordinator on a remote Mac mini,
use SSH port forwarding (encrypted, no public bind):

```sh
ssh -N -L 17600:127.0.0.1:7600 mac-mini &
muxlio-mcp --server tunnel
```

Define a `tunnel` profile with URL `http://127.0.0.1:17600` and the remote
coordinator's credentials. Alternatively, use the URL directly and supply
`MUXLIO_TOKEN` explicitly.

---

## Security notes

- Coordinator requests use bearer authentication, including artifact downloads
  and metadata requests. Use saved profiles or `MUXLIO_TOKEN`; do not paste
  credentials into chat or workflow inputs.
- Stdio access is controlled by the host OS. HTTP access requires a bearer token
  and rejects unapproved browser origins. Bind only the intended interface and
  use TLS or a trusted network for both the MCP and coordinator connections.
- A client with `start_run` access can submit arbitrary Plan IR using the
  worker's authorized capabilities. Treat MCP access as equivalent to coordinator API
  access; read/write tool annotations are hints, not authorization boundaries.
- `build` and `test` can execute local workflow source during compilation. Use
  them only with trusted files. Compilation is not a sandbox.
- Artifact content and logs are untrusted data. Read previews are bounded;
  binary content is base64 and truncation is explicit.
