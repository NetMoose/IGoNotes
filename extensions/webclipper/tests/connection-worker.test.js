import { afterEach, it, expect, vi } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';
import { createDraftStore } from '../src/storage/drafts.js';

afterEach(() => { vi.unstubAllGlobals(); vi.resetModules(); });
it('restricts storage on every worker start and on install/browser startup without reading secrets', async () => {
  const installed = vi.fn(); const startup = vi.fn(); const setAccessLevel = vi.fn(async () => {});
  const get = vi.fn();
  vi.stubGlobal('chrome', { storage: { local: { setAccessLevel, get } }, runtime: {
    onInstalled: { addListener: installed }, onStartup: { addListener: startup }, onMessage: { addListener: vi.fn() },
  } });
  await import('../src/worker.js');
  expect(setAccessLevel).toHaveBeenCalledWith({ accessLevel: 'TRUSTED_CONTEXTS' });
  expect(installed).toHaveBeenCalledOnce(); expect(startup).toHaveBeenCalledOnce();
  await installed.mock.calls[0][0](); await startup.mock.calls[0][0]();
  expect(setAccessLevel).toHaveBeenCalledTimes(3);
  expect(get).not.toHaveBeenCalled();
});
it('worker re-evaluation registers without awaiting protection and opens the same durable DB without reading credentials', async () => {
  const indexedDB = new IDBFactory(); vi.stubGlobal('indexedDB', indexedDB);
  let release;
  const setAccessLevel = vi.fn(() => new Promise(resolve => { release = resolve; }));
  const get = vi.fn(() => { throw new Error('Credentials must not be read'); });
  const value = { mode: 'article', title: 'Captured', markdown: '# Converted body', images: [], sourceUrl: 'https://example.test/', capturedAt: '2026-10-23T00:00:00Z' };
  const addListener = vi.fn();
  const chrome = { storage: { local: { get, setAccessLevel } }, runtime: {
    id: 'own', getURL: path => `chrome-extension://own/${path}`,
    onInstalled: { addListener: vi.fn() }, onStartup: { addListener: vi.fn() }, onMessage: { addListener },
  }, tabs: { query: vi.fn(async () => [{ id: 1, url: value.sourceUrl }]), get: vi.fn(async () => ({ id: 1, url: value.sourceUrl })), create: vi.fn(async () => {}) },
  scripting: { executeScript: vi.fn().mockResolvedValueOnce([{ frameId: 0, documentId: 'doc' }]).mockResolvedValueOnce([{ frameId: 0, documentId: 'doc', result: { ok: true, value } }]) } };
  vi.stubGlobal('chrome', chrome);
  await import('../src/worker.js');
  expect(addListener).toHaveBeenCalledOnce(); // startup protection is still pending
  release(); setAccessLevel.mockResolvedValue(undefined);
  const response = await new Promise(resolve => {
    expect(addListener.mock.calls[0][0]({ type: 'capture', mode: 'article' }, { id: 'own', url: 'chrome-extension://own/popup.html', origin: 'chrome-extension://own' }, resolve)).toBe(true);
  });
  expect(response.ok).toBe(true);
  vi.resetModules();
  await import('../src/worker.js');
  expect(addListener).toHaveBeenCalledTimes(2);
  const store = createDraftStore({ indexedDB });
  expect((await store.getDraft(response.draftId)).markdown).toBe(value.markdown);
  expect(await store.listDrafts()).toHaveLength(1); store.close();
  expect(get).not.toHaveBeenCalled();
});
