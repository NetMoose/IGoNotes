import { it, expect, vi } from 'vitest';
import { IDBFactory, IDBObjectStore } from 'fake-indexeddb';
import { createDraftStore } from '../src/storage/drafts.js';
import { createDelivery } from '../src/delivery/stateMachine.js';
import { inspectImageBlob } from '../src/images/acquire.js';

const id = '550e8400-e29b-41d4-a716-446655440000', nextId = '550e8400-e29b-41d4-a716-446655440001';
const draft = () => ({ id: 'd', title: 'Clip', filename: 'clip.md', markdown: 'hello', sourceUrl: 'https://example.org/page', capturedAt: '2026-10-21T12:00:00Z', base: null, parent: '', destinationRevision: null, status: 'draft' });
const sha = async text => [...new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text)))].map(n => n.toString(16).padStart(2, '0')).join('');
async function receipt(manifest) { return { import_id: manifest.import_id, base: manifest.base, path: [manifest.parent, manifest.filename].filter(Boolean).join('/'), revision: `sha256:${await sha(manifest.markdown)}`, warnings: manifest.external_images.map(e => e.reason) }; }
async function setup() {
  const indexedDB = new IDBFactory(), store = createDraftStore({ indexedDB }), other = createDraftStore({ indexedDB });
  await store.saveDraft(draft());
  const settings = { endpoint: 'http://localhost:9123', token: 'first-secret' };
  let lastManifest;
  const transport = { info: vi.fn(async () => ({ protocol_version: 1 })), bases: vi.fn(async () => ({ bases: ['active', 'inactive'] })),
    folders: vi.fn(async base => ({ base, folders: ['', 'nested'], destination_revision: 'rev1' })),
    postImport: vi.fn(async body => { lastManifest = JSON.parse(await body.get('manifest').text()); return { state: 'succeeded', result: await receipt(lastManifest) }; }),
    importStatus: vi.fn(async () => ({ state: 'unknown' })) };
  const factory = vi.fn(() => transport), flush = vi.fn(async () => true), uuid = vi.fn().mockReturnValueOnce(id).mockReturnValue(nextId);
  const delivery = createDelivery({ store, connectionStore: { load: async () => ({ ...settings }) }, transportFactory: factory, flush, uuid });
  async function confirm() { await delivery.loadDestinations(); await delivery.loadFolders('inactive'); return delivery.confirmDestination('d', { base: 'inactive', parent: 'nested', destinationRevision: 'rev1' }); }
  return { store, other, transport, delivery, settings, factory, flush, confirm, manifest: () => lastManifest };
}

it('requires an explicit destination with no fallback; preflight offline does not create an attempt', async () => {
  const f = await setup();
  await expect(f.delivery.send('d')).rejects.toMatchObject({ code: 'confirm_destination' });
  expect(f.transport.postImport).not.toHaveBeenCalled();
  await f.confirm(); f.transport.info.mockRejectedValue({ code: 'unavailable' });
  await expect(f.delivery.send('d')).rejects.toMatchObject({ code: 'unavailable' });
  expect(await f.store.getAttempts('d')).toEqual([]);
  expect(await f.store.getDraft('d')).toMatchObject({ status: 'draft' });
});
it('persists and freezes before fetch; validates receipt and cleans only owned data after success', async () => {
  const f = await setup(); await f.confirm(); await f.store.saveDraft({ ...draft(), id: 'other' });
  f.transport.postImport.mockImplementation(async body => {
    const bundle = await f.other.readBundle('d');
    expect(bundle.draft).toMatchObject({ status: 'sending', activeAttemptId: id });
    expect(bundle.attempts[0]).toMatchObject({ phase: 'posting' });
    await expect(f.other.saveDraft({ ...draft(), title: 'stale' })).rejects.toMatchObject({ code: 'draft_frozen' });
    const manifest = JSON.parse(await body.get('manifest').text());
    expect(body.get('manifest').type).toBe('application/json');
    expect(manifest).toEqual(bundle.attempts[0].manifest);
    expect(JSON.stringify(bundle)).not.toContain('first-secret');
    return { state: 'succeeded', result: await receipt(manifest) };
  });
  expect(await f.delivery.send('d')).toMatchObject({ state: 'succeeded', result: { base: 'inactive', path: 'nested/clip.md' } });
  expect(await f.store.getDraft('d')).toBeUndefined();
  expect(await f.store.getDraft('other')).toBeDefined();
});
it.each(['lost response', '5xx', 'unauthorized', 'timeout', 'cancelled', 'invalid_response'])('retains immutable data on %s, GET first and exact replay with rotated token', async code => {
  const f = await setup(); await f.confirm();
  const bodies = [];
  f.transport.postImport.mockImplementation(async body => { bodies.push(await body.get('manifest').text()); throw { code }; });
  expect(await f.delivery.send('d')).toMatchObject({ state: 'uncertain' });
  const before = await f.store.readBundle('d');
  f.settings.token = 'new-secret';
  f.transport.postImport.mockImplementation(async body => { bodies.push(await body.get('manifest').text()); return { state: 'succeeded', result: await receipt(JSON.parse(bodies[0])) }; });
  await f.delivery.check('d');
  expect(f.transport.postImport).toHaveBeenCalledTimes(1);
  expect(await f.delivery.retry('d')).toMatchObject({ state: 'succeeded' });
  expect(bodies[1]).toBe(bodies[0]);
  expect(before.attempts[0].id).toBe(id);
  expect(f.factory.mock.calls.at(-1)[0].token).toBe('new-secret');
});
it('lost response resolves from GET receipt without replay or destination refresh; endpoint change refuses all retry traffic', async () => {
  const f = await setup(); await f.confirm(); let result;
  f.transport.postImport.mockImplementation(async body => { result = await receipt(JSON.parse(await body.get('manifest').text())); throw { code: 'unavailable' }; });
  await f.delivery.send('d');
  f.settings.endpoint = 'http://localhost:9999';
  await expect(f.delivery.retry('d')).rejects.toMatchObject({ code: 'restore_endpoint' });
  expect(f.transport.importStatus).not.toHaveBeenCalled();
  f.settings.endpoint = 'http://localhost:9123'; f.transport.importStatus.mockResolvedValue({ state: 'succeeded', result });
  await f.delivery.check('d');
  expect(f.transport.postImport).toHaveBeenCalledTimes(1);
  expect(f.transport.folders).toHaveBeenCalledTimes(2);
});
it.each(['pending', 'recovery_required', 'uncertain'])('retains %s and never automatically repeats POST', async state => {
  const f = await setup(); await f.confirm();
  f.transport.postImport.mockResolvedValue({ state, code: state === 'uncertain' ? 'clipper_request_changed' : 'hold' });
  await f.delivery.send('d');
  await f.delivery.check('d');
  if (state === 'uncertain') expect(await f.delivery.retry('d')).toMatchObject({ code: 'clipper_request_changed', readOnly: true });
  expect(f.transport.postImport).toHaveBeenCalledTimes(1);
  expect((await f.store.getAttempts('d'))[0].manifest.import_id).toBe(id);
  expect((await f.store.getDraft('d')).status).not.toBe('draft');
});
it.each(['clipper_destination_changed', 'note_conflict', 'clipper_invalid_request', 'clipper_too_large', 'clipper_unsupported_image', 'clipper_import_abandoned'])('known rejection %s unlocks but requires reconfirmation and a fresh ID', async code => {
  const f = await setup(); await f.confirm(); f.transport.postImport.mockResolvedValueOnce({ state: 'rejected', code });
  expect(await f.delivery.send('d')).toMatchObject({ state: 'rejected', code });
  expect(await f.store.getDraft('d')).toMatchObject({ status: 'draft', activeAttemptId: null, destinationRevision: null });
  await expect(f.delivery.retry('d')).rejects.toMatchObject({ code: 'no_active_attempt' });
  await expect(f.delivery.send('d')).rejects.toMatchObject({ code: 'confirm_destination' });
  await f.confirm(); await f.delivery.send('d');
  expect(f.manifest().import_id).toBe(nextId);
});
it('stale revision or missing folder requires explicit reconfirmation; never silently refreshes', async () => {
  for (const folders of [{ base: 'inactive', folders: ['', 'nested'], destination_revision: 'rev2' }, { base: 'inactive', folders: [''], destination_revision: 'rev1' }]) {
    const f = await setup(); await f.confirm(); f.transport.folders.mockResolvedValue(folders);
    await expect(f.delivery.send('d')).rejects.toMatchObject({ code: 'confirm_destination' });
    expect((await f.store.getDraft('d')).destinationRevision).toBe('rev1');
    expect(await f.store.getAttempts('d')).toEqual([]);
    expect(f.transport.postImport).not.toHaveBeenCalled();
  }
});
it('removed or rebound base requires reconfirmation even if a later preflight would find it again', async () => {
  const f = await setup(); await f.confirm();
  f.transport.folders.mockRejectedValueOnce({ code: 'clipper_destination_not_found' });
  await expect(f.delivery.send('d')).rejects.toMatchObject({ code: 'confirm_destination' });
  await expect(f.delivery.send('d')).rejects.toMatchObject({ code: 'confirm_destination' });
  expect(await f.store.getAttempts('d')).toEqual([]);
});
it.each(['import_id', 'base', 'path', 'revision', 'warnings'])('mismatched receipt %s never cleans or unlocks', async field => {
  const f = await setup(); await f.confirm();
  f.transport.postImport.mockImplementation(async body => ({ state: 'succeeded', result: { ...await receipt(JSON.parse(await body.get('manifest').text())), [field]: field === 'warnings' ? ['unexpected'] : 'wrong' } }));
  expect(await f.delivery.send('d')).toMatchObject({ state: 'uncertain', code: 'invalid_receipt' });
  expect(await f.store.getDraft('d')).toMatchObject({ status: 'uncertain' });
});
it('cleanup failure leaves durable confirmed receipt and a manual local cleanup retry without network', async () => {
  const f = await setup(); await f.confirm();
  const finish = f.store.finishSuccess;
  f.store.finishSuccess = vi.fn(async () => { throw Error('disk'); });
  await expect(f.delivery.send('d')).rejects.toMatchObject({ code: 'cleanup_pending' });
  expect((await f.store.getAttempts('d'))[0].state).toBe('succeeded');
  f.store.finishSuccess = finish;
  await f.delivery.retry('d');
  expect(f.transport.postImport).toHaveBeenCalledTimes(1);
  expect(f.transport.importStatus).not.toHaveBeenCalled();
});
it('identity conflict remains sticky across auth errors and unknown status; never unlocks or reissues it', async () => {
  const f = await setup(); await f.confirm();
  f.transport.postImport.mockResolvedValueOnce({ state: 'uncertain', code: 'clipper_request_changed' });
  await f.delivery.send('d');
  f.transport.importStatus.mockRejectedValueOnce({ code: 'unauthorized' });
  await f.delivery.check('d');
  expect(await f.delivery.retry('d')).toMatchObject({ code: 'clipper_request_changed', readOnly: true });
  expect(f.transport.postImport).toHaveBeenCalledOnce();
});
it('terminal receipt persistence failure preserves posting snapshot and notifies the UI that it is frozen', async () => {
  const f = await setup(); await f.confirm();
  const original = f.store.transitionOutcome;
  f.store.transitionOutcome = async (...args) => { if (args[2].state === 'succeeded') throw Error('disk'); return original(...args); };
  await expect(f.delivery.send('d')).rejects.toThrow('disk');
  expect((await f.store.getAttempts('d'))[0]).toMatchObject({ phase: 'posting', state: 'sending' });
  expect(await f.store.getDraft('d')).toMatchObject({ status: 'sending', activeAttemptId: id });
  await expect(f.other.saveDraft(draft(), { requireExisting: true })).rejects.toMatchObject({ code: 'draft_frozen' });
});
it('multipart image parts and frozen bytes are identical on manual unknown-ID replay', async () => {
  const f = await setup(); await f.confirm();
  const blob = new Blob([Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==', 'base64')]);
  const descriptor = { ...await inspectImageBlob(blob), status: 'ready' };
  await f.store.saveDraft({ ...await f.store.getDraft('d'), markdown: '![x](https://example.org/a.gif)' }, { assets: [{ id: 'image1', normalizedUrl: 'https://example.org/a.gif', blob, descriptor }] });
  const bodies = [];
  f.transport.postImport.mockImplementation(async body => { bodies.push(body); throw { code: 'timeout' }; });
  await f.delivery.send('d');
  await expect(f.other.saveDraft({ ...draft(), markdown: 'different' }, { deleteAssetIds: ['image1'] })).rejects.toMatchObject({ code: 'draft_frozen' });
  await f.delivery.retry('d');
  expect([...bodies[0].keys()]).toEqual(['manifest', 'image1']);
  expect(await bodies[0].get('manifest').text()).toBe(await bodies[1].get('manifest').text());
  expect(bodies[1].get('image1').type).toBe('image/gif');
  expect(new Uint8Array(await bodies[1].get('image1').arrayBuffer())).toEqual(new Uint8Array(await blob.arrayBuffer()));
});
it('revalidates fresh images after flush and verifies frozen persisted bytes before posting, not caller Blob handles', async () => {
  const f = await setup(); await f.confirm();
  const blob = new Blob([Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==', 'base64')], { type: 'image/gif' });
  const descriptor = { ...await inspectImageBlob(blob), status: 'ready' };
  await f.store.saveDraft({ ...await f.store.getDraft('d'), markdown: '![x](https://example.org/a.gif)' }, { assets: [{ id: 'image1', normalizedUrl: 'https://example.org/a.gif', blob, descriptor }] });
  const original = IDBObjectStore.prototype.add;
  IDBObjectStore.prototype.add = function(value, ...args) {
    if (this.name === 'attempts') this.transaction.objectStore('assets').put({ draftId: 'd', id: 'image1', normalizedUrl: 'https://example.org/a.gif', size: blob.size, descriptor, blob: new Blob([new Uint8Array(blob.size)], { type: 'image/gif' }) });
    return original.call(this, value, ...args);
  };
  try {
    expect(await f.delivery.send('d')).toMatchObject({ state: 'rejected', code: 'prepare_again' });
    expect(f.transport.postImport).not.toHaveBeenCalled();
    expect((await f.store.getDraft('d')).status).toBe('draft');
  } finally { IDBObjectStore.prototype.add = original; }
});
it('unacknowledged image errors block new sends despite a cached ready flag; exact ordered warning strings succeed', async () => {
  const f = await setup(); await f.confirm();
  await f.store.saveDraft({ ...await f.store.getDraft('d'), markdown: '![x](https://example.org/a.gif)' });
  await expect(f.delivery.send('d', { ready: true })).rejects.toThrow();
  expect(await f.store.getAttempts('d')).toEqual([]);
  await expect(f.delivery.send('d', { confirmedFallbacks: [{ url: 'https://example.org/a.gif', reason: '<literal is disallowed>' }] })).rejects.toThrow();
  const result = await f.delivery.send('d', { confirmedFallbacks: [{ url: 'https://example.org/a.gif', reason: 'Explicit external image.' }] });
  expect(result.result.warnings).toEqual(['Explicit external image.']);
});
