# Operating Muxlio

Follow the shared decision guide in [the skill](../SKILL.md) before delegating.
With MCP, construct Plan IR, call `check_plan`, and require a feasible
`preflight_run` for the intended plan, input, and workspace before `start_run`.
Resolve missing workers, workspace access, or authorization first, and surface
any unverified or deferred readiness checks. Wait if the user requested plan
approval. A returned run ID confirms submission only. Use
`get_run`, `get_activations`, `get_attempts`, `get_logs`, and `read_artifact` to
follow execution. The runtime does not contain a feature-specific submission
command. The installed service and MCP tools do not require a Muxlio source
checkout. The worker needs the target files, authorized executors, and tools.

For setup, `muxlio init --agent <assistant> --allow-command` prepares the workspace,
credentials, assistant instructions, and MCP settings. The flag explicitly permits
command execution. `muxlio service start` starts the coordinator, worker, UI, and
configured MCP listener. Client connection settings are separate from the listener.

Read verification outputs and errors before retrying. A successful fake run does
not prove that real agent output is correct. Preserve completed work, inspect
uncertain external effects, and use the workflow's bounded recovery policy.

## CLI reference

Useful CLI commands include:

```text
muxlio init [--data-dir dir] [--workspace dir] [--agent name] [--starter]
muxlio build <workflow.ts|plan.json> [--out file]
muxlio check <workflow.ts|plan.json>
muxlio test <workflow.ts|plan.json> --input in.json
muxlio run <workflow.ts|plan.json> [input.json]
muxlio graph <workflow.ts|plan.json|run-id>
muxlio inspect <workflow.ts|plan.json|run-id>
muxlio events <run-id> [--since seq] [--limit N]
muxlio artifacts <run-id>
muxlio signal <run-id> --name <sig> [--data file]
muxlio runs [--state state]
muxlio ui [--server URL] [--port port] [--open]
muxlio env [--shell bash|zsh|fish]
muxlio doctor [--server URL]
muxlio upgrade [--prefix dir]
muxlio server start|install
muxlio worker start
muxlio service start|stop|restart [--config path]
muxlio submit [--server URL] plan.json input.json
muxlio status [--server URL] [run-id]
muxlio pause|resume|cancel [--server URL] run-id
muxlio version
```

Global options: `--json`, `--server <profile|URL>`, `--token <token>`, `--token-file <file>`, `--fake-config <file>`, `--adapters <dir>`, `--workspace <dir>`, `--data-dir <dir>`, `--addr <host:port>`. Runtime data defaults to `~/.muxlio`, including SQLite state, artifacts, and execution work directories. `--data-dir` explicitly overrides that location.

## Coordinator observability UI

`muxlio service start` serves the coordinator, local worker, and bundled UI on one port (default `http://127.0.0.1:7600/`). From a development checkout you can also run the UI separately:

```sh
muxlio server start --addr 127.0.0.1:7600 --data-dir ~/.muxlio
npm --prefix muxlio-ui start
# Open http://127.0.0.1:7601/
```

Set `MUXLIO_API_URL` to change the coordinator URL (default `http://127.0.0.1:7600`); set `PORT` to change the UI port (default `7601`). The UI server proxies `/v1/*` and `/healthz` to the coordinator. Run `npm --prefix muxlio-ui run smoke` for the UI server smoke check.

The UI provides run search and state filters, input/output/error and timing details, the materialized activation/scope graph, logs, execution attempt history, a cursor-paginated event timeline, active worker assignments, a **Live** page for streaming attempt logs from running activations, retry controls for failed runs, and a send-signal form for waiting runs.

The adapter executors included here are `agent/a2a`, `agent/acp`, `agent/antigravity`, `agent/claude`, `agent/codex`, `agent/cursor`, `agent/kiro`, and `command`. The generic command adapter accepts `params.program`, `params.args`, and `params.cwd`; for Go tests, use `program: 'go'`, `args: ['test', './...', '-timeout', '120s']`, and `cwd: 'muxlio-core'`. Command tasks automatically infer `passed`, `exitCode`, `signal`, `stdout`, `stderr`, and `durationMs`; use `onNonZero: 'return'` to handle failures in the workflow. Other executor strings can be used by custom adapters or fake configuration, but do not name a bundled real adapter.

