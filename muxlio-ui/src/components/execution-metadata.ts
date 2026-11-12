interface ExecutionMetadata {
  runId: string;
  nodeId: string;
  state: string;
  stateTone: string;
  executor: string;
  workflowName?: string;
  className?: "transcript-meta-kv";
  attempt?: { retryNumber: number | string; tone: string; label: string };
  transcriptActivationId?: string;
}
function renderExecutionMetadata(value: ExecutionMetadata): string {
  const transcriptLink = value.transcriptActivationId
    ? renderComponent("execution-metadata-link", {
        href: `/transcripts/${encodeURIComponent(value.runId)}/${encodeURIComponent(value.transcriptActivationId)}`,
      })
    : "";
  return renderComponent("execution-metadata", {
    className: value.className ?? "",
    runHref: `/runs/${encodeURIComponent(value.runId)}`,
    runId: value.runId,
    nodeId: value.nodeId,
    executor: value.executor,
    workflowName: value.workflowName ? ` · ${value.workflowName}` : "",
    status: componentBody(renderStatusBadge(value.stateTone, value.state)),
    attempt: value.attempt
      ? componentBody(
          renderAttemptStatus(
            value.attempt.retryNumber,
            value.attempt.tone,
            value.attempt.label,
          ),
        )
      : "—",
    transcriptLink: componentBody(transcriptLink),
  });
}
