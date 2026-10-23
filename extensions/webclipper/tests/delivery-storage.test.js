import { it, expect } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';
import { createDraftStore } from '../src/storage/drafts.js';
import { buildImportSnapshot } from '../src/images/rewrite.js';

export const draft = () => ({ id: 'delivery', title: 'Clip', filename: 'clip.md', markdown: 'hello', sourceUrl: 'https://example.org/page', capturedAt: '2026-10-21T12:00:00Z', base: 'inactive', parent: 'nested', destinationRevision: 'rev1', status: 'draft' });
export const importId = '550e8400-e29b-41d4-a716-446655440000';
export async function fixture() {
  const indexedDB = new IDBFactory();
  const store = createDraftStore({ indexedDB });
  const other = createDraftStore({ indexedDB });
  const saved = await store.saveDraft(draft());
  const snapshot = await buildImportSnapshot({ ...await store.readBundle(saved.id), endpoint: 'http://localhost:9123', importId });
  return { store, other, saved, snapshot };
}

it('atomically compares preparation and freezes every mutation API across handles before posting', async () => {
  const { store, other, saved, snapshot } = await fixture();
  const attempt = await store.beginAttempt(saved.id, snapshot);
  expect(attempt).toMatchObject({ id: importId, phase: 'prepared', state: 'sending' });
  expect(await other.getDraft(saved.id)).toMatchObject({ status: 'sending', activeAttemptId: importId });
  for (const mutate of [() => other.saveDraft(saved), () => other.deleteDraft(saved.id),
    () => other.updateMetadata(saved.id, { base: 'other', parent: '', destinationRevision: 'other' }),
    () => other.updateDraftAssets(saved.id, { expectedMarkdown: saved.markdown, expectedAssets: [], assets: [] })]) {
    await expect(mutate()).rejects.toMatchObject({ code: 'draft_frozen' });
  }
  await expect(other.transitionOutcome(saved.id, 'wrong', { state: 'uncertain', phase: 'posting' }, { state: 'sending', phase: 'prepared', lifecycleRevision: 0 })).rejects.toMatchObject({ code: 'attempt_changed' });
  expect((await store.getAttempts(saved.id))[0].manifest).toEqual(snapshot.manifest);
});

it('rejects stale preparations and stale UI resurrection; destination update merges current metadata', async () => {
  const { store, saved, snapshot } = await fixture();
  await store.saveDraft({ ...saved, title: 'new' }, { requireExisting: true });
  await expect(store.beginAttempt(saved.id, snapshot)).rejects.toMatchObject({ code: 'preparation_changed' });
  const updated = await store.updateMetadata(saved.id, { base: 'new', parent: '', destinationRevision: 'rev2' });
  expect(updated.title).toBe('new');
  await store.deleteDraft(saved.id);
  await expect(store.saveDraft(saved, { requireExisting: true })).rejects.toMatchObject({ code: 'draft_deleted' });
});

it('persists terminal receipt before owned cleanup, rejects premature cleanup and preserves other work', async () => {
  const { store, saved, snapshot } = await fixture();
  await store.saveDraft({ ...draft(), id: 'other' });
  await store.beginAttempt(saved.id, snapshot);
  await expect(store.finishSuccess(saved.id, importId, 0)).rejects.toMatchObject({ code: 'attempt_changed' });
  const result = { import_id: importId, base: 'inactive', path: 'nested/clip.md', revision: `sha256:${'a'.repeat(64)}`, warnings: [] };
  const terminal = await store.transitionOutcome(saved.id, importId, { state: 'succeeded', phase: 'posting', result }, { state: 'sending', phase: 'prepared', lifecycleRevision: 0 });
  expect((await store.getAttempts(saved.id))[0].result).toEqual(result);
  await store.finishSuccess(saved.id, importId, terminal.lifecycleRevision);
  expect(await store.getDraft(saved.id)).toBeUndefined();
  expect(await store.getDraft('other')).toBeDefined();
});
it('validates lifecycle fields and requires exact state/phase CAS without rewriting immutable payload', async () => {
  const { store, saved, snapshot } = await fixture();
  await expect(store.saveDraft({ ...saved, activeAttemptId: 42 })).rejects.toMatchObject({ code: 'invalid_data' });
  await expect(store.saveDraft({ ...saved, status: 'made_up' })).rejects.toMatchObject({ code: 'invalid_data' });
  await store.beginAttempt(saved.id, snapshot);
  for (const [outcome, expected] of [
    [{ state: 'uncertain', phase: 'posting', manifest: {} }, { state: 'sending', phase: 'prepared', lifecycleRevision: 0 }],
    [{ state: 'uncertain', phase: 'posting' }, {}],
    [{ state: 'succeeded', phase: 'posting', result: {} }, { state: 'sending', phase: 'prepared', lifecycleRevision: 0 }],
  ]) await expect(store.transitionOutcome(saved.id, importId, outcome, expected)).rejects.toMatchObject({ code: 'invalid_data' });
  await expect(store.transitionOutcome(saved.id, importId, { state: 'uncertain', phase: 'posting' }, { state: 'pending', phase: 'posting', lifecycleRevision: 0 })).rejects.toMatchObject({ code: 'attempt_changed' });
});
