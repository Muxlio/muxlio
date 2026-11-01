// Workflow: audit-test-coverage
//
// Maps over Go packages in muxlio-core, analyzes each for test coverage,
// branches on whether tests exist, and collects a report. This is a real
// workflow that uses map (fan-out), branch (conditional), and execute
// (per-package analysis) — the core M0 dataflow primitives.
//
// Compile: npx tsx examples/audit-coverage.workflow.ts
// Run:     .bin/muxlio run examples/audit-coverage.plan.json examples/audit-coverage.input.json --json

import { defineWorkflow } from '../muxlio-sdk/src/index.js';
import { writeFileSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync as wfs } from 'node:fs';

// --- Discover the packages at authoring time so the workflow input is concrete ---

const packages = execSync('find muxlio-core/internal muxlio-core/cmd muxlio-core/tests -name "*.go" -exec dirname {} \\; | sort -u', { encoding: 'utf-8' })
  .trim()
  .split('\n')
  .map((p) => ({ id: p.replace(/\//g, '_'), path: p }));

const { plan } = defineWorkflow({
  name: 'audit-test-coverage',
  input: {
    type: 'object',
    properties: {
      packages: {
        type: 'array',
        items: {
          type: 'object',
          properties: {
            id: { type: 'string' },
            path: { type: 'string' },
          },
        },
      },
    },
    required: ['packages'],
  },
  output: { type: 'array' },

  build(g) {
    const results = g.map('packages', {
      items: g.inputField('packages'),
      key: g.field(g.item(), 'id'),
      concurrency: 4,
      maxItems: 50,
      body: (ctx) => {
        // Step 1: analyze the package for test files
        const analysis = ctx.execute('analyze', {
          executor: 'command/go-list',
          task: 'List Go files in the package and check for _test.go files.',
          params: { cmd: 'find', args: ['-name', '*_test.go'] },
          outputs: {
            hasTests: { mime: 'application/json' },
            fileCount: { mime: 'application/json' },
          },
        });

        // Step 2: branch — if the package has tests, verify them; if not, flag it
        const check = ctx.branch('has-tests', {
          condition: ctx.eq(
            ctx.field(analysis.output('hasTests'), 'hasTests'),
            ctx.literal(true),
          ),
          then: (thenCtx) => {
            // The scope path already identifies the package; no cross-scope ref needed.
            const verify = thenCtx.execute('verify-tests', {
              executor: 'command/go-test',
              task: 'Run go test for this package and report pass/fail.',
              outputs: { passed: { mime: 'application/json' } },
            });
            return {
              status: { expr: thenCtx.literal('tested') },
              passed: verify.output('passed'),
            };
          },
          else: (elseCtx) => {
            return {
              status: { expr: elseCtx.literal('no-tests') },
              passed: { expr: elseCtx.literal(false) },
            };
          },
        });

        return {
          package: { expr: ctx.field(ctx.item(), 'id') },
          status: check.result(),
        };
      },
    });

    g.output({ result: results.items() });
  },
});

writeFileSync('examples/audit-coverage.plan.json', JSON.stringify(plan, null, 2));
writeFileSync('examples/audit-coverage.input.json', JSON.stringify({ packages }, null, 2));
console.log(`Compiled audit-coverage workflow: ${packages.length} packages`);
console.log(`Subgraphs: ${Object.keys(plan.subgraphs).join(', ')}`);
console.log(`Required executors: ${plan.requiredExecutors?.join(', ')}`);
