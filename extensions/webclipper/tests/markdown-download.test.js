import { expect, it } from 'vitest';
import { markdownFilename } from '../src/ui/markdown-download.js';

it.each([
  ['', 'clip.md'], ['../..', '_.md'], ['folder/hello.md', 'folder_hello.md'],
  ['C:\\notes\\hello.MD', 'C__notes_hello.md'], ['<bad>:name.html', '_bad__name.html.md'],
  ['CON', '_CON.md'], ['lpt1.md', '_lpt1.md'],
])('uses a safe Markdown download basename for %j', (value, expected) => {
  expect(markdownFilename(value)).toBe(expected);
});
