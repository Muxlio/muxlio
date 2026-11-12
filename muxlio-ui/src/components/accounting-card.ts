interface AccountingCardReading {
  label: string;
  value: string;
  title?: string;
}
function renderAccountingCard(
  label: string,
  readings: readonly AccountingCardReading[],
  coverage: string,
): string {
  return renderComponent("accounting-card", {
    label,
    coverage,
    rows: componentBody(
      readings
        .map((reading) =>
          renderDefinitionRow(reading.label, reading.value, reading.title),
        )
        .join(""),
    ),
  });
}
