import { captureActiveMarkdownTab } from './browser.js';
import { createDraftStore } from '../storage/drafts.js';
import { captureRequest, popupSender, convertedCapture, failureCode, draftEditorUrl, MAX_MARKDOWN_BYTES } from './messages.js';

// This module never reads settings/credentials or performs fetch. Every capture
// owns its store handle, and popup teardown cannot cancel an accepted operation.
export function registerCaptureCoordinator({ chrome = globalThis.chrome, protect, capture = captureActiveMarkdownTab, createStore = createDraftStore } = {}) {
  async function run(mode) {
    let material;
    try {
      await protect();
      const result = convertedCapture(await capture(mode, { chrome }), mode);
      const { title, markdown, sourceUrl, capturedAt } = result;
      material = { title, markdown, sourceUrl, capturedAt };
      if (new TextEncoder().encode(markdown).byteLength > MAX_MARKDOWN_BYTES) return { ok: false, code: 'too_large', unsaved: material };
      const id = crypto.randomUUID();
      const assets = [...new Set(result.images)].map(normalizedUrl => ({ id: crypto.randomUUID(), normalizedUrl, descriptor: { status: 'pending' }, blob: null }));
      const store = createStore();
      try {
        await store.saveDraft({ id, ...material, filename: 'clip.md', base: null, parent: '', destinationRevision: null, status: 'draft' }, { assets });
      } finally { store.close(); }
      // Commit is definitive. Tab/channel errors cannot turn it into a capture
      // failure, delete the draft, or trigger a second capture/save.
      let editorOpened = false;
      try { await chrome.tabs.create({ url: draftEditorUrl(id, chrome) }); editorOpened = true; } catch { /* manual popup fallback */ }
      return { ok: true, draftId: id, editorOpened };
    } catch (error) {
      const code = failureCode(error);
      const recoverable = material && (code.startsWith('storage_') || ['draft_limit', 'blob_limit'].includes(code));
      return { ok: false, code, ...(recoverable ? { unsaved: material } : {}) };
    }
  }
  // Deliberately NOT async: Chrome 120 needs literal true + sendResponse.
  chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
    const reply = value => { try { sendResponse(value); } catch { /* popup closed; committed data remains */ } };
    if (!captureRequest(message) || !popupSender(sender, chrome.runtime)) {
      reply({ ok: false, code: 'invalid_request' });
      return false;
    }
    void run(message.mode).then(reply);
    return true;
  });
}
