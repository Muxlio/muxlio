# Start Muxlio

Run Muxlio on the machine that will do the work. That can be your workstation,
another computer, or a server. Keep that machine running while workflows execute.

## Install

With [Homebrew](https://brew.sh) on macOS or Linux:

```sh
brew tap Muxlio/muxlio https://github.com/Muxlio/muxlio
brew install --HEAD Muxlio/muxlio/muxlio
muxlio version
```

Homebrew builds the current main branch and installs the CLI, MCP server,
adapters, and UI. You do not need to clone Muxlio. Install the agent CLIs your
workflow uses and sign in to them on the worker machine. Install any tools your
workflow commands need as well.

## Set up your workspace

From the project or workspace where you want work to happen:

```sh
muxlio init --client kiro --scope project --allow-command
muxlio service start
```

Choose `--client` for the assistant you use: `kiro`, `codex`, `claude`, `cursor`,
or `antigravity`. It installs Muxlio authoring instructions and registers its local stdio MCP server.
`--agent` is a compatible alias for the authoring client, independent of workflow
execution adapters. Antigravity receives instructions only until its MCP CLI
contract can be verified. Use `--scope user` for user registration.
`--allow-command` explicitly permits command steps, including build checks and
Git commands. Omit it if your workflows do not need command execution.

`init` saves your workspace and prepares runtime directories, access credentials,
and MCP settings. `service start` starts the coordinator, local worker, web UI,
and MCP listener together in the background.

## Connect and run

Open **http://localhost:7600** for the UI. Connect your assistant using the
[MCP guide](mcp.md), then ask it to build, validate, and run a workflow.
Run `muxlio integrations doctor --client kiro --scope project` to verify tool
calls through the client (requires login and trust approvals). See the MCP guide
for remote HTTP profiles, repair and removal.
The worker needs the files being processed; the installed service does not need
Muxlio's source repository.

## Manage the service

```sh
muxlio service stop
muxlio service restart
```

Restart after changing workspace or executor configuration. Logs are in
`~/.muxlio/logs/service.log` unless you selected another runtime directory.

If work does not start, run `muxlio doctor` and ask your assistant to inspect the
workers and run state. A disconnected chat does not cancel a running workflow.

For remote workers, configuration fields, security settings, and recovery behavior,
see the [service reference](architecture/service-reference.md).
