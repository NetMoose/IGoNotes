import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';
import { marked } from '../tests/helpers/app-markdown.js';
import { formatNote } from '../src/markdown/note.js';

// The supervisor owns Chromium, the profile and fixture. Even a stuck evaluate
// or close here cannot block the supervisor's deadline and independent cleanup.
const [endpoint, fixtureURL] = process.argv.slice(2);
const failures = [];
let browser;
let result;
try {
  browser = await chromium.connectOverCDP(endpoint);
  const context = browser.contexts()[0];
  context.setDefaultTimeout(5000);
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
  const id = new URL(worker.url()).host;
  assert.match(id, /^[a-p]{32}$/);
  assert.equal(await worker.evaluate(() => chrome.runtime.onInstalled.hasListeners()), true);
  assert.equal(await worker.evaluate(() => chrome.runtime.onStartup.hasListeners()), true);
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
  await popup.getByRole('button', { name: 'Article', exact: true }).waitFor();
  await popup.getByRole('button', { name: 'Selection', exact: true }).waitFor();
  assert.match(await popup.locator('main').innerText(), /local Markdown draft/);
  // Exercise the actual popup link and inspect the new editor before it loads.
  const editorPromise = context.waitForEvent('page');
  const navigation = popup.getByRole('link', { name: 'Open draft editor' }).click();
  const editor = await editorPromise;
  await inspect(editor);
  await navigation;
  await editor.getByRole('heading', { name: 'Local draft editor', exact: true }).waitFor();
  assert.equal(editor.url(), `chrome-extension://${id}/editor.html`);
  await editor.getByText('No local drafts yet. Capture an Article or Selection from the popup.', { exact: true }).waitFor();

  // Manual settings DOM: no backend is needed and Test is deliberately not clicked.
  // Watch for unsolicited requests, including requests from other extension contexts.
  const connectionRequests = [];
  context.on('request', request => {
    if (request.url().includes('/api/clipper/')) connectionRequests.push(request.method());
  });
  await editor.getByRole('button', { name: 'Connection settings', exact: true }).click();
  const endpointInput = editor.getByLabel('Endpoint', { exact: true });
  const tokenInput = editor.getByLabel('Connection token', { exact: true });
  await endpointInput.waitFor();
  await editor.getByRole('button', { name: 'Save connection', exact: true }).waitFor({ state: 'visible' });
  await endpointInput.fill('http://localhost:9123');
  assert.equal(await tokenInput.getAttribute('type'), 'password');
  await tokenInput.fill('smoke-only-opaque-credential');
  await editor.getByRole('button', { name: 'Save connection', exact: true }).click();
  await editor.getByText('Connection saved locally. Not tested.', { exact: true }).waitFor();
  const publicConnection = await worker.evaluate(async () => {
    const { clipperConnection } = await chrome.storage.local.get('clipperConnection');
    return { endpoint: clipperConnection?.endpoint, hasToken: Boolean(clipperConnection?.token) };
  });
  assert.deepEqual(publicConnection, { endpoint: 'http://localhost:9123', hasToken: true });
  assert.equal((await editor.locator('body').innerText()).includes('smoke-only-opaque-credential'), false);
  await editor.getByRole('button', { name: 'Close connection settings', exact: true }).click();
  await editor.getByRole('button', { name: 'Connection settings', exact: true }).click();
  await editor.waitForFunction(() => document.querySelector('#connection-endpoint')?.value === 'http://localhost:9123');
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
  await editor.getByText('No local drafts yet. Capture an Article or Selection from the popup.', { exact: true }).waitFor();

  const page = await context.newPage();
  await inspect(page);
  await page.goto(fixtureURL);
  await page.waitForLoadState('networkidle');
  await page.evaluate(() => {
    const range = document.createRange();
    range.selectNodeContents(document.querySelector('article'));
    const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range);
  });
  const before = await page.content();
  const selectionBefore = await page.evaluate(() => ({ text: getSelection().toString(), start: getSelection().getRangeAt(0).startOffset, end: getSelection().getRangeAt(0).endOffset, x: scrollX, y: scrollY }));
  const tabId = await worker.evaluate(async (url) => {
    const tabs = await chrome.tabs.query({});
    return tabs.find((tab) => tab.url === url)?.id;
  }, page.url());
  assert.ok(Number.isInteger(tabId), 'local fixture tab found');
  // A real action popup does not become an active tab. This test popup is an
  // extension tab, so click its DOM without focusing it, leaving the fixture
  // active. Production worker capture performs the real query/injection/call;
  // localhost host permission supplies the grant in this automation only.
  const captureRequests = [];
  const recordCaptureRequest = request => {
    if (!request.url().startsWith(`chrome-extension://${id}/`)) captureRequests.push(request.url());
  };
  // Guard the actual worker API during capture, separately from settings tests.
  await worker.evaluate(() => {
    globalThis.smokeStorageGet = chrome.storage.local.get;
    globalThis.smokeCaptureCredentialReads = 0;
    chrome.storage.local.get = () => { globalThis.smokeCaptureCredentialReads++; throw new Error('Capture must not read connection settings'); };
    chrome.runtime.onMessage.addListener((_message, sender) => { globalThis.smokeCaptureSender = sender; });
  });
  context.on('request', recordCaptureRequest);
  const capturedDrafts = [];
  for (const mode of ['Article', 'Selection']) {
    const capturePopup = await context.newPage();
    await inspect(capturePopup);
    await capturePopup.goto(`chrome-extension://${id}/popup.html`);
    await capturePopup.getByRole('button', { name: mode, exact: true }).waitFor();
    await page.bringToFront();
    const nextEditor = context.waitForEvent('page').catch(error => error);
    await capturePopup.evaluate(name => [...document.querySelectorAll('button')].find(button => button.textContent === name).click(), mode);
    await capturePopup.waitForFunction(() => document.querySelector('main')?.getAttribute('aria-busy') === 'false');
    if (!(await capturePopup.getByRole('status').innerText()).includes('Saved locally')) {
      throw new Error(`Capture failed: ${await capturePopup.locator('main').innerText()}; sender=${JSON.stringify(await worker.evaluate(() => globalThis.smokeCaptureSender))}`);
    }
    const capturedEditor = await nextEditor;
    if (capturedEditor instanceof Error) throw capturedEditor;
    await inspect(capturedEditor);
    await capturePopup.getByRole('status').getByText(/Saved locally/).waitFor();
    assert.match(await capturePopup.getByRole('status').innerText(), /editor opened/);
    await capturePopup.close(); // confirmed capture survives popup destruction
    await capturedEditor.getByLabel('Markdown', { exact: true }).waitFor();
    const body = await capturedEditor.getByLabel('Markdown', { exact: true }).inputValue();
    assert.match(body, /## Наблюдения/);
    assert.match(body, /\| Name \| Value \|/);
    assert.doesNotMatch(body, /PRIVATE_|<iframe|<style/);
    assert.equal(await capturedEditor.getByLabel('Title', { exact: true }).inputValue(), 'Лес и наука 🌲');
    assert.equal(await capturedEditor.getByLabel('Filename', { exact: true }).inputValue(), 'Лес и наука 🌲.md');
    await capturedEditor.getByRole('link', { name: fixtureURL, exact: true }).waitFor();
    const editorURL = new URL(capturedEditor.url());
    assert.equal(editorURL.pathname, '/editor.html');
    assert.equal([...editorURL.searchParams].length, 1);
    const draftId = editorURL.searchParams.get('draft');
    assert.match(draftId, /^[0-9a-f-]{36}$/);
    capturedDrafts.push(draftId);
    await capturedEditor.reload();
    await capturedEditor.getByLabel('Markdown', { exact: true }).waitFor();
    assert.equal(await capturedEditor.getByLabel('Markdown', { exact: true }).inputValue(), body);
    await capturedEditor.getByRole('link', { name: fixtureURL, exact: true }).waitFor();
    await capturedEditor.getByRole('status').getByText('Saved locally', { exact: true }).waitFor();
    // Real Blob download uses unsaved metadata/body, without rewriting the
    // user's filename or persisting generated frontmatter into the body.
    await capturedEditor.getByLabel('Filename', { exact: true }).fill('manual.md');
    await capturedEditor.getByLabel('Title', { exact: true }).fill('<b>Unsaved "title" 🌲</b>');
    await capturedEditor.getByLabel('Markdown', { exact: true }).fill(body + '\nUnsaved body');
    const downloadPromise = capturedEditor.waitForEvent('download');
    await capturedEditor.getByRole('button', { name: 'Download Markdown', exact: true }).click();
    const download = await downloadPromise;
    assert.equal(download.suggestedFilename(), 'manual.md');
    const stream = await download.createReadStream();
    const chunks = [];
    for await (const chunk of stream) chunks.push(chunk);
    const capturedAt = await capturedEditor.locator('.metadata dd').last().innerText();
    assert.equal(Buffer.concat(chunks).toString('utf8'), formatNote({ title: '<b>Unsaved "title" 🌲</b>', sourceUrl: fixtureURL, capturedAt, markdown: body + '\nUnsaved body' }));
    assert.equal(await capturedEditor.getByLabel('Filename', { exact: true }).inputValue(), 'manual.md');
    await capturedEditor.reload();
    await capturedEditor.getByLabel('Markdown', { exact: true }).waitFor();
    assert.equal(await capturedEditor.getByLabel('Markdown', { exact: true }).inputValue(), body);
    await capturedEditor.close();
    assert.equal(await page.content(), before);
    assert.deepEqual(await page.evaluate(() => ({ text: getSelection().toString(), start: getSelection().getRangeAt(0).startOffset, end: getSelection().getRangeAt(0).endOffset, x: scrollX, y: scrollY })), selectionBefore);
  }
  context.off('request', recordCaptureRequest);
  assert.deepEqual(captureRequests, [], 'capture/persistence/editor must not request images or backend');
  assert.equal(new Set(capturedDrafts).size, 2);
  const captureCredentialReads = await worker.evaluate(() => {
    chrome.storage.local.get = globalThis.smokeStorageGet;
    return globalThis.smokeCaptureCredentialReads;
  });
  assert.equal(captureCredentialReads, 0);
  const injected = await worker.evaluate(async tabId => {
    const results = await chrome.scripting.executeScript({ target: { tabId, frameIds: [0] }, world: 'ISOLATED', func: () => typeof globalThis.IGoNotesContentScript.capture });
    return { frames: results.map(result => result.frameId), extractor: results[0].result };
  }, tabId);
  assert.deepEqual(injected, { frames: [0], extractor: 'function' });
  const conversionRequests = [];
  const recordConversionRequest = request => conversionRequests.push(request.url());
  context.on('request', recordConversionRequest);
  const conversions = await worker.evaluate(async ({ tabId, url }) => {
    const values = [];
    for (const mode of ['selection', 'article']) {
      const [result] = await chrome.scripting.executeScript({
        target: { tabId, frameIds: [0] }, world: 'ISOLATED',
        args: [mode, url], func: (mode, url) => globalThis.IGoNotesContentScript.captureMarkdown(mode, url),
      });
      values.push(result.result);
    }
    return values;
  }, { tabId, url: page.url() });
  const renderPage = await context.newPage();
  await inspect(renderPage);
  for (const conversion of conversions) {
    assert.equal(conversion.ok, true);
    assert.match(conversion.value.markdown, /## Наблюдения/);
    assert.match(conversion.value.markdown, /\| Name \| Value \|/);
    assert.match(conversion.value.markdown, /`````js\nconst html = '<img onerror="bad\(\)">';/);
    assert.ok(marked.parse(conversion.value.markdown).includes('Spacing <code>a  b</code>; edges <code> a </code>; blank <code>   </code>.'));
    assert.ok(conversion.value.markdown.includes('```\nfirst  line\n second\n```'));
    assert.ok(conversion.value.images.includes(`${fixtureURL}conversion-never-request.png`));
    assert.ok(conversion.value.images.includes(`${fixtureURL}baseline.png`));
    assert.equal(Object.hasOwn(conversion.value, 'html'), false);
    assert.doesNotMatch(conversion.value.markdown, /PRIVATE_|<iframe|<style/);
    // Render the code-only hostile fixture with the app's actual Marked +
    // highlight configuration in Chromium, not just a string/source assertion.
    // Exclude the earlier legitimate article images from this render probe.
    const start = conversion.value.markdown.indexOf('## Adjacent code safety');
    assert.ok(start >= 0);
    await renderPage.setContent(marked.parse(conversion.value.markdown.slice(start)));
    const rendered = await renderPage.evaluate(() => ({
      paragraphs: [...document.querySelectorAll('p')].filter(p => p.textContent.startsWith('Adjacent')).map(p => ({
        text: p.textContent, code: [...p.querySelectorAll('code')].map(code => code.textContent).join(''),
      })),
      cell: document.querySelector('td')?.textContent,
      cellCode: document.querySelector('td code')?.textContent,
      formatting: [...document.querySelectorAll('p, td')].filter(element => element.querySelector('em, strong')).map(element => ({
        text: element.textContent,
        em: [...element.querySelectorAll('em')].map(node => node.textContent),
        strong: [...element.querySelectorAll('strong')].map(node => node.textContent),
      })),
      injected: Boolean(document.querySelector('img, a, script, svg, iframe, math')),
      eventAttributes: [...document.querySelectorAll('*')].flatMap(element => [...element.attributes].map(attr => attr.name)).filter(name => /^on/i.test(name)),
    }));
    assert.equal(rendered.injected, false);
    assert.deepEqual(rendered.eventAttributes, []);
    assert.deepEqual(rendered.paragraphs, [
      { text: 'Adjacent HTML: a`<img src=x onerror=bad()>', code: 'a`<img src=x onerror=bad()>' },
      { text: 'Adjacent link: a`[x](javascript:bad())', code: 'a`[x](javascript:bad())' },
      { text: 'Adjacent wrappers: a`<img src=x onerror=bad()>', code: 'a`<img src=x onerror=bad()>' },
      { text: 'Adjacent spaces:  `` a ', code: ' `` a ' },
      { text: 'Adjacent blank formatting: a `<img src=x onerror=bad()>', code: 'a `<img src=x onerror=bad()>' },
      { text: 'Adjacent emphasis: a`x`', code: 'a`x`' },
      { text: 'Adjacent strong: a`x`', code: 'a`x`' },
      { text: 'Adjacent nested: 左```世界`右```', code: '左```世界`右```' },
      { text: 'Adjacent parent: alpha ` 尾', code: ' ` ' },
    ]);
    assert.deepEqual(rendered.formatting, [
      { text: 'Adjacent emphasis: a`x`', em: ['`'], strong: [] },
      { text: 'Adjacent strong: a`x`', em: [], strong: ['`'] },
      { text: 'Adjacent nested: 左```世界`右```', em: ['`世界`'], strong: ['`世界`'] },
      { text: 'Adjacent parent: alpha ` 尾', em: [' ` '], strong: [' ` '] },
      { text: 'Adjacent table emphasis: a`x`', em: ['`'], strong: ['`'] },
    ]);
    assert.equal(rendered.cell, 'Adjacent table: a`<img src=x onerror=bad()>[x](javascript:bad())');
    assert.equal(rendered.cellCode, 'a`<img src=x onerror=bad()>[x](javascript:bad())');
  }
  // Resource activation is asynchronous; include the actual render probes too.
  await renderPage.waitForTimeout(300);
  context.off('request', recordConversionRequest);
  await renderPage.close();
  assert.deepEqual(conversionRequests, [], 'conversion must initiate zero new requests after the live-page baseline');
  assert.equal(await page.content(), before);
  assert.deepEqual(await page.evaluate(() => ({ text: getSelection().toString(), start: getSelection().getRangeAt(0).startOffset, end: getSelection().getRangeAt(0).endOffset, x: scrollX, y: scrollY })), selectionBefore);
  assert.equal(await page.evaluate(() => typeof globalThis.IGoNotesContentScript), 'undefined');

  const trustedStorageOnly = await worker.evaluate(async tabId => {
    const [probe] = await chrome.scripting.executeScript({
      target: { tabId, frameIds: [0] }, world: 'ISOLATED',
      func: async () => {
        // Test only native Chrome access. No settings import, messages, logging,
        // DOM mutation or credential return to the page/test process.
        try { await chrome.storage.local.get('clipperConnection'); return false; }
        catch { return true; }
      },
    });
    return probe.result;
  }, tabId);
  assert.equal(trustedStorageOnly, true);
  await editor.getByRole('button', { name: 'Connection settings', exact: true }).click();
  await editor.getByRole('button', { name: 'Clear connection', exact: true }).click();
  await editor.getByText('Local connection removed. Server token is not revoked.', { exact: true }).waitFor();
  assert.equal(await worker.evaluate(async () => Object.hasOwn(await chrome.storage.local.get('clipperConnection'), 'clipperConnection')), false);
  assert.deepEqual(connectionRequests, []);
  await editor.getByRole('button', { name: 'Close connection settings', exact: true }).click();

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
    durableCapture: ['Article', 'Selection'], popupClosedAndEditorReloaded: true,
    proposedCaptureFilename: true, unsavedFrontmatterDownload: true, manualFilenameIndependent: true,
    captureRequests: captureRequests.length, captureCredentialReads,
    markdownConversion: true, conversionRequests: conversionRequests.length,
    adjacentCodeRenderedSafely: true,
    trustedStorageOnly, manualConnectionSettings: true, unsolicitedConnectionRequests: connectionRequests.length,
    manifestErrors: info.manifestErrors, runtimeErrors: info.runtimeErrors, cdpErrors: failures,
    sandbox: process.env.CLIPPER_SMOKE_NO_SANDBOX !== '1',
  };
} finally {
  await browser?.close();
}
process.send(result);
