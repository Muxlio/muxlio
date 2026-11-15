import { z } from 'zod';

export const input = z.object({
  topic: z.string().min(1),
  questions: z.array(z.string().min(1)).min(1).max(20),
  audience: z.string().optional(),
  depth: z.enum(['brief', 'standard', 'thorough']).optional(),
});

// A JSON result port (without a file path) lets the loop inspect done directly.
export const progress = z.object({
  iteration: z.number().int().min(1).max(10),
  done: z.boolean(),
  answeredCount: z.number().int().min(0).max(20),
  partialCount: z.number().int().min(0).max(20),
  openCount: z.number().int().min(0).max(20),
  totalQuestions: z.number().int().min(1).max(20),
  thisIterationQuestion: z.string().nullable(),
}).strict();
