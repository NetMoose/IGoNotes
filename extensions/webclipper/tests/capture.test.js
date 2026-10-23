import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { describe, it, expect, vi } from 'vitest';
import { Readability } from '@mozilla/readability';
import { extractPage } from '../src/capture/extract.js';

const fixture = name => readFileSync(new URL(`./fixtures/${name}.html`, import.meta.url), 'utf8');
const page = (name = 'article', options = {}) => new JSDOM(fixture(name), { url: 'https://example.test/path/page', ...options }).window.document;
function select(doc, node) {
  const range = doc.createRange(); range.selectNodeContents(node);
  const selection = doc.getSelection(); selection.removeAllRanges(); selection.addRange(range);
  return { range, selection, start: range.startContainer, end: range.endContainer, startOffset: range.startOffset, endOffset: range.endOffset };
}
function unchanged(doc, run) {
  const before = doc.documentElement.outerHTML;
  const selection = doc.getSelection();
  const ranges = Array.from({ length: selection.rangeCount }, (_, i) => selection.getRangeAt(i).cloneRange());
  try { return run(); } finally {
    expect(doc.documentElement.outerHTML).toBe(before);
    expect(selection.rangeCount).toBe(ranges.length);
    ranges.forEach((range, i) => {
      const after = selection.getRangeAt(i);
      for (const key of ['startContainer', 'endContainer', 'startOffset', 'endOffset']) expect(after[key]).toBe(range[key]);
    });
  }
}
describe('extractPage', () => {
  it('preserves recognized code-language classes through Readability for conversion', () => {
    const doc = page();
    doc.querySelector('article').insertAdjacentHTML('beforeend', fixture('conversion'));
    const result = unchanged(doc, () => extractPage(doc, 'article'));
    expect(result.html).toContain('class="language-js"');
  });
  it('extracts Readability article on a clone, with metadata and image hints, without changing DOM/selection', () => {
    const doc = page(); select(doc, doc.querySelector('h1'));
    Object.defineProperty(doc.querySelector('img'), 'currentSrc', { value: 'https://example.test/retina.png' });
    const result = unchanged(doc, () => extractPage(doc, 'article'));
    expect(result).toMatchObject({ mode: 'article', title: 'Лес и наука 🌲', sourceUrl: doc.URL, baseURI: doc.baseURI });
    expect(result.html).toContain('<h2>Наблюдения</h2>');
    expect(result.text).toContain('Scientists observe');
    expect(result.html).not.toMatch(/Navigation noise|Sidebar noise|private form value|Hidden noise/);
    expect(result.html).toContain('data-igonotes-current-src="https://example.test/retina.png"');
    expect(result.html).toContain('lazy.png');
    expect(Number.isFinite(Date.parse(result.capturedAt))).toBe(true);
    expect(JSON.parse(JSON.stringify(result))).toEqual(result);
  });
  it('clones only a selected rich Range including paragraphs and images', () => {
    const doc = page('selection'); select(doc, doc.querySelector('#chosen'));
    Object.defineProperty(doc.querySelector('img'), 'currentSrc', { value: 'https://example.test/current.png' });
    const result = unchanged(doc, () => extractPage(doc, 'selection'));
    expect(result.html).toContain('<strong>rich</strong>');
    expect(result.html).toContain('<em>paragraph</em>');
    expect(result.html).toContain('data-igonotes-current-src="https://example.test/current.png"');
    expect(result.html).not.toMatch(/Outside selection|not selected|<form|<iframe/);
    expect(result.text).toContain('Первый rich paragraph');
  });
  it('accepts an image-only selection', () => {
    const doc = page('selection'); const range = doc.createRange(); range.selectNode(doc.querySelector('img')); doc.getSelection().addRange(range);
    expect(unchanged(doc, () => extractPage(doc, 'selection')).html).toContain('<img');
  });
  it('respects partial text boundaries across paragraphs rather than cloning the ancestor', () => {
    const doc = page('selection');
    const paragraphs = doc.querySelectorAll('#chosen p');
    const range = doc.createRange();
    range.setStart(paragraphs[0].firstChild, 3);
    range.setEnd(paragraphs[1].firstChild, 3);
    doc.getSelection().addRange(range);
    const result = unchanged(doc, () => extractPage(doc, 'selection'));
    expect(result.text).toBe('вый rich paragraph link.Sec');
    expect(result.html).toContain('<strong>rich</strong>');
    expect(result.html).not.toContain('<img');
    expect(result.html).not.toContain('<em>');
  });
  it('reports a genuinely empty article and preserves the empty original document', () => {
    const doc = new JSDOM('<!doctype html><title>Empty</title><body></body>', { url: 'https://example.test/' }).window.document;
    expect(() => unchanged(doc, () => extractPage(doc, 'article'))).toThrow(/Could not extract an article/);
  });
  it.each(['absent', 'collapsed', 'whitespace', 'foreign'])('rejects %s selection without fallback or mutations', kind => {
    const doc = page('selection');
    if (kind === 'collapsed') { const range = doc.createRange(); range.setStart(doc.body, 0); doc.getSelection().addRange(range); }
    if (kind === 'whitespace') select(doc, doc.querySelector('#space'));
    if (kind === 'foreign') {
      const other = page('selection'); const range = other.createRange(); range.selectNodeContents(other.body);
      vi.spyOn(doc, 'getSelection').mockReturnValue({ rangeCount: 1, isCollapsed: false, getRangeAt: () => range });
    }
    expect(() => unchanged(doc, () => extractPage(doc, 'selection'))).toThrow(/Select non-empty content in the main page/);
  });
  it('reports null or empty Readability and never falls back to selection/full DOM', () => {
    const doc = page(); select(doc, doc.querySelector('main'));
    const spy = vi.spyOn(Readability.prototype, 'parse');
    try {
      for (const value of [null, { content: '', textContent: '' }]) {
        spy.mockReturnValueOnce(value);
        expect(() => unchanged(doc, () => extractPage(doc, 'article'))).toThrow(/Could not extract an article.*Selection/);
      }
    } finally { spy.mockRestore(); }
  });
  it.each(['chrome://settings/', 'chrome-extension://abcdefghijklmnop/page', 'extension://id/page', 'file:///private/page', 'https://user:secret@example.test/page', 'data:text/html,hello'])('rejects unsupported or credentialed source %s', url => {
    const doc = page('article', { url });
    expect(() => unchanged(doc, () => extractPage(doc, 'article'))).toThrow(/Only HTTP/);
  });
  it('rejects PDF documents and invalid modes', () => {
    const doc = page(); Object.defineProperty(doc, 'contentType', { value: 'application/pdf' });
    expect(() => extractPage(doc, 'article')).toThrow(/PDF/);
    expect(() => extractPage(page(), 'unknown')).toThrow(/capture mode/);
  });
});
