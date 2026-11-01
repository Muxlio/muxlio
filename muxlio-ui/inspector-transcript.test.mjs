import assert from 'node:assert/strict';
import vm from 'node:vm';

const mergeSource = `
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
`;

const merge = vm.runInNewContext(`${mergeSource}; mergeActivationLogs`, {});
assert.equal(
  merge('a', [{ stream: 'agent', seq: 1, data: 'partial ' }, { stream: 'agent', seq: 2, data: 'text' }]),
  'partial text',
);
console.log('inspector transcript merge check passed');

// ── resolveInspectorAgent tests ──────────────────────────────────────────
// The resolve function reads `runAttempts` from the enclosing scope.
const resolveSource = `
function resolveInspectorAgent(act) {
  const attempts = typeof runAttempts !== 'undefined' ? runAttempts : [];
  const actAttempts = attempts.filter(a => a.activationId === act.id && a.state !== 'queued')
    .sort((a, b) => (b.retryNumber || 0) - (a.retryNumber || 0));
  return actAttempts[0]?.executor || act.executorHint || act.executor || '—';
}
`;

// Test: falls back to activation executor when no attempts
const resolve1 = vm.runInNewContext(`${resolveSource}; resolveInspectorAgent`, { runAttempts: [] });
assert.equal(
  resolve1({ id: 'act-1', executor: 'agent/kiro' }),
  'agent/kiro',
  'should use activation executor when no attempts',
);

const resolveQueued = vm.runInNewContext(`${resolveSource}; resolveInspectorAgent`, {
  runAttempts: [
    { activationId: 'act-4', executor: 'agent/kiro', retryNumber: 1, state: 'failed' },
    { activationId: 'act-4', executor: 'agent/cursor', retryNumber: 2, state: 'queued' },
  ],
});
assert.equal(resolveQueued({ id: 'act-4', executor: 'agent/kiro' }), 'agent/kiro',
  'a queued retry must not replace the executor that actually ran');

// Test: prefers latest attempt executor over activation executor
const resolve2 = vm.runInNewContext(`${resolveSource}; resolveInspectorAgent`, {
  runAttempts: [
    { activationId: 'act-2', executor: 'agent/cursor', retryNumber: 1, state: 'failed' },
    { activationId: 'act-2', executor: 'agent/kiro', retryNumber: 2, state: 'running' },
  ],
});
assert.equal(
  resolve2({ id: 'act-2', executor: 'agent/old' }),
  'agent/kiro',
  'should use latest attempt executor (highest retryNumber)',
);

// Test: returns — when nothing is set
const resolve3 = vm.runInNewContext(`${resolveSource}; resolveInspectorAgent`, { runAttempts: [] });
assert.equal(
  resolve3({ id: 'act-3' }),
  '—',
  'should return — when no executor is set',
);

console.log('inspector agent resolution checks passed');
