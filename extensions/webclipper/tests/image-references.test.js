import { expect, it } from 'vitest';
import { scanImageReferences } from '../src/images/references.js';

it('resolves inline/full/collapsed/shortcut images, first definitions, escapes and casefolding', () => {
  const body = '🦉 ![a](../图\\(1\\).png?q=1&amp;x=2 "title") ![b][FOO] ![foo][] ![FoO]\n\n[foo]: <https://cdn.example/`图`.png> "shared"\n[foo]: https://wrong.example/x\n[ordinary][foo] ![undefined]\n';
  const refs = scanImageReferences(body, 'https://example.org/path/page');
  expect(refs.map(r => r.url)).toEqual(['https://example.org/%E5%9B%BE(1).png?q=1&x=2', ...Array(3).fill('https://cdn.example/%60%E5%9B%BE%60.png')]);
  expect(refs.map(r => r.kind)).toEqual(['inline', 'full', 'collapsed', 'shortcut']);
  expect(body.slice(refs[0].position.start.offset, refs[0].position.end.offset)).toBe('![a](../图\\(1\\).png?q=1&amp;x=2 "title")');
  expect(refs[1].definition.url).toBe('https://cdn.example/`图`.png');
  expect(body.slice(refs[1].definition.position.start.offset, refs[1].definition.position.end.offset)).toContain('[foo]:');
});

it('scans GFM table cells and nested images but excludes links, code, fences, escaped markers, undefined references and raw HTML', () => {
  const body = '| image | literal |\r\n| --- | --- |\r\n| ![图](https://cdn.example/a\\(b\\).png) | `![no](https://no.example/x)` |\r\n\r\n[![nested](https://cdn.example/in.png)](https://link.example/)\n\n```md\n![no](https://no.example/fence)\n```\n\n    ![no](https://no.example/indent)\n\n\\![escaped](https://no.example/escape) [link](https://no.example/link) ![missing]\n\n<img src="https://no.example/html">\n';
  const refs = scanImageReferences(body, 'https://example.org/');
  expect(refs.map(r => r.url)).toEqual(['https://cdn.example/a(b).png', 'https://cdn.example/in.png']);
  expect(refs.map(r => body.slice(r.position.start.offset, r.position.end.offset))).toEqual(['![图](https://cdn.example/a\\(b\\).png)', '![nested](https://cdn.example/in.png)']);
});

it('keeps decoded destination aliases and exact data spelling for future rewriting', () => {
  const refs = scanImageReferences('![x](<HTTPS://CDN.example:443/a#one>) ![y](data:image/png;base64,YWJj) ![z](javascript:bad)', 'https://example.org/');
  expect(refs[0]).toMatchObject({ destination: 'HTTPS://CDN.example:443/a#one', url: 'https://cdn.example/a#one' });
  expect(refs[1].url).toBe('data:image/png;base64,YWJj');
  expect(refs[2].url).toBe('javascript:bad');
});
