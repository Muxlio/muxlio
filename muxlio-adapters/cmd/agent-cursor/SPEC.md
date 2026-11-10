# Cursor adapter behavior

The `agent/cursor` executor is implemented in `main.go`, with its shared
contract descriptor in `adapter.json`. Build with the Go version declared in
`muxlio-adapters/go.mod`.

## Invocation and workspace

The adapter reads one manifest-validated request from stdin and writes one JSON
response to stdout. It resolves `CURSOR_AGENT`, then `agent` on PATH. It uses the
shared `adapter.WorkingDir` selection for the attempt workspace; a Git repository
is not required. Declared outputs use the separate request scratch directory.

The current invocation is:

```text
agent --print --yolo --trust --add-dir <scratch> --output-format stream-json --model <model> <prompt>
```

The default model is `composer-2.5`; `params.model` overrides it. The adapter
builds the prompt with `adapter.ComposePrompt`, including the task, inputs,
output instructions, and node identity. It sets `AGENT_CLI_CREDENTIAL_STORE=file`
for both readiness checks and execution. Configure credentials through the
worker's executor environment or existing on-disk login state.

## Readiness and execution

`--muxlio-probe` checks the resolved runner and its status through the shared
probe helpers. Pinned runner checks use that same executable before execution.
Provider protocol handling remains entirely inside this adapter.

The stream parser collects assistant text and incremental `text` events, accepts
`result`, `completion`, or `done` as completion markers, and ignores unknown or
malformed event lines. Scanner lines are bounded to 4 MiB. A successful process
exit and a completion marker are both required; reported result errors fail the
invocation. Stderr is captured in a bounded diagnostic buffer.

## Outputs and usage

After completion, `adapter.CollectOutputs` reads declared output ports. Scalar
ports return parsed JSON; file ports return artifact-shaped values for the
worker's generic uploader. Missing or invalid outputs retain per-port errors.
`_text` contains collected assistant text as `text/plain`.

Cursor reports uncached `inputTokens` plus `cacheReadTokens` and
`cacheWriteTokens` on the terminal result; the adapter folds cache read/write into
`input` while preserving `cached` as the read subset. Incremental `usage` events,
when present, use the same mapping and are summed across model calls. Complete
terminal usage replaces prior aggregates; partial terminal usage overlays only the
dimensions it reports. Unavailable counts remain unavailable, distinct from
reported zero.
The response contains collected outputs, output errors, diagnostic logs, and
available usage.
