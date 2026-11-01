import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const source = readFileSync(new URL('./js/workbench.js', import.meta.url), 'utf8');
const { graphMarkup, numbers } = vm.runInNewContext(`${source}; ({graphMarkup,numbers:activationDisplayNumbers})`, {
  selectedActivation: 'verify-10',
  esc: value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),
});
const item = ['map:each__0:001'];
const acts = [{ id: 'map', nodeId: 'each__0', state: 'succeeded' }];
for (const nodeId of ['implement', 'review', 'verification']) {
  acts.push({ id: nodeId, nodeId, scopePath: item, state: 'succeeded' });
}
// Deliberately arrive out of order, without explicit intermediate scope records.
for (const iteration of [10, 2, 0]) {
  const path = [...item, `loop:verification:${iteration}`];
  acts.push({ id: `verify-${iteration}`, nodeId: 'verify', scopePath: path, state: 'succeeded' });
  acts.push({ id: `branch-${iteration}`, nodeId: 'check', scopePath: path, state: 'succeeded' });
  acts.push({ id: `fix-${iteration}`, nodeId: 'fix', scopePath: [...path, 'branch:check:true'], state: 'succeeded' });
}
acts.push({ id: 'other-map', nodeId: 'other', state: 'succeeded' });
acts.push({ id: 'other-item', nodeId: '<unsafe>', scopePath: ['map:other:002'], state: 'succeeded' });
const html = graphMarkup(acts);
assert.equal((html.match(/graph-map-set graph-loop-set/g) || []).length, 1, 'iterations share one loop container');
assert.match(html, /3 iterations/);
assert.ok(html.indexOf('iteration 0') < html.indexOf('iteration 2'));
assert.ok(html.indexOf('iteration 2') < html.indexOf('iteration 10'), 'iterations sort numerically');
for (const iteration of [0, 2, 10]) {
  const disclosure = html.match(new RegExp(`<details[^>]*data-preserve-key="loop:[^>]*verification:${iteration}[^>]*>`))?.[0];
  assert.ok(disclosure, `iteration ${iteration} is addressable`);
  assert.equal(/ open>/.test(disclosure), false, 'iterations start closed');
}
assert.match(html, /Map · each__0/);
assert.match(html, /Map · other/);
assert.match(html, /Branch · check/);
assert.match(html, /&lt;unsafe&gt;/);
assert.match(html, /node-card succeeded selected/);
assert.equal(numbers.size, acts.length, 'collapsed steps retain inspector numbering');
assert.equal(new Set(numbers.values()).size, acts.length);
for (const act of acts) assert.equal(html.split(`data-act="${act.id}"`).length - 1, 1, 'each activation renders exactly once');
assert.match(html, /node-done-gate/);
assert.equal((html.match(/node-start-gate/g)||[]).length,1, 'one default start for the whole graph');
assert.equal((html.match(/node-done-gate/g)||[]).length,1, 'one default completion for the whole graph');
assert.ok(html.indexOf('node-start-gate')<html.indexOf('data-act="map"'), 'start precedes the map');
assert.ok(html.indexOf('node-done-gate')>html.indexOf('data-act="other-item"'), 'done follows all nested work');
assert.match(html, /Start<\/div><\/div><div class="graph-connector"/, 'start has an outgoing edge');
assert.match(html, /class="graph-connector" aria-hidden="true"><div class="connector-line"><\/div><\/div><div class="graph-col"><div class="node-done-gate"/, 'done has an incoming edge');
const failed = graphMarkup(acts.map(a => a.id === 'verify-0' ? {...a, state:'failed'} : a));
assert.doesNotMatch(failed, /<details[^>]* open[ >]/, 'failed groups start closed too');
assert.match(failed, /graph-status failed">1 failed/, 'collapsed parents expose failures');
assert.match(failed, /node-done-gate pending/, 'failed runs retain a pending completion');
const running = graphMarkup(acts.map(a => a.id === 'verify-2' ? {...a, state:'running'} : a));
assert.doesNotMatch(running, /<details[^>]* open[ >]/, 'running groups start closed too');
assert.match(running, /graph-status running">1 running/);
assert.match(running, /node-done-gate pending/);
const empty=graphMarkup([]);
assert.match(empty, /No activations/);
assert.match(empty, /node-start-gate pending/);
assert.match(empty, /node-done-gate pending/);
assert.doesNotMatch(html, /<details[^>]* open[ >]/, 'all scopes default to collapsed');
assert.match(html, /graph-branch-route taken/);
assert.match(html, /graph-branch-route untaken/);
assert.match(html, /TRUE/);
assert.match(html, /FALSE/);
assert.match(html, /Paths rejoin before continuing/);
assert.match(html, /Repeat: iteration 2/);
const falsePath=graphMarkup([{id:'decision',nodeId:'choose',state:'succeeded'}, {id:'fallback',nodeId:'fallback',state:'failed',scopePath:['branch:choose:false']}]);
assert.match(falsePath, /untaken[\s\S]*TRUE[\s\S]*taken[\s\S]*FALSE/, 'false choices select the false lane');
assert.match(falsePath, /graph-status failed/);
console.log('nested graph rendering checks passed');
