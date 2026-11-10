/* Safe JSON viewing for coordinator output. Values are escaped; content is never executed. */
(function () {
  'use strict';

  const BIGINT_MARK = '__muxlioJsonBigint:';
  const CHILD_LIMIT = 48;
  const OPEN_DEPTH = 2;
  const MAX_DEPTH = 40;
  const PREVIEW_BYTES = 4096;

  function isJsonMime(mime) {
    if (!mime || typeof mime !== 'string') return false;
    const type = mime.split(';')[0].trim().toLowerCase();
    return type === 'application/json' || type === 'text/json' || /\+json$/.test(type);
  }

  function jsonPathLooksJson(path) {
    return typeof path === 'string' && /\.json$/i.test(path);
  }

  function isBigIntNode(value) {
    return value && typeof value === 'object' && value.__muxlioJsonBigint === true && typeof value.value === 'string';
  }

  function jsonValueType(value) {
    if (value === null) return 'null';
    if (isBigIntNode(value)) return 'integer';
    if (Array.isArray(value)) return 'array';
    if (typeof value === 'object') return 'object';
    return typeof value;
  }

  function transformBigIntLiterals(text, marker = BIGINT_MARK) {
    let out = '';
    let i = 0;
    let inString = false;
    let escaped = false;
    while (i < text.length) {
      const ch = text[i];
      if (inString) {
        out += ch;
        if (escaped) escaped = false;
        else if (ch === '\\') escaped = true;
        else if (ch === '"') inString = false;
        i++;
        continue;
      }
      if (ch === '"') {
        inString = true;
        out += ch;
        i++;
        continue;
      }
      if (ch === '-' || (ch >= '0' && ch <= '9')) {
        const start = i;
        if (text[i] === '-') i++;
        while (i < text.length && text[i] >= '0' && text[i] <= '9') i++;
        const intPart = text.slice(start, i);
        if (i < text.length && text[i] === '.') {
          while (i < text.length && ((text[i] >= '0' && text[i] <= '9') || text[i] === '.' || text[i] === 'e' || text[i] === 'E' || text[i] === '+' || text[i] === '-')) i++;
          out += text.slice(start, i);
          continue;
        }
        if (i < text.length && (text[i] === 'e' || text[i] === 'E')) {
          i++;
          if (text[i] === '+' || text[i] === '-') i++;
          while (i < text.length && text[i] >= '0' && text[i] <= '9') i++;
          out += text.slice(start, i);
          continue;
        }
        const digits = intPart.replace(/^-/, '');
        const unsafe = digits.length > 15 || Math.abs(Number(intPart)) > Number.MAX_SAFE_INTEGER;
        out += unsafe ? `"${marker}${intPart}"` : intPart;
        continue;
      }
      out += ch;
      i++;
    }
    return out;
  }

  function parseJsonDocument(text) {
    if (text == null) return { kind: 'unavailable' };
    const raw = String(text);
    const trimmed = raw.trim();
    if (!trimmed) return { kind: 'empty', raw };
    try {
      // Pick a sentinel absent from the source. A fixed sentinel would corrupt
      // a valid user string that happened to start with that text.
      let marker = BIGINT_MARK;
      while (raw.includes(marker)) marker += '_';
      const prepared = transformBigIntLiterals(trimmed, marker);
      const value = JSON.parse(prepared, (key, val) => {
        if (typeof val === 'string' && val.startsWith(marker)) {
          return { __muxlioJsonBigint: true, value: val.slice(marker.length) };
        }
        return val;
      });
      return { kind: 'value', value, raw };
    } catch (error) {
      return { kind: 'error', raw, message: error.message || 'Invalid JSON' };
    }
  }

  function scalarText(value) {
    if (isBigIntNode(value)) return value.value;
    const type = jsonValueType(value);
    if (type === 'string') return JSON.stringify(value);
    if (type === 'null') return 'null';
    return String(value);
  }

  function jsonTreeMarkup(value, key, depth, statePath, escape, opts = {}) {
    const esc = escape;
    const label = esc(key);
    const type = jsonValueType(value);
    const openDepth = opts.openDepth ?? OPEN_DEPTH;
    const childLimit = opts.childLimit ?? CHILD_LIMIT;
    const maxDepth = opts.maxDepth ?? MAX_DEPTH;

    if (depth >= maxDepth) {
      return `<div class="json-row json-truncated"><span class="json-key">${label}</span><span class="json-muted" role="note">Depth limit (${maxDepth})</span></div>`;
    }

    if (type === 'object' || type === 'array') {
      const entries = type === 'array' ? value.map((v, i) => [String(i), v]) : Object.entries(value);
      const count = entries.length;
      if (!count) {
        return `<div class="json-row json-empty"><span class="json-key">${label}</span><span class="json-type json-muted">${type} · empty</span></div>`;
      }
      const visible = entries.slice(0, childLimit);
      const hidden = count - visible.length;
      const open = depth < openDepth;
      const children = visible.map(([k, v]) => jsonTreeMarkup(v, k, depth + 1, `${statePath}/${k}`, escape, opts)).join('');
      const more = hidden
        ? `<details class="json-more"><summary class="json-muted">Show ${hidden} more ${type === 'array' ? 'items' : 'properties'}</summary><div class="json-children json-tree">${entries
            .slice(childLimit)
            .map(([k, v]) => jsonTreeMarkup(v, k, depth + 1, `${statePath}/${k}`, escape, opts))
            .join('')}</div></details>`
        : '';
      return `<div class="json-row"><details class="json-branch" data-preserve-key="${esc(statePath)}"${open ? ' open' : ''}><summary class="json-summary"><span class="json-key">${label}</span><span class="json-type json-muted">${type} · ${count}</span></summary><div class="json-children json-tree">${children}${more}</div></details></div>`;
    }

    if (type === 'null') {
      return `<div class="json-row json-scalar"><span class="json-key">${label}</span><span class="json-value json-null">null</span><span class="json-type json-muted">null</span></div>`;
    }

    const cls = type === 'integer' ? 'integer' : type;
    return `<div class="json-row json-scalar"><span class="json-key">${label}</span><span class="json-value ${esc(cls)}">${esc(scalarText(value))}</span><span class="json-type json-muted">${esc(type)}</span></div>`;
  }

  function jsonToolsMarkup(copyLabel, copyMode, copyTarget, escape) {
    const esc = escape;
    return `<div class="json-tools"><button type="button" class="btn ghost" data-copy-json="${esc(copyMode)}"${copyTarget ? ` data-copy-target="${esc(copyTarget)}"` : ''}>${esc(copyLabel)}</button></div>`;
  }

  function jsonStatusMarkup(note, role = 'note') {
    if (!note) return '';
    return `<p class="json-status" role="${role}">${note}</p>`;
  }

  function jsonRawMarkup(text, escape, { truncated = false, error = false, note = '' } = {}) {
    const banner = truncated
      ? jsonStatusMarkup('Preview shows the first part of the file only. Open the artifact to load the complete JSON.', 'status')
      : error
        ? jsonStatusMarkup(note || 'This content is not valid JSON. Showing the raw text.', 'alert')
        : note
          ? jsonStatusMarkup(note)
          : '';
    const cls = `json-raw output-raw${truncated ? ' preview' : ''}${error ? ' json-raw-error' : ''}`;
    return `${banner}<pre class="${cls}">${escape(text)}</pre>`;
  }

  function jsonPanelFromDocument(doc, escape, { title = 'Value', statePath = 'json', copyMode = 'full', copyLabel = 'Copy JSON' } = {}) {
    if (doc.kind === 'unavailable') {
      return `${jsonStatusMarkup('Output unavailable.', 'status')}`;
    }
    if (doc.kind === 'empty') {
      return `${jsonStatusMarkup('Empty document.', 'status')}${jsonRawMarkup('', escape)}`;
    }
    if (doc.kind === 'error') {
      return `${jsonToolsMarkup('Copy text', 'full', statePath, escape)}${jsonRawMarkup(doc.raw, escape, { error: true, note: doc.message })}`;
    }
    return `${jsonToolsMarkup(copyLabel, copyMode, statePath, escape)}<div class="json-tree" data-json-root="${escape(statePath)}">${jsonTreeMarkup(doc.value, title, 0, statePath, escape)}</div>`;
  }

  function jsonPanelFromText(text, escape, opts = {}) {
    const doc = parseJsonDocument(text);
    return { html: jsonPanelFromDocument(doc, escape, opts), doc, raw: doc.raw ?? String(text ?? '') };
  }

  function jsonPanelFromValue(value, escape, opts = {}) {
    if (value === undefined) return { html: jsonStatusMarkup('Output unavailable.', 'status'), doc: { kind: 'unavailable' }, raw: '' };
    if (value === null) return { html: jsonPanelFromDocument({ kind: 'value', value: null, raw: 'null' }, escape, opts), doc: { kind: 'value', value: null, raw: 'null' }, raw: 'null' };
    const raw = typeof value === 'string' ? value : JSON.stringify(value, null, 2);
    return jsonPanelFromText(raw, escape, opts);
  }

  function jsonArtifactPreview(ref, escape) {
    const mime = typeof (ref.mime || ref.mimeType) === 'string' ? ref.mime || ref.mimeType : '';
    if (!isJsonMime(mime) && !jsonPathLooksJson(ref.path)) return '';
    if (ref.data == null && !ref.digest) {
      return `<div class="artifact-no-preview" role="status"><span aria-hidden="true">◇</span><div><strong>Preview unavailable</strong><small>No inline data recorded for this artifact.</small></div></div>`;
    }
    if (ref.data == null) return '';
    const raw = typeof ref.data === 'string' ? ref.data : JSON.stringify(ref.data, null, 2);
    const truncated = raw.length > PREVIEW_BYTES;
    const previewText = truncated ? raw.slice(0, PREVIEW_BYTES) : raw;
    const doc = parseJsonDocument(previewText);
    const target = `artifact-preview:${ref.path || 'json'}`;
    let body;
    let copyNote;
    if (truncated && doc.kind === 'error') {
      body = jsonRawMarkup(previewText, escape, { truncated: true });
      copyNote = 'preview';
    } else if (doc.kind === 'error') {
      body = jsonRawMarkup(previewText, escape, { error: true, note: doc.message });
      copyNote = 'preview';
    } else {
      body = `<div class="json-tree">${jsonTreeMarkup(doc.value, 'Value', 0, target, escape, { openDepth: 1 })}</div>`;
      copyNote = truncated ? 'preview' : 'full';
    }
    const copyLabel = copyNote === 'preview' ? 'Copy preview' : 'Copy JSON';
    return `<details class="artifact-preview"><summary>${truncated ? 'JSON preview (truncated)' : 'JSON preview'}</summary>${truncated ? jsonStatusMarkup('Preview shows the first 4096 bytes only.', 'status') : ''}${jsonToolsMarkup(copyLabel, copyNote, target, escape)}${body}</details>`;
  }

  function jsonPreviewPayload(ref) {
    if (ref?.data == null) return null;
    const raw = typeof ref.data === 'string' ? ref.data : JSON.stringify(ref.data, null, 2);
    const truncated = raw.length > PREVIEW_BYTES;
    return { raw, preview: truncated ? raw.slice(0, PREVIEW_BYTES) : raw, truncated };
  }

  function stringifyForCopy(value, rawText) {
    if (typeof rawText === 'string' && rawText.length) return rawText;
    if (value === undefined) return '';
    if (typeof value === 'string') return value;
    return JSON.stringify(
      value,
      (key, val) => (isBigIntNode(val) ? val.value : val),
      2,
    );
  }

  const root = typeof globalThis !== 'undefined' ? globalThis : typeof self !== 'undefined' ? self : this;
  root.MuxlioJson = {
    PREVIEW_BYTES,
    isJsonMime,
    jsonPathLooksJson,
    jsonValueType,
    parseJsonDocument,
    jsonTreeMarkup,
    jsonPanelFromText,
    jsonPanelFromValue,
    jsonPanelFromDocument,
    jsonArtifactPreview,
    jsonPreviewPayload,
    jsonRawMarkup,
    stringifyForCopy,
    scalarText,
  };
  if (typeof window !== 'undefined' && window !== root) window.MuxlioJson = root.MuxlioJson;
})();
