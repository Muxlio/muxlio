<p align="center">
  <img src="logo.png" alt="Muxlio" width="88">
</p>

<h1 align="center">Muxlio</h1>

<p align="center">Build a workflow. Let your agents run it.</p>

<p align="center">
  <a href="https://muxlio.github.io/muxlio/">Documentation</a> ·
  <a href="muxlio-docs/service.md">Get started</a> ·
  <a href="recipes/implement-features/workflow.ts">Implement-features recipe</a> ·
  <a href="LICENSE">Apache-2.0</a>
</p>

Muxlio runs multi-step workflows across AI agents and command tools. You or your
assistant define the steps, their inputs, and the checks they must pass. Muxlio
executes the workflow, saves progress, and gives you a place to follow the work.

Use it for work that needs several steps, checks, or hand-offs. Your client can
disconnect while the coordinator continues running. You can return to the run's
status, logs, outputs, and any failure that needs attention.

## One example: implement a list of features

I use Muxlio to develop Muxlio itself. The
[implement-features recipe](recipes/implement-features/workflow.ts) works through
features one at a time:

```text
Implement → Review → Run checks → Commit locally → Next feature
                         │
                         └─ if checks fail: Fix → Run checks again
```

The workflow runs the repository's build and tests before committing. After ten
unsuccessful verification attempts it pauses for attention. You can inspect the
change, review notes, test output, and execution history before deciding to push.
The recipe creates local commits; it does not push them.

I keep Muxlio running on a Mac mini, queue work through my assistant, and review
progress later. You choose the machine: your workstation, another computer, or
a server. The execution host needs to stay running; your chat session does not.

This is one workflow, not a special product command. Adapt the
[recipe's tasks and checks](recipes/implement-features/tasks.ts) to your own project,
or build a workflow for research, data processing, documents, or other work.

## Install

With [Homebrew](https://brew.sh) available on macOS or Linux:

```sh
brew tap Muxlio/muxlio https://github.com/Muxlio/muxlio
brew install --HEAD Muxlio/muxlio/muxlio
muxlio version
```

This builds the current `main` branch and installs the CLI, MCP server, adapters,
and web UI. Homebrew handles the build dependencies; you do not clone the repo.
Install and sign in to the agent CLIs your workflows will use separately.

## Start with your own workflow

With Muxlio installed, initialize the workspace where work should happen:

```sh
muxlio init --agent kiro --allow-command
muxlio service start
```

Choose `--agent` for your assistant. `--allow-command` explicitly permits command
steps such as checks and Git operations. `init` prepares the workspace settings,
credentials, agent instructions, and MCP settings. The service starts the
coordinator, local worker, UI, and configured MCP listener together.

[Connect your assistant through MCP](muxlio-docs/mcp.md), then ask it to build a
workflow for your task. Describe the inputs, the steps to run, and the output you need.
Have it validate the workflow and show you the plan before starting the run.
Developers can also author workflows directly with the TypeScript SDK.

The installed service does not need Muxlio's source checkout. Workers need the
files, authenticated agent CLIs, and tools required by your workflow.
[Source-build instructions](muxlio-docs/architecture/developer-reference.md#build-from-source)
are available if you prefer to build the runtime yourself.

## Follow the work

Open **http://localhost:7600** or ask your assistant for run status. Inspect the
execution graph, attempts, live logs, and generated artifacts. Pause, resume, or
retry when appropriate. Uncertain external effects require reconciliation before
work can safely repeat.

```sh
muxlio service stop
muxlio service restart
```

## Explore further

| Start here | Go deeper |
| --- | --- |
| [Service Guide](muxlio-docs/service.md) — setup and service controls | [Service reference](muxlio-docs/architecture/service-reference.md) |
| [MCP Integration](muxlio-docs/mcp.md) — build and run from your assistant | [MCP tools and protocol](muxlio-docs/architecture/mcp-reference.md) |
| [Observability UI](muxlio-docs/ui-operations.md) — follow progress and results | [Operations API](muxlio-docs/architecture/operations-reference.md) |
| [Benchmarks](muxlio-docs/benchmarks.md) — what the suite checks | [Benchmark methodology](muxlio-docs/architecture/benchmark-methodology.md) |

Explore the [architecture](muxlio-docs/architecture/overview.md),
[execution and security contracts](muxlio-docs/architecture/execution-security.md),
and [CLI, SDK, and configuration reference](muxlio-docs/architecture/developer-reference.md).
The [example recipe](recipes/implement-features/workflow.ts) and
[authoring guide](muxlio-skills/SKILL.md) show how workflows are put together.

## License

[Apache License 2.0](LICENSE).
