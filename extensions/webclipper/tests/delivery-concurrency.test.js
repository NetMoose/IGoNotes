import { afterEach, expect, it, vi } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';
import { createDraftStore } from '../src/storage/drafts.js';
import { createDelivery } from '../src/delivery/stateMachine.js';
import { buildImportSnapshot } from '../src/images/rewrite.js';
import { inspectImageBlob } from '../src/images/acquire.js';
import { createTransport } from '../src/connection/transport.js';

const importId = '550e8400-e29b-41d4-a716-446655440000';
const conflict = 'clipper_request_changed';
const handles = [];
afterEach(() => { handles.splice(0).forEach(store => store.close()); vi.unstubAllGlobals(); });
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
const expected = attempt => ({ state: attempt.state, phase: attempt.phase,
  lifecycleRevision: Object.hasOwn(attempt, 'lifecycleRevision') ? attempt.lifecycleRevision : null });
async function setup() {
  const indexedDB = new IDBFactory();
  const a = createDraftStore({ indexedDB }), b = createDraftStore({ indexedDB }); handles.push(a, b);
  const blob = new Blob([Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAkQBADs=', 'base64')], { type: 'image/gif' });
  const asset = { id: 'pixel', normalizedUrl: 'https://example.org/pixel.gif', blob, descriptor: { ...await inspectImageBlob(blob), status: 'ready' } };
  const draft = await a.saveDraft({ id: 'd', title: 'Clip', filename: 'clip.md', markdown: '![pixel](https://example.org/pixel.gif)',
    sourceUrl: 'https://example.org/page', capturedAt: '2026-10-21T12:00:00Z', base: 'inactive', parent: 'nested', destinationRevision: 'rev1', status: 'draft' }, { assets: [asset] });
  const snapshot = await buildImportSnapshot({ ...await a.readBundle('d'), endpoint: 'http://localhost:9123', importId });
  let attempt = await a.beginAttempt('d', snapshot);
  attempt = await a.transitionOutcome('d', importId, { state: 'sending', phase: 'posting' }, expected(attempt));
  attempt = await a.transitionOutcome('d', importId, { state: 'uncertain', phase: 'posting', code: 'unavailable' }, expected(attempt));
  const cookieGet = vi.fn(), fetch = vi.fn();
  vi.stubGlobal('chrome', { cookies: { get: cookieGet, getAll: cookieGet } }); vi.stubGlobal('fetch', fetch);
  const uuid = vi.fn(() => { throw Error('No new import ID in recovery'); });
  const makeController = store => {
    const api = { importStatus: vi.fn(async () => ({ state: 'unknown' })), postImport: vi.fn(async () => ({ state: 'uncertain', code: conflict })) };
    const connectionStore = { load: vi.fn(async () => ({ endpoint: 'http://localhost:9123', token: 'private-test-token' })) };
    return { api, controller: createDelivery({ store, connectionStore, transportFactory: () => api, uuid }) };
  };
  return { a, b, draft, attempt, asset, snapshot, first: makeController(a), second: makeController(b), uuid, fetch, cookieGet };
}
async function success(manifest) {
  const hash = [...new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(manifest.markdown)))].map(n => n.toString(16).padStart(2, '0')).join('');
  return { state: 'succeeded', result: { import_id: importId, base: manifest.base, path: `${manifest.parent}/${manifest.filename}`, revision: `sha256:${hash}`, warnings: [] } };
}
async function heldCheck(f, wire = false) {
  const entered = deferred(), response = deferred();
  if (wire) {
    const transport = createTransport({ endpoint: 'http://localhost:9123', token: 'private-test-token' }, { fetch: () => { entered.resolve(); return response.promise; } });
    f.second.api.importStatus.mockImplementationOnce(id => transport.importStatus(id));
  } else f.second.api.importStatus.mockImplementationOnce(() => { entered.resolve(); return response.promise; });
  // Observe rejection immediately as well, so a broken CAS path cannot create
  // an unhandled promise while another independent controller is progressing.
  const result = f.second.controller.check('d').then(value => ({ value }), error => ({ error }));
  await entered.promise;
  return { response, result };
}

it.each(['rejection', 'internal rejection', 'auth', 'matching success'])('two controllers: delayed %s cannot undo a later same-ID POST identity conflict', async kind => {
  const f = await setup();
  const delayedSuccess = await success(f.snapshot.manifest);
  const delayed = await heldCheck(f, kind === 'internal rejection'); // B owns the old uncertain/posting row.
  await f.first.controller.retry('d'); // A: unknown GET -> exact POST -> request_changed.
  const authoritative = await f.a.readBundle('d');
  expect(authoritative.draft).toMatchObject({ status: 'uncertain', activeAttemptId: importId });
  expect(authoritative.attempts[0].code).toBe(conflict);
  if (kind === 'auth') delayed.response.reject({ code: 'unauthorized' });
  else if (kind === 'internal rejection') delayed.response.resolve(new Response(JSON.stringify({ import_id: importId, state: 'rejected', error: { code: 'internal_error', message: 'private diagnostic' } }), { headers: { 'Content-Type': 'application/json' } }));
  else delayed.response.resolve(kind === 'rejection' ? { state: 'rejected', code: 'note_conflict' } : delayedSuccess);
  expect(await delayed.result).toMatchObject({ value: { state: 'uncertain', code: conflict, readOnly: true } });
  const retained = await f.b.readBundle('d');
  expect(retained.draft).toEqual(authoritative.draft);
  expect(retained.attempts).toEqual(authoritative.attempts);
  expect(retained.attempts[0].manifest).toEqual(f.snapshot.manifest);
  expect(retained.attempts[0].deliverySnapshot).toEqual(authoritative.attempts[0].deliverySnapshot);
  expect(new Uint8Array(await retained.assets[0].blob.arrayBuffer())).toEqual(new Uint8Array(await f.asset.blob.arrayBuffer()));
  for (const store of [f.a, f.b]) {
    for (const mutate of [() => store.saveDraft(f.draft), () => store.deleteDraft('d'),
      () => store.updateMetadata('d', { base: 'other', parent: '', destinationRevision: 'new' }),
      () => store.updateDraftAssets('d', { expectedMarkdown: f.draft.markdown, expectedAssets: retained.assets, assets: [] })]) {
      await expect(mutate()).rejects.toMatchObject({ code: 'draft_frozen' });
    }
  }
  // Even a fresh matching receipt cannot dissolve a persisted sticky hold.
  f.second.api.importStatus.mockResolvedValue(delayedSuccess);
  expect(await f.second.controller.check('d')).toMatchObject({ state: 'uncertain', code: conflict, readOnly: true });
  f.second.api.importStatus.mockResolvedValue({ state: 'unknown' });
  expect(await f.second.controller.retry('d')).toMatchObject({ state: 'uncertain', code: conflict, readOnly: true });
  expect(f.first.api.postImport).toHaveBeenCalledOnce(); expect(f.second.api.postImport).not.toHaveBeenCalled();
  const sent = f.first.api.postImport.mock.calls[0][0];
  expect(JSON.parse(await sent.get('manifest').text())).toEqual(f.snapshot.manifest);
  expect(new Uint8Array(await sent.get('pixel').arrayBuffer())).toEqual(new Uint8Array(await f.asset.blob.arrayBuffer()));
  expect(JSON.stringify(retained.attempts)).not.toContain('private-test-token');
  expect(f.uuid).not.toHaveBeenCalled(); expect(f.fetch).not.toHaveBeenCalled(); expect(f.cookieGet).not.toHaveBeenCalled();
  expect((await f.b.readBundle('d')).attempts).toEqual(authoritative.attempts);
});

it('same-state/phase nonsticky transitions invalidate every older lifecycle revision', async () => {
  const f = await setup();
  const changed = await f.a.transitionOutcome('d', importId, { state: 'uncertain', phase: 'posting', code: 'timeout' }, expected(f.attempt));
  await expect(f.b.transitionOutcome('d', importId, { state: 'uncertain', phase: 'posting', code: 'unavailable' }, expected(f.attempt)))
    .rejects.toMatchObject({ code: 'attempt_changed' });
  expect(changed.lifecycleRevision).toBe(f.attempt.lifecycleRevision + 1);
  expect((await f.b.getAttempts('d'))[0]).toEqual(changed);
});

it('concurrent same-pending checks emit an authoritative read-only hold without reapplying the stale response', async () => {
  const f = await setup();
  await f.a.transitionOutcome('d', importId, { state: 'pending', phase: 'posting' }, expected(f.attempt));
  f.first.api.importStatus.mockResolvedValue({ state: 'pending' });
  const delayed = await heldCheck(f);
  await f.first.controller.check('d');
  const current = await f.a.readBundle('d');
  delayed.response.resolve({ state: 'pending' });
  expect(await delayed.result).toMatchObject({ value: { state: 'pending', readOnly: true, observedState: 'pending' } });
  expect(await f.b.readBundle('d')).toEqual(current);
  expect(f.first.api.postImport).not.toHaveBeenCalled(); expect(f.second.api.postImport).not.toHaveBeenCalled();
  expect(f.uuid).not.toHaveBeenCalled();
});
