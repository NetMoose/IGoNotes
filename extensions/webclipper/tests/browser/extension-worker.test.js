import { it, expect, vi } from 'vitest';
import { extensionWorker } from './extension-worker.js';
const worker = (path, name) => ({ url: () => `chrome-extension://${'a'.repeat(32)}/${path}`, evaluate: vi.fn(async () => name) });
it('selects the product worker even when a built-in extension starts first', async () => {
  const builtin = worker('thunk.js', 'Google Hangouts'), product = worker('worker.js', 'IGoNotes Web Clipper');
  const context = { serviceWorkers: () => [builtin, product], waitForEvent: vi.fn() };
  expect(await extensionWorker(context)).toBe(product);
  expect(builtin.evaluate).not.toHaveBeenCalled();
});
it('waits for the product script while ignoring unrelated serviceworker events', async () => {
  const builtin = worker('thunk.js', 'Google Hangouts'), product = worker('worker.js', 'IGoNotes Web Clipper');
  const context = { serviceWorkers: () => [builtin], waitForEvent: vi.fn(async (event, options) => {
    expect(event).toBe('serviceworker');expect(options.timeout).toBe(15000);
    expect(options.predicate(builtin)).toBe(false);expect(options.predicate(product)).toBe(true);return product;
  }) };
  expect(await extensionWorker(context)).toBe(product);
});
it('rejects a worker with the right script path but the wrong extension manifest', async () => {
  const other = worker('worker.js', 'Other extension');
  await expect(extensionWorker({ serviceWorkers: () => [other] })).rejects.toThrow();
});
