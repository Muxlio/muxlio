import { z } from 'zod';
import { maxResearchIterations, maxResearchQuestions } from './limits.js';

export function createResearchSchemas(iterationLimit = maxResearchIterations) {
  if (!Number.isSafeInteger(iterationLimit) || iterationLimit < 1) {
    throw new Error('Research iteration limit must be a positive safe integer');
  }
  // One question is researched per pass. Reject inputs that cannot fit even
  // before allowing for questions that need more than one pass.
  const questionLimit = Math.min(maxResearchQuestions, iterationLimit);
  const input = z.object({
    topic: z.string().min(1),
    questions: z.array(z.string().min(1)).min(1).max(questionLimit),
    audience: z.string().optional(),
    depth: z.enum(['brief', 'standard', 'thorough']).optional(),
  });

  // A JSON result port (without a file path) lets the loop inspect done directly.
  const progress = z.object({
    iteration: z.number().int().min(1).max(iterationLimit),
    done: z.boolean(),
    answeredCount: z.number().int().min(0).max(questionLimit),
    partialCount: z.number().int().min(0).max(questionLimit),
    openCount: z.number().int().min(0).max(questionLimit),
    totalQuestions: z.number().int().min(1).max(questionLimit),
    thisIterationQuestion: z.string().nullable(),
  }).strict();
  return { input, progress };
}

export const { input, progress } = createResearchSchemas();
