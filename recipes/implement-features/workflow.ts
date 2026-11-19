// recipes/implement-features/workflow.ts
//
// Batch feature implementation: implement → review → verify/fix loop → commit.
// Shared-write tasks retain the worker checkout's Git metadata.
//
// Register the checkout as a workspace before submitting the recipe.
//   muxlio check recipes/implement-features/workflow.ts
//   muxlio run recipes/implement-features/workflow.ts recipes/implement-features/input.example.json \
//     --server local --workspace-id localhost --json
import { workflow, zodToJsonSchema } from '@muxlio/sdk';
import { feature as featureSchema, input, maxFeatures } from './schemas.js';
import { commit, fix, implement, requireCleanWorkspace, review, stageChanges, verify } from './tasks.js';

export function implementFeatures({ live = false } = {}) {
  return workflow(live ? 'implement-features-live' : 'implement-features', {
    input,

    build: ({ input, step, each, repeatUntil }) => {
      const results = each(
        input.features,
        {
          key: f => f.id,
          concurrency: 1,
          ...(live ? {
            maxItems: maxFeatures,
            appendable: {
              name: 'features',
              itemSchema: zodToJsonSchema(featureSchema),
              idleTimeoutSeconds: 15 * 60,
            },
          } : {}),
        },
        feature => {
          step('require-clean-workspace', requireCleanWorkspace, { feature });
          const implementation = step('implement', implement, {
            feature,
            branch: input.branch,
          });

          const reviewed = step('review', review, {
            feature,
            notes: implementation.notes,
          });

          const verification = repeatUntil('verification', {
            attempt: () => step('verify', verify, { feature }, { after: reviewed }),

            until: result => result.passed,

            otherwise: result =>
              step('fix', fix, {
                feature,
                failures: {
                  passed: result.passed,
                  exitCode: result.exitCode,
                  signal: result.signal,
                  stdout: result.stdout,
                  stderr: result.stderr,
                  durationMs: result.durationMs,
                },
              }),

            maxAttempts: 10,
            onExhausted: 'pause',
          });

          const staged = step('stage-changes', stageChanges, { feature, verificationReport: verification.value }, { requiresPassed: verification });
          step('commit', commit, { feature }, { after: staged, requiresPassed: verification });

          return {
            id: feature.id,
            attempts: verification.attempts,
          };
        },
      );

      return { results };
    },
  });
}

export default implementFeatures();
