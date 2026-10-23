import { CaptureError, validateMode, validateSourceUrl } from './errors.js';

const storageCodes = ['draft_limit', 'blob_limit', 'storage_quota', 'storage_cancelled', 'storage_unavailable', 'storage_blocked', 'storage_version_changed', 'storage_closed', 'storage_conflict', 'storage_error'];
const codes = ['invalid_request', 'too_large', ...storageCodes];
export const MAX_MARKDOWN_BYTES = 5 * 1024 ** 2;
const invalid = () => { throw new CaptureError('capture_failed'); };
export function exact(value, keys) {
  return value !== null && typeof value === 'object' && Object.getPrototypeOf(value) === Object.prototype &&
    Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key));
}
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0');
function metadata(value) {
  if (!text(value.title) || !text(value.markdown) || !value.markdown.trim() || !text(value.sourceUrl) ||
      !text(value.capturedAt) || !Number.isFinite(Date.parse(value.capturedAt))) invalid();
  validateSourceUrl(value.sourceUrl);
  return { title: value.title, markdown: value.markdown, sourceUrl: value.sourceUrl, capturedAt: value.capturedAt };
}
function imageSource(value) {
  if (!text(value)) invalid();
  if (!/^data:/i.test(value)) { validateSourceUrl(value); return value; }
  const header = /^data:image\/(png|jpeg|gif|webp)(;base64)?,/i.exec(value);
  if (!header) invalid();
  const payload = value.slice(header[0].length);
  if (!payload || (header[2] ? !/^[A-Za-z0-9+/]*={0,2}$/.test(payload) || (payload.endsWith('=') ? payload.length % 4 !== 0 : payload.length % 4 === 1) : /[^A-Za-z0-9!$&'()*+,\-./:;=?@_~%]/.test(payload) || /%(?![A-Fa-f0-9]{2})/.test(payload))) invalid();
  return value;
}
export function captureRequest(value) {
  return exact(value, ['type', 'mode']) && value.type === 'capture' && ['article', 'selection'].includes(value.mode);
}
export function popupSender(sender, runtime) {
  const origin = runtime.getURL('').replace(/\/$/, '');
  return sender?.id === runtime.id && sender.url === runtime.getURL('popup.html') &&
    sender.origin === origin && (sender.frameId === undefined || sender.frameId === 0) &&
    (sender.tab === undefined || sender.tab?.url === runtime.getURL('popup.html'));
}
export function convertedCapture(value, mode, expectedUrl) {
  try {
    if (!exact(value, ['mode', 'title', 'markdown', 'images', 'sourceUrl', 'capturedAt']) || value.mode !== mode ||
        (expectedUrl !== undefined && value.sourceUrl !== expectedUrl) || !Array.isArray(value.images)) invalid();
    return { mode, ...metadata(value), images: value.images.map(imageSource) };
  } catch { invalid(); }
}
export function failureCode(error) {
  if (codes.includes(error?.code)) return error.code;
  return error instanceof CaptureError ? new CaptureError(error.code).code : 'capture_failed';
}
export function captureResponse(value) {
  if (exact(value, ['ok', 'draftId', 'editorOpened']) && value.ok === true && text(value.draftId) &&
      value.draftId.trim() && typeof value.editorOpened === 'boolean') {
    return { ok: true, draftId: value.draftId, editorOpened: value.editorOpened };
  }
  if (value?.ok === false && (exact(value, ['ok', 'code']) || exact(value, ['ok', 'code', 'unsaved'])) &&
      typeof value.code === 'string' && (codes.includes(value.code) || new CaptureError(value.code).code === value.code)) {
    const result = { ok: false, code: value.code };
    if (Object.hasOwn(value, 'unsaved')) {
      if (![...storageCodes, 'too_large'].includes(value.code) || !exact(value.unsaved, ['title', 'markdown', 'sourceUrl', 'capturedAt'])) invalid();
      result.unsaved = metadata(value.unsaved);
    }
    return result;
  }
  invalid();
}
export function captureMessage(mode, { chrome = globalThis.chrome } = {}) {
  validateMode(mode);
  return chrome.runtime.sendMessage({ type: 'capture', mode }).then(captureResponse);
}
export function draftEditorUrl(id, chrome = globalThis.chrome) {
  const url = new URL(chrome.runtime.getURL('editor.html'));
  url.searchParams.set('draft', id);
  return url.href;
}
export function manualOpenDraftEditor(id, { chrome = globalThis.chrome } = {}) {
  return chrome.tabs.create({ url: draftEditorUrl(id, chrome) });
}
export function captureFailureMessage(code) {
  if (code === 'draft_limit') return 'The 50-draft limit was reached. Open the draft list and manually delete unwanted drafts, or download the current material.';
  if (code === 'too_large') return 'Markdown exceeds the 5 MiB UTF-8 limit. Download the current material and capture a smaller selection.';
  if (storageCodes.includes(code)) return 'Local storage failed. Existing drafts were preserved. Free profile storage or manually delete unwanted drafts, or download the current material.';
  if (code === 'invalid_request') return 'The capture request was rejected. Reopen the popup on the source page.';
  return new CaptureError(code).message;
}
