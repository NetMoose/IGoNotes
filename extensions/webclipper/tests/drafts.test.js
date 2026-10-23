import { describe, it, expect } from 'vitest';
import { IDBFactory, IDBObjectStore } from 'fake-indexeddb';
import { createDraftStore, MAX_DRAFTS, MAX_BLOB_BYTES } from '../src/storage/drafts.js';

const draft = (id = 'a', title = 'Заметка 🦉') => ({ id, title, filename: 'note.md', markdown: '# hello', sourceUrl: 'https://example.org/article?q=1', capturedAt: '2026-10-23T10:00:00Z', base: null, parent: '', destinationRevision: null, status: 'draft' });
const asset = (id = 'image', text = 'abc') => ({ id, normalizedUrl: 'https://example.org/image.png', descriptor: { mime: 'image/png' }, blob: new Blob([text]), size: 999999 });
const attempt = (id = 'try') => ({ id, createdAt: '2026-10-23T10:00:00Z', manifest: { import_id: id, title: 'immutable' }, deliverySnapshot: { endpoint: 'http://localhost:8080', assetIds: ['image'], digest: 'sha256:example' } });
function setup(options = {}) {
  const indexedDB = new IDBFactory();
  return { indexedDB, store: createDraftStore({ indexedDB, ...options }) };
}
function request(req) { return new Promise((resolve, reject) => { req.onsuccess = () => resolve(req.result); req.onerror = () => reject(req.error); }); }

describe('local draft storage', () => {
  it('uses production application caps', () => {
    expect(MAX_DRAFTS).toBe(50);
    expect(MAX_BLOB_BYTES).toBe(250 * 1024 ** 2);
  });

  it('atomically persists Unicode metadata, real Blob sizes and immutable attempts across reopen', async () => {
    const { store, indexedDB } = setup();
    await store.saveDraft(draft(), { assets: [asset()], attempt: attempt() });
    store.close();
    const reopened = createDraftStore({ indexedDB });
    expect((await reopened.getDraft('a')).title).toBe('Заметка 🦉');
    const [saved] = await reopened.getAssets('a');
    expect(saved.size).toBe(3);
    expect(await saved.blob.text()).toBe('abc');
    expect((await reopened.getAttempts('a'))[0].manifest.title).toBe('immutable');
    await expect(reopened.saveDraft(draft('a', 'changed'), { attempt: attempt() })).rejects.toMatchObject({ code: 'storage_conflict' });
    expect((await reopened.getDraft('a')).title).toBe('Заметка 🦉');
    reopened.close();
  });

  it.each(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])('preserves exact %s data source variants, Blob and metadata across reopen', async mime => {
    const { store, indexedDB } = setup();
    const sources = [`data:${mime};base64,YWJj`, `DATA:${mime.toUpperCase()};BASE64,YQ==`, `data:${mime};base64,YWI=`, `data:${mime};base64,YQ`, `data:${mime};base64,YWI`, `data:${mime},%00%fF%2cabc`];
    const assets = sources.map((normalizedUrl, i) => ({ ...asset(String(i)), normalizedUrl, descriptor: { mime, status: 'ready', originalSource: normalizedUrl }, blob: new Blob([new Uint8Array([0, 255, i])], { type: mime }) }));
    await store.saveDraft(draft(), { assets });
    store.close();
    const reopened = createDraftStore({ indexedDB });
    const saved = await reopened.getAssets('a');
    for (let i = 0; i < assets.length; i++) {
      expect(saved[i]).toEqual({ ...assets[i], draftId: 'a', size: 3 });
      expect(saved[i].blob.type).toBe(mime);
      expect(new Uint8Array(await saved[i].blob.arrayBuffer())).toEqual(new Uint8Array([0, 255, i]));
    }
    reopened.close();
  });

  it('keeps data image Blob accounting exact and preserves previous work on quota failure', async () => {
    const { store } = setup({ limits: { maxBlobBytes: 3 } });
    const normalizedUrl = 'data:image/png;base64,YWJj';
    await store.saveDraft(draft(), { assets: [{ ...asset(), normalizedUrl, size: 0 }] });
    await store.saveDraft(draft('a', 'updated'), { assets: [{ ...asset('image', '123'), normalizedUrl, size: 999999 }] });
    await expect(store.saveDraft(draft('a', 'lost'), { assets: [{ ...asset('image', '1234'), normalizedUrl, size: 0 }] })).rejects.toMatchObject({ code: 'blob_limit' });
    expect((await store.getDraft('a')).title).toBe('updated');
    expect(await (await store.getAssets('a'))[0].blob.text()).toBe('123');
    store.close();
  });

  it.each([
    'data:image/svg+xml;base64,YWJj', 'data:text/html,%3Cscript%3E',
    'data:image/jpg;base64,YWJj', 'data:image/apng;base64,YWJj',
    'data:application/javascript,alert(1)',
    'data:text/plain;base64,YWJj', 'javascript:alert(1)', 'blob:https://example.org/id',
    'data:image/png;base64,', 'data:image/png;base64,Y', 'data:image/png;base64,Y===',
    'data:image/png;base64,YW Jj', 'data:image/png;base64,YWJj#fragment',
    'data:image/png,%GG', 'data:image/png,%0', 'data:image/png,abc\n',
    'data:image/png,', 'data:image/png,<script>', 'data:image/png;charset=utf-8,abc',
  ])('rejects unsafe or malformed asset source %s', async normalizedUrl => {
    const { store } = setup();
    await expect(store.saveDraft(draft(), { assets: [{ ...asset(), normalizedUrl }] })).rejects.toMatchObject({ code: 'invalid_data' });
    expect(await store.listDrafts()).toEqual([]);
    store.close();
  });

  it('rejects data sources as page URLs and attempt endpoints', async () => {
    const { store } = setup();
    const dataUrl = 'data:image/png;base64,YWJj';
    await expect(store.saveDraft({ ...draft(), sourceUrl: dataUrl })).rejects.toMatchObject({ code: 'invalid_data' });
    await expect(store.saveDraft(draft(), { attempt: { ...attempt(), deliverySnapshot: { ...attempt().deliverySnapshot, endpoint: dataUrl } } })).rejects.toMatchObject({ code: 'invalid_data' });
    expect(await store.listDrafts()).toEqual([]);
    store.close();
  });

  it('bounds data source metadata at 32 MiB characters without truncation or modifying old work', async () => {
    const { store } = setup({ limits: { maxBlobBytes: 3 } });
    const prefix = 'data:image/png,';
    const normalizedUrl = prefix + 'a'.repeat(32 * 1024 ** 2 - prefix.length);
    await store.saveDraft(draft(), { assets: [{ ...asset(), normalizedUrl }] });
    await expect(store.saveDraft(draft('a', 'lost'), { assets: [{ ...asset(), normalizedUrl: normalizedUrl + 'a' }] })).rejects.toMatchObject({ code: 'invalid_data' });
    expect((await store.getAssets('a'))[0].normalizedUrl).toBe(normalizedUrl);
    expect((await store.getDraft('a')).title).toBe('Заметка 🦉');
    store.close();
  });

  it('enforces count and exact replacement byte deltas without evicting old work', async () => {
    const { store } = setup({ limits: { maxDrafts: 2, maxBlobBytes: 5 } });
    await store.saveDraft(draft(), { assets: [asset()] });
    await store.saveDraft(draft('b'));
    await expect(store.saveDraft(draft('c'))).rejects.toMatchObject({ code: 'draft_limit' });
    await store.saveDraft(draft('a', 'updated'), { assets: [asset('image', '12345')] });
    await expect(store.saveDraft(draft('a', 'lost'), { assets: [asset('image', '123456')] })).rejects.toMatchObject({ code: 'blob_limit' });
    expect((await store.getDraft('a')).title).toBe('updated');
    expect(await (await store.getAssets('a'))[0].blob.text()).toBe('12345');
    expect(await store.listDrafts()).toHaveLength(2);
    store.close();
  });

  it.each(['count', 'bytes'])('serializes global %s caps across independent database handles', async (kind) => {
    const indexedDB = new IDBFactory();
    const options = { indexedDB, limits: { maxDrafts: kind === 'count' ? 1 : 50, maxBlobBytes: 3 } };
    const one = createDraftStore(options), two = createDraftStore(options);
    const results = await Promise.allSettled([one.saveDraft(draft('a'), { assets: [asset()] }), two.saveDraft(draft('b'), { assets: [asset()] })]);
    expect(results.filter(r => r.status === 'fulfilled')).toHaveLength(1);
    expect(await one.listDrafts()).toHaveLength(1);
    one.close(); two.close();
  });

  it.each(['QuotaExceededError', 'UnknownError'])('rolls back already queued writes on runtime %s without masking other errors', async (name) => {
    const { store } = setup();
    await store.saveDraft(draft(), { assets: [asset()], attempt: attempt() });
    const original = IDBObjectStore.prototype.add;
    IDBObjectStore.prototype.add = function(value, ...args) {
      if (this.name === 'attempts') throw new DOMException('injected failure after asset/metadata writes', name);
      return original.call(this, value, ...args);
    };
    try {
      await expect(store.saveDraft(draft('a', 'lost'), { assets: [asset('image', 'new')], attempt: attempt('new') })).rejects.toMatchObject({ code: name === 'QuotaExceededError' ? 'storage_quota' : 'storage_error' });
    } finally { IDBObjectStore.prototype.add = original; }
    expect((await store.getDraft('a')).title).toBe('Заметка 🦉');
    expect(await (await store.getAssets('a'))[0].blob.text()).toBe('abc');
    expect(await store.getAttempts('a')).toHaveLength(1);
    store.close();
  });

  it('cleans only explicitly deleted ownership rows, including orphans, never another draft sharing IDs', async () => {
    const { store, indexedDB } = setup();
    await store.saveDraft(draft(), { assets: [asset()], attempt: attempt() });
    await store.saveDraft(draft('b'), { assets: [asset()], attempt: attempt() });
    store.close();
    const db = await request(indexedDB.open('igonotes-webclipper', 1));
    await request(db.transaction('drafts', 'readwrite').objectStore('drafts').delete('a'));
    db.close();
    const reopened = createDraftStore({ indexedDB });
    expect(await reopened.getAssets('a')).toHaveLength(1);
    await reopened.saveDraft(draft('c'));
    expect(await reopened.getAttempts('a')).toHaveLength(1);
    await reopened.deleteDraft('a');
    expect(await reopened.getAssets('a')).toEqual([]);
    expect(await reopened.getAttempts('a')).toEqual([]);
    expect(await reopened.getAssets('b')).toHaveLength(1);
    expect(await reopened.getAttempts('b')).toHaveLength(1);
    reopened.close();
  });

  it('upgrades an existing database without wiping unrelated stores or records', async () => {
    const fresh = new IDBFactory();
    // Seed existing stores earlier in the same v1 upgrade event: IndexedDB has
    // no persisted version zero. Exercise the migration's preserve-existing path.
    const factory = { open(...args) {
      const req = fresh.open(...args);
      req.addEventListener('upgradeneeded', () => {
        req.result.createObjectStore('legacy').put('keep', 'key');
        req.result.createObjectStore('drafts', { keyPath: 'id' }).put(draft());
        req.result.createObjectStore('assets', { keyPath: ['draftId', 'id'] }).put({ ...asset(), draftId: 'a' });
      });
      return req;
    } };
    const store = createDraftStore({ indexedDB: factory });
    expect((await store.getDraft('a')).id).toBe('a');
    expect(await (await store.getAssets('a'))[0].blob.text()).toBe('abc');
    store.close();
    const check = await request(fresh.open('igonotes-webclipper'));
    expect(await request(check.transaction('legacy').objectStore('legacy').get('key'))).toBe('keep');
    check.close();
  });

  it.each(['QuotaExceededError', 'UnknownError'])('rolls back successful requests before an asynchronous %s request failure', async name => {
    const { store } = setup();
    await store.saveDraft(draft(), { assets: [asset()], attempt: attempt() });
    const original = IDBObjectStore.prototype.add;
    IDBObjectStore.prototype.add = function(value, ...args) {
      // Force an actual request failure late in the transaction using an existing
      // unique key; inject the browser/disk error at the request event boundary.
      const req = original.call(this, this.name === 'attempts' ? { ...value, id: 'try' } : value, ...args);
      if (this.name === 'attempts') req.addEventListener('error', () => {
        Object.defineProperty(req, 'error', { value: new DOMException('injected runtime storage failure', name) });
      });
      return req;
    };
    try {
      await expect(store.saveDraft(draft('a', 'lost'), { assets: [asset('image', 'changed')], attempt: attempt('new') })).rejects.toMatchObject({ code: name === 'QuotaExceededError' ? 'storage_quota' : 'storage_error' });
    } finally { IDBObjectStore.prototype.add = original; }
    expect((await store.getDraft('a')).title).toBe('Заметка 🦉');
    expect(await (await store.getAssets('a'))[0].blob.text()).toBe('abc');
    expect(await store.getAttempts('a')).toHaveLength(1);
    store.close();
  });

  it('allows updating at the production 50-draft cap and explicitly removing only named assets', async () => {
    const { store } = setup();
    for (let i = 0; i < 50; i++) await store.saveDraft(draft(String(i)));
    await store.saveDraft(draft('0'), { assets: [asset('one'), asset('two')] });
    await store.saveDraft(draft('0', 'updated'), { deleteAssetIds: ['one'] });
    expect((await store.getAssets('0')).map(a => a.id)).toEqual(['two']);
    await expect(store.saveDraft(draft('51'))).rejects.toMatchObject({ code: 'draft_limit' });
    store.close();
  });

  it('reports blocked open without deleting data and closes a late successful connection', async () => {
    const indexedDB = new IDBFactory();
    const blocker = await request(indexedDB.open('blocked', 1));
    const factory = { open: (name, version) => indexedDB.open(name, version + 1) };
    const store = createDraftStore({ indexedDB: factory, name: 'blocked' });
    await expect(store.listDrafts()).rejects.toMatchObject({ code: 'storage_blocked' });
    blocker.close();
    // This queued upgrade can finish only if the rejected store closes its late handle.
    const later = await request(indexedDB.open('blocked', 3));
    later.close();
    store.close();
  });

  it('requires asset descriptors to be JSON objects and rejects conflicting asset updates', async () => {
    const { store } = setup();
    await expect(store.saveDraft(draft(), { assets: [{ ...asset(), descriptor: 'not a descriptor' }] })).rejects.toMatchObject({ code: 'invalid_data' });
    await expect(store.saveDraft(draft(), { assets: [asset()], deleteAssetIds: ['image'] })).rejects.toMatchObject({ code: 'invalid_data' });
    store.close();
  });

  it('rejects invalid data, auth metadata and credential/query-bearing endpoints before writing', async () => {
    const { store } = setup();
    for (const bad of [{ ...draft(), token: 'secret' }, { ...draft(), title: '\ud800' }, { ...draft(), sourceUrl: 'https://user:secret@example.org' }]) {
      await expect(store.saveDraft(bad)).rejects.toMatchObject({ code: 'invalid_data' });
    }
    for (const bad of [{ ...attempt(), manifest: { nested: { authorization: 'Bearer secret' } } }, { ...attempt(), deliverySnapshot: { ...attempt().deliverySnapshot, endpoint: 'http://localhost:8080?token=secret' } }]) {
      await expect(store.saveDraft(draft(), { attempt: bad })).rejects.toMatchObject({ code: 'invalid_data' });
    }
    expect(await store.listDrafts()).toEqual([]);
    store.close();
  });

  it('reports unavailable, closed and versionchanged connections explicitly', async () => {
    await expect(createDraftStore({ indexedDB: null }).listDrafts()).rejects.toMatchObject({ code: 'storage_unavailable' });
    const { store, indexedDB } = setup();
    await store.saveDraft(draft());
    const upgrade = await request(indexedDB.open('igonotes-webclipper', 2));
    await expect(store.listDrafts()).rejects.toMatchObject({ code: 'storage_version_changed' });
    upgrade.close();
    store.close();
    await expect(store.listDrafts()).rejects.toMatchObject({ code: 'storage_closed' });
  });

  it('reports profile access denial as unavailable storage', async () => {
    const indexedDB = { open() { throw new DOMException('Profile storage denied', 'SecurityError'); } };
    await expect(createDraftStore({ indexedDB }).listDrafts()).rejects.toMatchObject({ code: 'storage_unavailable' });
  });

  it('reports explicit transaction cancellation and preserves prior work', async () => {
    const { store } = setup();
    await store.saveDraft(draft());
    const original = IDBObjectStore.prototype.put;
    IDBObjectStore.prototype.put = function(...args) {
      const req = original.apply(this, args);
      if (this.name === 'drafts') this.transaction.abort();
      return req;
    };
    try {
      await expect(store.saveDraft(draft('a', 'lost'))).rejects.toMatchObject({ code: 'storage_cancelled' });
    } finally { IDBObjectStore.prototype.put = original; }
    expect((await store.getDraft('a')).title).toBe('Заметка 🦉');
    store.close();
  });
});
