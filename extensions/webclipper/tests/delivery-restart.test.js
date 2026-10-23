import { afterEach, expect, it, vi } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';
import { createDraftStore, attemptLifecycleRevision } from '../src/storage/drafts.js';
import { createDelivery } from '../src/delivery/stateMachine.js';
import { buildImportSnapshot } from '../src/images/rewrite.js';
import { inspectImageBlob } from '../src/images/acquire.js';

const id = '550e8400-e29b-41d4-a716-446655440000';
const handles = [];
afterEach(() => handles.splice(0).forEach(s => s.close()));
async function fixture(withImage = false) {
  const factory = new IDBFactory();
  const open = () => { const s = createDraftStore({ indexedDB: factory }); handles.push(s); return s; };
  const store = open();
  await store.saveDraft({ id: 'd', title: 'Clip', filename: 'clip.md', markdown: 'original', sourceUrl: 'https://example.org/', capturedAt: '2026-10-21T12:00:00Z', base: 'work', parent: '', destinationRevision: 'old', status: 'draft' });
  if (withImage) {
    const blob = new Blob([Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAkQBADs=', 'base64')], { type: 'image/gif' });
    await store.saveDraft({ ...await store.getDraft('d'), markdown: '![original](https://cdn.example.org/image.gif)' }, { assets: [{ id: 'image', normalizedUrl: 'https://cdn.example.org/image.gif', blob, descriptor: { status: 'ready', ...await inspectImageBlob(blob) } }] });
  }
  const snapshot = await buildImportSnapshot({ ...await store.readBundle('d'), endpoint: 'http://localhost:9123', importId: id });
  const attempt = await store.beginAttempt('d', snapshot);
  let settings = { endpoint: snapshot.endpoint, token: 'secret' };
  const api = { importStatus: vi.fn(async () => ({ state: 'unknown' })), postImport: vi.fn(async () => ({ state: 'uncertain' })) };
  const factorySpy = vi.fn(() => api), load = vi.fn(async () => ({ ...settings }));
  const driver = s => createDelivery({ store: s, connectionStore: { load }, transportFactory: factorySpy, uuid: () => { throw Error('No new ID'); } });
  const transition = (state, code) => store.transitionOutcome('d', id, { state, phase: 'posting', ...(code ? { code } : {}) }, { state: attempt.state, phase: attempt.phase, lifecycleRevision: attemptLifecycleRevision(attempt) });
  async function edit(change) {
    const db = await new Promise(resolve => { const r = factory.open('igonotes-webclipper', 1); r.onsuccess = () => resolve(r.result); });
    await new Promise((resolve, reject) => { const tx = db.transaction('attempts', 'readwrite'), rows = tx.objectStore('attempts'), r = rows.get(['d', id]); r.onsuccess = () => { change(r.result); rows.put(r.result); }; tx.oncomplete = resolve; tx.onabort = () => reject(tx.error); }); db.close();
  }
  return { store, open, driver, api, load, factorySpy, transition, edit, setSettings: value => { settings = value; } };
}
it.each(['prepared', 'posting'])('close/reopen classifies sending/%s as unresolved without writing or contacting a server', async phase => {
  const f = await fixture(true);
  if (phase === 'posting') await f.transition('sending');
  const before = await f.store.readBundle('d'); f.store.close();
  const reopened = f.open(), driver = f.driver(reopened);
  expect(await driver.recoverDraft('d')).toMatchObject({ state: 'uncertain', code: 'interrupted', readOnly: true, observedState: 'sending', phase });
  expect(await reopened.readBundle('d')).toEqual(before);
  expect(f.load).not.toHaveBeenCalled(); expect(f.factorySpy).not.toHaveBeenCalled();
  await expect(reopened.deleteDraft('d')).rejects.toMatchObject({ code: 'draft_frozen' });
  expect(await driver.retry('d')).toMatchObject({ state: 'uncertain' });
  const posted = JSON.parse(await f.api.postImport.mock.calls[0][0].get('manifest').text());
  expect(posted).toEqual(before.attempts[0].manifest);
  expect(new Uint8Array(await f.api.postImport.mock.calls[0][0].get('image').arrayBuffer())).toEqual(new Uint8Array(await before.assets[0].blob.arrayBuffer()));
});
it.each([['pending', undefined], ['pending', 'clipper_recovery_required'], ['uncertain', 'clipper_request_changed']])('restores %s/%s without clearing holds', async (state, code) => {
  const f = await fixture(); await f.transition(state, code);
  const before = await f.store.readBundle('d'); f.store.close(); const s = f.open();
  expect(await f.driver(s).recoverDraft('d')).toMatchObject({ state, readOnly: true, ...(code ? { code } : {}) });
  expect(await s.readBundle('d')).toEqual(before); expect(f.load).not.toHaveBeenCalled();
});
it('opening another handle does not steal an active controller CAS; legacy ABSENCE is retained', async () => {
  const f = await fixture(); await f.edit(a => { delete a.lifecycleRevision; });
  const s = f.open(); const before = await s.readBundle('d');
  expect(await f.driver(s).recoverDraft('d')).toMatchObject({ lifecycleRevision: null });
  expect(await s.readBundle('d')).toEqual(before);
  await expect(f.store.transitionOutcome('d', id, { state: 'sending', phase: 'posting' }, { state: 'sending', phase: 'prepared', lifecycleRevision: null })).resolves.toMatchObject({ lifecycleRevision: 1 });
});
it.each([a => { a.lifecycleRevision = null; }, a => { a.manifest = {}; }, a => { a.deliverySnapshot.digest = 'bad'; }, a => { a.deliverySnapshot = null; }, a => { a.manifest.import_id = 'invalid'; }])('malformed restore fails closed, with no request, cleanup, or invented attempt', async change => {
  const f = await fixture(); await f.edit(change); const before = await f.store.readBundle('d'); f.store.close(); const s = f.open(), d = f.driver(s);
  expect(await d.recoverDraft('d')).toMatchObject({ state: 'uncertain', code: 'invalid_snapshot', readOnly: true });
  await expect(d.check('d')).rejects.toMatchObject({ code: 'invalid_snapshot' });
  expect(await s.readBundle('d')).toEqual(before); expect(f.load).not.toHaveBeenCalled();
});
it.each([
  ['state', 'future-state'], ['phase', 'future-phase'], ['state', null], ['phase', null],
  ['code', 123], ['code', null], ['code', ''], ['code', { diagnostic: 'invalid' }],
  ['result', null], ['result', {}],
])('restored invalid %s=%j is rejected before credentials, transport, lifecycle writes or cleanup', async (field, value) => {
  const f = await fixture(true);
  // Tamper only this field; immutable ID/manifest/digest/endpoint/Blob stay valid.
  await f.edit(a => { a[field] = value; });
  const before = await f.store.readBundle('d'); f.store.close();
  const reopened = f.open(), observer = f.open(), d = f.driver(reopened);
  const transition = vi.spyOn(reopened, 'transitionOutcome'), cleanup = vi.spyOn(reopened, 'finishSuccess');
  expect(await d.recoverDraft('d')).toEqual({ state: 'uncertain', code: 'invalid_snapshot', readOnly: true });
  await expect(d.check('d')).rejects.toMatchObject({ code: 'invalid_snapshot' });
  await expect(d.retry('d')).rejects.toMatchObject({ code: 'invalid_snapshot' });
  expect(f.load).not.toHaveBeenCalled(); expect(f.factorySpy).not.toHaveBeenCalled();
  expect(f.api.importStatus).not.toHaveBeenCalled(); expect(f.api.postImport).not.toHaveBeenCalled();
  expect(transition).not.toHaveBeenCalled(); expect(cleanup).not.toHaveBeenCalled();
  expect(await observer.readBundle('d')).toEqual(before);
  expect(new Uint8Array(await (await observer.getAssets('d'))[0].blob.arrayBuffer())).toEqual(new Uint8Array(await before.assets[0].blob.arrayBuffer()));
  await expect(observer.deleteDraft('d')).rejects.toMatchObject({ code: 'draft_frozen' });
});
it.each(['prepared', 'posting'])('valid pending/%s with legacy ABSENCE remains read-only and preserves live controller CAS', async phase => {
  const f = await fixture(true);
  await f.edit(a => { a.state = 'pending'; a.phase = phase; delete a.lifecycleRevision; });
  const before = await f.store.readBundle('d'), s = f.open();
  expect(await f.driver(s).recoverDraft('d')).toMatchObject({ state: 'pending', phase, lifecycleRevision: null, readOnly: true });
  expect(await f.store.readBundle('d')).toEqual(before); expect(f.load).not.toHaveBeenCalled();
  await expect(f.store.transitionOutcome('d', id, { state: 'uncertain', phase }, { state: 'pending', phase, lifecycleRevision: null })).resolves.toMatchObject({ lifecycleRevision: 1 });
});
it.each([{ label: 'missing', result: undefined }, { label: 'null', result: null }, { label: 'array', result: [] }])('restored succeeded with $label result cannot clean up', async ({ result }) => {
  const f = await fixture(true);
  await f.edit(a => { a.state = 'succeeded'; if (result === undefined) delete a.result; else a.result = result; });
  const before = await f.store.readBundle('d'); f.store.close(); const s = f.open(), d = f.driver(s), cleanup = vi.spyOn(s, 'finishSuccess');
  expect(await d.recoverDraft('d')).toEqual({ state: 'uncertain', code: 'invalid_snapshot', readOnly: true });
  await expect(d.check('d')).rejects.toMatchObject({ code: 'invalid_snapshot' });
  await expect(d.retry('d')).rejects.toMatchObject({ code: 'invalid_snapshot' });
  expect(cleanup).not.toHaveBeenCalled(); expect(f.load).not.toHaveBeenCalled(); expect(f.factorySpy).not.toHaveBeenCalled();
  expect(await s.readBundle('d')).toEqual(before);
});
it.each(['prepared', 'posting'])('matched succeeded/%s with legacy ABSENCE can finish locally without network', async phase => {
  const f = await fixture(true), attempt = (await f.store.getAttempts('d'))[0];
  const hash = [...new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(attempt.manifest.markdown)))].map(n => n.toString(16).padStart(2, '0')).join('');
  const result = { import_id: id, base: 'work', path: 'clip.md', revision: `sha256:${hash}`, warnings: [] };
  await f.edit(a => { a.state = 'succeeded'; a.phase = phase; a.result = result; delete a.lifecycleRevision; });
  const before = await f.store.readBundle('d'); f.store.close(); const s = f.open(), d = f.driver(s);
  expect(await d.recoverDraft('d')).toMatchObject({ state: 'succeeded', code: 'cleanup_pending', phase, lifecycleRevision: null, readOnly: true });
  expect(await s.readBundle('d')).toEqual(before);
  expect(await d.check('d')).toEqual({ state: 'succeeded', result });
  expect(await s.getDraft('d')).toBeUndefined(); expect(f.load).not.toHaveBeenCalled(); expect(f.factorySpy).not.toHaveBeenCalled();
});
it('endpoint change blocks GET as well as POST; original endpoint with rotated token can check again', async () => {
  const f = await fixture(); await f.transition('sending'); f.store.close(); const d = f.driver(f.open());
  await d.recoverDraft('d'); f.setSettings({ endpoint: 'http://localhost:9999', token: 'new' });
  await expect(d.check('d')).rejects.toMatchObject({ code: 'restore_endpoint' });
  await expect(d.retry('d')).rejects.toMatchObject({ code: 'restore_endpoint' });
  expect(f.factorySpy).not.toHaveBeenCalled();
  f.setSettings({ endpoint: 'http://localhost:9123', token: 'rotated' });
  await d.check('d'); expect(f.api.importStatus).toHaveBeenCalledWith(id); expect(f.api.postImport).not.toHaveBeenCalled();
});
it('persisted exact success survives cleanup failure and finishes locally only on manual check', async () => {
  const f = await fixture(), a = (await f.store.getAttempts('d'))[0];
  const hash = [...new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(a.manifest.markdown)))].map(n => n.toString(16).padStart(2, '0')).join('');
  const result = { import_id: id, base: 'work', path: 'clip.md', revision: `sha256:${hash}`, warnings: [] };
  await f.store.transitionOutcome('d', id, { state: 'succeeded', phase: 'posting', result }, { state: a.state, phase: a.phase, lifecycleRevision: a.lifecycleRevision });
  f.store.close(); const s = f.open(), d = f.driver(s);
  expect(await d.recoverDraft('d')).toMatchObject({ state: 'succeeded', code: 'cleanup_pending', readOnly: true });
  expect(await s.getDraft('d')).toBeDefined();
  const finish = s.finishSuccess; s.finishSuccess = async () => { throw Error('quota'); };
  await expect(d.check('d')).rejects.toMatchObject({ code: 'cleanup_pending' }); s.finishSuccess = finish;
  expect(await d.check('d')).toEqual({ state: 'succeeded', result });
  expect(await s.getDraft('d')).toBeUndefined(); expect(f.load).not.toHaveBeenCalled();
});

it('closing the store during POST cannot erase posting when even the hold write fails', async () => {
  const f = await fixture(true), d = f.driver(f.store);
  let release, entered;
  const started = new Promise(resolve => { entered = resolve; });
  f.api.postImport.mockImplementation(() => { entered(); return new Promise(resolve => { release = resolve; }); });
  const pending = d.retry('d'); await started;
  const before = await f.store.readBundle('d'); f.store.close();
  release({ state: 'uncertain', code: 'cancelled' });
  await expect(pending).rejects.toMatchObject({ code: 'storage_closed' });
  const s = f.open(); expect(await f.driver(s).recoverDraft('d')).toMatchObject({ state: 'uncertain', code: 'interrupted' });
  expect(await s.readBundle('d')).toEqual(before); expect(before.attempts[0].phase).toBe('posting');
});
it('malformed locally terminal receipt is retained without cleanup or network', async () => {
  const f = await fixture(); await f.edit(a => { a.state = 'succeeded'; a.result = { import_id: id, base: 'work', path: 'clip.md', revision: `sha256:${'0'.repeat(64)}`, warnings: [] }; });
  const before = await f.store.readBundle('d'); f.store.close(); const s = f.open(), d = f.driver(s);
  expect(await d.recoverDraft('d')).toMatchObject({ code: 'invalid_receipt' });
  await expect(d.check('d')).rejects.toMatchObject({ code: 'invalid_receipt' });
  expect(await s.readBundle('d')).toEqual(before); expect(f.load).not.toHaveBeenCalled();
});
