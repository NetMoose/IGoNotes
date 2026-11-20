import { extensionWorker } from '../tests/browser/extension-worker.js';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chromium } from 'playwright-core';
import { build } from 'vite';
import { resolve } from 'node:path';
import { fenceNetwork } from '../tests/browser/network.js';

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
const worker = await extensionWorker(context);
const id = new URL(worker.url()).host;
const editor = await context.newPage();
const failures = [];
await fenceNetwork(context, failures);
editor.on('pageerror', error => failures.push(error.message));
await editor.goto(`chrome-extension://${id}/editor.html`);
await editor.getByRole('heading', { name: 'Local draft editor', exact: true }).waitFor();
await editor.evaluate(async fixtureURL => {
  const { createImageAcquirer } = await import('./acquire-under-test.js');
  globalThis.imageAPI = createImageAcquirer({ get connection() { throw Error('No credential access'); } });
  globalThis.fixtureURL = fixtureURL;
}, fixtureURL);
assert.equal(await editor.evaluate(async fixtureURL => {
  const { createImageAcquirer } = await import('./acquire-under-test.js');
  return (await createImageAcquirer().acquire([`${fixtureURL}ok`]))[0].error.code;
}, fixtureURL), 'permission');

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
// Actual production Editor UI, default store and native IDB. First a data image
// with no host permission, then unsaved HTTP edits and the explicit Prepare click
// on the pregranted public-named loopback fixture (never direct 127.0.0.1).
await editor.evaluate(async () => {
  const { createDraftStore } = await import('./prepare-under-test.js');
  const store = createDraftStore();
  await store.saveDraft({ id: 'ui-images', title: 'UI preparation', filename: 'ui.md',
    markdown: '![data](data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==)',
    sourceUrl: 'https://example.org/article', capturedAt: '2026-10-23T10:00:00Z', base: null, parent: '', destinationRevision: null, status: 'draft' });
  store.close();
});
await editor.goto(`chrome-extension://${id}/editor.html?draft=ui-images`);
await editor.getByText('Pending', { exact: true }).waitFor();
assert.equal(await editor.locator('img').count(), 0);
await editor.getByRole('button', { name: 'Prepare images', exact: true }).click();
await editor.getByText('Stored locally', { exact: true }).waitFor();
await editor.getByText('Image preparation finished. Review each image below.', { exact: true }).waitFor();
const readUI = () => editor.evaluate(async () => {
  const { createDraftStore } = await import('./prepare-under-test.js');
  const store = createDraftStore(); const bundle = await store.readBundle('ui-images'); store.close();
  return { markdown: bundle.draft.markdown, blobs: bundle.assets.filter(row => row.blob).length,
    ready: bundle.assets.filter(row => row.descriptor.status === 'ready').length };
});
assert.equal((await readUI()).blobs, 1);
const uiBody = `![data](data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==)\n![http](${fixtureURL}ui-ok)\n![404](${fixtureURL}missing)`;
await editor.getByLabel('Markdown', { exact: true }).fill(uiBody);
// Instrument only the native request invocation; keep its actual implementation.
await editor.evaluate(() => {
  const request = chrome.permissions.request.bind(chrome.permissions);
  chrome.permissions.request = options => {
    globalThis.uiGesture = navigator.userActivation.isActive;
    globalThis.uiOrigins = options.origins;
    return request(options);
  };
});
await editor.getByRole('button', { name: 'Retry images / grant access', exact: true }).click();
await editor.getByText('Image preparation finished. Review each image below.', { exact: true }).waitFor();
assert.equal(await editor.getByText('Stored locally', { exact: true }).count(), 2);
await editor.getByText(/http \(404\)/).waitFor();
assert.deepEqual(await editor.evaluate(() => ({ gesture: uiGesture, origins: uiOrigins })), { gesture: true, origins: ['http://images.example.test/*'] });
assert.deepEqual(await readUI(), { markdown: uiBody, blobs: 1, ready: 2 });
const external = editor.getByLabel(`Keep external reference: ${fixtureURL}missing`, { exact: true });
assert.equal(await external.isChecked(), false);
await external.check();
await editor.getByRole('button', { name: 'Confirm external references', exact: true }).click();
await editor.getByText('External confirmed', { exact: true }).waitFor();
await editor.getByText('This material is not fully offline / uses external images.', { exact: true }).waitFor();
assert.equal(await editor.locator('img').count(), 0);
await editor.reload();
await editor.getByText('Failed', { exact: true }).waitFor();
assert.equal(await editor.getByText('Stored locally', { exact: true }).count(), 2);
assert.equal(await editor.getByLabel(`Keep external reference: ${fixtureURL}missing`, { exact: true }).isChecked(), false);
await extensions.evaluate(async id => {
  await chrome.developerPrivate.removeHostPermission(id, 'http://images.example.test/*');
}, id);
assert.equal(await editor.evaluate(async fixtureURL => {
  const { createImageAcquirer } = await import('./acquire-under-test.js');
  return (await createImageAcquirer().acquire([`${fixtureURL}ok`]))[0].error.code;
}, fixtureURL), 'permission');
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
const snapshot = await reopened.evaluate(async () => {
  const { createDraftStore, buildImportSnapshot, manifestDigest } = await import('./prepare-under-test.js');
  const store = createDraftStore({ name: 'smoke-image-preparation' });
  const draft = await store.getDraft('offline');
  await store.saveDraft({ ...draft, base: 'work', parent: 'research/articles', destinationRevision: 'retained-revision' });
  const bundle = await store.readBundle('offline');
  const built = await buildImportSnapshot({ ...bundle, endpoint: 'http://localhost:8080/', importId: '550e8400-e29b-41d4-a716-446655440000', confirmedFallbacks: [] });
  const saved = await store.readBundle('offline');
  const result = { endpoint: built.endpoint, markdown: built.manifest.markdown, assetCount: built.manifest.assets.length,
    digestMatches: built.digest === await manifestDigest(built.manifest), fingerprint: built.preparationFingerprint,
    frozen: Object.isFrozen(built.manifest.assets[0]) && Object.isFrozen(built.assetBlobs) && Object.isFrozen(built.sourceAliases[0]),
    blobBytes: [...new Uint8Array(await Object.values(built.assetBlobs)[0].arrayBuffer())],
    unchanged: saved.draft.markdown === draft.markdown && saved.attempts.length === 0,
    aliases: built.sourceAliases.length, autonomous: built.autonomous };
  store.close();
  return result;
});
assert.equal(snapshot.endpoint, 'http://localhost:8080');
assert.equal(snapshot.assetCount, 1);
assert.equal(snapshot.aliases, 2);
assert.equal(snapshot.digestMatches, true);
assert.match(snapshot.fingerprint, /^[0-9a-f]{64}$/);
assert.equal(snapshot.frozen, true);
assert.equal(snapshot.unchanged, true);
assert.equal(snapshot.autonomous, true);
assert.deepEqual(snapshot.blobBytes, [...expectedBytes]);
assert.equal(snapshot.markdown.split(`../../assets/images/clip-550e8400-e29b-41d4-a716-446655440000-${results[0].sha256}.gif`).length, 3);
assert.ok(snapshot.markdown.endsWith('`![literal](https://never.example/x)`'));
const after = await (await fetch(`${nativeLogURL}log`)).json();
assert.deepEqual(after.map(x => x.path).sort(), ['/missing', '/missing', '/ok', '/ok', '/redirect', '/same-bytes', '/ui-ok']);
assert.ok(after.every(x => !x.authorization && !x.cookie));
assert.deepEqual(failures, []);
process.send?.({ ok: true, browser: browser.version(), nativeImageAcquisition: true, offlineCachedBytes: true, revokedPermission: true, cookieAndBearerOmitted: true,
  acquisition: 'native ESM, missing/granted/revoked optional host, gesture request on pregrant, exact bytes/hash, redirect blocked, 404, cookie omission',
  preparation: 'actual Editor Prepare/Retry, data and HTTP, flush unsaved body, native IDB canonical Blob, 404 status, separate external confirmation, reload resets consent; native module offline reopen',
  snapshot: 'native ESM, atomic bundle, nested destinations, immutable manifest/Blobs, matching digest, no source fetch or store mutation',
  permissionBoundary: 'trusted developerPrivate pregrant; actual Editor click user gesture verified; real permission prompt grant/denial NOT manually tested' });
await browser.close();
