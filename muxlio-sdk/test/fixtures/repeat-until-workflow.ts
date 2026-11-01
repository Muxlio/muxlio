/** Minimal repeatUntil plan used by Go acceptance tests (test/* executors). */
import { workflow, task, artifact } from '../../src/workflow.js';
import { z } from 'zod';

const verify = task({
  executor: 'test/verify',
  outputs: {
    passed: z.boolean(),
    report: artifact.markdown('verification.md'),
  },
});

const fix = task({
  executor: 'test/fix',
  outputs: { notes: artifact.markdown('fix.md') },
});

export default workflow('repeat-until-test', {
  build: ({ repeatUntil, step }) => {
    repeatUntil('loop', {
      maxAttempts: 10,
      attempt: () => step('verify', verify, {}),
      until: (r) => r.passed,
      otherwise: (r) => step('fix', fix, { failures: r.report }),
      onExhausted: 'pause',
    });
  },
});
