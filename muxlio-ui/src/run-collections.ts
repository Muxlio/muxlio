type RunCollectionDeclaration = {
  name: string;
  itemSchema: unknown;
};

type RunCollectionSnapshot = {
  open: boolean;
  items: { id: string; value: unknown }[];
};

let runCollectionsView: {
  runId: string;
  refreshing: boolean;
  entries: Map<string, ReturnType<typeof mountRunCollection>>;
};

function mountRunCollection(
  container: HTMLElement,
  path: string,
  declaration: RunCollectionDeclaration,
) {
  const template = document.createElement("template");
  template.innerHTML = renderViewTemplate("run-collections/collection", [
    viewValue(esc(declaration.name)),
  ]);
  const section = template.content.firstElementChild as HTMLElement;
  container.append(section);
  const state = section.querySelector<HTMLElement>("[data-collection-state]");
  const items = section.querySelector("ol");
  const empty = section.querySelector<HTMLElement>("[data-collection-empty]");
  const loadError = section.querySelector<HTMLElement>(
    "[data-collection-load-error]",
  );
  const actions = section.querySelector<HTMLElement>(
    "[data-collection-actions]",
  );
  const add = section.querySelector<HTMLButtonElement>("[data-collection-add]");
  const close = section.querySelector<HTMLButtonElement>(
    "[data-collection-close]",
  );
  const editor = section.querySelector<HTMLFormElement>("form");
  const draft = editor.querySelector("textarea");
  const submit = editor.querySelector("button");
  const status = section.querySelector<HTMLElement>("[data-collection-status]");
  section.querySelector("[data-collection-schema]").textContent = pretty(
    declaration.itemSchema,
  );

  let canAppend = false;
  let snapshot: RunCollectionSnapshot;
  let itemsSignature = "";
  let busy = false;
  let revision = 0;

  function updateControls() {
    const editable = canAppend && snapshot?.open === true;
    actions.hidden = !editable;
    add.hidden = !editor.hidden;
    add.disabled = close.disabled = submit.disabled = busy;
    submit.hidden = !editable;
    draft.readOnly = busy || !editable;
  }

  function showSnapshot(next: RunCollectionSnapshot) {
    snapshot = next;
    state.textContent = next.open ? "Open" : "Closed";
    loadError.hidden = true;
    const signature = JSON.stringify(next.items);
    if (signature !== itemsSignature) {
      itemsSignature = signature;
      const list = document.createDocumentFragment();
      for (const { id, value } of next.items) {
        const item = document.createElement("li");
        const title = document.createElement("strong");
        const content = document.createElement("pre");
        title.textContent = id;
        content.className = "collection-json";
        content.textContent = pretty(value);
        item.append(title, content);
        list.append(item);
      }
      items.replaceChildren(list);
      empty.hidden = next.items.length !== 0;
    }
    updateControls();
  }

  async function readSnapshot() {
    const version = revision;
    try {
      const { collection } = await api(path);
      if (version === revision) showSnapshot(collection);
    } catch (error) {
      if (version !== revision) return;
      loadError.textContent = error.message;
      loadError.hidden = false;
      if (!snapshot) state.textContent = "Unavailable";
    }
  }

  async function change(operation: "items" | "close", body?: string) {
    if (busy || !canAppend || !snapshot?.open) return;
    busy = true;
    revision++;
    updateControls();
    status.textContent = "";
    let focus: HTMLElement;
    try {
      const { collection, accepted } = await api(`${path}/${operation}`, {
        method: "POST",
        ...(body === undefined
          ? {}
          : {
              headers: { "Content-Type": "application/json" },
              body,
            }),
      });
      if (operation === "items") {
        draft.value = "";
        editor.hidden = true;
        status.textContent = accepted
          ? "Item accepted."
          : "Item already accepted.";
        focus = collection.open ? add : state;
      } else {
        status.textContent =
          "Collection closed. Accepted work will finish before the run completes.";
        focus = state;
        if (!draft.value) editor.hidden = true;
      }
      showSnapshot(collection);
    } catch (error) {
      status.textContent = error.message;
      await readSnapshot();
    } finally {
      busy = false;
      updateControls();
      if (focus === state) state.tabIndex = -1;
      if (
        section.contains(document.activeElement) ||
        document.activeElement === document.body
      )
        focus?.focus();
    }
  }

  add.onclick = () => {
    editor.hidden = false;
    updateControls();
    draft.focus();
  };
  close.onclick = () => change("close");
  editor.onsubmit = (event) => {
    event.preventDefault();
    try {
      JSON.parse(draft.value);
    } catch {
      status.textContent =
        "Enter a valid JSON item that matches the item schema.";
      draft.focus();
      return;
    }
    // Preserve the original JSON, including numbers beyond JavaScript precision.
    void change("items", draft.value);
  };

  state.textContent = "Loading…";
  return {
    refresh(allowed: boolean) {
      canAppend = allowed;
      updateControls();
      if (!busy) return readSnapshot();
    },
  };
}

async function refreshRunCollections(runId: string) {
  const container = document.getElementById("runCollections");
  const tab = document.getElementById("tab-collections");
  const error = document.getElementById("runCollectionsError");
  if (!container || !tab || !error) return;

  if (runCollectionsView?.runId !== runId) {
    runCollectionsView = { runId, refreshing: false, entries: new Map() };
    container.replaceChildren();
    tab.hidden = error.hidden = true;
    if (wbActiveTab === "collections") showWbTab("graph");
  }
  const view = runCollectionsView;
  if (view.refreshing) return;
  view.refreshing = true;
  const path = `/v1/runs/${encodeURIComponent(runId)}/collections`;
  try {
    const { collections, canAppend } = await api(path);
    if (runCollectionsView !== view || current !== runId) return;
    tab.hidden = !collections.length;
    error.hidden = true;
    await Promise.all(
      collections.map((collection: RunCollectionDeclaration) => {
        if (!view.entries.has(collection.name)) {
          const itemPath = `${path}/${encodeURIComponent(collection.name)}`;
          view.entries.set(
            collection.name,
            mountRunCollection(container, itemPath, collection),
          );
        }
        return view.entries.get(collection.name).refresh(canAppend === true);
      }),
    );
  } catch (failure) {
    if (runCollectionsView !== view || current !== runId) return;
    error.textContent = failure.message;
    error.hidden = false;
  } finally {
    view.refreshing = false;
  }
}
