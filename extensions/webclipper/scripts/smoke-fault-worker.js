import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';

// Test-only minimal path. The supervisor owns all resources and both clocks;
// functional packaging/image checks remain in their full normal smoke workers.
const [endpoint] = process.argv.slice(2);
const fault = process.env.CLIPPER_SMOKE_FAULT;
assert.ok(['hang-evaluate', 'close-fail', 'close-hang'].includes(fault));
const browser = await chromium.connectOverCDP(endpoint);
const context = browser.contexts()[0];
const worker = context.serviceWorkers()[0] ?? await context.waitForEvent('serviceworker', { timeout: 15000 });
assert.match(new URL(worker.url()).host, /^[a-p]{32}$/);
assert.equal(await worker.evaluate(() => chrome.runtime.onInstalled.hasListeners()), true);

// Install the receiver before announcing readiness. No fault executes until the
// parent has armed its own short timer; worker/event-loop hangs cannot starve it.
const inject = new Promise(resolve => process.once('message', message => {
  assert.deepEqual(message, { type: 'inject-fault', fault });
  resolve();
}));
process.send({ type: 'fault-ready', fault, sessionReady: true, browser: browser.version() });
await inject;

if (fault === 'hang-evaluate') {
  console.log('SMOKE_FAULT_REACHED hang-evaluate');
  await worker.evaluate(() => new Promise(() => {}));
} else {
  // The marker is inside the injected close implementation: observing it proves
  // browser.close() was actually called, not merely that setup approached finally.
  browser.close = async () => {
    console.log(`SMOKE_FAULT_REACHED ${fault}`);
    if (fault === 'close-fail') throw new Error('Injected browser.close failure');
    await new Promise(() => {});
  };
  await browser.close();
}
throw new Error('Injected fault unexpectedly completed');
