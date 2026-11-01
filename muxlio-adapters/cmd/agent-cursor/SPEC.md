# agent-cursor Adapter Specification

Implement a Go adapter binary at `muxlio-adapters/cmd/agent-cursor` that follows the muxlio adapter protocol and the existing `agent-codex` / `agent-kiro` patterns. Use only the Go standard library and the existing `github.com/muxlio/muxlio-adapters/adapter` package.

## Files and package layout

Add:

```text
muxlio-adapters/
└── cmd/
    └── agent-cursor/
        └── main.go
```

Keep the module declaration and Go version from `muxlio-adapters/go.mod` (`github.com/muxlio/muxlio-adapters`, Go 1.22). No new dependencies or shared adapter changes are needed. `main.go` is package `main`, imports the common adapter package, and contains the entry point plus prompt-building, executable-resolution, and repository-root helpers, following `cmd/agent-codex/main.go` in structure.

## Adapter protocol and execution

Read one `adapter.Request` from stdin with `adapter.ReadRequest()`. On request, configuration, process, or stream errors, report through `adapter.Fail(...)` so the muxlio caller receives an `adapter.Response` JSON object on stdout. On normal completion, write exactly one response using `adapter.WriteResponse(...)`; reserve stdout for this protocol response and do not forward Cursor output there.

Resolve the repository root by walking upward from the executable directory until `.git` is found, with the same current-working-directory fallback as the existing adapters. Run Cursor with `cmd.Dir` set to that repository root: the Cursor agent requires execution inside a git repository. Do not use `req.WorkDir` as the process cwd; it is the scratch directory for declared outputs.

Invoke the resolved executable with arguments in this order (prompt is the final positional argument):

```text
agent --print --yolo --trust --output-format stream-json --model <model> <prompt>
```

The default model is `auto`. Apply the `params.model` handling below. `--print` enables non-interactive scripted output, `--yolo` allows tool execution, `--trust` trusts the current workspace, and `--output-format stream-json` requests machine-readable streaming events. Do not pass credentials as arguments: Cursor uses its existing login session or `CURSOR_API_KEY` from the inherited environment. Use `exec.Command` (not a shell) so arguments and prompt remain intact.

Capture stdout through `cmd.StdoutPipe()` and parse it as newline-delimited JSON. Send stderr directly to `os.Stderr` so CLI diagnostics remain visible without contaminating the adapter's stdout protocol. Start the process, consume all stdout lines, then call `cmd.Wait()`. A non-zero process exit or start/pipe/read error must become an adapter error; include useful stderr only if stderr is captured instead of passed through. Increase the scanner buffer to at least 1 MiB, as event lines may be large.

## Stream JSON parsing

Each line is an independent JSON event. Ignore blank lines, non-JSON lines, malformed events, and event types that do not carry user-facing text or terminal status. Decode `type` and inspect payload fields defensively because tool and metadata event shapes may vary.

- **Text:** collect assistant text from events with `type: "assistant"`. Cursor stream events may put the text in `message.content` (often an array of `{ "type": "text", "text": "..." }` blocks); support a direct string content form as well. If the CLI emits `type: "text"` with a `data.content` string, collect that incrementally too. Do not treat `tool_call`, `system`, or `user` events as assistant text.
- **Completion:** `type: "result"` is the terminal result event. Record terminal failure when it indicates an error (for example `is_error: true`, an error subtype, or an explicit error field); preserve any final result text if present. Completion still requires the process to exit successfully and the stream to be fully consumed. Do not use an assistant/text event as evidence that execution is finished.
- **Other events:** ignore `system` initialization and tool-call lifecycle events unless they contain explicit error information that the implementation chooses to report. Unknown future event types should not break parsing.

The CLI help confirms `--output-format stream-json`, but does not enumerate the event schema; keep parsing tolerant and use the known `assistant` text / `result` terminal event contract above. Join collected text chunks in arrival order. Expose the complete assistant text as a built-in `_text` output with MIME `text/plain` and `data` containing the joined text, matching the useful behavior of `agent-kiro`.

## Prompt construction

Build the prompt from `req.Task`, then expand the special `feature` input the same way as `agent-codex`:

- If `req.Inputs["feature"]` is a map, append a `Feature title: ...` line for non-empty string `title`; append a `Files to read first:` section with one indented bullet per string in `files`; and append `Spec:` plus the non-empty string `spec`.
- If `feature` is a string, append it under `Feature input:`.
- Ignore unsupported feature shapes and non-string fields.

When `req.Outputs` is non-empty, append the exact absolute output instructions used by `agent-codex`: for each output declaration, use `decl.Path`, or the output port name when the path is empty, and tell the agent to write `${req.WorkDir}/${name}` with the declared MIME. Append `Node ID: ${req.NodeID}` at the end of the prompt. Do not add Kiro's expansion of arbitrary non-feature inputs; the requested behavior is the Codex feature expansion.

## Model parameter

Parse `req.Params` as JSON with a `model` string, as in `agent-kiro`. Use `auto` when params are absent, invalid, or `model` is empty. Otherwise pass the supplied value with `--model <name>`.

## Executable resolution

Resolve the Cursor executable in this order:

1. If `CURSOR_AGENT` is non-empty, use its value as the executable path/name.
2. Otherwise call `exec.LookPath("agent")` and use the resolved path.

If lookup fails, return a descriptive adapter error. Preserve the inherited environment so `CURSOR_API_KEY` and the user's Cursor login session can authenticate the CLI.

## Output file collection and response

After successful process completion, iterate `req.Outputs`. For each port, compute the output name from `decl.Path` or fall back to the port name, then read `filepath.Join(req.WorkDir, name)`. For each readable file return:

```json
{"mime":"<decl.Mime>","path":"<full path>","data":"<UTF-8 file contents>"}
```

Record missing/unreadable declared output ports and set the response `Error` to a message listing those ports, following `agent-codex` and `agent-kiro`. Include all files that were successfully read even if another declared file is missing. Always include the `_text` output described above after a successful run. Write the final `adapter.Response` with the collected output map and error string.
