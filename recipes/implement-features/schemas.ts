import { z } from 'zod';

export const feature = z.object({
  id: z.string().min(1),
  feature: z.string().min(1),
  context: z.string().optional(),
});

export const input = z.object({
  branch: z.string().optional(),
  features: z.array(feature).min(1).max(20),
});
