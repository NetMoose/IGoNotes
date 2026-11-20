import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { afterEach, expect, it, vi } from 'vitest';
import { createImageAcquirer } from '../src/images/acquire.js';

const probe = new URL('./helpers/images-acquire-probe.js', import.meta.url);
const permissions = { contains: async () => true };
const url = 'https://images.example.test/probe.png';
const png = Uint8Array.from(Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6Z1sAAAAASUVORK5CYII=', 'base64'));
afterEach(() => vi.restoreAllMocks());

it.each(['heap-fresh', 'heap-reused'])('bounds live bookkeeping across 100k one-byte reads (%s)', mode => {
  const output = execFileSync(process.execPath, ['--expose-gc', probe.pathname, mode], { timeout: 10000, encoding: 'utf8', maxBuffer: 65536 });
  const report = JSON.parse(output);
  expect(report.counts).toEqual([1000, 100000]);
  // Generous allowance for runtime/stream noise, not an exact V8 heap value:
  // 99k additional bytes cannot justify megabytes of per-read bookkeeping.
  // Old code retains ~37–56 MiB here, even with a single reused source array.
  expect(report.heapGrowth, output).toBeLessThan(8 * 1024 * 1024);
  expect(report.bufferGrowth, output).toBeLessThan(1024 * 1024);
});

it.each(['empty-timeout', 'empty-abort', 'tiny-timeout'])('yields to clock/abort during a microtask-only %s stream with hanging source.cancel', mode => {
  const output = execFileSync(process.execPath, [probe.pathname, mode], { timeout: 3000, encoding: 'utf8', maxBuffer: 65536 });
  expect(JSON.parse(output)).toMatchObject({ code: mode.endsWith('abort') ? 'cancel' : 'timeout', cancelled: 1 });
});

it('copies a reused source buffer before the next read for every byte stride', async () => {
  for (let stride = 1; stride <= png.length; stride++) {
    const buffer = new Uint8Array(stride); let offset = 0;
    const stream = new ReadableStream({ pull(controller) {
      if (offset === png.length) { buffer.fill(0); controller.close(); return; }
      const length = Math.min(stride, png.length - offset);
      buffer.set(png.subarray(offset, offset + length)); offset += length;
      controller.enqueue(buffer.subarray(0, length));
    } }, { highWaterMark: 0 });
    const api = createImageAcquirer({ permissions, fetch: async () => new Response(stream) });
    const result = (await api.acquire([url]))[0];
    expect(result.ok, `stride ${stride}: ${JSON.stringify(result.error)}`).toBe(true);
    expect(new Uint8Array(await result.blob.arrayBuffer())).toEqual(png);
    expect(result.sha256).toBe(createHash('sha256').update(png).digest('hex'));
  }
});

it('preserves a 100k-byte image across slab boundaries and highly fragmented/empty reads', async () => {
  // Insert a CRC-correct ancillary chunk, keeping the original raster bytes.
  const chunk = new Uint8Array(100000 - png.length);
  const view = new DataView(chunk.buffer);
  view.setUint32(0, chunk.length - 12);
  chunk.set(new TextEncoder().encode('tEXtComment\0'), 4);
  chunk.fill(120, 16, chunk.length - 4);
  let crc = 0xffffffff;
  for (const byte of chunk.subarray(4, -4)) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0);
  }
  view.setUint32(chunk.length - 4, (crc ^ 0xffffffff) >>> 0);
  const bytes = new Uint8Array(png.length + chunk.length);
  bytes.set(png.subarray(0, 33)); bytes.set(chunk, 33); bytes.set(png.subarray(33), 33 + chunk.length);
  const expectedHash = createHash('sha256').update(bytes).digest('hex');
  for (const stride of [1, 17, 65535, 65536, 65537, bytes.length]) {
    const reused = new Uint8Array(stride); let offset = 0; let empty = false;
    const stream = new ReadableStream({ pull(controller) {
      if (offset === bytes.length) { reused.fill(0); controller.close(); return; }
      empty = !empty;
      if (empty) { controller.enqueue(new Uint8Array(0)); return; }
      const length = Math.min(stride, bytes.length - offset);
      reused.set(bytes.subarray(offset, offset + length)); offset += length;
      controller.enqueue(reused.subarray(0, length));
    } }, { highWaterMark: 0 });
    const result = (await createImageAcquirer({ permissions, fetch: async () => new Response(stream) }).acquire([url]))[0];
    expect(result).toMatchObject({ ok: true, size: bytes.length, sha256: expectedHash });
    expect(new Uint8Array(await result.blob.arrayBuffer())).toEqual(bytes);
  }
});

it.each(['timeout', 'cancel'])('keeps the asynchronous hash guarded by %s', async code => {
  const caller = new AbortController();
  const digest = vi.fn(() => new Promise(() => {}));
  const api = createImageAcquirer({ permissions, timeoutMs: code === 'timeout' ? 10 : 1000,
    crypto: { subtle: { digest } }, fetch: async () => new Response(png) });
  const result = api.acquire([url], { signal: caller.signal });
  if (code === 'cancel') setTimeout(() => caller.abort(), 10);
  expect((await result)[0].error.code).toBe(code);
  expect(digest).toHaveBeenCalledOnce();
});

it('removes each wait abort subscription on settlement, and the caller subscription on completion', async () => {
  const active = new Map(); let peak = 0; let registrations = 0;
  const add = AbortSignal.prototype.addEventListener;
  const remove = AbortSignal.prototype.removeEventListener;
  vi.spyOn(AbortSignal.prototype, 'addEventListener').mockImplementation(function (type, listener, options) {
    if (type === 'abort') { const listeners = active.get(this) ?? new Set(); listeners.add(listener); active.set(this, listeners); peak = Math.max(peak, listeners.size); registrations++; }
    return add.call(this, type, listener, options);
  });
  vi.spyOn(AbortSignal.prototype, 'removeEventListener').mockImplementation(function (type, listener, options) {
    if (type === 'abort') active.get(this)?.delete(listener);
    return remove.call(this, type, listener, options);
  });
  let offset = 0;
  const stream = new ReadableStream({ pull(controller) {
    if (offset === png.length) controller.close(); else controller.enqueue(png.slice(offset, ++offset));
  } }, { highWaterMark: 0 });
  const caller = new AbortController();
  const result = (await createImageAcquirer({ permissions, fetch: async () => new Response(stream) }).acquire([url], { signal: caller.signal }))[0];
  expect(result.ok).toBe(true);
  expect(registrations).toBeGreaterThan(png.length); // actual removable per-read guards
  expect(peak).toBeLessThanOrEqual(1);
  expect([...active.values()].every(listeners => listeners.size === 0)).toBe(true);
});
