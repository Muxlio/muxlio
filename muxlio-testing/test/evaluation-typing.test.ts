import { z } from 'zod';
import { defineDataset, evaluateWorkflow } from '../src/index.js';

const dataset = defineDataset({ id: 'typed/v1', input: z.object({ text: z.string() }), output: z.number(), cases: [{ id: 'a', input: { text: 'question' }, expected: 1 }] });
void evaluateWorkflow({ dataset, versions: [], scorers: [{ id: 'typed/v1', score: ({ input, output, expected }) => {
  const text: string = input.text;
  const number: number = output;
  const optional: number | undefined = expected;
  // @ts-expect-error output is numeric, never a string
  const invalid: string = output;
  return number + (optional ?? 0) + text.length;
} }] });
// @ts-expect-error dataset inputs must match the schema
 defineDataset({ id: 'bad', input: z.string(), output: z.number(), cases: [{ id: 'a', input: 42 }] });
// @ts-expect-error expected output must match the output schema
 defineDataset({ id: 'bad', input: z.string(), output: z.number(), cases: [{ id: 'a', input: 'x', expected: 'bad' }] });
