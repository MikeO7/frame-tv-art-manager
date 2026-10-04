import { spawnSync } from 'node:child_process';
import { mkdir, writeFile } from 'node:fs/promises';
import { fixtures } from './runtime.mjs';

await mkdir('.e2e', { recursive: true });
const build = spawnSync('go', ['build', '-o', '.e2e/frame-tv-art-manager', './cmd/frame-tv-art-manager'], { stdio: 'inherit' });
if (build.status !== 0) process.exit(build.status ?? 1);
await fixtures();
const revision = spawnSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).stdout.trim();
await writeFile('.e2e/environment.json', JSON.stringify({
  revision, command: ['pnpm', 'test:e2e', ...process.argv.slice(2)],
  node: process.version, platform: process.platform, arch: process.arch,
  data: 'Fresh temporary directories; generated 2x2 PNG fixtures; loopback unreachable TV only',
  e2e: '0.17.0', web: '0.12.0',
}, null, 2) + '\n');
const run = spawnSync('pnpm', ['exec', 'e2e', 'run', ...process.argv.slice(2)], {
  stdio: 'inherit', env: { ...process.env, E2E_TELEMETRY_DISABLED: '1' },
});
process.exitCode = run.status ?? 1;
