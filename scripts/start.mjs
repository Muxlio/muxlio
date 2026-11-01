import { spawn } from 'node:child_process';
import { mkdirSync, openSync, closeSync, readFileSync, writeFileSync, unlinkSync, rmdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const root = join(homedir(), '.muxlio');
const argv = process.argv.slice(2);
const restarting = argv[0] === 'restart';
if (restarting) argv.shift();
const [service = 'server', ...args] = argv;
if (!['server', 'ui'].includes(service)) {
  console.error('Usage: bash scripts/start.sh [restart] [server|ui] [options]');
  process.exit(2);
}
mkdirSync(join(root, 'logs'), { recursive: true });
mkdirSync(join(root, 'data'), { recursive: true });
const pidPath = join(root, service + '.pid');
async function waitForExit(pid, timeoutMs) {
 const start = Date.now();
 while (Date.now() - start < timeoutMs) {
  try { process.kill(pid, 0); } catch (error) { if (error.code === 'ESRCH') return true; throw error; }
  await new Promise(resolve => setTimeout(resolve, 100));
 }
 return false;
}
const lockPath = pidPath + '.lock';
try { mkdirSync(lockPath); } catch (error) {
  console.error(error.code === 'EEXIST' ? service + ' startup is already in progress' : error.message);
  process.exit(1);
}
let log;
try {
  let pid;
  try { pid = Number(readFileSync(pidPath, 'utf8').trim()); }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  if (Number.isSafeInteger(pid) && pid > 0) {
    try {
      process.kill(pid, 0);
      if (restarting) {
        process.kill(pid, 'SIGTERM');
        if (!await waitForExit(pid, 10000)) { process.kill(pid, 'SIGKILL'); await waitForExit(pid, 2000); }
        try { unlinkSync(pidPath); } catch (error) { if (error.code !== 'ENOENT') throw error; }
        pid = undefined;
      } else {
        console.log(service + ' is already running (PID ' + pid + ')');
        process.exitCode = 0;
      }
    } catch (error) {
      if (error.code !== 'ESRCH') throw error;
      unlinkSync(pidPath);
      pid = undefined;
    }
  } else if (pid !== undefined) {
    throw new Error('Invalid PID file: ' + pidPath);
  }
  if (!pid) {
    log = openSync(join(root, 'logs', service + '.log'), 'a', 0o600);
    const command = service === 'server' ? join(repo, '.bin', 'muxlio') : process.execPath;
    const commandArgs = service === 'server' ? ['server', 'start', ...args] : [join(repo, 'muxlio-ui', 'server.mjs'), ...args];
    const child = spawn(command, commandArgs, { cwd: repo, env: { ...process.env, ...(service === 'ui' ? { HOST: process.env.HOST || '127.0.0.1' } : {}) }, detached: true, stdio: ['ignore', log, log] });
    await new Promise((resolve, reject) => {
      child.once('error', reject);
      child.once('spawn', resolve);
    });
    writeFileSync(pidPath, String(child.pid) + '\n', { mode: 0o600 });
    // Catch immediate startup failures, such as a port already being occupied.
    await new Promise(resolve => setTimeout(resolve, 300));
    if (child.exitCode !== null || child.signalCode !== null) {
      unlinkSync(pidPath);
      throw new Error(service + ' exited during startup; see ' + join(root, 'logs', service + '.log'));
    }
    child.unref();
    console.log(service + ' started (PID ' + child.pid + '); PID file: ' + pidPath);
  }
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
} finally {
  if (log !== undefined) closeSync(log);
  rmdirSync(lockPath);
}
