import { afterEach, expect, it, vi } from 'vitest';
import { IDBFactory, IDBObjectStore } from 'fake-indexeddb';
import { createDraftStore } from '../src/storage/drafts.js';
import { registerCaptureCoordinator } from '../src/capture/coordinator.js';

const value = { mode: 'article', title: 'Title', markdown: '# Converted\n\nBody', images: ['https://example.test/a.png', 'https://example.test/a.png'], sourceUrl: 'https://example.test/article', capturedAt: '2026-10-23T12:00:00.000Z' };
const sender = { id: 'own', url: 'chrome-extension://own/popup.html', origin: 'chrome-extension://own' };
const request = { type: 'capture', mode: 'article' };
afterEach(() => vi.unstubAllGlobals());
function setup(options = {}) {
  const order = [];
  const indexedDB = new IDBFactory();
  const store = createDraftStore({ indexedDB, ...options });
  const save = store.saveDraft.bind(store);
  store.saveDraft = vi.fn(async (...args) => { const saved = await save(...args); order.push('complete'); return saved; });
  const chrome = { runtime: { id: 'own', getURL: path => `chrome-extension://own/${path}`, onMessage: { addListener: vi.fn() } }, tabs: { create: vi.fn(async () => { order.push('tab'); }) } };
  const capture = vi.fn(async () => { order.push('capture'); return value; });
  const protect = vi.fn(async () => { order.push('protect'); });
  const createStore = vi.fn(() => createDraftStore({ indexedDB, ...options })).mockReturnValueOnce(store);
  const network = vi.fn(() => { throw new Error('No network'); }); vi.stubGlobal('fetch', network);
  registerCaptureCoordinator({ chrome, capture, protect, createStore });
  const listener = chrome.runtime.onMessage.addListener.mock.calls[0][0];
  function send(message = request, from = sender, callback) {
    let returned;
    const response = new Promise(resolve => { returned = listener(message, from, result => { order.push('ack'); callback?.(result); resolve(result); }); });
    return { response, returned };
  }
  return { order, indexedDB, store, chrome, capture, protect, createStore, network, send };
}
it('registers synchronously, protects before capture, commits before opening editor and ack; reopen retains converted text/assets', async () => {
  const s = setup();
  const call = s.send(); expect(call.returned).toBe(true);
  const response = await call.response;
  expect(response).toEqual({ ok: true, draftId: expect.any(String), editorOpened: true });
  expect(s.order).toEqual(['protect', 'capture', 'complete', 'tab', 'ack']);
  expect(s.capture).toHaveBeenCalledExactlyOnceWith('article', { chrome: s.chrome });
  expect(s.chrome.tabs.create).toHaveBeenCalledExactlyOnceWith({ url: `chrome-extension://own/editor.html?draft=${encodeURIComponent(response.draftId)}` });
  const reopened = createDraftStore({ indexedDB: s.indexedDB });
  expect(await reopened.getDraft(response.draftId)).toMatchObject({ title: value.title, filename: 'Title.md', markdown: value.markdown, sourceUrl: value.sourceUrl, capturedAt: value.capturedAt, base: null, parent: '', destinationRevision: null, status: 'draft' });
  const assets = await reopened.getAssets(response.draftId);
  expect(assets).toHaveLength(1);
  expect(assets[0]).toMatchObject({ id: expect.any(String), normalizedUrl: value.images[0], descriptor: { status: 'pending' }, blob: null });
  expect(s.network).not.toHaveBeenCalled(); reopened.close();
});
it.each([
  [{ type: 'fetch', url: 'https://evil.test/' }, sender],
  [{ ...request, token: 'forged' }, sender],
  [{ ...request, expectedUrl: value.sourceUrl }, sender],
  [{ ...request, mode: 'everything' }, sender],
  [request, { ...sender, id: 'other' }],
  [request, { ...sender, url: 'https://example.test/article', origin: 'https://example.test', tab: { id: 1 }, frameId: 0 }],
  [request, { ...sender, tab: { id: 1 }, frameId: 1 }],
  [request, { ...sender, tab: { id: 1, url: value.sourceUrl }, frameId: 0 }],
  [request, { ...sender, tab: { id: 1 }, frameId: 0 }],
  [request, { ...sender, url: sender.url + '?forged=1' }],
  [request, { ...sender, url: 'chrome-extension://own/editor.html' }],
  [request, { ...sender, origin: 'https://evil.test' }],
])('rejects untrusted sender/command before any effects', async (message, from) => {
  const s = setup();
  expect(await s.send(message, from).response).toEqual({ ok: false, code: 'invalid_request' });
  for (const operation of [s.capture, s.protect, s.createStore, s.chrome.tabs.create, s.network]) expect(operation).not.toHaveBeenCalled();
});
it('accepts only the exact own popup URL when Chrome includes extension-tab/frame metadata', async () => {
  const s = setup();
  expect(await s.send(request, { ...sender, frameId: 0, tab: { id: 20, url: sender.url } }).response).toMatchObject({ ok: true, editorOpened: true });
});
it.each([null, { ...value, capturedAt: '2026-02-30T12:00:00Z' }, { ...value, capturedAt: '2026-10-23T12:00:00+00:00' }, { ...value, sourceUrl: 'https://example.test/?q=%0a' }, { ...value, html: '<script>bad</script>' }, { ...value, token: 'secret' }, { ...value, markdown: '' }, { ...value, images: [{ url: 'https://x.test/' }] }, { ...value, images: ['javascript:bad()'] }, { ...value, mode: 'selection' }])('rejects malformed converter payload before persistence', async payload => {
  const s = setup(); s.capture.mockResolvedValue(payload);
  expect(await s.send().response).toEqual({ ok: false, code: 'capture_failed' });
  expect(s.createStore).not.toHaveBeenCalled(); expect(s.chrome.tabs.create).not.toHaveBeenCalled();
});
it('quota failure preserves existing drafts and returns only safe unsaved material without opening a tab', async () => {
  const s = setup({ limits: { maxDrafts: 1 } });
  const first = await s.send().response;
  const failed = await s.send().response;
  expect(failed).toEqual({ ok: false, code: 'draft_limit', unsaved: { title: value.title, markdown: value.markdown, sourceUrl: value.sourceUrl, capturedAt: value.capturedAt } });
  const reopened = createDraftStore({ indexedDB: s.indexedDB });
  expect((await reopened.listDrafts()).map(row => row.id)).toEqual([first.draftId]);
  expect(s.chrome.tabs.create).toHaveBeenCalledOnce(); reopened.close();
});
it.each(['storage_quota', 'storage_cancelled'])('failed transaction %s does not acknowledge saved or open editor', async code => {
  const s = setup(); s.store.saveDraft.mockRejectedValue(Object.assign(new Error('secret native text'), { code }));
  expect(await s.send().response).toMatchObject({ ok: false, code, unsaved: { markdown: value.markdown } });
  expect(s.chrome.tabs.create).not.toHaveBeenCalled();
});
it('tab failure after commit still acknowledges durable success without re-capture', async () => {
  const s = setup(); s.chrome.tabs.create.mockRejectedValue(new Error('native secret'));
  const result = await s.send().response;
  expect(result).toEqual({ ok: true, draftId: expect.any(String), editorOpened: false });
  const reopened = createDraftStore({ indexedDB: s.indexedDB });
  expect((await reopened.getDraft(result.draftId)).markdown).toBe(value.markdown); reopened.close();
});
it('closed response channel never deletes or recreates the committed draft', async () => {
  const s = setup();
  s.send(request, sender, () => { throw new Error('channel closed'); });
  await vi.waitFor(() => expect(s.order).toContain('ack'));
  const reopened = createDraftStore({ indexedDB: s.indexedDB });
  expect(await reopened.listDrafts()).toHaveLength(1);
  expect(s.capture).toHaveBeenCalledOnce(); expect(s.store.saveDraft).toHaveBeenCalledOnce(); reopened.close();
});
it('real quota rollback after asset writes leaves old work and no new metadata, returns downloadable Markdown', async () => {
  const s = setup();
  const old = await s.send().response;
  const put = IDBObjectStore.prototype.put;
  const injected = vi.spyOn(IDBObjectStore.prototype, 'put').mockImplementation(function(...args) {
    if (this.name === 'drafts') throw new DOMException('private native error', 'QuotaExceededError');
    return put.apply(this, args);
  });
  let result;
  try { result = await s.send().response; } finally { injected.mockRestore(); }
  expect(result).toEqual({ ok: false, code: 'storage_quota', unsaved: { title: value.title, markdown: value.markdown, sourceUrl: value.sourceUrl, capturedAt: value.capturedAt } });
  const reopened = createDraftStore({ indexedDB: s.indexedDB });
  expect((await reopened.listDrafts()).map(row => row.id)).toEqual([old.draftId]);
  expect(await reopened.getAssets(old.draftId)).toHaveLength(1);
  const db = await new Promise(resolve => { const req = s.indexedDB.open('igonotes-webclipper'); req.onsuccess = () => resolve(req.result); });
  const assets = await new Promise(resolve => { const req = db.transaction('assets').objectStore('assets').getAll(); req.onsuccess = () => resolve(req.result); });
  expect(assets).toHaveLength(1); db.close(); reopened.close();
  expect(s.chrome.tabs.create).toHaveBeenCalledOnce();
});
it('protection failure stops before capture and never loads credentials', async () => {
  const s = setup(); s.protect.mockRejectedValue(new Error('private native failure'));
  expect(await s.send().response).toEqual({ ok: false, code: 'capture_failed' });
  expect(s.capture).not.toHaveBeenCalled(); expect(s.createStore).not.toHaveBeenCalled();
});
it('oversized UTF-8 material remains downloadable without saving or opening an editor', async () => {
  const s = setup(); const markdown = '🦉'.repeat(5 * 1024 ** 2 / 4 + 1);
  s.capture.mockResolvedValue({ ...value, markdown });
  expect(await s.send().response).toMatchObject({ ok: false, code: 'too_large', unsaved: { markdown } });
  expect(s.createStore).not.toHaveBeenCalled(); expect(s.chrome.tabs.create).not.toHaveBeenCalled();
});
