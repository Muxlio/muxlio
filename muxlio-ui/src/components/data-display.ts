function renderEmptyState(title: string, description = ""): string {
  return renderComponent("empty-state", { title, description });
}
function renderDataTable(
  headers: readonly string[],
  rows: readonly ComponentHTML[],
  options: {
    operational?: boolean;
    emptyTitle?: string;
    emptyDescription?: string;
  } = {},
): string {
  if (!rows.length)
    return renderEmptyState(
      options.emptyTitle ?? "No records",
      options.emptyDescription,
    );
  return renderComponent("data-table", {
    className: options.operational ? "tablebox ops-table" : "tablebox",
    headers: componentBody(
      headers
        .map((label) => renderComponent("table-header", { label }))
        .join(""),
    ),
    rows: componentBody(rows.map((row) => row.value).join("")),
  });
}
function renderMetricCard(
  label: string,
  value: string | number | null | undefined,
  note = "",
): string {
  return renderComponent("metric-card", {
    label,
    value,
    note: componentBody(note ? renderSmallContent(note) : ""),
  });
}

interface SummaryCard {
  label: string;
  value: string | number;
  tone?: "green" | "red" | "amber";
}
function renderSummaryCards(cards: readonly SummaryCard[]): string {
  return cards
    .map((card) =>
      renderComponent("summary-card", {
        label: card.label,
        value: card.value,
        tone: card.tone ?? "",
      }),
    )
    .join("");
}

function renderDefinitionRow(
  label: string,
  value: string | number | boolean | null | undefined,
  title?: string,
): string {
  return renderComponent(
    title ? "definition-row-described" : "definition-row",
    { label, value, title },
  );
}

function renderSmallContent(content: string | ComponentHTML): string {
  return renderComponent("small-content", { content });
}
