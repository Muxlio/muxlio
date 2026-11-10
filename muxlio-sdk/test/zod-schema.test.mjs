import test from 'node:test';
import assert from 'node:assert/strict';
import { zodToJsonSchema } from '../dist/index.js';
import { z } from 'zod';

test('zod array min/max maps to JSON Schema bounds', () => {
  const schema = zodToJsonSchema(
    z.array(z.object({ id: z.string().min(1) })).min(1).max(20),
  );
  assert.equal(schema.minItems, 1);
  assert.equal(schema.maxItems, 20);
});

test('zod refinements preserve the underlying JSON Schema shape', () => {
  const schema = zodToJsonSchema(z.string().min(3).refine((value) => value.includes('@')));
  assert.deepEqual(schema, { type: 'string', minLength: 3 });
});

test('zod discriminatedUnion compiles to anyOf object variants', () => {
  const schema = zodToJsonSchema(
    z.discriminatedUnion('kind', [
      z.object({ kind: z.literal('go'), module: z.string() }),
      z.object({ kind: z.literal('npm'), package: z.string() }),
    ]),
  );
  assert.ok(Array.isArray(schema.anyOf));
  assert.equal(schema.anyOf.length, 2);
  assert.deepEqual(schema.anyOf[0].properties.kind, { const: 'go' });
});

test('zod number min/max/int maps to JSON Schema bounds', () => {
  assert.deepEqual(zodToJsonSchema(z.number().min(0).max(10)), {
    type: 'number',
    minimum: 0,
    maximum: 10,
  });
  assert.deepEqual(zodToJsonSchema(z.number().int()), { type: 'integer' });
});

test('zod number exclusive bounds and multipleOf are preserved', () => {
  assert.deepEqual(zodToJsonSchema(z.number().gt(2).gte(5).lt(10).lte(8).multipleOf(2)), {
    type: 'number',
    minimum: 5,
    maximum: 8,
    multipleOf: 2,
  });
  assert.deepEqual(zodToJsonSchema(z.number().gt(5).gte(5).lt(8).lte(8)), {
    type: 'number',
    exclusiveMinimum: 5,
    exclusiveMaximum: 8,
  });
});

test('zod object defaults omit required and emit default values', () => {
  const schema = zodToJsonSchema(
    z.object({ retries: z.number().default(3), label: z.string() }),
  );
  assert.deepEqual(schema.required, ['label']);
  assert.deepEqual(schema.properties.retries, { type: 'number', default: 3 });
});

test('zod composite shapes preserve their JSON Schema constraints', () => {
  assert.deepEqual(zodToJsonSchema(z.intersection(z.object({ a: z.string() }), z.object({ b: z.number() }))), {
    allOf: [
      { type: 'object', properties: { a: { type: 'string' } }, required: ['a'] },
      { type: 'object', properties: { b: { type: 'number' } }, required: ['b'] },
    ],
  });
  assert.deepEqual(zodToJsonSchema(z.record(z.string(), z.boolean())), {
    type: 'object',
    additionalProperties: { type: 'boolean' },
  });
  assert.deepEqual(zodToJsonSchema(z.tuple([z.string(), z.number()])), {
    type: 'array',
    items: [{ type: 'string' }, { type: 'number' }],
    minItems: 2,
    maxItems: 2,
  });
  assert.deepEqual(zodToJsonSchema(z.tuple([z.string()]).rest(z.number())), {
    type: 'array',
    items: [{ type: 'string' }],
    minItems: 1,
    additionalItems: { type: 'number' },
  });
});
