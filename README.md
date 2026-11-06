<p align="center">
  <img src="logo.png" alt="Muxlio" width="160">
</p>

<h1 align="center">Muxlio</h1>

<p align="center">
  A durable workflow execution engine that coordinates AI coding agents through compiled, content-addressed plans.
</p>

<p align="center">
  <a href="#license">Apache-2.0 License</a> &middot; <a href="muxlio-docs/mcp.md">MCP Integration</a> &middot; <a href="muxlio-docs/service.md">Service Guide</a>
</p>

---

Muxlio lets you author multi-step workflows that fan out across AI coding agents — Claude, Codex, Cursor, Gemini CLI, Kiro — and command tools, then run them durably on a coordinator that survives client disconnects. Submit a plan tonight and check the results in the morning.

Workflows are defined in TypeScript, compiled to a canonical **Plan IR** JSON document, and interpreted by a Go coordinator. No JavaScript reaches the runtime: the IR is the contract between authoring and execution. Plans are immutable and content-addressed (SHA-256 digest), so a run always executes the exact bytes that were submitted.

## How it works

```
┌─────────────────┐     compile      ┌──────────────┐      submit      ┌─────────────────────────┐
│  TypeScript SDK │  ──────────────▶ │   Plan IR    │  ──────────────▶ │     Go Coordinator       │
│  (authoring)    │                  │  (JSON,      │                   │  • durable SQLite state  │
│  workflow/task  │                  │   immutable, │                   │  • recovery & retry      │
│  builder API     │                  │   digest)    │                   │  • signals / pause       │
└─────────────────┘                  └──────────────┘                   │  • event streaming (SSE)  │
                                                                        └────────────┬────────────┘
                                                                                     │ assigns
                                                                        ┌────────────▼────────────┐
                                                                        │   Outbound Workers       │
                                                                        │   adapter scripts →      │
                                                                        │   agent CLIs & commands  │
                                                                        └─────────────────────────┘
```

## Key features

- **Compiled, content-addressed plans** — Workflows compile to a stable Plan IR (v1.0) with a SHA-256 digest. A run pins immutable plan JSON; recovery verifies those bytes, never a latest definition.
- **Fan-out and control flow** — `map` for bounded-concurrency fan-out, `branch` for conditionals, `loop` with finite iteration bounds, `wait` for external signals, `join` for aggregation, `child` for nested sub-runs.
- **Durable execution** — SQLite-backed state, event-sourced history, and explicit recovery. Queued work is eligible for reassignment; effects that may have already happened are held for manual reconciliation rather than silently replayed.
- **Retry and fallback** — Per-step retry policies (`maxAttempts`, bounded backoff) and fallback executors. Effect policies control whether repeating an operation is safe.
- **Agent adapters** — Ship adapters for `agent/claude`, `agent/codex`, `agent/cursor`, `agent/gemini-cli`, `agent/kiro`, and `command`. Custom adapter scripts plug in with no coordinator changes.
- **Observability UI** — A web dashboard for run search, activation/scope graphs, event timelines, logs, artifacts, attempt history, worker status, a **Live** view that polls running activations for agent output, and a signal form for waiting runs.
- **MCP server** — Exposes coordinator operations as MCP tools so any MCP-capable assistant (Codex, Cursor, Claude Code, Kiro) can build, check, submit, and inspect runs from its own chat.
- **Fake executor for testing** — Validate plans and dataflow end to end without invoking real model calls. Fake outputs are keyed by node ID or executor name.
- **Remote access over SSH** — The coordinator binds to loopback; forward over SSH for secure remote use without exposing an unauthenticated HTTP API.

## Quick start

### Prerequisites

- **Go** 1.26+ (to build the coordinator, CLI, and adapters)
- **Node.js** 18+ (for the TypeScript SDK, compiler, testing harness, and UI)
- **Agent provider CLIs** on `PATH` for whichever executors you intend to use (e.g. `claude`, `codex`, `gemini`, `kiro`)

### Build from source

```sh
npm run setup          # builds release prefix under ~/.muxlio and muxlio init
npm run release        # build CLI, adapters, and UI into ~/.muxlio (no init)
```

`npm run setup` runs `scripts/setup.sh`: `scripts/build-release.sh` (Go CLI, six adapters, UI assets), copies the CLI to `.bin/muxlio` for this checkout, and runs `muxlio init` to create `~/.muxlio/config.json` with a coordinator bearer token. Setup authorizes `~/.muxlio` itself as the workspace; use `muxlio init --workspace /absolute/path/to/project` afterward to select a project workspace. Authoring workflows from this checkout still requires `npm run build` once to compile the TypeScript workspaces (`muxlio-sdk`, `muxlio-compiler`, `muxlio-testing`). Override the runtime root with `MUXLIO_HOME`.

`npm run release` runs the same `scripts/build-release.sh` install into `~/.muxlio` (or `MUXLIO_HOME`) without `muxlio init`, SDK workspace builds, or copying the CLI into this checkout’s `.bin/muxlio`. Use it to refresh binaries and UI assets in an existing runtime.

For a custom install prefix without the npm wrapper:

```sh
bash scripts/build-release.sh "$HOME/.local/muxlio"
export PATH="$HOME/.local/muxlio/bin:$PATH"
muxlio init --data-dir "$HOME/.local/muxlio" --workspace /absolute/path/to/project
```

### Initialize runtime

Before `muxlio service start`, point the service at an authorized workspace and the executor capabilities the local worker should advertise:

```sh
# Example: choose a project workspace and install Gemini authoring instructions
muxlio init --data-dir ~/.muxlio --workspace /absolute/path/to/project --agent gemini
# Creates ~/.muxlio/{data,logs,work,artifacts,bin,skills,libexec/muxlio/adapters,share/muxlio/ui}
# and ~/.muxlio/config.json (workspace, capabilities, addr, attemptTimeoutSeconds)
# A coordinator bearer token is generated and stored in config.json.
# --starter writes a sample workflow into the workspace; --agent installs muxlio-skills/SKILL.md for that agent.
```

Default capabilities match the shipped adapters: `agent/claude`, `agent/codex`, `agent/cursor`, `agent/gemini-cli`, `agent/kiro`, and `command`. Edit `capabilities` in config to restrict what the service will run.

### Install with Homebrew

Add the Muxlio tap and install the formula:

```sh
brew tap Muxlio/muxlio
brew install muxlio
```

The formula installs `muxlio`, `muxlio-mcp`, the bundled adapters, UI assets,
and `SKILL.md`. Runtime state and configuration remain in `~/.muxlio`.

To publish a formula release, first commit the source and formula changes, then
run the release helper with the new semantic version:

```sh
scripts/release-formula.sh 0.2.1
```

The helper creates and pushes the `v0.2.1` source tag, downloads its GitHub
archive, computes its SHA-256, and updates `Formula/muxlio.rb`. Review and commit
that formula update to the tap so Homebrew can install the release.

### Author a workflow

Workflows are authored with the TypeScript SDK and compile to Plan IR at build time. The high-level API — `workflow`, `task`, `artifact`, `step`, `each`, `repeatUntil` — uses symbolic typed references and Zod schemas; the lower-level `defineWorkflow` builder remains available for advanced cases.

```ts
import { workflow, task, artifact } from '@muxlio/sdk';
import { z } from 'zod';

const input = z.object({
  items: z.array(z.object({ id: z.string(), title: z.string() })).min(1).max(100),
});

const gather = task({
  executor: 'agent/gemini-cli',
  task: 'Investigate the topic and write findings to findings.md.',
  outputs: { findings: artifact.markdown('findings.md') },
});

const transform = task({
  executor: 'agent/claude',
  task: 'Read the findings and produce result.md.',
  outputs: { result: artifact.markdown('result.md') },
});

export default workflow('batch', {
  input,

  build: ({ input, step, each }) => {
    const results = each(input.items, {
      key: i => i.id,
      concurrency: 5,
    }, item => {
      const gathered = step('gather', gather, { item });
      const transformed = step('transform', transform, {
        findings: gathered.findings,
      });
      return { id: item.id, result: transformed.result };
    });

    return { results };
  },
});
```

The `build` callback runs once at compile time. `step`, `each`, and `repeatUntil` return symbolic proxies — `gathered.findings`, `item.id`, and `transformed.result` are typed references compiled into the plan, not runtime values. Two compilations of the same workflow yield identical content-addressed (SHA-256) digests. `repeatUntil` compiles to a durable loop: run an attempt, evaluate `until` against its outputs, run `otherwise` (a fix) on failure, and `onExhausted: 'pause'` makes the run resumable. See `recipes/implement-features/workflow.ts` for a full prepare → implement → review → verify/fix loop → commit recipe.

Compile it (TypeScript → Plan IR):

```sh
muxlio build examples/batch.workflow.ts --out examples/batch.plan.json
# or validate/compile in one step:
muxlio check examples/batch.workflow.ts
```

`muxlio build`, `check`, `test`, and `run` use the same workflow loading path for `.ts`, `.js`, and `.mjs` files. The CLI executes the module with `npx tsx` from the workflow file's directory, so workflow code and its side effects run during compilation. The module must default-export an object with a `plan` property or a plan-shaped object. Pass an existing `.json` file to skip workflow execution. `muxlio build` validates and prints canonical Plan IR on stdout by default; use `--out` to write it and `--json` for a structured summary. You can also run `npx tsx examples/batch.workflow.ts` directly when iterating on the SDK.

### Validate and fake-run

```sh
muxlio check examples/batch.plan.json
muxlio test examples/batch.plan.json --input examples/batch.input.json --json
# equivalent one-shot local run:
muxlio run examples/batch.plan.json examples/batch.input.json --json
```

With no `--adapters`, `run` uses the fake executor, which generates placeholder artifact references for declared output ports. This exercises the full execution engine and dataflow without invoking agents. Supply `--fake-config` to control fake outputs by node ID or executor name.

### A small local authoring loop

`examples/local.workflow.ts` mixes an inline command task with an inline agent
summary task. From a built checkout, check the bindings, fake-run the same plan,
and inspect its persisted result:

```sh
muxlio check examples/local.workflow.ts --json
muxlio build examples/local.workflow.ts --out /tmp/local.plan.json --json
muxlio test /tmp/local.plan.json --input examples/local.input.json --fake-config examples/local.fake.json --json
muxlio run /tmp/local.plan.json examples/local.input.json --fake-config examples/local.fake.json --json
muxlio inspect RUN_ID --json
muxlio graph RUN_ID --json
muxlio artifacts RUN_ID --json
```

Replace `RUN_ID` with the `runId` from `run`. Fake execution needs no provider
credentials; `local.fake.json` supplies the command's typed process outputs.
Check/build validate binding shapes, node references (including
nested expressions), declared execute output ports, and dependency cycles before
execution starts. Warnings remain advisory for local and remote runs.

For real execution with a coordinator and one local worker, initialize your
workspace as above, authorize the command executor, and start `muxlio service
start`. Then use `muxlio run examples/local.workflow.ts examples/local.input.json
--server http://127.0.0.1:7600 --json`. This compiles locally and uses the same
worker assignment contract as distributed execution. Inspect the run in the UI
for its graph, queue reason, attempts, and artifacts. `muxlio cancel RUN_ID
--server http://127.0.0.1:7600` cancels it; `muxlio resume RUN_ID --server
http://127.0.0.1:7600` continues a paused run.

### Run with real adapters

After `npm run setup` or `build-release.sh`, adapters live under `~/.muxlio/libexec/muxlio/adapters` (see `muxlio env`). Point `run` at that directory with an absolute workspace:

```sh
muxlio run examples/batch.plan.json examples/batch.input.json \
  --adapters "$HOME/.muxlio/libexec/muxlio/adapters" \
  --workspace /absolute/path/to/project \
  --data-dir "$HOME/.muxlio" \
  --json
```

Each executor maps to one executable file: replace `/` with `-` (e.g. `agent/claude` → `agent-claude`, `command` → `command`). The generic `command` executor passes `program` and `args` directly to the process without a shell. `task({ executor: 'command', ... })` automatically provides typed process outputs, so success is available as `result.passed`, including in `repeatUntil` conditions; no `outputs` declaration is needed. With `onNonZero: 'return'`, a nonzero exit produces `passed: false` so the workflow can run its fix step; `onNonZero: 'fail'` fails the step instead. Command details are also top-level outputs: `exitCode`, `signal`, `stdout`, `stderr`, and `durationMs`. Declare `outputs` only to add artifacts or customize a schema. `signal` is null unless the process was terminated by a signal, and stdout/stderr contain bounded previews. Declared files are collected from `MUXLIO_OUTPUT_DIR`.

Executor permissions and environment values are configured per executor in `~/.muxlio/config.json`. An adapter can declare `requiresHostExecution` in a descriptor beside its executable (`<executable>.json`). Workers apply that shared metadata when advertising and dispatching capabilities. Host execution requires explicit local authorization; workflow parameters cannot grant it. The bundled command adapter also checks the authorization in its runtime request, so missing metadata never enables it. Its `execution.isolation: "sandbox"` option fails closed because this release has no verified OCI command backend.

`examples/command.workflow.ts` shows two tasks using the same executor. Task output files live under the per-attempt artifact directory, exposed to child programs through `MUXLIO_OUTPUT_DIR`; executables must be installed and authorized on the selected worker. Host mode is intended for a self-hosted, single-owner worker. There is no OCI sandbox support in this release.

To authorize command tasks, configure the command executor and restart the service:

```json
{
  "executors": {
    "command": {
      "allow": true
    }
  }
}
```

The `executors` map is optional and defaults to `{}`. Omitted entries use executor defaults; `allow: false` disables an executor, and `allow: true` explicitly authorizes it. The same configuration applies to service, standalone, and outbound workers. Environment values are scoped to the named executor and are not inherited from the shell. Isolated execution does not receive these values. These entries replace the old top-level `commandHostEnabled` and `sshAgentSocket` settings.

The command adapter forwards the worker's scoped environment generically,
keeping its scratch `HOME`, `TMPDIR`, and output directory. Task `params.env`
can override forwarded values, except managed `PATH`, `HOME`, `TMPDIR`, `LANG`,
and `MUXLIO_OUTPUT_DIR`.

### Implement-features recipe

`recipes/implement-features/workflow.ts` is a ready-to-use recipe: in the current git workspace, each feature runs implement → review → a verify/fix loop (`repeatUntil`, `maxAttempts: 10`, `onExhausted: 'pause'`) → commit. Features run serially (`concurrency: 1`); the commit step is gated on a passed verification via `{ requiresPassed }`. The recipe finishes with verified local commits; it does not push or require Git remote authentication.

Review the commits in the worker's workspace and push manually when ready.
Commits stay on that worker until you push them.

```sh
muxlio check recipes/implement-features/workflow.ts
muxlio run recipes/implement-features/workflow.ts recipes/implement-features/input.json \
  --adapters adapters --data-dir ~/.muxlio --json
```

The recipe splits task definitions into `tasks.ts` and Zod input schemas into `schemas.ts`, imported by `workflow.ts` — the same pattern to follow for your own multi-file workflows.

## Submit tonight, check in the morning

The durable service pattern keeps work running after you disconnect:

```sh
# Requires muxlio init (or ~/.muxlio/config.json with absolute workspace + capabilities)
muxlio service start
# Background daemon: coordinator, outbound local worker (worker-id local), and bundled UI
# Default http://127.0.0.1:7600 — logs in ~/.muxlio/logs/service.log (service.pid / service.lock)

# Submit compiled Plan IR JSON (not .ts); CLI prints the coordinator response and exits
muxlio submit --server http://127.0.0.1:7600 plan.json input.json
# Loopback requests load the bearer token from ~/.muxlio/config.json automatically
# Use muxlio build/check first, or muxlio run plan.ts input.json --server ... to compile locally then block until done

# Check on it later
muxlio status --server http://127.0.0.1:7600
muxlio status --server http://127.0.0.1:7600 run_RETURNED_ID

# Graceful shutdown or reload config/process
muxlio service stop
muxlio service restart
```

`muxlio run plan.ts input.json --server http://127.0.0.1:7600` compiles the workflow locally, submits it remotely, and blocks until a terminal state (like submit + poll). Closing the client or SSH session does not cancel the coordinator's work. Pause takes effect at the next operation boundary; cancel stops local adapter processes when possible.

For remote access, forward over SSH instead of exposing the API (see [muxlio-docs/service.md](muxlio-docs/service.md#submit-tonight-check-in-the-morning) for details):

```sh
ssh -N -L 17600:127.0.0.1:7600 mac-mini
muxlio submit --server http://127.0.0.1:17600 plan.json input.json
```

## Observability UI

With the supervised service, static UI assets are served from the coordinator address (same port as the API):

```sh
muxlio service start
# Open http://127.0.0.1:7600/  (API under /v1/, UI proxies via /_ui-api/v1/)
```

For a coordinator without the bundled UI, start the API and launch the UI helper on a second port:

```sh
muxlio server start --data-dir ~/.muxlio
muxlio ui --server http://127.0.0.1:7600 --port 7601
# Open http://127.0.0.1:7601/
```

From a development checkout you can also run `npm --prefix muxlio-ui start` with `MUXLIO_API_URL=http://127.0.0.1:7600`. The dashboard provides run search and state filters, input/output/error and timing details, the materialized activation/scope graph, logs, execution attempt history, a cursor-paginated event timeline, active worker assignments, a **Live** page for streaming attempt logs from running activations, and a send-signal form for waiting runs.

## Workers

- **Local worker (service)** — `muxlio service start` registers an outbound worker (`worker-id` `local`) that executes steps in `config.json` `workspace` using adapters from `adapters` (default `~/.muxlio/libexec/muxlio/adapters`). Only capabilities listed in `config.json` are advertised, even if more adapters exist on disk.
- **Remote worker** — On another machine, connect with explicit identity and optional capacity:

```sh
muxlio worker start \
  --coordinator http://127.0.0.1:7600 \
  --worker-id mac-mini-1 \
  --workspace /path/to/workspace \
  --worker-token-file ~/.muxlio/worker-token \
  --capacity 2
# Use MUXLIO_WORKER_TOKEN or --worker-token / --worker-token-file for remote worker authentication.
```

Omit `--capabilities` to auto-detect executable adapters in `--adapters` (default `~/.muxlio/adapters`). For a release install, pass `--adapters ~/.muxlio/libexec/muxlio/adapters`; alternatively copy or symlink the adapters there. Pass a comma-separated list to advertise a subset. `--capacity` sets concurrent assignments (default 1; override with `MUXLIO_WORKER_CAPACITY`).

Supported first-party executors (one adapter binary each): `agent/claude`, `agent/codex`, `agent/cursor`, `agent/gemini-cli`, `agent/kiro`, and `command`.

## MCP integration

The `muxlio-mcp` binary exposes coordinator operations as MCP tools over stdio or authenticated Streamable HTTP. Any MCP-capable assistant can call them to build, check, run, and inspect workflows without leaving its chat:

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

`muxlio init` registers MCP HTTP at `127.0.0.1:7602` and prepares persistent
authentication tokens. The service starts and stops MCP with the coordinator.
For direct network access, run `muxlio init --mcp-listen 0.0.0.0:7602` and
`muxlio service restart` on the server, then connect the client to
`http://<server>:7602/mcp`. HTTP requests
require the listener's bearer token (the coordinator token by default); optional
TLS is available with `mcp.tlsCert` and `mcp.tlsKey`. No SSH or local MCP process is
required for this connection. See the [network setup](muxlio-docs/mcp.md#network-transport-streamable-http).

Available tools include `submit_features` (an MCP-constructed workflow, with local commits and no push), `build`, `test`, `check_plan`, `start_run`, `list_runs`, `get_run`, `get_graph`, `get_events`, `get_activations`, `get_attempts`, `get_logs`, `read_artifact`, `get_artifact_metadata`, `cancel_run`, `pause_run`, `resume_run`, `retry_run`, `send_signal`, `get_workers`, and `server_health`. Saved coordinator profiles and bearer authentication are supported; for example, use `muxlio-mcp --server yamini`. See [muxlio-docs/mcp.md](muxlio-docs/mcp.md) for the full reference.

## Testing with fakes

`@muxlio/testing` exports `testWorkflow` and `fakeExecutor` for validating workflows end to end without real model calls:

```ts
import { workflow, task, artifact } from '@muxlio/sdk';
import { z } from 'zod';
import { fakeExecutor, testWorkflow } from '@muxlio/testing';

const summarize = task({
  executor: 'agent/codex',
  task: 'Summarize the input.',
  outputs: { summary: artifact.markdown('summary.md') },
});

const compiled = workflow('example', {
  input: z.object({ text: z.string() }),
  build: ({ input, step }) => {
    const s = step('summarize', summarize, { text: input.text });
    return { summary: s.summary };
  },
});

const result = await testWorkflow(compiled, {
  input: { text: 'Example' },
  executors: fakeExecutor.byNode({
    summarize: { outputs: { summary: { digest: 'sha256:fixture', mime: 'text/markdown' } } },
  }),
});
if (result.state !== 'succeeded') throw new Error(`Run state: ${result.state}`);
```

## Monorepo structure

| Path | Language | Description |
|------|----------|-------------|
| `muxlio-core` | Go | Coordinator, `muxlio` CLI, `muxlio-mcp` server, Plan IR interpreter, SQLite state store |
| `muxlio-adapters` | Go | Executor adapter binaries (agent CLIs and command tools) |
| `muxlio-sdk` | TypeScript | `@muxlio/sdk` — plan builder and types for authoring workflows |
| `muxlio-compiler` | TypeScript | `@muxlio/compiler` — restricted TS frontend that lowers to Plan IR |
| `muxlio-testing` | TypeScript | `@muxlio/testing` — fake workers, fixtures, and test harness |
| `muxlio-ui` | JS/HTML | Observability web UI for the coordinator |
| `muxlio-skills` | Markdown | Workflow authoring guide |
| `muxlio-docs` | Markdown | Architecture, MCP, and service documentation |
| `examples` | TypeScript | Example workflows (batch fan-out, audit-coverage with branch/map) |
| `recipes` | TypeScript | Ready-to-use recipes (implement-features) |
| `scripts` | Shell | Build, setup, start, upgrade, and install verification scripts |

## CLI reference

```text
muxlio init [--data-dir dir] [--workspace dir] [--agent name] [--starter]
                                                    Initialize runtime and optionally install agent skills/sample workflow
muxlio build <workflow.ts|plan.json> [--out file]    Compile workflow to canonical Plan IR
muxlio check <workflow.ts|plan.json>                Validate a workflow or plan
muxlio test <workflow.ts|plan.json> --input in.json  Test run with fake executor
muxlio run <workflow.ts|plan.json> [input.json]     Run a plan (fake or real executor; or --server to submit remotely)
muxlio graph <workflow.ts|plan.json|run-id>         Display DAG visualization (ascii, json, mermaid)
muxlio inspect <workflow.ts|plan.json|run-id>       Print plan structure or run status
muxlio events <run-id> [--since seq] [--limit N]    Print run execution events
muxlio artifacts <run-id>                           List artifacts generated by run
muxlio signal <run-id> --name <sig> [--data file]   Deliver external signal to waiting run
muxlio runs [--state state]                       List runs in the local runtime (optionally filtered by state)
muxlio ui [--server URL] [--port port] [--open]     Launch or open the Observability UI
muxlio env [--shell bash|zsh|fish]                  Display or export environment configuration
muxlio doctor [--server URL]                        Run installation and environment health checks
muxlio upgrade [--prefix dir]                       Upgrade Muxlio runtime from source repository
muxlio server start|install                         Start or install the coordinator server
muxlio worker start                                 Connect an outbound remote worker (auto-detects capabilities)
muxlio service start [--config path]                Start supervised coordinator, worker and UI (background)
muxlio service stop [--config path]                 Stop the running service
muxlio service restart [--config path]              Stop then start the service (start if not running)
muxlio submit [--server URL] plan.json input.json    Submit a durable run
muxlio status [--server URL] [run-id]                Query remote run status
muxlio pause|resume|cancel [--server URL] run-id      Control a remote run
muxlio version                                      Print version info
```

Global options: `--json`, `--server <profile|URL>`, `--token <token>`, `--token-file <file>`, `--fake-config <file>`, `--adapters <dir>`, `--workspace <dir>`, `--data-dir <dir>`, `--addr <host:port>`.

## Development

```sh
# Build all TypeScript workspaces
npm run build

# Run tests
(cd muxlio-core && go test ./...)
(cd muxlio-adapters && go test ./...)
npm test
npm --prefix muxlio-ui run smoke

# Build a release prefix and verify it
bash scripts/build-release.sh /tmp/muxlio-install
python3 scripts/verify-install.sh /tmp/muxlio-install
```

Run `npm run build` before Go tests to include the real async compiler regression. Tests use temporary runtimes, ephemeral loopback ports, and fake adapters — they do not install a service or control an existing coordinator.

## Plan IR node kinds

| Kind | Purpose |
|------|---------|
| `execute` | Run a single step through an executor (agent or command) |
| `map` | Fan out over an array with bounded concurrency and per-item keys |
| `branch` | Conditional execution with `then` / `else` subgraphs |
| `loop` | Iterate with a finite `maxIterations` bound |
| `join` | Aggregate results across fan-out branches |
| `wait` | Block until an external signal arrives |
| `child` | Spawn a nested sub-run |
| `project` | Reshape or extract values from bindings |

## Security notes

- The coordinator requires a **Bearer token** on `/v1/*` API routes (including worker registration). `muxlio init` creates a token in `~/.muxlio/config.json`; the CLI attaches it for loopback URLs. Override with `MUXLIO_TOKEN`, `--token`, or `--token-file`. `/healthz` stays unauthenticated for liveness checks. Bind to loopback (`127.0.0.1:7600`) and use SSH port forwarding for remote access; forward the token to clients that call the forwarded port.
- Any client that can reach the service with a valid token can submit plans that invoke advertised capabilities with your user permissions. Treat MCP and HTTP access as equivalent.
- The service runs as your login user, never as root. A workspace is an execution target, not a filesystem sandbox. Native child processes receive a minimal environment with `HOME` set to the operator's real home by default (reusing existing provider logins such as `~/.codex`, `~/.cursor`); set an explicit `agentHome` to use a dedicated credential directory instead. Required isolation (OCI/bwrap/VM) uses a scratch HOME and mounts no provider credentials. For Cursor on headless macOS, see [muxlio-docs/service.md](muxlio-docs/service.md#configure-and-start) for the one-time credential store setup.
- Remote workers may continue an operation already assigned to them after a run is cancelled, so external side effects can occur after cancellation. Recovery does not guarantee exactly-once execution for arbitrary external systems.

## Configuration

The coordinator reads `~/.muxlio/config.json` (override with `--config PATH` or `MUXLIO_CONFIG`). Key fields:

| Field | Description |
|-------|-------------|
| `workspace` | Authorized execution workspace (absolute path; required for service start) |
| `capabilities` | Executor capabilities the local worker advertises (defaults created by `muxlio init` list all six shipped adapters; edit to restrict them) |
| `attemptTimeoutSeconds` | Per-attempt timeout (default 600) |
| `workerEnv` | Shared native executor environment; saved by init and overridden by per-executor `env` |
| `mcp` | Service-owned MCP HTTP listener: `listen`, optional `tokenFile`, `tlsCert`, `tlsKey`, `allowOrigins`; `muxlio init` registers loopback port 7602 |
| `runtimeDir` | Runtime data directory (default `~/.muxlio`) |
| `addr` | Listen address (default `127.0.0.1:7600`) |
| `adapters` | Adapter directory (default beside installed CLI: `libexec/muxlio/adapters`) |
| `ui` | Static UI assets (default `share/muxlio/ui`; served at `/` when using `service start`) |
| `agentHome` | Credential directory for native adapters (defaults to the operator's home) |
| `executors` | Per-executor operator policy: `allow` and `env` |
| `token` | Bearer token for API access (auto-generated by `muxlio init` if missing) |
| `defaultServer` / `servers` / `profiles` | Named coordinator URLs for `--server` (optional) |

Environment overrides: `MUXLIO_HOME`, `MUXLIO_ADDR`, `MUXLIO_ADAPTERS`, `MUXLIO_UI`, `MUXLIO_WORKSPACE`, `MUXLIO_CAPABILITIES` (comma-separated), `MUXLIO_SERVER`, `MUXLIO_TOKEN`, `MUXLIO_CONFIG`. Explicit environment values take precedence over config.

Runtime layout under `~/.muxlio`: `data/coordinator.db` (SQLite), `logs/` (including `service.log`), scratch `work/`, `artifacts/`, `bin/`, `libexec/muxlio/adapters/`, `share/muxlio/ui/`, `service.pid`, `service.lock`, `config.json`, `env` (shell exports).

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
