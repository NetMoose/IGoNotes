import { expect, it } from 'vitest';
import { build } from 'vite';
import { resolve } from 'node:path';

it('bundles the preparation API as standalone browser ESM using the pure entity decoder without DOM or bare imports', async () => {
  const root = resolve(import.meta.dirname, '..');
  const entry = resolve(root, 'src/images/prepare.js');
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
  expect(typeof api.prepareDraftImages).toBe('function');
});
