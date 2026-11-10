import { workflow, task, artifact } from '../muxlio-sdk/src/index.js';
import { z } from 'zod';

// Minimal A2A example: remote task handoff, reconcilable effect, artifact output.
// Dry-run: muxlio test examples/a2a.workflow.ts --input examples/a2a.input.json \
//   --fake-config examples/a2a.fake.json --json
export default workflow('a2a-minimal', {
  input: z.object({ prompt: z.string() }),
  build: ({ input, step }) => {
    const remote = step(
      'submit-remote',
      task({
        executor: 'agent/a2a',
        task: 'Complete the prompt and write result.json.',
        params: {
          url: 'https://agent.example',
          timeout: '30s',
        },
        requiredCapabilities: { remoteTask: '1' },
        retry: { maxAttempts: 1, effect: 'reconcilable' },
        executionPolicy: { allowedSecrets: ['A2A_TOKEN'] },
        secretRefs: { A2A_TOKEN: 'a2a-production' },
        outputs: { result: artifact.json('result.json') },
      }),
      { prompt: input.prompt },
    );
    return { result: remote.result };
  },
});
