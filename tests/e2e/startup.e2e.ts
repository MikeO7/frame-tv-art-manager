import { createServer } from 'node:http';
import { writeFile } from 'node:fs/promises';
import { test } from 'e2e';
import { expect } from 'e2e';
import { workspace, launch, stop, dispose, tree, png } from '../../scripts/e2e/runtime.mjs';

test('CLI help/version and invalid configuration exit without creating state', async () => {
  const paths = await workspace();
  try {
    for (const args of [['--help'], ['--version']]) {
      const process = launch(paths, {}, args);
      expect((await process.exited)[0]).toBe(0);
      expect(process.output()).toContain(args[0] === '--help' ? 'Usage' : 'dev');
    }
    for (const override of [
      { TV_IPS: '' }, { TV_IPS: 'not-an-ip' }, { TV_IPS: '127.0.0.1,127.0.0.1' },
      { UPLOAD_TOKEN: 'short' }, { HEALTH_PORT: '70000' }, { PORTRAIT_MODE: 'unknown' },
      { SLIDESHOW_INTERVAL: '30' }, { IMAGE_JPEG_QUALITY: '101' }, { AUTO_OFF_TIME: '25:00' },
    ]) {
      const process = launch(paths, override);
      expect((await process.exited)[0]).toBe(1);
      expect(process.output()).toContain('load config');
      expect(await tree(paths.root)).toEqual([]);
    }
  } finally { await dispose(paths); }
});

test('startup bootstraps sources; dry-run preserves bytes and disables uploads', async () => {
  const paths = await workspace();
  let process;
  try {
    process = launch(paths, { ARTWORK_SOURCES_FILE: paths.root + '/sources.yaml', HEALTH_PORT: '39061' });
    await expect.poll(async () => {
      try { return (await fetch('http://127.0.0.1:39061/live')).status; } catch { return 0; }
    }).toBe(200);
    await stop(process);
    expect((await tree(paths.root)).some(([name]) => name === 'sources.yaml')).toBe(true);
    const before = await tree(paths.root);
    process = launch(paths, { ARTWORK_SOURCES_FILE: paths.root + '/sources.yaml', HEALTH_PORT: '39061', DRY_RUN: 'true' });
    await expect.poll(async () => {
      try { return (await fetch('http://127.0.0.1:39061/live')).status; } catch { return 0; }
    }).toBe(200);
    expect((await fetch('http://127.0.0.1:39061/upload')).status).toBe(403);
    await stop(process);
    expect(await tree(paths.root)).toEqual(before);
    expect(process.output()).toContain('Shutdown complete');
    expect((await process.exited)[0]).toBe(0);
  } finally { if (process) await stop(process); await dispose(paths); }
});

test('disabled uploads return forbidden and bind collisions fail startup', async () => {
  const paths = await workspace();
  let process;
  try {
    process = launch(paths, { HEALTH_PORT: '39062', UPLOAD_ENABLED: 'false' });
    await expect.poll(async () => {
      try { return (await fetch('http://127.0.0.1:39062/live')).status; } catch { return 0; }
    }).toBe(200);
    expect((await fetch('http://127.0.0.1:39062/upload')).status).toBe(403);
    const other = await workspace();
    try {
      const conflict = launch(other, { HEALTH_PORT: '39062' });
      expect((await conflict.exited)[0]).toBe(1);
      expect(conflict.output()).toContain('address already in use');
    } finally { await dispose(other); }
  } finally { if (process) await stop(process); await dispose(paths); }
});

test('direct-source import survives a later provider failure', async () => {
  const paths = await workspace();
  let fail = false;
  const provider = createServer((_, response) => {
    response.writeHead(fail ? 503 : 200, { 'Content-Type': 'image/png' });
    response.end(fail ? 'unavailable' : png(211));
  });
  await new Promise<void>((resolve) => provider.listen(0, '127.0.0.1', resolve));
  let process;
  try {
    const address = provider.address() as { port: number };
    await writeFile(paths.root + '/sources.txt', `http://127.0.0.1:${address.port}/art.png\n`);
    process = launch(paths, { ARTWORK_SOURCES_FILE: paths.root + '/sources.txt', HEALTH_PORT: '39063' });
    await expect.poll(async () => (await tree(paths.root)).filter(([name]) => name.endsWith('.png')).length).toBe(1);
    await stop(process);
    const before = (await tree(paths.artwork)).filter(([name]) => name.endsWith('.png'));
    fail = true;
    await writeFile(paths.root + '/sources.txt', `http://127.0.0.1:${address.port}/art.png\nhttp://127.0.0.1:${address.port}/new.png\n`);
    process = launch(paths, { ARTWORK_SOURCES_FILE: paths.root + '/sources.txt', HEALTH_PORT: '39063' });
    await expect.poll(() => process.output()).toContain('source');
    await expect.poll(() => process.output()).toContain('503');
    await stop(process);
    expect((await tree(paths.artwork)).filter(([name]) => name.endsWith('.png'))).toEqual(before);
  } finally {
    if (process) await stop(process);
    await new Promise<void>((resolve, reject) => provider.close((error) => error ? reject(error) : resolve()));
    await dispose(paths);
  }
});
