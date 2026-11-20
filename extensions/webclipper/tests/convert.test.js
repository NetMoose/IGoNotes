import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { describe, expect, test } from 'vitest';
import * as converter from '../src/capture/convert.js';
import { marked } from './helpers/app-markdown.js';

const document = new JSDOM('', { url: 'https://live.test/ignored' }).window.document;
const fixture = name => readFileSync(new URL(`./fixtures/${name}`, import.meta.url), 'utf8');
const raw = html => ({ mode: 'selection', title: 'Title 🌲', html, text: 'not a fallback', baseURI: 'https://source.test/root/pages/', sourceUrl: 'https://source.test/original?identity=1', capturedAt: '2026-10-23T12:34:56.000Z' });
const convert = html => converter.convertCapture(raw(html), { document });

describe('inert allowlist to portable Markdown', () => {
  test('preserves Unicode, structure, base URI, GFM and literal code', () => {
    const result = convert(fixture('conversion.html'));
    expect(result.markdown).toContain('# Лес 🌲 &amp; science');
    expect(result.markdown).toContain('**bold** and _quiet_ ~~old~~');
    expect(result.markdown).toContain('> Quote 世界');
    expect(result.markdown).toMatch(/3\. +Third\n +\- +Nested/);
    expect(result.markdown).toMatch(/- +\[x\] Done/);
    expect(result.markdown).toMatch(/- +\[ \] Todo/);
    expect(result.markdown).toContain('[Read](https://source.test/root/%D0%BF%D1%83%D1%82%D1%8C?q=one&x=2#%D1%87%D0%B0%D1%81%D1%82%D1%8C)');
    expect(result.markdown).toContain('| Name | Value |\n| --- | --- |\n| 林 | 1 \\| 2 |');
    expect(result.markdown).toContain('`````js\nconst html = \'<img onerror="bad()">\';\n  ````\nconsole.log(\'世界\');\n`````');
    expect(result.markdown).toContain('`` `a` <b> ``');
    expect(result.markdown).not.toContain('PRIVATE');
    expect(result).toMatchObject({ mode: 'selection', title: 'Title 🌲', sourceUrl: raw('').sourceUrl, capturedAt: raw('').capturedAt });
    expect(result).not.toHaveProperty('html');
  });

  test('drops active containers, foreign namespaces and controls; unwraps ordinary unknown tags', () => {
    const { markdown } = convert(fixture('conversion-hostile.html'));
    expect(markdown).toContain('Keep **safe children**');
    expect(markdown).toContain('&lt;img src=x onerror=bad()&gt; &amp;lt;script&amp;gt;');
    expect(markdown).toContain('bad one bad two bad three');
    expect(markdown).toContain('visible unsupported');
    expect(markdown).not.toMatch(/SECRET_|<|javascript:|data:|network-|onclick|style=/);
  });

  test.each(['javascript:bad()', 'java\tscript:bad()', 'vbscript:bad()', 'data:text/html,bad', 'blob:https://source.test/id', 'file:///etc/passwd', 'https://user:pass@source.test/a'])('unwraps unsupported link %s', href => {
    const { markdown } = convert(`<p><a href="${href}">Visible</a></p>`);
    expect(markdown).toBe('Visible');
  });

  test('does not double-decode encoded schemes or trust injected base tags', () => {
    expect(convert('<base href="https://evil.test/"><a href="jav&#x61;script:bad()">No</a><a href="/ok">Yes</a>').markdown)
      .toBe('No[Yes](https://source.test/ok)');
    expect(convert('<a href="javascript%3Abad()">Encoded relative path</a>').markdown).toContain('https://source.test/root/pages/javascript%3Abad');
  });

  test('chooses real currentSrc, then lazy sources and responsive candidates over placeholders', () => {
    const { markdown, images } = convert('<img src="placeholder.gif" data-src="lazy.jpg" data-igonotes-current-src="https://cdn.test/current.webp" alt="Current"><img src="data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==" data-src="../lazy.png" alt="Lazy"><img srcset="small.png 1x, large.png 2x" alt="Responsive"><img src="blob:bad" alt="Blob">');
    expect(images).toEqual(['https://cdn.test/current.webp', 'https://source.test/root/lazy.png', 'https://source.test/root/pages/large.png']);
    for (const url of images) expect(markdown).toContain(`](${url})`);
    expect(markdown).toContain('Blob');
    expect(markdown).not.toMatch(/blob:|placeholder|data:/);
  });

  test.each(['png', 'jpeg', 'gif', 'webp'])('accepts only shaped raster data sources (%s), without decoding or fetching', mime => {
    expect(convert(`<img src="data:image/${mime};base64,AAAA" alt="Raster">`).images).toEqual([`data:image/${mime};base64,AAAA`]);
    expect(convert(`<img src="data:image/${mime},%FF%00" alt="Raster">`).images).toEqual([`data:image/${mime},%FF%00`]);
    expect(convert(`<img src="data:image/${mime};base64,!!!!" alt="Unsupported">`).markdown).toBe('Unsupported');
  });

  test('headerless, empty and nested tables never fall back to raw HTML or discard data', () => {
    expect(convert('<table><tr><td>Alpha</td><td>Beta</td></tr></table>').markdown).toContain('|  |  |\n| --- | --- |');
    const { markdown } = convert('<table><tr><td>Alpha</td><td>Beta<table><tr><td>Nested</td></tr></table></td></tr><tr><td>Gamma</td></tr></table><table></table>');
    for (const text of ['Alpha', 'Beta', 'Nested', 'Gamma']) expect(markdown).toContain(text);
    expect(markdown).not.toMatch(/<|>/);
  });

  test('untrusted attributes cannot break out of Markdown links, images or titles', () => {
    const { markdown } = convert('<a href="/a(b)\\c?q=&quot;&amp;x=1" title="&quot;)&#10;![x](javascript:bad()) &lt;img onerror=bad()&gt;">link</a><img src="/image.png" alt="](&#10;javascript:bad()) &lt;img onerror=bad()&gt;" title="\\&quot; onerror=bad()">');
    expect(markdown).not.toMatch(/<img|\n!\[x\]/);
    expect(markdown).toContain('&lt;img');
    expect(markdown).toContain('a%28b%29/c?q=%22&x=1');
    expect(markdown).toContain('\\]');
  });

  test('rendered adversarial prose/attributes create only the intended safe links and images', () => {
    const payloads = [
      fixture('conversion-hostile.html'),
      '<p>&lt;script&gt;bad()&lt;/script&gt; &lt;img src=x onerror=bad()&gt; &amp;#60;iframe&amp;#62;</p>',
      '<a href="/ok" title="\\&quot;)&#10;![injected](javascript:bad())&#10;&lt;img src=x onerror=bad()&gt;">[text] &lt;b&gt;</a><img src="/ok.png" alt="\\](&#10;javascript:bad()) &lt;svg onload=bad()&gt;" title="\\&quot;)&#10;&lt;script&gt;bad()&lt;/script&gt;">',
    ];
    for (const html of payloads) {
      const { markdown } = convert(html);
      const rendered = new JSDOM(marked.parse(markdown)).window.document;
      expect(rendered.querySelector('script, style, iframe, svg, math, object, embed, form')).toBeNull();
      for (const element of rendered.body.querySelectorAll('*')) {
        for (const attr of element.attributes) expect(attr.name).not.toMatch(/^on|^style$/i);
      }
      const links = [...rendered.querySelectorAll('[href], [src]')];
      expect(links).toHaveLength(html === payloads[2] ? 2 : 0);
      for (const element of links) expect(element.getAttribute('href') || element.getAttribute('src')).toMatch(/^https:\/\/source\.test\//);
    }
  });

  test('rendered code remains literal, including indented closing-fence attacks and inline backticks', () => {
    const literal = '<img src=x onerror=bad()>\n   ````\n[bad](javascript:bad())\n&';
    const html = `<pre><code>${literal.replace(/&/g, '&amp;').replace(/</g, '&lt;')}</code></pre><p><code>\`edge\` &lt;img&gt;</code></p>`;
    const rendered = new JSDOM(marked.parse(convert(html).markdown)).window.document;
    expect(rendered.querySelector('pre code').textContent).toBe(`${literal}\n`);
    expect(rendered.querySelector('p code').textContent).toBe('`edge` <img>');
    expect(rendered.querySelector('img, a')).toBeNull();
  });

  const adjacentCases = [
    ['a', 'b'],
    ['a', '`', '<img src=x onerror=bad()>'],
    ['a', '`', '[x](javascript:bad())'],
    [' ', '``', ' a '],
    ['a', '`', '```', '<svg onload=bad()> &'],
    ['', 'a', '', '`', '<img src=x onerror=bad()>'],
  ];
  const codeWrappers = [
    ['plain', code => code],
    ['span', code => `<span>${code}</span>`],
    ['unknown', code => `<x-wrap>${code}</x-wrap>`],
    ['unsafe link', code => `<a href="javascript:bad()">${code}</a>`],
    ['empty wrappers', code => `<span></span><em><span></span></em><code></code>${code}<strong></strong>`],
    ['dropped content', code => `<script>ignored()</script><!-- ignored -->${code}<img src="blob:unsupported" alt="">`],
  ];
  for (const [wrapper, wrap] of codeWrappers) {
    for (const context of ['paragraph', 'table cell']) {
      test.each(adjacentCases.map(parts => [parts]))(`adjacent code remains literal across ${wrapper} in ${context}: %j`, parts => {
        const content = parts.map(text => wrap(`<code>${text.replace(/&/g, '&amp;').replace(/</g, '&lt;')}</code>`)).join('');
        const html = context === 'paragraph' ? `<p>${content}</p>` : `<table><tr><td>${content}</td></tr></table>`;
        const rendered = new JSDOM(marked.parse(convert(html).markdown)).window.document;
        const container = rendered.querySelector(context === 'paragraph' ? 'p' : 'td');
        expect(container?.textContent).toBe(parts.join(''));
        expect([...container.querySelectorAll('code')].map(code => code.textContent).join('')).toBe(parts.join(''));
        expect(rendered.querySelector('img, a, script, iframe, svg, math')).toBeNull();
        for (const element of rendered.body.querySelectorAll('*')) {
          for (const attr of element.attributes) expect(attr.name).not.toMatch(/^on|^style$/i);
        }
      });
    }
  }

  test.each(['paragraph', 'table cell'])('blank formatting around all-space code cannot hide adjacency in %s', context => {
    const content = '<code>a</code><strong><em><code> </code></em></strong><code>`</code><code>&lt;img src=x onerror=bad()&gt;</code>';
    const html = context === 'paragraph' ? `<p>${content}</p>` : `<table><tr><td>${content}</td></tr></table>`;
    const rendered = new JSDOM(marked.parse(convert(html).markdown)).window.document;
    const container = rendered.querySelector(context === 'paragraph' ? 'p' : 'td');
    expect(rendered.querySelector('img, a, script')).toBeNull();
    expect(container.textContent).toBe('a `<img src=x onerror=bad()>');
    expect([...container.querySelectorAll('code')].map(code => code.textContent).join('')).toBe(container.textContent);
  });

  const formatCases = [
    ['em', '<em>', '</em>', true, false],
    ['strong', '<strong>', '</strong>', false, true],
    ['strong/em', '<strong><em>', '</em></strong>', true, true],
    ['em/strong', '<em><strong>', '</strong></em>', true, true],
  ];
  for (const [name, open, close, emphasis, strong] of formatCases) {
    for (const context of ['paragraph', 'table cell']) {
      test.each([['alpha', '尾'], ['（', '）'], ['前：', '。終']])(`retains ${name} code formatting next to parent text in ${context}: %j`, (left, right) => {
        const content = `${left}${open}<code> \` </code>${close}${right}`;
        const html = context === 'paragraph' ? `<p>${content}</p>` : `<table><tr><td>${content}</td></tr></table>`;
        const rendered = new JSDOM(marked.parse(convert(html).markdown)).window.document;
        const container = rendered.querySelector(context === 'paragraph' ? 'p' : 'td');
        expect(container?.textContent).toBe(`${left} \` ${right}`);
        const code = container.querySelector('code');
        expect(code?.textContent).toBe(' ` ');
        expect(Boolean(code.closest('em'))).toBe(emphasis);
        expect(Boolean(code.closest('strong'))).toBe(strong);
        expect(rendered.querySelector('img, a, script, svg')).toBeNull();
      });
      test.each([
        ['', 'a', '`', 'x`', ''],
        ['前：', '左``', '`世界`', '右```', '。終'],
        ['(', 'a', '<img onerror=bad()> `', '[x](javascript:bad())', ')'],
        ['plain', 'a', '`', 'x`', 'tail'],
        ['', 'a', ' ` ', 'x`', ''],
      ])(`retains ${name} formatting and exact code/text boundaries in ${context}: %j`, (prefix, left, middle, right, suffix) => {
        const code = text => `<code>${text.replace(/&/g, '&amp;').replace(/</g, '&lt;')}</code>`;
        const content = `${prefix}${code(left)}${open}${code(middle)}${close}${code(right)}${suffix}`;
        const html = context === 'paragraph' ? `<p>${content}</p>` : `<table><tr><td>${content}</td></tr></table>`;
        const rendered = new JSDOM(marked.parse(convert(html).markdown)).window.document;
        const container = rendered.querySelector(context === 'paragraph' ? 'p' : 'td');
        expect(container?.textContent).toBe(prefix + left + middle + right + suffix);
        const codes = [...container.querySelectorAll('code')];
        expect(codes.map(code => code.textContent)).toEqual([left, middle, right]);
        expect(Boolean(codes[1].closest('em'))).toBe(emphasis);
        expect(Boolean(codes[1].closest('strong'))).toBe(strong);
        for (const code of [codes[0], codes[2]]) expect(code.closest('em, strong')).toBeNull();
        expect(container.querySelectorAll('em')).toHaveLength(emphasis ? 1 : 0);
        expect(container.querySelectorAll('strong')).toHaveLength(strong ? 1 : 0);
        expect(rendered.querySelector('img, a, script, svg')).toBeNull();
        for (const element of rendered.body.querySelectorAll('*')) {
          for (const attr of element.attributes) expect(attr.name).not.toMatch(/^on/i);
        }
      });
    }
  }

  test.each([
    'a  b', ' a ', '  a  ', ' a', 'a ', ' ', '   ', '\t \t', ' \t ', ' \u00a0 ',
    '`', '``', '`edge`', ' `edge` ', 'a `` b ` c',
    '  <img onerror=bad()>  & [x](javascript:bad()) ` ',
  ])('preserves rendered inline-code text exactly: %j', literal => {
    const html = `<p><code>${literal.replace(/&/g, '&amp;').replace(/</g, '&lt;')}</code></p>`;
    const rendered = new JSDOM(marked.parse(convert(html).markdown)).window.document;
    expect(rendered.querySelector('p code')?.textContent).toBe(literal);
    expect(rendered.querySelector('pre, img, a, script')).toBeNull();
  });

  test('preserves inline-code whitespace through wrapper and table clones without changing surrounding prose', () => {
    const { markdown } = convert('<p>Before  prose <em><code> a  b </code></em> after  prose.</p><table><tr><td><code> c  d </code></td></tr></table><p><span><code>   </code></span></p>');
    const rendered = new JSDOM(marked.parse(markdown)).window.document;
    expect([...rendered.querySelectorAll('code')].map(code => code.textContent)).toEqual([' a  b ', ' c  d ', '   ']);
    expect(rendered.querySelector('p').textContent).toBe('Before prose  a  b  after prose.');
    expect(rendered.querySelector('td code')).not.toBeNull();
  });

  test('controlled code/format metadata cannot be supplied by source attributes or lose nested literal text', () => {
    const { markdown } = convert('<p><span data-igonotes-text-entity="60;img src=x onerror=bad()">Visible</span><code data-igonotes-code-text="forged" data-igonotes-code-fence="9999999999">a<code>b</code>c</code></p>');
    const rendered = new JSDOM(marked.parse(markdown)).window.document;
    expect(rendered.querySelector('p').textContent).toBe('Visibleabc');
    expect(rendered.querySelector('code').textContent).toBe('abc');
    expect(rendered.querySelector('img')).toBeNull();
  });

  test.each(['', 'em', 'a href="/safe"'])('preserves multiline inline-code as a fence inside %j without altering other paragraphs', wrapper => {
    const literal = ' a  b\n  ````\n<img onerror=bad()> &\n ';
    const code = `<code>${literal.replace(/&/g, '&amp;').replace(/</g, '&lt;')}</code>`;
    const wrapped = wrapper ? `<${wrapper}>${code}</${wrapper.split(' ')[0]}>` : code;
    const { markdown } = convert(`<p>Unrelated before.</p><p>Before ${wrapped} after.</p><p>Unrelated after.</p>`);
    const rendered = new JSDOM(marked.parse(markdown)).window.document;
    expect(rendered.querySelector('pre code')?.textContent).toBe(`${literal}\n`);
    expect([...rendered.querySelectorAll('p')].map(p => p.textContent)).toEqual(['Unrelated before.', 'Before ', ' after.', 'Unrelated after.']);
    expect(rendered.querySelector('img, a')).toBeNull();
  });

  test('multiline inline-code in a table uses block fallback without flattening literal newlines', () => {
    const rendered = new JSDOM(marked.parse(convert('<table><tr><td><code>a  b\n c</code></td></tr></table>').markdown)).window.document;
    expect(rendered.querySelector('pre code')?.textContent).toBe('a  b\n c\n');
    expect(rendered.querySelector('table')).toBeNull();
  });

  test('flattens tables with literal code pipes to preserve backslashes without exposing HTML', () => {
    const literal = 'a\\|b | <img onerror=bad()> `edge`';
    const { markdown } = convert(`<table><tr><td><code>${literal.replace(/</g, '&lt;')}</code></td></tr></table>`);
    const rendered = new JSDOM(marked.parse(markdown)).window.document;
    expect(rendered.querySelector('code')?.textContent).toBe(literal);
    expect(rendered.querySelector('table')).toBeNull();
    expect(rendered.querySelector('img')).toBeNull();
  });

  test('block code inside a complex table stays in a literal fence when flattened', () => {
    const { markdown } = convert('<table><tr><td><pre><code class="language-html">&lt;img onerror=bad()&gt;\n   ````\n&amp; | end</code></pre></td></tr></table>');
    const rendered = new JSDOM(marked.parse(markdown)).window.document;
    expect(rendered.querySelector('img')).toBeNull();
    expect(rendered.querySelector('pre code')?.textContent).toBe('<img onerror=bad()>\n   ````\n& | end\n');
    expect(rendered.querySelector('table')).toBeNull();
  });

  test('table link/image titles containing pipes cannot split cells or break inline code', () => {
    const { markdown } = convert('<table><tr><td><a href="/ok" title="x | y">Read</a> <img src="/ok.png" title="x | y" alt="Alt | text"> <code>&lt;img&gt;</code></td></tr></table>');
    const rendered = new JSDOM(marked.parse(markdown)).window.document;
    expect(rendered.querySelectorAll('td')).toHaveLength(1);
    expect(rendered.querySelector('td a')?.title).toBe('x | y');
    expect(rendered.querySelector('td img')?.title).toBe('x | y');
    expect(rendered.querySelector('td img')?.alt).toBe('Alt | text');
    expect(rendered.querySelector('td code')?.textContent).toBe('<img>');
  });

  test('all backtick runs are contained and language hints cannot inject fences', () => {
    const { markdown } = convert('<pre><code class="language-js&#10;```bad">&lt;script&gt;\n   ``````\n`inline`\n&amp;\n</code></pre>');
    expect(markdown).toBe('```````js\n<script>\n   ``````\n`inline`\n&\n```````');
  });

  test('preserves whitespace-only fenced code rather than classifying it as empty content', () => {
    expect(convert('<pre><code>  \n \n</code></pre>').markdown).toBe('```\n  \n \n```');
  });

  test('rejects empty converted Markdown rather than using extraction text or the full page', () => {
    expect(() => convert('<script>bad()</script><form>private</form>')).toThrow(expect.objectContaining({ code: 'conversion_empty' }));
  });

  test('parses/rebuilds without changing the caller document or constructing page custom elements', () => {
    const dom = new JSDOM('<p>Live page</p>', { url: 'https://source.test/' });
    let constructed = 0;
    dom.window.customElements.define('x-counter', class extends dom.window.HTMLElement { constructor() { super(); constructed++; } });
    const before = dom.window.document.documentElement.outerHTML;
    expect(converter.convertCapture(raw('<x-counter><p>Safe</p></x-counter><img src="/a.png">'), { document: dom.window.document }).markdown).toContain('Safe');
    expect(constructed).toBe(0);
    expect(dom.window.document.documentElement.outerHTML).toBe(before);
  });
});
