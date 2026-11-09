# Follow your workflows

Open **http://localhost:7600** after starting the Muxlio service. If Muxlio runs
on another machine, use the address configured for that service.

## Find a run

Use **Run history** to find your workflow and filter by state. Open it to see
its current status, inputs, outputs, and any error.

## See what is happening

The run view brings the execution graph, attempts, events, logs, and artifacts
together. Use **Live** to follow output while steps run. Open artifacts to inspect
the files and reports produced by the workflow.

A successful step has saved outputs. A failed or paused run needs attention:
read its error and latest logs before deciding what to do next.

## Take the next action

Pause or cancel a run when needed. Resume a paused run when it is ready to
continue. Retry a failed run after fixing its cause; completed steps are preserved.
If an operation's external outcome is uncertain, inspect it under **Operations**
and resolve it with evidence before retrying.

**Workers** shows which machines and executors are available. **Automation**
manages schedules and webhooks. **Retention** controls which execution data stays
protected from cleanup.

The UI is intended for a trusted network. Read the
[execution security guide](architecture/execution-security.md) before exposing it
beyond that environment. API fields and operational details are in the
[operations reference](architecture/operations-reference.md).
