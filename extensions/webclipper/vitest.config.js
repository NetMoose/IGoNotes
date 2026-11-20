import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { svelteTesting } from '@testing-library/svelte/vite';
import { fileURLToPath } from 'node:url';

export default defineConfig({
  plugins: [svelte(), svelteTesting()],
  resolve: { alias: { 'decode-named-character-reference': fileURLToPath(new URL('./node_modules/decode-named-character-reference/index.js', import.meta.url)) } },
  test: {
    // Match full dependency paths: native external imports bypass resolve.alias.
    server: { deps: { inline: [/\/node_modules\/(?:mdast-util-|micromark(?:-|\/)|decode-named-character-reference\/)/] } },
    environment: 'node',
    include: ['tests/**/*.test.js'],
    testTimeout: 30000,
    hookTimeout: 30000,
  },
});
