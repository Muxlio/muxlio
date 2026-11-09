// Sweep packages one at a time: find and fix bugs, review, verify/fix, commit.
// Discovery builds the package list; compiling the plan has no side effects.
import { workflow } from '@muxlio/sdk';
import { input } from './schemas.js';
import { commit, findAndFix, fix, requireCleanWorkspace, review, verify } from './tasks.js';

export default workflow('fix-package-bugs', {
  input,
  limits: { maxActiveAttempts: 1, maxAttempts: 10000, maxActivations: 30000 },

  build: ({ input, step, each, repeatUntil }) => {
    const results = each(input.packages, { key: pkg => pkg.id, concurrency: 1, maxItems: 200 }, pkg => {
      const clean = step('require-clean-workspace', requireCleanWorkspace, { package: pkg });

      const found = step('find-and-fix', findAndFix, { package: pkg }, { after: clean });
      const reviewed = step('review', review, { package: pkg, findings: found.findings });

      const verification = repeatUntil('verification', {
        attempt: () => step('verify', verify, { package: pkg }, { after: reviewed }),
        until: result => result.passed,
        otherwise: result => step('fix', fix, {
          package: pkg,
          failures: {
            passed: result.passed,
            exitCode: result.exitCode,
            signal: result.signal,
            stdout: result.stdout,
            stderr: result.stderr,
            durationMs: result.durationMs,
          },
          checks: result.report,
        }),
        maxAttempts: 10,
        onExhausted: 'pause',
      });

      const committed = step('commit', commit, { package: pkg }, { requiresPassed: verification });
      return {
        id: pkg.id,
        findings: found.findings,
        review: reviewed.report,
        verification: verification.value.report,
        attempts: verification.attempts,
        commit: committed.stdout,
      };
    });

    return { results };
  },
});
