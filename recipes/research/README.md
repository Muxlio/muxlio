# Research

Context: [What Muxlio is](../../muxlio-docs/index.md#what-is-muxlio), [Setup defaults](../../muxlio-docs/service.md#setup-defaults).

This recipe researches a topic one question at a time, carrying a complete
`findings.md` artifact into the next iteration. Each pass reassesses coverage,
records evidence and gaps, and returns structured progress. When all questions
are answered, synthesis produces:

- `report.md`: the research report with citations and limitations.
- `metrics.json`: coverage, confidence, and evidence counts by question.
- `report.canvas.json`: a self-contained research document with the full report,
  source links, practical implications, and limitations. Coverage cards, a chart,
  and a question table follow the narrative and bind to the sibling metrics
  artifact. The Markdown report is also available as a separate file.

Copy [input.example.json](input.example.json) to `input.json`, then edit it with
your topic and 1–20 questions. The local `input.json` is git-ignored. `audience` is
optional (general technical reader); `depth` is optional (`standard`), with
`brief` and `thorough` also supported. Each pass focuses on one question; the
loop allows 40 passes for at most 20 questions, leaving room to revisit gaps.
The input schema caps question count at the smaller of 20 and the configured
pass limit. The coordinator enforces that schema before accepting a run.
Change shared bounds in `limits.ts`; progress validation and run budgets use
the same values, so expanding the loop cannot leave a ten-pass output ceiling
or an undersized retry budget behind.

With project dependencies installed, a current Muxlio CLI supporting Canvas,
and a running coordinator and worker:

```sh
cp recipes/research/input.example.json recipes/research/input.json
muxlio check recipes/research/workflow.ts
muxlio run recipes/research/workflow.ts recipes/research/input.json \
  --server http://127.0.0.1:7600 --json
```

The tasks use `agent/any` to select a compatible available agent at runtime.
Configure the worker with an available executor and authorized research tools
and sources. Tool access comes from the executor environment; the recipe does
not provision browsing or credentials. Synthesis also needs `muxlio canvas
catalog` and `muxlio canvas validate` on PATH. Open the resulting Canvas artifact
in the Muxlio UI to inspect coverage and read the report.

Execution is serial, with up to three attempts per task, a three-minute queue
timeout per attempt, and a separate 15-minute execution timeout. Configure the
queue window with `queueTimeoutSeconds` in `tasks.ts`; a positive value overrides
the default. If coverage remains incomplete after 40 research passes, the run pauses
before synthesis. Inspect the latest findings and progress through the run's
activation outputs. The iteration cap is a total bound: resuming alone does not
provide more passes. Narrow the questions or start another run after addressing
access limitations. Missing evidence must remain an open gap.

The workflow uses the SDK's explicit Plan builder to carry the latest artifact
and iteration count in loop state. Progress is a schema-validated JSON result
port (written to the executor's scalar output file); findings and final reports
are file artifacts. JSON file artifacts are references and cannot serve directly
as the loop's boolean condition.

Validate without making research calls:

```sh
npm run build --workspace muxlio-recipes
npm test --workspace muxlio-recipes
```

To also exercise completion and exhaustion with the real interpreter and fake
executor results:

```sh
go build -o /private/tmp/muxlio-research-test ./cmd/muxlio # from muxlio-core
MUXLIO_TEST_CLI=/private/tmp/muxlio-research-test npm test --workspace muxlio-recipes
```
