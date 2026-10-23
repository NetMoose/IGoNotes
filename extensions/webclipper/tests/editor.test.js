// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/svelte';
import { IDBFactory } from 'fake-indexeddb';
import { createDraftStore } from '../src/storage/drafts.js';
import * as storage from '../src/storage/drafts.js';
import Editor from '../src/ui/Editor.svelte';

const draft = (id = 'a') => ({ id, title: `Title ${id}`, filename: `${id}.md`, markdown: `# Body ${id}`, sourceUrl: 'https://example.org/article?q=1', capturedAt: '2026-10-23T10:00:00Z', base: null, parent: 'folder', destinationRevision: 'revision', status: 'draft' });
const image = { id: 'same', normalizedUrl: 'https://example.org/a.png', descriptor: { mime: 'image/png' }, blob: null };
const attempt = { id: 'same', createdAt: '2026-10-23T10:00:00Z', manifest: { title: 'immutable' }, deliverySnapshot: { endpoint: 'http://localhost:8080', assetIds: ['same'], digest: 'digest' } };
const handles = [];
async function setup(ids = ['a', 'b']) {
  const indexedDB = new IDBFactory();
  const store = createDraftStore({ indexedDB });
  handles.push(store);
  for (const id of ids) await store.saveDraft(draft(id), { assets: [image], attempt });
  return { store, indexedDB };
}
const click = name => fireEvent.click(screen.getByRole('button', { name, exact: true }));
const edit = (label, value) => fireEvent.input(screen.getByLabelText(label), { target: { value } });
async function opened(store, initialDraftId = 'a') {
  const view = render(Editor, { store, initialDraftId });
  await screen.findByLabelText('Markdown');
  return view;
}
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
afterEach(() => {
  cleanup();
  for (const handle of handles.splice(0)) handle.close();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.history.replaceState(null, '', '/');
});

it('mounts connection settings only on demand and preserves unsaved draft edits on close', async () => {
  const { store } = await setup();
  const get = vi.fn(async () => ({}));
  vi.stubGlobal('chrome', { storage: { local: { setAccessLevel: vi.fn(async () => {}), get } } });
  await opened(store);
  expect(get).not.toHaveBeenCalled();
  await edit('Markdown', 'unsaved while connecting');
  await click('Connection settings');
  await screen.findByLabelText('Endpoint');
  await waitFor(() => expect(get).toHaveBeenCalledOnce());
  await click('Close connection settings');
  expect(screen.queryByLabelText('Endpoint')).toBeNull();
  expect(screen.getByLabelText('Markdown').value).toBe('unsaved while connecting');
  expect(screen.getByRole('status').textContent).toBe('Unsaved changes');
  expect((await store.getDraft('a')).markdown).toBe('# Body a');
});

it('clears only local credentials while preserving drafts, stored assets and immutable attempts', async () => {
  const { store } = await setup();
  const remove = vi.fn(async () => {});
  vi.stubGlobal('chrome', { storage: { local: {
    setAccessLevel: vi.fn(async () => {}), get: vi.fn(async () => ({})), remove,
  } } });
  const beforeAssets = await store.getAssets('a');
  const beforeAttempts = await store.getAttempts('a');
  await opened(store);
  await edit('Markdown', 'still unsaved');
  await click('Connection settings');
  await waitFor(() => expect(screen.getByRole('button', { name: 'Clear connection' }).disabled).toBe(false));
  await click('Clear connection');
  await screen.findByText('Local connection removed. Server token is not revoked.');
  expect(remove).toHaveBeenCalledWith('clipperConnection');
  await click('Close connection settings');
  expect(screen.getByLabelText('Markdown').value).toBe('still unsaved');
  expect(await store.listDrafts()).toHaveLength(2);
  expect(await store.getAssets('a')).toEqual(beforeAssets);
  expect(await store.getAttempts('a')).toEqual(beforeAttempts);
});

it('edits metadata and Markdown, saves transactionally and reopens without losing assets or attempts', async () => {
  const { store, indexedDB } = await setup();
  const view = await opened(store);
  expect(screen.getByText(draft().capturedAt)).toBeTruthy();
  expect(screen.getByRole('link', { name: draft().sourceUrl }).href).toBe(draft().sourceUrl);
  await edit('Title', '<b>Edited 🦉</b>');
  await edit('Filename', 'edited.md');
  await edit('Markdown', '# Edited\n<script>globalThis.executed = true</script>');
  expect(screen.getByRole('status').textContent).toBe('Unsaved changes');
  await click('Save');
  await waitFor(() => expect(screen.getByRole('status').textContent).toBe('Saved locally'));
  expect(await store.getDraft('a')).toMatchObject({ ...draft(), title: '<b>Edited 🦉</b>', filename: 'edited.md', markdown: '# Edited\n<script>globalThis.executed = true</script>' });
  expect((await store.getAssets('a'))[0]).toMatchObject(image);
  expect((await store.getAttempts('a'))[0].manifest).toEqual(attempt.manifest);
  view.unmount();
  // The injected handle remains usable; reopen a separate database connection.
  expect(await store.getDraft('a')).toBeTruthy();
  const reopened = createDraftStore({ indexedDB });
  handles.push(reopened);
  await opened(reopened);
  expect(screen.getByLabelText('Title').value).toBe('<b>Edited 🦉</b>');
  expect(screen.getByLabelText('Filename').value).toBe('edited.md');
  expect(screen.getByLabelText('Markdown').value).toContain('<script>');
  expect(document.querySelector('script, img, b')).toBeNull();
  expect(globalThis.executed).toBeUndefined();
});

it('keeps new typing dirty while an older save completes, then serially persists the latest text', async () => {
  const { store } = await setup();
  const gate = deferred();
  const saveDraft = vi.fn().mockImplementationOnce(async value => { await gate.promise; return store.saveDraft(value); }).mockImplementation(value => store.saveDraft(value));
  await opened({ ...store, saveDraft });
  await edit('Markdown', 'first edit');
  await click('Save');
  expect(screen.getByRole('status').textContent).toBe('Saving…');
  await edit('Markdown', 'newer edit');
  expect(screen.getByRole('status').textContent).toContain('Unsaved');
  gate.resolve();
  await waitFor(() => expect(screen.getByRole('status').textContent).toBe('Saved locally'));
  expect(saveDraft).toHaveBeenCalledTimes(2);
  expect((await store.getDraft('a')).markdown).toBe('newer edit');
  expect(screen.getByLabelText('Markdown').value).toBe('newer edit');
});

it('blocks switching and refresh on save failure, preserves edits and retries', async () => {
  const { store } = await setup();
  const saveDraft = vi.fn().mockRejectedValue(new Error('Storage quota exceeded. Free space and retry.'));
  await opened({ ...store, saveDraft });
  await edit('Markdown', 'precious unsaved text');
  await click('Title b');
  await screen.findByRole('alert');
  expect(screen.getByLabelText('Markdown').value).toBe('precious unsaved text');
  expect(screen.getByRole('status').textContent).toContain('not saved');
  await click('Refresh drafts');
  expect(screen.getByLabelText('Markdown').value).toBe('precious unsaved text');
  expect((await store.getDraft('a')).markdown).toBe('# Body a');
  saveDraft.mockImplementation(value => store.saveDraft(value));
  await click('Retry');
  await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
  expect((await store.getDraft('a')).markdown).toBe('precious unsaved text');
  await click('Title b');
  await waitFor(() => expect(screen.getByLabelText('Markdown').value).toBe('# Body b'));
});

it('flushes edits on switching and refresh rather than dropping them', async () => {
  const { store } = await setup();
  await opened(store);
  await edit('Title', 'New title');
  await click('Title b');
  await waitFor(() => expect(screen.getByLabelText('Markdown').value).toBe('# Body b'));
  expect((await store.getDraft('a')).title).toBe('New title');
  await edit('Markdown', 'refresh this');
  await click('Refresh drafts');
  await waitFor(() => expect(screen.getByRole('status').textContent).toBe('Saved locally'));
  expect((await store.getDraft('b')).markdown).toBe('refresh this');
});

it('explicitly deletes only the selected draft and its owned assets/attempts', async () => {
  const { store } = await setup();
  await opened(store);
  await click('Delete draft');
  await waitFor(() => expect(screen.getByLabelText('Markdown').value).toBe('# Body b'));
  expect(await store.getDraft('a')).toBeUndefined();
  expect(await store.getAssets('a')).toEqual([]);
  expect(await store.getAttempts('a')).toEqual([]);
  expect(await store.getAssets('b')).toHaveLength(1);
  expect(await store.getAttempts('b')).toHaveLength(1);
});

it('downloads current unsaved Markdown on quota failure with a safe filename and revokes the URL', async () => {
  const { store } = await setup();
  await opened({ ...store, saveDraft: async () => { throw new Error('Storage full'); } });
  await edit('Markdown', '<img src="https://external.invalid/pixel" onerror="executed=true">');
  await edit('Filename', '../../unsafe.html');
  await click('Save');
  await screen.findByRole('alert');
  let blob;
  vi.stubGlobal('URL', class extends URL {
    static createObjectURL(value) { blob = value; return 'blob:local-download'; }
    static revokeObjectURL = vi.fn();
  });
  const download = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function () {
    expect(this.download).toBe('unsafe.html.md');
    expect(this.href).toBe('blob:local-download');
  });
  await click('Download Markdown');
  expect(download).toHaveBeenCalledOnce();
  expect(blob.type).toBe('text/markdown;charset=utf-8');
  const text = await new Promise(resolve => { const reader = new FileReader(); reader.onload = () => resolve(reader.result); reader.readAsText(blob); });
  expect(text).toBe(screen.getByLabelText('Markdown').value);
  await waitFor(() => expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:local-download'));
  expect(document.querySelector('img')).toBeNull();
});

it('shows truthful empty state and loading/storage errors with retry', async () => {
  const { store } = await setup([]);
  const gate = deferred();
  const listDrafts = vi.fn().mockReturnValueOnce(gate.promise).mockImplementation(() => store.listDrafts());
  render(Editor, { store: { ...store, listDrafts } });
  expect(screen.getByText('Loading drafts…')).toBeTruthy();
  gate.reject(new Error('Storage unavailable'));
  await screen.findByRole('alert');
  await click('Retry');
  await screen.findByText('No local drafts yet. Page capture is not available yet.');
  expect(screen.queryByLabelText('Markdown')).toBeNull();
  expect(screen.queryByRole('button', { name: /capture/i })).toBeNull();
});

it('never silently selects another draft for an unknown query ID', async () => {
  const { store } = await setup();
  window.history.replaceState(null, '', '/editor.html?draft=missing');
  render(Editor, { store });
  expect((await screen.findByRole('alert')).textContent).toContain('not found');
  expect(screen.queryByLabelText('Markdown')).toBeNull();
  await click('Title a');
  await screen.findByLabelText('Markdown');
  expect(screen.getByLabelText('Markdown').value).toBe('# Body a');
});

it('selects a known query ID, otherwise the most recently updated draft', async () => {
  const { store } = await setup();
  await store.saveDraft(draft('a'));
  window.history.replaceState(null, '', '/editor.html?draft=b');
  const view = render(Editor, { store });
  await screen.findByLabelText('Markdown');
  expect(screen.getByLabelText('Markdown').value).toBe('# Body b');
  view.unmount();
  window.history.replaceState(null, '', '/editor.html');
  render(Editor, { store });
  await screen.findByLabelText('Markdown');
  expect(screen.getByLabelText('Markdown').value).toBe('# Body a');
});

it('ignores late async completions after unmount and does not close an injected handle', async () => {
  const { store } = await setup();
  const gate = deferred();
  const close = vi.fn();
  const view = await opened({ ...store, close, saveDraft: async value => { await gate.promise; return store.saveDraft(value); } });
  await edit('Markdown', 'pending');
  await click('Save');
  view.unmount();
  gate.resolve();
  await waitFor(async () => expect((await store.getDraft('a')).markdown).toBe('pending'));
  expect(close).not.toHaveBeenCalled();
  expect(screen.queryByRole('status')).toBeNull();
});

it('renders unsafe source metadata literally without a script link', async () => {
  const value = { ...draft(), sourceUrl: 'javascript:alert(1)', capturedAt: '<img src=x onerror=alert(1)>' };
  await opened({ listDrafts: async () => [value], getDraft: async () => value });
  expect(screen.getByText(value.sourceUrl)).toBeTruthy();
  expect(screen.getByText(value.capturedAt)).toBeTruthy();
  expect(screen.queryByRole('link')).toBeNull();
  expect(document.querySelector('img')).toBeNull();
});

it('keeps the old draft on selection read errors and retries the requested draft', async () => {
  const { store } = await setup();
  const getDraft = vi.fn().mockImplementation(id => store.getDraft(id));
  await opened({ ...store, getDraft });
  getDraft.mockRejectedValueOnce(new Error('Read failed'));
  await click('Title b');
  await screen.findByRole('alert');
  expect(screen.getByLabelText('Markdown').value).toBe('# Body a');
  await click('Retry');
  await waitFor(() => expect(screen.getByLabelText('Markdown').value).toBe('# Body b'));
});

it('keeps edits on failed deletion and permits explicit retry', async () => {
  const { store } = await setup();
  const deleteDraft = vi.fn().mockRejectedValueOnce(new Error('Deletion failed')).mockImplementation(id => store.deleteDraft(id));
  await opened({ ...store, deleteDraft });
  await edit('Markdown', 'unsaved but retained');
  await click('Delete draft');
  await screen.findByRole('alert');
  expect(screen.getByLabelText('Markdown').value).toBe('unsaved but retained');
  await click('Retry');
  await waitFor(() => expect(screen.getByLabelText('Markdown').value).toBe('# Body b'));
  expect(await store.getDraft('a')).toBeUndefined();
});

it('blocks navigation while a save is pending and warns on browser reload until it completes', async () => {
  const { store } = await setup();
  const gate = deferred();
  await opened({ ...store, saveDraft: async value => { await gate.promise; return store.saveDraft(value); } });
  await edit('Markdown', 'wait for commit');
  await click('Save');
  await click('Title b');
  expect(screen.getByLabelText('Markdown').value).toBe('wait for commit');
  expect(screen.getByLabelText('Markdown').matches(':disabled')).toBe(true);
  const event = new Event('beforeunload', { cancelable: true });
  window.dispatchEvent(event);
  expect(event.defaultPrevented).toBe(true);
  gate.resolve();
  await waitFor(() => expect(screen.getByLabelText('Markdown').value).toBe('# Body b'));
  const cleanEvent = new Event('beforeunload', { cancelable: true });
  window.dispatchEvent(cleanEvent);
  expect(cleanEvent.defaultPrevented).toBe(false);
});

it('keeps download failure retryable and clears only its error after a successful download', async () => {
  const { store } = await setup();
  await opened(store);
  vi.stubGlobal('URL', class extends URL {
    static createObjectURL = vi.fn().mockImplementationOnce(() => { throw new Error('Download unavailable'); }).mockReturnValue('blob:retry');
    static revokeObjectURL = vi.fn();
  });
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
  await click('Download Markdown');
  await screen.findByRole('alert');
  await click('Retry');
  await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
  await waitFor(() => expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:retry'));
});

it('drains newer edits before closing its own handle after unmount', async () => {
  const { store } = await setup();
  const gate = deferred();
  const close = vi.fn(() => store.close());
  const saveDraft = vi.fn().mockImplementationOnce(async value => { await gate.promise; return store.saveDraft(value); }).mockImplementation(value => store.saveDraft(value));
  vi.spyOn(storage, 'createDraftStore').mockReturnValue({ ...store, close, saveDraft });
  const view = render(Editor, { initialDraftId: 'a' });
  await screen.findByLabelText('Markdown');
  await edit('Markdown', 'first');
  await click('Save');
  await edit('Markdown', 'newest before unmount');
  view.unmount();
  expect(close).not.toHaveBeenCalled();
  gate.resolve();
  await waitFor(() => expect(close).toHaveBeenCalledOnce());
  expect(saveDraft).toHaveBeenCalledTimes(2);
  expect(saveDraft.mock.calls[1][0].markdown).toBe('newest before unmount');
  expect(screen.queryByRole('status')).toBeNull();
});

it('ignores a list result delivered after unmount', async () => {
  const { store } = await setup();
  const gate = deferred();
  const getDraft = vi.fn();
  const view = render(Editor, { store: { ...store, listDrafts: () => gate.promise, getDraft } });
  view.unmount();
  gate.resolve(await store.listDrafts());
  await gate.promise;
  expect(getDraft).not.toHaveBeenCalled();
  expect(screen.queryByLabelText('Markdown')).toBeNull();
});

it('reopens an owned failed-open handle on Retry instead of reusing its cached rejection', async () => {
  const { store } = await setup();
  const close = vi.fn();
  vi.spyOn(storage, 'createDraftStore')
    .mockReturnValueOnce({ close, listDrafts: async () => { throw new Error('Opening storage failed'); } })
    .mockReturnValueOnce(store);
  render(Editor, { initialDraftId: 'a' });
  await screen.findByRole('alert');
  await click('Retry');
  await screen.findByLabelText('Markdown');
  expect(screen.getByLabelText('Markdown').value).toBe('# Body a');
  expect(close).toHaveBeenCalledOnce();
});
