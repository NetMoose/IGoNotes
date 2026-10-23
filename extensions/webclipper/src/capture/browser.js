import { CaptureError, validateMode, validateSourceUrl } from './errors.js';
import { convertedCapture, exact } from './messages.js';

/** Trusted extension contexts only; no settings/transport/storage dependency.
 * Call only from an explicit user action. activeTab comes from opening action. */
export async function captureActiveTab(mode, { chrome = globalThis.chrome } = {}) {
  return captureTab(mode, chrome, false);
}

export async function captureActiveMarkdownTab(mode, { chrome = globalThis.chrome } = {}) {
  return captureTab(mode, chrome, true);
}

async function captureTab(mode, chrome, markdown) {
  validateMode(mode);
  try {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!Number.isSafeInteger(tab?.id) || tab.id < 0) throw new CaptureError('capture_failed');
    const sourceUrl = validateSourceUrl(tab.url);
    const target = { tabId: tab.id, frameIds: [0] };
    const installed = await chrome.scripting.executeScript({ target, world: 'ISOLATED', files: ['content-script.js'] });
    if (installed.length !== 1 || installed[0].frameId !== 0 || !installed[0].documentId) throw new CaptureError('capture_failed');
    const results = await chrome.scripting.executeScript({
      target, world: 'ISOLATED', args: [mode, sourceUrl],
      // Serialized by Chrome: no closure bindings or page-world bridge.
      func: markdown
        ? (captureMode, expectedUrl) => globalThis.IGoNotesContentScript.captureMarkdown(captureMode, expectedUrl)
        : (captureMode, expectedUrl) => globalThis.IGoNotesContentScript.capture(captureMode, expectedUrl),
    });
    if (results.length !== 1 || results[0].frameId !== 0 || results[0].documentId !== installed[0].documentId) throw new CaptureError('page_changed');
    const current = await chrome.tabs.get(tab.id);
    if (current.pendingUrl || current.url !== tab.url) throw new CaptureError('page_changed');
    const response = results[0].result;
    if (exact(response, ['ok', 'code']) && response.ok === false) throw new CaptureError(response.code);
    if (!exact(response, ['ok', 'value']) || response.ok !== true) throw new CaptureError('capture_failed');
    const value = response?.value;
    if (markdown) return convertedCapture(value, mode, sourceUrl);
    if (!exact(value, ['mode', 'title', 'html', 'text', 'baseURI', 'sourceUrl', 'capturedAt']) || value.mode !== mode || value.sourceUrl !== sourceUrl ||
        !['title', 'html', 'text', 'baseURI', 'capturedAt'].every(key => typeof value[key] === 'string') ||
        !value.html.trim() || !Number.isFinite(Date.parse(value.capturedAt))) throw new CaptureError('capture_failed');
    return { mode, title: value.title, html: value.html, text: value.text, baseURI: value.baseURI, sourceUrl, capturedAt: value.capturedAt };
  } catch (error) {
    if (error instanceof CaptureError) throw error;
    throw new CaptureError('capture_failed');
  }
}
