import { task, artifact } from '@muxlio/sdk';

export const implement = task({
  executor: 'agent/kiro',
  fallbackExecutors: ['agent/cursor'],
  workspaceMode: 'shared-write',
  concurrencyKeys: ['implement-features-workspace'],
  task: `Implement the requested feature in the current workspace, following
existing conventions and patterns. Add or update focused tests where relevant.
Run any applicable checks and make sure they pass.

Write notes.md to the scratch directory summarising: what you changed, the
exact commands run, and their exit codes. Work only on this feature; do not
delegate to another agent. Do not commit or push; the recipe creates local commits.`,
  outputs: { notes: artifact.markdown('notes.md') },
  retry: { maxAttempts: 3, effect: 'retryable' },
});
export const review = task({
  executor: 'agent/antigravity',
  fallbackExecutors: ['agent/codex'],
  workspaceMode: 'shared-write',
  concurrencyKeys: ['implement-features-workspace'],
  task: `Independently review the feature implementation. Read the implementation
notes from the previous step (provided in context). Check correctness,
regressions, security, and test coverage. Fix any issues directly in the
workspace. Write review.md to the scratch directory with findings, fixes made,
and what you verified. If nothing needed fixing, state what you inspected and
confirmed. Do not delegate or edit SKILL.md. Do not commit or push; the recipe
creates local commits.`,
  outputs: { report: artifact.markdown('review.md') },
  retry: { maxAttempts: 3, effect: 'retryable' },
});

export const fix = task({
  executor: 'agent/kiro',
  fallbackExecutors: ['agent/cursor'],
  workspaceMode: 'shared-write',
  concurrencyKeys: ['implement-features-workspace'],
  task: `Read the verification failure report provided in context. Determine root
causes and fix them in the workspace without weakening or disabling any checks.
Keep changes scoped to the feature. Write fix.md to the scratch directory
describing what was broken and what was repaired. Do not delegate or edit
SKILL.md. Do not commit or push; the recipe creates local commits.`,
  outputs: { notes: artifact.markdown('fix.md') },
  retry: { maxAttempts: 3, effect: 'retryable' },
});

export const verify = task({
  executor: 'command',
  workspaceMode: 'shared-write',
  concurrencyKeys: ['implement-features-workspace'],
  task: 'Run the repository build and test suites; return nonzero if any suite fails.',
  params: {
    program: 'node',
    args: ['-e', `const {spawnSync}=require('node:child_process');
const checks=[['npm run build','npm',['run','build'],'.'],['npm test','npm',['test'],'.'],['UI tests','npm',['--prefix','muxlio-ui','test'],'.'],['core Go tests','go',['test','./...'],'muxlio-core'],['adapter Go tests','go',['test','./...'],'muxlio-adapters']];
let failed=false;
for(const [label,program,args,cwd] of checks){process.stdout.write('\\n=== '+label+' ===\\n');const r=spawnSync(program,args,{cwd,stdio:'inherit'});const code=r.error?1:(r.status??1);process.stdout.write('\\n=== '+label+': exit '+code+' ===\\n');if(code!==0)failed=true;}
process.exitCode=failed?1:0;`],
    cwd: '.',
    onNonZero: 'return',
    timeout: '10m',
  },
});

export const commit = task({
  executor: 'command',
  workspaceMode: 'shared-write',
  concurrencyKeys: ['implement-features-workspace'],
  task: 'Create the verified feature commit.',
  params: {
    program: 'git',
    args: ['commit', '-m', ''],
    argTemplates: [null, null, 'implement {{feature.feature}}'],
    cwd: '.',
    onNonZero: 'fail',
  },
});

export const stageChanges = task({
  executor: 'command',
  workspaceMode: 'shared-write',
  concurrencyKeys: ['implement-features-workspace'],
  task: 'Stage workspace changes for the verified feature commit.',
  params: { program: 'git', args: ['add', '-A'], cwd: '.', onNonZero: 'fail' },
});

export const requireCleanWorkspace = task({
  executor: 'command',
  workspaceMode: 'shared-write',
  concurrencyKeys: ['implement-features-workspace'],
  task: 'Require a clean workspace before beginning a feature.',
  params: { program: 'git', args: ['status', '--porcelain'], cwd: '.', onNonZero: 'fail', failOnOutput: true },
});

