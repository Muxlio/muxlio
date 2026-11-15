(() => {
  const parts = location.pathname.replace(/\/$/, "").split("/").slice(3);
  const [runId, activationId, port] = parts.map(decodeURIComponent);
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
