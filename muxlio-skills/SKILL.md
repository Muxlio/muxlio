# Muxlio workflow authoring

Use this guide to author and run workflows with this repository's TypeScript SDK and Go CLI. A workflow definition compiles into Plan IR JSON; the Go coordinator interprets that plan. JavaScript runs at compile time only.

## Muxlio-assisted feature work

For feature work, use `recipes/implement-features/workflow.ts`. Edit its input JSON with the requested features and relevant context paths, then check and fake-run before a real run. The recipe works in the current clean git workspace and maps features serially (`concurrency: 1`): implement → review → command verification/fix loop → command commit. Agent tasks currently use Kiro with Codex fallback. A final command step runs `git push` once after every feature finishes.

Before a real run, configure `executors.command.allow: true` in the worker's
`config.json`. For SSH Git remotes, set `executors.command.env.SSH_AUTH_SOCK`
to the socket path obtained on that worker machine, check GitHub authentication
as its OS user, and ensure the workspace branch has an upstream. Restart the
service after changing configuration. Agent steps should not commit or push;
the recipe owns those operations. If only push fails, fix its prerequisites and
use **Retry run** in the UI to preserve completed feature work and retry push.

Typical repository commands:

```sh
npm run build
go -C muxlio-core build ./...
muxlio check recipes/implement-features/workflow.ts
muxlio run recipes/implement-features/workflow.ts recipes/implement-features/input.json --json --fake-config recipes/implement-features/fake.json
```

`npm run build` builds the SDK, compiler, and testing workspaces. Run `npm run setup` once to build and install the full release prefix under `~/.muxlio` and copy the CLI to `.bin/muxlio`. Use `npm run release` to refresh binaries in an existing runtime without re-running `muxlio init`. `check` compiles the TypeScript workflow and validates the resulting Plan IR. With no `--adapters`, `run` uses the fake executor. Fake outputs are keyed by node ID or executor name:

```json
{
  "byNode": {
    "implement": { "outputs": { "notes": { "digest": "sha256:fake", "mime": "text/markdown" } } },
    "review": { "outputs": { "review": { "digest": "sha256:fake", "mime": "text/markdown" } } }
  }
}
```

Supply it with `--fake-config path/to/fake.json`. With no fake config, the fake executor generates placeholder artifact references for declared output ports. `--json` returns run state, output, and events as JSON. A failed run returns a nonzero status; with JSON enabled it also emits the failed state and error. Inspect validation errors and run events/output, correct the workflow or inputs, then recompile, check, and fake-run. For real execution, build the adapters and pass `--adapters adapters --data-dir ~/.muxlio`; the adapter directory must contain executable scripts named by replacing `/` in each executor with `-` (for example, `agent/codex` maps to `adapters/agent-codex`).

Verification outputs are evidence to act on, not just summaries. Read `passed` alongside `notes.md`, `build-output.txt`, run state, and events. If verification fails, use the reported failure and relevant files to repair the implementation, rerun the relevant build/tests, then rerun the workflow checks. A successful fake run alone does not verify real adapter behavior or code correctness.

## CLI reference

The current CLI commands are:

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

The adapter executors included here are `agent/claude`, `agent/codex`, `agent/cursor`, `agent/gemini-cli`, `agent/kiro`, and `command`. The generic command adapter accepts `params.program`, `params.args`, and `params.cwd`; for Go tests, use `program: 'go'`, `args: ['test', './...', '-timeout', '120s']`, and `cwd: 'muxlio-core'`. Command tasks automatically infer `passed`, `exitCode`, `signal`, `stdout`, `stderr`, and `durationMs`; use `onNonZero: 'return'` to handle failures in the workflow. Other executor strings can be used by custom adapters or fake configuration, but do not name a bundled real adapter.

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
