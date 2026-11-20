import { extensionWorker } from '../tests/browser/extension-worker.js';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { createServer } from 'node:http';
import { join } from 'node:path';
import { chromium } from 'playwright-core';
import { fenceNetwork } from '../tests/browser/network.js';
import { openOwnership } from '../tests/browser/ownership.js';

// This process survives actual Chromium exits; the outer supervisor owns every
// browser/backend process group and a single overall deadline (use 120000ms).
let [endpoint, fixtureURL] = process.argv.slice(2);
let browser, context, extensionId, popup, source;
const failures = [], browserVersions = [];
let wrongRequests = 0;
const wrong = createServer((req, res) => { wrongRequests++; res.end('wrong server'); });
await new Promise(resolve => wrong.listen(0, '127.0.0.1', resolve));
if (process.env.CLIPPER_RESOURCE_DIRECTORY) openOwnership().recordListener(wrong.address().port);
const wrongEndpoint = `http://127.0.0.1:${wrong.address().port}`;
const stats = async () => (await fetch(`${fixtureURL}stats`)).json();
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
async function command(command) {
  const reply = new Promise(resolve => { const receive = message => { if (message.command === command) { process.off('message', receive); resolve(message); } }; process.on('message', receive); });
  process.send({ command }); return reply;
}
async function connect() {
  browser = await chromium.connectOverCDP(endpoint); browserVersions.push(browser.version());
  context = browser.contexts()[0]; context.setDefaultTimeout(7000);
  await fenceNetwork(context, failures);
  context.on('page', page => page.on('pageerror', e => failures.push(e.message)));
  const worker = await extensionWorker(context);
  extensionId = new URL(worker.url()).host;
}
async function reopen(draftId) {
  const editor = await context.newPage(); await editor.goto(`chrome-extension://${extensionId}/editor.html?draft=${draftId}`);
  await editor.getByLabel('Markdown', { exact: true }).waitFor(); return editor;
}
async function bundle(page, id) {
  return page.evaluate(async id => {
    const db = await new Promise((resolve, reject) => { const r = indexedDB.open('igonotes-webclipper', 1); r.onsuccess = () => resolve(r.result); r.onerror = () => reject(r.error); });
    const b = await new Promise((resolve, reject) => {
      const tx = db.transaction(['drafts', 'assets', 'attempts'], 'readonly'), b = {};
      for (const name of ['drafts', 'assets', 'attempts']) { const r = name === 'drafts' ? tx.objectStore(name).get(id) : tx.objectStore(name).index('draftId').getAll(id); r.onsuccess = () => { b[name] = r.result; }; }
      tx.oncomplete = () => resolve(b); tx.onabort = () => reject(tx.error);
    }); db.close();
    b.assets = await Promise.all(b.assets.map(async a => ({ ...a, blob: a.blob ? [...new Uint8Array(await a.blob.arrayBuffer())] : null })));
    return b;
  }, id);
}
async function capture() {
  popup ??= await context.newPage(); await popup.goto(`chrome-extension://${extensionId}/popup.html`);
  source ??= await context.newPage(); await source.goto(fixtureURL); await source.bringToFront();
  const opened = context.waitForEvent('page');
  await popup.evaluate(() => [...document.querySelectorAll('button')].find(b => b.textContent === 'Article').click());
  const editor = await opened; await editor.getByLabel('Markdown', { exact: true }).waitFor(); return editor;
}
async function saveConnection(editor, address) {
  await editor.getByRole('button', { name: 'Connection settings', exact: true }).click();
  await editor.getByLabel('Endpoint', { exact: true }).fill(address);
  await editor.getByLabel('Connection token', { exact: true }).fill(process.env.CLIPPER_SMOKE_SECRET);
  await editor.getByRole('button', { name: 'Save connection', exact: true }).click();
  await editor.getByText('Connection saved locally. Not tested.', { exact: true }).waitFor();
  await editor.getByRole('button', { name: 'Close connection settings', exact: true }).click();
}
async function confirm(editor) {
  await editor.getByRole('button', { name: 'Load destinations', exact: true }).click();
  const base = editor.getByLabel('Destination base', { exact: true }); await base.waitFor(); assert.equal(await base.inputValue(), '');
  await base.selectOption('inactive');
  const folder = editor.getByLabel('Destination folder', { exact: true }); await folder.waitFor(); assert.equal(await folder.inputValue(), '/');
  await folder.selectOption('research/articles'); await editor.getByRole('button', { name: 'Confirm destination', exact: true }).click();
  await editor.getByText('Confirmed destination: inactive / research/articles', { exact: true }).waitFor();
}
async function pregrantImages() {
  const settings = await context.newPage(); await settings.goto('chrome://extensions');
  await settings.evaluate(async id => {
    await chrome.developerPrivate.addHostPermission(id, 'http://images.example.test/*');
    await chrome.developerPrivate.addHostPermission(id, 'http://cdn.example.test/*');
  }, extensionId);
  await settings.close();
}
async function waitStats(predicate) {
  for (;;) { const s = await stats(); if (predicate(s)) return s; await new Promise(resolve => setTimeout(resolve, 30)); }
}
async function restartBrowser() {
  ({ endpoint } = await command('restart-browser')); popup = source = undefined; await connect();
}
try {
  await connect();
  let editor = await capture(); const draftId = new URL(editor.url()).searchParams.get('draft');
  await saveConnection(editor, fixtureURL.slice(0, -1));
  // Test-only permission pregrant, not evidence of native permission UI consent.
  await pregrantImages();
  await editor.bringToFront();
  await editor.getByRole('button', { name: 'Prepare images', exact: true }).click();
  await editor.getByText(/1 \/ 2 stored locally/).waitFor(); await waitStats(s => s.imageRequests === 1);
  const partial = await bundle(editor, draftId);
  assert.equal(partial.assets.filter(a => a.blob).length, 1);
  assert.equal(partial.assets.filter(a => a.descriptor.status === 'pending').length, 1);
  await restartBrowser(); editor = await reopen(draftId);
  await editor.getByText(/1 \/ 2 stored locally/).waitFor();
  assert.deepEqual(await bundle(editor, draftId), partial);
  assert.equal((await stats()).imageRequests, 1, 'reopen must not refetch the pending image');
  assert.equal((await stats()).posts, 0);
  await pregrantImages(); await editor.bringToFront();
  await fetch(`${fixtureURL}release-images`);
  await editor.getByRole('button', { name: 'Prepare images', exact: true }).click();
  await editor.getByText(/2 \/ 2 stored locally/).waitFor();
  assert.equal((await stats()).imageRequests, 2, 'only the missing image is fetched');
  const ready = await bundle(editor, draftId);
  assert.deepEqual(ready.assets.find(a => a.id === partial.assets.find(a => a.blob).id).blob, partial.assets.find(a => a.blob).blob);
  await source?.close(); source = undefined;
  assert.equal(context.pages().some(p => p.url() === fixtureURL), false, 'capture source is closed after actual browser restart');
  const permissions = await context.newPage(); await permissions.goto('chrome://extensions');
  await permissions.evaluate(async id => {
    await chrome.developerPrivate.removeHostPermission(id, 'http://images.example.test/*');
  }, extensionId);
  await permissions.close();
  assert.equal(await editor.evaluate(() => chrome.permissions.contains({ origins: ['http://images.example.test/*'] })), false);
  const cachedRequests = (await stats()).imageRequests;
  await context.setOffline(true);
  await editor.close(); editor = await reopen(draftId);
  await editor.getByText(/2 \/ 2 stored locally/).waitFor();
  assert.deepEqual(await bundle(editor, draftId), ready, 'captured Markdown and cached bytes survive offline reopen with source closed and permission revoked');
  await context.setOffline(false);
  assert.equal((await stats()).imageRequests, cachedRequests);
  await pregrantImages();
  // Also retain a warning in the historical receipt after process restart.
  const external = `http://cdn.example.test:${new URL(fixtureURL).port}/missing.gif`;
  await editor.getByLabel('Markdown', { exact: true }).fill(`${ready.drafts.markdown}\n![external](${external})`);
  await editor.getByRole('button', { name: 'Prepare images', exact: true }).click();
  await editor.getByText('Image preparation finished. Review each image below.', { exact: true }).waitFor();
  await editor.getByLabel(`Keep external reference: ${external}`, { exact: true }).check();
  await editor.getByRole('button', { name: 'Confirm external references', exact: true }).click();
  await confirm(editor);
  await fetch(`${fixtureURL}arm-hold`);
  await editor.getByRole('button', { name: 'Send', exact: true }).click();
  const completed = await waitStats(s => s.held === 1);
  const posting = await bundle(editor, draftId), attempt = posting.attempts[0];
  assert.equal(posting.drafts.status, 'sending'); assert.equal(attempt.phase, 'posting');
  assert.equal(attempt.state, 'sending');
  await restartBrowser(); await command('restart-go'); editor = await reopen(draftId);
  await editor.getByText(/Interrupted or still active in another window/).waitFor();
  assert.deepEqual(await bundle(editor, draftId), posting);
  assert.equal((await stats()).posts, 1); assert.equal((await stats()).gets, 0);
  await saveConnection(editor, wrongEndpoint);
  await editor.evaluate(() => { window.dispatchEvent(new Event('offline')); window.dispatchEvent(new Event('online')); });
  await editor.getByRole('button', { name: 'Check attempt', exact: true }).click();
  await editor.getByText(/Restore its original connection address/).waitFor();
  await editor.getByRole('button', { name: 'Check / retry exact attempt', exact: true }).click();
  await editor.getByText(/Restore its original connection address/).waitFor();
  assert.equal(wrongRequests, 0, 'old identity/token must never reach another endpoint');
  await saveConnection(editor, fixtureURL.slice(0, -1));
  const publishedPath = join(process.env.CLIPPER_SMOKE_HOME, 'bases/inactive/research/articles', attempt.manifest.filename);
  const beforeReplay = statSync(publishedPath);
  await fetch(`${fixtureURL}arm-unknown`);
  await editor.getByRole('button', { name: 'Check / retry exact attempt', exact: true }).click();
  await editor.getByRole('heading', { name: 'Import confirmed', exact: true }).waitFor();
  assert.equal(statSync(publishedPath).ino, beforeReplay.ino, 'historical replay must preserve publication inode');
  const replayed = await stats();
  assert.equal(replayed.posts, 2); assert.equal(replayed.gets, 1);
  assert.deepEqual(replayed.receipts[1], completed.receipts[0], 'same-ID POST after unknown GET returns exact historical receipt');
  assert.deepEqual(replayed.outcomes.map(o => o.status), [201, 200]);
  assert.deepEqual(await bundle(editor, draftId), { drafts: undefined, assets: [], attempts: [] });
  const receipt = completed.receipts[0]; assert.equal(receipt.import_id, attempt.id);
  assert.deepEqual(receipt.warnings, attempt.manifest.external_images.map(e => e.reason)); assert.equal(receipt.warnings.length, 1);
  assert.equal(receipt.revision, `sha256:${hash(attempt.manifest.markdown)}`);
  const home = process.env.CLIPPER_SMOKE_HOME;
  assert.equal(readFileSync(join(home, 'bases/inactive', receipt.path), 'utf8'), attempt.manifest.markdown);
  assert.equal(readdirSync(join(home, 'bases/inactive/research/articles')).length, 1);
  assert.equal(readdirSync(join(home, 'bases/inactive/assets/images')).length, 1);
  for (const a of attempt.manifest.assets) assert.equal(hash(readFileSync(join(home, 'bases/inactive/assets/images', `clip-${attempt.id}-${a.sha256}.gif`))), a.sha256);
  // Kill the actual extension service-worker target. Popup capture wakes a new
  // worker and must leave an existing draft intact; no delivery is auto-started.
  let fresh = await capture(); const freshId = new URL(fresh.url()).searchParams.get('draft');
  const freshBefore = await bundle(fresh, freshId);
  const cdp = await browser.newBrowserCDPSession(); const { targetInfos } = await cdp.send('Target.getTargets');
  const target = targetInfos.find(t => t.type === 'service_worker' && t.url.startsWith(`chrome-extension://${extensionId}/`)); assert.ok(target);
  await cdp.send('Target.closeTarget', { targetId: target.targetId });
  const woken = await capture(); assert.deepEqual(await bundle(woken, freshId), freshBefore);
  // A chosen destination, no attempt: actual Go restart invalidates the old
  // process-local handle. Send preflight must demand explicit reconfirmation.
  await fresh.getByLabel('Markdown', { exact: true }).fill('Text only restart preflight');
  await fresh.getByLabel('Filename', { exact: true }).fill('fresh.md');
  await fresh.getByRole('button', { name: 'Prepare images', exact: true }).click();
  await fresh.getByText('Image preparation finished. Review each image below.', { exact: true }).waitFor();
  await confirm(fresh);
  const old = await bundle(fresh, freshId); assert.equal(old.attempts.length, 0);
  await command('restart-go');
  await fresh.getByRole('button', { name: 'Send', exact: true }).click();
  await fresh.getByText(/explicitly confirm the destination again/).waitFor();
  assert.equal((await stats()).posts, 2);
  assert.equal((await bundle(fresh, freshId)).drafts.destinationRevision, old.drafts.destinationRevision);
  await confirm(fresh);
  assert.notEqual((await bundle(fresh, freshId)).drafts.destinationRevision, old.drafts.destinationRevision);
  await fresh.getByRole('button', { name: 'Send', exact: true }).click();
  await fresh.getByRole('heading', { name: 'Import confirmed', exact: true }).waitFor();
  const final = await stats(); assert.equal(final.posts, 3); assert.equal(final.gets, 1); assert.deepEqual(failures, []);
  process.send({ result: 'PASS', browserVersions, browserProcessRestarts: 2, goProcessRestarts: 2, sameBrowserProfile: true,
    offlineCapturedDraft: true, partialPreparationDurable: true, pendingOnlyManualFetch: true, noAutoPOSTorGET: true, historicalExactPOSTReplay: true, heldCompletedReceiptRecoveredAfterBothRestarts: true,
    originalIDManifestBlobsPreserved: true, warningsPathsHashesPreserved: true, noDuplicateFiles: true, wrongEndpointRequests: wrongRequests,
    workerTargetTerminatedAndCaptureWoke: true, oldDestinationRequiresExplicitReconfirm: true, posts: final.posts, gets: final.gets, nativePermissionDialogTested: false });
} catch (error) {
  console.error('RESTART_FAILURE', JSON.stringify(await stats()));
  for (const page of context?.pages() ?? []) if (page.url().includes('/editor.html')) console.error(await page.locator('main').innerText().catch(() => page.url()));
  throw error;
} finally { wrong.closeAllConnections(); wrong.close(); await browser?.close(); }
