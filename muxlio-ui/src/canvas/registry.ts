// Canvas component registry mapping public vocabulary types to trusted renderers.

function renderCanvasStack(
  node: CanvasStackNode,
  context: CanvasRendererContext,
  renderChild: (n: CanvasNode) => ComponentHTML,
): ComponentHTML {
  const gapClass = node.gap ? "canvas-stack--" + node.gap : "canvas-stack--comfortable";
  const renderedChildren = (node.children || [])
    .map((c) => renderChild(c).value)
    .join("");
  return componentBody(
    renderComponent("canvas-stack", {
      id: node.id,
      gapClass,
      children: componentBody(renderedChildren),
    }),
  );
}

function renderCanvasGrid(
  node: CanvasGridNode,
  context: CanvasRendererContext,
  renderChild: (n: CanvasNode) => ComponentHTML,
): ComponentHTML {
  const gapClass = node.gap ? "canvas-grid--" + node.gap : "canvas-grid--comfortable";
  let gridStyle = "";
  if (node.columns === "auto-fit") {
    gridStyle = "grid-template-columns:repeat(auto-fit,minmax(240px,1fr))";
  } else if (typeof node.columns === "number" && node.columns > 0) {
    gridStyle = "grid-template-columns:repeat(" + Math.min(12, Math.max(1, node.columns)) + ",1fr)";
  }
  const renderedChildren = (node.children || [])
    .map((c) => renderChild(c).value)
    .join("");
  return componentBody(
    renderComponent("canvas-grid", {
      id: node.id,
      gridClass: gapClass,
      gridStyle,
      children: componentBody(renderedChildren),
    }),
  );
}

function renderCanvasRow(
  node: CanvasRowNode,
  context: CanvasRendererContext,
  renderChild: (n: CanvasNode) => ComponentHTML,
): ComponentHTML {
  let rowClass = node.gap ? "canvas-row--" + node.gap : "canvas-row--compact";
  if (node.wrap) {
    rowClass += " canvas-row--wrap";
  }
  const renderedChildren = (node.children || [])
    .map((c) => renderChild(c).value)
    .join("");
  return componentBody(
    renderComponent("canvas-row", {
      id: node.id,
      rowClass,
      children: componentBody(renderedChildren),
    }),
  );
}

function renderCanvasSection(
  node: CanvasSectionNode,
  context: CanvasRendererContext,
  renderChild: (n: CanvasNode) => ComponentHTML,
): ComponentHTML {
  const isCollapsed = context.collapsedSections
    ? context.collapsedSections.has(node.id)
    : !!node.collapsed;
  const openAttr = isCollapsed ? "" : "open";
  const renderedChildren = (node.children || [])
    .map((c) => renderChild(c).value)
    .join("");
  return componentBody(
    renderComponent("canvas-section", {
      id: node.id,
      title: node.title || "",
      description: node.description || "",
      openAttr,
      children: componentBody(renderedChildren),
    }),
  );
}

function renderCanvasTabs(
  node: CanvasTabsNode,
  context: CanvasRendererContext,
  renderChild: (n: CanvasNode) => ComponentHTML,
): ComponentHTML {
  const tabs = node.tabs || [];
  const activeTabId =
    (context.activeTabsState && context.activeTabsState.get(node.id)) ||
    node.initial ||
    (tabs.length > 0 ? tabs[0].id : "");

  const renderedButtons = tabs
    .map((t) => {
      const isSelected = t.id === activeTabId;
      return renderComponent("canvas-tab-btn", {
        btnId: "btn-" + node.id + "-" + t.id,
        panelId: "panel-" + node.id + "-" + t.id,
        tabId: t.id,
        ownerId: node.id,
        label: t.label,
        selected: isSelected ? "true" : "false",
        activeClass: isSelected ? "tab-btn--active" : "",
      });
    })
    .join("");

  const renderedPanels = tabs
    .map((t) => {
      const isSelected = t.id === activeTabId;
      const childrenHtml = (t.children || [])
        .map((c) => renderChild(c).value)
        .join("");
      return renderComponent("canvas-tab-panel", {
        panelId: "panel-" + node.id + "-" + t.id,
        btnId: "btn-" + node.id + "-" + t.id,
        hiddenAttr: isSelected ? "" : "hidden",
        children: componentBody(childrenHtml),
      });
    })
    .join("");

  return componentBody(
    renderComponent("canvas-tabs", {
      id: node.id,
      tabButtons: componentBody(renderedButtons),
      tabPanels: componentBody(renderedPanels),
    }),
  );
}

function renderCanvasDivider(node: CanvasDividerNode): ComponentHTML {
  const spacingClass = node.spacing
    ? "canvas-divider--" + node.spacing
    : "canvas-divider--comfortable";
  return componentBody(
    renderComponent("canvas-divider", {
      id: node.id,
      spacingClass,
    }),
  );
}

function renderCanvasHeading(node: CanvasHeadingNode): ComponentHTML {
  const level = Math.min(6, Math.max(1, node.level || 2));
  return componentBody(
    renderComponent("canvas-heading", {
      id: node.id,
      level,
      text: node.text || "",
    }),
  );
}

function renderCanvasText(node: CanvasTextNode): ComponentHTML {
  const variantClass = node.variant
    ? "canvas-text--" + node.variant
    : "canvas-text--body";
  return componentBody(
    renderComponent("canvas-text", {
      id: node.id,
      variantClass,
      text: node.text || "",
    }),
  );
}

function renderCanvasMarkdown(node: CanvasMarkdownNode): ComponentHTML {
  const rendered = typeof renderMarkdown === "function"
    ? renderMarkdown(node.text)
    : componentEscape(node.text);
  return componentBody(
    renderComponent("canvas-text", {
      id: node.id,
      variantClass: "canvas-text--body markdown-body",
      text: componentBody(rendered),
    }),
  );
}

function renderCanvasCode(node: CanvasCodeNode): ComponentHTML {
  return componentBody(
    renderComponent("canvas-code", {
      id: node.id,
      language: node.language ? "language-" + node.language : "",
      code: node.text || "",
    }),
  );
}

function renderCanvasCallout(node: CanvasCalloutNode): ComponentHTML {
  return componentBody(
    renderComponent("canvas-callout", {
      id: node.id,
      tone: node.tone || "info",
      title: node.title || "",
      text: node.text || "",
    }),
  );
}

function renderCanvasMetric(node: CanvasMetricNode): ComponentHTML {
  return componentBody(renderMetricCard(node.label, node.value, node.note || ""));
}

function renderCanvasKeyValue(node: CanvasKeyValueNode): ComponentHTML {
  return componentBody(renderDefinitionRow(node.label, node.value, node.note));
}

function renderCanvasStatus(node: CanvasStatusNode): ComponentHTML {
  return componentBody(
    renderComponent("status-badge", {
      label: node.label,
      tone: node.tone || node.state || "unknown",
    }),
  );
}

function renderCanvasSummary(node: CanvasSummaryNode): ComponentHTML {
  const cards = (node.items || []).map((it) => ({
    label: it.label,
    value: it.value,
    tone: it.tone === "neutral" ? undefined : it.tone,
  }));
  return componentBody(
    renderComponent("canvas-summary", {
      id: node.id,
      items: componentBody(renderSummaryCards(cards)),
    }),
  );
}

function renderCanvasTable(
  node: CanvasTableNode,
  context: CanvasRendererContext,
): ComponentHTML {
  const rawData = context.resolveData(node.data);
  if (!Array.isArray(rawData)) {
    return componentBody(
      renderEmptyState(
        node.title || "Table",
        "Data not yet available or unresolvable",
      ),
    );
  }

  // Local sorting
  const sortInfo = context.tableSortState ? context.tableSortState.get(node.id) : undefined;
  const sorted = sortDataRows(rawData, sortInfo?.field, sortInfo?.direction || "asc");

  // Local paging
  const pageSize = Math.min(100, Math.max(1, node.pageSize || 25));
  const currentPage = (context.tablePageState && context.tablePageState.get(node.id)) || 1;
  const totalRows = sorted.length;
  const pageCount = Math.max(1, Math.ceil(totalRows / pageSize));
  const validPage = Math.min(Math.max(1, currentPage), pageCount);
  const start = (validPage - 1) * pageSize;
  const pageRows = sorted.slice(start, start + pageSize);

  // Headers
  const renderedHeaders = (node.columns || [])
    .map((col) => {
      let sortIndicator = "";
      if (node.sortable) {
        if (sortInfo && sortInfo.field === col.field) {
          sortIndicator = sortInfo.direction === "asc" ? " ▲" : " ▼";
        }
      }
      return renderComponent("canvas-table-header", {
        thClass: node.sortable ? "canvas-th-sortable" : "",
        ownerId: node.id,
        field: col.field,
        label: col.label,
        sortIndicator,
      });
    })
    .join("");

  // Rows
  const renderedRows = pageRows
    .map((row: any, rIdx) => {
      const renderedCells = (node.columns || [])
        .map((col) => {
          const val = row && typeof row === "object" ? row[col.field] : "";
          let formatted = val === null || val === undefined ? "—" : String(val);
          if (col.kind === "percent" && typeof val === "number") {
            formatted = (val * 100).toFixed(1) + "%";
          }
          return renderComponent("canvas-table-cell", {
            cellClass: col.kind ? "canvas-cell--" + col.kind : "",
            content: formatted,
          });
        })
        .join("");
      return renderComponent("canvas-table-row", {
        rowIndex: start + rIdx,
        cells: componentBody(renderedCells),
      });
    })
    .join("");

  // Pager footer
  let footerHtml = "";
  if (pageCount > 1) {
    footerHtml = renderPagination({
      label: node.title || "Table pages",
      summary: "Page " + validPage + " of " + pageCount + " · " + totalRows + " rows",
      previous: {
        attributes: {
          "data-canvas-action": "page",
          "data-canvas-owner": node.id,
          "data-canvas-page": String(validPage - 1),
        },
        disabled: validPage <= 1,
      },
      next: {
        attributes: {
          "data-canvas-action": "page",
          "data-canvas-owner": node.id,
          "data-canvas-page": String(validPage + 1),
        },
        disabled: validPage >= pageCount,
      },
    });
  }

  return componentBody(
    renderComponent("canvas-table", {
      id: node.id,
      title: node.title || "Data Table",
      searchControl: "",
      headers: componentBody(renderedHeaders),
      rows: componentBody(renderedRows),
      footer: componentBody(footerHtml),
    }),
  );
}

function renderCanvasJson(
  node: CanvasJsonNode,
  context: CanvasRendererContext,
): ComponentHTML {
  const rawData = context.resolveData(node.data);
  let panelHtml = "";
  if (typeof MuxlioJson !== "undefined" && typeof MuxlioJson.jsonPanelFromValue === "function") {
    const panel = MuxlioJson.jsonPanelFromValue(rawData, componentEscape, {
      title: node.title || "JSON Document",
      statePath: "canvas:" + node.id,
    });
    panelHtml = panel.html;
  } else {
    panelHtml = renderComponent("canvas-code", {
      id: "raw-" + node.id,
      language: "json",
      code: JSON.stringify(rawData ?? null, null, 2),
    });
  }
  return componentBody(panelHtml);
}

function renderCanvasBarChart(
  node: CanvasBarChartNode,
  context: CanvasRendererContext,
): ComponentHTML {
  const rawData = context.resolveData(node.data);
  if (!Array.isArray(rawData) || rawData.length === 0) {
    return componentBody(
      renderEmptyState(node.title || "Chart", "No chart data available"),
    );
  }

  // Bound chart points to 2,000 max
  const bounded = rawData.slice(0, 2000);
  const selectedKey = context.selectedPointState
    ? context.selectedPointState.get(node.id)
    : node.selectedKey;

  const numericVals = bounded.map((it: any) => {
    const v = it && typeof it === "object" ? Number(it[node.valueField]) : 0;
    return Number.isFinite(v) ? v : 0;
  });
  const maxVal = Math.max(...numericVals, 1);

  const renderedBars = bounded
    .map((it: any) => {
      const label = it && typeof it === "object" ? String(it[node.categoryField] ?? "") : "";
      const rawVal = it && typeof it === "object" ? it[node.valueField] : 0;
      const numVal = Number(rawVal);
      const val = Number.isFinite(numVal) ? numVal : 0;
      const pct = maxVal > 0 ? Math.max(0, Math.min(100, (val / maxVal) * 100)) : 0;
      let displayVal = String(rawVal);
      if (node.format === "percent" && Number.isFinite(numVal)) {
        displayVal = (numVal * 100).toFixed(1) + "%";
      }

      const isSelected = selectedKey === label;
      const barStyle = "width:" + pct.toFixed(1) + "%;background:var(--teal)";

      return renderComponent("canvas-bar-item", {
        ownerId: node.id,
        pointKey: label,
        label,
        val: displayVal,
        barStyle,
        selectedClass: isSelected ? "canvas-bar-row--selected" : "",
      });
    })
    .join("");

  return componentBody(
    renderComponent("canvas-bar-chart", {
      id: node.id,
      title: node.title || "",
      bars: componentBody(renderedBars),
    }),
  );
}

function renderCanvasSparkline(
  node: CanvasSparklineNode,
  context: CanvasRendererContext,
): ComponentHTML {
  const rawData = context.resolveData(node.data);
  if (!Array.isArray(rawData) || rawData.length === 0) {
    return componentBody(
      renderEmptyState(node.title || "Sparkline", "No data points"),
    );
  }

  const bounded = rawData.slice(0, 200);
  const values = bounded.map((it: any) => {
    if (typeof it === "number") return it;
    if (it && typeof it === "object" && node.valueField) {
      const v = Number(it[node.valueField]);
      return Number.isFinite(v) ? v : 0;
    }
    return 0;
  });
  const maxVal = Math.max(...values, 1);

  const renderedBars = values
    .map((v, i) => {
      const h = maxVal > 0 ? (v / maxVal) * 40 : 0;
      const label = (node.labelField && bounded[i] && (bounded[i] as any)[node.labelField]) || String(i + 1);
      const colStyle = "height:" + Math.max(2, Math.round(h)) + "px";
      const title = label + ": " + v;
      return renderComponent("canvas-spark-bar", {
        colStyle,
        title,
      });
    })
    .join("");

  return componentBody(
    renderComponent("canvas-sparkline", {
      id: node.id,
      title: node.title || "",
      ariaLabel: node.title || "Trend",
      bars: componentBody(renderedBars),
      labels: "",
    }),
  );
}

function renderCanvasSelect(
  node: CanvasControlSelect,
  context: CanvasRendererContext,
): ComponentHTML {
  const currentVal =
    context.state.get(node.id) !== undefined
      ? context.state.get(node.id)
      : node.initial !== undefined
        ? node.initial
        : "";

  const optionsHtml = (node.options || [])
    .map((opt) => {
      const isSelected = String(opt.value) === String(currentVal);
      return renderComponent("canvas-select-option", {
        value: opt.value,
        label: opt.label,
        selectedAttr: isSelected ? "selected" : "",
      });
    })
    .join("");

  return componentBody(
    renderComponent("canvas-control-select", {
      id: "ctrl-" + node.id,
      label: node.label,
      stateId: node.id,
      options: componentBody(optionsHtml),
    }),
  );
}

function renderCanvasToggle(
  node: CanvasControlToggle,
  context: CanvasRendererContext,
): ComponentHTML {
  const currentVal =
    context.state.get(node.id) !== undefined
      ? Boolean(context.state.get(node.id))
      : Boolean(node.initial);

  return componentBody(
    renderComponent("canvas-control-toggle", {
      id: "ctrl-" + node.id,
      label: node.label,
      stateId: node.id,
      checkedAttr: currentVal ? "checked" : "",
    }),
  );
}

function renderCanvasSearch(
  node: CanvasControlSearch,
  context: CanvasRendererContext,
): ComponentHTML {
  const currentVal =
    context.state.get(node.id) !== undefined
      ? String(context.state.get(node.id))
      : node.initial || "";

  return componentBody(
    renderComponent("canvas-control-search", {
      id: "ctrl-" + node.id,
      label: node.label || "",
      placeholder: node.placeholder || "Search...",
      stateId: node.id,
      value: currentVal,
    }),
  );
}

function renderCanvasArtifactLink(
  node: CanvasArtifactLinkNode,
  context: CanvasRendererContext,
): ComponentHTML {
  let href = "#";
  if (context.runId && context.activationId && node.port) {
    href =
      "/artifacts/view/" +
      encodeURIComponent(context.runId) +
      "/" +
      encodeURIComponent(context.activationId) +
      "/" +
      encodeURIComponent(node.port);
  }
  return componentBody(
    renderComponent("canvas-artifact-link", {
      id: node.id,
      href,
      label: node.label,
      description: node.description || "",
    }),
  );
}
