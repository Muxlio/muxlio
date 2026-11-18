# Follow your workflows

Open **http://localhost:7600** after starting the Muxlio service. If Muxlio runs
on another machine, use SSH port forwarding to its loopback address, or the
HTTPS address of its authenticated browser gateway.

## Find a run

Use **Run history** to find your workflow and filter by state. Open it to see
its current status, inputs, outputs, and any error.

## See what is happening

The run view brings the execution graph, attempts, events, logs, and artifacts
together. Use **Live** to follow output while steps run. Open artifacts to inspect
the files and reports produced by the workflow.

A successful step has saved outputs. A failed or paused run needs attention:
read its error and latest logs before deciding what to do next.

## Launch a workflow

Open **Workflows** to see registered Plan IR versions. Use **Run workflow** or a
row **Run** action to open the launch dialog. Pick the immutable version and an
authorized workspace, then edit the JSON input, which starts at `{}`. A single
workspace is preselected and visible, including the legacy `default` workspace.
If the catalog lists several workspaces, select the one you want to use.

Select **Preflight** to validate the plan, workspace binding, and input schema.
The dialog shows the coordinator's diagnostics and unverified checks. A feasible
result enables **Start run**. If you change the version, workspace, or input,
run preflight again. When the coordinator returns a run ID, the dashboard opens
that run. If a submission response is lost, retry the same version, workspace,
and JSON in the same dashboard tab to reuse its submission key.

The dialog requires workspace catalog access. If it reports that your
credential cannot read the catalog, ask the operator for `workspaces:read` access.

## Take the next action

Pause or cancel a run when needed. Resume a paused run when it is ready to
continue. Retry a failed run after fixing its cause; completed steps are preserved.
If an operation's external outcome is uncertain, inspect it under **Operations**
and resolve it with evidence before retrying.

**Workers** shows which machines and executors are available. **Automation**
manages schedules and webhooks. **Retention** controls which execution data stays
protected from cleanup.

The UI defaults to local operator access. Remote browser hosting requires HTTPS
and an authenticated gateway that supplies each user's scoped credential. Read
the [execution security guide](architecture/execution-security.md#remote-web-and-mcp-access)
before exposing it remotely. API fields and operational details are in the
[operations reference](architecture/operations-reference.md).

## Browser links and analytics

UI pages use normal paths such as `/runs`, `/runs/<run-id>`, and `/analytics`.
These links support direct loading, refresh, and browser back/forward navigation.
Existing hash bookmarks are replaced with their path equivalent when opened.
Analytics filters and chart selections are preserved in the URL query string.

Analytics shows reported usage, trends, executor activity, and timing/retry
breakdowns. Select a chart bar or **View attempts** to open a titled panel of
matching records. **Open run** opens the run workbench; the step link opens its
transcript. Closing the panel or pressing Escape returns to the chart without
reloading the dataset. Advanced state, executor, and timezone filters are under
**More filters**. Reported subtotals retain their coverage and do not imply
complete consumption when some measurements are missing.
