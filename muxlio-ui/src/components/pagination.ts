interface PageAction {
  attributes: Readonly<Record<string, string | number>>;
  disabled: boolean;
}
function renderPagination(options: {
  label: string;
  summary: string;
  previous: PageAction;
  next: PageAction;
}): string {
  const button = (label: string, action: PageAction) => {
    const attributes = Object.entries(action.attributes)
      .map(([name, value]) => {
        if (!/^data-[a-z][a-z0-9-]*$/.test(name))
          throw new Error("Pagination actions require data attributes");
        return renderComponent("data-attribute", { name, value });
      })
      .join(" ");
    return renderComponent("page-button", {
      label,
      attributes: componentBody(attributes),
      disabled: componentBody(action.disabled ? "disabled" : ""),
    });
  };
  return renderComponent("pagination", {
    label: options.label,
    summary: options.summary,
    previous: componentBody(button("Previous", options.previous)),
    next: componentBody(button("Next", options.next)),
  });
}
