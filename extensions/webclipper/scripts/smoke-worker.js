import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';

// The supervisor owns Chromium, the profile and fixture. Even a stuck evaluate
// or close here cannot block the supervisor's deadline and independent cleanup.
const [endpoint, fixtureURL] = process.argv.slice(2);
const failures = [];
let browser;
let result;
try {
  browser = await chromium.connectOverCDP(endpoint);
  const context = browser.contexts()[0];
  const inspect = async (page) => {
    page.on('pageerror', (error) => failures.push(error.message));
    page.on('console', (message) => {
      if (message.type() === 'error') failures.push(message.text());
    });
    page.on('requestfailed', (request) => failures.push(`${request.url()}: ${request.failure()?.errorText}`));
    const cdp = await context.newCDPSession(page);
    cdp.on('Log.entryAdded', ({ entry }) => {
      if (entry.level === 'error') failures.push(entry.text);
    });
    cdp.on('Runtime.exceptionThrown', ({ exceptionDetails }) => failures.push(JSON.stringify(exceptionDetails)));
    await cdp.send('Log.enable');
    await cdp.send('Runtime.enable');
  };
  const worker = context.serviceWorkers()[0] ?? await context.waitForEvent('serviceworker', { timeout: 15000 });
  if (process.env.CLIPPER_SMOKE_FAULT === 'hang-evaluate') {
    console.log('SMOKE_FAULT_REACHED hang-evaluate');
    await worker.evaluate(() => new Promise(() => {}));
  }
  const id = new URL(worker.url()).host;
  assert.match(id, /^[a-p]{32}$/);
  assert.equal(await worker.evaluate(() => chrome.runtime.onInstalled.hasListeners()), true);
  const workerStatus = await worker.evaluate(async () => ({
    manifest: chrome.runtime.getManifest(),
    script: (await self.registration.active)?.scriptURL,
  }));
  assert.equal(workerStatus.manifest.manifest_version, 3);
  assert.equal(workerStatus.script, `chrome-extension://${id}/worker.js`);

  const popup = await context.newPage();
  await inspect(popup);
  await popup.goto(`chrome-extension://${id}/popup.html`);
  await popup.getByRole('heading', { name: 'IGoNotes Web Clipper', exact: true }).waitFor();
  assert.match(await popup.locator('main').innerText(), /capture is not available yet/);
  // Exercise the actual popup link and inspect the new editor before it loads.
  const editorPromise = context.waitForEvent('page');
  const navigation = popup.getByRole('link', { name: 'Open draft editor' }).click();
  const editor = await editorPromise;
  await inspect(editor);
  await navigation;
  await editor.getByRole('heading', { name: 'Local draft editor', exact: true }).waitFor();
  assert.equal(editor.url(), `chrome-extension://${id}/editor.html`);
  await editor.getByText('No local drafts yet. Page capture is not available yet.', { exact: true }).waitFor();
  const storage = await editor.evaluate(async () => {
    const db = await new Promise((resolve, reject) => {
      const request = indexedDB.open('igonotes-webclipper', 1);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    const result = { version: db.version, stores: [...db.objectStoreNames] };
    db.close();
    return result;
  });
  assert.deepEqual(storage, { version: 1, stores: ['assets', 'attempts', 'drafts'] });
  // Also load the editor afresh with CDP enabled before navigation, catching startup errors.
  await editor.reload();
  await editor.getByRole('heading', { name: 'Local draft editor', exact: true }).waitFor();
  await editor.getByText('No local drafts yet. Page capture is not available yet.', { exact: true }).waitFor();

  const page = await context.newPage();
  await inspect(page);
  await page.goto(fixtureURL);
  const before = await page.content();
  const tabId = await worker.evaluate(async (url) => {
    const tabs = await chrome.tabs.query({});
    return tabs.find((tab) => tab.url === url)?.id;
  }, page.url());
  assert.ok(Number.isInteger(tabId), 'local fixture tab found');
  const injected = await worker.evaluate(async (tabId) => {
    const results = await chrome.scripting.executeScript({
      target: { tabId, frameIds: [0] }, world: 'ISOLATED', files: ['content-script.js'],
    });
    const [probe] = await chrome.scripting.executeScript({
      target: { tabId, frameIds: [0] }, world: 'ISOLATED',
      func: () => globalThis.IGoNotesContentScript,
    });
    return { frames: results.map((result) => result.frameId), scaffold: probe.result };
  }, tabId);
  assert.deepEqual(injected, { frames: [0], scaffold: { status: 'scaffold', captureAvailable: false } });
  assert.equal(await page.content(), before);
  assert.equal(await page.evaluate(() => typeof globalThis.IGoNotesContentScript), 'undefined');

  // Chromium's extension error registry retains startup/worker/CSP errors that
  // might precede our page CDP attachment. Do not rely solely on console output.
  const extensions = await context.newPage();
  await extensions.goto('chrome://extensions/');
  const info = await extensions.evaluate(async (id) => {
    const extensions = await chrome.developerPrivate.getExtensionsInfo({ includeDisabled: true, includeTerminated: true });
    return extensions.find((extension) => extension.id === id);
  }, id);
  assert.ok(info, 'unpacked extension registered');
  assert.equal(info.state, 'ENABLED');
  assert.deepEqual(info.manifestErrors, []);
  assert.deepEqual(info.runtimeErrors, []);
  assert.deepEqual(failures, []);
  result = {
    result: 'PASS', browser: browser.version(), extensionId: id,
    serviceWorker: worker.url(), popup: popup.url(), editor: editor.url(),
    injection: injected, domUnchanged: true, pageGlobalAbsent: true, storage,
    manifestErrors: info.manifestErrors, runtimeErrors: info.runtimeErrors, cdpErrors: failures,
    sandbox: process.env.CLIPPER_SMOKE_NO_SANDBOX !== '1',
  };
} finally {
  if (process.env.CLIPPER_SMOKE_FAULT === 'close-fail') {
    console.log('SMOKE_FAULT_REACHED close-fail');
    throw new Error('Injected browser.close failure');
  }
  if (process.env.CLIPPER_SMOKE_FAULT === 'close-hang') {
    console.log('SMOKE_FAULT_REACHED close-hang');
    await new Promise(() => {});
  }
  await browser?.close();
}
process.send(result);
