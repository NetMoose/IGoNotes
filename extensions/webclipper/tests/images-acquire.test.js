import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createImageAcquirer } from '../src/images/acquire.js';

const create = createImageAcquirer;
const png = Uint8Array.from(Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6Z1sAAAAASUVORK5CYII=', 'base64'));
const gif = Uint8Array.from(Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==', 'base64'));
const url = 'https://images.example.test/a.png';
const data = (bytes = png, mime = 'image/png') => `data:${mime};base64,${Buffer.from(bytes).toString('base64')}`;
const permissions = granted => ({ contains: vi.fn(async () => granted), request: vi.fn(async () => granted) });
const response = (bytes = png, mime = 'image/png', headers = {}) => new Response(bytes, { headers: { 'Content-Type': mime, ...headers } });
const make = (options = {}) => {
  const perms = permissions(true);
  const fetch = vi.fn(async () => response());
  const acquirer = create({ permissions: perms, fetch, ...options });
  return { ...acquirer, perms, fetch: options.fetch ?? fetch };
};
afterEach(() => vi.restoreAllMocks());

describe('gesture-only access', () => {
  it('requests exact unique validated host patterns synchronously before any await or contains', async () => {
    const events = [];
    const perms = {
      contains: vi.fn(() => { throw Error('must not query before request'); }),
      request: vi.fn(origins => { events.push(origins); return Promise.resolve(true); }),
    };
    const api = create({ permissions: perms });
    const result = api.requestAccessFromUserGesture([url, `${url}#a`, 'https://images.example.test/b', 'http://other.example.test:8123/a', data()]);
    expect(events).toEqual([{ origins: ['https://images.example.test/*', 'http://other.example.test/*'] }]);
    expect(await result).toEqual({ ok: true, origins: events[0].origins });
    expect(perms.contains).not.toHaveBeenCalled();
  });
  it.each([false, 'throw', 'reject'])('reports permission denial/platform failure (%s) without broadening', async mode => {
    const perms = permissions(false);
    perms.request = vi.fn(() => { if (mode === 'throw') throw Error('private native error'); return mode === 'reject' ? Promise.reject(Error('private native error')) : Promise.resolve(false); });
    const api = create({ permissions: perms });
    expect(await api.requestAccessFromUserGesture([url])).toMatchObject({ ok: false, error: { code: 'permission' } });
    expect(perms.request).toHaveBeenCalledTimes(1);
    expect((await api.acquire([url]))[0]).toMatchObject({ ok: false, error: { code: 'permission' } });
  });
  it('does not request hosts for data, invalid sources, or an empty list', async () => {
    const { requestAccessFromUserGesture, perms } = make();
    expect(await requestAccessFromUserGesture([data()])).toEqual({ ok: true, origins: [] });
    expect(await requestAccessFromUserGesture([])).toEqual({ ok: true, origins: [] });
    expect(await requestAccessFromUserGesture([url, 'http://127.1/a'])).toMatchObject({ ok: false, error: { code: 'unsafe' } });
    expect(perms.request).not.toHaveBeenCalled();
  });
  it.each(['https://*/a', 'https://*.example.test/a', 'http://%2A/a', 'https://foo*.example.test/a'])('never turns a URL hostname into a broad permission pattern: %s', async value => {
    const { requestAccessFromUserGesture, acquire, perms, fetch } = make();
    expect(await requestAccessFromUserGesture([value])).toMatchObject({ ok: false, error: { code: 'unsafe' } });
    expect((await acquire([value]))[0].error.code).toBe('unsafe');
    expect(perms.request).not.toHaveBeenCalled(); expect(perms.contains).not.toHaveBeenCalled(); expect(fetch).not.toHaveBeenCalled();
  });
});

describe('isolated network acquisition', () => {
  it('preserves bytes, hashes bare SHA256, normalizes fragments but retains every alias', async () => {
    const fetch = vi.fn(async () => response()); const perms = permissions(true);
    const { acquire } = create({ fetch, permissions: perms, get connection() { throw Error('must never read credentials'); } });
    const results = await acquire([`${url}#first`, `${url}#second`, url]);
    expect(results).toHaveLength(3);
    for (const [i, result] of results.entries()) {
      expect(result).toMatchObject({ ok: true, url: [`${url}#first`, `${url}#second`, url][i], normalizedUrl: url, mediaType: 'image/png', extension: 'png', size: png.length, sha256: createHash('sha256').update(png).digest('hex') });
      expect(new Uint8Array(await result.blob.arrayBuffer())).toEqual(png);
      expect(result.blob.type).toBe('image/png');
    }
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(perms.contains).toHaveBeenCalledWith({ origins: ['https://images.example.test/*'] });
    expect(perms.request).not.toHaveBeenCalled();
    const init = fetch.mock.calls[0][1];
    expect(init).toMatchObject({ credentials: 'omit', redirect: 'error', cache: 'no-store', referrerPolicy: 'no-referrer' });
    expect([...new Headers(init.headers).keys()]).toEqual([]);
  });
  it('checks contains and never auto-prompts even on a platform failure', async () => {
    for (const contains of [async () => false, async () => { throw Error('platform failure'); }]) {
      const fetch = vi.fn(); const request = vi.fn();
      const results = await create({ permissions: { contains, request }, fetch }).acquire([url]);
      expect(results[0]).toMatchObject({ ok: false, error: { code: 'permission' } });
      expect(fetch).not.toHaveBeenCalled(); expect(request).not.toHaveBeenCalled();
    }
  });
  it('does not start fetch when cancellation races a successful permission result', async () => {
    const controller = new AbortController(); const fetch = vi.fn(async () => response());
    const api = create({ permissions: { contains: () => { controller.abort(); return Promise.resolve(true); } }, fetch });
    expect((await api.acquire([url], { signal: controller.signal }))[0].error.code).toBe('cancel');
    expect(fetch).not.toHaveBeenCalled();
  });
  it.each([
    [() => new Response(null, { status: 404 }), 'http', 404],
    [() => new Response(null, { status: 302, headers: { Location: 'https://cdn.example.test/a' } }), 'redirect'],
    [() => { throw new TypeError('native secret'); }, 'network'],
  ])('returns fixed HTTP/redirect/network failures without native details', async (reply, code, status) => {
    const { acquire, fetch } = make({ fetch: vi.fn(reply) });
    const result = (await acquire([url]))[0];
    expect(result).toMatchObject({ ok: false, error: { code, ...(status ? { status } : {}) } });
    expect(JSON.stringify(result)).not.toContain('native secret');
    if (code === 'network') expect(result.error.message).toMatch(/redirect/i);
    expect(fetch).toHaveBeenCalledTimes(1);
  });
  it.each([undefined, '1', '999'])('counts streamed bytes with missing/lying/oversized Content-Length (%s)', async length => {
    const cancel = vi.fn(); let signal;
    const fetch = vi.fn(async (_, init) => {
      signal = init.signal;
      return new Response(new ReadableStream({ start(c) { c.enqueue(new Uint8Array(11)); }, cancel }), { headers: length ? { 'Content-Length': length } : {} });
    });
    const result = (await create({ permissions: permissions(true), fetch, limits: { asset_bytes: 10 } }).acquire([url]))[0];
    expect(result.error.code).toBe('tooLarge'); expect(signal.aborted).toBe(true); expect(cancel).toHaveBeenCalledOnce();
  });
  it('cancels body consumption on deadline and caller abort, including a hanging read', async () => {
    for (const callerAbort of [false, true]) {
      const cancel = vi.fn(); const controller = new AbortController(); let signal;
      const fetch = vi.fn(async (_, init) => { signal = init.signal; return new Response(new ReadableStream({ cancel })); });
      const api = create({ permissions: permissions(true), fetch, timeoutMs: 20 });
      const pending = api.acquire([url], { signal: controller.signal });
      if (callerAbort) setTimeout(() => controller.abort(), 5);
      expect((await pending)[0].error.code).toBe(callerAbort ? 'cancel' : 'timeout');
      expect(signal.aborted).toBe(true); expect(cancel).toHaveBeenCalledOnce();
    }
  });
  it('bounds permission/fetch waits and handles cancellation before starting', async () => {
    const api = create({ permissions: { contains: () => new Promise(() => {}) }, timeoutMs: 10 });
    expect((await api.acquire([url]))[0].error.code).toBe('timeout');
    const controller = new AbortController(); controller.abort();
    const { acquire, fetch, perms } = make();
    expect((await acquire([url, `${url}?2`], { signal: controller.signal })).map(x => x.error.code)).toEqual(['cancel', 'cancel']);
    expect(fetch).not.toHaveBeenCalled(); expect(perms.contains).not.toHaveBeenCalled();
  });
  it('bounds a fetch that never resolves and cancels all queued entries visibly', async () => {
    const signals = [];
    const api = create({ permissions: permissions(true), timeoutMs: 10, fetch: (_, init) => { signals.push(init.signal); return new Promise(() => {}); } });
    expect((await api.acquire([url]))[0].error.code).toBe('timeout');
    expect(signals[0].aborted).toBe(true);
    const controller = new AbortController();
    const pending = api.acquire(Array.from({ length: 8 }, (_, i) => `${url}?${i}`), { signal: controller.signal });
    setTimeout(() => controller.abort(), 1);
    expect((await pending).map(x => x.error.code)).toEqual(Array(8).fill('cancel'));
    expect(signals).toHaveLength(4);
  });
  it('counts all chunks and accepts exactly the byte cap without using unbounded body helpers', async () => {
    const cancel = vi.fn();
    const body = new ReadableStream({ start(c) { c.enqueue(png.slice(0, 20)); c.enqueue(png.slice(20)); c.close(); }, cancel });
    const reply = new Response(body);
    reply.arrayBuffer = reply.blob = () => { throw Error('unbounded body helper'); };
    const api = make({ limits: { asset_bytes: png.length }, fetch: async () => reply });
    expect((await api.acquire([url]))[0]).toMatchObject({ ok: true, size: png.length });
    expect(cancel).not.toHaveBeenCalled();
    const overflow = new ReadableStream({ start(c) { c.enqueue(png); c.enqueue(Uint8Array.of(0)); }, cancel });
    expect((await make({ limits: { asset_bytes: png.length }, fetch: async () => new Response(overflow) }).acquire([url]))[0].error.code).toBe('tooLarge');
    expect(cancel).toHaveBeenCalledOnce();
  });
  it('uses at most three workers, preserves order and includes every failed entry', async () => {
    let active = 0; let peak = 0;
    const fetch = vi.fn(async input => {
      active++; peak = Math.max(peak, active);
      await new Promise(resolve => setTimeout(resolve, 5)); active--;
      return input.endsWith('?4') ? new Response(null, { status: 404 }) : response();
    });
    const urls = Array.from({ length: 11 }, (_, i) => `${url}?${i}`);
    const results = await create({ permissions: permissions(true), fetch }).acquire(urls);
    expect(peak).toBe(3); expect(results.map(x => x.url)).toEqual(urls);
    expect(results[4].error.code).toBe('http'); expect(results.filter(x => x.ok)).toHaveLength(10);
  });
});

describe('source policy and data quotas', () => {
  it.each(['http://127.0.0.1/a', 'http://127.1/a', 'http://2130706433/a', 'http://0x7f000001/a', 'http://0177.0.0.1/a', 'http://0/a', 'http://10.1.2.3/a', 'http://172.16.0.1/a', 'http://192.168.1.1/a', 'http://169.254.1.1/a', 'http://localhost/a', 'http://foo.localhost./a', 'http://[::]/a', 'http://[::1]/a', 'http://[fc00::1]/a', 'http://[fd12::1]/a', 'http://[fe80::1]/a', 'http://[::ffff:127.0.0.1]/a', 'http://[::ffff:c0a8:101]/a', 'http://user:pass@public.example/a', 'http://@public.example/a', 'blob:https://images.example.test/a', 'file:///tmp/a', 'javascript:alert(1)', 'data:image/svg+xml,%3Csvg%3E'])('rejects unsafe source before permission or fetch: %s', async source => {
    const { acquire, fetch, perms } = make();
    expect((await acquire([source]))[0].error.code).toBe('unsafe');
    expect(fetch).not.toHaveBeenCalled(); expect(perms.contains).not.toHaveBeenCalled();
  });
  it('accepts public IPv4, public IPv6 and ordinary names (literal policy, not DNS defense)', async () => {
    const { acquire } = make();
    expect((await acquire(['https://8.8.8.8/a', 'https://[2606:4700:4700::1111]/a', url])).every(x => x.ok)).toBe(true);
  });
  it('decodes both bounded data forms preserving exact keys/bytes, without host access', async () => {
    const percent = `DATA:IMAGE/PNG,${[...png].map(n => `%${n.toString(16).padStart(2, '0')}`).join('')}`;
    const { acquire, fetch, perms } = make();
    for (const result of await acquire([data(), percent, data().replace(/=+$/, '')])) {
      expect(result.ok).toBe(true); expect(result.normalizedUrl).toBe(result.url);
      expect(new Uint8Array(await result.blob.arrayBuffer())).toEqual(png);
    }
    expect(fetch).not.toHaveBeenCalled(); expect(perms.contains).not.toHaveBeenCalled();
  });
  it.each(['data:image/png;base64,A', 'data:image/png;base64,AA=A', 'data:image/png,%Q0', 'data:image/png,a#fragment', 'data:image/png;foo=1,abc', 'data:image/png;base64, AA=='])('rejects malformed data: %s', async source => {
    expect((await make().acquire([source]))[0].error.code).toBe('unsafe');
  });
  it('preflights data decoded size before atob/allocation, and bounds source to 32 MiB', async () => {
    const atob = vi.spyOn(globalThis, 'atob');
    const api = make({ limits: { asset_bytes: 2 } });
    for (const source of [data(), 'data:image/png,%FF%FF%FF', `data:image/png,${'a'.repeat(32 * 1024 * 1024)}`]) {
      expect((await api.acquire([source]))[0].error.code).toBe('tooLarge');
    }
    expect(atob).not.toHaveBeenCalled();
  });
  it('allows a full 10 MiB percent-encoded payload through source/byte bounds before signature checking', async () => {
    const source = `data:image/png,${'%00'.repeat(10 * 1024 * 1024)}`;
    const { acquire, fetch } = make();
    // Deliberately not image bytes: the exact-budget payload reaches signature
    // checking, not tooLarge. One more decoded octet must fail before inspection.
    expect((await acquire([source]))[0].error.code).toBe('unsupported');
    expect((await acquire([`${source}%00`]))[0].error.code).toBe('tooLarge');
    expect(fetch).not.toHaveBeenCalled();
  });
});

// Minimal structural containers intentionally exercise preflight, not full decode.
const jpeg = Uint8Array.of(255,216,255,192,0,11,8,0,1,0,1,1,1,17,0,255,218,0,8,1,1,0,0,63,0,0,255,217);
const webp = Uint8Array.of(...Buffer.from('RIFF'),22,0,0,0,...Buffer.from('WEBPVP8L'),9,0,0,0,47,0,0,0,0,0,0,0,0,0);
describe('signature, MIME and structural budgets (server remains full validator)', () => {
  it.each([[png, 'image/png', 'png'], [jpeg, 'image/jpeg', 'jpg'], [gif, 'image/gif', 'gif'], [webp, 'image/webp', 'webp']])('infers actual bytes and rejects a conflicting MIME: %s', async (bytes, mediaType, extension) => {
    for (const mime of [mediaType, 'application/octet-stream', '']) {
      const api = make({ fetch: async () => response(bytes, mime) });
      expect((await api.acquire([url]))[0]).toMatchObject({ ok: true, mediaType, extension, size: bytes.length });
    }
    const other = mediaType === 'image/png' ? 'image/jpeg' : 'image/png';
    expect((await make({ fetch: async () => response(bytes, other) }).acquire([url]))[0].error.code).toBe('mimeMismatch');
    expect((await make().acquire([data(bytes, other)]))[0].error.code).toBe('mimeMismatch');
  });
  it.each([new Uint8Array(), Uint8Array.of(1,2,3), new TextEncoder().encode('<svg/>'), png.slice(0, 16), gif.slice(0, -2)])('rejects unsupported/truncated containers', async bytes => {
    expect((await make().acquire([data(bytes)]))[0].ok).toBe(false);
  });
  it('rejects PNG/GIF/JPEG/WebP dimensions above budget', async () => {
    for (const bytes of [png, gif, jpeg, webp]) {
      expect((await make({ limits: { image_pixels: 1 } }).acquire([data(bytes, bytes === gif ? 'image/gif' : bytes === jpeg ? 'image/jpeg' : bytes === webp ? 'image/webp' : 'image/png')]))[0].ok).toBe(true);
    }
    const largePNG = png.slice(); new DataView(largePNG.buffer).setUint32(16, 40000001);
    const largeGIF = gif.slice(); largeGIF.set([255,255,255,255], 6);
    const largeJPEG = jpeg.slice(); largeJPEG.set([255,255,255,255], 7);
    const largeWebP = webp.slice(); largeWebP.set([255,255,255,15], 21);
    for (const [bytes, mime] of [[largePNG,'image/png'],[largeGIF,'image/gif'],[largeJPEG,'image/jpeg'],[largeWebP,'image/webp']]) {
      expect((await make().acquire([data(bytes, mime)]))[0].error.code).toBe('tooLarge');
    }
  });
  it('walks all GIF frames, enforcing frame dimensions/count/total before any pixel decode', async () => {
    const start = gif.slice(0, 19); const frame = gif.slice(19, -1);
    const animated = count => Uint8Array.of(...start, ...Array.from({ length: count }, () => [...frame]).flat(), 59);
    expect((await make().acquire([data(animated(100), 'image/gif')]))[0].ok).toBe(true);
    expect((await make().acquire([data(animated(101), 'image/gif')]))[0].error.code).toBe('tooLarge');
    expect((await make({ limits: { gif_total_pixels: 1 } }).acquire([data(animated(2), 'image/gif')]))[0].error.code).toBe('tooLarge');
    const largeFrame = gif.slice(); largeFrame.set([255,255,255,255], 24);
    expect((await make().acquire([data(largeFrame, 'image/gif')]))[0].error.code).toBe('tooLarge');
  });
  it('accepts exact 40 MP/200 MP structural boundaries and rejects the next GIF frame', async () => {
    const canvas = gif.slice(0, 19); canvas.set([64,31,136,19], 6); // 8000 x 5000
    const frame = gif.slice(19, -1); frame.set([64,31,136,19], 5);
    const source = count => data(Uint8Array.of(...canvas, ...Array.from({ length: count }, () => [...frame]).flat(), 59), 'image/gif');
    expect((await make().acquire([source(5)]))[0].ok).toBe(true);
    expect((await make().acquire([source(6)]))[0].error.code).toBe('tooLarge');
    const lateTruncation = Uint8Array.of(...canvas, ...frame, ...frame.slice(0, -2));
    expect((await make().acquire([data(lateTruncation, 'image/gif')]))[0].error.code).toBe('unsupported');
  });
  it('rejects APNG and animated WebP instead of declaring them ready', async () => {
    const apng = Uint8Array.of(...png.slice(0, 33), 0,0,0,8, ...Buffer.from('acTL'), ...new Uint8Array(12), ...png.slice(33));
    const animated = Uint8Array.of(...Buffer.from('RIFF'),22,0,0,0,...Buffer.from('WEBPVP8X'),10,0,0,0,2,0,0,0,0,0,0,0,0,0);
    for (const [bytes, mime] of [[apng,'image/png'],[animated,'image/webp']]) expect((await make().acquire([data(bytes, mime)]))[0].error.code).toBe('unsupported');
  });
});

it('uses actual fetch streams: 404 and CDN redirect never contact the target or send credentials', async () => {
  const seen = [];
  const server = createServer((req, res) => {
    seen.push({ url: req.url, headers: req.headers });
    if (req.url === '/redirect') { res.writeHead(302, { Location: '/must-not-follow' }); res.end(); }
    else if (req.url === '/missing') { res.writeHead(404); res.end(); }
    else { res.writeHead(200, { 'Content-Type': 'image/png', 'Set-Cookie': 'imagehost=private' }); res.end(png); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  try {
    // Trusted test transport maps a public-name fixture to loopback. Product URL
    // policy sees only that name; this deliberately makes no DNS-rebinding claim.
    const api = create({ permissions: permissions(true), fetch: (input, init) => fetch(input.replace('images.example.test', `127.0.0.1:${server.address().port}`), init) });
    const results = await api.acquire(['http://images.example.test/ok', 'http://images.example.test/redirect', 'http://images.example.test/missing']);
    expect(results[0].ok).toBe(true); expect(results[1].error).toMatchObject({ code: 'network', message: expect.stringMatching(/redirect/i) });
    expect(results[2].error).toMatchObject({ code: 'http', status: 404 });
    expect(seen.map(x => x.url).sort()).toEqual(['/missing', '/ok', '/redirect']);
    expect(seen.every(x => !x.headers.authorization && !x.headers.cookie)).toBe(true);
  } finally { await new Promise(resolve => server.close(resolve)); }
});
