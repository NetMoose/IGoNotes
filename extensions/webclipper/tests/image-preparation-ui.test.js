// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/svelte';
import { IDBFactory } from 'fake-indexeddb';
import { Blob as NativeBlob } from 'node:buffer';
import { webcrypto } from 'node:crypto';
import { createDraftStore } from '../src/storage/drafts.js';
import { createImageAcquirer } from '../src/images/acquire.js';
import { buildImportSnapshot } from '../src/images/rewrite.js';
import Editor from '../src/ui/Editor.svelte';
import ImagePreparationHost from './helpers/ImagePreparationHost.svelte';

const a = 'https://cdn.example/a.png', b = 'https://cdn.example/b.png';
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6Z1sAAAAASUVORK5CYII=', 'base64');
const image = url => `![image](${url})`;
const handles = [];
const click = name => fireEvent.click(screen.getByRole('button', { name, exact: true }));
const edit = value => fireEvent.input(screen.getByLabelText('Markdown'), { target: { value } });
function deferred() { let resolve; const promise = new Promise(yes => { resolve = yes; }); return { promise, resolve }; }
async function setup({ markdown = image(a), storeOptions, wrap = x => x, fetch, permissions } = {}) {
  vi.stubGlobal('Blob', NativeBlob); vi.stubGlobal('crypto', webcrypto);
  // Node Response streams produce Node-realm typed arrays, not jsdom's realm.
  vi.stubGlobal('Uint8Array', Object.getPrototypeOf(Buffer.prototype).constructor);
  const indexedDB = new IDBFactory();
  const store = createDraftStore({ indexedDB, ...storeOptions }); handles.push(store);
  await store.saveDraft({ id: 'a', title: 'Title a', filename: 'note.md', markdown, sourceUrl: 'https://example.org/article', capturedAt: '2026-10-23T10:00:00Z', base: 'work', parent: '', destinationRevision: 'rev', status: 'draft' });
  fetch ??= vi.fn(async () => new Response(png, { headers: { 'Content-Type': 'image/png' } }));
  permissions ??= { request: vi.fn(async () => true), contains: vi.fn(async () => true) };
  const onImageState = vi.fn();
  const view = render(Editor, { store: wrap(store), initialDraftId: 'a', imageAcquirer: createImageAcquirer({ fetch, permissions }), onImageState });
  await screen.findByLabelText('Markdown');
  return { store, indexedDB, fetch, permissions, onImageState, view, state: () => onImageState.mock.calls.at(-1)?.[0] };
}
afterEach(() => { cleanup(); handles.splice(0).forEach(x => x.close()); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it('requests current form hosts synchronously before flush, commits bytes before readiness, and reopens cached without fetching', async () => {
  const gate = deferred(), order = [];
  const permissions = { request: vi.fn(() => { order.push('request'); return Promise.resolve(true); }), contains: vi.fn(async () => { order.push('contains'); return true; }) };
  const ctx = await setup({ permissions, wrap: store => ({ ...store, saveDraft: async draft => { order.push('flush'); await gate.promise; return store.saveDraft(draft); } }) });
  await edit(image(b));
  await click('Prepare images');
  expect(order).toEqual(['request', 'flush']);
  expect(permissions.request).toHaveBeenCalledWith({ origins: ['https://cdn.example/*'] });
  expect(ctx.fetch).not.toHaveBeenCalled();
  expect(ctx.state().ready).toBe(false);
  gate.resolve();
  await screen.findByText('Stored locally');
  await waitFor(() => expect(ctx.state().ready).toBe(true));
  await waitFor(() => expect(document.activeElement.classList.contains('image-summary')).toBe(true));
  expect((await ctx.store.getAssets('a')).filter(row => row.blob)).toHaveLength(1);
  const built = await buildImportSnapshot({ ...await ctx.store.readBundle('a'), endpoint: 'http://localhost:8080', importId: '550e8400-e29b-41d4-a716-446655440000', confirmedFallbacks: ctx.state().confirmedFallbacks });
  expect(built.manifest.markdown).toContain('assets/images/clip-');
  expect(built.autonomous).toBe(true);
  ctx.view.unmount(); ctx.fetch.mockClear();
  render(Editor, { store: ctx.store, initialDraftId: 'a', imageAcquirer: createImageAcquirer({ fetch: ctx.fetch, permissions }) });
  await screen.findByText('Stored locally');
  expect(ctx.fetch).not.toHaveBeenCalled();
  expect(document.querySelector('img')).toBeNull();
});

it('keeps unconfirmed errors blocked; selection alone is not consent; confirms exact URLs/reasons and invalidates on edits and retry', async () => {
  const ctx = await setup({ permissions: { request: vi.fn(async () => false), contains: vi.fn(async () => false) } });
  await click('Prepare images');
  await screen.findByText(/Image host access was not granted/);
  await screen.findByText('Image preparation finished. Review each image below.');
  expect(ctx.state().ready).toBe(false);
  await expect(buildImportSnapshot({ ...await ctx.store.readBundle('a'), endpoint: 'http://localhost:8080',
    importId: '550e8400-e29b-41d4-a716-446655440000', confirmedFallbacks: ctx.state().confirmedFallbacks })).rejects.toThrow(/acknowledgement/);
  await fireEvent.click(screen.getByLabelText(`Keep external reference: ${a}`));
  expect(ctx.state().confirmedFallbacks).toEqual([]);
  await click('Confirm external references');
  await waitFor(() => expect(ctx.state().ready).toBe(true));
  expect(screen.getByText(/not fully offline/)).toBeTruthy();
  const ack = ctx.state().confirmedFallbacks;
  expect(Object.isFrozen(ack)).toBe(true);
  const built = await buildImportSnapshot({ ...await ctx.store.readBundle('a'), endpoint: 'http://localhost:8080', importId: '550e8400-e29b-41d4-a716-446655440000', confirmedFallbacks: ack });
  expect(built.manifest.external_images).toEqual(ack);
  expect(built.manifest.markdown).toContain(a); expect(built.autonomous).toBe(false);
  await fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Changed title' } });
  expect(ctx.state().confirmedFallbacks).toEqual(ack);
  await click('Retry images / grant access');
  await waitFor(() => expect(ctx.state().confirmedFallbacks).toEqual([]));
  await edit(image(b));
  await screen.findByText(b);
  expect(ctx.state().ready).toBe(false);
  expect(ctx.state().confirmedFallbacks).toEqual([]);
  expect(screen.queryByLabelText(`Keep external reference: ${a}`)).toBeNull();
});

it('preserves unsaved Markdown on flush quota failure, never fetches and displays failure', async () => {
  const ctx = await setup({ wrap: store => ({ ...store, saveDraft: async () => { throw Error('Storage quota exceeded'); } }) });
  await edit(image(b) + '\nprecious unsaved');
  await click('Prepare images');
  await screen.findByText('Storage quota exceeded');
  expect(ctx.fetch).not.toHaveBeenCalled(); expect(ctx.permissions.request).toHaveBeenCalledOnce();
  expect(screen.getByLabelText('Markdown').value).toContain('precious unsaved');
  expect(ctx.state().ready).toBe(false);
});

it('lists every failure literally and forbids unsafe/private/data external fallbacks', async () => {
  const urls = [a, b, 'https://cdn.example/large', 'http://127.0.0.1/private', 'blob:https://example.org/no', 'data:image/svg+xml,bad'];
  const ctx = await setup({ markdown: urls.map(image).join('\n'), fetch: vi.fn(async url => url === a ? Response.redirect('https://other.example/x') : url === b ? new Response('', { status: 404 }) : new Response(png, { headers: { 'Content-Length': '99999999' } })) });
  await click('Prepare images');
  await waitFor(() => expect(ctx.state().preparationErrors.length).toBeGreaterThanOrEqual(6));
  for (const url of urls) expect(screen.getByText(url)).toBeTruthy();
  expect(screen.getAllByText('Failed')).toHaveLength(6);
  expect(ctx.state().preparationErrors).toEqual(expect.arrayContaining([
    expect.objectContaining({ url: a, code: 'redirect' }),
    expect.objectContaining({ url: b, code: 'http', status: 404 }),
    expect.objectContaining({ url: urls[2], code: 'tooLarge' }),
    ...urls.slice(3).map(url => expect.objectContaining({ url, code: 'unsafe' })),
  ]));
  expect(screen.getAllByRole('checkbox')).toHaveLength(3);
  expect(screen.getAllByText(/Edit or remove/)).toHaveLength(3);
  expect(ctx.state().ready).toBe(false);
});

it('shows committed-cache persistence failure rather than false ready', async () => {
  const ctx = await setup({ storeOptions: { limits: { maxBlobBytes: 1 } } });
  await click('Prepare images');
  await screen.findByText(/could not be saved/);
  expect(ctx.state().ready).toBe(false);
  expect((await ctx.store.getAssets('a')).filter(row => row.blob)).toHaveLength(0);
});

it('keeps the future send predicate blocked until a separate confirmation and preserves literal result warnings', async () => {
  const ctx = await setup(); ctx.view.unmount();
  const draft = await ctx.store.getDraft('a');
  // Exact HTTP success/receipt DTO: warnings are reason strings, not the
  // structured {url, reason} acknowledgements used during preparation.
  const result = JSON.parse(JSON.stringify({ import_id: '550e8400-e29b-41d4-a716-446655440000',
    base: 'work', path: 'note.md', revision: 'sha256:' + 'a'.repeat(64),
    warnings: ['Keep "quoted" external reference.', 'Keep "quoted" external reference.',
      '<img src=x onerror="executed=true"><script>executed=true</script>'] }));
  const view = render(ImagePreparationHost, { store: ctx.store, draft,
    acquirer: createImageAcquirer({ permissions: { contains: async () => false, request: async () => false } }),
    resultWarnings: result.warnings });
  await waitFor(() => expect(screen.getByRole('button', { name: 'Future send predicate' }).disabled).toBe(true));
  await fireEvent.click(screen.getByLabelText(`Keep external reference: ${a}`));
  expect(screen.getByRole('button', { name: 'Future send predicate' }).disabled).toBe(true);
  await click('Confirm external references');
  await waitFor(() => expect(screen.getByRole('button', { name: 'Future send predicate' }).disabled).toBe(false));
  const warnings = screen.getByRole('complementary', { name: 'External image warnings' });
  expect([...warnings.querySelectorAll('li')].map(row => row.textContent)).toEqual([
    `${a} — User chose external reference instead of local storage.`, ...result.warnings]);
  expect(warnings.textContent).toContain(`${a} — User chose external reference instead of local storage.`);
  expect(screen.getByText(/not fully offline/)).toBeTruthy();
  expect(document.querySelector('script, img')).toBeNull();
  for (const status of ['sending', 'uncertain', 'pending']) {
    await view.rerender({ draft: { ...draft, status } });
    expect(screen.getByRole('button', { name: 'Prepare images' }).disabled).toBe(true);
    expect(screen.getByRole('button', { name: 'Confirm external references' }).disabled).toBe(true);
    expect(screen.getByRole('button', { name: 'Future send predicate' }).disabled).toBe(true);
  }
});

it('does not count fetch success before the ready transaction commits, then retries clear failures', async () => {
  const gate = deferred(); let readyWrite;
  const ctx = await setup({ wrap: store => ({ ...store, updateDraftAssets: async (id, update) => {
    if (update.assets.some(row => row.descriptor.status === 'ready')) { readyWrite = true; await gate.promise; }
    return store.updateDraftAssets(id, update);
  } }) });
  await click('Prepare images');
  await waitFor(() => expect(readyWrite).toBe(true));
  expect(ctx.fetch).toHaveBeenCalledOnce(); expect(ctx.state().ready).toBe(false);
  expect(screen.queryByText('Stored locally')).toBeNull();
  expect((await ctx.store.getAssets('a')).filter(row => row.blob)).toHaveLength(0);
  gate.resolve(); await screen.findByText('Stored locally');
  await waitFor(() => expect(ctx.state().ready).toBe(true));
  await edit(image(b));
  ctx.fetch.mockImplementationOnce(async () => new Response('', { status: 404 }));
  await click('Retry images / grant access');
  await screen.findByText(/http \(404\)/);
  await waitFor(() => expect(screen.getByRole('button', { name: 'Retry images / grant access' }).disabled).toBe(false));
  await click('Retry images / grant access');
  await screen.findByText('Stored locally');
  await waitFor(() => expect(ctx.state().ready).toBe(true));
  expect(ctx.state().preparationErrors).toEqual([]);
  expect(screen.queryByText(/http \(404\)/)).toBeNull();
});

it('retains committed images on cancel and unmount', async () => {
  let signal; const gate = deferred();
  const ctx = await setup({ markdown: image(a) + '\n' + image(b), fetch: vi.fn(async (url, options) => {
    if (url === b) { signal = options.signal; await gate.promise; }
    return new Response(png, { headers: { 'Content-Type': 'image/png' } });
  }) });
  await click('Prepare images');
  await waitFor(() => expect(signal).toBeTruthy());
  // Acquisition batches settle together; seed a prior committed checkpoint to
  // exercise cancellation retaining bytes independently of the in-flight batch.
  await click('Cancel image preparation'); gate.resolve();
  await edit(image(a));
  await click('Retry images / grant access');
  await screen.findByText('Stored locally');
  await waitFor(() => expect(ctx.state().ready).toBe(true));
  const committed = await ctx.store.getAssets('a');
  ctx.view.unmount();
  expect((await ctx.store.getAssets('a')).filter(row => row.blob)).toEqual(committed.filter(row => row.blob));
});

it('preserves earlier confirmed rows when separately confirming another URL, and unchecking immediately revokes consent', async () => {
  const ctx = await setup({ markdown: image(a) + '\n' + image(b) });
  await waitFor(() => expect(ctx.state()).toBeTruthy());
  await fireEvent.click(screen.getByLabelText(`Keep external reference: ${a}`));
  await click('Confirm external references');
  expect(ctx.state().confirmedFallbacks.map(ack => ack.url)).toEqual([a]);
  await fireEvent.click(screen.getByLabelText(`Keep external reference: ${b}`));
  await click('Confirm external references');
  expect(ctx.state().confirmedFallbacks.map(ack => ack.url)).toEqual([a, b]);
  await fireEvent.click(screen.getByLabelText(`Keep external reference: ${a}`));
  expect(ctx.state().confirmedFallbacks.map(ack => ack.url)).toEqual([b]);
  expect(ctx.state().ready).toBe(false);
});

it('ignores late preparation view updates after cancel and keeps committed bytes and newer Markdown', async () => {
  const gate = deferred(); let writing;
  const ctx = await setup({ wrap: store => ({ ...store, updateDraftAssets: async (id, update) => {
    const saved = await store.updateDraftAssets(id, update);
    if (update.assets.some(row => row.descriptor.status === 'ready')) { writing = true; await gate.promise; }
    return saved;
  } }) });
  await click('Prepare images');
  await waitFor(() => expect(writing).toBe(true));
  expect((await ctx.store.getAssets('a')).filter(row => row.blob)).toHaveLength(1);
  await click('Cancel image preparation');
  await edit('newer precious body');
  gate.resolve();
  await waitFor(() => expect(ctx.state().markdown).toBe('newer precious body'));
  expect(screen.getByLabelText('Markdown').value).toBe('newer precious body');
  expect(screen.queryByText('Image preparation finished. Review each image below.')).toBeNull();
  expect((await ctx.store.getAssets('a')).filter(row => row.blob)).toHaveLength(1);
});

it('keeps the mutable editor body intact when another tab edits the persisted draft during preparation', async () => {
  const gate = deferred(); let entered;
  const ctx = await setup({ fetch: vi.fn(async () => { entered = true; await gate.promise; return new Response(png, { headers: { 'Content-Type': 'image/png' } }); }) });
  await click('Prepare images'); await waitFor(() => expect(entered).toBe(true));
  await ctx.store.saveDraft({ ...await ctx.store.getDraft('a'), markdown: 'external changed body' });
  gate.resolve();
  await screen.findByText('Image preparation finished. Review each image below.');
  expect(screen.getByLabelText('Markdown').value).toBe(image(a));
  expect(ctx.state().ready).toBe(false);
  expect(ctx.state().confirmedFallbacks).toEqual([]);
  expect(await ctx.store.getDraft('a')).toMatchObject({ markdown: 'external changed body' });
});

it('drops late view completion if cancelled during the final cache read', async () => {
  const gate = deferred(); let reads = 0, held = false;
  const ctx = await setup({ wrap: store => ({ ...store, readBundle: async id => {
    const bundle = await store.readBundle(id);
    // Mount, initial progress, ready progress, final result read.
    if (++reads === 4) { held = true; await gate.promise; }
    return bundle;
  } }) });
  await click('Prepare images'); await waitFor(() => expect(held).toBe(true));
  await click('Cancel image preparation');
  gate.resolve();
  await new Promise(resolve => setTimeout(resolve, 20));
  expect(screen.getByText('Image preparation cancelled. Already stored images are retained.')).toBeTruthy();
  expect(screen.queryByText('Image preparation finished. Review each image below.')).toBeNull();
});

it('cancels network and ignores late callbacks on cancel/unmount without closing the supplied store', async () => {
  let signal; const gate = deferred();
  const ctx = await setup({ fetch: vi.fn(async (_url, options) => { signal = options.signal; await gate.promise; return new Response(png); }) });
  await click('Prepare images');
  await waitFor(() => expect(signal).toBeTruthy());
  expect(screen.getByLabelText('Markdown').matches(':disabled')).toBe(true);
  await click('Cancel image preparation');
  expect(signal.aborted).toBe(true);
  await waitFor(() => expect(document.activeElement.classList.contains('image-summary')).toBe(true));
  await edit('newer body');
  ctx.view.unmount(); const calls = ctx.onImageState.mock.calls.length;
  gate.resolve();
  await new Promise(resolve => setTimeout(resolve, 30));
  expect(ctx.onImageState).toHaveBeenCalledTimes(calls);
  expect(await ctx.store.getDraft('a')).toBeTruthy();
});

it('resets consent on source/selected draft changes and never submits removed URL candidates', async () => {
  const ctx = await setup();
  const original = await ctx.store.getDraft('a');
  await ctx.store.saveDraft({ ...original, id: 'b', title: 'Title b', markdown: image(b) });
  await click('Refresh drafts');
  await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh drafts' }).disabled).toBe(false));
  await fireEvent.click(screen.getByLabelText(`Keep external reference: ${a}`));
  await click('Confirm external references');
  expect(ctx.state().confirmedFallbacks).toHaveLength(1);
  await click('Title b');
  await waitFor(() => expect(ctx.state().draftId).toBe('b'));
  expect(ctx.state().confirmedFallbacks).toEqual([]);
  expect(ctx.state().ready).toBe(false);
  await fireEvent.click(screen.getByLabelText(`Keep external reference: ${b}`));
  await click('Confirm external references');
  await edit('no active images');
  expect(ctx.state().confirmedFallbacks).toEqual([]);
  expect(screen.queryByLabelText(`Keep external reference: ${b}`)).toBeNull();
  ctx.view.unmount();
  const view = render(ImagePreparationHost, { store: ctx.store, draft: original });
  await fireEvent.click(screen.getByLabelText(`Keep external reference: ${a}`));
  await click('Confirm external references');
  await view.rerender({ draft: { ...original, sourceUrl: 'https://changed.example/article' } });
  expect(screen.getByLabelText(`Keep external reference: ${a}`).checked).toBe(false);
  expect(screen.getByRole('button', { name: 'Future send predicate' }).disabled).toBe(true);
});

it('reports corrupt cached ready descriptors visibly and blocks readiness without network', async () => {
  const ctx = await setup(); await click('Prepare images');
  await screen.findByText('Image preparation finished. Review each image below.');
  ctx.view.unmount();
  const [asset] = await ctx.store.getAssets('a');
  await ctx.store.saveDraft(await ctx.store.getDraft('a'), { assets: [{ id: asset.id, normalizedUrl: asset.normalizedUrl,
    blob: asset.blob, descriptor: { ...asset.descriptor, sha256: '0'.repeat(64) } }] });
  ctx.fetch.mockClear(); const onImageState = vi.fn();
  render(Editor, { store: ctx.store, initialDraftId: 'a', onImageState });
  await screen.findByText(/Cached image bytes or descriptor are invalid/);
  expect(screen.getByText('Failed')).toBeTruthy();
  expect(onImageState.mock.calls.at(-1)[0].ready).toBe(false);
  expect(ctx.fetch).not.toHaveBeenCalled();
});

it('aborts an active acquisition directly on unmount and emits no late state callbacks', async () => {
  const gate = deferred(); let signal;
  const ctx = await setup({ fetch: vi.fn(async (_url, options) => { signal = options.signal; await gate.promise; return new Response(png); }) });
  await click('Prepare images'); await waitFor(() => expect(signal).toBeTruthy());
  ctx.view.unmount(); const count = ctx.onImageState.mock.calls.length;
  expect(signal.aborted).toBe(true); gate.resolve();
  await new Promise(resolve => setTimeout(resolve, 20));
  expect(ctx.onImageState).toHaveBeenCalledTimes(count);
  expect(await ctx.store.getDraft('a')).toBeTruthy();
});
