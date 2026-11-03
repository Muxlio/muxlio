import { task, artifact } from '@muxlio/sdk';
import { z } from 'zod';

export const implement = task({
  executor: 'agent/kiro',
  fallbackExecutors: ['agent/codex'],
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
  executor: 'agent/kiro',
  fallbackExecutors: ['agent/codex'],
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
  executor: 'agent/kiro',
  fallbackExecutors: ['agent/codex'],
  task: `Read the verification failure report provided in context. Determine root
causes and fix them in the workspace without weakening or disabling any checks.
Keep changes scoped to the feature. Write fix.md to the scratch directory
describing what was broken and what was repaired. Do not delegate or edit
SKILL.md.`,
  outputs: { notes: artifact.markdown('fix.md') },
  retry: { maxAttempts: 3, effect: 'retryable' },
});

const commandResult = z.object({
  exitCode: z.number().nullable(), signal: z.string().optional(),
  stdout: z.string(), stderr: z.string(), durationMs: z.number(),
});

function verifyCommand(id: string, program: string, args: string[], cwd: string) {
  return task({
  executor: 'command',
  task: `Run ${id}.`,
  params: { program, args, cwd, onNonZero: 'return', timeout: '10m' },
  outputs: { _result: commandResult },
  });
}

export const verify = task({
  executor: 'command',
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
  outputs: { _result: commandResult.extend({ passed: z.boolean() }) },
});

export const commit = task({
  executor: 'command',
  task: 'Create the verified feature commit.',
  params: {
    program: 'git',
    args: ['commit', '-m', ''],
    argTemplates: [null, null, 'implement {{feature.feature}}'],
    cwd: '.',
    onNonZero: 'fail',
  },
  outputs: { _result: commandResult },
});

export const readCommitSHA = task({
  executor: 'command',
  task: 'Read the commit SHA.',
  params: { program: 'git', args: ['rev-parse', 'HEAD'], cwd: '.', onNonZero: 'fail', trimStdout: true },
  outputs: { _result: commandResult },
});

export const stageChanges = task({
  executor: 'command',
  task: 'Stage workspace changes for the verified feature commit.',
  params: { program: 'git', args: ['add', '-A'], cwd: '.', onNonZero: 'fail' },
  outputs: { _result: commandResult },
});

export const requireCleanWorkspace = task({
  executor: 'command',
  task: 'Require a clean workspace before beginning a feature.',
  params: { program: 'git', args: ['status', '--porcelain'], cwd: '.', onNonZero: 'fail', failOnOutput: true },
  outputs: { _result: commandResult },
});
