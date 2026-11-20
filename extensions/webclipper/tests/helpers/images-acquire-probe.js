// Controlled child process: --expose-gc, bounded by the parent's exec timeout.
// Sample while acquisition is still alive and awaiting more body bytes, not
// after EOF/cleanup when the old shared-promise reaction chain could disappear.
import assert from 'node:assert/strict';
import { createImageAcquirer } from '../../src/images/acquire.js';

const mode = process.argv[2];
const url = 'https://images.example.test/probe.png';
const permissions = { contains: async () => true };
if (mode.startsWith('heap-')) {
  let count = 0; let release; let reached;
  const reused = Uint8Array.of(1);
  const checkpoint = () => new Promise(resolve => { reached = resolve; });
  let checkpointReached = checkpoint();
  const stream = new ReadableStream({
    pull(controller) {
      if (count === 1000 || count === 100000) {
        reached();
        return new Promise(resolve => { release = () => { count++; controller.enqueue(mode === 'heap-reused' ? reused : Uint8Array.of(1)); resolve(); }; });
      }
      count++;
      controller.enqueue(mode === 'heap-reused' ? reused : Uint8Array.of(1));
    },
  }, { highWaterMark: 0 });
  const abort = new AbortController();
  const pending = createImageAcquirer({ permissions, timeoutMs: 15000, fetch: async () => new Response(stream) }).acquire([url], { signal: abort.signal });
  const sample = async () => {
    await checkpointReached;
    await new Promise(resolve => setImmediate(resolve));
    global.gc(); global.gc();
    return process.memoryUsage();
  };
  const small = await sample();
  checkpointReached = checkpoint(); release();
  const large = await sample();
  abort.abort();
  assert.equal((await pending)[0].error.code, 'cancel');
  console.log(JSON.stringify({ mode, counts: [1000, 100000], heapGrowth: large.heapUsed - small.heapUsed, bufferGrowth: large.arrayBuffers - small.arrayBuffers }));
} else {
  let reads = 0; let cancelled = 0; let nativeSignal;
  const abort = new AbortController();
  const stream = new ReadableStream({
    pull(controller) { reads++; controller.enqueue(new Uint8Array(mode.includes('empty') ? 0 : 1)); },
    // A broken source must not hold acquisition or cleanup hostage.
    cancel() { cancelled++; return new Promise(() => {}); },
  }, { highWaterMark: 0 });
  const api = createImageAcquirer({ permissions, timeoutMs: mode.endsWith('abort') ? 1000 : 20,
    fetch: async (_, init) => { nativeSignal = init.signal; return new Response(stream); } });
  if (mode.endsWith('abort')) setTimeout(() => abort.abort(), 20);
  const start = performance.now();
  const result = (await api.acquire([url], { signal: abort.signal }))[0];
  assert.equal(result.error.code, mode.endsWith('abort') ? 'cancel' : 'timeout');
  assert.equal(nativeSignal.aborted, true);
  assert.equal(cancelled, 1);
  assert.ok(reads > 0);
  console.log(JSON.stringify({ mode, reads, elapsedMs: performance.now() - start, code: result.error.code, cancelled }));
}
