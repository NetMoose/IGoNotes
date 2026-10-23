import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chromium } from 'playwright-core';
import { build } from 'vite';
import { resolve } from 'node:path';

// Owned/deadline-bounded by smoke.js, including CDP launch and cleanup.
const [endpoint, fixtureURL, extensionPath] = process.argv.slice(2);
// Bundle imports/dependencies using the production resolution policy. Copying
// prepare.js alone would leave broken relative/bare module imports at runtime.
const root = resolve(import.meta.dirname, '..');
const entry = resolve(root, 'tests/helpers/image-preparation-api.js');
await build({ configFile: resolve(root, 'vite.config.js'), logLevel: 'silent',
  build: { outDir: extensionPath, emptyOutDir: false, lib: { entry, formats: ['es'], fileName: () => 'prepare-under-test.js' },
    rolldownOptions: { input: entry, preserveEntrySignatures: 'strict', output: { entryFileNames: 'prepare-under-test.js' } } } });
const browser = await chromium.connectOverCDP(endpoint);
const context = browser.contexts()[0];
context.setDefaultTimeout(5000);
const worker = context.serviceWorkers()[0] ?? await context.waitForEvent('serviceworker', { timeout: 15000 });
const id = new URL(worker.url()).host;
const editor = await context.newPage();
const failures = [];
editor.on('pageerror', error => failures.push(error.message));
await editor.goto(`chrome-extension://${id}/editor.html`);
await editor.getByRole('heading', { name: 'Local draft editor', exact: true }).waitFor();
await editor.evaluate(async fixtureURL => {
  const { createImageAcquirer } = await import('./acquire-under-test.js');
  globalThis.imageAPI = createImageAcquirer({ get connection() { throw Error('No credential access'); } });
  globalThis.fixtureURL = fixtureURL;
}, fixtureURL);
assert.equal(await editor.evaluate(async () => (await imageAPI.acquire([`${fixtureURL}ok`]))[0].error.code), 'permission');

// TEST ONLY trusted setup, not a real permission prompt acceptance. Chrome's
// internal extensions page grants the optional host for this isolated profile.
const extensions = await context.newPage();
await extensions.goto('chrome://extensions');
await extensions.evaluate(async id => {
  await chrome.developerPrivate.addHostPermission(id, 'http://images.example.test/*');
}, id);
await editor.bringToFront();
await editor.evaluate(() => {
  const button = document.createElement('button');
  button.textContent = 'Test-only image access';
  button.id = 'test-image-access';
  button.onclick = () => {
    globalThis.gestureActive = navigator.userActivation.isActive;
    const result = imageAPI.requestAccessFromUserGesture([`${fixtureURL}ok`, `${fixtureURL}ok#alias`]);
    result.then(value => { globalThis.accessResult = value; });
  };
  document.body.append(button);
});
await editor.locator('#test-image-access').click();
await editor.waitForFunction(() => globalThis.accessResult);
assert.deepEqual(await editor.evaluate(() => ({ gesture: gestureActive, result: accessResult })), {
  gesture: true, result: { ok: true, origins: ['http://images.example.test/*'] },
});

// A real cookie for the image host exists; acquisition must not send it. No
// backend credential is configured or exposed to the module for this check.
await context.addCookies([{ name: 'imagehost', value: 'private', url: fixtureURL }]);
assert.ok((await context.cookies(fixtureURL)).some(cookie => cookie.name === 'imagehost'));
const results = await editor.evaluate(async () => {
  const entries = await imageAPI.acquire([`${fixtureURL}ok`, `${fixtureURL}redirect`, `${fixtureURL}missing`]);
  return Promise.all(entries.map(async ({ blob, ...entry }) => ({ ...entry, ...(blob ? { bytes: [...new Uint8Array(await blob.arrayBuffer())] } : {}) })));
});
assert.equal(results[0].ok, true);
assert.equal(results[0].mediaType, 'image/gif');
assert.equal(results[0].bytes.length, results[0].size);
const expectedBytes = Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==', 'base64');
assert.deepEqual(results[0].bytes, [...expectedBytes]);
assert.equal(results[0].sha256, createHash('sha256').update(expectedBytes).digest('hex'));
assert.equal(results[1].error.code, 'network');
assert.match(results[1].error.message, /redirect/);
assert.equal(results[2].error.code, 'http');
assert.equal(results[2].error.status, 404);

const nativeLogURL = fixtureURL.replace('images.example.test', '127.0.0.1');
const requests = await (await fetch(`${nativeLogURL}log`)).json();
assert.deepEqual(requests.map(x => x.path).sort(), ['/missing', '/ok', '/redirect']);
assert.ok(requests.every(x => x.host.startsWith('images.example.test:') && !x.authorization && !x.cookie));
const preparation = await editor.evaluate(async () => {
  const { createDraftStore, prepareDraftImages } = await import('./prepare-under-test.js');
  const store = createDraftStore({ name: 'smoke-image-preparation' });
  const urls = [`${fixtureURL}ok#one`, `${fixtureURL}same-bytes`];
  const markdown = urls.map(url => `![image](${url})`).join('\n') + '\n`![literal](https://never.example/x)`';
  await store.saveDraft({ id: 'offline', title: 'native', filename: 'note.md', markdown, sourceUrl: 'https://example.org/article', capturedAt: '2026-10-23T10:00:00Z', base: null, parent: '', destinationRevision: null, status: 'draft' });
  const result = await prepareDraftImages(store, 'offline', { acquirer: imageAPI });
  const assets = await store.getAssets('offline');
  store.close();
  return { ok: result.ok, count: result.assets.length, blobs: assets.filter(row => row.blob).length, bytes: [...new Uint8Array(await result.blobs[0].blob.arrayBuffer())] };
});
assert.deepEqual(preparation, { ok: true, count: 2, blobs: 1, bytes: [...expectedBytes] });
await extensions.evaluate(async id => {
  await chrome.developerPrivate.removeHostPermission(id, 'http://images.example.test/*');
}, id);
assert.equal(await editor.evaluate(async () => (await imageAPI.acquire([`${fixtureURL}ok`]))[0].error.code), 'permission');
// Entire extension page is closed/reopened: no retained JS store/Blob handles,
// source page, permission, token, or running IGoNotes is needed to reuse bytes.
await editor.close();
const reopened = await context.newPage();
reopened.on('pageerror', error => failures.push(error.message));
await reopened.goto(`chrome-extension://${id}/editor.html`);
const offline = await reopened.evaluate(async () => {
  const { createDraftStore, prepareDraftImages } = await import('./prepare-under-test.js');
  const store = createDraftStore({ name: 'smoke-image-preparation' });
  const result = await prepareDraftImages(store, 'offline', { acquirer: { acquire() { throw Error('Must not refetch cached bytes'); } } });
  const bytes = [...new Uint8Array(await result.blobs[0].blob.arrayBuffer())];
  store.close();
  return { ok: result.ok, count: result.assets.length, bytes };
});
assert.deepEqual(offline, { ok: true, count: 2, bytes: [...expectedBytes] });
const after = await (await fetch(`${nativeLogURL}log`)).json();
assert.deepEqual(after.map(x => x.path).sort(), ['/missing', '/ok', '/ok', '/redirect', '/same-bytes']);
assert.ok(after.every(x => !x.authorization && !x.cookie));
assert.deepEqual(failures, []);
process.send?.({ ok: true, acquisition: 'native ESM, missing/granted/revoked optional host, gesture request on pregrant, exact bytes/hash, redirect blocked, 404, cookie omission', preparation: 'native IndexedDB Blob/hash/status, physical byte deduplication, editor close/reopen after permission revocation with zero acquisition calls', permissionBoundary: 'trusted developerPrivate pregrant; real permission prompt grant/denial NOT manually tested', browser: browser.version() });
await browser.close();
