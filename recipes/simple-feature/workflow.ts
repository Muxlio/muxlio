// recipes/simple-feature/workflow.ts
//
// Minimal single-step feature implementation recipe. Unlike
// implement-features, this declares only a file-artifact output, which is the
// only output shape the real agent adapters (codex/kiro/cursor) support
// end-to-end: they write each declared output as a file in the run's scratch
// dir and upload it as a content-addressed artifact. Scalar outputs
// (z.string/z.boolean) get rewritten to artifact refs, so they cannot carry a
// path/boolean/sha value between steps.
//
//   muxlio check recipes/simple-feature/workflow.ts
//   muxlio build recipes/simple-feature/workflow.ts --out /tmp/simple-feature.plan.json
//   muxlio submit --server yamini /tmp/simple-feature.plan.json recipes/simple-feature/input.json
import { workflow, task, artifact } from '@muxlio/sdk';
import { z } from 'zod';

const implement = task({
  executor: 'agent/codex',
  fallbackExecutors: ['agent/kiro', 'agent/cursor'],
  task: `Implement the requested feature in this repository, following existing
conventions. Add or update focused tests if appropriate and run any relevant
checks. Then write notes.md summarizing what you changed and the exact
commands/results you ran. Work only on this feature; do not delegate to
another agent.`,
  outputs: { notes: artifact.markdown('notes.md') },
  retry: { maxAttempts: 2, effect: 'retryable' },
});

export default workflow('simple-feature', {
  input: z.object({ feature: z.string().min(1) }),
  build: ({ input, step }) => {
    const r = step('implement', implement, { feature: input.feature });
    return { notes: r.notes };
  },
});
