import { Readability } from '@mozilla/readability';
import { CaptureError, validateMode, validateSourceUrl } from './errors.js';

// Only detached clones receive hints. currentSrc is a runtime property that is
// otherwise lost by cloneNode/Range.cloneContents; raw lazy/srcset attrs survive.
function preserveImageHints(originals, clonedRoot) {
  const copies = [...clonedRoot.querySelectorAll('img')];
  originals.forEach((image, i) => {
    const copy = copies[i];
    if (!copy) return;
    copy.removeAttribute('data-igonotes-current-src');
    if (image.currentSrc) copy.setAttribute('data-igonotes-current-src', image.currentSrc);
  });
}

// Range.cloneContents() in a live ownerDocument can activate cloned images.
// Preserve exact text/node offsets, but perform the clone in a detached document.
function inertSelection(document, range) {
  const clone = document.cloneNode(true);
  function counterpart(node) {
    const indices = [];
    while (node !== document) {
      indices.push(Array.prototype.indexOf.call(node.parentNode.childNodes, node));
      node = node.parentNode;
    }
    return indices.reverse().reduce((parent, index) => parent.childNodes[index], clone);
  }
  const copiedRange = clone.createRange();
  copiedRange.setStart(counterpart(range.startContainer), range.startOffset);
  copiedRange.setEnd(counterpart(range.endContainer), range.endOffset);
  return copiedRange.cloneContents();
}

/** Extract only. html/baseURI/image hints are UNTRUSTED, never render or persist
 * as a draft before the separate conversion/sanitization step (task 7.2). */
export function extractPage(document, mode) {
  validateMode(mode);
  const sourceUrl = validateSourceUrl(document.URL);
  if (!['text/html', 'application/xhtml+xml'].includes(document.contentType)) throw new CaptureError('unsupported_document');
  try {
    let title = document.title;
    let html;
    let text;
    if (mode === 'article') {
      const clone = document.cloneNode(true);
      preserveImageHints([...document.querySelectorAll('img')], clone);
      // Readability normally strips these before the converter can normalize
      // them. Preserve only syntax hints; all classes remain untrusted input.
      const classesToPreserve = [...new Set([...clone.querySelectorAll('pre, code, div')]
        .flatMap(node => [...node.classList])
        .filter(name => /^(?:language-|lang-|highlight-(?:source|text)-)[a-zA-Z0-9_+-]+$/.test(name)))];
      const article = new Readability(clone, { classesToPreserve }).parse();
      if (!article?.content?.trim() || !article.textContent?.trim()) throw new CaptureError('article_empty');
      title = article.title || title;
      html = article.content;
      text = article.textContent;
    } else {
      const selection = document.getSelection();
      if (!selection || selection.rangeCount !== 1 || selection.isCollapsed) throw new CaptureError('selection_empty');
      const range = selection.getRangeAt(0);
      if (range.collapsed || range.startContainer.ownerDocument !== document || range.endContainer.ownerDocument !== document ||
          !document.contains(range.startContainer) || !document.contains(range.endContainer)) throw new CaptureError('selection_empty');
      const fragment = inertSelection(document, range);
      const images = [...document.querySelectorAll('img')].filter(image => range.intersectsNode(image));
      preserveImageHints(images, fragment);
      const container = document.implementation.createHTMLDocument('').createElement('div');
      container.appendChild(fragment);
      text = container.textContent;
      if (!text.trim() && !container.querySelector('img')) throw new CaptureError('selection_empty');
      html = container.innerHTML;
    }
    return { mode, title, html, text, baseURI: document.baseURI, sourceUrl, capturedAt: new Date().toISOString() };
  } catch (error) {
    if (error instanceof CaptureError) throw error;
    throw new CaptureError(mode === 'article' ? 'article_empty' : 'capture_failed');
  }
}
