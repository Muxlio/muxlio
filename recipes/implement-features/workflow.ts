// recipes/implement-features/workflow.ts
//
// Batch feature implementation: implement → review → verify/fix loop → commit.
// Generic: works for any workspace, not just git repositories.
//
//   muxlio check recipes/implement-features/workflow.ts
//   muxlio run recipes/implement-features/workflow.ts recipes/implement-features/input.json \
//     --adapters adapters --data-dir ~/.muxlio --json
import { workflow } from '@muxlio/sdk';
import { input } from './schemas.js';
import { commit, fix, implement, review, verify } from './tasks.js';

export default workflow('implement-features', {
  input,

  build: ({ input, step, each, repeatUntil }) => {
    const results = each(
      input.features,
      {
        key: f => f.id,
        concurrency: 1,
      },
      feature => {
        const implementation = step('implement', implement, {
          feature,
          branch: input.branch,
        });

        const reviewed = step('review', review, {
          feature,
          notes: implementation.notes,
        });

        const verification = repeatUntil('verification', {
          attempt: () =>
            step(
              'verify',
              verify,
              { feature },
              { after: reviewed },
            ),

          until: result => result.passed,

          otherwise: result =>
            step('fix', fix, {
              feature,
              failures: result.output,
            }),

          maxAttempts: 10,
          onExhausted: 'pause',
        });

        const committed = step(
          'commit',
          commit,
          {
            feature,
            verificationReport: verification.value.output,
          },
          { requiresPassed: verification },
        );

        return {
          id: feature.id,
          commit: committed.sha,
          attempts: verification.attempts,
        };
      },
    );

    return { results };
  },
});
