/** Templates contain structure only. Text slots are escaped by default; callers
 * must explicitly identify markup assembled by other components. */
const componentMarkupToken: unique symbol = Symbol("component markup");
interface ComponentHTML {
  readonly [componentMarkupToken]: true;
  readonly value: string;
}
type ComponentValue =
  string | number | boolean | null | undefined | ComponentHTML;

function componentBody(value: string): ComponentHTML {
  return { [componentMarkupToken]: true, value };
}
function componentEscape(value: unknown): string {
  const entities: Record<string, string> = {
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  };
  return String(value ?? "").replace(/[&<>"']/g, (char) => entities[char]);
}
function renderComponent(
  name: string,
  slots: Record<string, ComponentValue>,
): string {
  const template = componentTemplates[name];
  if (!Object.hasOwn(componentTemplates, name))
    throw new Error(`Unknown component: ${name}`);
  return template.replace(/\{\{(\w+)\}\}/g, (_, key: string) => {
    if (!Object.hasOwn(slots, key))
      throw new Error(`Missing ${name} component slot: ${key}`);
    const value = slots[key];
    return typeof value === "object" &&
      value !== null &&
      componentMarkupToken in value
      ? value.value
      : componentEscape(value);
  });
}
