// Example: the same batch workflow authored with Frontend A (restricted
// async TypeScript). The `run` function is called once at compile time
// with a recording context; `await ctx.execute(...)` and `await ctx.map(...)`
// are traced to build the same Plan IR as Frontend B.
//
// Run: npx tsx examples/batch-async.workflow.ts

import { defineAsyncWorkflow } from '../muxlio-compiler/src/index.js';
import { writeFileSync } from 'node:fs';

async function main() {
const { plan } = await defineAsyncWorkflow({
  name: 'batch-async',
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

  async run(ctx, input) {
    return ctx.map('items', input.items, async (item, itemCtx) => {
      const gather = await itemCtx.execute('gather', {
        executor: 'agent/gemini-cli',
        task: 'Investigate the topic.',
        outputs: { findings: { mime: 'text/markdown', path: 'findings.md' } },
      });
      const transform = await itemCtx.execute('transform', {
        executor: 'agent/claude',
        task: 'Read the findings and produce result.md.',
        inputs: { findings: gather.outputs.findings },
        outputs: { result: { mime: 'text/markdown', path: 'result.md' } },
      });
      return { id: item.id, result: transform.outputs.result };
    }, { key: (item: any) => item.id, concurrency: 5, maxItems: 100 });
  },
});

writeFileSync('examples/batch-async.plan.json', JSON.stringify(plan, null, 2));
console.log('Compiled async plan written to examples/batch-async.plan.json');
console.log(`Name: ${plan.name}`);
console.log(`Digest: ${plan.digest}`);
console.log(`Subgraphs: ${Object.keys(plan.subgraphs).join(', ')}`);
}

main();
