import { workflow, task, artifact } from '../muxlio-sdk/src/index.js';
import { z } from 'zod';

// Minimal ACP example: local stdio agent, host execution, and file outputs.
// Dry-run: muxlio test examples/acp.workflow.ts --input examples/acp.input.json \
//   --fake-config examples/acp.fake.json --json
export default workflow('acp-minimal', {
  input: z.object({ question: z.string() }),
  build: ({ input, step }) => {
    const answered = step(
      'run-acp-agent',
      task({
        executor: 'agent/acp',
        task: 'Answer the question and write answer.json with a short JSON object.',
        params: {
          program: '/absolute/path/to/acp-compatible-agent',
          args: ['--acp'],
          timeout: '10m',
        },
        outputs: { answer: artifact.json('answer.json') },
      }),
      { question: input.question },
    );
    return { answer: answered.answer };
  },
});
