interface AccountingCardReading {
  label: string;
  value: string;
  title?: string;
}
function renderAccountingCard(
  label: string,
  reading: AccountingCardReading,
  note: string,
  breakdown: readonly AccountingCardReading[] = [],
): string {
  const rows = breakdown
    .map((item) => renderDefinitionRow(item.label, item.value, item.title))
    .join("");
  return renderComponent("accounting-card", {
    label,
    note,
    reading: componentBody(
      renderDefinitionRow(reading.label, reading.value, reading.title),
    ),
    breakdown: componentBody(
      rows
        ? renderComponent("accounting-breakdown", { rows: componentBody(rows) })
        : "",
    ),
  });
}
