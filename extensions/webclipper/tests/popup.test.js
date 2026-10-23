// @vitest-environment jsdom
import { afterEach, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, cleanup } from '@testing-library/svelte';
import Popup from '../src/ui/Popup.svelte';
import { CaptureError } from '../src/capture/errors.js';

afterEach(cleanup);
it.each(['Article', 'Selection'])('requests explicit %s once while busy and acknowledges only persisted draft', async mode => {
  let resolve; const capture = vi.fn(() => new Promise(yes => { resolve = yes; }));
  render(Popup, { capture }); expect(capture).not.toHaveBeenCalled();
  await fireEvent.click(screen.getByRole('button', { name: mode, exact: true }));
  await fireEvent.click(screen.getByRole('button', { name: mode, exact: true }));
  expect(capture).toHaveBeenCalledExactlyOnceWith(mode.toLowerCase());
  expect(screen.getByRole('button', { name: 'Article', exact: true }).disabled).toBe(true);
  expect(screen.getByRole('button', { name: 'Selection', exact: true }).disabled).toBe(true);
  expect(screen.getByRole('status').textContent).toContain('Capturing');
  resolve({ ok: true, draftId: 'draft-id', editorOpened: true });
  await screen.findByText(/Saved locally/);
  expect(screen.getByRole('status').textContent).toContain('editor opened');
  expect(screen.queryByText(/preview only/)).toBeNull();
});
it('shows a friendly capture error and never silently switches mode', async () => {
  const capture = vi.fn().mockRejectedValue(new Error('private URL <html>'));
  render(Popup, { capture });
  await fireEvent.click(screen.getByRole('button', { name: 'Article', exact: true }));
  expect((await screen.findByRole('alert')).textContent).not.toMatch(/private|<html>/);
  expect(capture).toHaveBeenCalledExactlyOnceWith('article');
});
it('offers Selection explicitly after an article failure', async () => {
  const capture = vi.fn().mockResolvedValueOnce({ ok: false, code: 'article_empty' })
    .mockResolvedValueOnce({ ok: true, draftId: 'id', editorOpened: true });
  render(Popup, { capture });
  await fireEvent.click(screen.getByRole('button', { name: 'Article', exact: true }));
  expect((await screen.findByRole('alert')).textContent).toContain('choose Selection');
  await fireEvent.click(screen.getByRole('button', { name: 'Selection', exact: true }));
  await screen.findByText(/Saved locally/);
  expect(screen.queryByRole('alert')).toBeNull();
  expect(capture.mock.calls).toEqual([['article'], ['selection']]);
});
it('saved draft with failed tab open offers manual open of same ID, never another capture', async () => {
  const capture = vi.fn(async () => ({ ok: true, draftId: 'id & +', editorOpened: false }));
  const openEditor = vi.fn(async () => {});
  render(Popup, { capture, openEditor });
  await fireEvent.click(screen.getByRole('button', { name: 'Article', exact: true }));
  await screen.findByText(/Saved locally/);
  expect((await screen.findByRole('alert')).textContent).toContain('could not open');
  await fireEvent.click(screen.getByRole('button', { name: 'Open saved draft' }));
  expect(openEditor).toHaveBeenCalledExactlyOnceWith('id & +');
  expect(capture).toHaveBeenCalledOnce();
});
it.each(['storage_quota', 'draft_limit'])('%s explicitly keeps unsaved material downloadable and offers manual space management', async code => {
  const unsaved = { title: '<img src=x>', markdown: '# Keep current 🌍', sourceUrl: 'https://example.test/', capturedAt: '2026-10-23T00:00:00Z' };
  const capture = vi.fn(async () => ({ ok: false, code, unsaved }));
  const download = vi.fn();
  render(Popup, { capture, download });
  await fireEvent.click(screen.getByRole('button', { name: 'Selection', exact: true }));
  await screen.findByText(/Not saved.*closing.*loses/i);
  expect(screen.queryByText(/Saved locally/)).toBeNull();
  expect(screen.getByRole('link', { name: 'Open draft editor' })).toBeTruthy();
  await fireEvent.click(screen.getByRole('button', { name: 'Download current Markdown' }));
  expect(download).toHaveBeenCalledExactlyOnceWith(unsaved.markdown, 'clip.md');
  expect(document.querySelector('main img, main script')).toBeNull();
});
it.each([{}, { ok: true, draftId: 'id', editorOpened: true, html: '<script>bad</script>' }, { ok: false, code: 'secret native message' }])('rejects malformed worker response without presenting saved', async response => {
  render(Popup, { capture: async () => response });
  await fireEvent.click(screen.getByRole('button', { name: 'Article', exact: true }));
  expect((await screen.findByRole('alert')).textContent).toBe(new CaptureError('capture_failed').message);
  expect(screen.queryByText(/Saved locally/)).toBeNull();
});
it.each([false, true])('late resolution after unmount has no DOM updates or fallback side effects (failure=%s)', async failure => {
  let resolve; const capture = vi.fn(() => new Promise(yes => { resolve = yes; }));
  const openEditor = vi.fn(); const download = vi.fn();
  const view = render(Popup, { capture, openEditor, download });
  await fireEvent.click(screen.getByRole('button', { name: 'Article', exact: true }));
  view.unmount();
  resolve(failure ? { ok: false, code: 'storage_quota' } : { ok: true, draftId: 'id', editorOpened: false });
  await new Promise(yes => setTimeout(yes, 0));
  expect(view.container.textContent).toBe('');
  expect(openEditor).not.toHaveBeenCalled(); expect(download).not.toHaveBeenCalled();
});
