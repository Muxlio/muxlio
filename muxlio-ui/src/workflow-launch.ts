type WorkflowCatalogEntry = {
  digest: string;
  name?: string;
};
type WorkspaceCatalogEntry = {
  workspaceId: string;
  status: string;
};

function parseWorkflowRunInput(text: string) {
  const doc = MuxlioJson.parseJsonDocument(text);
  if (doc.kind === "empty" || doc.kind === "unavailable")
    return { error: "Input must be a JSON value." };
  if (doc.kind === "error")
    return { error: "Invalid JSON. Enter a complete JSON value." };
  // Submit the trimmed source so literals (including large integers) stay exact.
  return { raw: doc.raw.trim() };
}

function renderPreflightPanel(result: Record<string, any>) {
  const parts = [
    renderNotice(
      `Preflight ${result.feasible ? "feasible" : "infeasible"} · ${result.workersChecked} workers checked`,
      { error: !result.feasible },
    ),
  ];
  const appendLines = (title: string, lines: string[] = []) => {
    if (!lines?.length) return;
    const items = lines
      .map((line) =>
        renderViewTemplate("markdown/blocks-li", [viewValue(esc(line))]),
      )
      .join("");
    parts.push(
      renderViewTemplate("workspace/preflight-section", [
        viewValue(esc(title)),
        viewValue(items),
      ]),
    );
  };
  if (result.workspaceError) appendLines("Workspace", [result.workspaceError]);
  if (result.workerError)
    appendLines("Worker registry", [
      "The coordinator could not read the worker registry.",
    ]);
  if (result.inputError) appendLines("Input schema", [result.inputError]);
  for (const issue of result.planIssues ?? []) {
    const where = [issue.subgraph, issue.nodeId].filter(Boolean).join(" · ");
    const head = [issue.severity, issue.code, where].filter(Boolean).join(" ");
    appendLines(
      "Plan",
      [`${head}: ${issue.message}`, issue.fix].filter(Boolean),
    );
  }
  for (const ex of result.executors ?? []) {
    const lines = [...(ex.blockingReasons ?? []), ...(ex.diagnostics ?? [])];
    appendLines(
      `${ex.executor} (${ex.subgraph} · ${ex.nodeIds.join(", ")})`,
      lines,
    );
  }
  appendLines("Verified checks", result.verified);
  appendLines("Unverified caveats", result.unverified);
  appendLines("Deferred checks", result.deferred);
  return parts.join("");
}

async function workflowRunFetch(path: string, init?: RequestInit) {
  const response = await uiFetch(path, init);
  if (!response.ok) {
    const data = await response.json().catch(() => null);
    throw Object.assign(
      new Error(
        response.status < 500
          ? data?.message || `HTTP ${response.status}`
          : `Coordinator request failed (HTTP ${response.status}).`,
      ),
      { status: response.status },
    );
  }
  return response;
}

function workflowRunPost(path: string, body: string, key?: string) {
  return workflowRunFetch(path, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(key && { "Idempotency-Key": key }),
    },
    body,
  }).then((response) => response.json());
}

function workflowRunOption(value: string, label: string) {
  return renderViewTemplate("shared/select-option", [
    viewValue(esc(value)),
    viewValue(esc(label)),
  ]);
}

const workflowRunDialog =
  document.querySelector<HTMLDialogElement>("#runWorkflowModal")!;
const workflowRunVersion = workflowRunDialog.querySelector<HTMLSelectElement>(
  "#runWorkflowVersion",
)!;
const workflowRunWorkspace = workflowRunDialog.querySelector<HTMLSelectElement>(
  "#runWorkflowWorkspace",
)!;
const workflowRunInput =
  workflowRunDialog.querySelector<HTMLTextAreaElement>("#runWorkflowInput")!;
const workflowRunInputError = workflowRunDialog.querySelector<HTMLElement>(
  "#runWorkflowInputError",
)!;
const workflowRunStatus =
  workflowRunDialog.querySelector<HTMLElement>("#runWorkflowStatus")!;
const workflowRunResult = workflowRunDialog.querySelector<HTMLElement>(
  "#runWorkflowPreflight",
)!;
const workflowRunPreflightButton =
  workflowRunDialog.querySelector<HTMLButtonElement>(
    "#runWorkflowPreflightBtn",
  )!;
const workflowRunStartButton =
  workflowRunDialog.querySelector<HTMLButtonElement>("#runWorkflowSubmitBtn")!;
const workflowRunCloseButtons =
  workflowRunDialog.querySelectorAll<HTMLButtonElement>(
    "[data-close-run-workflow]",
  );

let workflowRunSession = 0;
let workflowRunBusy: "preflight" | "start" | null = null;
let workflowRunFeasible = false;
let workflowRunOpener: HTMLElement | null = null;
const workflowRunKeys = new Map<string, string>();

function setWorkflowRunStatus(message: string, error = false) {
  workflowRunStatus.className = error ? "notice error" : "subtle";
  workflowRunStatus.setAttribute("role", error ? "alert" : "status");
  workflowRunStatus.textContent = message;
}

function syncWorkflowRunControls() {
  const { raw, error } = parseWorkflowRunInput(workflowRunInput.value);
  workflowRunInputError.textContent = error ?? "";
  workflowRunInput.setAttribute("aria-invalid", String(!!error));
  const busy = workflowRunBusy !== null;
  workflowRunVersion.disabled = busy;
  workflowRunWorkspace.disabled =
    busy || ![...workflowRunWorkspace.options].some((option) => option.value);
  workflowRunInput.readOnly = busy;
  const ready =
    !!workflowRunVersion.value &&
    !!workflowRunWorkspace.value &&
    raw !== undefined;
  workflowRunPreflightButton.disabled = busy || !ready;
  workflowRunStartButton.disabled = busy || !ready || !workflowRunFeasible;
  workflowRunStartButton.textContent =
    workflowRunBusy === "start" ? "Starting…" : "Start run";
  for (const button of workflowRunCloseButtons)
    button.disabled = workflowRunBusy === "start";
}

function resetWorkflowRunPreflight() {
  workflowRunFeasible = false;
  workflowRunResult.innerHTML = "";
}

function closeWorkflowRunModal() {
  if (!workflowRunDialog.open || workflowRunBusy === "start") return;
  workflowRunSession++;
  workflowRunBusy = null;
  workflowRunDialog.close();
  // Workflow rows can be refreshed while the dialog is open.
  const opener = workflowRunOpener;
  workflowRunOpener = null;
  if (opener?.isConnected) opener.focus();
  else document.getElementById("openRunWorkflow")?.focus();
}

async function openWorkflowRunModal(
  versions: WorkflowCatalogEntry[],
  opener: HTMLElement,
  selectedDigest?: string,
) {
  if (workflowRunDialog.open) return;
  const session = ++workflowRunSession;
  workflowRunOpener = opener;
  workflowRunVersion.innerHTML = versions
    .map(({ digest, name }) =>
      workflowRunOption(digest, `${name || "Unnamed"} · ${digest}`),
    )
    .join("");
  if (selectedDigest) workflowRunVersion.value = selectedDigest;
  workflowRunWorkspace.innerHTML = workflowRunOption("", "Loading workspaces…");
  workflowRunInput.value = "{}";
  resetWorkflowRunPreflight();
  setWorkflowRunStatus("");
  syncWorkflowRunControls();
  workflowRunDialog.showModal();
  try {
    const workspaces: WorkspaceCatalogEntry[] = [];
    let offset = 0;
    while (true) {
      const response = await workflowRunFetch(
        `/v1/workspaces?limit=100&offset=${offset}`,
      );
      const page = await response.json();
      if (session !== workflowRunSession) return;
      workspaces.push(...page.workspaces);
      if (!page.hasMore) break;
      if (!Number.isSafeInteger(page.nextOffset) || page.nextOffset <= offset)
        throw new Error("The workspace catalog returned an invalid page.");
      offset = page.nextOffset;
    }
    const placeholder =
      workspaces.length === 0
        ? workflowRunOption("", "No authorized workspaces")
        : workspaces.length > 1
          ? workflowRunOption("", "Select a workspace…")
          : "";
    workflowRunWorkspace.innerHTML =
      placeholder +
      workspaces
        .map(({ workspaceId, status }) =>
          workflowRunOption(
            workspaceId,
            `${workspaceId} · ${status.replaceAll("_", " ")}`,
          ),
        )
        .join("");
  } catch (error) {
    if (session !== workflowRunSession) return;
    workflowRunWorkspace.innerHTML = workflowRunOption(
      "",
      "Catalog unavailable",
    );
    setWorkflowRunStatus(
      error.status === 403 || error.status === 401
        ? "This credential cannot read the workspace catalog."
        : "Could not load the workspace catalog. Reopen the dialog to retry.",
      true,
    );
  }
  syncWorkflowRunControls();
}

function workflowRunRequestError(error, step: "preflight" | "start") {
  if (
    error.status === 410 ||
    (error.status === 404 && (step === "start" || error.versionLookup))
  ) {
    workflowRunVersion.selectedOptions[0].disabled = true;
    workflowRunVersion.selectedIndex = -1;
    resetWorkflowRunPreflight();
    setWorkflowRunStatus(
      "This workflow version is no longer registered. Choose another version.",
      true,
    );
  } else if (error.status === 409) {
    setWorkflowRunStatus(
      "The submission conflicts with an existing request. Check Runs before starting a new submission.",
      true,
    );
    resetWorkflowRunPreflight();
  } else if (error.status === 401 || error.status === 403) {
    setWorkflowRunStatus(
      step === "preflight"
        ? "This credential cannot preflight the selected workflow."
        : "This credential cannot start the selected workflow.",
      true,
    );
  } else {
    setWorkflowRunStatus(error.message || "Coordinator request failed.", true);
  }
}

async function runWorkflowPreflight() {
  if (workflowRunPreflightButton.disabled) return;
  const session = workflowRunSession;
  const digest = workflowRunVersion.value;
  const workspace = JSON.stringify(workflowRunWorkspace.value);
  const { raw } = parseWorkflowRunInput(workflowRunInput.value);
  workflowRunBusy = "preflight";
  resetWorkflowRunPreflight();
  setWorkflowRunStatus("Running preflight…");
  syncWorkflowRunControls();
  try {
    const response = await workflowRunFetch(
      `/v1/workflow-versions/${encodeURIComponent(digest)}`,
      { cache: "no-store" },
    ).catch((error) => {
      throw Object.assign(error, { versionLookup: true });
    });
    const plan = await response.text();
    if (session !== workflowRunSession) return;
    const result = await workflowRunPost(
      "/v1/runs/preflight",
      `{"workspaceId":${workspace},"plan":${plan},"input":${raw}}`,
    );
    if (session !== workflowRunSession) return;
    if (
      typeof result?.feasible !== "boolean" ||
      !Number.isInteger(result.workersChecked)
    )
      throw new Error(
        "The coordinator returned an invalid preflight response.",
      );
    workflowRunResult.innerHTML = renderPreflightPanel(result);
    workflowRunFeasible = result.feasible;
    setWorkflowRunStatus("");
  } catch (error) {
    if (session === workflowRunSession)
      workflowRunRequestError(error, "preflight");
  } finally {
    if (session === workflowRunSession) {
      workflowRunBusy = null;
      syncWorkflowRunControls();
    }
  }
}

async function submitWorkflowRun() {
  if (workflowRunStartButton.disabled) return;
  const { raw } = parseWorkflowRunInput(workflowRunInput.value);
  const digest = JSON.stringify(workflowRunVersion.value);
  const workspace = JSON.stringify(workflowRunWorkspace.value);
  const body = `{"planDigest":${digest},"workspaceId":${workspace},"input":${raw}}`;
  let key = workflowRunKeys.get(body);
  if (!key) {
    key =
      typeof crypto.randomUUID === "function"
        ? crypto.randomUUID()
        : Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
            byte.toString(16).padStart(2, "0"),
          ).join("");
    workflowRunKeys.set(body, key);
  }
  workflowRunBusy = "start";
  setWorkflowRunStatus("Submitting run…");
  syncWorkflowRunControls();
  try {
    const result = await workflowRunPost("/v1/runs", body, key);
    if (typeof result?.runId !== "string" || !result.runId.trim())
      throw new Error(
        "The coordinator returned no run ID. Retry to confirm the submission.",
      );
    workflowRunKeys.delete(body);
    workflowRunBusy = null;
    closeWorkflowRunModal();
    navigateTo(`/runs/${encodeURIComponent(result.runId)}`);
  } catch (error) {
    // Keep the same key after an ambiguous response, including across reopens.
    if (
      error.status >= 400 &&
      error.status < 500 &&
      error.status !== 408 &&
      error.status !== 429
    )
      workflowRunKeys.delete(body);
    workflowRunRequestError(error, "start");
  } finally {
    workflowRunBusy = null;
    syncWorkflowRunControls();
  }
}

for (const button of workflowRunCloseButtons)
  button.addEventListener("click", closeWorkflowRunModal);
workflowRunDialog.addEventListener("cancel", (event) => {
  event.preventDefault();
  closeWorkflowRunModal();
});
workflowRunVersion.addEventListener("change", workflowRunEdited);
workflowRunWorkspace.addEventListener("change", workflowRunEdited);
workflowRunInput.addEventListener("input", workflowRunEdited);
workflowRunPreflightButton.addEventListener("click", runWorkflowPreflight);
workflowRunStartButton.addEventListener("click", submitWorkflowRun);

function workflowRunEdited() {
  resetWorkflowRunPreflight();
  setWorkflowRunStatus("");
  syncWorkflowRunControls();
}
