import { z } from 'zod';

// Paths are workspace-relative; Go targets are relative to their module root.
const relativePath = z.string().min(1);

export const packageSchema = z.object({
  id: z.string().regex(/^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/),
  path: relativePath,
  kind: z.enum(['go', 'npm', 'ui']),
  target: relativePath,
  context: z.string().optional(),
}).strict();

export const input = z.object({
  packages: z.array(packageSchema).min(1).max(200),
}).strict();
