import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { deflateSync } from 'node:zlib';

// Public fixture credential, never used by an operator's running service.
export const token = 'isolated-e2e-fixture-password';
export const binary = resolve('.e2e/frame-tv-art-manager');

function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0);
  }
  return (crc ^ 0xffffffff) >>> 0;
}

export function png(red = 100) {
  const chunk = (name, data) => {
    const type = Buffer.from(name);
    const length = Buffer.alloc(4);
    length.writeUInt32BE(data.length);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(Buffer.concat([type, data])));
    return Buffer.concat([length, type, data, crc]);
  };
  const header = Buffer.alloc(13);
  header.writeUInt32BE(2, 0);
  header.writeUInt32BE(2, 4);
  header[8] = 8;
  header[9] = 2;
  const row = [0, red, 40, 80, red, 40, 80];
  return Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
    chunk('IHDR', header), chunk('IDAT', deflateSync(Buffer.from([...row, ...row]))), chunk('IEND', Buffer.alloc(0)),
  ]);
}

export async function workspace() {
  const root = await mkdtemp(join(tmpdir(), 'frame-e2e-'));
  return { root, artwork: join(root, 'artwork'), tokens: join(root, 'tokens') };
}

export function environment(paths, overrides = {}) {
  return {
    PATH: process.env.PATH, HOME: process.env.HOME,
    TV_IPS: '127.0.0.1', ARTWORK_DIR: paths.artwork, TOKEN_DIR: paths.tokens,
    HEALTH_BIND_ADDRESS: '127.0.0.1', HEALTH_PORT: '0',
    UPLOAD_ENABLED: 'true', UPLOAD_TOKEN: token, IMAGE_OPTIMIZE_ENABLED: 'false',
    MAX_DOWNLOAD_SIZE_MB: '1', CONNECTION_TIMEOUT_SECONDS: '1', API_TIMEOUT_SECONDS: '1',
    SHUTDOWN_TIMEOUT_SECONDS: '3', SYNC_INTERVAL_MINUTES: '60',
    ...overrides,
  };
}

export function launch(paths, overrides = {}, args = []) {
  const child = spawn(binary, args, { env: environment(paths, overrides), stdio: ['ignore', 'pipe', 'pipe'] });
  let output = '';
  child.stdout.on('data', (data) => { output += data; });
  child.stderr.on('data', (data) => { output += data; });
  const exited = once(child, 'exit');
  return { child, exited, output: () => output };
}

export async function stop(process) {
  if (process.child.exitCode !== null || process.child.signalCode !== null) return;
  process.child.kill('SIGTERM');
  const timer = setTimeout(() => process.child.kill('SIGKILL'), 5000);
  try { await process.exited; } finally { clearTimeout(timer); }
}

export async function tree(root) {
  const entries = [];
  async function visit(directory, prefix) {
    for (const item of (await readdir(directory, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
      const path = join(directory, item.name);
      const name = prefix + item.name;
      if (item.isDirectory()) await visit(path, name + '/');
      else entries.push([name, createHash('sha256').update(await readFile(path)).digest('hex')]);
    }
  }
  await visit(root, '');
  return entries;
}

export async function fixtures() {
  await mkdir('.e2e/fixtures', { recursive: true });
  await writeFile('.e2e/fixtures/landscape.png', png());
  await writeFile('.e2e/fixtures/second.png', png(170));
  await writeFile('.e2e/fixtures/invalid.png', 'not an image');
}

export async function dispose(paths) { await rm(paths.root, { recursive: true, force: true }); }
