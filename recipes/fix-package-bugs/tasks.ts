import { artifact, task } from '@muxlio/sdk';
import { commitScript, verifyScript } from './commands.js';

// Serialize each shared workspace operation; run only one writer workflow per checkout.
const workspace = { workspaceMode: 'shared-write' as const, concurrencyKeys: ['package-bugfix-workspace'] };
const investigator = {
  ...workspace,
  executor: 'agent/kiro',
  fallbackExecutors: ['agent/cursor'],
  timeoutSeconds: 1200,
  queueTimeoutSeconds: 1800,
  retry: { maxAttempts: 3, backoffSeconds: 5, maxBackoffSeconds: 30, effect: 'retryable' },
};
const reviewer = {
  ...investigator,
  executor: 'agent/antigravity',
  fallbackExecutors: ['agent/codex'],
};
const rules = `Follow AGENTS.md. Keep changes scoped to the supplied package and
related code needed for a demonstrated bug. Do not weaken checks, delegate,
edit SKILL.md, commit, or push. On retries, inspect the current diff and continue
from existing work. Write reports to the scratch directory.`;

export const requireCleanWorkspace = task({
  ...workspace,
  executor: 'command',
  task: 'Require a clean tracked and untracked workspace before auditing a package.',
  params: { program: 'git', args: ['status', '--porcelain'], cwd: '.', onNonZero: 'fail', failOnOutput: true },
  retry: { maxAttempts: 1, effect: 'pure' },
  timeoutSeconds: 60,
});

export const findAndFix = task({
  ...investigator,
  task: `Inspect the supplied package, its callers, and tests for concrete bugs.
Reproduce each finding or identify the exact failing code path. Fix confirmed
bugs and add focused regression tests where practical. If nothing is wrong,
leave the workspace clean. Run the relevant package checks. Write findings.md
with the bugs found, evidence, changes, commands and exit codes. ${rules}`,
  outputs: { findings: artifact.markdown('findings.md') },
});

export const review = task({
  ...reviewer,
  task: `Independently review the findings and current diff. Check correctness,
regressions, test coverage, and architecture boundaries. Fix issues directly in
the workspace and run relevant checks. Write review.md with what you inspected,
what you fixed, and any unresolved issues. If no bug was found, confirm what you
checked. ${rules}`,
  outputs: { report: artifact.markdown('review.md') },
});

export const verify = task({
  ...workspace,
  executor: 'command',
  task: 'Run package checks and repository regression suites.',
  params: {
    program: 'node',
    args: ['-e', verifyScript, '', '', ''],
    argTemplates: [null, null, '{{package.kind}}', '{{package.path}}', '{{package.target}}'],
    cwd: '.', onNonZero: 'return', timeout: '10m',
  },
  outputs: { report: artifact.json('checks.json') },
  timeoutSeconds: 600,
  queueTimeoutSeconds: 1800,
  retry: { maxAttempts: 1, effect: 'nonretryable' },
});

export const fix = task({
  ...investigator,
  task: `Read the verification failures and check report. Fix their root causes
without weakening checks. Keep changes scoped to this package. Write fix.md
with what failed, what changed, and the exact checks run. ${rules}`,
  outputs: { notes: artifact.markdown('fix.md') },
});

export const commit = task({
  ...workspace,
  executor: 'command',
  task: 'Commit reviewed and verified fixes locally; succeed without a commit when no changes are needed.',
  params: { program: 'node', args: ['-e', commitScript, ''], argTemplates: [null, null, '{{package.id}}'], cwd: '.', onNonZero: 'fail' },
  timeoutSeconds: 120,
  retry: { maxAttempts: 1, effect: 'nonretryable' },
});
