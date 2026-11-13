# Use Muxlio from your assistant

MCP connects your assistant to Muxlio. You describe the work; the assistant
builds a workflow, validates it, starts a run, and helps you inspect the results.

## Connect once

First follow [Start Muxlio](service.md). The service starts its MCP listener at
**http://localhost:7602/mcp** by default.

Register your authoring client from the workspace:

```sh
muxlio integrations add --client kiro --scope project
muxlio integrations doctor --client kiro --scope project
```

Choose `claude` (Claude Code), `codex` (CLI and IDE), `cursor`, or `kiro`.
Registration is noninteractive and uses the installed absolute `muxlio-mcp`
executable over stdio by default. Use `--binary /absolute/path/to/muxlio-mcp`
if it is not beside `muxlio` or on PATH. The bridge reads coordinator credentials
from the protected Muxlio config; tokens are never copied into client settings.
Use `--data-dir` for a nondefault Muxlio runtime and `--server NAME` to select a
saved coordinator profile.

Every integrations command requires `--scope project` or `--scope user`.
`--workspace /absolute/project` selects the project (default: current directory).
Only the chosen client and scope are modified:

| Client | Project config | User config |
| --- | --- | --- |
| Claude Code | `.mcp.json` | `~/.claude.json` |
| Codex CLI / IDE | `.codex/config.toml` | `$CODEX_HOME/config.toml` or `~/.codex/config.toml` |
| Cursor | `.cursor/mcp.json` | `~/.cursor/mcp.json` |
| Kiro | `.kiro/settings/mcp.json` | `~/.kiro/settings/mcp.json` |

Muxlio merges the named `muxlio` server, preserving unrelated settings. Repeating
an identical add leaves the file unchanged. A conflicting custom or edited entry
is rejected with instructions; it is never overwritten. Config symlinks are
rejected. JSON and TOML may be reformatted on a change (including TOML comments).
A hash receipt beside the config tracks ownership without storing credentials.
Keep that receipt to allow safe repair and removal.

```sh
muxlio integrations list --scope project
muxlio integrations doctor --client kiro --scope project --config-only
muxlio integrations repair --client kiro --scope project --binary /new/path/muxlio-mcp
muxlio integrations remove --client kiro --scope project
```

`repair` can recreate a missing registration or update an unchanged managed
entry. Custom or edited entries require manual review first. `remove` deletes
only an unchanged managed entry. Restart the client after changes.

Doctor starts the actual client's noninteractive CLI and requests `server_health`
and `get_workers`, requiring structured successful tool results. It can consume
model quota and requires client login, project trust and MCP approvals. It does
not bypass workspace trust. If the CLI is absent, unsupported by its installed
version, or cannot confirm the calls, doctor fails with recovery instructions;
open the IDE and ask it to invoke both tools. `--config-only` checks registration
and executable/endpoint validity without claiming a live connection. A user
registration cannot be verified in a workspace with a project `muxlio` override.
Claude's private local-scope `muxlio` entry also takes precedence over both scopes;
doctor asks you to review that entry before verifying a different registration.
Client output and configuration contents are withheld from diagnostics.

### Remote HTTP

Use the service's Streamable HTTP endpoint and an environment variable containing
its bearer token. Export the variable securely in the environment that launches
the client; Muxlio stores only its name, using each client's interpolation syntax.
Kiro IDE may require approval of that environment variable. Remote endpoints
require HTTPS; HTTP is accepted only for loopback addresses.

```sh
muxlio integrations add --client codex --scope user --transport http \
  --url https://muxlio.example.com/mcp --bearer-env MUXLIO_MCP_TOKEN
```

For saved remote endpoints, add an `mcpProfiles` entry to your protected Muxlio
`config.json`, then use `--transport http --profile office`:

```json
{"mcpProfiles":{"office":{"url":"https://muxlio.example.com/mcp","bearerEnv":"MUXLIO_MCP_TOKEN"}}}
```

`muxlio init --client NAME --scope project` combines setup, authoring instructions
and local stdio registration. `--agent NAME` remains a compatible authoring-client
alias; it does not select an execution adapter. Init defaults to project scope.
Antigravity currently receives instructions only: no installed CLI contract was
available to verify MCP registration, so `integrations` rejects it explicitly.

Client config contracts: [Claude Code](https://code.claude.com/docs/en/mcp),
[Codex](https://developers.openai.com/codex/mcp/),
[Cursor](https://cursor.com/docs/context/mcp), and
[Kiro](https://kiro.dev/docs/mcp/configuration/).

## Ask for a workflow

Describe the inputs, the steps to run, the checks they must pass, and the output
you need. For example:

> Build a Muxlio workflow for implementing a list of features in this project.
> For each feature, implement it, review the change, and run this project's build
> and tests. If checks fail, fix the issue and retry up to a limit. Commit locally
> only after checks pass. Show me the workflow before starting it.

The assistant authors the workflow. It can construct Plan IR directly, validate
it with `check_plan`, and start it with `start_run`. This does not require downloading
Muxlio's repository. Developers can also author workflow source and use `build`
and `test` when the required authoring tools are installed.

## Follow the work

Ask your assistant to check the run, read logs and reports, or explain a failure.
You can also use the [web UI](ui-operations.md). Closing the client leaves the run
on the coordinator. Your execution machine still needs to stay running.

For client configuration examples, tool arguments, authentication, and protocol
behavior, see the [MCP reference](architecture/mcp-reference.md).
