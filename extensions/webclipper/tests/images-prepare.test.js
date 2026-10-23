import { expect, it, vi } from 'vitest';
import { IDBFactory, IDBObjectStore } from 'fake-indexeddb';
import { createDraftStore } from '../src/storage/drafts.js';
import { createImageAcquirer } from '../src/images/acquire.js';
import { prepareDraftImages } from '../src/images/prepare.js';
import { buildImportSnapshot } from '../src/images/rewrite.js';

const png = Uint8Array.from(Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6Z1sAAAAASUVORK5CYII=', 'base64'));
const image = url => `![image](${url})`;
const a = 'https://cdn.example/a.png', b = 'https://cdn.example/b.png';
const draft = (markdown = image(a)) => ({ id: 'd', title: 'original', filename: 'note.md', markdown, sourceUrl: 'https://example.org/article', capturedAt: '2026-10-23T10:00:00Z', base: null, parent: '', destinationRevision: null, status: 'draft' });
async function setup(markdown, limits) {
  const indexedDB = new IDBFactory();
  const store = createDraftStore({ indexedDB, limits });
  await store.saveDraft(draft(markdown));
  return { store, indexedDB };
}
function acquirer() {
  const fetch = vi.fn(async () => new Response(png, { headers: { 'Content-Type': 'image/png' } }));
  const permissions = { contains: vi.fn(async () => true), request: vi.fn() };
  return { fetch, permissions, ...createImageAcquirer({ fetch, permissions }) };
}
function delayed(acq) {
  let release, entered;
  const started = new Promise(resolve => { entered = resolve; });
  const gate = new Promise(resolve => { release = resolve; });
  return { started, release, acquire: async (...args) => { entered(); await gate; return acq.acquire(...args); } };
}

it('commits real Blob/hash/status before progress; reopens offline without any acquisition or permission call', async () => {
  const { store, indexedDB } = await setup();
  const acq = acquirer(), observations = [];
  const result = await prepareDraftImages(store, 'd', { acquirer: acq, progress: value => { observations.push(value); } });
  expect(result.ok).toBe(true);
  expect(result.assets).toHaveLength(1);
  expect(observations.at(-1)).toMatchObject({ status: 'ready' });
  const [saved] = await store.getAssets('d');
  expect(saved.descriptor).toMatchObject({ status: 'ready', sha256: expect.stringMatching(/^[a-f0-9]{64}$/), active: true, mediaType: 'image/png', extension: 'png' });
  expect(new Uint8Array(await saved.blob.arrayBuffer())).toEqual(png);
  store.close();
  const reopened = createDraftStore({ indexedDB });
  const offline = { acquire: vi.fn(() => { throw Error('offline'); }) };
  expect((await prepareDraftImages(reopened, 'd', { acquirer: offline })).ok).toBe(true);
  expect(offline.acquire).not.toHaveBeenCalled();
  expect(acq.permissions.request).not.toHaveBeenCalled();
  reopened.close();
});

it('deduplicates canonical URL aliases and different URLs with identical bytes into one physical Blob and exact quota', async () => {
  const { store } = await setup([a, a + '#one', 'HTTPS://CDN.example:443/a.png#two', b, a].map(image).join('\n'), { maxBlobBytes: png.length });
  const acq = acquirer();
  const result = await prepareDraftImages(store, 'd', { acquirer: acq });
  expect(result.ok).toBe(true);
  expect(acq.fetch).toHaveBeenCalledTimes(2);
  const rows = await store.getAssets('d');
  expect(rows.filter(row => row.blob)).toHaveLength(1);
  expect(rows.reduce((n, row) => n + row.size, 0)).toBe(png.length);
  expect(rows.flatMap(row => row.descriptor.aliases)).toEqual(expect.arrayContaining([a, a + '#one', 'https://cdn.example/a.png#two']));
  expect(rows.flatMap(row => row.descriptor.aliases)).toContain('HTTPS://CDN.example:443/a.png#two');
  expect(rows.find(row => !row.blob).descriptor.canonicalAssetId).toBe(rows.find(row => row.blob).id);
  store.close();
});

it('recomputes current references after edits and prepares added images without selecting removed evidence', async () => {
  const { store } = await setup(); const acq = acquirer();
  const first = await prepareDraftImages(store, 'd', { acquirer: acq });
  await store.saveDraft({ ...await store.getDraft('d'), markdown: image(b) });
  const second = await prepareDraftImages(store, 'd', { acquirer: acq });
  expect(second.refs.map(ref => ref.url)).toEqual([b]);
  expect(second.assets.map(row => row.normalizedUrl)).toEqual([b]);
  expect((await store.getAssets('d')).find(row => row.id === first.assets[0].id).descriptor.active).toBe(false);
  expect(acq.fetch).toHaveBeenCalledTimes(2);
  await store.saveDraft({ ...await store.getDraft('d'), markdown: 'All images removed; [ordinary](https://cdn.example/a.png)' });
  const empty = await prepareDraftImages(store, 'd', { acquirer: acq });
  expect(empty.assets).toEqual([]);
  expect(empty.blobs).toEqual([]);
  expect(empty.refs).toEqual([]);
  store.close();
});

it('does not clobber latest title/body/attempts during delayed fetch; re-evaluates additions and removals', async () => {
  const { store, indexedDB } = await setup(); const other = createDraftStore({ indexedDB });
  const acq = acquirer(), gate = delayed(acq);
  const pending = prepareDraftImages(store, 'd', { acquirer: gate });
  await gate.started;
  await other.saveDraft({ ...await other.getDraft('d'), title: 'latest', markdown: image(b) }, { attempt: { id: 'rejected-history', createdAt: '2026-10-23T10:00:00Z', manifest: {}, deliverySnapshot: { endpoint: 'http://localhost:8080', assetIds: [], digest: 'old' } } });
  gate.release();
  const result = await pending;
  expect(result.refs.map(ref => ref.url)).toEqual([b]);
  expect(result.assets.map(row => row.normalizedUrl)).toEqual([b]);
  expect(await store.getDraft('d')).toMatchObject({ title: 'latest', markdown: image(b) });
  expect(await store.getAttempts('d')).toHaveLength(1);
  expect((await store.getAssets('d')).some(row => row.normalizedUrl === a && row.blob)).toBe(false);
  store.close(); other.close();
});

it('never recreates a deleted draft or manually removed assets after fetch', async () => {
  for (const removeDraft of [true, false]) {
    const { store } = await setup(); const gate = delayed(acquirer());
    const pending = prepareDraftImages(store, 'd', { acquirer: gate });
    await gate.started;
    if (removeDraft) await store.deleteDraft('d');
    else await store.saveDraft(await store.getDraft('d'), { deleteAssetIds: (await store.getAssets('d')).map(row => row.id) });
    gate.release();
    await expect(pending).rejects.toMatchObject({ code: removeDraft ? 'draft_deleted' : 'assets_changed' });
    expect(await store.getAssets('d')).toEqual([]);
    if (removeDraft) expect(await store.getDraft('d')).toBeUndefined();
    store.close();
  }
});

it.each(['sending', 'uncertain', 'pending'])('freezes preparation in live %s status, including status changed during fetch', async status => {
  for (const duringFetch of [false, true]) {
    const { store } = await setup(); const acq = acquirer();
    const gate = delayed(acq);
    const pending = duringFetch ? prepareDraftImages(store, 'd', { acquirer: gate }) : null;
    if (duringFetch) await gate.started;
    await store.updateMetadata('d', { base: 'work', parent: '', destinationRevision: 'revision' });
    const snapshot = await buildImportSnapshot({ ...await store.readBundle('d'), endpoint: 'http://localhost:8080', importId: '550e8400-e29b-41d4-a716-446655440000', confirmedFallbacks: [{ url: a, reason: 'explicit fixture' }] });
    await store.beginAttempt('d', snapshot);
    if (status !== 'sending') await store.transitionOutcome('d', snapshot.importId, { state: status, phase: 'posting' }, { state: 'sending', phase: 'prepared', lifecycleRevision: 0 });
    if (duringFetch) gate.release();
    await expect(pending ?? prepareDraftImages(store, 'd', { acquirer: acq })).rejects.toMatchObject({ code: 'draft_frozen' });
    if (!duringFetch) expect(acq.fetch).not.toHaveBeenCalled();
    expect((await store.getAssets('d')).every(row => !row.blob)).toBe(true);
    store.close();
  }
});

it('persists fixed acquisition errors while preserving previous ready work', async () => {
  const { store } = await setup(); const acq = acquirer();
  await prepareDraftImages(store, 'd', { acquirer: acq });
  await store.saveDraft({ ...await store.getDraft('d'), markdown: image(a) + '\n' + image(b) });
  acq.permissions.contains.mockResolvedValue(false);
  const result = await prepareDraftImages(store, 'd', { acquirer: acq });
  expect(result.ok).toBe(false);
  expect(result.assets.find(row => row.normalizedUrl === a).descriptor.status).toBe('ready');
  expect(result.assets.find(row => row.normalizedUrl === b).descriptor).toMatchObject({ status: 'error', error: { code: 'permission', message: expect.any(String) } });
  expect(acq.fetch).toHaveBeenCalledTimes(1);
  store.close();
});

it('reports persistence failure separately; rolls back ready writes and never reports uncommitted progress', async () => {
  const { store } = await setup(undefined, { maxBlobBytes: png.length - 1 }); const progress = vi.fn();
  const result = await prepareDraftImages(store, 'd', { acquirer: acquirer(), progress });
  expect(result).toMatchObject({ ok: false, persistenceError: { code: 'blob_limit' } });
  expect((await store.getAssets('d')).every(row => !row.blob && row.descriptor.status !== 'ready')).toBe(true);
  expect(progress.mock.calls.some(([value]) => value.status === 'ready')).toBe(false);
  const original = IDBObjectStore.prototype.put;
  IDBObjectStore.prototype.put = function(...args) { if (this.name === 'assets') throw new DOMException('full', 'QuotaExceededError'); return original.apply(this, args); };
  try {
    expect(await prepareDraftImages(store, 'd', { acquirer: acquirer() })).toMatchObject({ ok: false, persistenceError: { code: 'storage_quota' }, errorStatusPersistenceError: { code: 'storage_quota' } });
  } finally { IDBObjectStore.prototype.put = original; }
  store.close();
});

it('bounds acquisition batches to three, commits each before requesting more and supports more than 100 URL references', async () => {
  const urls = Array.from({ length: 103 }, (_, i) => `https://cdn.example/${i}.png`);
  const { store } = await setup(urls.map(image).join('\n'), { maxBlobBytes: png.length });
  const acq = acquirer(); let calls = 0;
  const result = await prepareDraftImages(store, 'd', { acquirer: { acquire: async (batch, options) => {
    expect(batch.length).toBeLessThanOrEqual(3);
    if (calls++) expect((await store.getAssets('d')).some(row => row.blob)).toBe(true);
    return acq.acquire(batch, options);
  } } });
  expect(result.ok).toBe(true);
  expect(result.assets).toHaveLength(103);
  expect((await store.getAssets('d')).filter(row => row.blob)).toHaveLength(1);
  store.close();
});

it('persists unsafe edited sources as explicit errors without fetch or permission prompts', async () => {
  const urls = ['javascript:bad', 'blob:https://example.org/id', 'http://127.1/image', 'data:image/svg+xml;base64,YWJj'];
  const { store } = await setup(urls.map(image).join('\n'));
  const acq = acquirer();
  const result = await prepareDraftImages(store, 'd', { acquirer: acq });
  expect(result.persistenceError).toBeUndefined();
  expect(result.assets).toHaveLength(4);
  expect(result.assets.every(row => row.descriptor.status === 'error' && row.descriptor.error.code === 'unsafe')).toBe(true);
  expect((await store.getAssets('d')).every(row => row.descriptor.error.code === 'unsafe')).toBe(true);
  expect(acq.fetch).not.toHaveBeenCalled();
  expect(acq.permissions.contains).not.toHaveBeenCalled();
  store.close();
});

it('preserves captured pending IDs, exact data aliases and revalidates cached metadata/bytes rather than trusting ready', async () => {
  const data = `data:image/png;base64,${Buffer.from(png).toString('base64')}`;
  const { store } = await setup(image(data) + '\n' + image(data));
  await store.saveDraft(await store.getDraft('d'), { assets: [{ id: 'capture-id', normalizedUrl: data, blob: null, descriptor: { status: 'pending' } }] });
  const acq = acquirer();
  const first = await prepareDraftImages(store, 'd', { acquirer: acq });
  expect(first.assets.map(row => row.id)).toEqual(['capture-id']);
  expect(acq.fetch).not.toHaveBeenCalled();
  const row = first.assets[0];
  await store.saveDraft(await store.getDraft('d'), { assets: [{ id: row.id, normalizedUrl: row.normalizedUrl, blob: new Blob(['wrong']), descriptor: row.descriptor }] });
  const acquire = vi.fn(acq.acquire);
  const repaired = await prepareDraftImages(store, 'd', { acquirer: { acquire } });
  expect(acquire).toHaveBeenCalledTimes(1);
  expect(repaired.ok).toBe(true);
  expect(new Uint8Array(await repaired.assets[0].blob.arrayBuffer())).toEqual(png);
  store.close();
});

it('notifies only after native transaction complete, never at put success', async () => {
  const { store, indexedDB } = await setup(); const observer = createDraftStore({ indexedDB });
  let completed = true;
  const original = IDBObjectStore.prototype.put;
  IDBObjectStore.prototype.put = function(...args) {
    if (this.name === 'assets') {
      completed = false;
      this.transaction.addEventListener('complete', () => { completed = true; });
    }
    return original.apply(this, args);
  };
  const reads = [];
  try {
    await prepareDraftImages(store, 'd', { acquirer: acquirer(), progress: state => {
      expect(completed).toBe(true);
      if (state.status === 'ready') reads.push(observer.getAssets('d'));
    } });
    expect(reads).toHaveLength(1);
    expect((await reads[0])[0].descriptor.status).toBe('ready');
  } finally { IDBObjectStore.prototype.put = original; }
  store.close(); observer.close();
});

it('keeps old ready bytes, latest metadata and attempts unchanged when adding distinct bytes exceeds quota', async () => {
  const { store } = await setup(undefined, { maxBlobBytes: png.length });
  const acq = acquirer();
  await prepareDraftImages(store, 'd', { acquirer: acq });
  const old = (await store.getAssets('d'))[0];
  await store.saveDraft({ ...await store.getDraft('d'), title: 'latest', markdown: image(a) + '\n' + image(b) });
  const gif = Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==', 'base64');
  acq.fetch.mockImplementation(async () => new Response(gif, { headers: { 'Content-Type': 'image/gif' } }));
  const result = await prepareDraftImages(store, 'd', { acquirer: acq });
  expect(result.persistenceError.code).toBe('blob_limit');
  expect((await store.getDraft('d')).title).toBe('latest');
  const rows = await store.getAssets('d');
  expect(rows.find(row => row.id === old.id).blob.size).toBe(png.length);
  expect(rows.find(row => row.normalizedUrl === b).descriptor).toMatchObject({ status: 'error', error: { code: 'persistence' } });
  expect(rows.filter(row => row.blob)).toHaveLength(1);
  expect(result.assets.find(row => row.normalizedUrl === b).descriptor.status).toBe('error');
  store.close();
});

it('persists HTTP and cancellation codes without modifying the original body', async () => {
  for (const cancelled of [false, true]) {
    const { store } = await setup(); const acq = acquirer(); const controller = new AbortController();
    if (cancelled) controller.abort();
    else acq.fetch.mockResolvedValue(new Response('missing', { status: 404 }));
    const result = await prepareDraftImages(store, 'd', { acquirer: acq, signal: controller.signal });
    expect(result.assets[0].descriptor.error).toMatchObject({ code: cancelled ? 'cancel' : 'http', ...(cancelled ? {} : { status: 404 }) });
    expect((await store.getDraft('d')).markdown).toBe(image(a));
    store.close();
  }
});

it('deduplicates independently cached ready owners and flattens aliases without acquiring bytes again', async () => {
  const { store } = await setup();
  const first = await prepareDraftImages(store, 'd', { acquirer: acquirer() });
  const owner = first.assets[0];
  await store.saveDraft({ ...await store.getDraft('d'), markdown: image(a) + '\n' + image(b) + '\n' + image('https://cdn.example/c.png') }, { assets: [
    { id: 'duplicate-owner', normalizedUrl: b, blob: owner.blob, descriptor: { ...owner.descriptor, canonicalAssetId: 'duplicate-owner' } },
    { id: 'existing-alias', normalizedUrl: 'https://cdn.example/c.png', blob: null, descriptor: { ...owner.descriptor, canonicalAssetId: 'duplicate-owner' } },
  ] });
  const offline = { acquire: vi.fn(() => { throw Error('offline'); }) };
  const result = await prepareDraftImages(store, 'd', { acquirer: offline });
  expect(result.ok).toBe(true);
  const rows = await store.getAssets('d');
  expect(rows.filter(row => row.blob)).toHaveLength(1);
  expect(new Set(result.assets.map(row => row.descriptor.canonicalAssetId)).size).toBe(1);
  expect(result.blobs).toHaveLength(1);
  expect(offline.acquire).not.toHaveBeenCalled();
  store.close();
});
