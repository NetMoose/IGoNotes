import { resolve } from 'node:path';
import { defineConfig } from 'vite';
import { root } from './vite.config.js';

// executeScript({files}) consumes classic scripts, never ESM or shared UI chunks.
export default defineConfig({
  root,
  base: './',
  publicDir: false,
  build: {
    target: 'chrome120',
    emptyOutDir: false,
    lib: {
      entry: resolve(root, 'src/content-script.js'),
      name: 'IGoNotesContentScript',
      formats: ['iife'],
      fileName: () => 'content-script.js',
    },
    license: { fileName: 'LICENSES/bundled-content.md' },
  },
});
