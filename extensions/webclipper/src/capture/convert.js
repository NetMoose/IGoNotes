import TurndownService from 'turndown';
import { gfm } from 'turndown-plugin-gfm';
import { CaptureError, validateMode, validateSourceUrl } from './errors.js';

const HTML = 'http://www.w3.org/1999/xhtml';
const allowed = new Set('p div section article main header footer aside nav figure figcaption h1 h2 h3 h4 h5 h6 br hr strong b em i del s strike blockquote ul ol li dl dt dd pre code a img table caption thead tbody tfoot tr th td span'.split(' '));
const dropped = new Set('script style form input textarea button select option optgroup fieldset legend output datalist iframe frame frameset noframes object embed applet param audio video source track canvas svg math base meta link template noscript head title'.split(' '));
const htmlText = value => value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const singleLine = value => value.replace(/[\u0000-\u0020\u007f]+/g, ' ');
const codeText = node => node.getAttribute('data-igonotes-code-text') ?? node.textContent;
const codeFenceLength = text => Math.max(1, ...[...text.matchAll(/`+/g)].map(match => match[0].length + 1));

function httpUrl(value, baseURI) {
  if (typeof value !== 'string' || !value.trim()) return null;
  try {
    // URL's parser handles entity-decoded controls/schemes. Never percent-decode
    // first: a percent-encoded colon denotes a relative path, not a scheme.
    const url = new URL(value, baseURI);
    return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password ? url.href : null;
  } catch { return null; }
}

function imageUrl(value, baseURI) {
  if (typeof value !== 'string') return null;
  if (!/^data:/i.test(value)) return httpUrl(value, baseURI);
  const header = /^data:image\/(png|jpeg|gif|webp)(;base64)?,/i.exec(value);
  if (!header) return null;
  const payload = value.slice(header[0].length);
  if (!payload) return null;
  if (header[2]) {
    if (!/^[A-Za-z0-9+/]*={0,2}$/.test(payload) || (payload.includes('=') ? payload.length % 4 !== 0 : payload.length % 4 === 1)) return null;
  } else if (!/^(?:[A-Za-z0-9!$&'()*+,;=:@/?._~-]|%[0-9a-f]{2})+$/i.test(payload)) return null;
  // Shape only, NOT a declaration that bytes are decoded/validated/acquired.
  return value;
}

function srcsetCandidates(value) {
  const candidates = [];
  let rest = value.trim();
  while (rest) {
    rest = rest.replace(/^[\s,]+/, '');
    const token = /^\S+/.exec(rest)?.[0];
    if (!token) break;
    rest = rest.slice(token.length);
    if (token.endsWith(',')) { candidates.push({ url: token.replace(/,+$/, ''), size: 1 }); continue; }
    const end = rest.indexOf(',');
    const descriptor = (end < 0 ? rest : rest.slice(0, end)).trim();
    rest = end < 0 ? '' : rest.slice(end + 1);
    if (!descriptor || /^(?:\d+(?:\.\d+)?x|\d+w)$/.test(descriptor)) candidates.push({ url: token, size: parseFloat(descriptor) || 1 });
  }
  return candidates.sort((a, b) => b.size - a.size).map(candidate => candidate.url);
}

function imageSource(node, baseURI) {
  const attr = name => node.getAttribute(name) || '';
  const placeholder = value => /(?:^|[/_.-])(placeholder|spacer|blank|transparent|loading|pixel)(?:[/_.-]|$)/i.test(value) ||
    /^data:image\/(?:gif;base64,R0lGODlhAQAB|png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB)/i.test(value);
  const current = attr('data-igonotes-current-src');
  const lazy = [attr('data-src'), attr('data-original'), attr('data-lazy-src'), ...srcsetCandidates(attr('data-srcset'))];
  const responsive = srcsetCandidates(attr('srcset'));
  const src = attr('src');
  const candidates = [current, ...lazy, ...responsive, src];
  for (const candidate of candidates) {
    if (placeholder(candidate)) continue;
    const url = imageUrl(candidate, baseURI);
    if (url) return url;
  }
  // Preserve a placeholder only when no usable real source exists.
  for (const candidate of candidates) {
    const url = imageUrl(candidate, baseURI);
    if (url) return url;
  }
  return null;
}

function rebuild(fragment, baseURI) {
  const doc = fragment.ownerDocument; // template contents have no browsing context/registry
  const root = doc.createElement('div');
  const images = new Set();
  function copy(node, parent) {
    if (node.nodeType === 3) { parent.appendChild(doc.createTextNode(node.data)); return; }
    if (node.nodeType !== 1 || node.namespaceURI !== HTML) return;
    const tag = node.localName;
    // The only permitted control is a value-free, inert GFM list checkbox.
    if (tag === 'input' && node.getAttribute('type')?.toLowerCase() === 'checkbox' && parent.localName === 'li') {
      const checkbox = doc.createElement('input');
      checkbox.setAttribute('type', 'checkbox');
      if (node.hasAttribute('checked')) checkbox.setAttribute('checked', '');
      parent.appendChild(checkbox);
      return;
    }
    if (dropped.has(tag)) return;
    // These wrappers emit no Markdown delimiter. Unwrap before code-run
    // normalization so serialization cannot hide adjacent code boundaries.
    if (!allowed.has(tag) || tag === 'span' || (tag === 'a' && !httpUrl(node.getAttribute('href'), baseURI))) {
      for (const child of node.childNodes) copy(child, parent);
      return;
    }
    const clean = doc.createElement(tag);
    if (tag === 'a') {
      const href = httpUrl(node.getAttribute('href'), baseURI);
      if (href) clean.setAttribute('href', href);
    }
    if (tag === 'img') {
      const src = imageSource(node, baseURI);
      if (!src) { parent.appendChild(doc.createTextNode(node.getAttribute('alt') || '')); return; }
      clean.setAttribute('src', src);
      clean.setAttribute('alt', node.getAttribute('alt') || '');
      images.add(src);
    }
    if ((tag === 'a' || tag === 'img') && node.hasAttribute('title')) clean.setAttribute('title', node.getAttribute('title'));
    if (tag === 'ol' && /^\d+$/.test(node.getAttribute('start') || '')) clean.setAttribute('start', node.getAttribute('start'));
    if (tag === 'code' || tag === 'pre' || tag === 'div') {
      const language = /(?:^|\s)(?:language-|lang-|highlight-(?:source|text)-)([a-zA-Z0-9_+-]+)(?=\s|$)/.exec(node.getAttribute('class') || '')?.[1];
      if (language) clean.setAttribute('class', `language-${language.toLowerCase()}`);
    }
    parent.appendChild(clean);
    for (const child of node.childNodes) copy(child, clean);
  }
  for (const node of fragment.childNodes) copy(node, root);
  coalesceCodeRuns(root);
  prepareInlineCode(root);
  prepareFormattingEdges(root);
  return { root, images: [...images] };
}

function coalesceCodeRuns(parent) {
  // Work only on the rebuilt inert tree, before Turndown clones/serializes it.
  // PRE/CODE payloads are already literal text to the code emitters; do not
  // reinterpret their inner markup or touch block-fence boundaries.
  if (parent.localName === 'pre' || parent.localName === 'code') return;
  for (const child of [...parent.children]) {
    coalesceCodeRuns(child);
    // Turndown's blank rule also unwraps whitespace-only formatting, including
    // wrappers containing an all-space CODE. Expose those boundaries here too.
    if (['em', 'i', 'strong', 'b', 'del', 's', 'strike'].includes(child.localName) &&
        /^\s*$/.test(child.textContent) && !child.querySelector('a, img, br, hr, input, pre, table')) {
      child.replaceWith(...child.childNodes);
    } else if (child.localName === 'code' && !child.textContent) child.remove();
  }
  parent.normalize(); // remove empty text/alt fallbacks, not whitespace
  for (const child of parent.children) {
    if (child.localName !== 'code') continue;
    // Choose one delimiter for the concatenated literal payload. Separately
    // safe code spans are NOT compositional: touching backtick fences merge.
    while (child.nextSibling?.nodeType === 1 && child.nextSibling.localName === 'code') {
      const next = child.nextSibling;
      child.append(...next.childNodes);
      next.remove();
    }
  }
}

function prepareInlineCode(root) {
  let previousFence = 0;
  const codes = [...root.querySelectorAll('code')].filter(code => !code.parentElement.closest('pre, code'));
  for (const code of codes) {
    const text = code.textContent;
    const minimum = codeFenceLength(text);
    // Marked's emphasis mask cannot mask a span containing backticks in full.
    // Equal neighboring fences can then mask the formatting between spans as
    // code. Alternate only when necessary; lengths stay <= payload maximum + 2.
    const length = minimum === previousFence ? minimum + 1 : minimum;
    previousFence = length;
    code.setAttribute('data-igonotes-code-fence', String(length));
    code.setAttribute('data-igonotes-code-text', text);
    // A controlled nonblank stand-in prevents Turndown from moving literal
    // code-edge spaces outside enclosing EM/STRONG wrappers. Attributes survive
    // its clones; the emitter reads only our original payload, never this text.
    code.textContent = 'code';
  }
}

function prepareFormattingEdges(root) {
  function encodeEdge(text, fromEnd) {
    if (text?.nodeType !== 3) return;
    const characters = [...text.data];
    const character = fromEnd ? characters.pop() : characters.shift();
    if (!character || /\s/u.test(character)) return;
    const entity = root.ownerDocument.createElement('span');
    entity.setAttribute('data-igonotes-text-entity', String(character.codePointAt(0)));
    entity.textContent = character;
    text.data = characters.join('');
    if (fromEnd) text.after(entity); else text.before(entity);
  }
  for (const format of root.querySelectorAll('em, i, strong, b')) {
    if (!format.querySelector('code') || format.closest('pre, code') || format.querySelector('pre') ||
        [...format.querySelectorAll('code')].some(code => /[\r\n]/.test(codeText(code)))) continue;
    // A delimiter next to a backtick is punctuation-flanked. Direct alphanumeric
    // prose can therefore make even * or ** invalid. Encode only the adjacent
    // prose character as a CommonMark numeric entity: identical rendered text,
    // no separator/HTML, and lexical punctuation on both delimiter boundaries.
    encodeEdge(format.previousSibling, true);
    encodeEdge(format.nextSibling, false);
  }
}

function markdownService() {
  const fencedCode = node => {
    const code = node.localName === 'code' ? codeText(node) : node.textContent;
    const language = (node.querySelector('code')?.getAttribute('class') || node.getAttribute('class') || node.parentNode.getAttribute('class') || '').replace(/^language-/, '');
    const delimiter = '`'.repeat(Math.max(3, ...[...code.matchAll(/`+/g)].map(match => match[0].length + 1)));
    return `\n\n${delimiter}${/^[a-z0-9_+-]+$/.test(language) ? language : ''}\n${code}${code.endsWith('\n') ? '' : '\n'}${delimiter}\n\n`;
  };
  const inlineCode = node => {
    const text = codeText(node);
    // CommonMark normalizes line endings inside code spans to spaces. Use a
    // block fence for genuine multiline code instead of changing its payload.
    if (/[\r\n]/.test(text)) return fencedCode(node);
    const delimiter = '`'.repeat(Number(node.getAttribute('data-igonotes-code-fence')) || codeFenceLength(text));
    // Code spans strip exactly one edge space when both edges are spaces and
    // the payload is not entirely ASCII spaces. Add one extra pair to retain
    // those original edges; all-space spans must receive no padding.
    const pad = /^`|`$/.test(text) || (text.startsWith(' ') && text.endsWith(' ') && /[^ ]/.test(text)) ? ' ' : '';
    return text ? `${delimiter}${pad}${text}${pad}${delimiter}` : '';
  };
  const containsBlockCode = node => node.querySelector('pre') || [...node.querySelectorAll('code')].some(code => /[\r\n]/.test(codeText(code)));
  const service = new TurndownService({
    headingStyle: 'atx', codeBlockStyle: 'fenced', bulletListMarker: '-', emDelimiter: '_',
    // Preserve CODE payloads before custom rules run, including cloned table
    // cells. Ordinary prose still uses Turndown's whitespace normalization.
    preformattedCode: true,
    // Blank rules precede added rules, including for all-space CODE elements.
    blankReplacement: (content, node) => node.nodeName === 'PRE' ? fencedCode(node) : node.nodeName === 'CODE' ? inlineCode(node) : node.isBlock ? `\n\n${content}\n\n` : content,
  });
  service.use(gfm);
  const escape = service.escape.bind(service);
  // Text nodes only; code uses its original literal text instead. Encoding &
  // first prevents double entity decoding into HTML-looking prose.
  service.escape = value => escape(htmlText(value)).replace(/[!|~]/g, '\\$&');
  service.addRule('formatBoundaryEntity', {
    // Created only after allowlist rebuilding; source spans/attributes are
    // unwrapped, so an untrusted attribute can never enter this emitter.
    filter: node => node.localName === 'span' && node.hasAttribute('data-igonotes-text-entity'),
    replacement: (_content, node) => `&#${node.getAttribute('data-igonotes-text-entity')};`,
  });
  const destination = value => value.replace(/[\s<>()[\]\\"`|]/g, character => encodeURIComponent(character).replace(/[()]/g, c => `%${c.charCodeAt(0).toString(16).toUpperCase()}`));
  const title = node => {
    const value = node.getAttribute('title');
    return value ? ` "${htmlText(singleLine(value)).replace(/\|/g, '&#124;').replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"` : '';
  };
  service.addRule('safeLink', {
    filter: 'a',
    replacement: (content, node) => node.hasAttribute('href') ? `[${content}](${destination(node.getAttribute('href'))}${title(node)})` : content,
  });
  service.addRule('safeImage', {
    filter: 'img',
    replacement: (_content, node) => `![${service.escape(singleLine(node.getAttribute('alt')))}](${destination(node.getAttribute('src'))}${title(node)})`,
  });
  service.addRule('safeStrike', { filter: ['del', 's', 'strike'], replacement: content => content ? `~~${content}~~` : '' });
  service.addRule('literalPre', {
    filter: 'pre',
    replacement: (_content, node) => fencedCode(node),
  });
  service.addRule('literalInlineCode', {
    filter: 'code',
    replacement: (_content, node) => inlineCode(node),
  });
  service.addRule('blockCodeInlineWrappers', {
    // Inline formatting/link delimiters cannot enclose a fenced block. Unwrap
    // only such wrappers, retaining their converted text and literal code.
    filter: node => ['a', 'em', 'i', 'strong', 'b', 'del', 's', 'strike'].includes(node.localName) && containsBlockCode(node),
    replacement: content => content,
  });
  // Never let GFM's keep(TABLE) or its assumptions about THEAD/whitespace run.
  service.addRule('tableParts', { filter: ['thead', 'tbody', 'tfoot', 'tr', 'th', 'td'], replacement: content => content });
  service.addRule('portableTables', {
    filter: 'table',
    replacement: (_content, node) => {
      const rows = [...node.rows].filter(row => row.closest('table') === node);
      const cells = rows.map(row => [...row.cells].map(cell => service.turndown(cell).trim()));
      if (!cells.length) return '';
      const caption = [...node.children].find(child => child.localName === 'caption');
      const prefix = caption ? `${service.turndown(caption)}\n\n` : '';
      // GFM cells cannot represent block code, nesting or arbitrary literal
      // backslash+pipe runs in code faithfully. Flatten those tables into blocks
      // instead of emitting raw HTML or rewriting code content after conversion.
      if (node.querySelector('table') || containsBlockCode(node) || [...node.querySelectorAll('code')].some(code => codeText(code).includes('|'))) {
        return `\n\n${prefix}${cells.map(row => row.join('\n\n')).join('\n\n')}\n\n`;
      }
      const width = Math.max(...cells.map(row => row.length));
      if (!width) return prefix;
      const header = rows[0].parentNode.localName === 'thead' || [...rows[0].cells].every(cell => cell.localName === 'th');
      if (!header) cells.unshift(Array(width).fill(''));
      cells.splice(1, 0, Array(width).fill('---'));
      // Prose, attribute titles and URLs already have context-safe pipe escapes.
      const line = row => `| ${Array.from({ length: width }, (_, i) => (row[i] || '').replace(/\n+/g, ' ')).join(' | ')} |`;
      return `\n\n${prefix}${cells.map(line).join('\n')}\n\n`;
    },
  });
  return service;
}

/** UNTRUSTED extracted HTML -> data only. No live DOM writes, resource loads,
 * custom-element construction, raw-HTML output, storage or image acquisition.
 * Supply a DOM document (including in tests); only its inert implementation is
 * used. Final protocol byte limits are the later caller's responsibility. */
export function convertCapture(capture, { document = globalThis.document } = {}) {
  validateMode(capture?.mode);
  validateSourceUrl(capture.sourceUrl);
  try {
    if (typeof capture.html !== 'string' || typeof capture.title !== 'string' ||
        typeof capture.capturedAt !== 'string' || !Number.isFinite(Date.parse(capture.capturedAt))) throw new CaptureError('conversion_failed');
    const baseURI = httpUrl(capture.baseURI);
    if (!baseURI) throw new CaptureError('conversion_failed');
    const inert = document.implementation.createHTMLDocument('');
    const template = inert.createElement('template');
    template.innerHTML = capture.html; // input-only sink in inert template contents
    const { root, images } = rebuild(template.content, baseURI);
    const markdown = markdownService().turndown(root).trim();
    if (!markdown) throw new CaptureError('conversion_empty');
    return { mode: capture.mode, title: capture.title, markdown, images, sourceUrl: capture.sourceUrl, capturedAt: capture.capturedAt };
  } catch (error) {
    if (error instanceof CaptureError) throw error;
    throw new CaptureError('conversion_failed');
  }
}
