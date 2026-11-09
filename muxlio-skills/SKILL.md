# Muxlio workflow authoring

Use this guide to author and run workflows with this repository's TypeScript SDK and Go CLI. A workflow definition compiles into Plan IR JSON; the Go coordinator interprets that plan. JavaScript runs at compile time only.

## Build the user's workflow

Start from the user's task, inputs, required outputs, and success criteria.
Inspect available workers and executor contracts before choosing agent or tool
steps. Make dependencies explicit, bound maps and loops, and choose retry and
effect policies appropriate to each operation. Show the user the workflow when
requested, validate it before starting, and inspect the run's outputs and errors.

With MCP, construct Plan IR and call `check_plan`, then `start_run`. Use
`get_run`, `get_activations`, `get_attempts`, `get_logs`, and `read_artifact` to
follow execution. The runtime does not contain a feature-specific submission
command. The installed service and MCP tools do not require a Muxlio source
checkout. The worker needs the target files, authorized executors, and tools.

For setup, `muxlio init --agent <assistant> --allow-command` prepares the workspace,
credentials, assistant instructions, and MCP settings. The flag explicitly permits
command execution. `muxlio service start` starts the coordinator, worker, UI, and
configured MCP listener. Client connection settings are separate from the listener.

Developers can author TypeScript workflows using the SDK and compile them with
`muxlio build`. Compilation runs source code locally and requires Node, tsx, and
its dependencies. An existing JSON plan skips that compilation step. Use
`muxlio test` with fake outputs to check dataflow before real execution.

The repository's `recipes/implement-features` workflow is an example used to
develop Muxlio. It implements, reviews, checks, repairs, and commits features
serially. Its commands target Muxlio's build and tests; adapt those commands and
executor choices when building a workflow for another project. It is not a
special runtime API. Shared-write tasks with concurrency keys must run through
coordinator admission rather than direct `--adapters` execution.

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

## Testing with fake executors

`@muxlio/testing` exports `testWorkflow` and `fakeExecutor`. `workflow(...)` returns a compiled workflow object (with a `.plan` property); pass that object to the harness. The Go CLI must be built and either available as `muxlio` on `PATH` or supplied through `cliPath`. Example:

```typescript
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

Use `dryRun: true` to validate and return the plan without calling the CLI. The harness creates temporary plan/input/fake-config files and invokes `muxlio run --json` otherwise.

## Authoring rules and common validation errors

- `for...of` and array methods in a `build` callback execute at compile time; use `each(...)` (or `g.map` in the low-level API) for runtime fan-out. Map concurrency is bounded by its `concurrency` setting. `repeatUntil(...)` compiles to a durable loop with `maxAttempts` and `onExhausted: 'pause'` (resumable). Arbitrary runtime `if`/`while`/`await` inside `build` are not supported — they run at compile time only.
- `step` returns symbolic proxies; `gathered.findings` or `item.id` compile to typed references in the plan, not runtime values. `{ after: stepRef }` adds a control dependency; `{ requiresPassed: loopResult }` gates a step on a successful loop exit.
- Map keys should be stable and unique for each input item.
- Tasks (`task({ executor, ... })`) declare executor, prompt, output ports, and retry; they schedule no work until used in a `step`. Use `artifact.markdown(path)` (or `.text`, `.json`, `.of(mime, path)`) for file outputs and Zod types for structured JSON outputs.
- Execute nodes require an executor and declared outputs. Output declarations define ports and may include MIME type and file path.
- Loop nodes need a finite `maxIterations` and valid loop state.
- Node IDs must be unique within a subgraph. Bindings must reference valid nodes and ports.

Common plan validation codes include:

| Code | Meaning | Repair |
|---|---|---|
| `MUXLIO_DUPLICATE_NODE_ID` | Two nodes share an ID in a subgraph | Give each node a unique ID. |
| `MUXLIO_UNBOUNDED_LOOP` | A loop has no positive iteration bound | Set `maxIterations` to a finite positive bound. |
| `MUXLIO_BAD_SUBGRAPH_REF` | A branch, loop, or map points to a missing subgraph | Correct the referenced subgraph name. |
| `MUXLIO_MISSING_EXECUTOR` | An execute node has no executor name | Set its `executor` capability string. |
| `MUXLIO_BAD_REF` | A binding references an unknown node | Correct the node reference or create the missing node. |

`muxlio check --json` reports structured validation errors; the text form includes a suggested `fix` when available. Use those diagnostics, plus `inspect` and failed run events, to locate the source, repair the TypeScript definition or input, and compile/check/run again.
