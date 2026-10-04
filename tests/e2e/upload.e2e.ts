import { readFile } from 'node:fs/promises';
import { test } from '@e2e-dev/web';
import { expect } from 'e2e';
import { png, token, tree, launch, stop } from '../../scripts/e2e/runtime.mjs';

const auth = 'Basic ' + Buffer.from('frame:' + token).toString('base64');
const paths = async () => JSON.parse(await readFile('.e2e/runtime.json', 'utf8'));
const upload = (baseUrl: string, body: BodyInit, headers = {}) => fetch(new URL('/upload', baseUrl), {
  method: 'POST', headers: { Authorization: auth, 'Content-Type': 'image/png', ...headers }, body,
});

test('liveness stays healthy while unreachable TV prevents readiness', async ({ app }) => {
  const live = await fetch(new URL('/live', app.baseUrl));
  expect(live.status).toBe(200);
  expect((await live.json()).status).toBe('ok');
  await expect.poll(async () => (await (await fetch(new URL('/status', app.baseUrl))).json()).sync_count).toBeGreaterThan(0);
  for (const route of ['/ready', '/health']) {
    const response = await fetch(new URL(route, app.baseUrl));
    expect(response.status).toBe(503);
    const status = await response.json();
    expect(status.last_sync_ok).toBe(false);
    expect(status.last_error).not.toBe('');
  }
  const status = await (await fetch(new URL('/status', app.baseUrl))).json();
  expect(status.tvs['127.0.0.1'].status).not.toBe('ok');
  expect((await fetch(new URL('/does-not-exist', app.baseUrl))).status).toBe(404);
  for (const [argument, code] of [['-livenesscheck', 0], ['-healthcheck', 1]] as const) {
    const process = launch(await paths(), { HEALTH_PORT: '39060' }, [argument]);
    try { expect((await process.exited)[0]).toBe(code); } finally { await stop(process); }
  }
});

test('unauthorized and wrong-account uploads cannot mutate the collection', async ({ app }) => {
  const before = await tree((await paths()).root);
  for (const credential of ['', 'Basic ' + Buffer.from('frame:wrong').toString('base64'), 'Basic ' + Buffer.from('other:' + token).toString('base64')]) {
    for (const method of ['GET', 'POST']) {
      const response = await fetch(new URL('/upload', app.baseUrl), { method, headers: { Authorization: credential }, ...(method === 'POST' ? { body: png() } : {}) });
      expect(response.status).toBe(401);
      expect(response.headers.get('www-authenticate')).toContain('Basic');
    }
  }
  expect(await tree((await paths()).root)).toEqual(before);
});

test('invalid and oversized uploads are rejected without durable writes', async ({ app }) => {
  const before = await tree((await paths()).root);
  for (const body of [Buffer.alloc(0), Buffer.from('not an image'), png().subarray(0, 40), Buffer.alloc(1024 * 1024 + 1)]) {
    expect((await upload(app.baseUrl, body)).status).toBe(400);
  }
  const missing = new FormData();
  missing.set('wrong-field', new Blob([png()]), 'image.png');
  expect((await fetch(new URL('/upload', app.baseUrl), { method: 'POST', headers: { Authorization: auth }, body: missing })).status).toBe(400);
  expect((await upload(app.baseUrl, png(), { 'Content-Type': 'multipart/form-data; boundary=missing' })).status).toBe(400);
  const method = await fetch(new URL('/upload', app.baseUrl), { method: 'DELETE', headers: { Authorization: auth } });
  expect(method.status).toBe(405);
  expect(method.headers.get('allow')).toBe('GET, POST');
  expect(await tree((await paths()).root)).toEqual(before);
});

test('cross-origin and malformed Origin values are rejected before import', async ({ app }) => {
  const before = await tree((await paths()).root);
  const host = new URL(app.baseUrl).origin;
  for (const origin of ['', 'https://attacker.example', 'null', host + '/path', host + '?query', host + '#fragment', host.replace('://', '://attacker@'), host + 'a'.repeat(2048)]) {
    expect((await upload(app.baseUrl, png(201), { Origin: origin })).status).toBe(403);
  }
  expect(await tree((await paths()).root)).toEqual(before);
});

test('raw Shortcut upload commits one image and deduplicates repeat bytes', async ({ app }) => {
  const first = await upload(app.baseUrl, png(123), { Origin: new URL(app.baseUrl).origin });
  expect(first.status).toBe(200);
  const result = await first.json();
  expect(result.status).toBe('ok');
  expect(result.filename).toMatch(/^upload--[a-f0-9]+\.png$/);
  expect(await readFile((await paths()).artwork + '/' + result.filename)).toEqual(png(123));
  const before = await tree((await paths()).root);
  const duplicate = await upload(app.baseUrl, png(123));
  expect(duplicate.status).toBe(200);
  expect((await duplicate.json()).message).toContain('deduplicated');
  expect(await tree((await paths()).root)).toEqual(before);
});

test('browser uploader handles multiple files, duplicates, and invalid images', async ({ app, screen, browser }) => {
  await app.open('/upload');
  await expect(screen.getByRole('heading', 'Frame TV Art Uploader')).toBeVisible();
  await expect(browser).toHaveTitle('Frame TV Art Uploader');
  const response = await fetch(new URL('/upload', app.baseUrl), { headers: { Authorization: auth } });
  expect(response.headers.get('content-security-policy')).toContain("frame-ancestors 'none'");
  expect(response.headers.get('cache-control')).toBe('no-store');
  await browser.locator('#file-input').setInputFiles(['.e2e/fixtures/landscape.png', '.e2e/fixtures/second.png', '.e2e/fixtures/invalid.png']);
  await expect(screen.getByText('Success')).toHaveCount(2);
  await expect(screen.getByText('Invalid or unsafe image')).toBeVisible();
  await expect(browser.locator('#progress-bar')).toBeHidden();
  await app.open('/upload');
  await browser.locator('#file-input').setInputFiles('.e2e/fixtures/landscape.png');
  await expect(screen.getByText('Deduplicated')).toBeVisible();
  await browser.setViewport({ width: 390, height: 844 });
  await expect(screen.getByRole('heading', 'Frame TV Art Uploader')).toBeVisible();
  expect(await browser.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await app.screenshot('uploader-phone');
});

test('browser reports empty, pending, and network-failure states', async ({ app, screen, browser }) => {
  await app.open('/upload');
  await expect(browser.locator('.file-item')).toHaveCount(0);
  let release: () => void;
  const blocked = new Promise<void>((resolve) => { release = resolve; });
  let entered = false;
  await browser.route('**/upload', async (route) => {
    if (route.request.method !== 'POST') { await route.continue(); return; }
    entered = true;
    await blocked;
    await route.abort();
  });
  try {
    await browser.locator('#file-input').setInputFiles('.e2e/fixtures/landscape.png');
    await expect.poll(() => entered).toBe(true);
    await expect(screen.getByText('Uploading...')).toBeVisible();
    await expect(browser.locator('#progress-bar')).toBeVisible();
  } finally { release!(); }
  await expect(screen.getByText('Network Error')).toBeVisible();
  await expect(browser.locator('#progress-bar')).toBeHidden();
  await app.screenshot('uploader-network-failure');
});
