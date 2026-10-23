// Standalone trusted-editor API. No storage, connection credentials, worker
// messages, automatic retries, preview allocation or background capture hooks.
const SOURCE_LIMIT = 32 * 1024 * 1024;
const DEFAULT_LIMITS = Object.freeze({ asset_bytes: 10 * 1024 * 1024, image_pixels: 40000000, gif_frames: 100, gif_total_pixels: 200000000 });
const messages = Object.freeze({
  permission: 'Image host access was not granted. Use the explicit permission action and retry.',
  http: 'The image host returned an unsuccessful HTTP status.',
  redirect: 'Image redirects are blocked. Replace the source with a direct URL and grant that host access.',
  network: 'The image request failed or a redirect was blocked. Check the source or replace it with a direct URL.',
  timeout: 'Image acquisition timed out. Retry explicitly.',
  cancel: 'Image acquisition was cancelled.',
  unsafe: 'This image source is unsafe or has an unsupported URL scheme or data encoding.',
  unsupported: 'Unsupported image bytes or container. Use static PNG, JPEG, WebP or GIF.',
  mimeMismatch: 'The declared image MIME type does not match its bytes.',
  tooLarge: 'The image exceeds the byte, pixel or GIF frame budget.',
});
class AcquisitionError extends Error {
  constructor(code, status) { super(messages[code]); this.code = code; this.status = status; }
}
const fail = (code, status) => { throw new AcquisitionError(code, status); };
const errorValue = error => ({ code: error.code, message: error.message, ...(error.status === undefined ? {} : { status: error.status }) });

function privateIPv4(parts) {
  const [a, b] = parts;
  return a === 0 || a === 10 || a === 127 || (a === 169 && b === 254) ||
    (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) ||
    (a === 100 && b >= 64 && b <= 127) || a >= 224;
}

function privateHost(host) {
  host = host.toLowerCase().replace(/\.$/, '');
  if (host === 'localhost' || host.endsWith('.localhost')) return true;
  // WHATWG URL has already canonicalized short/octal/hex/integer IPv4 forms.
  if (/^\d+\.\d+\.\d+\.\d+$/.test(host)) return privateIPv4(host.split('.').map(Number));
  if (!host.startsWith('[')) return false;
  const text = host.slice(1, -1);
  const halves = text.split('::');
  const left = halves[0] ? halves[0].split(':') : [];
  const right = halves[1] ? halves[1].split(':') : [];
  const words = (halves.length === 1 ? left : [...left, ...Array(8 - left.length - right.length).fill('0'), ...right]).map(word => parseInt(word, 16));
  if (words.slice(0, 7).every(n => n === 0) && words[7] <= 1) return true;
  if ((words[0] & 0xfe00) === 0xfc00 || (words[0] & 0xffc0) === 0xfe80 || (words[0] & 0xff00) === 0xff00) return true;
  // IPv4-mapped (and deprecated compatible) addresses use the same IPv4 policy.
  if (words.slice(0, 5).every(n => n === 0) && (words[5] === 0xffff || words[5] === 0)) {
    return privateIPv4([words[6] >> 8, words[6] & 255, words[7] >> 8, words[7] & 255]);
  }
  return false;
}

function dataSource(value) {
  if (value.length > SOURCE_LIMIT) fail('tooLarge');
  const header = /^data:(image\/(?:png|jpeg|gif|webp))(;base64)?,/i.exec(value);
  if (!header) fail('unsafe');
  const payload = value.slice(header[0].length);
  if (!payload) fail('unsafe');
  let size;
  if (header[2]) {
    if (!/^[A-Za-z0-9+/]*={0,2}$/.test(payload) || (payload.includes('=') ? payload.length % 4 !== 0 : payload.length % 4 === 1)) fail('unsafe');
    const padding = payload.endsWith('==') ? 2 : payload.endsWith('=') ? 1 : 0;
    size = Math.floor(payload.length * 3 / 4) - padding;
  } else {
    size = 0;
    // Iterative: a repeated regex over a 32 MiB percent source can overflow the
    // regexp stack. Count decoded octets BEFORE allocating their buffer.
    for (let i = 0; i < payload.length; i++, size++) {
      if (payload[i] === '%') {
        if (!/^[0-9a-f]{2}$/i.test(payload.slice(i + 1, i + 3))) fail('unsafe');
        i += 2;
      } else if (!/[A-Za-z0-9!$&'()*+,;=:@/?._~-]/.test(payload[i])) fail('unsafe');
    }
  }
  return { normalizedUrl: value, payload, base64: !!header[2], size, mediaType: header[1].toLowerCase() };
}

function source(value) {
  if (typeof value !== 'string' || !value) fail('unsafe');
  if (/^data:/i.test(value)) return dataSource(value);
  let parsed;
  try { parsed = new URL(value); } catch { fail('unsafe'); }
  if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password ||
      /^[^/]*:\/\/[^/\\?#]*@/.test(value) || parsed.hostname.includes('*') || privateHost(parsed.hostname)) fail('unsafe');
  parsed.hash = '';
  // Chrome host match patterns ignore ports; never use wildcard host/scheme.
  // IPv6 pattern support is platform-dependent: failures stay permission errors.
  return { normalizedUrl: parsed.href, origin: `${parsed.protocol}//${parsed.hostname}/*` };
}

// Shared normalization/policy for the persistence adapter. Never prompts/fetches.
export function normalizeImageSource(value) { return source(value).normalizedUrl; }

// Revalidate cached/acquired descriptors outside IndexedDB transactions. A saved
// status alone is not evidence: check actual size, container/MIME and byte hash.
// Server full decoding remains authoritative. Only one image buffer at a time.
export async function inspectImageBlob(blob) {
  if (!(blob instanceof Blob) || !blob.size) fail('unsupported');
  if (blob.size > DEFAULT_LIMITS.asset_bytes) fail('tooLarge');
  const bytes = new Uint8Array(await blob.arrayBuffer());
  const [mediaType, extension] = inspect(bytes, DEFAULT_LIMITS);
  if (blob.type && blob.type !== mediaType) fail('mimeMismatch');
  const digest = await globalThis.crypto.subtle.digest('SHA-256', bytes);
  return { size: blob.size, mediaType, extension, sha256: [...new Uint8Array(digest)].map(n => n.toString(16).padStart(2, '0')).join('') };
}

function decodeData(info, limit) {
  if (info.size > limit) fail('tooLarge');
  const bytes = new Uint8Array(info.size);
  if (info.base64) {
    let decoded;
    try { decoded = atob(info.payload); } catch { fail('unsafe'); }
    for (let i = 0; i < decoded.length; i++) bytes[i] = decoded.charCodeAt(i);
  } else {
    let offset = 0;
    for (let i = 0; i < info.payload.length; i++) {
      if (info.payload[i] === '%') { bytes[offset++] = parseInt(info.payload.slice(i + 1, i + 3), 16); i += 2; }
      else bytes[offset++] = info.payload.charCodeAt(i);
    }
  }
  return bytes;
}

// Structural preflight only, without pixel buffers. This is deliberately NOT a
// port of the server's full decoder/CRC/entropy/container-integrity validation.
function inspect(bytes, limits) {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const need = (at, length) => { if (at < 0 || at + length > bytes.length) fail('unsupported'); };
  const text = (at, length) => { need(at, length); return String.fromCharCode(...bytes.subarray(at, at + length)); };
  const u16 = (at, little = false) => { need(at, 2); return view.getUint16(at, little); };
  const u32 = (at, little = false) => { need(at, 4); return view.getUint32(at, little); };
  const u24 = at => { need(at, 3); return bytes[at] + bytes[at + 1] * 256 + bytes[at + 2] * 65536; };
  const pixels = (width, height) => {
    if (!width || !height) fail('unsupported');
    if (width > Math.floor(limits.image_pixels / height)) fail('tooLarge');
    return width * height;
  };
  if (bytes.length >= 8 && text(0, 8) === '\x89PNG\r\n\x1a\n') {
    let dimensions = false; let payload = false;
    for (let at = 8; at < bytes.length;) {
      const length = u32(at); const type = text(at + 4, 4); need(at + 8, length + 4);
      if (!dimensions && (at !== 8 || type !== 'IHDR' || length !== 13)) fail('unsupported');
      if (type === 'IHDR') { pixels(u32(at + 8), u32(at + 12)); dimensions = true; }
      if (['acTL', 'fcTL', 'fdAT'].includes(type)) fail('unsupported');
      if (type === 'IDAT') payload = true;
      at += length + 12;
      if (type === 'IEND') {
        if (!dimensions || !payload || length !== 0 || at !== bytes.length) fail('unsupported');
        return ['image/png', 'png'];
      }
    }
    fail('unsupported');
  }
  if (bytes.length >= 6 && ['GIF87a', 'GIF89a'].includes(text(0, 6))) {
    need(0, 13); pixels(u16(6, true), u16(8, true));
    let at = 13 + (bytes[10] & 128 ? 3 * (2 ** ((bytes[10] & 7) + 1)) : 0);
    let frames = 0; let total = 0;
    const subblocks = () => {
      while (true) { need(at, 1); const length = bytes[at++]; if (!length) return; need(at, length); at += length; }
    };
    while (at < bytes.length) {
      const marker = bytes[at++];
      if (marker === 59) { if (!frames || at !== bytes.length) fail('unsupported'); return ['image/gif', 'gif']; }
      if (marker === 33) { need(at++, 1); subblocks(); continue; }
      if (marker !== 44) fail('unsupported');
      need(at, 9);
      const width = u16(at + 4, true); const height = u16(at + 6, true);
      const area = pixels(width, height);
      if (++frames > limits.gif_frames || area > limits.gif_total_pixels - total) fail('tooLarge');
      total += area;
      if (u16(at, true) + width > u16(6, true) || u16(at + 2, true) + height > u16(8, true)) fail('unsupported');
      const packed = bytes[at + 8]; at += 9;
      if (packed & 128) at += 3 * (2 ** ((packed & 7) + 1));
      need(at, 1); if (bytes[at] < 2 || bytes[at] > 8) fail('unsupported'); at++;
      subblocks();
    }
    fail('unsupported');
  }
  if (bytes.length >= 3 && bytes[0] === 255 && bytes[1] === 216 && bytes[2] === 255) {
    let at = 2; let dimensions = false;
    while (at < bytes.length) {
      if (bytes[at++] !== 255) fail('unsupported');
      while (bytes[at] === 255) at++;
      need(at, 1); const marker = bytes[at++];
      const length = u16(at); if (length < 2) fail('unsupported'); need(at, length);
      if ([0xc0,0xc1,0xc2,0xc3,0xc5,0xc6,0xc7,0xc9,0xca,0xcb,0xcd,0xce,0xcf].includes(marker)) {
        if (length < 8) fail('unsupported'); pixels(u16(at + 5), u16(at + 3)); dimensions = true;
      }
      if (marker === 218) {
        if (!dimensions || bytes[bytes.length - 2] !== 255 || bytes[bytes.length - 1] !== 217) fail('unsupported');
        return ['image/jpeg', 'jpg'];
      }
      at += length;
    }
    fail('unsupported');
  }
  if (bytes.length >= 12 && text(0, 4) === 'RIFF' && text(8, 4) === 'WEBP') {
    if (u32(4, true) + 8 !== bytes.length) fail('unsupported');
    let dimensions = false; let payload = false;
    for (let at = 12; at < bytes.length;) {
      const type = text(at, 4); const length = u32(at + 4, true); const start = at + 8; need(start, length + (length & 1));
      if (type === 'ANIM' || type === 'ANMF') fail('unsupported');
      if (type === 'VP8X') {
        if (length !== 10) fail('unsupported');
        if (bytes[start] & 2) fail('unsupported');
        pixels(u24(start + 4) + 1, u24(start + 7) + 1); dimensions = true;
      }
      if (type === 'VP8L') {
        if (length < 5 || bytes[start] !== 47) fail('unsupported');
        const bits = u32(start + 1, true); pixels((bits & 0x3fff) + 1, ((bits >>> 14) & 0x3fff) + 1); dimensions = payload = true;
      }
      if (type === 'VP8 ') {
        if (length < 10 || text(start + 3, 3) !== '\x9d\x01\x2a') fail('unsupported');
        pixels(u16(start + 6, true) & 0x3fff, u16(start + 8, true) & 0x3fff); dimensions = payload = true;
      }
      at = start + length + (length & 1);
    }
    if (!dimensions || !payload) fail('unsupported');
    return ['image/webp', 'webp'];
  }
  fail('unsupported');
}

export function createImageAcquirer({ permissions = globalThis.chrome?.permissions, fetch: fetchImage = globalThis.fetch, crypto = globalThis.crypto, timeoutMs = 10000, limits: configured = {} } = {}) {
  const limits = {};
  for (const [key, fallback] of Object.entries(DEFAULT_LIMITS)) {
    const value = configured[key] ?? fallback;
    if (!Number.isSafeInteger(value) || value <= 0) throw new TypeError(`Invalid image limit: ${key}`);
    limits[key] = Math.min(value, fallback);
  }
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0) throw new TypeError('Invalid image timeout');

  // Intentionally NOT async: request() must run on the handler's synchronous
  // stack, before contains(), fetch(), storage, or any other awaited operation.
  function requestAccessFromUserGesture(urls) {
    let origins;
    try { origins = [...new Set(urls.map(value => source(value).origin).filter(Boolean))]; }
    catch (error) { return Promise.resolve({ ok: false, error: errorValue(error instanceof AcquisitionError ? error : new AcquisitionError('unsafe')) }); }
    if (!origins.length) return Promise.resolve({ ok: true, origins });
    try {
      const requested = permissions.request({ origins });
      return Promise.resolve(requested).then(granted => granted ? { ok: true, origins } : { ok: false, origins, error: errorValue(new AcquisitionError('permission')) }, () => ({ ok: false, origins, error: errorValue(new AcquisitionError('permission')) }));
    } catch { return Promise.resolve({ ok: false, origins, error: errorValue(new AcquisitionError('permission')) }); }
  }

  async function acquireOne(info, signal) {
    const controller = new AbortController();
    let reader; let timer; let stopped;
    // A guard also makes custom test transports/permission APIs that ignore the
    // signal bounded. Each settled wait REMOVES its subscription: racing every
    // read against one pending Promise would retain O(read count) reactions.
    const wait = operation => new Promise((resolve, reject) => {
      let settled = false;
      const finish = (callback, value) => {
        if (settled) return;
        settled = true;
        controller.signal.removeEventListener('abort', aborted);
        callback(value);
      };
      const aborted = () => finish(reject, stopped);
      if (controller.signal.aborted) aborted();
      else controller.signal.addEventListener('abort', aborted, { once: true });
      // Observe late rejection even when already aborted. Only the outstanding
      // operation retains these handlers; no shared never-settled Promise does.
      Promise.resolve(operation).then(value => finish(resolve, value), error => finish(reject, error));
    });
    const stop = code => {
      if (stopped) return;
      stopped = new AcquisitionError(code);
      controller.abort();
      if (reader) void reader.cancel().catch(() => {});
    };
    const cancel = () => stop('cancel');
    try {
      if (signal?.aborted) fail('cancel');
      signal?.addEventListener('abort', cancel, { once: true });
      timer = setTimeout(() => stop('timeout'), timeoutMs);
      let bytes; let declared = info.mediaType;
      if (info.origin) {
        let allowed;
        try { allowed = await wait(permissions.contains({ origins: [info.origin] })); }
        catch (error) { if (stopped) throw stopped; fail('permission'); }
        if (stopped) throw stopped;
        if (!allowed) fail('permission');
        const pending = fetchImage(info.normalizedUrl, {
          method: 'GET', credentials: 'omit', redirect: 'error', cache: 'no-store',
          referrerPolicy: 'no-referrer', headers: {}, signal: controller.signal,
        });
        // If a non-native transport ignores abort and resolves late, cancel its
        // body too rather than leave an unconsumed stream behind.
        const response = await wait(Promise.resolve(pending).then(value => {
          if (stopped) { void value.body?.cancel().catch(() => {}); throw stopped; }
          return value;
        }));
        reader = response.body?.getReader();
        if (response.redirected || response.type === 'opaqueredirect' || (response.status >= 300 && response.status < 400)) fail('redirect');
        if (!response.ok) fail('http', response.status);
        const length = response.headers.get('Content-Length');
        if (length && /^\d+$/.test(length) && Number(length) > limits.asset_bytes) fail('tooLarge');
        declared = (response.headers.get('Content-Type') ?? '').split(';')[0].trim().toLowerCase();
        if (!reader) fail('unsupported');
        let size = 0; let reads = 0; let slab; let used = 0;
        const slabs = [];
        while (true) {
          // Even an endless stream of immediately resolved empty reads must let
          // the deadline/caller's timer run. This is scheduling, not a chunk cap.
          if (++reads % 256 === 0) await wait(new Promise(resolve => setTimeout(resolve, 0)));
          const { done, value } = await wait(reader.read());
          if (done) break;
          if (!(value instanceof Uint8Array)) fail('unsupported');
          if (value.byteLength > limits.asset_bytes - size) fail('tooLarge');
          // Empty chunks retain nothing. Copy before the next read/await so a
          // source may safely reuse its array; part count depends on byte budget,
          // not fragmentation (at most ceil(asset_bytes / 64 KiB) slabs).
          for (let offset = 0; offset < value.byteLength;) {
            if (!slab || used === slab.length) {
              slab = new Uint8Array(Math.min(64 * 1024, limits.asset_bytes - size));
              slabs.push(slab); used = 0;
            }
            const length = Math.min(value.byteLength - offset, slab.length - used);
            slab.set(value.subarray(offset, offset + length), used);
            used += length; offset += length; size += length;
          }
        }
        bytes = new Uint8Array(size);
        let offset = 0;
        for (const part of slabs) {
          const length = Math.min(part.length, size - offset);
          bytes.set(part.subarray(0, length), offset); offset += length;
        }
      } else bytes = decodeData(info, limits.asset_bytes);
      const [mediaType, extension] = inspect(bytes, limits);
      if (declared && declared !== 'application/octet-stream' && declared !== mediaType) fail('mimeMismatch');
      const digest = await wait(crypto.subtle.digest('SHA-256', bytes));
      return { ok: true, normalizedUrl: info.normalizedUrl, blob: new Blob([bytes], { type: mediaType }), sha256: [...new Uint8Array(digest)].map(n => n.toString(16).padStart(2, '0')).join(''), mediaType, extension, size: bytes.length };
    } catch (error) {
      controller.abort();
      if (reader && !stopped) void reader.cancel().catch(() => {});
      return { ok: false, normalizedUrl: info.normalizedUrl, error: errorValue(stopped ?? (error instanceof AcquisitionError ? error : new AcquisitionError('network'))) };
    } finally {
      clearTimeout(timer); signal?.removeEventListener('abort', cancel);
      // No awaited cleanup can extend the deadline for a broken transport.
      try { reader?.releaseLock(); } catch { /* A cancelled pending read owns it until settlement. */ }
    }
  }

  async function acquire(urls, { signal } = {}) {
    const results = new Array(urls.length); const jobs = []; const unique = new Map();
    for (let index = 0; index < urls.length; index++) {
      const url = urls[index];
      try {
        const info = source(url);
        let job = unique.get(info.normalizedUrl);
        if (!job) { job = { info, aliases: [] }; unique.set(info.normalizedUrl, job); jobs.push(job); }
        job.aliases.push({ index, url });
      } catch (error) { results[index] = { ok: false, url, error: errorValue(error instanceof AcquisitionError ? error : new AcquisitionError('unsafe')) }; }
    }
    let next = 0;
    await Promise.all(Array.from({ length: Math.min(3, jobs.length) }, async () => {
      while (next < jobs.length) {
        const job = jobs[next++]; const result = await acquireOne(job.info, signal);
        for (const { index, url } of job.aliases) results[index] = { ...result, url };
      }
    }));
    return results;
  }
  return { requestAccessFromUserGesture, acquire };
}
