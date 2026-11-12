function renderInputField(
  label: string,
  name: string,
  type: string,
  value: string | number,
  attributes: ComponentHTML,
): string {
  return renderComponent("input-field", {
    label,
    name,
    type,
    value,
    attributes,
  });
}
function renderTextField(
  label: string,
  name: string,
  value: string,
  attributes: ComponentHTML,
): string {
  return renderComponent("text-field", { label, name, value, attributes });
}
