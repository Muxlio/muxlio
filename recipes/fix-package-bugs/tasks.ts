import { artifact, task } from '@muxlio/sdk';
import { z } from 'zod';
import { commitScript, verifyScript } from './commands.js';

// Serialize each shared workspace operation; run only one writer workflow per checkout.
const workspace = { workspaceMode: 'shared-write' as const, concurrencyKeys: ['package-bugfix-workspace'] };
const agent = {
  ...workspace,
  executor: 'agent/kiro',
  fallbackExecutors: ['agent/codex'],
  timeoutSeconds: 1200,
  queueTimeoutSeconds: 1800,
  retry: { maxAttempts: 3, backoffSeconds: 5, maxBackoffSeconds: 30, effect: 'retryable' },
};
const rules = `Follow AGENTS.md and preserve architecture boundaries. Keep provider
protocols, authentication, environment handling and usage normalization in their
own adapters. Missing usage is unavailable; reported zero stays zero.
On a retried attempt, inspect the current diff and continue from existing work.
Work on the supplied package and only the related code needed for a demonstrated
bug. Do not refactor unrelated code, weaken checks, edit SKILL.md, delegate,
commit, or push. Write requested reports in the scratch directory, not the repository.`;

export const requireCleanWorkspace = task({
  ...workspace,
  executor: 'command',
  task: 'Require a clean tracked and untracked workspace before auditing a package.',
  params: { program: 'git', args: ['status', '--porcelain'], cwd: '.', onNonZero: 'fail', failOnOutput: true },
  retry: { maxAttempts: 1, effect: 'pure' },
  timeoutSeconds: 60,
});

export const audit = task({
  ...agent,
  task: `Inspect the supplied package for concrete bugs. Read its code, callers,
tests and contracts. Look for incorrect behavior, regressions, edge cases,
security issues and recovery failures. Reproduce each finding or give a precise
code path and expected versus actual behavior. Do not invent findings to fill a
quota. Do not modify repository files in this step. Write findings.md with
evidence, affected paths, severity and suggested focused verification. Explicitly
say when no actionable bug was found. ${rules}`,
  outputs: { findings: artifact.markdown('findings.md') },
});

export const repair = task({
  ...agent,
  task: `Read the supplied audit findings and independently confirm them. Fix
confirmed bugs and add focused regression tests that fail before the fix where
practical. If there is no confirmed bug, leave the repository clean. Run the
package checks. Write fixes.md with confirmed and rejected findings, changes,
tests, exact commands and exit codes, and any unresolved issues. ${rules}`,
  outputs: { notes: artifact.markdown('fixes.md') },
});

export const review = task({
  ...agent,
  task: `Independently review the supplied package and current diff against the
audit and repair notes. Inspect behavior and regression tests, including any
repairs from previous verification attempts visible in the current workspace.
Check all remaining findings and architecture boundaries. Do not edit repository
files. Approve only when no actionable issue remains. Write review.md with the
evidence inspected, remaining issues and verification performed. Return approved
as a boolean and feedback as a self-contained string listing every required
repair with paths and reproduction details; downstream repair receives this
feedback through the verification logs. ${rules}`,
  outputs: { approved: z.boolean(), feedback: z.string(), report: artifact.markdown('review.md') },
});

export const verify = task({
  ...workspace,
  executor: 'command',
  task: 'Run package checks and repository regression suites; require independent review approval too.',
  params: {
    program: 'node',
    args: ['-e', verifyScript, '', '', '', '', ''],
    argTemplates: [null, null, '{{package.kind}}', '{{package.path}}', '{{package.target}}', '{{approved}}', '{{feedback}}'],
    cwd: '.', onNonZero: 'return', timeout: '10m',
  },
  outputs: { report: artifact.json('checks.json') },
  timeoutSeconds: 600,
  queueTimeoutSeconds: 1800,
  retry: { maxAttempts: 1, effect: 'nonretryable' },
});

export const fixVerification = task({
  ...agent,
  task: `Read the supplied verification result, including reviewer feedback,
stdout, stderr, exit code and signal. Resolve the root cause of each remaining
review finding or failing check. Do not declare success just because a command
was started. Keep changes scoped to the package's confirmed bugs and necessary
regression repairs. Write retry-fixes.md with changes and exact checks run.
The next attempt independently reviews the complete diff and reruns all checks.
${rules}`,
  outputs: { notes: artifact.markdown('retry-fixes.md') },
});

export const commit = task({
  ...workspace,
  executor: 'command',
  task: 'Commit reviewed and verified fixes locally; succeed without a commit when no changes are needed.',
  params: { program: 'node', args: ['-e', commitScript, ''], argTemplates: [null, null, '{{package.id}}'], cwd: '.', onNonZero: 'fail' },
  timeoutSeconds: 120,
  retry: { maxAttempts: 1, effect: 'nonretryable' },
});
