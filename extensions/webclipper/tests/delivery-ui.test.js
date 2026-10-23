// @vitest-environment jsdom
import { it, expect, vi, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/svelte';
import { IDBFactory } from 'fake-indexeddb';
import { webcrypto } from 'node:crypto';
import { createDraftStore } from '../src/storage/drafts.js';
import Editor from '../src/ui/Editor.svelte';
afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.history.replaceState(null, '', '/'); });
async function setup() {
  vi.stubGlobal('crypto', webcrypto);
  const store = createDraftStore({ indexedDB: new IDBFactory() });
  await store.saveDraft({ id: 'd', title: 'Clip', filename: 'clip.md', markdown: 'hello', sourceUrl: 'https://example.org/page', capturedAt: '2026-10-21T12:00:00Z', base: null, parent: '', destinationRevision: null, status: 'draft' });
  const api = { info: vi.fn(async () => ({ protocol_version: 1 })), bases: vi.fn(async () => ({ bases: ['active', 'inactive'] })),
    folders: vi.fn(async base => ({ base, folders: ['', 'nested'], destination_revision: 'rev1' })),
    postImport: vi.fn(async body => {
      const m = JSON.parse(await body.get('manifest').text());
      const hash = [...new Uint8Array(await webcrypto.subtle.digest('SHA-256', new TextEncoder().encode(m.markdown)))].map(n => n.toString(16).padStart(2, '0')).join('');
      return { state: 'succeeded', result: { import_id: m.import_id, base: m.base, path: `${m.parent}/${m.filename}`, revision: `sha256:${hash}`, warnings: m.external_images.map(e => e.reason) } };
    }), importStatus: vi.fn(async () => ({ state: 'pending' })) };
  const transportFactory = vi.fn(() => api);
  window.history.replaceState(null, '', '/editor.html?draft=d&keep=1');
  render(Editor, { store, deliveryOptions: { connectionStore: { load: async () => ({ endpoint: 'http://localhost:9123', token: 'secret' }) }, transportFactory } });
  await screen.findByLabelText('Markdown');
  return { store, api, transportFactory };
}
const click = name => fireEvent.click(screen.getByRole('button', { name, exact: true }));
async function confirm() {
  await waitFor(() => expect(screen.getByRole('button', { name: 'Load destinations', exact: true }).disabled).toBe(false));
  await click('Load destinations');
  const base = await screen.findByLabelText('Destination base');
  expect(base.value).toBe('');
  await fireEvent.change(base, { target: { value: 'inactive' } });
  const folder = await screen.findByLabelText('Destination folder');
  await waitFor(() => expect(folder.options.length).toBe(3));
  expect(folder.value).toBe('/');
  await fireEvent.change(folder, { target: { value: 'nested' } });
  await click('Confirm destination');
  await screen.findByText('Confirmed destination: inactive / nested');
}
it('does no network on mount or online; explicit choices persist and actual Send retains success view after cleanup', async () => {
  const f = await setup();
  window.dispatchEvent(new Event('online'));
  expect(f.transportFactory).not.toHaveBeenCalled();
  await confirm();
  expect(await f.store.getDraft('d')).toMatchObject({ base: 'inactive', parent: 'nested', destinationRevision: 'rev1' });
  await waitFor(() => expect(screen.getByRole('button', { name: 'Send', exact: true }).disabled).toBe(false));
  await click('Send');
  await screen.findByRole('heading', { name: 'Import confirmed' });
  expect(screen.getByText('inactive / nested/clip.md')).toBeTruthy();
  expect(await f.store.getDraft('d')).toBeUndefined();
  expect(window.location.search).toBe('?keep=1');
  await click('Refresh drafts');
  expect(screen.queryByText(/Requested draft not found/)).toBeNull();
  expect(screen.getByRole('heading', { name: 'Import confirmed' })).toBeTruthy();
});
it('freezes inputs from send start and keeps connection settings reachable while unresolved', async () => {
  const f = await setup(); await confirm();
  let release;
  f.api.info.mockImplementationOnce(() => new Promise(resolve => { release = resolve; }));
  await click('Send');
  expect(screen.getByLabelText('Markdown').closest('fieldset').disabled).toBe(true);
  await waitFor(() => expect(release).toBeTypeOf('function'));
  f.api.postImport.mockRejectedValueOnce({ code: 'unavailable' }); release({ protocol_version: 1 });
  await screen.findByText(/Delivery uncertain/);
  expect(screen.getByLabelText('Markdown').closest('fieldset').disabled).toBe(true);
  expect(screen.getByRole('button', { name: 'Connection settings', exact: true }).disabled).toBe(false);
  await click('Check attempt');
  await waitFor(() => expect(f.api.importStatus).toHaveBeenCalledOnce());
  expect(f.api.postImport).toHaveBeenCalledOnce();
});
it('keeps UI frozen if local result persistence and subsequent reads fail after server success', async () => {
  const f = await setup(); await confirm();
  const transition = f.store.transitionOutcome;
  f.store.transitionOutcome = async (...args) => {
    if (args[2].state === 'succeeded') { f.store.getDraft = async () => { throw Error('read failed'); }; throw Error('result disk failure'); }
    return transition(...args);
  };
  await click('Send');
  await screen.findByText('result disk failure');
  expect(screen.getByLabelText('Markdown').closest('fieldset').disabled).toBe(true);
  expect(screen.getByRole('button', { name: 'Delete draft', exact: true }).disabled).toBe(true);
});
it('can explicitly select an existing folder literally named __choose__', async () => {
  const f = await setup();
  f.api.folders.mockResolvedValue({ base: 'inactive', folders: ['', '__choose__'], destination_revision: 'rev1' });
  await click('Load destinations');
  await fireEvent.change(await screen.findByLabelText('Destination base'), { target: { value: 'inactive' } });
  const folder = await screen.findByLabelText('Destination folder');
  await fireEvent.change(folder, { target: { value: '__choose__' } });
  expect(screen.getByRole('button', { name: 'Confirm destination', exact: true }).disabled).toBe(false);
  await click('Confirm destination');
  await screen.findByText('Confirmed destination: inactive / __choose__');
});
it.each([false, true])('switch-back from uncertain, optional pending=%s, then Check rejection unlocks images and a reconfirmed new Send uses a new ID', async checkPending => {
  const f = await setup();
  await f.store.saveDraft({ ...await f.store.getDraft('d'), id: 'other', title: 'Other draft', markdown: 'other body' });
  await click('Refresh drafts');
  await confirm();
  f.api.postImport.mockRejectedValueOnce({ code: 'unavailable' });
  await click('Send');
  await screen.findByText(/Delivery uncertain/);
  const firstId = (await f.store.getDraft('d')).activeAttemptId;
  await click('Other draft');
  await waitFor(() => expect(screen.getByLabelText('Markdown').value).toBe('other body'));
  await click('Clip');
  await waitFor(() => expect(screen.getByLabelText('Markdown').value).toBe('hello'));
  // Mount has authoritatively cached the persisted uncertain status.
  await screen.findByText('Resolve the live delivery attempt before preparing images or changing external references.');
  expect(screen.getByRole('button', { name: 'Prepare images', exact: true }).disabled).toBe(true);
  if (checkPending) {
    f.api.importStatus.mockResolvedValueOnce({ state: 'pending' });
    await click('Check attempt');
    await screen.findByText(/Delivery pending/);
    expect(screen.getByLabelText('Markdown').closest('fieldset').disabled).toBe(true);
    expect(screen.getByRole('button', { name: 'Prepare images', exact: true }).disabled).toBe(true);
  }
  f.api.importStatus.mockResolvedValueOnce({ state: 'rejected', code: 'note_conflict' });
  await click('Check attempt');
  await screen.findByText(/Delivery rejected/);
  expect(await f.store.getDraft('d')).toMatchObject({ status: 'draft', activeAttemptId: null });
  expect(screen.getByLabelText('Markdown').closest('fieldset').disabled).toBe(false);
  await waitFor(() => expect(screen.getByRole('button', { name: 'Prepare images', exact: true }).disabled).toBe(false));
  await fireEvent.input(screen.getByLabelText('Filename'), { target: { value: 'fixed.md' } });
  await confirm();
  await waitFor(() => expect(screen.getByRole('button', { name: 'Send', exact: true }).disabled).toBe(false));
  await click('Send');
  await screen.findByRole('heading', { name: 'Import confirmed' });
  expect(f.api.postImport).toHaveBeenCalledTimes(2);
  const manifest = JSON.parse(await f.api.postImport.mock.calls[1][0].get('manifest').text());
  expect(manifest.import_id).not.toBe(firstId);
  expect(manifest.filename).toBe('fixed.md');
});
