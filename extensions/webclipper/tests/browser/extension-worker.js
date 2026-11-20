import assert from 'node:assert/strict';

export async function extensionWorker(context) {
  const matches = worker => /^chrome-extension:\/\/[a-p]{32}\/worker\.js$/.test(worker.url());
  const worker = context.serviceWorkers().find(matches)
    ?? await context.waitForEvent('serviceworker', { predicate: matches, timeout: 15000 });
  assert.equal(await worker.evaluate(() => chrome.runtime.getManifest().name), 'IGoNotes Web Clipper');
  return worker;
}
