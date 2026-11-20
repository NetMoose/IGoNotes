import { expect, it } from 'vitest';
import { build } from 'vite';
import { resolve } from 'node:path';

it.each(['prepare', 'rewrite'])('bundles the %s API as standalone browser ESM using the pure entity decoder without DOM or bare imports', async name => {
  const root = resolve(import.meta.dirname, '..');
  const entry = resolve(root, `src/images/${name}.js`);
  const output = await build({
    configFile: resolve(root, 'vite.config.js'), logLevel: 'silent',
    build: { write: false, lib: { entry, formats: ['es'] },
      rolldownOptions: { input: entry, preserveEntrySignatures: 'strict' } },
  });
  const chunks = (Array.isArray(output) ? output : [output]).flatMap(bundle => bundle.output).filter(part => part.type === 'chunk');
  expect(chunks).toHaveLength(1);
  expect(chunks[0].code).not.toContain('document.createElement');
  // No node_modules or external module resolver exists inside this data module.
  const api = await import(/* @vite-ignore */ `data:text/javascript;base64,${Buffer.from(chunks[0].code).toString('base64')}`);
  expect(typeof api[name === 'prepare' ? 'prepareDraftImages' : 'buildImportSnapshot']).toBe('function');
  if (name === 'rewrite') {
    const snapshot = await api.buildImportSnapshot({
      draft: { id: 'bundle', title: 'native', filename: 'note.md', markdown: '![x](../a.gif)', sourceUrl: 'https://example.org/path/page', capturedAt: '2026-10-21T12:00:00Z', base: 'work', parent: '', destinationRevision: 'opaque', status: 'draft' },
      assets: [], endpoint: 'http://localhost:8080', importId: '550e8400-e29b-41d4-a716-446655440000',
      confirmedFallbacks: [{ url: 'https://example.org/a.gif', reason: 'explicit' }],
    });
    expect(snapshot.manifest.markdown).toContain('![x](https://example.org/a.gif)');
    expect(snapshot.autonomous).toBe(false);
  }
});
