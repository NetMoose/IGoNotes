import { DraftStorageError } from '../storage/drafts.js';
import { normalizeImageSource, inspectImageBlob } from './acquire.js';
import { scanImageReferences } from './references.js';

const fields = ['size', 'sha256', 'mediaType', 'extension'];
const inputAsset = ({ id, normalizedUrl, descriptor, blob }) => ({ id, normalizedUrl, descriptor, blob });
const sourceKey = url => { try { return normalizeImageSource(url); } catch { return url; } };
const errorRecord = error => ({ code: error.code, message: error.message, ...(error.status === undefined ? {} : { status: error.status }) });

// Caller flushes unsaved editor text FIRST. This is an explicit preparation API,
// never a permission prompt or a capture/worker hook. Results/progress are durable
// checkpoints, not delivery snapshots: 8.3 must take/revalidate its own snapshot.
export async function prepareDraftImages(store, draftId, { acquirer, signal, progress } = {}) {
  let draft, assets = [];
  const attempted = new Set();
  let refs = [];
  function checkCancelled() {
    if (signal?.aborted) throw new DraftStorageError('storage_cancelled', 'Image preparation cancelled. Already stored images are retained.');
  }
  function checkLive() {
    if (!draft) throw new DraftStorageError('draft_deleted', 'The draft was deleted. Preparation stopped.');
    if (['sending', 'uncertain', 'pending'].includes(draft.status)) throw new DraftStorageError('draft_frozen', 'Resolve the live delivery attempt before preparing images.');
  }

  async function reconcile(results = []) {
    while (true) {
      checkCancelled();
      checkLive();
      refs = scanImageReferences(draft.markdown, draft.sourceUrl);
      const groups = new Map();
      for (const ref of refs) {
        const key = sourceKey(ref.url);
        if (!groups.has(key)) groups.set(key, new Set());
        groups.get(key).add(ref.url).add(ref.destination);
      }
      // Blob handles stay in IndexedDB/structured clones. Byte buffers are only
      // materialized one at a time here; there is no pixel decode or batch cache.
      const verified = new Map(), owners = new Map();
      for (const row of assets) {
        if (row.descriptor.status !== 'ready' || !row.blob) continue;
        try {
          const actual = await inspectImageBlob(row.blob);
          checkCancelled();
          if (row.size !== actual.size || !fields.every(key => row.descriptor[key] === actual[key])) continue;
          verified.set(row.id, actual);
          const contentKey = `${actual.sha256}:${actual.mediaType}:${actual.extension}`;
          if (!owners.has(contentKey)) owners.set(contentKey, row.id);
        } catch { checkCancelled(); /* Corrupt descriptors/bytes cannot be reused as ready. */ }
      }
      const rows = assets.map(row => {
        const descriptor = { ...row.descriptor, active: false, activeAliases: [] };
        const actual = verified.get(descriptor.canonicalAssetId ?? row.id);
        if (descriptor.status === 'ready' && actual && fields.every(field => descriptor[field] === actual[field])) {
          // Compact duplicate cached owners too, keeping direct (not chained)
          // aliases. Live delivery statuses were checked before touching bytes.
          descriptor.canonicalAssetId = owners.get(`${actual.sha256}:${actual.mediaType}:${actual.extension}`);
          return { ...inputAsset(row), blob: descriptor.canonicalAssetId === row.id ? row.blob : null, descriptor };
        }
        return { ...inputAsset(row), descriptor };
      });
      for (const [key, aliases] of groups) {
        let row = rows.find(row => sourceKey(row.normalizedUrl) === key);
        if (!row) {
          row = { id: crypto.randomUUID(), normalizedUrl: key, descriptor: {}, blob: null };
          rows.push(row);
        }
        const descriptor = row.descriptor;
        const ownerId = descriptor.canonicalAssetId ?? row.id;
        const valid = verified.get(ownerId);
        row.descriptor = { ...descriptor, active: true, aliases: [...new Set([...(descriptor.aliases ?? []), row.normalizedUrl, ...aliases])], activeAliases: [...aliases] };
        if (valid && fields.every(field => descriptor[field] === valid[field])) {
          row.descriptor.status = 'ready';
          delete row.descriptor.error;
        } else if (!attempted.has(key)) {
          row.descriptor.status = 'pending';
          delete row.descriptor.error;
          delete row.descriptor.canonicalAssetId;
        }
        try { normalizeImageSource(key); }
        catch (error) {
          row.descriptor = { ...row.descriptor, status: 'error', error: errorRecord(error) };
          attempted.add(key);
        }
        const result = results.find(result => sourceKey(result.url) === key);
        if (!result) continue;
        if (!result.ok) {
          row.descriptor = { ...row.descriptor, status: 'error', error: errorRecord(result.error) };
          continue;
        }
        let actual;
        try {
          actual = await inspectImageBlob(result.blob);
          checkCancelled();
          if (!fields.every(field => result[field] === actual[field])) throw new Error('descriptor mismatch');
        } catch {
          checkCancelled();
          row.descriptor = { ...row.descriptor, status: 'error', error: { code: 'invalid_bytes', message: 'Image bytes do not match their verified descriptor. Retry preparation.' } };
          continue;
        }
        const contentKey = `${actual.sha256}:${actual.mediaType}:${actual.extension}`;
        const canonicalAssetId = owners.get(contentKey) ?? row.id;
        row.blob = canonicalAssetId === row.id ? result.blob : null;
        row.descriptor = { ...row.descriptor, ...actual, status: 'ready', canonicalAssetId };
        delete row.descriptor.error;
        owners.set(contentKey, canonicalAssetId);
        verified.set(canonicalAssetId, actual);
      }
      try {
        const updates = rows.filter(row => {
          const previous = assets.find(previous => previous.id === row.id);
          return !previous || previous.blob !== row.blob || previous.normalizedUrl !== row.normalizedUrl || JSON.stringify(previous.descriptor) !== JSON.stringify(row.descriptor);
        });
        checkCancelled();
        const saved = await store.updateDraftAssets(draftId, { expectedMarkdown: draft.markdown, expectedAssets: assets, assets: updates, signal });
        draft = saved.draft; assets = saved.assets;
        checkCancelled();
        return;
      } catch (error) {
        checkCancelled();
        if (error.code !== 'markdown_changed') throw error;
        // Reparse outside the transaction. Keep the old expected asset state so
        // a simultaneous manual delete/other preparer cannot resurrect bytes.
        draft = await store.getDraft(draftId);
        checkCancelled();
      }
    }
  }
  function current(persistenceError) {
    const active = assets.filter(row => row.descriptor.active);
    const ready = active.every(row => row.descriptor.status === 'ready');
    const ownerIds = new Set(active.filter(row => row.descriptor.status === 'ready').map(row => row.descriptor.canonicalAssetId ?? row.id));
    return { ok: !persistenceError && ready, markdown: draft?.markdown, refs, assets: active,
      blobs: assets.filter(row => ownerIds.has(row.id) && row.blob),
      ...(persistenceError ? { persistenceError: errorRecord(persistenceError) } : {}) };
  }
  function notify() {
    checkCancelled();
    const active = assets.filter(row => row.descriptor.active);
    progress?.({ status: active.every(row => row.descriptor.status === 'ready') ? 'ready' : active.some(row => row.descriptor.status === 'error') ? 'error' : 'pending',
      total: active.length, ready: active.filter(row => row.descriptor.status === 'ready').length });
  }
  try {
    checkCancelled();
    draft = await store.getDraft(draftId);
    checkCancelled();
    assets = await store.getAssets(draftId);
    checkCancelled();
    await reconcile();
    notify();
    while (true) {
      checkCancelled();
      const batch = assets.filter(row => row.descriptor.active && row.descriptor.status !== 'ready' && !attempted.has(sourceKey(row.normalizedUrl))).slice(0, 3);
      if (!batch.length) return current();
      // Each completion is durable independently of a stalled sibling. Keep
      // acquisition concurrency three, but serialize reconciliation/CAS so our
      // own checkpoints cannot race each other or resurrect an older asset row.
      let checkpoint = Promise.resolve();
      const completed = new Set();
      const onResult = result => {
        checkpoint = checkpoint.then(async () => {
          if (signal?.aborted) return;
          if (completed.has(result.url)) return;
          attempted.add(sourceKey(result.url));
          await reconcile([result]);
          completed.add(result.url);
          notify();
        });
        return checkpoint;
      };
      const results = await acquirer.acquire(batch.map(row => row.normalizedUrl), { signal, onResult });
      checkCancelled();
      // Compatibility with injected acquirers and immediate invalid-source rows.
      for (const result of results) await onResult(result);
    }
  } catch (error) {
    if (signal?.aborted || error.code === 'storage_cancelled') return { ...current(), ok: false, cancelled: true };
    if (['draft_deleted', 'draft_frozen', 'assets_changed'].includes(error.code)) throw error;
    if (!(error instanceof DraftStorageError)) throw error;
    // Never claim a failed ready write succeeded. Try a separate tiny error
    // checkpoint; if even that fails, report the persistence error independently.
    const persistenceError = error;
    let errorStatusPersistenceError;
    try {
      const failed = assets.filter(row => row.descriptor.active && row.descriptor.status !== 'ready').map(row => ({ ...inputAsset(row), descriptor: { ...row.descriptor, status: 'error', error: { code: 'persistence', message: 'Image preparation could not be saved. Free storage and retry.' } } }));
      checkCancelled();
      const saved = await store.updateDraftAssets(draftId, { expectedMarkdown: draft.markdown, expectedAssets: assets, assets: failed, signal });
      draft = saved.draft; assets = saved.assets;
      checkCancelled();
    } catch (secondary) {
      if (signal?.aborted || secondary.code === 'storage_cancelled') return { ...current(), ok: false, cancelled: true };
      if (['draft_deleted', 'draft_frozen', 'assets_changed'].includes(secondary.code)) throw secondary;
      errorStatusPersistenceError = errorRecord(secondary);
    }
    return { ...current(persistenceError), ...(errorStatusPersistenceError ? { errorStatusPersistenceError } : {}) };
  }
}
