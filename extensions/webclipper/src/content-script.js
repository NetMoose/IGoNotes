import { extractPage } from './capture/extract.js';
import { CaptureError } from './capture/errors.js';
import { convertCapture } from './capture/convert.js';

function capture(mode, expectedUrl, convert) {
  try {
    if (document.URL !== expectedUrl) throw new CaptureError('page_changed');
    const extracted = extractPage(document, mode);
    return { ok: true, value: convert ? convertCapture(extracted, { document }) : extracted };
  } catch (error) {
    return { ok: false, code: error instanceof CaptureError ? error.code : 'capture_failed' };
  }
}

// Classic IIFE export lives ONLY in Chrome's ISOLATED world. Re-injection
// replaces this stateless binding; no listeners, messages or retained captures.
export default {
  capture(mode, expectedUrl) {
    return capture(mode, expectedUrl, false);
  },
  // Fixed data-only entry for the durable worker capture coordinator.
  captureMarkdown(mode, expectedUrl) {
    return capture(mode, expectedUrl, true);
  },
};
