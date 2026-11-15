// The artifact page shares the renderer, but has no workbench state or polling.
function uiFetch(path: string, opts?: RequestInit) {
  return fetch(path, opts);
}

async function api(path: string, opts?: RequestInit) {
  const response = await uiFetch(path, opts);
  const data = await response.json().catch(() => ({}));
  if (!response.ok)
    throw new Error(data.message || data.error || `HTTP ${response.status}`);
  return data;
}

function message(text: string, error = false) {
  document.getElementById("message")!.innerHTML = text
    ? renderNotice(text, { error })
    : "";
}
