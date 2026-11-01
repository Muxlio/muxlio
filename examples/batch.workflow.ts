// Example: a batch workflow that processes items in parallel using the
// symbolic builder (Frontend B). Each item is processed by two agent steps
// (gather → transform). The compiled Plan IR is written to a JSON file and
// run by the Go interpreter.
//
// Run: npx tsx examples/batch.workflow.ts

import { defineWorkflow, field, item, inputField, itemField } from '../muxlio-sdk/src/index.js';
import { writeFileSync } from 'node:fs';

const { plan } = defineWorkflow({
  name: 'batch',
  input: {
    type: 'object',
    properties: {
      items: {
        type: 'array',
        items: {
          type: 'object',
          properties: {
            id: { type: 'string' },
            title: { type: 'string' },
          },
        },
      },
    },
    required: ['items'],
  },
  output: { type: 'array' },

  build(g) {
    const result = g.map('items', {
      items: g.inputField('items'),
      key: g.field(g.item(), 'id'),
      concurrency: 5,
      maxItems: 100,
      body: (ctx) => {
        const gather = ctx.execute('gather', {
          executor: 'agent/gemini-cli',
          task: 'Investigate the topic and write findings to findings.md.',
          outputs: { findings: { mime: 'text/markdown', path: 'findings.md' } },
        });
        const transform = ctx.execute('transform', {
          executor: 'agent/claude',
          task: 'Read the findings and produce result.md.',
          inputs: { findings: gather.output('findings') },
          outputs: { result: { mime: 'text/markdown', path: 'result.md' } },
        });
        return {
          id: { expr: ctx.field(ctx.item(), 'id') },
          result: transform.output('result'),
        };
      },
    });

    g.output({ result: result.items() });
  },
});

writeFileSync('examples/batch.plan.json', JSON.stringify(plan, null, 2));
console.log('Compiled plan written to examples/batch.plan.json');
console.log(`Name: ${plan.name}`);
console.log(`Digest: ${plan.digest}`);
console.log(`Subgraphs: ${Object.keys(plan.subgraphs).join(', ')}`);
console.log(`Required executors: ${plan.requiredExecutors?.join(', ')}`);
