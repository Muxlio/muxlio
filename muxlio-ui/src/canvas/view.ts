// Canvas top-level view controller, data loading, toolbar, and revisions.

let activeCanvasStore: CanvasInteractionStore | null = null;
let activeCanvasDoc: CanvasDocument | null = null;
let activeCanvasContext: CanvasRendererContext | null = null;
let activeCanvasViewMode: "preview" | "source" = "preview";
let activeCanvasUnsubscribe: (() => void) | null = null;
let activeCanvasRawText = "";
let canvasLoadGeneration = 0;
let activeCanvasInfo = {
  runId: "",
  activationId: "",
  port: "",
  digest: "",
  mime: "",
  path: "",
  size: 0,
  isDraft: false,
  attempt: "",
  seq: 0,
};

async function loadCanvasSnapshots(
  runId: string,
  activationId: string,
  port: string,
  attempt?: string,
): Promise<any[]> {
  const snapshots: any[] = [];
  let after = 0;
  while (true) {
    const page = await api(
      "/v1/runs/" +
        encodeURIComponent(runId) +
        "/snapshots?activationId=" +
        encodeURIComponent(activationId) +
        "&limit=200&after=" +
        after +
        (attempt ? "&attemptId=" + encodeURIComponent(attempt) : ""),
    );
    for (const event of page.snapshots || []) {
      const ref = event.payload?.snapshot;
      if (ref?.name === port)
        snapshots.push({ ...ref, seq: event.seq, attemptId: event.subjectId });
    }
    if (!page.hasMore || !(page.nextAfter > after)) break;
    after = page.nextAfter;
  }
  return snapshots;
}

function canvasFallbackCard(title: string, detail: string): string {
  return renderViewTemplate("canvas/fallback-card", [
    viewValue(componentEscape(title)),
    viewValue(componentEscape(detail)),
    viewValue(""),
  ]);
}

// All sibling artifacts share the same header, actions, and Info hierarchy.
function renderCanvasArtifactPage(
  rootHtml: string,
  sourceHtml: string,
  docTitle: string,
  docDesc: string,
  siblingOutputs: Record<string, CanvasSiblingOutput>,
  portSnapshots: any[],
): void {
  const container = document.getElementById("canvasView");
  if (!container) return;
  const {
    runId,
    activationId,
    port,
    path: targetPath,
    size: targetSize,
    digest: targetDigest,
    mime: targetMime,
    isDraft,
  } = activeCanvasInfo;
  // Toolbar

  const toolbarHtml = renderViewTemplate("canvas/toolbar", [
    viewValue(componentEscape(docTitle)),
    viewValue(componentEscape(docDesc)),
  ]);

  // Sibling file navigation
  const fileLinks = Object.entries(siblingOutputs)
    .map(([pName, pInfo]) => {
      const isCurrent = pName === port;
      const href =
        "/artifacts/view/" +
        encodeURIComponent(runId) +
        "/" +
        encodeURIComponent(activationId) +
        "/" +
        encodeURIComponent(pName);
      return renderViewTemplate("canvas/rail-file", [
        viewValue(isCurrent ? " canvas-rail-link--active" : ""),
        viewValue(componentEscape(href)),
        viewValue(componentEscape(pInfo.path || pName)),
      ]);
    })
    .join("");
  const railHtml = renderViewTemplate("canvas/rail-nav", [
    viewValue(fileLinks),
  ]);

  // Provenance panel
  const provRows = [
    renderDefinitionRow("File", targetPath),
    renderDefinitionRow(
      "Size",
      targetSize ? targetSize.toLocaleString() + " bytes" : "Unavailable",
    ),
    renderDefinitionRow("Run", runId),
    renderDefinitionRow("Step", activationId),
    renderDefinitionRow("Output", port),
    renderDefinitionRow("Digest", targetDigest),
    renderDefinitionRow("Format", targetMime),
    renderDefinitionRow(
      "Status",
      isDraft ? "Draft observation" : "Committed output",
    ),
  ].join("");

  let revOptions = renderComponent("canvas-select-option", {
    value: "committed",
    label: "Committed output",
    selectedAttr: !isDraft ? "selected" : "",
  });
  for (const snap of portSnapshots) {
    const isSel = isDraft && snap.seq === activeCanvasInfo.seq;
    const lbl =
      "Draft seq " + snap.seq + " (" + (snap.attemptId || "attempt") + ")";
    revOptions += renderComponent("canvas-select-option", {
      value: "seq-" + snap.seq,
      label: lbl,
      selectedAttr: isSel ? "selected" : "",
    });
  }
  const provHtml = renderViewTemplate("canvas/provenance-panel", [
    viewValue(provRows),
    viewValue(revOptions),
    viewValue(activeCanvasViewMode === "preview" ? "tab-btn--active" : ""),
    viewValue(activeCanvasViewMode === "preview" ? "true" : "false"),
    viewValue(activeCanvasViewMode === "source" ? "tab-btn--active" : ""),
    viewValue(activeCanvasViewMode === "source" ? "true" : "false"),
    viewValue(railHtml),
    viewValue(activeCanvasDoc ? "Canvas" : "Preview"),
    viewValue(/json/i.test(targetMime) ? "Source JSON" : "Source"),
  ]);

  container.innerHTML = renderViewTemplate("canvas/shell", [
    viewValue(toolbarHtml),
    viewValue(railHtml),
    viewValue(rootHtml),
    viewValue(sourceHtml),
    viewValue(provHtml),
  ]);

  const rootEl = document.getElementById("canvasRoot");
  const srcEl = document.getElementById("canvasSourceView");
  if (rootEl && srcEl) {
    rootEl.hidden = activeCanvasViewMode !== "preview";
    srcEl.hidden = activeCanvasViewMode !== "source";
    // Apply presentation.density token as a class on the canvas root.
    const density = activeCanvasDoc?.presentation?.density;
    if (density) {
      rootEl.classList.add("canvas-density--" + density);
    }
  }

  attachCanvasDOMListeners(container);
}

async function loadCanvasPage(
  runId: string,
  activationId: string,
  port: string,
  snapshotAttempt?: string,
  snapshotSeq?: number,
): Promise<void> {
  const container = document.getElementById("canvasView");
  if (!container) return;

  const generation = ++canvasLoadGeneration;
  activeCanvasDoc = null;
  activeCanvasStore = null;
  activeCanvasContext = null;
  container.hidden = false;
  container.innerHTML = canvasFallbackCard(
    "Loading Canvas",
    "Fetching artifact metadata and document bytes...",
  );

  if (activeCanvasUnsubscribe) {
    activeCanvasUnsubscribe();
    activeCanvasUnsubscribe = null;
  }

  activeCanvasInfo = {
    runId,
    activationId,
    port,
    digest: "",
    mime: "",
    path: "",
    size: 0,
    isDraft: false,
    attempt: snapshotAttempt || "",
    seq: snapshotSeq || 0,
  };

  try {
    const [targetAct, portSnapshots] = await Promise.all([
      api(
        "/v1/runs/" +
          encodeURIComponent(runId) +
          "/activations/" +
          encodeURIComponent(activationId),
      ),
      loadCanvasSnapshots(runId, activationId, port, snapshotAttempt),
    ]);
    if (generation !== canvasLoadGeneration) return;
    const actOutputs = targetAct.outputs || {};
    const committedRef = actOutputs[port];

    let targetDigest = "";
    let targetMime = "application/vnd.muxlio.canvas+json";
    let targetPath = port + ".canvas.json";
    let targetSize = 0;
    let isDraft = false;

    if (snapshotSeq !== undefined && snapshotSeq > 0) {
      const match = portSnapshots.find((s: any) => s.seq === snapshotSeq);
      if (match) {
        targetDigest = match.digest;
        targetMime = match.mime || targetMime;
        targetPath = match.path || targetPath;
        targetSize = match.size || 0;
        isDraft = true;
        activeCanvasInfo.attempt = match.attemptId;
      } else {
        throw new Error("Requested snapshot is unavailable.");
      }
    }

    if (!targetDigest) {
      if (
        committedRef &&
        typeof committedRef === "object" &&
        committedRef.digest
      ) {
        targetDigest = committedRef.digest;
        targetMime = committedRef.mime || targetMime;
        targetPath = committedRef.path || targetPath;
        targetSize = committedRef.size || 0;
        isDraft = false;
      } else if (portSnapshots.length > 0) {
        const latest = portSnapshots[portSnapshots.length - 1];
        targetDigest = latest.digest;
        targetMime = latest.mime || targetMime;
        targetPath = latest.path || targetPath;
        targetSize = latest.size || 0;
        isDraft = true;
        activeCanvasInfo.attempt = latest.attemptId || "";
        activeCanvasInfo.seq = latest.seq || 0;
      }
    }

    if (!targetDigest) {
      container.innerHTML = canvasFallbackCard(
        "Artifact Unavailable",
        "No committed output or intermediate snapshot found for port '" +
          port +
          "'.",
      );
      return;
    }

    activeCanvasInfo.digest = targetDigest;
    activeCanvasInfo.mime = targetMime;
    activeCanvasInfo.path = targetPath;
    activeCanvasInfo.size = targetSize;
    activeCanvasInfo.isDraft = isDraft;

    // Collect sibling outputs for same-activation port resolution
    const siblingOutputs: Record<string, CanvasSiblingOutput> =
      Object.create(null);
    for (const [pName, pRef] of Object.entries(actOutputs)) {
      if (pRef && typeof pRef === "object" && (pRef as any).digest) {
        siblingOutputs[pName] = {
          digest: (pRef as any).digest,
          mime: (pRef as any).mime || "",
          path: (pRef as any).path || pName,
          size: (pRef as any).size,
        };
      }
    }

    // A file reference may point to Markdown, JSON, or another sibling artifact.
    // Dispatch before applying the Canvas-only parser and schema.
    const isCanvas =
      targetMime.split(";")[0].trim().toLowerCase() ===
      "application/vnd.muxlio.canvas+json";
    // Fetch artifact content
    const resp = await uiFetch(
      "/v1/artifacts/" + encodeURIComponent(targetDigest),
    );
    if (resp.status === 410) {
      container.innerHTML = canvasFallbackCard(
        "Artifact Expired",
        "This artifact has expired under the coordinator retention policy.",
      );
      return;
    }
    if (!resp.ok) {
      throw new Error(
        "Unable to download artifact bytes (HTTP " + resp.status + ")",
      );
    }

    const rawText = await readCanvasText(
      resp,
      isCanvas ? CANVAS_DOCUMENT_BYTES : CANVAS_SOURCE_BYTES,
    );
    if (generation !== canvasLoadGeneration) return;
    activeCanvasRawText = rawText;
    if (!isCanvas) {
      const mime = targetMime.split(";")[0].trim().toLowerCase();
      const markdown = mime === "text/markdown" || /\.md$/i.test(targetPath);
      let content: string;
      if (markdown)
        content = renderViewTemplate(
          "inspector/body-inner-html-markdown-body",
          [viewValue(renderMarkdown(rawText))],
        );
      else if (mime === "application/json" || /\+json$/.test(mime)) {
        content = MuxlioJson.jsonPanelFromText(rawText, componentEscape, {
          title: targetPath,
          statePath: "artifact:" + port,
        }).html;
      } else
        content = renderComponent("canvas-code", {
          id: "artifactText",
          language: "",
          code: rawText,
        });
      activeCanvasViewMode = "preview";
      const sourceHtml = renderComponent("canvas-code", {
        id: "artifactSource",
        language: mime.includes("json") ? "json" : "",
        code: rawText,
      });
      renderCanvasArtifactPage(
        content,
        sourceHtml,
        targetPath,
        "",
        siblingOutputs,
        portSnapshots,
      );
      return;
    }

    let parsedDoc: any = null;
    try {
      parsedDoc = JSON.parse(rawText);
    } catch (parseErr: any) {
      container.innerHTML = canvasFallbackCard(
        "Malformed Canvas JSON",
        "Failed to parse JSON: " + parseErr.message,
      );
      return;
    }

    if (
      !parsedDoc ||
      typeof parsedDoc !== "object" ||
      parsedDoc.version !== 1
    ) {
      const ver = parsedDoc && parsedDoc.version;
      const msg =
        "Canvas version " +
        (ver !== undefined ? ver : "unknown") +
        " is not supported by this viewer (expected version 1).";
      container.innerHTML = canvasFallbackCard("Unsupported Version", msg);
      return;
    }

    validateCanvasDocument(parsedDoc);
    activeCanvasDoc = parsedDoc;

    // Resolve sources
    const resolvedSources = new Map<string, unknown>();
    if (activeCanvasDoc.sources) {
      for (const [sName, sDef] of Object.entries(activeCanvasDoc.sources)) {
        if ("value" in sDef) {
          resolvedSources.set(sName, sDef.value);
        } else if ("artifactPort" in sDef) {
          const sibling = siblingOutputs[sDef.artifactPort];
          const siblingMime = sibling && sibling.mime ? sibling.mime : "";
          const isSiblingJson =
            siblingMime === "application/json" ||
            siblingMime.startsWith("application/json;") ||
            siblingMime === "" ||
            /\+json$/i.test(siblingMime);
          if (sibling && sibling.digest && isSiblingJson) {
            const siblingSize = sibling.size ?? 0;
            const SIBLING_BYTE_LIMIT = CANVAS_SOURCE_BYTES; // 5 MiB
            if (siblingSize > 0 && siblingSize > SIBLING_BYTE_LIMIT) {
              // Skip silently — data component will show "Data not yet available"
            } else {
              try {
                const sibResp = await uiFetch(
                  "/v1/artifacts/" + encodeURIComponent(sibling.digest),
                );
                if (sibResp.ok) {
                  // Artifact metadata determines the format. Byte storage may
                  // serve JSON as text/plain or a generic download MIME.
                  const sibText = await readCanvasText(
                    sibResp,
                    SIBLING_BYTE_LIMIT,
                  );
                  if (sibText.length <= SIBLING_BYTE_LIMIT) {
                    try {
                      resolvedSources.set(sName, JSON.parse(sibText));
                    } catch {}
                  }
                }
              } catch {}
            }
          }
        }
      }
    }

    if (generation !== canvasLoadGeneration) return;
    activeCanvasStore = new CanvasInteractionStore(activeCanvasDoc);

    const renderCurrentView = () => {
      if (!activeCanvasDoc || !activeCanvasStore) return;

      const rendererCtx: CanvasRendererContext = {
        sources: resolvedSources,
        state: activeCanvasStore.state,
        controlTypes: activeCanvasStore.controlTypes,
        resolveData: (ref) => resolveCanvasData(ref, rendererCtx),
        siblingOutputs,
        runId,
        activationId,
        tableSortState: activeCanvasStore.tableSort,
        tablePageState: activeCanvasStore.tablePages,
        activeTabsState: activeCanvasStore.activeTabs,
        selectedPointState: activeCanvasStore.selectedPoints,
        collapsedSections: activeCanvasStore.collapsedSections,
      };
      activeCanvasContext = rendererCtx;

      // Render root tree
      const rootHtml = renderCanvasDocumentBody(
        activeCanvasDoc,
        rendererCtx,
      ).value;

      // Source JSON view
      let sourceHtml = "";
      if (
        typeof MuxlioJson !== "undefined" &&
        typeof MuxlioJson.jsonPanelFromValue === "function"
      ) {
        const panel = MuxlioJson.jsonPanelFromValue(
          activeCanvasDoc,
          componentEscape,
          {
            title: targetPath,
            statePath: "canvas:source",
          },
        );
        sourceHtml = panel.html;
      } else {
        sourceHtml = renderComponent("canvas-code", {
          id: "canvasSourceCode",
          language: "json",
          code: activeCanvasRawText,
        });
      }

      renderCanvasArtifactPage(
        rootHtml,
        sourceHtml,
        activeCanvasDoc.title || "Canvas",
        activeCanvasDoc.description || "",
        siblingOutputs,
        portSnapshots,
      );
    };

    renderCurrentView();
    activeCanvasUnsubscribe = activeCanvasStore.subscribe(() => {
      // Re-render preview area preserving scroll
      if (!activeCanvasDoc || !activeCanvasStore || !activeCanvasContext)
        return;
      const rootEl = document.getElementById("canvasRoot");
      if (!rootEl) return;
      const scrollY = rootEl.scrollTop;
      const rootHtml = renderCanvasDocumentBody(
        activeCanvasDoc,
        activeCanvasContext,
      ).value;
      const focused = document.activeElement as HTMLInputElement | null;
      const focusId = focused && rootEl.contains(focused) ? focused.id : "";
      const focusControl =
        focused && rootEl.contains(focused)
          ? focused.dataset.canvasId
          : undefined;
      const selection =
        focused?.tagName === "INPUT" &&
        focused.dataset.canvasControl === "search"
          ? {
              start: focused.selectionStart,
              end: focused.selectionEnd,
              direction: focused.selectionDirection,
            }
          : null;
      rootEl.innerHTML = rootHtml;
      if (focusId || focusControl) {
        const replacement = focusControl
          ? Array.from(
              rootEl.querySelectorAll<HTMLInputElement>("[data-canvas-id]"),
            ).find((el) => el.dataset.canvasId === focusControl)
          : (document.getElementById(focusId) as HTMLInputElement | null);
        if (replacement && rootEl.contains(replacement)) {
          replacement.focus({ preventScroll: true });
          if (selection && selection.start !== null && selection.end !== null)
            replacement.setSelectionRange(
              selection.start,
              selection.end,
              selection.direction,
            );
        }
      }
      rootEl.scrollTop = scrollY;
    });
  } catch (err: any) {
    if (generation !== canvasLoadGeneration) return;
    container.innerHTML = canvasFallbackCard(
      "Unable to Load Canvas",
      err.message || String(err),
    );
  }
}

function attachCanvasDOMListeners(root: HTMLElement): void {
  // Mode toggles
  const btnPrev = document.getElementById("canvasModePreview");
  const btnSrc = document.getElementById("canvasModeSource");
  if (btnPrev && btnSrc) {
    btnPrev.tabIndex = activeCanvasViewMode === "preview" ? 0 : -1;
    btnSrc.tabIndex = activeCanvasViewMode === "source" ? 0 : -1;
    btnPrev.onclick = () => {
      activeCanvasViewMode = "preview";
      btnPrev.classList.add("tab-btn--active");
      btnPrev.setAttribute("aria-selected", "true");
      btnSrc.classList.remove("tab-btn--active");
      btnSrc.setAttribute("aria-selected", "false");
      btnPrev.tabIndex = 0;
      btnSrc.tabIndex = -1;
      const r = document.getElementById("canvasRoot");
      const s = document.getElementById("canvasSourceView");
      if (r) r.hidden = false;
      if (s) s.hidden = true;
    };
    btnSrc.onclick = () => {
      activeCanvasViewMode = "source";
      btnSrc.classList.add("tab-btn--active");
      btnSrc.setAttribute("aria-selected", "true");
      btnPrev.classList.remove("tab-btn--active");
      btnPrev.setAttribute("aria-selected", "false");
      btnSrc.tabIndex = 0;
      btnPrev.tabIndex = -1;
      const r = document.getElementById("canvasRoot");
      const s = document.getElementById("canvasSourceView");
      if (r) r.hidden = true;
      if (s) s.hidden = false;
    };
  }

  // Copy link
  const copyBtn = document.getElementById("canvasCopyLinkBtn");
  if (copyBtn) {
    copyBtn.onclick = async () => {
      try {
        await copyCanvasLink();
        copyBtn.textContent = "Copied";
        setTimeout(() => {
          copyBtn.textContent = "Copy link";
        }, 2000);
        message("Link copied.");
      } catch (e: any) {
        message("Failed to copy link: " + e.message, true);
      }
    };
  }

  // Download
  const downloadBtn = document.getElementById("canvasDownloadBtn");
  if (downloadBtn) {
    downloadBtn.title = "Download as PDF";
    downloadBtn.onclick = async () => {
      (downloadBtn as HTMLButtonElement).disabled = true;
      downloadBtn.textContent = "Preparing PDF…";
      try {
        await downloadCanvasPdf();
      } catch (error: any) {
        message("PDF download failed: " + error.message, true);
      } finally {
        (downloadBtn as HTMLButtonElement).disabled = false;
        downloadBtn.textContent = "Download";
      }
    };
  }

  // Pin
  const pinBtn = document.getElementById("canvasPinBtn");
  if (pinBtn && activeCanvasInfo.digest) {
    pinBtn.onclick = async () => {
      try {
        await api("/v1/retention", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            kind: "artifact",
            id: activeCanvasInfo.digest,
            pinned: true,
          }),
        });
        message("Artifact pinned.");
      } catch (e: any) {
        message("Pin failed: " + e.message, true);
      }
    };
  }

  // Toggle provenance
  const provToggle = document.getElementById("canvasToggleProvenanceBtn");
  if (provToggle) {
    const closeInfo = document.getElementById("canvasCloseInfoBtn");
    if (closeInfo)
      closeInfo.onclick = () => {
        const rail = document.getElementById("canvasInspectorSlot");
        if (rail) rail.hidden = true;
        provToggle.setAttribute("aria-expanded", "false");
        provToggle.focus();
      };
    provToggle.onclick = () => {
      const rail = document.getElementById("canvasInspectorSlot");
      if (rail) {
        rail.hidden = !rail.hidden;
        provToggle.setAttribute("aria-expanded", String(!rail.hidden));
      }
    };
  }

  // Revisions dropdown
  const revSelect = document.getElementById(
    "canvasRevisionSelect",
  ) as HTMLSelectElement | null;
  if (revSelect) {
    revSelect.onchange = () => {
      const val = revSelect.value;
      const url = new URL(location.href);
      url.searchParams.delete("attempt");
      url.searchParams.delete("seq");
      let seq: number | undefined;
      if (val.startsWith("seq-")) {
        seq = Number(val.slice(4));
        url.searchParams.set("seq", String(seq));
      } else if (val !== "committed") return;
      history.replaceState(
        history.state,
        "",
        url.pathname + url.search + url.hash,
      );
      loadCanvasPage(
        activeCanvasInfo.runId,
        activeCanvasInfo.activationId,
        activeCanvasInfo.port,
        undefined,
        seq,
      );
    };
  }

  // Delegated Canvas content events
  root.onclick = (e) => {
    if (!activeCanvasStore) return;
    const target = e.target as HTMLElement | null;
    if (!target) return;

    // Tabs
    const tabBtn = target.closest<HTMLElement>("[data-canvas-action='tab']");
    if (tabBtn) {
      const ownerId = tabBtn.dataset.canvasOwner;
      const tabId = tabBtn.dataset.canvasTarget;
      if (ownerId && tabId) {
        activeCanvasStore.dispatch({
          type: "selectTab",
          componentId: ownerId,
          tabId,
        });
      }
      return;
    }

    // Table sort
    const sortTh = target.closest<HTMLElement>("[data-canvas-action='sort']");
    if (sortTh) {
      const ownerId = sortTh.dataset.canvasOwner;
      const field = sortTh.dataset.canvasField;
      if (ownerId && field) {
        activeCanvasStore.dispatch({
          type: "sortTable",
          componentId: ownerId,
          field,
        });
      }
      return;
    }

    // Table page
    const pageBtn = target.closest<HTMLElement>("[data-canvas-action='page']");
    if (pageBtn) {
      const ownerId = pageBtn.dataset.canvasOwner;
      const pageStr = pageBtn.dataset.canvasPage;
      if (ownerId && pageStr) {
        activeCanvasStore.dispatch({
          type: "setPage",
          componentId: ownerId,
          page: Number(pageStr),
        });
      }
      return;
    }

    // Bar chart point selection
    const barItem = target.closest<HTMLElement>("[data-canvas-action='point']");
    if (barItem) {
      const ownerId = barItem.dataset.canvasOwner;
      const key = barItem.dataset.canvasKey;
      if (ownerId && key !== undefined) {
        activeCanvasStore.dispatch({
          type: "selectChartPoint",
          componentId: ownerId,
          key,
        });
      }
      return;
    }
  };

  root.onchange = (e) => {
    if (!activeCanvasStore) return;
    const target = e.target as HTMLInputElement | HTMLSelectElement | null;
    if (!target) return;

    const ctrlType = target.dataset.canvasControl;
    const stateId = target.dataset.canvasId;
    if (!ctrlType || !stateId) return;

    if (ctrlType === "select") {
      activeCanvasStore.dispatch({
        type: "setState",
        stateId,
        value: target.value,
      });
    } else if (ctrlType === "toggle") {
      const chk = (target as HTMLInputElement).checked;
      activeCanvasStore.dispatch({
        type: "setState",
        stateId,
        value: chk,
      });
    }
  };

  root.oninput = (e) => {
    if (!activeCanvasStore) return;
    const target = e.target as HTMLInputElement | null;
    if (!target) return;

    const ctrlType = target.dataset.canvasControl;
    const stateId = target.dataset.canvasId;
    if (ctrlType === "search" && stateId) {
      activeCanvasStore.dispatch({
        type: "setState",
        stateId,
        value: target.value,
      });
    }
  };

  // Section collapse/expand — <details> fires a native "toggle" event after
  // open state changes. Update the store so re-renders preserve the state.
  // The container persists across renders, so the capture listener is bound once.
  if (root.dataset.canvasToggleBound === "true") return;
  root.dataset.canvasToggleBound = "true";
  root.addEventListener(
    "toggle",
    (e) => {
      if (!activeCanvasStore) return;
      const details = e.target as HTMLDetailsElement | null;
      if (
        !details ||
        details.tagName !== "DETAILS" ||
        !details.classList.contains("canvas-section") ||
        !details.id
      )
        return;
      const id = details.id;
      const isNowOpen = details.open;
      const wasCollapsed = activeCanvasStore.collapsedSections.has(id);
      // Only dispatch if the DOM state disagrees with the store to avoid loops.
      if (isNowOpen && wasCollapsed) {
        activeCanvasStore.dispatch({ type: "toggleSection", componentId: id });
      } else if (!isNowOpen && !wasCollapsed) {
        activeCanvasStore.dispatch({ type: "toggleSection", componentId: id });
      }
    },
    true,
  );
}
