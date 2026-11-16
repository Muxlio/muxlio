// Bounded local interaction engine: ephemeral state store and scoped event dispatch.

class CanvasInteractionStore {
  state = new Map<string, CanvasStateValue>();
  controlTypes = new Map<string, CanvasControl["type"]>();
  activeTabs = new Map<string, string>();
  tableSort = new Map<string, { field: string; direction: "asc" | "desc" }>();
  tablePages = new Map<string, number>();
  selectedPoints = new Map<string, string>();
  collapsedSections = new Set<string>();
  listeners: Array<() => void> = [];

  constructor(doc?: CanvasDocument) {
    if (doc) {
      this.initFromDocument(doc);
    }
  }

  initFromDocument(doc: CanvasDocument): void {
    this.state.clear();
    this.controlTypes.clear();
    this.activeTabs.clear();
    this.tableSort.clear();
    this.tablePages.clear();
    this.selectedPoints.clear();
    this.collapsedSections.clear();

    // A select without `initial` displays its first option, so filters must
    // start from that option too.
    const seedControl = (id: string, ctrl: CanvasControl) => {
      this.controlTypes.set(id, ctrl.type);
      if (ctrl.initial !== undefined) this.state.set(id, ctrl.initial);
      else if (ctrl.type === "select" && ctrl.options?.length)
        this.state.set(id, ctrl.options[0].value);
    };
    if (doc.controls) {
      for (const [key, ctrl] of Object.entries(doc.controls)) {
        seedControl(ctrl.id || key, ctrl);
      }
    }

    const walk = (node: CanvasNode) => {
      if (!node || typeof node !== "object") return;
      if (node.type === "select" || node.type === "toggle" || node.type === "search") {
        seedControl(node.id, node as unknown as CanvasControl);
      } else if (node.type === "tabs") {
        const tn = node as CanvasTabsNode;
        const tabs = tn.tabs || [];
        const initial = tabs.find((tab) => tab.id === tn.initial) || tabs[0];
        if (initial) this.activeTabs.set(node.id, initial.id);
      } else if (node.type === "section") {
        const sn = node as CanvasSectionNode;
        if (sn.collapsed) {
          this.collapsedSections.add(node.id);
        }
      }

      if ("children" in node && Array.isArray((node as any).children)) {
        for (const child of (node as any).children) {
          walk(child);
        }
      }
      if ("tabs" in node && Array.isArray((node as any).tabs)) {
        for (const tab of (node as any).tabs) {
          if (Array.isArray(tab.children)) {
            for (const child of tab.children) {
              walk(child);
            }
          }
        }
      }
    };

    if (doc.content) {
      walk(doc.content);
    }
  }

  subscribe(listener: () => void): () => void {
    this.listeners.push(listener);
    return () => {
      const idx = this.listeners.indexOf(listener);
      if (idx >= 0) this.listeners.splice(idx, 1);
    };
  }

  notify(): void {
    for (const listener of this.listeners) {
      listener();
    }
  }

  dispatch(event: CanvasEvent): void {
    switch (event.type) {
      case "setState":
        this.state.set(event.stateId, event.value);
        this.notify();
        break;

      case "selectTab":
        this.activeTabs.set(event.componentId, event.tabId);
        this.notify();
        break;

      case "sortTable": {
        const current = this.tableSort.get(event.componentId);
        if (current && current.field === event.field) {
          const nextDir = current.direction === "asc" ? "desc" : "asc";
          this.tableSort.set(event.componentId, {
            field: event.field,
            direction: nextDir,
          });
        } else {
          this.tableSort.set(event.componentId, {
            field: event.field,
            direction: "asc",
          });
        }
        // Reset page to 1 on sort change
        this.tablePages.set(event.componentId, 1);
        this.notify();
        break;
      }

      case "setPage":
        this.tablePages.set(event.componentId, event.page);
        this.notify();
        break;

      case "selectChartPoint": {
        const current = this.selectedPoints.get(event.componentId);
        if (current === event.key) {
          this.selectedPoints.delete(event.componentId);
        } else {
          this.selectedPoints.set(event.componentId, event.key);
        }
        this.notify();
        break;
      }

      case "toggleSection": {
        if (this.collapsedSections.has(event.componentId)) {
          this.collapsedSections.delete(event.componentId);
        } else {
          this.collapsedSections.add(event.componentId);
        }
        this.notify();
        break;
      }
    }
  }
}
