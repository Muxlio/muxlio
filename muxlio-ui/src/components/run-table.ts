interface RunTableRecord {
  id: string;
  workflowName?: string;
  state: string;
  createdAt: string;
  updatedAt: string;
  totalExecutionTimeMs?: number | null;
}
function renderRunTable(
  runs: readonly RunTableRecord[],
  emptyTitle = "No matching runs",
  emptyDescription = "Adjust the search or state filter.",
): string {
  const rows = runs.map((run) =>
    renderComponent("run-row", {
      href: `/runs/${encodeURIComponent(run.id)}`,
      id: run.id,
      workflow: run.workflowName || "Unnamed workflow",
      status: componentBody(renderStatusBadge(run.state, run.state)),
      created: date(run.createdAt),
      updated: date(run.updatedAt),
      elapsed: duration(
        run.createdAt,
        ["succeeded", "failed", "cancelled"].includes(run.state)
          ? run.updatedAt
          : new Date().toISOString(),
      ),
      execution:
        run.totalExecutionTimeMs == null
          ? "Unavailable"
          : duration(new Date(0), new Date(run.totalExecutionTimeMs)),
    }),
  );
  return renderDataTable(
    [
      "Workflow / run",
      "State",
      "Created",
      "Updated",
      "Elapsed time",
      "Total execution time",
    ],
    rows.map(componentBody),
    { emptyTitle, emptyDescription },
  );
}
