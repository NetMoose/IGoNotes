import { describe, it, expect, vi } from 'vitest';
import { createServer } from 'node:http';
import { createConnectionStore, validateEndpoint, validateToken, DEFAULT_ENDPOINT } from '../src/connection/settings.js';
import { createTransport } from '../src/connection/transport.js';

export const info = () => ({ protocol_version: 1, app_name: 'IGoNotes', app_version: 'dev',
  limits: { markdown_bytes: 5242880, assets: 100, asset_bytes: 10485760, total_asset_bytes: 52428800,
    multipart_bytes: 67108864, image_pixels: 40000000, gif_frames: 100, gif_total_pixels: 200000000 },
  supported_image_types: ['image/png', 'image/jpeg', 'image/gif', 'image/webp'] });
const json = (value, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } });
const token = 'opaque-test-credential';

describe('connection policy', () => {
  it('defaults and normalizes only manifest-supported loopback origins with custom ports', () => {
    expect(DEFAULT_ENDPOINT).toBe('http://127.0.0.1:8080');
    expect(validateEndpoint('http://localhost:9123/')).toBe('http://localhost:9123');
    expect(validateEndpoint('http://127.1:65535')).toBe('http://127.0.0.1:65535');
  });
  it.each(['https://localhost', 'http://example.org', 'http://127.0.0.2', 'http://[::1]',
    'http://@localhost', 'http://user@localhost', 'http://localhost:0', 'http://localhost:65536',
    'http://localhost/path', 'http://localhost/../', 'http://localhost?', 'http://localhost#',
    'http:localhost', 'http:///localhost', ' http://localhost', 'http://localhost\\x', 'http://localhost:abc'])('rejects %s', value => {
    expect(() => validateEndpoint(value)).toThrow();
  });
  it.each(['', 'has space', 'x\r\nAuthorization:x', 'é', 'a'.repeat(4097)])('rejects invalid opaque credentials', value => {
    expect(() => validateToken(value)).toThrow();
  });
});

describe('trusted local storage', () => {
  it('awaits restriction before every operation and clears only connection settings', async () => {
    const calls = [];
    let release;
    const local = { setAccessLevel: vi.fn(() => new Promise(resolve => { release = resolve; })),
      get: vi.fn(async key => { calls.push(['get', key]); return {}; }),
      set: vi.fn(async data => calls.push(['set', data])), remove: vi.fn(async key => calls.push(['remove', key])) };
    const store = createConnectionStore({ chrome: { storage: { local } } });
    const load = store.load();
    expect(calls).toEqual([]);
    expect(local.setAccessLevel).toHaveBeenCalledWith({ accessLevel: 'TRUSTED_CONTEXTS' });
    release();
    expect(await load).toEqual({ endpoint: DEFAULT_ENDPOINT, token: '' });
    const save = store.save({ endpoint: 'http://localhost:9090/', token });
    release(); await save;
    const clear = store.clear(); release(); await clear;
    expect(calls.map(([method]) => method)).toEqual(['get', 'set', 'remove']);
    expect(calls[1][1]).toEqual({ clipperConnection: { endpoint: 'http://localhost:9090', token } });
    expect(calls[2][1]).toBe('clipperConnection');
  });
  it('fails closed without access control or when restriction fails, sanitizing failures', async () => {
    for (const setAccessLevel of [undefined, async () => { throw new Error(token); }]) {
      const get = vi.fn(); const set = vi.fn(); const remove = vi.fn();
      const store = createConnectionStore({ chrome: { storage: { local: { setAccessLevel, get, set, remove } } } });
      for (const action of [() => store.load(), () => store.save({ endpoint: DEFAULT_ENDPOINT, token }), () => store.clear()]) {
        await expect(action()).rejects.toMatchObject({ code: 'storage_unavailable' });
        await action().catch(error => expect(error.message).not.toContain(token));
      }
      expect(get).not.toHaveBeenCalled(); expect(set).not.toHaveBeenCalled(); expect(remove).not.toHaveBeenCalled();
    }
  });
  it('sanitizes native read/write/remove failures and permits a later protected retry', async () => {
    for (const method of ['get', 'set', 'remove']) {
      const local = { setAccessLevel: vi.fn(async () => {}), get: vi.fn(async () => ({})),
        set: vi.fn(async () => {}), remove: vi.fn(async () => {}) };
      local[method].mockRejectedValueOnce(new Error(token));
      const store = createConnectionStore({ chrome: { storage: { local } } });
      const action = method === 'get' ? () => store.load() : method === 'set' ?
        () => store.save({ endpoint: DEFAULT_ENDPOINT, token }) : () => store.clear();
      const error = await action().then(() => { throw new Error('Expected rejection'); }, cause => cause);
      expect(error.code).toBe('storage_unavailable');
      expect(String(error)).not.toContain(token);
      expect(error.cause).toBeUndefined();
      await action();
      expect(local.setAccessLevel).toHaveBeenCalledTimes(2);
    }
  });
});

describe('restricted transport', () => {
  it('takes a fixed snapshot; exposes only read v1 methods and encodes query components', async () => {
    const fetch = vi.fn(async url => json(url.endsWith('/info') ? info() : url.includes('/folders') ?
      { base: 'a&b', folders: [''], destination_revision: 'opaque' } : { bases: ['a&b'] }));
    const settings = { endpoint: 'http://localhost:9123', token };
    const api = createTransport(settings, { fetch });
    settings.endpoint = 'http://example.org'; settings.token = 'changed';
    expect(Object.keys(api).sort()).toEqual(['bases', 'folders', 'importStatus', 'info', 'postImport']);
    await api.info(); await api.bases(); await api.folders('a&b');
    expect(fetch.mock.calls.map(([url]) => url)).toEqual([
      'http://localhost:9123/api/clipper/v1/info', 'http://localhost:9123/api/clipper/v1/bases',
      'http://localhost:9123/api/clipper/v1/folders?base=a%26b']);
    for (const [url, options] of fetch.mock.calls) {
      expect(url).not.toContain(token);
      expect(options).toMatchObject({ method: 'GET', credentials: 'omit', redirect: 'error', cache: 'no-store',
        headers: { Authorization: `Bearer ${token}` } });
      expect(options.headers['Content-Type']).toBeUndefined();
      expect(options.signal).toBeInstanceOf(AbortSignal);
    }
  });
  it('rejects invalid endpoints/tokens before fetching', () => {
    const fetch = vi.fn();
    expect(() => createTransport({ endpoint: 'http://example.org', token }, { fetch })).toThrow();
    expect(() => createTransport({ endpoint: DEFAULT_ENDPOINT, token: '' }, { fetch })).toThrow();
    expect(fetch).not.toHaveBeenCalled();
  });
  it('rejects malformed folder arguments before fetch with static errors', async () => {
    const fetch = vi.fn();
    const api = createTransport({ endpoint: DEFAULT_ENDPOINT, token }, { fetch });
    for (const base of ['', null, ' ', '\ud800', 'a'.repeat(1025)]) {
      await expect(api.folders(base)).rejects.toMatchObject({ code: 'invalid_request' });
    }
    expect(fetch).not.toHaveBeenCalled();
  });
  it('validates exact version and every negotiated DTO field including image caps', async () => {
    for (const value of ['1', 2, null]) {
      await expect(createTransport({ endpoint: DEFAULT_ENDPOINT, token }, { fetch: async () => json({ ...info(), protocol_version: value }) }).info())
        .rejects.toMatchObject({ code: 'protocol_mismatch' });
    }
    for (const key of Object.keys(info().limits)) {
      const value = info(); delete value.limits[key];
      await expect(createTransport({ endpoint: DEFAULT_ENDPOINT, token }, { fetch: async () => json(value) }).info())
        .rejects.toMatchObject({ code: 'invalid_response' });
    }
    for (const value of [{ ...info(), app_version: '' }, { ...info(), supported_image_types: ['image/svg+xml'] }]) {
      await expect(createTransport({ endpoint: DEFAULT_ENDPOINT, token }, { fetch: async () => json(value) }).info())
        .rejects.toMatchObject({ code: 'invalid_response' });
    }
  });
  it('maps setup/auth/unavailable separately without reflecting bodies or network errors', async () => {
    for (const [status, code] of [[401, 'unauthorized'], [403, 'unauthorized'], [428, 'setup_required'], [503, 'unavailable']]) {
      const api = createTransport({ endpoint: DEFAULT_ENDPOINT, token }, { fetch: async () => new Response(`<html>${token}`, { status }) });
      await expect(api.info()).rejects.toMatchObject({ code });
      await api.info().catch(error => expect(String(error)).not.toContain(token));
    }
    for (const fetch of [async () => { throw new TypeError(token); }, async () => new Response(token),
      async () => new Response(token, { headers: { 'Content-Type': 'application/json' } }),
      async () => ({ ok: true, redirected: true })]) {
      const api = createTransport({ endpoint: DEFAULT_ENDPOINT, token }, { fetch });
      await api.info().then(() => { throw new Error('Expected rejection'); }, error => {
        expect(['unavailable', 'invalid_response']).toContain(error.code);
        expect(String(error)).not.toContain(token);
      });
    }
  });
  it('native fetch refuses a real HTTP redirect without forwarding Bearer to its target', async () => {
    const paths = [];
    const server = createServer((request, response) => {
      paths.push(request.url);
      response.writeHead(302, { Location: '/redirect-target' });
      response.end(token);
    });
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    try {
      const api = createTransport({ endpoint: `http://127.0.0.1:${server.address().port}`, token });
      await expect(api.info()).rejects.toMatchObject({ code: 'unavailable' });
      expect(paths).toEqual(['/api/clipper/v1/info']);
    } finally {
      server.closeAllConnections();
      await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
    }
  });
  it('closes the request context when rejecting an unread HTML/error body', async () => {
    let requestSignal;
    const api = createTransport({ endpoint: DEFAULT_ENDPOINT, token }, { fetch: async (_url, options) => {
      requestSignal = options.signal;
      return new Response('<html>not a JSON API', { headers: { 'Content-Type': 'text/html' } });
    } });
    await expect(api.info()).rejects.toMatchObject({ code: 'invalid_response' });
    expect(requestSignal.aborted).toBe(true);
  });
  it('bounds response body parsing and distinguishes caller abort, including pre-abort', async () => {
    vi.useFakeTimers();
    try {
      const fetch = async () => ({ ok: true, headers: new Headers({ 'Content-Type': 'application/json' }), json: () => new Promise(() => {}) });
      const api = createTransport({ endpoint: DEFAULT_ENDPOINT, token }, { fetch });
      const timed = expect(api.info()).rejects.toMatchObject({ code: 'timeout' });
      await vi.advanceTimersByTimeAsync(10000); await timed;
      const controller = new AbortController();
      const aborted = expect(api.info({ signal: controller.signal })).rejects.toMatchObject({ code: 'cancelled' });
      controller.abort(new Error(token)); await aborted;
      const neverFetch = vi.fn();
      await expect(createTransport({ endpoint: DEFAULT_ENDPOINT, token }, { fetch: neverFetch }).info({ signal: controller.signal }))
        .rejects.toMatchObject({ code: 'cancelled' });
      expect(neverFetch).not.toHaveBeenCalled();
      expect(vi.getTimerCount()).toBe(0);
    } finally { vi.useRealTimers(); }
  });
});
