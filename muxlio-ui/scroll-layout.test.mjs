import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(root, 'app.css'), 'utf8');
const html = readFileSync(join(root, 'index.html'), 'utf8');

assert.match(css, /\.shell\{[^}]*height:100vh;overflow:hidden/, 'shell should lock viewport height');
assert.match(css, /\.content\{[^}]*overflow:auto[^}]*display:flex;flex-direction:column/, 'main content region should scroll tall pages');
assert.match(css, /\.content--wb[^}]*overflow:auto/, 'run workbench should scroll in the main content pane');
assert.match(css, /\.content > \.page:not\(\[hidden\]\)[^}]*flex:0 0 auto/, 'short pages should not stretch to force nested scrollbars');
assert.match(css, /#detailView > #workbenchView:not\(\[hidden\]\)[^}]*flex:0 0 auto/, 'workbench should grow with content instead of filling the viewport');
assert.match(css, /#detailView > #runDetail:not\(\[hidden\]\)[^}]*min-height:100%;overflow:visible/, 'fallback run detail should expand to its content');
assert.match(css, /\.workbench\{[^}]*overflow:visible/, 'workbench should not clip content below the fold');
assert.match(css, /\.wb-tab-body\{[^}]*overflow:visible/, 'workbench tabs should scroll with the page');
assert.match(css, /\.tablebox\{overflow-x:auto/, 'tables should grow vertically with the page');
assert.match(css, /\.inspector-panel\{[^}]*overflow:visible/, 'inspector should grow with long content');
assert.match(css, /\.insp-body\{[^}]*overflow:visible/, 'inspector content should scroll with the page');
assert.match(css, /\.graph-canvas\{[^}]*overflow:visible/, 'tall graphs should extend the page instead of nesting scroll');
assert.match(css, /\.wb-bottom\{[^}]*height:auto;overflow:visible/, 'event and artifact panels should grow with their content');
assert.match(css, /\.bottom-body\{flex:0 0 auto;overflow:visible/, 'event and artifact lists should scroll with the page');
assert.match(
  css,
  /\.content--wb > #detailView:not\(\[hidden\]\),\.content:has\(#workbenchView:not\(\[hidden\]\)\) > #detailView:not\(\[hidden\]\)\{flex:0 0 auto;min-height:0\}/,
  'workbench detail view should grow with content instead of clipping it',
);
assert.match(html, /class="wb-tab-body"/, 'events tab should use shared scroll body class');
assert.match(html, /class="wb-tab-body wb-tab-body--grid"/, 'data tab should use shared scroll body class');

assert.match(html, /id="liveView" class="page"/, 'live output should use the scrollable page shell');
assert.match(css, /\.live-monitor-body\{[^}]*overflow:visible/, 'live transcript pane should not trap scroll');

console.log('scroll layout CSS check passed');
