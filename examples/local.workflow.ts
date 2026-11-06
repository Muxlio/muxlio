import { workflow, task, artifact } from '../muxlio-sdk/src/index.js';
import { z } from 'zod';

// Inline tasks use the same executor contract locally and on remote workers.
export default workflow('local-summary', {
  input: z.object({ text: z.string() }),
  build: ({ input, step }) => {
    const prepared = step('prepare', task({
      executor: 'command',
      params: { program: 'node', args: ['-e', 'console.log("Ready to summarize")'] },
    }), {});
    const summary = step('summarize', task({
      executor: 'agent/codex',
      task: 'Summarize the supplied text and write summary.md.',
      outputs: { summary: artifact.markdown('summary.md') },
    }), { text: input.text, preparation: prepared.stdout });
    return { summary: summary.summary };
  },
});
