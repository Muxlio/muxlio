// Clipboard access on plain HTTP needs the browser's selection-based fallback.
function copyCanvasLinkFallback(text: string): boolean {
  const focused = document.activeElement as HTMLElement | null;
  const field = document.createElement("textarea");
  field.value = text;
  field.readOnly = true;
  field.style.cssText =
    "position:fixed;top:0;left:0;opacity:0;pointer-events:none";
  document.body.appendChild(field);
  field.select();
  field.setSelectionRange(0, text.length);
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    field.remove();
    focused?.focus({ preventScroll: true });
  }
}

async function copyCanvasLink(): Promise<void> {
  const text = location.href;
  try {
    if (!navigator.clipboard?.writeText)
      throw new Error("Clipboard unavailable");
    await navigator.clipboard.writeText(text);
  } catch {
    if (!copyCanvasLinkFallback(text))
      throw new Error(
        "Unable to copy automatically. Copy the URL from the address bar.",
      );
  }
}

function canvasPdfFilename(title: string): string {
  const stem = title.replace(/(?:\.canvas)?\.(?:json|md|txt|pdf)$/i, "");
  const slug = stem
    .normalize("NFKD")
    .replace(/\p{M}/gu, "")
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, "-")
    .replace(/^-+|-+$/g, "");
  const shortened =
    slug.length > 96 ? slug.slice(0, 97).replace(/-[^-]*$/, "") : slug;
  return (shortened || "canvas-report") + ".pdf";
}

let canvasPdfLibrary: Promise<void> | null = null;
function loadCanvasPdfLibrary(): Promise<void> {
  if (!canvasPdfLibrary) {
    canvasPdfLibrary = new Promise<void>((resolve, reject) => {
      const script = document.createElement("script");
      script.src = "/dist/canvas-pdf.js";
      script.onload = () => resolve();
      script.onerror = () => {
        script.remove();
        canvasPdfLibrary = null;
        reject(new Error("Unable to load PDF export. Please try again."));
      };
      document.head.appendChild(script);
    });
  }
  return canvasPdfLibrary;
}

function canvasPdfHTML(root: HTMLElement): string {
  const copy = root.cloneNode(true) as HTMLElement;
  copy.hidden = false;
  copy
    .querySelectorAll(
      "[hidden],button,input,select,.canvas-control,.canvas-table-footer,.tab-bar,.canvas-link-card",
    )
    .forEach((element) => element.remove());
  // Charts use CSS bars on screen. Preserve them as vector graphics in the PDF.
  for (const chart of copy.querySelectorAll<HTMLElement>(".canvas-bars")) {
    const rows = [...chart.querySelectorAll<HTMLElement>(".canvas-bar-row")];
    const graphics: string[] = [];
    for (let start = 0; start < rows.length; start += 20) {
      const group = rows.slice(start, start + 20);
      const bars = group
        .map((row, i) => {
          const label = componentEscape(
            row.querySelector(".abc-label")?.textContent || "",
          );
          const value = componentEscape(
            row.querySelector(".abc-val")?.textContent || "",
          );
          const percent = parseFloat(
            (row.querySelector(".abc-bar") as HTMLElement)?.style.width || "0",
          );
          const width = Math.max(0, Math.min(100, percent)) * 2.6;
          const y = i * 24;
          return renderViewTemplate("canvas/pdf-bar", [
            viewValue(y + 14),
            viewValue(label),
            viewValue(y + 3),
            viewValue(width),
            viewValue(value),
          ]);
        })
        .join("");
      graphics.push(
        renderViewTemplate("canvas/pdf-chart", [
          viewValue(group.length * 24),
          viewValue(bars),
        ]),
      );
    }
    chart.innerHTML = graphics.join("");
  }
  // Screen layout and dark-theme colors do not belong in the printed document.
  copy
    .querySelectorAll<HTMLElement>("[style]")
    .forEach((element) => element.removeAttribute("style"));
  for (const link of copy.querySelectorAll<HTMLAnchorElement>("a[href]")) {
    try {
      link.href = new URL(link.getAttribute("href")!, location.href).href;
    } catch {
      link.replaceWith(document.createTextNode(link.textContent || ""));
    }
  }
  // Move children so nested <details> in the static list stay attached.
  for (const details of copy.querySelectorAll("details")) {
    const section = document.createElement("div");
    section.append(...details.childNodes);
    details.replaceWith(section);
  }
  return copy.innerHTML;
}

async function downloadCanvasPdf(): Promise<void> {
  const root = document.getElementById("canvasRoot");
  if (!root) throw new Error("Output is not ready to download.");
  const title =
    activeCanvasDoc?.title || activeCanvasInfo.path || "Canvas report";
  const html = canvasPdfHTML(root);
  await loadCanvasPdfLibrary();
  const logoResponse = await uiFetch("/logo.png");
  if (!logoResponse.ok)
    throw new Error("Unable to load the PDF logo. Please try again.");
  const logo = await canvasBlobDataURL(await logoResponse.blob());
  const converter = (window as any).htmlToPdfmake;
  const content = converter(html, {
    window,
    defaultStyles: {
      h1: { fontSize: 21, bold: true, margin: [0, 0, 0, 14] },
      h2: { fontSize: 15, bold: true, margin: [0, 15, 0, 8] },
      h3: { fontSize: 12, bold: true, margin: [0, 12, 0, 6] },
      p: { margin: [0, 0, 0, 8] },
      a: { color: "#14615b", decoration: "underline" },
      th: { bold: true, fillColor: "#eef3f4" },
    },
  });
  if (root.querySelector("h1")?.textContent !== title)
    content.unshift({
      text: title,
      fontSize: 21,
      bold: true,
      margin: [0, 0, 0, 14],
    });
  const pdf = (window as any).pdfMake.createPdf({
    info: { title, creator: "Muxlio" },
    pageSize: "A4",
    pageMargins: [44, 44, 44, 48],
    defaultStyle: { fontSize: 9.5, lineHeight: 1.3, color: "#263547" },
    content,
    footer: (page: number, pages: number) => ({
      columns: [
        page === pages
          ? {
              columns: [
                { image: logo, width: 16, height: 16 },
                {
                  text: "Muxlio",
                  bold: true,
                  fontSize: 9,
                  color: "#334155",
                  margin: [6, 3, 0, 0],
                },
              ],
              width: "*",
            }
          : { text: "", width: "*" },
        { text: `${page} / ${pages}`, alignment: "right", width: "auto" },
      ],
      margin: [44, 12, 44, 0],
      fontSize: 8,
      color: "#64748b",
    }),
  });
  // Materialize and validate bytes before handing them to the browser's download
  // manager. A data URL also works in embedded browsers that cannot resolve blob URLs.
  const blob: Blob = await pdf.getBlob();
  const header = await blob.slice(0, 5).text();
  const tail = await blob.slice(-32).text();
  if (!blob.size || header !== "%PDF-" || !tail.includes("%%EOF"))
    throw new Error("The generated PDF is incomplete. Please try again.");
  const dataURL = await canvasBlobDataURL(blob);
  const link = document.createElement("a");
  link.href = dataURL;
  link.download = canvasPdfFilename(title);
  link.hidden = true;
  document.body.appendChild(link);
  link.click();
  setTimeout(() => link.remove(), 60000);
}

function canvasBlobDataURL(blob: Blob): Promise<string> {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as string);
    reader.onerror = () =>
      reject(new Error("Unable to prepare the PDF download."));
    reader.readAsDataURL(blob);
  });
}
