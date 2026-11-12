/** Shared feedback primitives receive text, never pre-escaped view markup. */
function renderNotice(
  message: string,
  options: { error?: boolean } = {},
): string {
  return renderComponent("notice", {
    message,
    tone: options.error ? " error" : "",
    role: options.error ? "alert" : "status",
  });
}
function renderPlaceholder(
  message: string,
  className = "inspector-placeholder",
): string {
  return renderComponent("placeholder", { message, className });
}
function renderStatusBadge(tone: string, label: string): string {
  return renderComponent("status-badge", { tone, label });
}
function renderAttemptStatus(
  retryNumber: string | number | null | undefined,
  tone: string,
  label: string,
): string {
  return renderComponent("attempt-status", {
    retryNumber,
    status: componentBody(renderStatusBadge(tone, label)),
  });
}
