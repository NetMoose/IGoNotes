import { afterEach, expect, it, vi } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';
import { createDraftStore } from '../src/storage/drafts.js';
import { createDelivery } from '../src/delivery/stateMachine.js';
import { createTransport } from '../src/connection/transport.js';

const firstId = '550e8400-e29b-41d4-a716-446655440000', nextId = '550e8400-e29b-41d4-a716-446655440001';
const handles = [];
afterEach(() => handles.splice(0).forEach(store => store.close()));
const json = (value, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } });
async function setup() {
  const store = createDraftStore({ indexedDB: new IDBFactory() }); handles.push(store);
  await store.saveDraft({ id: 'd', title: 'Clip', filename: 'clip.md', markdown: 'hello', sourceUrl: 'https://example.org/page', capturedAt: '2026-10-21T12:00:00Z', base: null, parent: '', destinationRevision: null, status: 'draft' });
  const status = vi.fn(async () => json({ import_id: firstId, state: 'pending' }));
  const posts = [];
  const post = vi.fn(async () => json({ code: 'internal_error', message: 'private reflected diagnostic' }, 500));
  const fetch = vi.fn(async (url, options) => {
    if (options.method === 'POST') { const m = JSON.parse(await options.body.get('manifest').text()); posts.push(m); return post(m); }
    if (url.includes('/imports?')) return status();
    if (url.endsWith('/bases')) return json({ bases: ['work'] });
    if (url.includes('/folders?')) return json({ base: 'work', folders: [''], destination_revision: 'rev1' });
    if (url.endsWith('/info')) return json({ protocol_version: 1, app_name: 'IGoNotes', app_version: 'dev',
      limits: { markdown_bytes: 5242880, assets: 100, asset_bytes: 10485760, total_asset_bytes: 52428800,
        multipart_bytes: 67108864, image_pixels: 40000000, gif_frames: 100, gif_total_pixels: 200000000 }, supported_image_types: ['image/gif'] });
    throw Error('Unexpected route');
  });
  const uuid = vi.fn().mockReturnValueOnce(firstId).mockReturnValue(nextId);
  const delivery = createDelivery({ store, uuid, connectionStore: { load: async () => ({ endpoint: 'http://localhost:9123', token: 'private-token' }) },
    transportFactory: settings => createTransport(settings, { fetch }) });
  async function confirm() { await delivery.loadDestinations(); await delivery.loadFolders('work'); await delivery.confirmDestination('d', { base: 'work', parent: '', destinationRevision: 'rev1' }); }
  return { store, status, post, posts, fetch, delivery, confirm, uuid };
}

it('real transport GET rejected/internal_error unlocks the preserved draft and needs reconfirmation/new ID for manual Send', async () => {
  const f = await setup(); await f.confirm();
  expect(await f.delivery.send('d')).toMatchObject({ state: 'uncertain' });
  const original = (await f.store.getAttempts('d'))[0];
  f.status.mockResolvedValue(json({ import_id: firstId, state: 'rejected', error: { code: 'internal_error', message: 'private reflected diagnostic' } }));
  expect(await f.delivery.check('d')).toEqual({ state: 'rejected', code: 'internal_error' });
  expect(await f.store.getDraft('d')).toMatchObject({ status: 'draft', activeAttemptId: null, destinationRevision: null, markdown: 'hello' });
  const rejected = (await f.store.getAttempts('d'))[0];
  expect(rejected.manifest).toEqual(original.manifest); expect(rejected.deliverySnapshot).toEqual(original.deliverySnapshot);
  expect(JSON.stringify(rejected)).not.toContain('private reflected diagnostic');
  expect(f.posts).toHaveLength(1);
  await expect(f.delivery.send('d')).rejects.toMatchObject({ code: 'confirm_destination' });
  await expect(f.delivery.retry('d')).rejects.toMatchObject({ code: 'no_active_attempt' });
  await f.confirm();
  f.post.mockImplementation(async m => {
    const hash = [...new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(m.markdown)))].map(n => n.toString(16).padStart(2, '0')).join('');
    return json({ import_id: m.import_id, base: m.base, path: m.filename, revision: `sha256:${hash}`, warnings: [] }, 201);
  });
  expect(await f.delivery.send('d')).toMatchObject({ state: 'succeeded' });
  expect(f.posts.map(m => m.import_id)).toEqual([firstId, nextId]);
  expect(await f.store.getDraft('d')).toBeUndefined();
});

it('real transport POST 500/internal_error retains a frozen immutable attempt and retries only the same ID', async () => {
  const f = await setup(); await f.confirm();
  expect(await f.delivery.send('d')).toMatchObject({ state: 'uncertain', code: 'unavailable' });
  const original = (await f.store.getAttempts('d'))[0];
  expect(await f.store.getDraft('d')).toMatchObject({ status: 'uncertain', activeAttemptId: firstId });
  await expect(f.store.deleteDraft('d')).rejects.toMatchObject({ code: 'draft_frozen' });
  f.status.mockImplementation(async () => json({ code: 'clipper_invalid_request' }, 400));
  expect(await f.delivery.retry('d')).toMatchObject({ state: 'uncertain', code: 'unavailable' });
  const retained = (await f.store.getAttempts('d'))[0];
  expect(retained.id).toBe(firstId); expect(retained.manifest).toEqual(original.manifest); expect(retained.deliverySnapshot).toEqual(original.deliverySnapshot);
  expect(await f.store.getDraft('d')).toMatchObject({ status: 'uncertain', activeAttemptId: firstId });
  expect(f.posts).toEqual([original.manifest, original.manifest]); expect(f.uuid).toHaveBeenCalledOnce();
  for (const [, options] of f.fetch.mock.calls) expect(options.credentials).toBe('omit');
});
