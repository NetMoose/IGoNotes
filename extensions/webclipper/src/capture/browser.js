import { CaptureError, validateMode, validateSourceUrl } from './errors.js';

/** Trusted extension contexts only; no settings/transport/storage dependency.
 * Call only from an explicit user action. activeTab comes from opening action. */
export async function captureActiveTab(mode, { chrome = globalThis.chrome } = {}) {
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
      func: (captureMode, expectedUrl) => globalThis.IGoNotesContentScript.capture(captureMode, expectedUrl),
    });
    if (results.length !== 1 || results[0].frameId !== 0 || results[0].documentId !== installed[0].documentId) throw new CaptureError('page_changed');
    const current = await chrome.tabs.get(tab.id);
    if (current.pendingUrl || current.url !== tab.url) throw new CaptureError('page_changed');
    const response = results[0].result;
    if (response?.ok === false) throw new CaptureError(response.code);
    const value = response?.value;
    if (response?.ok !== true || !value || value.mode !== mode || value.sourceUrl !== sourceUrl ||
        !['title', 'html', 'text', 'baseURI', 'capturedAt'].every(key => typeof value[key] === 'string') ||
        !value.html.trim() || !Number.isFinite(Date.parse(value.capturedAt))) throw new CaptureError('capture_failed');
    return value;
  } catch (error) {
    if (error instanceof CaptureError) throw error;
    throw new CaptureError('capture_failed');
  }
}
