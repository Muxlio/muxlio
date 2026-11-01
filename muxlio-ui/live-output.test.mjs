import assert from 'node:assert/strict';
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
  const transcriptUrl='#transcript/'+encodeURIComponent(entry.runId)+'/'+encodeURIComponent(act.id);
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
  ctx.document.lastTranscriptUrl === '#transcript/run-b/act-2',
  `transcript URL must be #transcript/<runId>/<actId>, got: ${ctx.document.lastTranscriptUrl}`,
);

stopLivePoll();
console.log('live output stream switch check passed');
