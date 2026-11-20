import { afterEach, expect, it } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';
import { createDraftStore } from '../src/storage/drafts.js';
import { createDelivery } from '../src/delivery/stateMachine.js';
import { buildImportSnapshot } from '../src/images/rewrite.js';

const importId = '550e8400-e29b-41d4-a716-446655440000';
const handles = [];
afterEach(() => handles.splice(0).forEach(store => store.close()));
const expected = attempt => ({ state: attempt.state, phase: attempt.phase, lifecycleRevision: attempt.lifecycleRevision });
const result = { import_id: importId, base: 'work', path: 'clip.md', revision: `sha256:${'a'.repeat(64)}`, warnings: [] };
async function setup(withHistory = false) {
  const indexedDB = new IDBFactory(), store = createDraftStore({ indexedDB }), other = createDraftStore({ indexedDB }); handles.push(store, other);
  const history = { id: 'historical', createdAt: '2026-10-21T12:00:00Z', manifest: { markdown: 'Old immutable history' }, deliverySnapshot: { endpoint: 'http://localhost:9123', digest: 'historical-digest', assetIds: [] } };
  await store.saveDraft({ id: 'd', title: 'Clip', filename: 'clip.md', markdown: 'hello', sourceUrl: 'https://example.org/page', capturedAt: '2026-10-21T12:00:00Z', base: 'work', parent: '', destinationRevision: 'rev1', status: 'draft' }, withHistory ? { attempt: history } : {});
  const snapshot = await buildImportSnapshot({ ...await store.readBundle('d'), endpoint: 'http://localhost:9123', importId });
  const attempt = await store.beginAttempt('d', snapshot);
  async function rawEdit(change) {
    const db = await new Promise((resolve, reject) => { const r = indexedDB.open('igonotes-webclipper', 1); r.onsuccess = () => resolve(r.result); r.onerror = () => reject(r.error); });
    try {
      await new Promise((resolve, reject) => {
        const tx = db.transaction('attempts', 'readwrite'), rows = tx.objectStore('attempts'), read = rows.get(['d', importId]);
        read.onsuccess = () => { change(read.result); rows.put(read.result); };
        tx.oncomplete = resolve; tx.onabort = () => reject(tx.error);
      });
    } finally { db.close(); }
  }
  return { store, other, snapshot, attempt, rawEdit };
}

it('starts at revision zero and increments even identical lifecycle transitions; expected revision is required', async () => {
  const f = await setup();
  expect(f.attempt.lifecycleRevision).toBe(0);
  let current = f.attempt;
  for (let revision = 1; revision <= 4; revision++) {
    current = await f.store.transitionOutcome('d', importId, { state: 'sending', phase: 'prepared' }, expected(current));
    expect(current.lifecycleRevision).toBe(revision);
  }
  await expect(f.other.transitionOutcome('d', importId, { state: 'sending', phase: 'prepared' }, { state: 'sending', phase: 'prepared' }))
    .rejects.toMatchObject({ code: 'invalid_data' });
  await expect(f.other.transitionOutcome('d', importId, { state: 'sending', phase: 'prepared', lifecycleRevision: 0 }, expected(current)))
    .rejects.toMatchObject({ code: 'invalid_data' });
  expect((await f.store.getAttempts('d'))[0]).toEqual(current);
});

it('storage refuses every transition and cleanup of a CURRENT sticky conflict even with its latest revision', async () => {
  const f = await setup();
  const held = await f.store.transitionOutcome('d', importId, { state: 'uncertain', phase: 'posting', code: 'clipper_request_changed' }, expected(f.attempt));
  const before = await f.other.readBundle('d');
  for (const outcome of [
    { state: 'uncertain', phase: 'posting', code: 'unauthorized' },
    { state: 'pending', phase: 'posting' }, { state: 'rejected', phase: 'posting', code: 'note_conflict' },
    { state: 'succeeded', phase: 'posting', result }, { state: 'sending', phase: 'posting' },
    { state: 'uncertain', phase: 'posting', code: 'clipper_request_changed' },
  ]) await expect(f.other.transitionOutcome('d', importId, outcome, expected(held))).rejects.toMatchObject({ code: 'identity_conflict' });
  await expect(f.other.finishSuccess('d', importId, held.lifecycleRevision)).rejects.toMatchObject({ code: 'identity_conflict' });
  expect(await f.store.readBundle('d')).toEqual(before);
  // Defense for any pre-existing terminal+sticky metadata: cleanup checks the
  // current code too, rather than inferring permission from state alone.
  await f.rawEdit(row => { row.state = 'succeeded'; row.result = result; });
  await expect(f.store.finishSuccess('d', importId, held.lifecycleRevision)).rejects.toMatchObject({ code: 'identity_conflict' });
  expect(await f.store.getDraft('d')).toMatchObject({ status: 'uncertain', activeAttemptId: importId });
});

it.each([-1, 0.5, Number.MAX_SAFE_INTEGER + 1, NaN, Infinity, '0', undefined])('rejects invalid expected lifecycle revision %s without writes', async revision => {
  const f = await setup(), before = await f.store.readBundle('d');
  await expect(f.store.transitionOutcome('d', importId, { state: 'sending', phase: 'posting' }, { ...expected(f.attempt), lifecycleRevision: revision }))
    .rejects.toMatchObject({ code: 'invalid_data' });
  expect(await f.other.readBundle('d')).toEqual(before);
});

it.each([null, -1, 0.5, Number.MAX_SAFE_INTEGER + 1, NaN, Infinity, '0'])('rejects invalid persisted lifecycle revision %s without writes or fallback to legacy', async revision => {
  const f = await setup();
  await f.rawEdit(row => { row.lifecycleRevision = revision; });
  const before = await f.store.readBundle('d');
  await expect(f.other.transitionOutcome('d', importId, { state: 'sending', phase: 'posting' }, { ...expected(f.attempt), lifecycleRevision: 0 }))
    .rejects.toMatchObject({ code: 'invalid_data' });
  expect(await f.store.readBundle('d')).toEqual(before);
});

it('the safe-integer boundary remains readable and refuses overflow atomically', async () => {
  const f = await setup();
  await f.rawEdit(row => { row.lifecycleRevision = Number.MAX_SAFE_INTEGER - 1; });
  const previous = (await f.store.getAttempts('d'))[0];
  const last = await f.other.transitionOutcome('d', importId, { state: 'uncertain', phase: 'posting', code: 'timeout' }, expected(previous));
  expect(last.lifecycleRevision).toBe(Number.MAX_SAFE_INTEGER);
  const before = await f.store.readBundle('d');
  await expect(f.store.transitionOutcome('d', importId, { state: 'uncertain', phase: 'posting', code: 'unavailable' }, expected(last)))
    .rejects.toMatchObject({ code: 'lifecycle_exhausted' });
  expect(await f.other.readBundle('d')).toEqual(before);
});

it('legacy missing revision uses an explicit null CAS once, retains immutable history, and upgrades only lifecycle JSON', async () => {
  const f = await setup(true);
  const history = (await f.store.getAttempts('d')).find(row => row.id === 'historical');
  await f.rawEdit(row => { delete row.lifecycleRevision; });
  const legacy = (await f.store.getAttempts('d'))[0];
  expect(Object.hasOwn(legacy, 'lifecycleRevision')).toBe(false); // RO reads do not rewrite history.
  await expect(f.other.transitionOutcome('d', importId, { state: 'uncertain', phase: 'posting' }, { state: 'sending', phase: 'prepared', lifecycleRevision: 0 }))
    .rejects.toMatchObject({ code: 'attempt_changed' });
  const migrated = await f.other.transitionOutcome('d', importId, { state: 'uncertain', phase: 'posting' }, { state: 'sending', phase: 'prepared', lifecycleRevision: null });
  expect(migrated.lifecycleRevision).toBe(1);
  expect(migrated.manifest).toEqual(legacy.manifest); expect(migrated.deliverySnapshot).toEqual(legacy.deliverySnapshot);
  expect(migrated.id).toBe(legacy.id); expect(migrated.createdAt).toBe(legacy.createdAt);
  await expect(f.store.transitionOutcome('d', importId, { state: 'uncertain', phase: 'posting' }, { state: 'uncertain', phase: 'posting', lifecycleRevision: null }))
    .rejects.toMatchObject({ code: 'attempt_changed' });
  expect((await f.store.getAttempts('d')).find(row => row.id === 'historical')).toEqual(history);
});

it('controller explicitly recovers a legacy active attempt without allocating an ID or reposting', async () => {
  const f = await setup();
  await f.rawEdit(row => { delete row.lifecycleRevision; });
  const controller = createDelivery({ store: f.other, connectionStore: { load: async () => ({ endpoint: 'http://localhost:9123', token: 'private' }) },
    transportFactory: () => ({ importStatus: async () => ({ state: 'pending' }), postImport: () => { throw Error('No POST'); } }), uuid: () => { throw Error('No new ID'); } });
  expect(await controller.check('d')).toMatchObject({ state: 'pending' });
  const current = (await f.store.getAttempts('d'))[0];
  expect(current.lifecycleRevision).toBe(1); expect(current.manifest).toEqual(f.snapshot.manifest);
});

it('confirmed terminal cleanup requires the exact persisted lifecycle revision', async () => {
  const f = await setup();
  const terminal = await f.store.transitionOutcome('d', importId, { state: 'succeeded', phase: 'posting', result }, expected(f.attempt));
  await expect(f.other.finishSuccess('d', importId, terminal.lifecycleRevision - 1)).rejects.toMatchObject({ code: 'attempt_changed' });
  expect(await f.other.getDraft('d')).toBeDefined();
  await f.other.finishSuccess('d', importId, terminal.lifecycleRevision);
  expect(await f.store.getDraft('d')).toBeUndefined();
});
