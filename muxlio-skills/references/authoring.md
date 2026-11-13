# Authoring Muxlio workflows

Muxlio executes Plan IR JSON. You can construct that plan directly with MCP or
compile a TypeScript definition using the SDK. The coordinator interprets the
plan; JavaScript runs at compile time only.

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
