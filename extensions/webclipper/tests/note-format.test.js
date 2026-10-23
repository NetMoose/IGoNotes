import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';
import { parseDocument } from 'yaml';
import { formatNote, proposeFilename } from '../src/markdown/note.js';

const fixtures = JSON.parse(readFileSync(new URL('../../../testdata/clipper/note-format.json', import.meta.url), 'utf8'));
const input = fixtures[0].input;
function assertYaml(note, metadata) {
  const end = note.indexOf('\n---\n\n', 4);
  const doc = parseDocument(note.slice(4, end), { version: '1.2' });
  expect(doc.errors).toEqual([]);
  expect(doc.warnings).toEqual([]);
  expect(doc.toJS()).toEqual({ title: metadata.title, source: metadata.sourceUrl, clipped_at: metadata.capturedAt });
  expect(Object.keys(doc.toJS())).toEqual(['title', 'source', 'clipped_at']);
  expect(note.slice(end + 6)).toBe(metadata.markdown);
}
it.each(fixtures)('$name matches exact shared bytes and YAML 1.2 string scalars', fixture => {
  expect(proposeFilename(fixture.input.title)).toBe(fixture.expectedFilename);
  const note = formatNote(fixture.input);
  expect(note).toBe(fixture.fullMarkdown);
  assertYaml(note, fixture.input);
});
it('round trips every C0/C1 control and Unicode separator without field injection', () => {
  const title = Array.from({ length: 160 }, (_, i) => String.fromCodePoint(i)).join('') + '\u2028\u2029\ufffe\uffff';
  assertYaml(formatNote({ ...input, title }), { ...input, title });
});
it.each(['CON', 'PRN.txt', 'AUX', 'NUL', 'CONIN$', 'CONOUT$.md', 'COM1', 'com9.md', 'LPT1', 'lpt9', 'COM¹', 'LPT²', 'COM³'])('avoids Windows device %s', title => {
  expect(proposeFilename(title)).toMatch(/^_/);
});
const devices = ['CON', 'PRN', 'AUX', 'NUL', 'CONIN$', 'CONOUT$',
  ...['COM', 'LPT'].flatMap(prefix => [...'123456789¹²³'].map(number => prefix + number))];
it.each(devices)('protects final truncated/trimmed device stem %s', device => {
  for (const tail of [' '.repeat(260) + 'x', (' .').repeat(130) + 'x']) {
    const title = device + tail;
    expect(proposeFilename(title)).toBe('_' + device + '.md');
    assertYaml(formatNote({ ...input, title }), { ...input, title });
  }
});
it.each(devices)('keeps prefixed dot-first-component device %s within the byte cap', device => {
  for (const character of ['x', 'я', '🌲']) {
    const filename = proposeFilename(device + ' .ext.' + character.repeat(300));
    expect(filename).toMatch(new RegExp('^_' + device.replace('$', '\\$') + ' \\.ext\\.'));
    expect(new TextEncoder().encode(filename).length).toBeLessThanOrEqual(255);
    expect(filename.isWellFormed()).toBe(true);
    expect(filename).not.toMatch(/[. ]\.md$/);
  }
});
it.each(['', '.'.repeat(260), ' '.repeat(260), ' . '.repeat(260), '.MD'])('uses a nonempty final fallback for %j', title => {
  expect(proposeFilename(title)).toBe('clip.md');
});
it.each([[' . .. ', 'clip.md'], ['hello.MD', 'hello.md'], ['.md', 'clip.md'], [' .hello. ', 'hello.md'], ['e\u0301', 'é.md'], ['folder/name\\x:*?"<>|', 'folder_name_x_______.md']])('proposes portable %j', (title, expected) => {
  expect(proposeFilename(title)).toBe(expected);
});
it.each(['a', 'я', '🌲'])('bounds UTF-8 bytes without splitting %s or suffix', character => {
  const filename = proposeFilename(character.repeat(300));
  expect(new TextEncoder().encode(filename).length).toBeLessThanOrEqual(255);
  expect(filename.isWellFormed()).toBe(true);
  expect(filename).toBe(character.repeat(Math.floor(252 / new TextEncoder().encode(character).length)) + '.md');
});
it('normalizes an existing suffix after trimming filename edges', () => {
  expect(proposeFilename(' hello.MD . ')).toBe('hello.md');
});
it.each([
  { title: 42 }, { title: '\ud800' }, { markdown: '\udfff' },
  { sourceUrl: 'https://user:pass@example.test/' }, { sourceUrl: 'https://@example.test/' },
  { sourceUrl: 'https:///example.test/' }, { sourceUrl: 'file:///tmp/x' },
  { sourceUrl: 'https://example.test/?q=%0a' }, { sourceUrl: 'https://example.test/%ff' },
  { sourceUrl: 'https://example.test/a b' }, { sourceUrl: 'https://example.test/%' },
  { capturedAt: '2026-02-30T12:00:00Z' }, { capturedAt: '2026-10-23T12:00:00+00:00' },
  { capturedAt: '2026-10-23T12:00:00.1234567890Z' }, { capturedAt: 'yesterday' },
])('rejects invalid metadata explicitly: %j', change => {
  expect(() => formatNote({ ...input, ...change })).toThrow(/Invalid note/);
});
