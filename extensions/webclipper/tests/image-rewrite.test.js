import { readFileSync } from 'node:fs';
import { expect, it, vi } from 'vitest';
import * as rewrite from '../src/images/rewrite.js';
import { inspectImageBlob } from '../src/images/acquire.js';
import { scanImageReferences } from '../src/images/references.js';
import { formatNote } from '../src/markdown/note.js';
import { JSDOM } from 'jsdom';
import { marked } from './helpers/app-markdown.js';

const importId = '550e8400-e29b-41d4-a716-446655440000';
const pathFixtures = JSON.parse(readFileSync(new URL('../../../testdata/clipper/portable-paths.json', import.meta.url)));
const draft = (markdown = '![x](../a.gif)') => ({ id: 'draft', title: 'Текст 😀', filename: 'Custom name.md', markdown, sourceUrl: 'https://example.org/path/page', capturedAt: '2026-10-21T12:00:00.1200Z', base: 'work', parent: 'research/articles', destinationRevision: 'original', status: 'draft' });
const options = (d = draft(), assets = [], confirmedFallbacks = []) => ({ draft: d, assets, confirmedFallbacks, endpoint: 'http://localhost:8080/', importId });
async function asset(id = 'A', normalizedUrl = 'https://example.org/a.gif') {
  const blob = new Blob([Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==', 'base64')], { type: 'image/gif' });
  const descriptor = { ...await inspectImageBlob(blob), status: 'ready', canonicalAssetId: id };
  return { id, normalizedUrl, blob, size: blob.size, descriptor };
}

it('matches every shared Go canonical byte/digest vector without mutating input', async () => {
  const fixtures = JSON.parse(readFileSync(new URL('../../../testdata/clipper/manifest-digests.json', import.meta.url)));
  for (const f of fixtures) {
    const before = structuredClone(f.manifest);
    expect(rewrite.canonicalManifest(f.manifest)).toBe(f.canonical);
    expect(await rewrite.manifestDigest(f.manifest)).toBe(f.digest);
    expect(f.manifest).toEqual(before);
  }
});

it('rewrites only inline destination token spans with Unicode, CRLF, nested alt, escapes and titles', async () => {
  const row = await asset();
  const url = 'https://example.org/a.gif';
  const body = '😀\r\n![a [nested] \\] `x`]( <../a.gif>  \'t\\\'itle\' )\r\n![b](https://example.org/a.gif "double") ![c](../a.gif (paren))\r\n[link](../a.gif) ../a.gif `![code](../a.gif)`\r\n```md\r\n![code](../a.gif)\r\n```\r\n';
  const refs = scanImageReferences(body, draft().sourceUrl);
  expect(refs).toHaveLength(3);
  for (const ref of refs) expect(body.slice(ref.destinationPosition.start.offset, ref.destinationPosition.end.offset)).toBe(ref.kind === 'inline' && ref.destination === url ? url : '../a.gif');
  const snapshot = await rewrite.buildImportSnapshot(options(draft(body), [row]));
  const path = `../../assets/images/clip-${importId}-${row.descriptor.sha256}.gif`;
  expect(snapshot.manifest.markdown).toBe(formatNote({ ...draft(body), markdown: body.replace('<../a.gif>', `<${path}>`).replace('(https://example.org/a.gif "double")', `(${path} "double")`).replace('(../a.gif (paren))', `(${path} (paren))`) }));
  expect(snapshot.endpoint).toBe('http://localhost:8080');
  expect(snapshot.manifest.assets).toEqual([{ id: 'A', sha256: row.descriptor.sha256, media_type: 'image/gif', size: row.blob.size }]);
  expect(snapshot.assetBlobs.A).toBeInstanceOf(Blob);
});

it('expands only full/collapsed/shortcut image occurrences and keeps shared link definitions untouched', async () => {
  const row = await asset();
  const body = '![a [nested] \\] `x`][pic] ![pic][] ![pic]\r\n[ordinary][pic] ![undefined]\r\n\r\n[pic]: <../a.gif> \'a "title"\'\r\n';
  const result = await rewrite.buildImportSnapshot(options(draft(body), [row]));
  const path = `../../assets/images/clip-${importId}-${row.descriptor.sha256}.gif`;
  const expected = `![a [nested] \\] \`x\`](${path} "a \\"title\\"") ![pic](${path} "a \\"title\\"") ![pic](${path} "a \\"title\\"")\r\n[ordinary][pic] ![undefined]\r\n\r\n[pic]: <../a.gif> 'a "title"'\r\n`;
  expect(result.manifest.markdown).toBe(formatNote({ ...draft(), markdown: expected }));
  expect(scanImageReferences(result.manifest.markdown, draft().sourceUrl).map(r => r.title)).toEqual(Array(3).fill('a "title"'));
});

it('handles balanced/escaped destination parentheses, empty destinations and angle sources', async () => {
  for (const syntax of ['![a](../a(b).gif)', '![a](../a\\(b\\).gif)', '![a](<../a(b).gif>)', '![a]()', '![a](<>)']) {
    const d = draft(syntax), ref = scanImageReferences(syntax, d.sourceUrl)[0];
    const row = await asset('A', ref.url);
    const result = await rewrite.buildImportSnapshot(options(d, [row]));
    expect(scanImageReferences(result.manifest.markdown, d.sourceUrl)[0].destination).toContain('assets/images/clip-');
  }
});

it('requires current per-URL/reason external intent, makes relative fallbacks absolute and excludes chosen assets', async () => {
  await expect(rewrite.buildImportSnapshot(options())).rejects.toThrow();
  const row = await asset();
  const ack = { url: row.normalizedUrl, reason: 'permission_denied' };
  const result = await rewrite.buildImportSnapshot(options(draft(), [row], [ack]));
  expect(result.manifest.external_images).toEqual([ack]);
  expect(result.manifest.assets).toEqual([]);
  expect(result.assetBlobs).toEqual({});
  expect(result.autonomous).toBe(false);
  expect(result.warnings).toEqual([ack]);
  expect(result.manifest.markdown).toContain('![x](https://example.org/a.gif)');
  await expect(rewrite.buildImportSnapshot(options(draft('![x](../new.gif)'), [], [ack]))).rejects.toThrow();
  for (const source of ['javascript:alert(1)', 'data:image/svg+xml,abc', 'data:image/png;base64,YWJj', 'http://127.0.0.1/x', 'http://192.168.1.1/x']) {
    await expect(rewrite.buildImportSnapshot(options(draft(`![x](<${source}>)`), [], [{ url: source, reason: 'explicit' }]))).rejects.toThrow();
  }
});

it('deduplicates canonical owners and byte-equal assets while retaining source alias mappings', async () => {
  const a = await asset('z'), b = await asset('A', 'https://example.org/b.gif');
  const alias = { ...b, id: 'alias', blob: null, size: 0, descriptor: { ...b.descriptor, canonicalAssetId: 'A' } };
  const result = await rewrite.buildImportSnapshot(options(draft('![a](../a.gif) ![b](../b.gif) ![repeat](../a.gif)'), [a, b, alias]));
  expect(result.manifest.assets).toHaveLength(1);
  expect(Object.keys(result.assetBlobs)).toHaveLength(1);
  expect(result.sourceAliases.map(a => a.url)).toEqual(['https://example.org/a.gif', 'https://example.org/b.gif', 'https://example.org/a.gif']);
  expect(new Set(result.sourceAliases.map(a => a.assetId)).size).toBe(1);
});

it('rejects corrupt caches, bad IDs and dangling/cyclic aliases rather than zero-byte ready', async () => {
  for (const change of [r => { r.descriptor.sha256 = '0'.repeat(64); }, r => { r.size++; }, r => { r.descriptor.extension = 'jpeg'; }, r => { r.id = 'é'; r.descriptor.canonicalAssetId = 'é'; }, r => { r.descriptor.canonicalAssetId = 'missing'; }, r => { r.blob = null; }, r => { r.descriptor.status = 'error'; }]) {
    const row = await asset(); change(row);
    await expect(rewrite.buildImportSnapshot(options(draft(), [row]))).rejects.toThrow();
  }
  const a = await asset(), b = await asset('B', 'https://example.org/b.gif');
  a.descriptor.canonicalAssetId = 'B'; b.descriptor.canonicalAssetId = 'A';
  await expect(rewrite.buildImportSnapshot(options(draft(), [a, b]))).rejects.toThrow();
});

it('validates exact metadata, portable visible paths, all formatted bytes and protocol limits', async () => {
  for (const filename of ['CON.md', 'COM¹.md', '../x.md', 'x.MD', '.md', 'x.md ', 'x:foo.md']) await expect(rewrite.buildImportSnapshot(options({ ...draft('text'), filename }))).rejects.toThrow();
  for (const parent of ['/root', '../x', 'a//b', '.hidden', 'a/assets', 'a/.git', 'NUL']) await expect(rewrite.buildImportSnapshot(options({ ...draft('text'), parent }))).rejects.toThrow();
  await expect(rewrite.buildImportSnapshot(options(draft('x'.repeat(5 * 1024 ** 2))))).rejects.toThrow();
  const f = JSON.parse(readFileSync(new URL('../../../testdata/clipper/manifest-digests.json', import.meta.url)))[0].manifest;
  for (const patch of [{ markdown: '\ud800' }, { base: 3 }, { import_id: 'bad' }, { assets: null }, { source_url: 'https://x/%0a' }, { captured_at: '2026-02-30T00:00:00Z' }, { extra: 'no' }, { assets: [{ id: 'manifest', size: 1, media_type: 'image/png', sha256: 'a'.repeat(64) }] }, { assets: Array.from({ length: 101 }, (_, i) => ({ id: `a${i}`, size: 1, media_type: 'image/png', sha256: 'a'.repeat(64) })) }, { assets: [{ id: 'a', size: 10 * 1024 ** 2 + 1, media_type: 'image/png', sha256: 'a'.repeat(64) }] }, { assets: Array.from({ length: 6 }, (_, i) => ({ id: `a${i}`, size: 10 * 1024 ** 2, media_type: 'image/png', sha256: 'a'.repeat(64) })) }]) expect(() => rewrite.canonicalManifest({ ...f, ...patch })).toThrow();
});

it('clones before the first hash await, freezes all JSON, keeps Blob handles and original editable body', async () => {
  const row = await asset(), input = options(draft(), [row]);
  const digest = crypto.subtle.digest.bind(crypto.subtle);
  let release;
  const paused = new Promise(resolve => { release = resolve; });
  const spy = vi.spyOn(crypto.subtle, 'digest').mockImplementation(async (...args) => { await paused; return digest(...args); });
  const pending = rewrite.buildImportSnapshot(input);
  input.draft.title = 'changed'; input.draft.destinationRevision = 'changed'; input.endpoint = 'http://localhost:9999'; row.descriptor.sha256 = 'bad'; input.assets.length = 0;
  release();
  const snapshot = await pending; spy.mockRestore();
  expect(snapshot.manifest.markdown).toContain('title: "Текст 😀"');
  expect(snapshot.manifest.destination_revision).toBe('original');
  expect(snapshot.endpoint).toBe('http://localhost:8080');
  expect(snapshot.originalDraft.markdown).toBe('![x](../a.gif)');
  expect(snapshot.preparationFingerprint).toMatch(/^[0-9a-f]{64}$/);
  for (const value of [snapshot, snapshot.manifest, snapshot.manifest.assets, snapshot.manifest.assets[0], snapshot.assetBlobs, snapshot.sourceAliases, snapshot.sourceAliases[0], snapshot.originalDraft]) expect(Object.isFrozen(value)).toBe(true);
  expect(() => { snapshot.manifest.assets[0].size = 0; }).toThrow();
  expect(await rewrite.manifestDigest(snapshot.manifest)).toBe(snapshot.digest);
});

it('rejects coercible descriptor metadata and leaves raw HTML and dangling references unmanaged', async () => {
  const f = JSON.parse(readFileSync(new URL('../../../testdata/clipper/manifest-digests.json', import.meta.url)))[1].manifest;
  f.assets[0].media_type = { toString: () => 'image/png' };
  expect(() => rewrite.canonicalManifest(f)).toThrow();
  const body = '<img src="https://private.invalid/not-managed">\n\n![undefined]\n';
  const d = draft(body);
  const result = await rewrite.buildImportSnapshot(options(d));
  expect(result.manifest.markdown).toBe(formatNote(d));
  expect(result.manifest.assets).toEqual([]);
  expect(result.sourceAliases).toEqual([]);
});

it('validates each acknowledgement and each active HTTP image independently', async () => {
  const d = draft('![a](../a.gif) ![b](../b.gif)');
  for (const acks of [[], [{ url: 'https://example.org/a.gif', reason: 'failed' }], [{ url: 'https://example.org/a.gif', reason: '' }, { url: 'https://example.org/b.gif', reason: 'failed' }], [{ url: 'https://example.org/a.gif', reason: 'failed', all: true }]]) {
    await expect(rewrite.buildImportSnapshot(options(d, [], acks))).rejects.toThrow();
  }
  const acks = ['a', 'b'].map(n => ({ url: `https://example.org/${n}.gif`, reason: 'explicit choice' }));
  const result = await rewrite.buildImportSnapshot(options(d, [], acks));
  expect(result.manifest.external_images).toEqual(acks);
  expect(result.manifest.assets).toEqual([]);
});

it('handles root destinations, GIF/JPEG server extensions and exact custom filenames', async () => {
  const jpeg = new Blob([new Uint8Array([255,216,255,192,0,8,8,0,1,0,1,1,255,218,0,2,255,217])], { type: 'image/jpeg' });
  const descriptor = { ...await inspectImageBlob(jpeg), status: 'ready', canonicalAssetId: 'jpeg' };
  const d = { ...draft('![x](../a.gif)'), parent: '', filename: 'Моя é 😀 note.md' };
  const result = await rewrite.buildImportSnapshot(options(d, [{ id: 'jpeg', normalizedUrl: 'https://example.org/a.gif', descriptor, size: jpeg.size, blob: jpeg }]));
  expect(result.manifest.filename).toBe(d.filename);
  expect(result.manifest.markdown).toContain(`![x](assets/images/clip-${importId}-${descriptor.sha256}.jpg)`);
});

it('applies the 5 MiB limit to the formatted rewritten note, not a large managed data URL', async () => {
  const small = await asset();
  const base = new Uint8Array(await small.blob.arrayBuffer());
  const comment = new Uint8Array(258); comment[0] = 33; comment[1] = 254; comment[2] = 254;
  const blob = new Blob([base.subarray(0, base.length - 1), ...Array(17000).fill(comment), new Uint8Array([59])], { type: 'image/gif' });
  const source = `data:image/gif;base64,${Buffer.from(await blob.arrayBuffer()).toString('base64')}`;
  expect(source.length).toBeGreaterThan(5 * 1024 ** 2);
  const descriptor = { ...await inspectImageBlob(blob), status: 'ready', canonicalAssetId: 'data' };
  const result = await rewrite.buildImportSnapshot(options(draft(`![large](<${source}>)`), [{ id: 'data', normalizedUrl: source, size: blob.size, descriptor, blob }]));
  expect(result.manifest.markdown.length).toBeLessThan(1000);
});

it('counts unique upload bytes rather than aliases and supports an explicit batch-budget choice', async () => {
  const sample = await asset();
  const base = new Uint8Array(await sample.blob.arrayBuffer());
  const rows = [];
  for (let i = 0; i < 101; i++) {
    const blob = new Blob([base.subarray(0, base.length - 1), new Uint8Array([33,254,1,i,0,59])], { type: 'image/gif' });
    const descriptor = { ...await inspectImageBlob(blob), status: 'ready', canonicalAssetId: `a${i}` };
    rows.push({ id: `a${i}`, normalizedUrl: `https://example.org/${i}.gif`, blob, size: blob.size, descriptor });
  }
  const d = draft(rows.map(r => `![x](${r.normalizedUrl})`).join(' '));
  await expect(rewrite.buildImportSnapshot(options(d, rows))).rejects.toThrow(/budget/);
  const result = await rewrite.buildImportSnapshot(options(d, rows, [{ url: rows[100].normalizedUrl, reason: 'batch budget choice' }]));
  expect(result.manifest.assets).toHaveLength(100);
  expect(result.assetBlobs.a100).toBeUndefined();
  expect(result.warnings).toEqual([{ url: rows[100].normalizedUrl, reason: 'batch budget choice' }]);
});

it('uses Go Unicode whitespace semantics instead of JavaScript trim coercion', () => {
  const f = JSON.parse(readFileSync(new URL('../../../testdata/clipper/manifest-digests.json', import.meta.url)))[0].manifest;
  for (const patch of [{ base: 'work\u0085' }, { destination_revision: '\u0085' }, { markdown: '\u0085' }]) expect(() => rewrite.canonicalManifest({ ...f, ...patch })).toThrow();
  expect(rewrite.canonicalManifest({ ...f, base: '\ufeffwork' })).toContain('\ufeffwork');
});

it('rejects non-JSON asset scalar fields even in inactive rows without freezing caller objects', async () => {
  for (const patch of [{ normalizedUrl: { value: 'https://example.org/a.gif' } }, { size: { value: 1 } }, { blob: new Uint8Array([1]) }]) {
    const row = { ...await asset(), ...patch };
    await expect(rewrite.buildImportSnapshot(options(draft('text'), [row]))).rejects.toThrow();
    expect(Object.isFrozen(Object.values(patch)[0])).toBe(false);
  }
});

it.each(['local', 'external'])('preserves exact decoded reference titles and rendered images for %s destinations', async mode => {
  const row = await asset();
  for (const title of ['a\n\nb', 'a\n b', 'a\r b', 'a\r\nb', 'a\t b', 'a\r\n\t\n b', '&amp; &#10; " \\ 😀', 'a\u2028\u2029b']) {
    const encodedTitle = title.replace(/[&"\\\r\n\t\u2028\u2029]/gu, c => `&#${c.codePointAt(0)};`);
    const tail = `\n\n[ordinary][p]\n\n[p]: https://example.org/a.gif "${encodedTitle}"\n`;
    const d = draft('![x][p] ![p][] ![p]' + tail);
    expect(scanImageReferences(d.markdown, d.sourceUrl).map(r => r.title)).toEqual(Array(3).fill(title));
    const result = await rewrite.buildImportSnapshot(options(d, [row], mode === 'external' ? [{ url: row.normalizedUrl, reason: 'explicit' }] : []));
    const refs = scanImageReferences(result.manifest.markdown, d.sourceUrl);
    expect(refs).toHaveLength(3);
    expect(refs.map(r => r.title)).toEqual(Array(3).fill(title));
    expect(result.manifest.markdown.endsWith(tail)).toBe(true);
    const body = result.manifest.markdown.slice(result.manifest.markdown.indexOf('\n---\n\n') + 6);
    const dom = new JSDOM(marked.parse(body));
    expect([...dom.window.document.querySelectorAll('img')].map(img => img.getAttribute('title'))).toEqual(Array(3).fill(title));
    expect(dom.window.document.querySelector('a').getAttribute('href')).toBe(row.normalizedUrl);
    dom.window.close();
  }
});

it.each(pathFixtures)('$name agrees with Go manifest path and visible-parent rules', async f => {
  const d = { ...draft('body'), filename: f.filename, parent: f.parent };
  const m = { import_id: importId, base: d.base, destination_revision: d.destinationRevision, parent: d.parent, filename: d.filename, markdown: 'body', source_url: d.sourceUrl, captured_at: d.capturedAt, assets: [], external_images: [] };
  if (f.manifestValid) expect(() => rewrite.canonicalManifest(m)).not.toThrow();
  else expect(() => rewrite.canonicalManifest(m)).toThrow();
  if (f.manifestValid && f.visibleParent) expect((await rewrite.buildImportSnapshot(options(d))).manifest.parent).toBe(f.parent);
  else await expect(rewrite.buildImportSnapshot(options(d))).rejects.toThrow();
});
