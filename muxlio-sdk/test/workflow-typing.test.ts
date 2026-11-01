import { artifact, task, workflow } from '../src/workflow.js';
import { z } from 'zod';

const notesTask = task({
  executor: 'fake/echo',
  outputs: { notes: artifact.markdown('notes.md') },
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
    const _ok = implementation.notes;
    // @ts-expect-error unknown output port must fail typecheck
    const _bad = implementation.notDeclared;
  },
});
