import { afterEach, expect, it, vi } from 'vitest';
import { IDBFactory, IDBDatabase, IDBObjectStore } from 'fake-indexeddb';
import { createDraftStore, DraftStorageError } from '../src/storage/drafts.js';
import { prepareDraftImages } from '../src/images/prepare.js';
import { inspectImageBlob } from '../src/images/acquire.js';

const a = 'https://cdn.example/a.gif';
const bytes = Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAkQBADs=', 'base64');
const handles = [];
afterEach(() => { vi.restoreAllMocks(); handles.splice(0).forEach(s => s.close()); });
function deferred() { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; }
async function fixture(markdown = `![a](${a})`) {
  const indexedDB = new IDBFactory(), store = createDraftStore({ indexedDB }); handles.push(store);
  await store.saveDraft({ id: 'd', title: 'Title', filename: 'note.md', markdown, sourceUrl: 'https://example.org/', capturedAt: '2026-10-23T10:00:00Z', base: null, parent: '', destinationRevision: null, status: 'draft' });
  const blob = new Blob([bytes], { type: 'image/gif' }), actual = await inspectImageBlob(blob);
  const result = url => ({ ok: true, url, blob, ...actual });
  const acquirer = { acquire: async (urls, options) => { const results = urls.map(result); for (const r of results) await options.onResult?.(r); return results; } };
  return { indexedDB, store, result, acquirer };
}
it.each(['result-read', 'cached-read', 'digest'])('abort during %s verification starts zero further checkpoints and emits no late progress', async stage => {
  const f = await fixture();
  if (stage === 'cached-read') await prepareDraftImages(f.store, 'd', { acquirer: f.acquirer });
  const before = await f.store.readBundle('d'), entered = deferred(), release = deferred(), controller = new AbortController();
  const prototype = stage === 'digest' ? crypto.subtle : Blob.prototype;
  const method = stage === 'digest' ? 'digest' : 'arrayBuffer';
  const original = prototype[method]; let armed = true;
  vi.spyOn(prototype, method).mockImplementation(async function (...args) {
    if (armed) { armed = false; entered.resolve(); await release.promise; }
    return original.apply(this, args);
  });
  const updates = vi.spyOn(f.store, 'updateDraftAssets'), progress = vi.fn();
  const pending = prepareDraftImages(f.store, 'd', { acquirer: f.acquirer, signal: controller.signal, progress });
  await entered.promise;
  const writesBeforeAbort = updates.mock.calls.length, progressBeforeAbort = progress.mock.calls.length;
  controller.abort(); release.resolve(); const result = await pending;
  expect(updates.mock.calls.length - writesBeforeAbort, 'writes started after abort').toBe(0);
  expect(progress).toHaveBeenCalledTimes(progressBeforeAbort);
  expect(result).toMatchObject({ ok: false, cancelled: true });
  expect(result.persistenceError).toBeUndefined(); expect(result.errorStatusPersistenceError).toBeUndefined();
  const after = await f.store.readBundle('d');
  if (stage === 'cached-read') expect(after).toEqual(before);
  else { expect(after.assets.filter(row => row.blob)).toEqual([]); expect(after.assets[0].descriptor.status).toBe('pending'); }
});
it('abort while the initial asset read is stalled starts no checkpoint or acquisition', async () => {
  const f = await fixture(), entered = deferred(), release = deferred(), controller = new AbortController();
  const read = f.store.getAssets;
  vi.spyOn(f.store, 'getAssets').mockImplementation(async (...args) => { const value = await read(...args); entered.resolve(); await release.promise; return value; });
  const updates = vi.spyOn(f.store, 'updateDraftAssets'), acquire = vi.spyOn(f.acquirer, 'acquire'), progress = vi.fn();
  const pending = prepareDraftImages(f.store, 'd', { acquirer: f.acquirer, signal: controller.signal, progress });
  await entered.promise; controller.abort(); release.resolve();
  expect(await pending).toMatchObject({ ok: false, cancelled: true });
  expect(updates).not.toHaveBeenCalled(); expect(acquire).not.toHaveBeenCalled(); expect(progress).not.toHaveBeenCalled();
});
it('abort during a markdown_changed reread stops the CAS retry loop without an error checkpoint', async () => {
  const f = await fixture(), controller = new AbortController(), entered = deferred(), release = deferred();
  vi.spyOn(f.store, 'updateDraftAssets').mockRejectedValueOnce(new DraftStorageError('markdown_changed', 'Recompute'));
  const read = f.store.getDraft; let reads = 0;
  vi.spyOn(f.store, 'getDraft').mockImplementation(async (...args) => { const value = await read(...args); if (++reads === 2) { entered.resolve(); await release.promise; } return value; });
  const progress = vi.fn(), pending = prepareDraftImages(f.store, 'd', { acquirer: f.acquirer, signal: controller.signal, progress });
  await entered.promise; controller.abort(); release.resolve();
  expect(await pending).toMatchObject({ ok: false, cancelled: true });
  expect(f.store.updateDraftAssets).toHaveBeenCalledOnce(); expect(progress).not.toHaveBeenCalled();
});
it('abort during an error checkpoint wait does not replace pending metadata with a persistence error', async () => {
  const f = await fixture(), controller = new AbortController(), entered = deferred(), release = deferred();
  const update = f.store.updateDraftAssets;
  let calls = 0;
  vi.spyOn(f.store, 'updateDraftAssets').mockImplementation(async (...args) => {
    if (++calls === 2) throw new DraftStorageError('storage_quota', 'Full');
    if (calls === 3) { entered.resolve(); await release.promise; }
    return update(...args);
  });
  const pending = prepareDraftImages(f.store, 'd', { acquirer: f.acquirer, signal: controller.signal });
  await entered.promise; controller.abort(); release.resolve();
  expect(await pending).toMatchObject({ ok: false, cancelled: true });
  const [row] = await f.store.getAssets('d'); expect(row.descriptor.status).toBe('pending'); expect(row.descriptor.error).toBeUndefined(); expect(row.blob).toBeNull();
});
it('a real queued IDB checkpoint aborts and rolls back instead of committing after cancellation', async () => {
  const f = await fixture(), controller = new AbortController();
  const db = await new Promise(resolve => { const r = f.indexedDB.open('igonotes-webclipper', 1); r.onsuccess = () => resolve(r.result); });
  const blocker = db.transaction(['drafts', 'assets', 'attempts'], 'readwrite');
  let blocking = true;
  const keepAlive = () => { const r = blocker.objectStore('drafts').get('d'); r.onsuccess = () => { if (blocking) keepAlive(); }; }; keepAlive();
  const blocked = new Promise(resolve => { blocker.oncomplete = resolve; });
  const created = deferred(), transaction = IDBDatabase.prototype.transaction;
  vi.spyOn(IDBDatabase.prototype, 'transaction').mockImplementation(function (...args) { const tx = transaction.apply(this, args); if (args[1] === 'readwrite') created.resolve(); return tx; });
  const pending = f.store.updateDraftAssets('d', { expectedMarkdown: `![a](${a})`, expectedAssets: [],
    assets: [{ id: 'image', normalizedUrl: a, blob: f.result(a).blob, descriptor: { status: 'ready' } }], signal: controller.signal });
  const rejected = expect(pending).rejects.toMatchObject({ code: 'storage_cancelled' });
  // Reads cannot run behind the blocker; use the known immutable fixture body.
  try {
    await created.promise; controller.abort(); blocking = false; await blocked;
    await rejected;
    expect(await f.store.getAssets('d')).toEqual([]);
  } finally { blocking = false; db.close(); }
});
it('aborting after a put but before transaction completion rolls back the uncommitted Blob', async () => {
  const f = await fixture(), controller = new AbortController(), put = IDBObjectStore.prototype.put;
  vi.spyOn(IDBObjectStore.prototype, 'put').mockImplementation(function (...args) { const req = put.apply(this, args); if (this.name === 'assets') controller.abort(); return req; });
  await expect(f.store.updateDraftAssets('d', { expectedMarkdown: `![a](${a})`, expectedAssets: [],
    assets: [{ id: 'image', normalizedUrl: a, blob: f.result(a).blob, descriptor: { status: 'ready' } }], signal: controller.signal })).rejects.toMatchObject({ code: 'storage_cancelled' });
  expect(await f.store.getAssets('d')).toEqual([]);
});
it('abort after commit retains the committed image and never serializes the signal', async () => {
  const f = await fixture(), controller = new AbortController();
  await f.store.updateDraftAssets('d', { expectedMarkdown: `![a](${a})`, expectedAssets: [],
    assets: [{ id: 'image', normalizedUrl: a, blob: f.result(a).blob, descriptor: { status: 'ready' } }], signal: controller.signal });
  controller.abort(); const before = await f.store.readBundle('d');
  expect(before.assets[0].blob.size).toBe(bytes.length);
  expect(JSON.stringify(before)).not.toContain('signal');
  await expect(f.store.updateDraftAssets('d', { expectedMarkdown: before.draft.markdown, expectedAssets: before.assets, assets: [], signal: controller.signal })).rejects.toMatchObject({ code: 'storage_cancelled' });
  expect(await f.store.readBundle('d')).toEqual(before);
});
it('abort after a completed checkpoint but before its return retains bytes without late ready progress', async () => {
  const f = await fixture(), controller = new AbortController(), entered = deferred(), release = deferred();
  const update = f.store.updateDraftAssets;
  vi.spyOn(f.store, 'updateDraftAssets').mockImplementation(async (...args) => {
    const saved = await update(...args);
    if (args[1].assets.some(row => row.blob)) { entered.resolve(); await release.promise; }
    return saved;
  });
  const progress = vi.fn(), pending = prepareDraftImages(f.store, 'd', { acquirer: f.acquirer, signal: controller.signal, progress });
  await entered.promise; const before = await f.store.readBundle('d'), progressBeforeAbort = progress.mock.calls.length;
  controller.abort(); release.resolve();
  expect(await pending).toMatchObject({ ok: false, cancelled: true });
  expect(await f.store.readBundle('d')).toEqual(before);
  expect(before.assets[0].blob.size).toBe(bytes.length); expect(progress).toHaveBeenCalledTimes(progressBeforeAbort);
  expect(progress.mock.calls.some(([p]) => p.status === 'ready')).toBe(false);
});
