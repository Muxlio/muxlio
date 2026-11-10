import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const liveSource = `
const activationTranscripts=new Map();
function decodeLogData(data){return data}
function mergeActivationLogs(actId,logs){
  let state=activationTranscripts.get(actId);
  if(!state){state={agent:'',seen:new Set()};activationTranscripts.set(actId,state)}
  const agentChunks=(logs||[]).filter(x=>x.stream==='agent').sort((a,b)=>(a.seq||0)-(b.seq||0));
  for(const chunk of agentChunks){
    const key=chunk.stream+':'+chunk.seq;
    if(state.seen.has(key))continue;
    state.seen.add(key);
    state.agent+=decodeLogData(chunk.data);
  }
  return state.agent;
}
let livePollTimer=null,livePollToken=0,liveSelection=null,liveMonitorEntry=null,liveEntries=[];
function stopLivePoll(){if(livePollTimer){clearTimeout(livePollTimer);livePollTimer=null}livePollToken++}
function updateLiveTranscript(text){document.getElementById('liveTranscriptBody').textContent=text||''}
function stateClass(s){return s||''}
function esc(s){return String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
function renderLiveMeta(entry){
  if(!entry){document.meta=null;return}
  const act=entry.act,attempt=entry.attempt;
  const executor=(attempt&&attempt.state!=='queued'?attempt.executor:'')||act.executorHint||act.executor||'—';
  const attemptState=attempt?.state||'—';
  const transcriptUrl='/transcripts/'+encodeURIComponent(entry.runId)+'/'+encodeURIComponent(act.id);
  document.meta=entry;
  document.lastTranscriptUrl=transcriptUrl;
}
function latestAttemptFor(attempts,activationId){
  return (attempts||[]).filter(a=>a.activationId===activationId&&a.state!=='queued').sort((a,b)=>(b.retryNumber||0)-(a.retryNumber||0))[0]||null;
}
function scheduleLiveOutputPoll(entry){
  stopLivePoll();
  const token=livePollToken,runId=entry.runId,actId=entry.act.id;
  async function tick(){
    if(token!==livePollToken||!liveSelection||liveSelection.runId!==runId||liveSelection.activationId!==actId)return;
    const d=await api(runId,actId);
    if(token!==livePollToken)return;
    updateLiveTranscript(mergeActivationLogs(actId,d.logs||[]));
    if(entry.act.state==='running')livePollTimer=setTimeout(tick,10);
  }
  tick();
}
function selectLiveStream(runId,actId,entry){
  liveSelection={runId,activationId:actId};
  liveMonitorEntry=entry;
  updateLiveTranscript(activationTranscripts.get(actId)?.agent||'');
  scheduleLiveOutputPoll(entry);
}
async function finishSelected(entry,graph,attempts){
  liveEntries=[];
  const selected=liveMonitorEntry;
  const completed=graph.activations.find(a=>a.id===selected.act.id);
  liveMonitorEntry={...selected,act:completed,attempt:attempts.find(a=>a.activationId===completed.id)};
  renderLiveMeta(liveMonitorEntry);
  updateLiveTranscript(activationTranscripts.get(completed.id)?.agent||el.textContent,false);
  stopLivePoll();
}
function applyCompleted(entry,graph,attempts){
  const completed=graph.activations.find(a=>a.id===entry.act.id);
  const updated={...entry,act:completed,attempt:attempts.find(a=>a.activationId===completed.id)};
  renderLiveMeta(updated);
  updateLiveTranscript(activationTranscripts.get(completed.id)?.agent||el.textContent,false);
  return updated;
}
`;

const el = { textContent: '' };
const ctx = {
  api(runId, actId) {
    if (runId === 'run-a' && actId === 'act-1') return Promise.resolve({ logs: [{ stream: 'agent', seq: 1, data: 'A1 ' }] });
    if (runId === 'run-b' && actId === 'act-2') return Promise.resolve({ logs: [{ stream: 'agent', seq: 1, data: 'B1' }] });
    return Promise.resolve({ logs: [] });
  },
  document: { getElementById() { return el; }, meta: null, lastTranscriptUrl: null },
  setTimeout(fn) { queueMicrotask(fn); return 1; },
  clearTimeout() {},
};
const api = ctx.api;
const { selectLiveStream, stopLivePoll, applyCompleted, latestAttemptFor } = vm.runInNewContext(`${liveSource}; ({selectLiveStream,stopLivePoll,applyCompleted,latestAttemptFor})`, ctx);
assert.equal(latestAttemptFor([
  {activationId:'x',retryNumber:1,executor:'agent/kiro',state:'failed'},
  {activationId:'x',retryNumber:2,executor:'agent/cursor',state:'queued'},
], 'x').executor, 'agent/kiro', 'queued attempts must not be presented as the active executor');

await selectLiveStream('run-a', 'act-1', { runId: 'run-a', act: { id: 'act-1', state: 'running' } });
await new Promise(resolve => queueMicrotask(resolve));
assert.equal(el.textContent, 'A1 ');

await selectLiveStream('run-b', 'act-2', { runId: 'run-b', act: { id: 'act-2', state: 'running' } });
await new Promise(resolve => queueMicrotask(resolve));
assert.equal(el.textContent, 'B1');
assert.ok(!el.textContent.includes('A1'), 'switching activations must replace the transcript');

applyCompleted({runId:'run-b',act:{id:'act-2',nodeId:'node-b',state:'running'}}, { activations: [{ id: 'act-2', nodeId: 'node-b', state: 'succeeded' }] }, [{ activationId: 'act-2', state: 'succeeded', retryNumber: 1 }]);
assert.equal(el.textContent, 'B1', 'finishing an activation must keep its final transcript visible');
assert.equal(ctx.document.meta.act.state, 'succeeded', 'finishing an activation must refresh final metadata');
assert.equal(ctx.document.meta.attempt.state, 'succeeded', 'finishing an activation must refresh its attempt state');

// ── transcript URL test ───────────────────────────────────────────────────
// After applyCompleted, the transcript URL must encode run + activation IDs.
assert.ok(
  ctx.document.lastTranscriptUrl === '/transcripts/run-b/act-2',
  `transcript URL must be /transcripts/<runId>/<actId>, got: ${ctx.document.lastTranscriptUrl}`,
);

stopLivePoll();

const prodLive = readFileSync(new URL('./js/live.js', import.meta.url), 'utf8');
const pollBlock = prodLive.slice(
  prodLive.indexOf('function scheduleLiveOutputPoll'),
  prodLive.indexOf('function renderLiveMeta'),
);
assert.match(pollBlock, /statePollCount\+\+/, 'live poll must periodically re-check activation state');
assert.match(pollBlock, /\/activations\/\$\{encodeURIComponent\(actId\)\}/, 'live poll must read activation state from the API');
const activationIsStreaming = vm.runInNewContext(
  `${prodLive.slice(prodLive.indexOf('function activationIsStreaming'), prodLive.indexOf('function stopLivePoll'))}; activationIsStreaming`,
  {},
);
assert.equal(
  activationIsStreaming({ state: 'succeeded' }, { state: 'running' }),
  false,
  'terminal activation state must end streaming even when attempt metadata is stale',
);

let pollTicks = 0;
let pollTimersAfterTerminal = 0;
const terminalOut = { lastMetaState: null, lastTranscriptLive: null };
const terminalCtx = vm.createContext({
  activationTranscripts: new Map([['act-1', { agent: 'done', seen: new Set() }]]),
  mergeActivationLogs(actId) { return activationTranscripts.get(actId)?.agent || ''; },
  renderMarkdown: String,
  LIVE_POLL_MS: 1,
  livePollTimer: null,
  livePollToken: 0,
  liveSelection: { runId: 'run-a', activationId: 'act-1' },
  liveMonitorEntry: { runId: 'run-a', act: { id: 'act-1', state: 'running' } },
  liveEntries: [{ runId: 'run-a', act: { id: 'act-1', state: 'running' }, attempt: { state: 'running' } }],
  document: { querySelector: () => null },
  renderLiveMeta(entry) { terminalOut.lastMetaState = entry?.act?.state; },
  renderLiveList() {},
  api(path) {
    if (path.includes('/logs?')) {
      pollTicks++;
      return Promise.resolve({ logs: [] });
    }
    if (path.includes('/activations/')) return Promise.resolve({ id: 'act-1', state: 'succeeded' });
    return Promise.resolve({});
  },
  setTimeout(fn) {
    if (terminalOut.lastMetaState === 'succeeded') pollTimersAfterTerminal++;
    queueMicrotask(fn);
    return pollTicks + 1;
  },
  clearTimeout() {},
  terminalOut,
});
vm.runInContext(
  `let livePollTimer=null,livePollToken=0;
  ${prodLive.slice(prodLive.indexOf('function activationIsStreaming'), prodLive.indexOf('function stopLivePoll'))}
  function stopLivePoll(){if(livePollTimer){clearTimeout(livePollTimer);livePollTimer=null}livePollToken++}
  function updateLiveTranscript(text,live){terminalOut.lastTranscriptLive=live;}
  ${pollBlock}
  scheduleLiveOutputPoll(liveMonitorEntry);`,
  terminalCtx,
);
for (let i = 0; i < 200; i++) await new Promise(resolve => queueMicrotask(resolve));
assert.ok(pollTicks >= 5, 'state poll should run after several log polls');
assert.equal(terminalOut.lastMetaState, 'succeeded', 'live poll must refresh metadata when an activation finishes');
assert.equal(terminalOut.lastTranscriptLive, false, 'live poll must switch to terminal transcript mode after completion');
assert.equal(pollTimersAfterTerminal, 0, 'live poll must stop after the activation is no longer streaming');

console.log('live output stream switch check passed');
