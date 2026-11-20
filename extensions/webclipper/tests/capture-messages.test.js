import { expect, it, vi } from 'vitest';
import { captureMessage, captureResponse, manualOpenDraftEditor } from '../src/capture/messages.js';

it('sends only fixed mode command and validates projected response', async () => {
  const chrome = { runtime: { sendMessage: vi.fn(async () => ({ ok: true, draftId: 'id', editorOpened: true })) } };
  expect(await captureMessage('selection', { chrome })).toEqual({ ok: true, draftId: 'id', editorOpened: true });
  expect(chrome.runtime.sendMessage).toHaveBeenCalledExactlyOnceWith({ type: 'capture', mode: 'selection' });
  expect(() => captureMessage('fetch', { chrome })).toThrow();
});
it.each([
  null, { ok: true, draftId: 'id', editorOpened: true, token: 'secret' },
  { ok: true, draftId: 'id', editorOpened: 'yes' },
  { ok: false, code: 'constructor' },
  { ok: false, code: 'storage_quota', unsaved: { markdown: 'body', html: '<p>body</p>' } },
])('rejects malformed response %j', value => expect(() => captureResponse(value)).toThrow());
it('manual editor fallback URL encodes only ID', async () => {
  const chrome = { runtime: { getURL: path => `chrome-extension://own/${path}` }, tabs: { create: vi.fn(async () => {}) } };
  await manualOpenDraftEditor('id &?+#', { chrome });
  const url = new URL(chrome.tabs.create.mock.calls[0][0].url);
  expect(url.pathname).toBe('/editor.html'); expect([...url.searchParams]).toEqual([['draft', 'id &?+#']]); expect(url.hash).toBe('');
});
