(() => {
  const parts = location.pathname.replace(/\/$/, "").split("/").slice(3);
  let decoded: string[];
  try {
    decoded = parts.map(decodeURIComponent);
  } catch {
    const container = document.getElementById("canvasView");
    if (container) {
      container.hidden = false;
      container.innerHTML = canvasFallbackCard(
        "Invalid Artifact Link",
        "This artifact link is malformed and cannot be opened.",
      );
    }
    return;
  }
  const [runId, activationId, port] = decoded;
  const back = document.getElementById("canvasRunLink") as HTMLAnchorElement;
  back.href = "/runs/" + encodeURIComponent(runId);
  const query = new URLSearchParams(location.search);
  const seq = Number(query.get("seq"));
  loadCanvasPage(
    runId,
    activationId,
    port,
    query.get("attempt") || undefined,
    seq > 0 ? seq : undefined,
  ).then(() => {
    document.title =
      (activeCanvasDoc?.title || activeCanvasInfo.path || "Artifact") +
      " · Muxlio";
  });
})();
