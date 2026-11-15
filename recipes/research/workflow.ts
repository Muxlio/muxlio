// Iterative research with a complete findings artifact carried between attempts.
// Uses the explicit Plan builder because this loop needs access to its own state.
import { defineWorkflow, zodToJsonSchema } from '@muxlio/sdk';
import { input } from './schemas.js';
import { researchIteration, synthesize } from './tasks.js';

export default defineWorkflow({
  name: 'research',
  input: zodToJsonSchema(input),
  limits: { maxActiveAttempts: 1, maxAttempts: 40, maxActivations: 100 },
  build: g => {
    const context = Object.fromEntries(
      ['topic', 'questions', 'audience', 'depth'].map(key => [key, { expr: g.inputField(key) }]),
    );
    const research = g.loop('research', {
      while: g.not(g.stateField('done')),
      initState: g.literal({ done: false, attempts: 0, findings: null, progress: null }),
      maxIterations: 10, // The runtime defaults to pausing when exhausted.
      body: body => {
        const iteration = body.add(body.stateField('attempts'), body.literal(1));
        const result = body.execute('iterate', {
          ...researchIteration,
          inputs: {
            ...context,
            iteration: { expr: iteration },
            previousFindings: { expr: body.stateField('findings') },
          },
        });
        return {
          done: { expr: body.field(result.output('progress'), 'done') },
          attempts: { expr: iteration },
          findings: result.output('findings'),
          progress: result.output('progress'),
        };
      },
    });
    // Exhaustion pauses the loop before synthesis can run.
    const synthesis = g.execute('synthesize', {
      ...synthesize,
      inputs: {
        ...context,
        findings: { expr: g.field(research.result(), 'findings') },
        progress: { expr: g.field(research.result(), 'progress') },
        iterations: { expr: g.field(research.result(), 'attempts') },
      },
    });
    g.output({
      report: synthesis.output('report'),
      metrics: synthesis.output('metrics'),
      canvas: synthesis.output('canvas'),
      iterations: { expr: g.field(research.result(), 'attempts') },
    });
  },
});
