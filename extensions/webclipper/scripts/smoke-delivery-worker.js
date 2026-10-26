import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { chromium } from 'playwright-core';
import { fenceNetwork } from '../tests/browser/network.js';

const [endpoint, fixtureURL] = process.argv.slice(2);
const home = process.env.CLIPPER_SMOKE_HOME, backend = process.env.CLIPPER_SMOKE_BACKEND;
let browser;
try {
  browser = await chromium.connectOverCDP(endpoint);
  const context = browser.contexts()[0]; context.setDefaultTimeout(7000);
  const worker = context.serviceWorkers()[0] ?? await context.waitForEvent('serviceworker');
  const id = new URL(worker.url()).host;
  const failures = [], requests = [];
  await fenceNetwork(context, failures);
  context.on('page', page => page.on('pageerror', error => failures.push(error.message)));
  context.on('request', req => { if (req.url().includes('/api/clipper/v1/')) requests.push({ method: req.method(), url: req.url() }); });
  const popup = await context.newPage(); await popup.goto(`chrome-extension://${id}/popup.html`);
  const source = await context.newPage(); await source.goto(fixtureURL);
  const captures = [];
  async function capture() {
    await source.bringToFront();
    const opened = context.waitForEvent('page');
    await popup.evaluate(() => [...document.querySelectorAll('button')].find(b => b.textContent === 'Article').click());
    const editor = await opened;
    await editor.getByLabel('Markdown', { exact: true }).waitFor();
    const draftId = new URL(editor.url()).searchParams.get('draft'); captures.push(draftId);
    return editor;
  }
  async function bundle(editor, draftId) {
    return editor.evaluate(async draftId => {
      const db = await new Promise((resolve, reject) => { const r = indexedDB.open('igonotes-webclipper', 1); r.onsuccess = () => resolve(r.result); r.onerror = () => reject(r.error); });
      const result = await new Promise((resolve, reject) => {
        const tx = db.transaction(['drafts', 'assets', 'attempts'], 'readonly'), value = {};
        for (const name of ['drafts', 'assets', 'attempts']) {
          const req = name === 'drafts' ? tx.objectStore(name).get(draftId) : tx.objectStore(name).index('draftId').getAll(draftId);
          req.onsuccess = () => { value[name] = req.result; };
        }
        tx.oncomplete = () => resolve(value); tx.onabort = () => reject(tx.error);
      }); db.close(); return result;
    }, draftId);
  }
  async function prepareAndConfirm(editor, filename) {
    await editor.getByLabel('Filename', { exact: true }).fill(filename);
    await editor.getByRole('button', { name: 'Prepare images', exact: true }).click();
    await editor.getByText('Image preparation finished. Review each image below.', { exact: true }).waitFor();
    await editor.getByText(/1 \/ 1 stored locally/).waitFor();
    await editor.getByRole('button', { name: 'Load destinations', exact: true }).click();
    const base = editor.getByLabel('Destination base', { exact: true }); await base.waitFor();
    assert.equal(await base.inputValue(), '');
    await base.selectOption('inactive');
    const folder = editor.getByLabel('Destination folder', { exact: true }); await folder.waitFor();
    assert.equal(await folder.inputValue(), '/');
    await folder.selectOption('research/articles');
    await editor.getByRole('button', { name: 'Confirm destination', exact: true }).click();
    await editor.getByText('Confirmed destination: inactive / research/articles', { exact: true }).waitFor();
  }
  const editor = await capture();
  assert.equal(requests.length, 0, 'capture and mount must not contact IGoNotes');
  await editor.getByRole('button', { name: 'Connection settings', exact: true }).click();
  await editor.getByLabel('Endpoint', { exact: true }).fill(fixtureURL.slice(0, -1));
  await editor.getByLabel('Connection token', { exact: true }).fill(process.env.CLIPPER_SMOKE_SECRET);
  await editor.getByRole('button', { name: 'Save connection', exact: true }).click();
  await editor.getByText('Connection saved locally. Not tested.', { exact: true }).waitFor();
  assert.equal(requests.length, 0, 'saving a connection is local');
  await editor.getByRole('button', { name: 'Close connection settings', exact: true }).click();
  await prepareAndConfirm(editor, 'normal.md');
  await editor.getByRole('button', { name: 'Send', exact: true }).click();
  await editor.getByRole('heading', { name: 'Import confirmed', exact: true }).waitFor().catch(async error => {
    throw Error(`${error.message}\nUI: ${await editor.locator('main').innerText()}\nProxy: ${JSON.stringify(await (await fetch(`${fixtureURL}stats`)).json())}`);
  });
  assert.equal((await bundle(editor, captures[0])).drafts, undefined);
  assert.equal(new URL(editor.url()).searchParams.has('draft'), false);

  const lost = await capture(); await prepareAndConfirm(lost, 'lost.md');
  await fetch(`${fixtureURL}arm-drop`, { method: 'POST' });
  await lost.getByRole('button', { name: 'Send', exact: true }).click();
  await lost.getByText(/Delivery uncertain/).waitFor({ timeout: 15000 }).catch(async error => {
    throw Error(`${error.message}\nUI: ${await lost.locator('main').innerText()}\nProxy: ${JSON.stringify(await (await fetch(`${fixtureURL}stats`)).json())}`);
  });
  assert.equal(await lost.getByLabel('Markdown', { exact: true }).isDisabled(), true);
  const uncertain = await bundle(lost, captures[1]);
  assert.equal(uncertain.drafts.status, 'uncertain');
  assert.equal(uncertain.attempts[0].phase, 'posting');
  assert.equal(uncertain.assets.filter(a => a.blob).length, 1);
  const attempt = uncertain.attempts[0], manifest = attempt.manifest;
  assert.match(manifest.markdown, /\.\.\/\.\.\/assets\/images\/clip-/);
  await lost.getByRole('button', { name: 'Check attempt', exact: true }).click();
  await lost.getByRole('heading', { name: 'Import confirmed', exact: true }).waitFor();
  const after = await bundle(lost, captures[1]);
  assert.equal(after.drafts, undefined); assert.deepEqual(after.assets, []); assert.deepEqual(after.attempts, []);
  const stats = await (await fetch(`${fixtureURL}stats`)).json();
  assert.equal(stats.posts, 2); assert.equal(stats.gets, 1); assert.equal(stats.dropped, 1);
  const receipt = stats.receipts.find(r => r.import_id === attempt.id);
  assert.equal(receipt.revision, `sha256:${createHash('sha256').update(manifest.markdown).digest('hex')}`);
  assert.equal(receipt.base, 'inactive'); assert.equal(receipt.path, 'research/articles/lost.md');
  assert.deepEqual(receipt.warnings, []);
  for (const name of ['normal.md', 'lost.md']) assert.match(readFileSync(join(home, 'bases/inactive/research/articles', name), 'utf8'), /\.\.\/\.\.\/assets\/images\/clip-/);
  assert.equal(readFileSync(join(home, 'bases/inactive/research/articles/lost.md'), 'utf8'), manifest.markdown);
  const imageFiles = readdirSync(join(home, 'bases/inactive/assets/images'));
  assert.equal(imageFiles.length, 2);
  for (const asset of manifest.assets) {
    const file = imageFiles.find(name => name.startsWith(`clip-${manifest.import_id}-${asset.sha256}`)); assert.ok(file);
    assert.equal(createHash('sha256').update(readFileSync(join(home, 'bases/inactive/assets/images', file))).digest('hex'), asset.sha256);
  }
  const config = await (await fetch(`${backend}/api/config`)).json();
  assert.equal(config.current_base, 'active');
  assert.deepEqual(await (await fetch(`${backend}/api/notes`)).json(), []);
  assert.deepEqual(readdirSync(join(home, 'bases/active')), []);
  // Observe headers received by the local HTTP proxy from REAL extension fetch.
  // A privileged extension may bypass CORS preflight; do not manufacture one.
  const nativeHeaders = stats.headers.filter(h => h.bearer && h.site !== null);
  assert.ok(nativeHeaders.some(h => h.method === 'GET' && h.status === 200));
  assert.ok(nativeHeaders.some(h => h.method === 'POST' && h.status === 201));
  for (const h of nativeHeaders) {
    assert.ok(h.origin === null || h.origin === `chrome-extension://${id}`);
    assert.ok(['cross-site', 'none'].includes(h.site));
    assert.equal(h.mode, 'cors'); assert.equal(h.dest, 'empty');
  }
  // Explicit harness contract, separately labelled from native browser traffic.
  const preflight = await fetch(`${fixtureURL}api/clipper/v1/imports`, { method: 'OPTIONS', headers: {
    Origin: `chrome-extension://${id}`, 'Access-Control-Request-Method': 'POST', 'Access-Control-Request-Headers': 'authorization, content-type' } });
  assert.equal(preflight.status, 204);
  assert.equal(preflight.headers.get('Access-Control-Allow-Origin'), `chrome-extension://${id}`);
  assert.equal(preflight.headers.get('Access-Control-Allow-Methods'), 'POST');
  assert.equal(preflight.headers.get('Access-Control-Allow-Headers'), 'Authorization, Content-Type');
  assert.equal(preflight.headers.get('Access-Control-Allow-Credentials'), null);
  // Hostile WEB origin knows this isolated fixture token intentionally. Browser
  // CORS must reject its preflight before ANY import POST/publication reaches Go.
  const hostile = await context.newPage();
  const hostileURL = fixtureURL.replace('127.0.0.1', 'hostile.example.test');
  await hostile.goto(hostileURL);
  const refused = await hostile.evaluate(async ({ url, token }) => {
    try { await fetch(`${url}api/clipper/v1/imports`, { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: '{}' }); return false; }
    catch { return true; }
  }, { url: fixtureURL, token: process.env.CLIPPER_SMOKE_SECRET });
  assert.equal(refused, true);
  const security = await (await fetch(`${fixtureURL}stats`)).json();
  const hostileOptions = security.headers.filter(h => h.origin === hostileURL.slice(0, -1));
  assert.equal(hostileOptions.length, 1);
  assert.equal(hostileOptions[0].method, 'OPTIONS'); assert.equal(hostileOptions[0].status, 403);
  assert.equal(hostileOptions[0].bearer, false); assert.equal(security.posts, stats.posts);
  assert.deepEqual(readdirSync(join(home, 'bases/inactive/research/articles')).sort(), ['lost.md', 'normal.md']);
  // Inactive imports intentionally do not populate the active notes index.
  // Switching explicitly now verifies that reindex exposes the nested notes.
  const switched = await fetch(`${backend}/api/bases/switch`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'inactive' }) });
  assert.equal(switched.status, 200);
  const tree = await (await fetch(`${backend}/api/notes`)).text();
  assert.match(tree, /normal\.md/); assert.match(tree, /lost\.md/);
  const nodes = JSON.parse(tree);
  const find = nodes => nodes.flatMap(n => [n, ...find(n.children ?? [])]);
  const note = find(nodes).find(n => n.name === 'lost'); assert.ok(note);
  const loaded = await (await fetch(`${backend}/api/note?id=${note.id}`)).json();
  assert.equal(loaded.content, manifest.markdown);
  const app = await context.newPage(); await app.goto(backend);
  await app.getByRole('button', { name: 'research', exact: true }).click();
  await app.getByRole('button', { name: 'articles', exact: true }).click();
  await app.getByRole('button', { name: 'lost', exact: true }).click();
  const imageResponse = app.waitForResponse(r => r.url().includes('/api/raw?'));
  await app.getByRole('button', { name: 'Предварительный просмотр', exact: true }).click();
  const raw = await imageResponse; assert.equal(raw.status(), 200);
  assert.equal(createHash('sha256').update(await raw.body()).digest('hex'), manifest.assets[0].sha256);
  await app.waitForFunction(() => [...document.querySelectorAll('article img')].some(i => i.complete && i.naturalWidth === 1));
  assert.deepEqual(failures, []);
  process.send({ result: 'PASS', browser: browser.version(), nativeIDB: true, customPort: true, inactiveNested: true, dataImageCapturedAndPrepared: true,
    nativeHeadersVerified: true, authorizedOPTIONS: true, hostilePreflightRejected: true, backendPreviewImageBytes: true,
    nativeHeaders: [...nativeHeaders, ...hostileOptions], nativePreflightObserved: stats.headers.some(h => h.method === 'OPTIONS'),
    confirmedReceiptHash: true, lostResponseGETResolved: true, posts: stats.posts, statusGETs: stats.gets, noDuplicateFiles: true,
    activeBaseUnchangedByImport: true, indexAfterExplicitSwitch: true, nativePermissionDialogTested: false });
} finally { await browser?.close(); }
