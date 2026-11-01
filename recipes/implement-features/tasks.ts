import { task, artifact } from '@muxlio/sdk';
import { z } from 'zod';

export const implement = task({
  executor: 'agent/codex',
  fallbackExecutors: ['agent/cursor'],
  task: `Implement the requested feature in the current workspace, following
existing conventions and patterns. Add or update focused tests where relevant.
Run any applicable checks and make sure they pass.

Write notes.md to the scratch directory summarising: what you changed, the
exact commands run, and their exit codes. Work only on this feature; do not
delegate to another agent.`,
  outputs: { notes: artifact.markdown('notes.md') },
  retry: { maxAttempts: 3, effect: 'retryable' },
});

export const review = task({
  executor: 'agent/codex',
  fallbackExecutors: ['agent/cursor'],
  task: `Independently review the feature implementation. Read the implementation
notes from the previous step (provided in context). Check correctness,
regressions, security, and test coverage. Fix any issues directly in the
workspace. Write review.md to the scratch directory with findings, fixes made,
and what you verified. If nothing needed fixing, state what you inspected and
confirmed. Do not delegate or edit SKILL.md.`,
  outputs: { report: artifact.markdown('review.md') },
  retry: { maxAttempts: 3, effect: 'retryable' },
});

export const fix = task({
  executor: 'agent/codex',
  fallbackExecutors: ['agent/cursor'],
  task: `Read the verification failure report provided in context. Determine root
causes and fix them in the workspace without weakening or disabling any checks.
Keep changes scoped to the feature. Write fix.md to the scratch directory
describing what was broken and what was repaired. Do not delegate or edit
SKILL.md.`,
  outputs: { notes: artifact.markdown('fix.md') },
  retry: { maxAttempts: 3, effect: 'retryable' },
});

export const verify = task({
  executor: 'agent/codex',
  fallbackExecutors: ['agent/cursor'],
  task: `Verify the feature implementation objectively. Run the project's primary
build and test commands. For this repository that is:
  1. npm run build   (from the repo root)
  2. npm test        (from the repo root)
  3. go test ./...   (from muxlio-adapters — these always pass)

The muxlio-core Go suite requires network listener binding which is blocked in
this sandbox; skip it or treat bind-permission failures as environment noise,
not feature regressions. Set passed=true if the npm build, npm test, and
muxlio-adapters Go tests all pass, regardless of core listener failures.

Write verification.md to the scratch directory: list every command run, its
exit code, and relevant log excerpts.

Write a file named \`passed\` to the scratch directory containing ONLY the JSON
literal true or false (no quotes, no surrounding text).`,
  outputs: {
    passed: z.boolean(),
    output: artifact.markdown('verification.md'),
  },
  retry: { maxAttempts: 2, effect: 'retryable' },
});

export const commit = task({
  executor: 'agent/codex',
  fallbackExecutors: ['agent/cursor'],
  task: `Verification passed. Commit the feature changes. Stage only the
intended changes — do not stage report files (notes.md, review.md,
verification.md, fix.md) from the scratch directory.

Create a commit with message "feat(<id>): <feature>" where <id> and <feature>
come from the feature input. If an equivalent commit already exists, use its
SHA.

Write the resulting commit sha as a JSON-encoded string (e.g. "abc1234") to
the output file listed below. The file must contain ONLY the JSON string.`,
  outputs: { sha: z.string() },
  retry: { maxAttempts: 2, effect: 'retryable' },
});
