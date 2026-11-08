import { workflow } from '@muxlio/sdk';
import { input } from './schemas.js';
import { audit, commit, fixVerification, repair, requireCleanWorkspace, review, verify } from './tasks.js';

export default workflow('fix-package-bugs', {
  input,
  // Cover the 200-item input bound, including review/fix loops and agent retries.
  limits: { maxActiveAttempts: 1, maxAttempts: 10000, maxActivations: 30000 },
  build: ({ input, step, each, repeatUntil }) => {
    const results = each(input.packages, { key: pkg => pkg.id, concurrency: 1, maxItems: 200 }, pkg => {
      const clean = step('require-clean-workspace', requireCleanWorkspace, { package: pkg });
      const findings = step('audit', audit, { package: pkg }, { after: clean });
      const repaired = step('repair', repair, { package: pkg, findings: findings.findings });
      const verification = repeatUntil('verification', {
        attempt: () => {
          const reviewed = step('review', review, {
            package: pkg, findings: findings.findings, notes: repaired.notes,
          });
          return step('verify', verify, {
            package: pkg, approved: reviewed.approved, feedback: reviewed.feedback, reviewReport: reviewed.report,
          });
        },
        until: result => result.passed,
        otherwise: result => step('fix-verification', fixVerification, {
          package: pkg,
          findings: findings.findings,
          failures: {
            passed: result.passed, exitCode: result.exitCode, signal: result.signal,
            stdout: result.stdout, stderr: result.stderr, durationMs: result.durationMs,
          },
          checks: result.report,
        }),
        maxAttempts: 5,
        onExhausted: 'pause',
      });
      const committed = step('commit', commit, { package: pkg, verificationReport: verification.value.report }, { requiresPassed: verification });
      return { id: pkg.id, findings: findings.findings, fixes: repaired.notes,
        verification: verification.value.report, passed: verification.value.passed,
        attempts: verification.attempts, commit: committed.stdout };
    });
    return { results };
  },
});
