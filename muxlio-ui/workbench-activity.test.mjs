import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const format=readFileSync(new URL('./js/format.js',import.meta.url),'utf8');
const usage=readFileSync(new URL('./js/usage-summary.js',import.meta.url),'utf8');
const wb=readFileSync(new URL('./js/workbench.js',import.meta.url),'utf8');
const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
let now=Date.parse('2026-06-01T12:00:00Z');
class Clock extends Date{constructor(...args){super(...(args.length?args:[now]))}static now(){return now}}
const {buildWbActivityMarkup,materializedStateCounts}=vm.runInNewContext(`${format}\n${usage}\n${wb};({buildWbActivityMarkup,materializedStateCounts})`,{Date:Clock,esc,document:{addEventListener(){}},selectedActivation:null,runAttempts:[],wbActivityAttempts:[],wbInputRequestSummary:[]});

test('materialized counts avoid percentage framing',()=>{
  const counts=materializedStateCounts([{state:'succeeded'},{state:'running'},{state:'queued'}]);
  const {html}=buildWbActivityMarkup({id:'run',state:'running',overview:{activations:3,completed:1}},[{id:'a',nodeId:'task.a',state:'running'},{id:'b',nodeId:'each__0',state:'queued',queueReason:'Waiting for capacity'}],[{activationId:'a',state:'running',startedAt:'2026-06-01T11:59:00Z',retryNumber:0}]);
  assert.match(html,/class="wb-activity-details"[^>]*>/);
  assert.doesNotMatch(html,/3 materialized|1 running|<details[^>]*\bopen\b/);
  assert.doesNotMatch(html,/\b\d{1,3}%\b|estimated time remaining/i);
  assert.match(html,/Waiting for capacity/);
  assert.equal(counts.succeeded,1);
  assert.equal(counts.running,1);
});

test('attention surfaces reconciliation, input, failure, and detached work',()=>{
  const acts=[{id:'bad',nodeId:'verify',state:'failed',error:'step broke'},{id:'run-node',nodeId:'work',state:'running'}];
  const attempts=[{id:'held',activationId:'run-node',state:'cancelled',awaitReconciliationReason:'Verify remote outcome',startedAt:'2026-06-01T11:58:00Z',finishedAt:'2026-06-01T11:59:30Z'}];
  const requests=[{requestId:'req-1',name:'Approve',state:'pending',activationId:'run-node'}];
  const {html,visible}=buildWbActivityMarkup({id:'run',state:'running',overview:{activations:2,heldAttempts:2,attemptStates:{detached:1}}},acts,attempts,requests);
  assert.ok(visible);
  assert.match(html,/Resolve uncertain execution/);
  assert.match(html,/data-ops-action="resolve"/);
  assert.match(html,/Detached remote operations/);
  assert.match(html,/Respond/);
  assert.match(html,/step broke/);
  assert.match(html,/Held 1m 30s/);
  assert.match(html,/Live transcript/);
  assert.doesNotMatch(html,/<script>/);
});

test('terminal succeeded run shows honest empty state',()=>{
  const {html,visible,announce}=buildWbActivityMarkup({id:'run',state:'succeeded',overview:{activations:2,completed:2}},[{id:'a',nodeId:'a',state:'succeeded'},{id:'b',nodeId:'b',state:'succeeded'}],[],[]);
  assert.ok(visible);
  assert.match(html,/materialized nodes only/);
  assert.match(announce,/succeeded/);
});

test('paused and waiting runs expose elapsed wait when timestamps exist',()=>{
  const paused=buildWbActivityMarkup({id:'run',state:'paused',createdAt:'2026-06-01T11:00:00Z',updatedAt:'2026-06-01T11:30:00Z',overview:{activations:0}},[],[],[]);
  assert.match(paused.html,/Waiting 30m 0s/);
  assert.match(paused.html,/Resume run/);
  const waiting=buildWbActivityMarkup({id:'run',state:'waiting',createdAt:'2026-06-01T11:00:00Z',updatedAt:'2026-06-01T11:55:00Z',overview:{activations:0}},[],[],[]);
  assert.match(waiting.html,/Waiting 5m 0s/);
});
