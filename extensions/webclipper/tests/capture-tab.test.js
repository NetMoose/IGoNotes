import { it, expect, vi } from 'vitest';
import { captureActiveTab, captureActiveMarkdownTab } from '../src/capture/browser.js';

const sourceUrl = 'https://example.test/article';
const result = { mode: 'article', sourceUrl, baseURI: sourceUrl, capturedAt: '2026-10-23T12:00:00.000Z', title: 'Title', html: '<p>Text</p>', text: 'Text' };
const converted = { mode: 'article', sourceUrl, capturedAt: result.capturedAt, title: 'Title', markdown: '# Text', images: [] };
function setup() {
  const chrome = { tabs: { query: vi.fn(async () => [{ id: 17, url: sourceUrl }]), get: vi.fn(async () => ({ id: 17, url: sourceUrl })) }, scripting: { executeScript: vi.fn().mockResolvedValueOnce([{ frameId: 0, documentId: 'doc' }]).mockResolvedValueOnce([{ frameId: 0, documentId: 'doc', result: { ok: true, value: result } }]) } };
  return chrome;
}
it('calls only captureMarkdown in ISOLATED frame 0 and returns strict converted data', async () => {
  const chrome = setup();
  chrome.scripting.executeScript.mockReset().mockResolvedValueOnce([{ frameId: 0, documentId: 'doc' }]).mockResolvedValueOnce([{ frameId: 0, documentId: 'doc', result: { ok: true, value: converted } }]);
  expect(await captureActiveMarkdownTab('article', { chrome })).toEqual(converted);
  const call = chrome.scripting.executeScript.mock.calls[1][0];
  const captureMarkdown = vi.fn(); const capture = vi.fn();
  vi.stubGlobal('IGoNotesContentScript', { captureMarkdown, capture });
  try { call.func(...call.args); } finally { vi.unstubAllGlobals(); }
  expect(captureMarkdown).toHaveBeenCalledExactlyOnceWith('article', sourceUrl); expect(capture).not.toHaveBeenCalled();
  expect(call.target).toEqual({ tabId: 17, frameIds: [0] }); expect(call.world).toBe('ISOLATED');
});
it.each([
  { ok: true, value: { ...converted, html: '<p>raw</p>' } },
  { ok: true, value: { ...converted, sourceUrl: 'https://other.test/' } },
  { ok: true, value: converted, extra: 'forged' },
])('rejects forged converted response %j', async response => {
  const chrome = setup();
  chrome.scripting.executeScript.mockReset().mockResolvedValueOnce([{ frameId: 0, documentId: 'doc' }]).mockResolvedValueOnce([{ frameId: 0, documentId: 'doc', result: response }]);
  await expect(captureActiveMarkdownTab('article', { chrome })).rejects.toThrow(/Could not capture/);
});
it.each(['article', 'selection'])('queries the active tab on action and injects only ISOLATED main frame with %s args', async mode => {
  const chrome = setup();
  chrome.scripting.executeScript.mockReset().mockResolvedValueOnce([{ frameId: 0, documentId: 'doc' }]).mockResolvedValueOnce([{ frameId: 0, documentId: 'doc', result: { ok: true, value: { ...result, mode } } }]);
  expect(await captureActiveTab(mode, { chrome })).toEqual({ ...result, mode });
  expect(chrome.tabs.query).toHaveBeenCalledWith({ active: true, currentWindow: true });
  const [file, call] = chrome.scripting.executeScript.mock.calls.map(([value]) => value);
  expect(file).toEqual({ target: { tabId: 17, frameIds: [0] }, world: 'ISOLATED', files: ['content-script.js'] });
  expect(call).toMatchObject({ target: file.target, world: 'ISOLATED', args: [mode, sourceUrl] });
  expect(typeof call.func).toBe('function');
  expect(chrome.tabs.get).toHaveBeenCalledWith(17);
});
it.each(['chrome://settings', 'file:///private', 'https://user:secret@example.test/', undefined])('rejects restricted URL before injection', async url => {
  const chrome = setup(); chrome.tabs.query.mockResolvedValue([{ id: 17, url }]);
  await expect(captureActiveTab('article', { chrome })).rejects.toThrow(/Only HTTP/);
  expect(chrome.scripting.executeScript).not.toHaveBeenCalled();
});
it.each([-1, undefined, 1.5])('rejects invalid tab id %s', async id => {
  const chrome = setup(); chrome.tabs.query.mockResolvedValue([{ id, url: sourceUrl }]);
  await expect(captureActiveTab('article', { chrome })).rejects.toThrow(/active page/);
  expect(chrome.scripting.executeScript).not.toHaveBeenCalled();
});
it.each(['denied', 'navigated', 'document', 'empty', 'article'])('shows fixed friendly errors for %s without leaking native errors or invoking fallback', async kind => {
  const chrome = setup();
  if (kind === 'denied') chrome.scripting.executeScript.mockReset().mockRejectedValue(new Error('secret https://private.test/ <html>'));
  if (kind === 'navigated') chrome.tabs.get.mockResolvedValue({ id: 17, url: 'https://other.test/' });
  if (kind === 'document') chrome.scripting.executeScript.mockReset().mockResolvedValueOnce([{ frameId: 0, documentId: 'a' }]).mockResolvedValueOnce([{ frameId: 0, documentId: 'b', result: { ok: true, value: result } }]);
  if (kind === 'empty') chrome.scripting.executeScript.mockReset().mockResolvedValue([]);
  if (kind === 'article') chrome.scripting.executeScript.mockReset().mockResolvedValueOnce([{ frameId: 0, documentId: 'doc' }]).mockResolvedValueOnce([{ frameId: 0, documentId: 'doc', result: { ok: false, code: 'article_empty' } }]);
  const error = await captureActiveTab('article', { chrome }).catch(error => error);
  expect(error).toBeInstanceOf(Error);
  expect(error.message).toMatch(/capture|changed|article/i);
  expect(error.message).not.toMatch(/secret|private\.test|<html>/);
  expect(chrome.scripting.executeScript.mock.calls.every(([call]) => !call.args || call.args[0] === 'article')).toBe(true);
});
it('never reads settings, credentials, storage or network APIs', async () => {
  const chrome = setup();
  for (const key of ['storage', 'runtime', 'permissions']) Object.defineProperty(chrome, key, { get() { throw new Error(`Unexpected ${key}`); } });
  expect((await captureActiveTab('article', { chrome })).title).toBe('Title');
});
it.each([
  null, { ok: true, value: {} }, { ok: true, value: { ...result, html: '' } },
  { ok: true, value: { ...result, sourceUrl: 'https://different.test/' } },
  { ok: true, value: { ...result, token: 'forged' } },
  { ok: false, code: 'https://secret.test/<html>' }, { ok: false, code: 'toString' },
])('rejects malformed or unknown result with a static message', async response => {
  const chrome = setup();
  chrome.scripting.executeScript.mockReset().mockResolvedValueOnce([{ frameId: 0, documentId: 'doc' }]).mockResolvedValueOnce([{ frameId: 0, documentId: 'doc', result: response }]);
  await expect(captureActiveTab('article', { chrome })).rejects.toThrow('Could not capture the active page. Check page access and try again.');
});
