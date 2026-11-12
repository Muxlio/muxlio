/** Shared shell behavior. Views remain independent of executor providers. */
(() => {
  const shell = document.querySelector<HTMLElement>(".shell");
  const menu = document.getElementById("menuToggle");
  const rail = document.getElementById("workspaceNavigation");
  const compact = matchMedia("(max-width: 680px)");
  const setMenu = (open: boolean) => {
    shell?.classList.toggle("menu-open", open);
    menu?.setAttribute("aria-expanded", String(open));
    if (rail) rail.inert = compact.matches && !open;
  };
  setMenu(false);
  menu?.addEventListener("click", () =>
    setMenu(menu.getAttribute("aria-expanded") !== "true"),
  );
  compact.addEventListener("change", () => setMenu(false));
  rail?.addEventListener("click", (event) => {
    if ((event.target as Element).closest("a[href]") && compact.matches) {
      setMenu(false);
      menu?.focus();
    }
  });

  // Roving focus follows the existing click handlers, including dynamically
  // rendered tab lists. Selection stays owned by the view.
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && shell?.classList.contains("menu-open")) {
      setMenu(false);
      menu?.focus();
    }
    const target = event.target as HTMLElement;
    const list = target.closest("[role=tablist]");
    if (!list || target.getAttribute("role") !== "tab") return;
    const tabs = [...list.querySelectorAll<HTMLButtonElement>("[role=tab]")];
    const index = tabs.indexOf(target as HTMLButtonElement);
    const next =
      event.key === "ArrowRight"
        ? (index + 1) % tabs.length
        : event.key === "ArrowLeft"
          ? (index - 1 + tabs.length) % tabs.length
          : event.key === "Home"
            ? 0
            : event.key === "End"
              ? tabs.length - 1
              : -1;
    if (next < 0) return;
    event.preventDefault();
    tabs[next]?.click();
    tabs[next]?.focus();
  });

  for (const list of document.querySelectorAll("[role=tablist]")) {
    const syncTabs = () =>
      list.querySelectorAll<HTMLElement>("[role=tab]").forEach((tab) => {
        tab.tabIndex = tab.getAttribute("aria-selected") === "true" ? 0 : -1;
      });
    syncTabs();
    new MutationObserver(syncTabs).observe(list, {
      subtree: true,
      attributes: true,
      attributeFilter: ["aria-selected"],
    });
  }

  const modalOpeners = new Map<HTMLElement, HTMLElement>();
  const focusable = (root: HTMLElement) =>
    [
      ...root.querySelectorAll<HTMLElement>(
        'a[href],button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex="0"]',
      ),
    ].filter(
      (element) =>
        element.getClientRects().length && !element.closest("[hidden]"),
    );
  const modals = [...document.querySelectorAll<HTMLElement>(".modal")];
  for (const modal of modals) {
    new MutationObserver(() => {
      if (!modal.hidden) {
        if (!modalOpeners.has(modal))
          modalOpeners.set(modal, document.activeElement as HTMLElement);
        if (!modal.contains(document.activeElement))
          focusable(modal)[0]?.focus();
      } else {
        const opener = modalOpeners.get(modal);
        modalOpeners.delete(modal);
        if (opener?.isConnected) opener.focus();
      }
    }).observe(modal, { attributes: true, attributeFilter: ["hidden"] });
    // Capture the opener before view handlers move focus into the dialog.
    document.addEventListener(
      "click",
      () => {
        if (modal.hidden)
          modalOpeners.set(modal, document.activeElement as HTMLElement);
      },
      true,
    );
  }
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Tab") return;
    const modal = [...modals].reverse().find((element) => !element.hidden);
    if (!modal) return;
    const controls = focusable(modal);
    const first = controls[0],
      last = controls.at(-1);
    if (!first) return;
    if (
      event.shiftKey &&
      (document.activeElement === first ||
        !modal.contains(document.activeElement))
    ) {
      event.preventDefault();
      last?.focus();
    } else if (
      !event.shiftKey &&
      (document.activeElement === last ||
        !modal.contains(document.activeElement))
    ) {
      event.preventDefault();
      first.focus();
    }
  });
  const context = document.getElementById("topContext");
  if (context) {
    const updateTitle = () => {
      document.title = `${context.textContent || "Workspace"} · Muxlio`;
    };
    new MutationObserver(updateTitle).observe(context, {
      childList: true,
      subtree: true,
      characterData: true,
    });
    updateTitle();
  }
})();
