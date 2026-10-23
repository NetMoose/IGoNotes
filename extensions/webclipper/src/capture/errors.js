const messages = {
  unsupported_source: 'Only HTTP(S) pages without URL credentials can be captured.',
  unsupported_document: 'PDF and non-HTML documents are not supported.',
  invalid_mode: 'Choose an Article or Selection capture mode.',
  selection_empty: 'Select non-empty content in the main page, then try Selection again.',
  article_empty: 'Could not extract an article. You can select content and choose Selection.',
  page_changed: 'The active page changed. Open the popup on the source page and try again.',
  capture_failed: 'Could not capture the active page. Check page access and try again.',
};

export class CaptureError extends Error {
  constructor(code) {
    const known = typeof code === 'string' && Object.hasOwn(messages, code);
    super(known ? messages[code] : messages.capture_failed);
    this.name = 'CaptureError';
    this.code = known ? code : 'capture_failed';
  }
}

export function validateMode(mode) {
  if (mode !== 'article' && mode !== 'selection') throw new CaptureError('invalid_mode');
}

export function validateSourceUrl(value) {
  try {
    const url = new URL(value);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || !url.hostname) throw new Error();
    return url.href;
  } catch { throw new CaptureError('unsupported_source'); }
}
