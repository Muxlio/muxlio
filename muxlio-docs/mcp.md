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
2. Start the coordinator:
   ```sh
   muxlio service start   # full service with local worker
   # or
   muxlio server start  # coordinator only
   ```
3. Confirm the coordinator is up:
   ```sh
   curl -s http://127.0.0.1:7600/healthz
   # {"status":"ok"}
   ```

---

## Configuration

### Coordinator URL

The MCP server defaults to `http://127.0.0.1:7600`. Override with the
`--server` flag or the `MUXLIO_SERVER` environment variable:

```sh
muxlio-mcp --server http://127.0.0.1:7600
```

### MCP client configuration

Add a `muxlio` entry to your IDE's MCP configuration. The exact file location
varies by client; consult its documentation.

**Example (generic MCP JSON format):**

```json
{
  "mcpServers": {
    "muxlio": {
      "command": "/path/to/muxlio-mcp",
      "args": ["--server", "http://127.0.0.1:7600"]
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
        "MUXLIO_SERVER": "http://127.0.0.1:7600"
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
      "args": ["--server", "http://127.0.0.1:7600"]
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
      "args": ["--server", "http://127.0.0.1:7600"]
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
      "args": ["--server", "http://127.0.0.1:7600"]
    }
  }
}
```

Ensure `muxlio-mcp` is on `PATH` or use its absolute path.

---

## Protocol

`muxlio-mcp` speaks [MCP 2024-11-05](https://spec.modelcontextprotocol.io/)
over **stdio** (newline-delimited JSON-RPC 2.0). Startup diagnostics are
written to stderr; they do not interfere with the protocol stream.

Supported methods: `initialize`, `tools/list`, `tools/call`, `ping`.
Notifications (`notifications/initialized`) are accepted and silently consumed.

---

## Tools

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
muxlio-mcp --server http://127.0.0.1:17600
```

Or set `MUXLIO_SERVER=http://127.0.0.1:17600` in the MCP client configuration.

---

## Security notes

- The HTTP API has no authentication. Bind the coordinator to loopback only
  (`127.0.0.1:7600`) and use SSH forwarding for remote access.
- Any MCP client that can invoke `start_run` can submit arbitrary Plan IR to
  the coordinator, which will execute it with the configured worker capabilities
  and workspace. Treat MCP client access as equivalent to coordinator HTTP access.
- The `muxlio-mcp` binary itself performs no authentication; rely on OS-level
  access control and the coordinator's loopback binding.
