interface FormDialogOptions {
  title: string;
  fields: ComponentHTML;
  submit?: string;
  onSubmit(form: HTMLFormElement): Promise<unknown>;
}
function openFormDialog(options: FormDialogOptions): HTMLFormElement {
  const dialog = document.createElement("dialog");
  dialog.className = "ops-dialog";
  dialog.setAttribute("aria-label", options.title);
  dialog.innerHTML = renderComponent("form-dialog", {
    title: options.title,
    fields: options.fields,
    submit: options.submit ?? "Save",
  });
  let busy = false;
  document.body.append(dialog);
  dialog.addEventListener("close", () => dialog.remove(), { once: true });
  dialog.addEventListener("cancel", (event) => {
    if (busy) event.preventDefault();
  });
  const closeButtons = [
    ...dialog.querySelectorAll<HTMLButtonElement>("[data-close-dialog]"),
  ];
  closeButtons.forEach(
    (button) =>
      (button.onclick = () => {
        if (!busy) dialog.close();
      }),
  );
  const form = dialog.querySelector<HTMLFormElement>("form")!;
  const submit = form.querySelector<HTMLButtonElement>("[type=submit]")!;
  const status = form.querySelector<HTMLElement>("[role=status]")!;
  form.onsubmit = async (event) => {
    event.preventDefault();
    if (busy) return;
    busy = true;
    submit.disabled = true;
    closeButtons.forEach((button) => (button.disabled = true));
    status.textContent = "Saving…";
    try {
      await options.onSubmit(form);
      dialog.close();
    } catch (error) {
      status.textContent =
        error instanceof Error ? error.message : String(error);
    } finally {
      busy = false;
      submit.disabled = false;
      closeButtons.forEach((button) => (button.disabled = false));
    }
  };
  dialog.showModal();
  return form;
}
