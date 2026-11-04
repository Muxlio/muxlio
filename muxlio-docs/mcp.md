# Muxlio MCP Server

The `muxlio-mcp` binary exposes Muxlio operations as tools over the
[Model Context Protocol](https://spec.modelcontextprotocol.io/) (MCP).
Any MCP-capable assistant — Codex, Cursor, Claude Code, or another — can call
these tools to build, check, run, and inspect Muxlio workflows without leaving
its chat interface.

The MCP server is an adapter to the existing HTTP API. It does not execute
runs itself; the Muxlio coordinator must be running separately.

---

## Prerequisites

1. Build or install Muxlio. After `bash scripts/build-release.sh PREFIX`, the
   binary lives at `PREFIX/bin/muxlio-mcp`.
2. Have a reachable coordinator and any credentials it requires. It can run on
   another machine; no local coordinator or worker is required on the MCP host.
   See the [service guide](service.md) to deploy one if needed.
3. Configure its URL or saved profile, then call `server_health` and `get_workers`
   to check connectivity and execution capacity.

---

## Configuration

### Coordinator URL

The server uses `--server`, then `MUXLIO_SERVER`, then `defaultServer` in
`~/.muxlio/config.json`. One must be set. Both explicit
HTTP(S) URLs and saved `servers` / `profiles` names are supported:

```sh
muxlio-mcp --server yamini
```

Profiles may be URL strings or objects with `url`, `token`, and/or `tokenFile`.
`MUXLIO_TOKEN` overrides configured tokens. A selected profile uses its `token`
or `tokenFile`, falling back to top-level credentials if omitted. When
`defaultServer` is a URL, it uses top-level `token` / `tokenFile`. Relative token
file paths resolve against the config directory.

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
      "args": ["--server", "yamini"]
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
        "MUXLIO_SERVER": "yamini"
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
      "args": ["--server", "yamini"]
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
      "args": ["--server", "yamini"]
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
      "args": ["--server", "yamini"]
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

For a network listener on `yamini`, run:

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

### ChatGPT desktop

For a direct network connection to `yamini`, choose **Streamable HTTP** in
Settings → MCP servers and use `http://192.168.1.1:7602/mcp` (or its HTTPS URL
when TLS is configured). Configure the listener's bearer token locally in the
client. Neither SSH nor a local Muxlio MCP process is needed.

The equivalent local `config.toml` entry is:

```toml
[mcp_servers.muxlio]
url = "http://192.168.1.1:7602/mcp"
bearer_token_env_var = "MUXLIO_MCP_HTTP_TOKEN"
```

This variable must be available to the ChatGPT desktop process. Alternatively,
configure a static `http_headers.Authorization` bearer header in the protected
local config. Keep the token out of chat and source control. Restart the MCP
connection after changing its configuration.

For stdio, add a **STDIO** server using the absolute path to `muxlio-mcp` and
arguments `--server yamini`. ChatGPT then starts the MCP process locally; only
the coordinator needs to be reachable. ChatGPT desktop and Codex clients on the
same host share MCP configuration.

Start by calling `server_health` and `get_workers`; `registeredWorkers` lists
live workers, while `workers` contains active assignments. Then use
`submit_features` with a small feature and inspect progress through `get_run`.
See the [official MCP setup](https://learn.chatgpt.com/docs/extend/mcp) for client
settings. Hosted ChatGPT web/cloud plugins have different authentication and
connectivity requirements; see the
[plugin connection guide](https://developers.openai.com/plugins/deploy/connect-chatgpt).
Muxlio provides fixed bearer authentication, not an OAuth authorization server.

Ensure `muxlio-mcp` is on `PATH` or use its absolute path.

---

## Protocol

Both transports support MCP 2025-11-25 only. Stdio uses newline-delimited
JSON-RPC 2.0. Startup diagnostics go to stderr in both modes.

HTTP implements the stateless JSON-response form of
[Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports):
POST `/mcp` accepts one JSON-RPC message and returns a JSON response, or HTTP 202
with no body for notifications and client responses. Send `Content-Type:
application/json` and `Accept: application/json, text/event-stream`. Subsequent
POSTs after initialization must include `MCP-Protocol-Version: 2025-11-25`.
The initial `initialize` request can omit that header; its response advertises
2025-11-25. Missing subsequent headers and unsupported versions are rejected.
Requests are limited to 4 MiB. Batch messages are rejected.

No session IDs or server-push SSE streams are created. GET and DELETE return
HTTP 405, as permitted for servers without those features. Disconnection does
not cancel submitted workflows; use `cancel_run` to stop one.

Supported methods: `initialize`, `tools/list`, `tools/call`, `ping`.
Notifications (`notifications/initialized`) are accepted without response bodies.

---

## Tools

### `submit_features`

Submit a feature workflow without writing a workflow file or constructing Plan
IR yourself. It runs implementation → review → verification/fix → local
commit for each feature, serially. It never pushes. The worker needs a clean
workspace, `executors.command.allow: true`, the workflow's agent executors, and
the repository's build/test tools.

```json
{
  "features": [
    { "id": "docs-checklist", "feature": "Add a service troubleshooting checklist", "context": "Keep the change small and documentation-only." }
  ]
}
```

`features` requires 1–20 entries, each with a unique, nonblank `id` and nonblank
`feature`; `context` is optional. Optional `branch` supplies context to the
implementation agent; it does not create or check out a branch.

Returns `{runId, state}` immediately. Poll `get_run`; inspect individual steps
with `get_activations`, `get_attempts`, and `get_logs`. Read reports using the
artifact digests in activation outputs. If a run fails, fix the cause and use
`retry_run` to preserve completed work rather than submitting duplicate work.

MCP constructs and hashes the workflow directly in Go for each submission.
It does not load the TypeScript recipe, read a generated plan, or invoke a
compiler. Submission therefore needs neither Node nor a source checkout on the
MCP host; the worker still needs the repository's build and test tools.

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
use trusted local files. The `submit_features` path constructs Plan IR directly
and avoids compilation.

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

### `start_run`

Submit a compiled Plan IR to the coordinator and start a workflow run.

**Input:**

| Field | Type   | Required | Description                                                    |
|-------|--------|----------|----------------------------------------------------------------|
| plan  | object | ✓        | The Plan IR document.                                          |
| input | object | ✓        | Typed JSON input matching the plan's `inputSchema`. Use `{}` when the plan requires no input. |

**Output:** `{runId, state}` — the new run ID and initial state (`running`).

The tool performs local validation before submitting; `isError` is set if
the plan is invalid or the coordinator rejects the request.

---

### `list_runs`

List all workflow runs known to the coordinator.

**Input:** (no fields required)

**Output:** `{runs: [{id, workflowName, state, createdAt, updatedAt, error?, output?}]}`

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
  worker's authorized capabilities. `submit_features` also makes workspace
  changes and local commits. Treat MCP access as equivalent to coordinator API
  access; read/write tool annotations are hints, not authorization boundaries.
- `build` and `test` can execute local workflow source during compilation. Use
  them only with trusted files. Compilation is not a sandbox.
- Artifact content and logs are untrusted data. Read previews are bounded;
  binary content is base64 and truncation is explicit.
