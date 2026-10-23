import { createConnectionStore, validateEndpoint } from '../connection/settings.js';
import { createTransport } from '../connection/transport.js';
import { buildImportSnapshot, manifestDigest } from '../images/rewrite.js';
import { inspectImageBlob } from '../images/acquire.js';
import { isFrozen, attemptLifecycleRevision } from '../storage/drafts.js';

const messages = {
  confirm_destination: 'Load destinations, choose an existing base and folder, then explicitly confirm the destination again.',
  restore_endpoint: 'This attempt belongs to a different endpoint. Restore its original connection address before checking or replaying it.',
  no_active_attempt: 'There is no unresolved attempt. Confirm the destination before a new Send.',
  identity_conflict: 'The server reported an import identity conflict. Keep this draft frozen and inspect the original import in IGoNotes; do not reissue it with a new ID.',
  invalid_receipt: 'The receipt did not match the saved import. Keep the draft and check the same attempt again.',
  cleanup_pending: 'IGoNotes confirmed success, but local cleanup failed. Check this attempt again to finish cleanup without sending a new import.',
  save_failed: 'Save the current edits successfully before delivery.',
  invalid_snapshot: 'Stored image bytes or snapshot no longer match. Keep the draft and prepare images again.',
  delivery_busy: 'Another delivery action is still running.',
};
export class DeliveryError extends Error {
  constructor(code) { super(messages[code]); this.name = 'DeliveryError'; this.code = code; }
}
const fail = code => { throw new DeliveryError(code); };
const sha256 = async text => [...new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text)))].map(n => n.toString(16).padStart(2, '0')).join('');
const destination = draft => JSON.stringify([draft.base, draft.parent, draft.destinationRevision]);

async function validReceipt(attempt, result) {
  const m = attempt.manifest;
  return !!result && result.import_id === attempt.id && result.base === m.base &&
    result.path === [m.parent, m.filename].filter(Boolean).join('/') &&
    result.revision === `sha256:${await sha256(m.markdown)}` &&
    Array.isArray(result.warnings) && result.warnings.every(w => typeof w === 'string') &&
    JSON.stringify(result.warnings) === JSON.stringify(m.external_images.map(e => e.reason));
}

// Re-read only persisted frozen data. A metadata CAS cannot prove Blob contents:
// hashing happens AFTER freezing, outside any IndexedDB transaction, on each POST.
async function persistedBody(store, draftId, attempt) {
  const bundle = await store.readBundle(draftId);
  const live = bundle.attempts.find(a => a.id === attempt.id);
  if (!isFrozen(bundle.draft) || bundle.draft.activeAttemptId !== attempt.id || !live ||
    live.deliverySnapshot.digest !== attempt.deliverySnapshot.digest || await manifestDigest(live.manifest) !== live.deliverySnapshot.digest ||
    JSON.stringify(live.deliverySnapshot.assetIds) !== JSON.stringify(live.manifest.assets.map(a => a.id))) fail('invalid_snapshot');
  const body = new FormData();
  body.append('manifest', new Blob([JSON.stringify(live.manifest)], { type: 'application/json' }), 'manifest.json');
  for (const asset of live.manifest.assets) {
    const row = bundle.assets.find(row => row.id === asset.id);
    if (!row?.blob) fail('invalid_snapshot');
    let actual;
    try { actual = await inspectImageBlob(row.blob); } catch { fail('invalid_snapshot'); }
    if (actual.sha256 !== asset.sha256 || actual.mediaType !== asset.media_type || actual.size !== asset.size) fail('invalid_snapshot');
    // Blob.type may be empty in legacy cache rows. Use the verified signature MIME.
    body.append(asset.id, row.blob.slice(0, row.blob.size, actual.mediaType), `${asset.id}.${actual.extension}`);
  }
  return body;
}

export function createDelivery({ store, connectionStore = createConnectionStore(), transportFactory = createTransport,
  flush = async () => true, uuid = () => crypto.randomUUID(), onFrozen }) {
  let choices, choicesEndpoint, busy = false;
  const folders = new Map(), confirmations = new Map();
  async function exclusive(action) {
    if (busy) fail('delivery_busy');
    busy = true;
    try { return await action(); } finally { busy = false; }
  }
  const conflicts = ['attempt_changed', 'identity_conflict', 'lifecycle_exhausted', 'draft_frozen'];
  async function authoritativeHold(draftId, code) {
    const bundle = await store.readBundle(draftId);
    const attempt = bundle.attempts.find(a => a.id === bundle.draft?.activeAttemptId);
    if (isFrozen(bundle.draft)) onFrozen?.(bundle.draft);
    // Never return succeeded here: a stale success has NOT persisted/cleaned up
    // on this controller's authority. Report the observed state as read-only.
    return { state: attempt?.state === 'pending' ? 'pending' : 'uncertain', readOnly: true,
      code: attempt?.code === 'clipper_request_changed' ? attempt.code : code,
      observedState: attempt?.state ?? bundle.draft?.status ?? 'removed',
      lifecycleRevision: attempt ? attemptLifecycleRevision(attempt) : null };
  }
  function deliveryAction(draftId, action) {
    return exclusive(async () => {
      try { return await action(); }
      catch (error) {
        if (conflicts.includes(error?.code)) return authoritativeHold(draftId, error.code);
        throw error;
      }
    });
  }
  async function connection(endpoint) {
    const settings = await connectionStore.load();
    settings.endpoint = validateEndpoint(settings.endpoint);
    if (endpoint && validateEndpoint(endpoint) !== settings.endpoint) fail('restore_endpoint');
    return { endpoint: settings.endpoint, api: transportFactory(settings) };
  }
  const transition = (draftId, attempt, outcome) => store.transitionOutcome(draftId, attempt.id, outcome,
    { state: attempt.state, phase: attempt.phase, lifecycleRevision: attemptLifecycleRevision(attempt) });
  async function finish(draftId, attempt, result, alreadyPersisted = false) {
    if (attempt.code === 'clipper_request_changed') return authoritativeHold(draftId, 'identity_conflict');
    if (!await validReceipt(attempt, result)) return transition(draftId, attempt, { state: 'uncertain', phase: attempt.phase, code: attempt.code === 'clipper_request_changed' ? attempt.code : 'invalid_receipt' });
    // Failure to store the terminal receipt leaves the immutable posting attempt
    // frozen. No cleanup and no new ID; a later manual GET can confirm it again.
    if (!alreadyPersisted) attempt = await transition(draftId, attempt, { state: 'succeeded', phase: attempt.phase, result });
    try { await store.finishSuccess(draftId, attempt.id, attemptLifecycleRevision(attempt)); }
    catch (error) { if (conflicts.includes(error?.code)) throw error; fail('cleanup_pending'); }
    return { state: 'succeeded', result };
  }
  async function accept(draftId, attempt, outcome) {
    if (attempt.code === 'clipper_request_changed') return authoritativeHold(draftId, 'identity_conflict');
    if (outcome?.state === 'succeeded') return finish(draftId, attempt, outcome.result);
    const state = outcome?.state === 'recovery_required' ? 'pending' : outcome?.state;
    if (!['uncertain', 'pending', 'rejected'].includes(state)) outcome = { state: 'uncertain', code: 'invalid_response' };
    const saved = await transition(draftId, attempt, { state: outcome.state === 'recovery_required' ? 'pending' : outcome.state, phase: attempt.phase,
      ...(outcome.code ? { code: outcome.code } : {}) });
    if (saved.state === 'rejected') confirmations.delete(draftId);
    return { state: saved.state, ...(saved.code ? { code: saved.code } : {}) };
  }
  async function post(draftId, attempt, api) {
    let body;
    try { body = await persistedBody(store, draftId, attempt); }
    catch {
      // Only prepared proves no POST was started. CAS verifies that proof still
      // holds before unlocking. Posting-phase failures always remain frozen.
      return accept(draftId, attempt, { state: attempt.phase === 'prepared' ? 'rejected' : 'uncertain', code: attempt.phase === 'prepared' ? 'prepare_again' : 'invalid_snapshot' });
    }
    attempt = await transition(draftId, attempt, { state: 'sending', phase: 'posting' });
    let outcome;
    try { outcome = await api.postImport(body); }
    catch (error) {
      // Native errors and server bodies never become lifecycle metadata.
      const code = ['unauthorized', 'setup_required', 'unavailable', 'timeout', 'cancelled', 'invalid_response'].includes(error?.code) ? error.code : 'unavailable';
      outcome = { state: 'uncertain', code };
    }
    return accept(draftId, attempt, outcome);
  }
  async function check(draftId, replayUnknown) {
    const bundle = await store.readBundle(draftId);
    const attempt = bundle.attempts.find(a => a.id === bundle.draft?.activeAttemptId);
    if (!attempt || !isFrozen(bundle.draft)) fail('no_active_attempt');
    if (attempt.state === 'succeeded') return finish(draftId, attempt, attempt.result, true);
    const { api } = await connection(attempt.deliverySnapshot.endpoint);
    let outcome;
    try { outcome = await api.importStatus(attempt.id); }
    catch (error) { return accept(draftId, attempt, { state: 'uncertain', code: ['unauthorized', 'setup_required', 'timeout', 'cancelled', 'invalid_response'].includes(error?.code) ? error.code : 'unavailable' }); }
    if (outcome.state === 'unknown') {
      if (attempt.code === 'clipper_request_changed') {
        return authoritativeHold(draftId, 'identity_conflict');
      }
      // Unknown is NOT evidence of no concurrent POST. Exact same-ID replay is
      // the only permitted publication and requires its own explicit user action.
      if (replayUnknown) return post(draftId, attempt, api);
      return authoritativeHold(draftId, 'unknown');
    }
    return accept(draftId, attempt, outcome);
  }
  return Object.freeze({
    loadDestinations: () => exclusive(async () => {
      const { endpoint, api } = await connection();
      await api.info(); const result = await api.bases();
      choices = result.bases; choicesEndpoint = endpoint; folders.clear();
      return result;
    }),
    loadFolders: base => exclusive(async () => {
      if (!choices?.includes(base)) fail('confirm_destination');
      const { api } = await connection(choicesEndpoint);
      const result = await api.folders(base); folders.set(base, result); return result;
    }),
    confirmDestination: (draftId, choice) => exclusive(async () => {
      const selected = folders.get(choice.base);
      if (!selected || selected.destination_revision !== choice.destinationRevision || !selected.folders.includes(choice.parent)) fail('confirm_destination');
      await connection(choicesEndpoint);
      if (!await flush()) fail('save_failed');
      const saved = await store.updateMetadata(draftId, choice);
      confirmations.set(draftId, { endpoint: choicesEndpoint, destination: destination(saved) });
      return saved;
    }),
    send: (draftId, { confirmedFallbacks = [] } = {}) => deliveryAction(draftId, async () => {
      const confirmed = confirmations.get(draftId);
      if (!confirmed) fail('confirm_destination');
      if (!await flush()) fail('save_failed');
      const bundle = await store.readBundle(draftId);
      if (!bundle.draft || isFrozen(bundle.draft) || destination(bundle.draft) !== confirmed.destination) fail('confirm_destination');
      const { api, endpoint } = await connection(confirmed.endpoint);
      await api.info();
      let current;
      try { current = await api.folders(bundle.draft.base); }
      catch (error) {
        if (['clipper_destination_changed', 'clipper_destination_not_found'].includes(error?.code)) { confirmations.delete(draftId); fail('confirm_destination'); }
        throw error;
      }
      if (current.destination_revision !== bundle.draft.destinationRevision || !current.folders.includes(bundle.draft.parent)) { confirmations.delete(draftId); fail('confirm_destination'); }
      const snapshot = await buildImportSnapshot({ ...bundle, endpoint, importId: uuid(), confirmedFallbacks });
      const attempt = await store.beginAttempt(draftId, snapshot);
      onFrozen?.({ ...snapshot.originalDraft, status: 'sending', activeAttemptId: attempt.id });
      confirmations.delete(draftId);
      return post(draftId, attempt, api);
    }),
    check: draftId => deliveryAction(draftId, () => check(draftId, false)),
    retry: draftId => deliveryAction(draftId, () => check(draftId, true)),
  });
}
