#!/usr/bin/env node
// Credentialed probes use this transparent stdio relay. Record only successful
// tool names, never arguments, results, credentials, or client transcripts.
import { spawn } from 'node:child_process';
import { appendFileSync } from 'node:fs';
import { createInterface } from 'node:readline';
import { isDeepStrictEqual } from 'node:util';
const submission = process.env.MUXLIO_ACCEPTANCE_SUBMISSION ? JSON.parse(process.env.MUXLIO_ACCEPTANCE_SUBMISSION) : undefined;
const child = spawn(process.env.MUXLIO_ACCEPTANCE_MCP, process.argv.slice(2), { stdio: ['pipe', 'pipe', 'inherit'] });
const pending = new Map();
createInterface({ input: process.stdin }).on('line', line => {
  try {
    const msg = JSON.parse(line);
    if (msg.method === 'tools/call' && (typeof msg.id === 'string' || typeof msg.id === 'number')) {
      const name = msg.params.name;
      const evidence = { name };
      // Keep only comparisons, never the submitted payload or run identifier.
      if (name === 'start_run') evidence.expected = submission !== undefined && isDeepStrictEqual(msg.params.arguments, submission);
      if (name === 'get_run') evidence.expected = Boolean(process.env.MUXLIO_ACCEPTANCE_RUN_ID) && msg.params.arguments?.runId === process.env.MUXLIO_ACCEPTANCE_RUN_ID;
      pending.set(msg.id, evidence);
    }
  } catch { /* Forward malformed frames too; the server owns validation. */ }
  child.stdin.write(line + '\n');
}).on('close', () => child.stdin.end());
createInterface({ input: child.stdout }).on('line', line => {
  try {
    const msg = JSON.parse(line);
    const evidence = pending.get(msg.id);
    if (evidence && msg.result && !msg.error && !msg.result.isError) {
      if (evidence.name === 'get_run') {
        try {
          const run = JSON.parse(msg.result.content?.[0]?.text);
          if (run.state === 'succeeded') evidence.succeeded = true;
        } catch { /* Successful transport alone does not prove a final result. */ }
      }
      appendFileSync(process.env.MUXLIO_ACCEPTANCE_AUDIT, JSON.stringify(evidence) + '\n', { mode: 0o600 });
    }
    pending.delete(msg.id);
  } catch { /* A bad frame remains visible to the actual client. */ }
  process.stdout.write(line + '\n');
});
child.on('error', () => process.exit(1));
child.stdin.on('error', () => child.kill('SIGTERM'));
child.on('close', code => process.exit(code ?? 1));
process.on('SIGTERM', () => child.kill('SIGTERM'));
