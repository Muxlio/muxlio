/** Preserve formatting and escaping performed by the existing view models.
 * New shared components should use renderComponent's escaped slots instead. */
function viewValue(value: unknown): string {
  return `${value}`;
}
function renderViewTemplate(
  name: string,
  values: readonly string[] = [],
): string {
  if (!Object.hasOwn(viewTemplates, name))
    throw new Error(`Unknown view template: ${name}`);
  return viewTemplates[name].replace(
    /\{\{value(\d+)\}\}/g,
    (_, index: string) => {
      if (Number(index) >= values.length)
        throw new Error(`Missing view template value: ${name}[${index}]`);
      return values[Number(index)];
    },
  );
}
