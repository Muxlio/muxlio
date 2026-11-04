import { artifact, task, workflow } from '../src/workflow.js';
import { z } from 'zod';

const notesTask = task({
  executor: 'fake/echo',
  outputs: { notes: artifact.markdown('notes.md') },
});

workflow('command-verification-typing', {
  input: z.object({}),
  build: ({ step, repeatUntil }) => {
    const verify = task({ executor: 'command' });
    const verification = repeatUntil('verification', {
      attempt: () => step('verify', verify, {}),
      until: result => {
        const passed: boolean = result.passed;
        const _exitCode: number | null = result.exitCode;
        const _signal: string | null = result.signal;
        const _stdout: string = result.stdout;
        const _stderr: string = result.stderr;
        const _durationMs: number = result.durationMs;
        // @ts-expect-error inferred outputs retain their concrete types
        const _wrongType: number = result.stdout;
        // @ts-expect-error command results have no wrapper output
        const _details = result._result;
        return passed;
      },
      otherwise: () => step('fix', notesTask, {}),
      maxAttempts: 2,
    });
    const _passed: boolean = verification.value.passed;
    const generated = step('generate', task({
      executor: 'command',
      outputs: { file: artifact.markdown('result.md'), stdout: z.literal('done') },
    }), {});
    const _file: string = generated.file.path;
    const _literal: 'done' = generated.stdout;
    const _generatedPassed: boolean = generated.passed;
    return { passed: verification.value.passed };
  },
});

workflow('typing-check', {
  input: z.object({
    repository: z.string(),
    features: z.array(
      z.object({
        id: z.string().min(1),
        feature: z.string().min(1),
      }),
    ).min(1).max(20),
  }),
  build: ({ input, step, each }) => {
    const _repo = input.repository;
    // @ts-expect-error misspelled input field
    const _badInput = input.repositry;

    each(input.features, { key: (f) => f.id }, (featureItem) => {
      const _id = featureItem.id;
      // @ts-expect-error misspelled feature field
      const _badFeature = featureItem.featur;
      step('noop', task({ executor: 'fake/echo', outputs: {} }), {
        feature: { id: featureItem.id },
      });
    });

    const implementation = step('implement', notesTask, {});
    // @ts-expect-error command outputs are not inferred for agent tasks
    const _notCommand = implementation.passed;
    const _ok = implementation.notes;
    // @ts-expect-error unknown output port must fail typecheck
    const _bad = implementation.notDeclared;
  },
});
