import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const jsonSource = readFileSync(new URL('./js/json.js', import.meta.url), 'utf8');
const inspectorChunk = readFileSync(new URL('./js/inspector.js', import.meta.url), 'utf8');
const helperStart = inspectorChunk.indexOf('const PREVIEW_LIMIT');
const helperEnd = inspectorChunk.indexOf('let currentRunInput,currentRunOutput;');
const helperSource = inspectorChunk.slice(helperStart, helperEnd + 'let currentRunInput,currentRunOutput;'.length);

const ctx = vm.createContext({ esc, TextEncoder, window: {}, document: { getElementById: () => null, addEventListener() {} } });
vm.runInContext(`${jsonSource};const MuxlioJson=window.MuxlioJson;\n${helperSource}`, ctx);
const Json = ctx.window.MuxlioJson;

const big = '9223372036854775807';
const parsed = Json.parseJsonDocument(`{"id":${big},"ok":false,"count":0,"label":"test"}`);
assert.equal(parsed.kind, 'value');
assert.equal(parsed.value.id.__muxlioJsonBigint, true);
assert.equal(parsed.value.id.value, big, 'large integers stay exact');
assert.equal(parsed.value.ok, false, 'boolean false preserved');
assert.equal(parsed.value.count, 0, 'zero preserved');
assert.equal(parsed.value.label, 'test');
const markerLike = '__muxlioJsonBigint:not-a-number';
const markerDoc = Json.parseJsonDocument(JSON.stringify({ text: markerLike, nested: ['__muxlioJsonBigint:'] }));
assert.equal(markerDoc.kind, 'value');
assert.equal(markerDoc.value.text, markerLike, 'marker-like user strings remain strings');
assert.equal(markerDoc.value.nested[0], '__muxlioJsonBigint:', 'marker prefixes are not interpreted as integers');

const malicious = Json.jsonPanelFromText('{"x":"<img src=x onerror=alert(1)>"}', esc);
assert.doesNotMatch(malicious.html, /<img/i, 'JSON text must not inject HTML');
assert.match(malicious.html, /&lt;img/, 'malicious strings are escaped in the tree');

const broken = Json.jsonPanelFromText('{"items":[1,2', esc);
assert.match(broken.html, /json-raw-error/);
assert.match(broken.html, /json-raw/, 'invalid JSON shows raw text');

const truncated = Json.jsonArtifactPreview(
  { mime: 'application/json', path: 'big.json', data: `${'{"part":'.repeat(900)}1}` },
  esc,
);
assert.match(truncated, /truncated/i);
assert.match(truncated, /Copy preview/);

const nested = ctx.outputMarkup({
  config: {
    mime: 'application/json',
    path: 'config.json',
    data: JSON.stringify({ services: [{ name: 'api', ports: [8080, null] }], enabled: true }, null, 2),
  },
});
assert.match(nested, /artifact-preview/);
assert.match(nested, /object · 2|services/, 'nested JSON preview renders structure');
assert.doesNotMatch(nested, /<script>/);

const empty = ctx.outputMarkup({ empty: { mime: 'application/json', path: 'empty.json', data: '{}' } });
assert.match(empty, /JSON preview/);

ctx.rememberJsonCopy('copy-fixture', '{"a":1,"preview-only":true}');
const full = ctx.resolveJsonCopy('copy-fixture', 'full');
const preview = ctx.resolveJsonCopy('copy-fixture', 'preview');
assert.equal(full.text.length, preview.text.length);
assert.match(preview.label, /JSON copied/);

ctx.rememberJsonCopy('copy-long', `${'{"x":'.repeat(500)}${'y'.repeat(5000)}"}`);
const previewLong = ctx.resolveJsonCopy('copy-long', 'preview');
assert.equal(previewLong.text.length, 4096);
assert.match(previewLong.label, /Preview copied/);

const scalar = ctx.inputMarkup({ ready: true, count: 0, note: null, ratio: 0.25 });
assert.match(scalar, /json-value boolean.*true/);
assert.match(scalar, /json-value number.*0\.25/);
assert.match(scalar, /json-null/);

const css = readFileSync(new URL('./app.css', import.meta.url), 'utf8');
assert.match(css, /\.json-branch>summary:focus-visible/, 'JSON expanders expose keyboard focus');
assert.match(css, /@media\(max-width:720px\)/, 'JSON layout adapts on narrow widths');

const html = readFileSync(new URL('./index.html', import.meta.url), 'utf8');
assert.ok(html.indexOf('/js/json.js') < html.indexOf('/js/inspector.js'), 'json renderer loads before inspector');

console.log('JSON output presentation checks passed');
