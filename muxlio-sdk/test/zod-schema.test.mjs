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
