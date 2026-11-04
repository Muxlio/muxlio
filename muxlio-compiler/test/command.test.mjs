import test from 'node:test';
import assert from 'node:assert/strict';
import { defineAsyncWorkflow } from '../dist/async.js';

test('command compilation exposes flat outputs alongside declared artifacts', async () => {
  const passed = { mime: 'application/json', schema: { type: 'boolean' } };
  const { plan } = await defineAsyncWorkflow({
    name: 'flat-command',
    run: async ctx => {
      const result = await ctx.execute('run', {
        executor: 'command',
        params: { program: 'node', args: ['--version'] },
        outputs: { passed, file: { mime: 'text/plain', path: 'version.txt' } },
      });
      assert.equal(result.outputs._result, undefined);
      return { passed: result.outputs.passed, stdout: result.outputs.stdout, file: result.outputs.file };
    },
  });
  const root = plan.subgraphs.root;
  assert.deepEqual(Object.keys(root.nodes[0].outputs).sort(),
    ['durationMs', 'exitCode', 'file', 'passed', 'signal', 'stderr', 'stdout']);
  assert.deepEqual(root.nodes[0].outputs.passed, passed, 'explicit declarations remain intact');
  assert.deepEqual(root.output.stdout, { ref: { node: 'run', port: 'stdout' } });
  assert.deepEqual(root.output.file, { ref: { node: 'run', port: 'file' } });
});
