import { spawnSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

// Run from the repository root. Discovery is explicit, never a compile-time effect.
export function discoverPackages(root = process.cwd()) {
  const packages = [];
  for (const path of ['muxlio-core', 'muxlio-adapters']) {
    const result = spawnSync('go', ['list', '-f', '{{.Dir}}', './...'], {
      cwd: resolve(root, path), encoding: 'utf8', timeout: 120000,
    });
    if (result.error || result.signal || result.status !== 0) {
      throw new Error(`Cannot discover ${path}: ${result.error?.message || result.stderr}`);
    }
    const directories = result.stdout.trim().split('\n').filter(Boolean).sort();
    for (const directory of directories) {
      const suffix = relative(resolve(root, path), directory).split(sep).join('/');
      packages.push({ id: `${path}-${suffix || 'root'}`.replaceAll('/', '.'), path,
        kind: 'go', target: suffix ? `./${suffix}` : '.' });
    }
  }
  const manifest = JSON.parse(readFileSync(resolve(root, 'package.json'), 'utf8'));
  for (const path of manifest.workspaces) {
    const pkg = JSON.parse(readFileSync(resolve(root, path, 'package.json'), 'utf8'));
    packages.push({ id: path.replaceAll('/', '.'), path, kind: 'npm', target: pkg.name });
  }
  packages.push({ id: 'muxlio-ui', path: 'muxlio-ui', kind: 'ui', target: '.' });
  if (!packages.length || packages.length > 200 || new Set(packages.map(pkg => pkg.id)).size !== packages.length) {
    throw new Error('Discovery requires 1–200 packages with unique ids');
  }
  return { packages };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  if (args.length && (args.length !== 2 || args[0] !== '--out')) {
    throw new Error('Usage: node recipes/fix-package-bugs/discover.mjs [--out input.json]');
  }
  const json = JSON.stringify(discoverPackages(), null, 2) + '\n';
  if (args[0] === '--out') writeFileSync(args[1], json);
  else process.stdout.write(json);
}
