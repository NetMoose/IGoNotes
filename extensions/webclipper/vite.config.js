import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export const root = fileURLToPath(new URL('.', import.meta.url));

export default defineConfig({
  root,
  base: './',
  publicDir: false,
  plugins: [svelte()],
  // Select only this package's pure (default/worker) entry. A global `worker`
  // condition would also select Svelte's server runtime in the editor bundle.
  resolve: { alias: { 'decode-named-character-reference': resolve(root, 'node_modules/decode-named-character-reference/index.js') } },
  build: {
    target: 'chrome120',
    modulePreload: false,
    license: { fileName: 'LICENSES/bundled-ui.md' },
    rolldownOptions: {
      input: {
        popup: resolve(root, 'popup.html'),
        editor: resolve(root, 'editor.html'),
        worker: resolve(root, 'src/worker.js'),
      },
      output: {
        entryFileNames: '[name].js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
});
