import { extractPage } from './capture/extract.js';
import { CaptureError } from './capture/errors.js';

// Classic IIFE export lives ONLY in Chrome's ISOLATED world. Re-injection
// replaces this stateless binding; no listeners, messages or retained captures.
export default {
  capture(mode, expectedUrl) {
    try {
      if (document.URL !== expectedUrl) throw new CaptureError('page_changed');
      return { ok: true, value: extractPage(document, mode) };
    } catch (error) {
      return { ok: false, code: error instanceof CaptureError ? error.code : 'capture_failed' };
    }
  },
};
