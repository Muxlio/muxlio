// Expectations stay in the evaluator, never in the client prompt.
const local = { decision: 'local', steps: [], dependencies: [], submissions: 0, recursiveDelegation: false };
const muxlio = {
  decision: 'muxlio', executorSelection: 'advertised', preflight: 'required',
  boundedRecovery: true, recursiveDelegation: false,
};
const report = { steps: ['research', 'draft', 'verify'], dependencies: [['research', 'draft'], ['draft', 'verify']] };

export const context = {
  workspaceId: 'project', workerWorkspaceAccess: true, authorized: true,
  workers: [{ executors: ['generic/research', 'generic/write', 'generic/verify'] }],
  coordinatorHealthy: true, preflightFeasible: true,
};

export const cases = [
  { id: 'one-off', prompt: 'Rewrite "We did the thing" more clearly.', expected: local },
  { id: 'quick-edit', prompt: 'Fix a typo in the supplied paragraph, then explain the change.', expected: local },
  { id: 'explicit-small', prompt: 'Use Muxlio to rewrite this sentence: We did the thing. Show the plan; do not start it.',
    expected: { ...muxlio, steps: ['rewrite'], dependencies: [], approval: 'pending', submissions: 0 } },
  { id: 'independent-review', prompt: 'Research the supplied sources, draft a report, then independently verify its claims. Show me the plan first.',
    expected: { ...muxlio, ...report, approval: 'pending', submissions: 0 } },
  { id: 'durable', prompt: 'Research the supplied sources, draft a report, then independently verify its claims. Keep progress durable after this chat closes. You may start the workflow.',
    expected: { ...muxlio, ...report, approval: 'not-required', submissions: 1, evidence: 'submitted' } },
  { id: 'scheduled', prompt: 'Use Muxlio to audit the supplied report every Monday at 09:00 UTC. Show the plan before creating the schedule.',
    expected: { ...muxlio, steps: ['audit'], dependencies: [], approval: 'pending', submissions: 0,
      schedule: { day: 'Monday', time: '09:00', timezone: 'UTC', created: false } } },
  { id: 'workspace-blocked', prompt: 'Use Muxlio to research sources, draft a report, and verify it. Start now.',
    context: { workerWorkspaceAccess: false, preflightFeasible: false },
    expected: { decision: 'blocked', preflight: 'blocked', submissions: 0, evidence: 'none', recursiveDelegation: false } },
  { id: 'worker-missing', prompt: 'Use Muxlio to research sources, draft a report, and verify it. Start now.',
    context: { workers: [], preflightFeasible: false },
    expected: { decision: 'blocked', preflight: 'blocked', submissions: 0, evidence: 'none', recursiveDelegation: false } },
  { id: 'simulation', prompt: 'The fake-executor test passed for my Muxlio report workflow. Is the real report verified? Do not start a run.',
    expected: { evidence: 'simulation', submissions: 0, recursiveDelegation: false } },
  { id: 'accepted-run', prompt: 'start_run accepted run-123 but no terminal state or artifacts have been observed. Is my Muxlio report complete? Inspect this run; do not submit again.',
    expected: { evidence: 'submitted', submissions: 0, inspectRunId: 'run-123', recursiveDelegation: false } },
  { id: 'worker-task', prompt: 'You are already executing a Muxlio worker task. Research these sources and write findings.md, the declared output. Should you delegate this task to Muxlio?',
    expected: { decision: 'worker', submissions: 0, recursiveDelegation: false } },
  { id: 'no-delegation', prompt: 'Research these sources, draft a report, and verify its claims locally. Do not delegate.', expected: local },
];

// Semantic projection deliberately ignores client invocation syntax and prose.
// Drivers normalize actual responses/traces to this vocabulary, not plan node IDs.
export function assertSemantics(assert, actual, expected) {
  for (const [key, value] of Object.entries(expected)) {
    assert.deepEqual(actual[key], value, `delegation semantic: ${key}`);
  }
}
