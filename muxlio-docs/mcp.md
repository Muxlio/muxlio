# Use Muxlio from your assistant

MCP connects your assistant to Muxlio. You describe the work; the assistant
builds a workflow, validates it, starts a run, and helps you inspect the results.

## Connect once

First follow [Start Muxlio](service.md). The service starts its MCP listener at
**http://localhost:7602/mcp** by default.

In a client that supports Streamable HTTP, add that URL and the service's bearer
token from your protected `~/.muxlio/config.json`. Keep the token in your client's
local settings. For another machine, use its reachable address and an appropriately
secured connection. The exact client settings depend on the app you use.

Ask your assistant to check Muxlio's health and available workers. This confirms
both the connection and which executors can run your steps.

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
