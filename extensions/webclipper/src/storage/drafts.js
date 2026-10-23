// Native IndexedDB only: usable from the extension worker and editor alike.
export const MAX_DRAFTS = 50;
export const MAX_BLOB_BYTES = 250 * 1024 ** 2;
const STORES = ['drafts', 'assets', 'attempts'];
// Enough for a 10 MiB image even when every byte is percent-encoded, plus
// its header. This is a metadata bound, separate from actual Blob usage.
const MAX_ASSET_SOURCE_LENGTH = 32 * 1024 ** 2;
const attemptID = value => typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value);

export class DraftStorageError extends Error {
  constructor(code, message, cause) {
    super(message, { cause });
    this.name = 'DraftStorageError';
    this.code = code;
  }
}
const fail = (message) => { throw new DraftStorageError('invalid_data', message); };
function classify(error) {
  if (error instanceof DraftStorageError) return error;
  if (error?.name === 'SecurityError') return new DraftStorageError('storage_unavailable', 'This profile denied IndexedDB access. Allow extension storage or reopen in a supported local profile.', error);
  if (error?.name === 'QuotaExceededError') return new DraftStorageError('storage_quota', 'Browser storage is full. Keep your unsaved work and manually delete unwanted drafts or free profile storage, then retry.', error);
  if (error?.name === 'ConstraintError') return new DraftStorageError('storage_conflict', 'This attempt already exists. Keep its immutable snapshot and use a new ID for a new attempt.', error);
  if (error?.name === 'AbortError') return new DraftStorageError('storage_cancelled', 'Draft transaction was cancelled; previous saved work was preserved. Retry saving.', error);
  return new DraftStorageError('storage_error', 'Draft storage failed; previous saved work was preserved. Retry or reopen the editor.', error);
}
function text(value, name, nonempty = false) {
  if (typeof value !== 'string' || !value.isWellFormed() || value.includes('\0') || (nonempty && !value.trim())) fail(`Invalid ${name}.`);
  return value;
}
function shape(value, allowed, name) {
  if (!value || Object.getPrototypeOf(value) !== Object.prototype || Object.keys(value).some(key => !allowed.includes(key))) fail(`Invalid ${name} fields (credentials/settings do not belong in drafts).`);
}
function timestamp(value, name) {
  text(value, name, true);
  if (!Number.isFinite(Date.parse(value))) fail(`Invalid ${name}.`);
  return value;
}
function url(value, endpoint = false) {
  text(value, 'URL', true);
  let parsed;
  try { parsed = new URL(value); } catch { fail('Invalid absolute URL.'); }
  if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password) fail('URL must be HTTP(S) without credentials.');
  if (endpoint && (parsed.search || parsed.hash || parsed.pathname !== '/')) fail('Attempt endpoint must be an origin without query, fragment or path.');
  return parsed.href;
}
function assetSource(value) {
  text(value, 'asset source', true);
  if (value.length > MAX_ASSET_SOURCE_LENGTH) fail('Asset source exceeds the 32 MiB-character metadata limit. Keep the original work and use a smaller source.');
  if (!/^data:/i.test(value)) return url(value);
  const header = /^data:image\/(png|jpeg|gif|webp)(;base64)?,/i.exec(value);
  if (!header) fail('Data image source must declare PNG, JPEG, GIF or WebP without extra parameters.');
  const payload = value.slice(header[0].length);
  if (!payload) fail('Data image source must have a nonempty payload.');
  if (header[2]) {
    // Standard alphabet, with conventional padding or unpadded final groups.
    // This validates representation only; acquisition owns decoding/signatures.
    const padded = payload.endsWith('=');
    if (!/^[A-Za-z0-9+/]*={0,2}$/.test(payload) || (padded ? payload.length % 4 !== 0 : payload.length % 4 === 1)) fail('Invalid base64 data image source.');
  } else if (/[^A-Za-z0-9!$&'()*+,\-./:;=?@_~%]/.test(payload) || /%(?![A-Fa-f0-9]{2})/.test(payload)) {
    fail('Data image source must contain URL-safe ASCII or complete percent-encoded bytes.');
  }
  // Do not parse/re-encode data URLs: exact spelling is the future lookup key.
  return value;
}
// Snapshots are JSON, never arbitrary structured-clone objects or auth settings.
// Content strings may of course contain words such as "token".
function json(value, depth = 0) {
  if (depth > 64) fail('Snapshot is too deeply nested.');
  if (value === null || typeof value === 'boolean') return value;
  if (typeof value === 'string') return text(value, 'snapshot string');
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (Array.isArray(value)) return value.map(item => json(item, depth + 1));
  shape(value, Object.keys(value ?? {}), 'snapshot');
  const result = {};
  for (const [key, entry] of Object.entries(value)) {
    text(key, 'snapshot key', true);
    if (/token|secret|password|authorization|credential|cookie|headers|endpoint/i.test(key) || ['__proto__', 'constructor', 'prototype'].includes(key)) fail('Auth/settings fields are forbidden in snapshot JSON.');
    result[key] = json(entry, depth + 1);
  }
  return result;
}
function prepareDraft(input) {
  shape(input, ['id', 'title', 'filename', 'markdown', 'sourceUrl', 'capturedAt', 'base', 'parent', 'destinationRevision', 'status', 'createdAt', 'updatedAt', 'activeAttemptId'], 'draft');
  const result = {};
  for (const name of ['id', 'title', 'filename', 'markdown', 'parent', 'status']) result[name] = text(input[name], name, ['id', 'filename', 'status'].includes(name));
  if (!['draft', 'sending', 'uncertain', 'pending'].includes(result.status)) fail('Invalid draft status.');
  result.sourceUrl = url(input.sourceUrl);
  result.capturedAt = timestamp(input.capturedAt, 'capturedAt');
  for (const name of ['base', 'destinationRevision']) result[name] = input[name] === null ? null : text(input[name], name);
  for (const name of ['createdAt', 'updatedAt']) if (input[name] !== undefined) timestamp(input[name], name);
  if (input.activeAttemptId !== undefined) {
    if (input.activeAttemptId !== null && !attemptID(input.activeAttemptId)) fail('Invalid active attempt ID.');
    result.activeAttemptId = input.activeAttemptId;
  }
  return result;
}
export const isFrozen = draft => ['sending', 'uncertain', 'pending'].includes(draft?.status);
function validateLifecycleRevision(value) {
  if (value !== null && (!Number.isSafeInteger(value) || value < 0)) fail('Invalid lifecycle revision.');
}
// Explicit compatibility token: null means the field is ABSENT in a legacy v1
// lifecycle row, not a stored null/invalid number. Reads never rewrite history.
export function attemptLifecycleRevision(attempt) {
  if (!['sending', 'uncertain', 'pending', 'rejected', 'succeeded'].includes(attempt?.state) || !['prepared', 'posting'].includes(attempt?.phase)) fail('Invalid persisted delivery lifecycle.');
  if (!Object.hasOwn(attempt, 'lifecycleRevision')) return null;
  if (attempt.lifecycleRevision === null) fail('Invalid persisted lifecycle revision.');
  validateLifecycleRevision(attempt.lifecycleRevision);
  return attempt.lifecycleRevision;
}
function checkIdentityHold(attempt) {
  if (attempt.code === 'clipper_request_changed') throw new DraftStorageError('identity_conflict', 'This import has a persisted identity conflict. Keep its immutable draft and inspect the original import in IGoNotes.');
}
function editable(draft) {
  if (!draft) throw new DraftStorageError('draft_deleted', 'The draft was deleted. Reload the draft list.');
  if (isFrozen(draft)) throw new DraftStorageError('draft_frozen', 'Resolve the live delivery attempt before changing or deleting this draft.');
}
const nextUpdatedAt = previous => new Date(Math.max(Date.now(), Date.parse(previous?.updatedAt ?? '') + 1 || 0)).toISOString();
function preparationState(draft, assets) {
  return { draft, assets: assets.map(a => ({ id: a.id, normalizedUrl: a.normalizedUrl, size: a.size, descriptor: a.descriptor, blobSize: a.blob?.size ?? null })).sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0) };
}
function prepareAsset(input, draftId) {
  shape(input, ['id', 'normalizedUrl', 'descriptor', 'blob', 'size'], 'asset');
  const descriptor = json(input.descriptor);
  if (!descriptor || Array.isArray(descriptor) || typeof descriptor !== 'object') fail('Asset descriptor must be a JSON object.');
  // Explicit failed edited sources must remain inspectable, even when their URL
  // scheme is not fetchable. Pending/ready sources keep the strict source schema.
  const failedSource = descriptor.status === 'error' && typeof descriptor.error?.code === 'string';
  const normalizedUrl = failedSource ? text(input.normalizedUrl, 'failed asset source', true) : assetSource(input.normalizedUrl);
  if (normalizedUrl.length > MAX_ASSET_SOURCE_LENGTH) fail('Asset source exceeds metadata limit.');
  const result = { draftId, id: text(input.id, 'asset id', true), normalizedUrl, descriptor };
  if (input.blob !== null && !(input.blob instanceof Blob)) fail('Asset blob must be a Blob or null.');
  result.blob = input.blob;
  result.size = input.blob?.size ?? 0;
  return result;
}

// This fingerprint excludes Blob identity (structured clones have new handles).
// Ready descriptors include hash/type/size; changes invalidate an in-flight CAS.
function assetState(assets) {
  return JSON.stringify(assets.map(a => [a.id, a.normalizedUrl, a.descriptor, a.size, a.blob?.size ?? null]).sort((a, b) => a[0].localeCompare(b[0])));
}
function prepareAttempt(input, draftId) {
  shape(input, ['id', 'createdAt', 'manifest', 'deliverySnapshot'], 'attempt');
  shape(input.deliverySnapshot, ['endpoint', 'assetIds', 'digest'], 'deliverySnapshot');
  const { endpoint, assetIds, digest } = input.deliverySnapshot;
  if (!Array.isArray(assetIds) || new Set(assetIds).size !== assetIds.length) fail('Attempt assetIds must be unique.');
  const manifest = json(input.manifest);
  if (!manifest || Array.isArray(manifest) || typeof manifest !== 'object') fail('Manifest must be a JSON object.');
  return { draftId, id: text(input.id, 'attempt id', true), createdAt: timestamp(input.createdAt, 'attempt createdAt'), manifest, deliverySnapshot: { endpoint: url(endpoint, true), assetIds: assetIds.map(id => text(id, 'asset id', true)), digest: text(digest, 'digest', true) } };
}

export function createDraftStore({ indexedDB = globalThis.indexedDB, name = 'igonotes-webclipper', limits = {} } = {}) {
  const maxDrafts = limits.maxDrafts ?? MAX_DRAFTS;
  const maxBlobBytes = limits.maxBlobBytes ?? MAX_BLOB_BYTES;
  if (![maxDrafts, maxBlobBytes].every(n => Number.isSafeInteger(n) && n >= 0)) fail('Invalid storage limits.');
  let opening, connection, stopped;
  function open() {
    if (stopped) return Promise.reject(stopped);
    if (!indexedDB) return Promise.reject(new DraftStorageError('storage_unavailable', 'IndexedDB is unavailable in this context. Open the extension in a supported local profile.'));
    if (!opening) opening = new Promise((resolve, reject) => {
      let request;
      try { request = indexedDB.open(name, 1); } catch (error) { reject(classify(error)); return; }
      request.onupgradeneeded = () => {
        const db = request.result;
        if (!db.objectStoreNames.contains('drafts')) db.createObjectStore('drafts', { keyPath: 'id' });
        for (const storeName of ['assets', 'attempts']) {
          const store = db.objectStoreNames.contains(storeName) ? request.transaction.objectStore(storeName) : db.createObjectStore(storeName, { keyPath: ['draftId', 'id'] });
          if (!store.indexNames.contains('draftId')) store.createIndex('draftId', 'draftId');
        }
      };
      request.onblocked = () => {
        stopped = new DraftStorageError('storage_blocked', 'Draft database upgrade is blocked. Close other extension tabs and retry with a new store instance.');
        reject(stopped);
      };
      request.onerror = () => reject(classify(request.error));
      request.onsuccess = () => {
        const db = request.result;
        if (stopped) { db.close(); reject(stopped); return; }
        connection = db;
        db.onversionchange = () => {
          stopped = new DraftStorageError('storage_version_changed', 'Draft database version changed. Reopen the editor before continuing.');
          db.close();
        };
        db.onclose = () => { stopped ??= new DraftStorageError('storage_closed', 'Draft database connection closed. Reopen the editor.'); };
        resolve(db);
      };
    });
    return opening;
  }
  async function transaction(mode, work) {
    const db = await open();
    if (stopped) throw stopped;
    return new Promise((resolve, reject) => {
      let tx, result, failure;
      try { tx = db.transaction(STORES, mode); } catch (error) { reject(classify(error)); return; }
      const abort = error => {
        failure ??= error;
        try { tx.abort(); } catch { /* already aborted */ }
      };
      tx.oncomplete = () => resolve(result);
      tx.onabort = () => reject(classify(failure ?? tx.error ?? new DOMException('Cancelled', 'AbortError')));
      tx.onerror = event => { failure ??= event.target.error; };
      // Only IDB requests and synchronous callbacks inside this critical section.
      const read = (req, next) => {
        req.onsuccess = () => { try { next(req.result); } catch (error) { abort(error); } };
        return req;
      };
      try { work(tx, read, value => { result = value; }); } catch (error) { abort(error); }
    });
  }
  return {
    async listDrafts() { return transaction('readonly', (tx, read, done) => read(tx.objectStore('drafts').getAll(), done)); },
    async getDraft(id) { text(id, 'draft id', true); return transaction('readonly', (tx, read, done) => read(tx.objectStore('drafts').get(id), done)); },
    async getAssets(id) { text(id, 'draft id', true); return transaction('readonly', (tx, read, done) => read(tx.objectStore('assets').index('draftId').getAll(id), done)); },
    async getAttempts(id) { text(id, 'draft id', true); return transaction('readonly', (tx, read, done) => read(tx.objectStore('attempts').index('draftId').getAll(id), done)); },
    async readBundle(id) {
      text(id, 'draft id', true);
      // One all-store RO point, serialized against saveDraft/updateDraftAssets.
      // No hashing/await in the transaction; returned Blobs are immutable handles.
      return transaction('readonly', (tx, read, done) => {
        const bundle = {};
        read(tx.objectStore('drafts').get(id), value => { bundle.draft = value; });
        read(tx.objectStore('assets').index('draftId').getAll(id), value => { bundle.assets = value; });
        read(tx.objectStore('attempts').index('draftId').getAll(id), value => { bundle.attempts = value; });
        done(bundle);
      });
    },
    async updateDraftAssets(id, { expectedMarkdown, expectedAssets, assets = [] }) {
      text(id, 'draft id', true);
      text(expectedMarkdown, 'expected Markdown');
      if (!Array.isArray(expectedAssets) || !Array.isArray(assets)) fail('Expected assets and updates must be arrays.');
      const expected = assetState(expectedAssets);
      const updates = assets.map(value => prepareAsset(value, id));
      if (new Set(updates.map(a => a.id)).size !== updates.length) fail('Conflicting asset updates.');
      return transaction('readwrite', (tx, read, done) => {
        const assetStore = tx.objectStore('assets');
        read(tx.objectStore('drafts').get(id), live => {
          if (!live) throw new DraftStorageError('draft_deleted', 'The draft was deleted. Preparation stopped.');
           editable(live);
          if (live.markdown !== expectedMarkdown) throw new DraftStorageError('markdown_changed', 'Markdown changed. Recompute image references.');
          read(assetStore.getAll(), all => {
            const owned = all.filter(a => a.draftId === id);
            if (assetState(owned) !== expected) throw new DraftStorageError('assets_changed', 'Stored images changed. Restart preparation explicitly.');
            const changed = new Set(updates.map(a => a.id));
            const bytes = all.reduce((sum, a) => sum + (a.draftId === id && changed.has(a.id) ? 0 : a.blob?.size ?? 0), 0) + updates.reduce((sum, a) => sum + a.size, 0);
            if (bytes > maxBlobBytes) throw new DraftStorageError('blob_limit', `Draft images exceed the ${maxBlobBytes}-byte application limit. Manually remove unwanted images or drafts and retry.`);
            for (const value of updates) assetStore.put(value);
            // Never replace draft metadata or append/edit attempts here.
            done({ draft: live, assets: [...owned.filter(a => !changed.has(a.id)), ...updates] });
          });
        });
      });
    },
    async saveDraft(input, { assets = [], deleteAssetIds = [], attempt, requireExisting = false, expectedUpdatedAt } = {}) {
      const draft = prepareDraft(input);
      if (!Array.isArray(assets) || !Array.isArray(deleteAssetIds)) fail('Asset updates must be arrays.');
      const updates = assets.map(value => prepareAsset(value, draft.id));
      const removals = deleteAssetIds.map(id => text(id, 'asset id', true));
      if (new Set([...updates.map(a => a.id), ...removals]).size !== updates.length + removals.length) fail('Conflicting asset updates.');
      const snapshot = attempt === undefined ? undefined : prepareAttempt(attempt, draft.id);
      return transaction('readwrite', (tx, read, done) => {
        const drafts = tx.objectStore('drafts'), assetStore = tx.objectStore('assets');
        // All global count/usage reads and mutations share the same all-store RW
        // transaction, so distinct tabs/handles serialize in IndexedDB itself.
         read(drafts.get(draft.id), previous => read(drafts.count(), count => read(assetStore.getAll(), allAssets => {
           if (previous) editable(previous);
           else if (requireExisting) editable(previous);
           if (expectedUpdatedAt !== undefined && previous?.updatedAt !== expectedUpdatedAt) throw new DraftStorageError('draft_changed', 'Another editor changed this draft. Reload before editing.');
           if (isFrozen(draft) || draft.activeAttemptId) fail('Only beginAttempt may freeze a draft.');
          if (!previous && count >= maxDrafts) throw new DraftStorageError('draft_limit', `Maximum ${maxDrafts} drafts reached. Manually delete an unwanted draft and retry.`);
          const changed = new Set([...updates.map(a => a.id), ...removals]);
          let bytes = allAssets.reduce((sum, a) => sum + (a.draftId === draft.id && changed.has(a.id) ? 0 : a.blob?.size ?? 0), 0);
          bytes += updates.reduce((sum, a) => sum + a.size, 0);
          if (bytes > maxBlobBytes) throw new DraftStorageError('blob_limit', `Draft images exceed the ${maxBlobBytes}-byte application limit. Manually remove unwanted images or drafts and retry.`);
           const now = nextUpdatedAt(previous);
          const saved = { ...draft, createdAt: previous?.createdAt ?? now, updatedAt: now };
          for (const id of removals) assetStore.delete([draft.id, id]);
          for (const value of updates) assetStore.put(value);
          drafts.put(saved);
          if (snapshot) tx.objectStore('attempts').add(snapshot);
          done(saved);
        })));
      });
    },
    async deleteDraft(id) {
      text(id, 'draft id', true);
      return transaction('readwrite', (tx, read) => read(tx.objectStore('drafts').get(id), live => {
        if (live) editable(live);
        tx.objectStore('drafts').delete(id);
        for (const name of ['assets', 'attempts']) {
          const store = tx.objectStore(name);
          read(store.index('draftId').getAllKeys(id), keys => {
            for (const key of keys) store.delete(key);
          });
        }
      }));
    },
    async updateMetadata(id, input) {
      text(id, 'draft id', true);
      shape(input, ['base', 'parent', 'destinationRevision'], 'destination');
      const fields = { base: text(input.base, 'base', true), parent: text(input.parent, 'parent'), destinationRevision: text(input.destinationRevision, 'destination revision', true) };
      return transaction('readwrite', (tx, read, done) => read(tx.objectStore('drafts').get(id), live => {
        editable(live);
        const saved = { ...live, ...fields, updatedAt: nextUpdatedAt(live) };
        tx.objectStore('drafts').put(saved); done(saved);
      }));
    },
    async beginAttempt(id, snapshot) {
      text(id, 'draft id', true);
      // Detach before opening the transaction. No hashing or asynchronous work in RW.
      const expected = structuredClone(snapshot.preparationState);
      const attempt = prepareAttempt({ id: snapshot.importId, createdAt: new Date().toISOString(), manifest: snapshot.manifest,
        deliverySnapshot: { endpoint: snapshot.endpoint, assetIds: snapshot.manifest.assets.map(a => a.id), digest: snapshot.digest } }, id);
      if (!attemptID(attempt.id) || attempt.manifest.import_id !== attempt.id || !/^[0-9a-f]{64}$/.test(attempt.deliverySnapshot.digest)) fail('Invalid immutable attempt identity/digest.');
      attempt.state = 'sending'; attempt.phase = 'prepared'; attempt.lifecycleRevision = 0;
      return transaction('readwrite', (tx, read, done) => read(tx.objectStore('drafts').get(id), live => {
        editable(live);
        read(tx.objectStore('assets').index('draftId').getAll(id), assets => {
          if (JSON.stringify(preparationState(live, assets)) !== JSON.stringify(expected)) throw new DraftStorageError('preparation_changed', 'Draft or images changed. Prepare and confirm again.');
          tx.objectStore('attempts').add(attempt);
          tx.objectStore('drafts').put({ ...live, status: 'sending', activeAttemptId: attempt.id });
          done(attempt);
        });
      }));
    },
    async transitionOutcome(id, attemptId, input, expected) {
      text(id, 'draft id', true); text(attemptId, 'attempt id', true);
      shape(input, ['state', 'phase', 'code', 'result'], 'outcome');
      shape(expected, ['state', 'phase', 'lifecycleRevision'], 'expected lifecycle');
      if (!['sending', 'uncertain', 'pending'].includes(expected.state) || !['prepared', 'posting'].includes(expected.phase)) fail('Expected lifecycle is required.');
      validateLifecycleRevision(expected.lifecycleRevision);
      const compare = { state: expected.state, phase: expected.phase, lifecycleRevision: expected.lifecycleRevision };
      const outcome = json(input);
      if (!['sending', 'uncertain', 'pending', 'rejected', 'succeeded'].includes(outcome.state) || !['prepared', 'posting'].includes(outcome.phase)) fail('Invalid delivery lifecycle.');
      if (outcome.code !== undefined) text(outcome.code, 'outcome code', true);
      if (outcome.state === 'succeeded' && !outcome.result) fail('Missing terminal result.');
      if (outcome.result !== undefined) {
        shape(outcome.result, ['import_id', 'base', 'path', 'revision', 'warnings'], 'receipt');
        const r = outcome.result;
        if (outcome.state !== 'succeeded' || r.import_id !== attemptId || !/^sha256:[0-9a-f]{64}$/.test(r.revision) || !Array.isArray(r.warnings) || !r.warnings.every(s => typeof s === 'string')) fail('Invalid terminal receipt.');
        text(r.base, 'receipt base', true); text(r.path, 'receipt path', true);
      }
      return transaction('readwrite', (tx, read, done) => read(tx.objectStore('drafts').get(id), live => {
        read(tx.objectStore('attempts').get([id, attemptId]), attempt => {
          if (!live || live.activeAttemptId !== attemptId || !isFrozen(live) || !attempt) throw new DraftStorageError('attempt_changed', 'Delivery ownership changed. Reload before checking again.');
          const revision = attemptLifecycleRevision(attempt);
          // Authority is the CURRENT row inside this RW transaction. Even a
          // caller with the latest revision cannot clear a sticky identity hold,
          // convert it to success, unlock the draft, or begin another POST.
          checkIdentityHold(attempt);
          if (['rejected', 'succeeded'].includes(attempt.state) ||
             attempt.state !== compare.state || attempt.phase !== compare.phase || revision !== compare.lifecycleRevision ||
             (attempt.phase === 'posting' && outcome.phase !== 'posting') ||
             (outcome.state === 'rejected' && attempt.phase === 'prepared' && outcome.code !== 'prepare_again')) throw new DraftStorageError('attempt_changed', 'Delivery state changed. Reload before checking again.');
          if (revision === Number.MAX_SAFE_INTEGER) throw new DraftStorageError('lifecycle_exhausted', 'Delivery lifecycle revision is exhausted. Keep this frozen attempt for inspection.');
          // Increment for EVERY transition, including identical state/phase/code.
          // Legacy absence upgrades atomically from its explicit null CAS to 1.
          const saved = { ...attempt, ...outcome, lifecycleRevision: (revision ?? 0) + 1 };
          tx.objectStore('attempts').put(saved);
          tx.objectStore('drafts').put({ ...live, status: outcome.state === 'rejected' ? 'draft' : outcome.state === 'succeeded' ? 'uncertain' : outcome.state,
            activeAttemptId: outcome.state === 'rejected' ? null : attemptId,
            destinationRevision: outcome.state === 'rejected' ? null : live.destinationRevision });
          done(saved);
        });
      }));
    },
    async finishSuccess(id, attemptId, expectedRevision) {
      text(id, 'draft id', true); text(attemptId, 'attempt id', true);
      validateLifecycleRevision(expectedRevision);
      return transaction('readwrite', (tx, read) => read(tx.objectStore('drafts').get(id), live => {
        read(tx.objectStore('attempts').get([id, attemptId]), attempt => {
          if (!live || live.activeAttemptId !== attemptId || !isFrozen(live) || !attempt) throw new DraftStorageError('attempt_changed', 'Confirmed ownership is required before cleanup.');
          const revision = attemptLifecycleRevision(attempt);
          checkIdentityHold(attempt);
          if (attempt.state !== 'succeeded' || !attempt.result || revision !== expectedRevision) throw new DraftStorageError('attempt_changed', 'Confirmed result and its exact lifecycle revision are required before cleanup.');
          tx.objectStore('drafts').delete(id);
          for (const name of ['assets', 'attempts']) {
            const rows = tx.objectStore(name);
            read(rows.index('draftId').getAllKeys(id), keys => { for (const key of keys) rows.delete(key); });
          }
        });
      }));
    },
    close() {
      stopped = new DraftStorageError('storage_closed', 'Draft store is closed. Create a new store instance to reopen it.');
      connection?.close();
    },
  };
}
