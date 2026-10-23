import { validateEndpoint, validateToken } from './settings.js';

const messages = {
  unavailable: 'IGoNotes is unavailable. Check that it is running at the chosen address.',
  unauthorized: 'Connection token was not accepted. Paste a valid token from IGoNotes.',
  setup_required: 'Complete the initial setup in IGoNotes before connecting.',
  protocol_mismatch: 'This IGoNotes protocol version is not supported by the extension.',
  invalid_response: 'IGoNotes returned an unsupported response.',
  invalid_request: 'Choose a valid base before requesting folders.',
  timeout: 'The connection check timed out. Check IGoNotes and try again.',
  cancelled: 'The connection check was cancelled.',
};
export class TransportError extends Error {
  constructor(code) { super(messages[code]); this.name = 'TransportError'; this.code = code; }
}
const fail = code => { throw new TransportError(code); };
const text = value => typeof value === 'string' && value.length > 0 && value.length <= 1024;
const strings = value => Array.isArray(value) && value.every(item => typeof item === 'string');
const limitKeys = ['markdown_bytes', 'assets', 'asset_bytes', 'total_asset_bytes', 'multipart_bytes', 'image_pixels', 'gif_frames', 'gif_total_pixels'];
const imageTypes = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];

function infoDTO(value) {
  if (value?.protocol_version !== 1) fail('protocol_mismatch');
  if (!text(value.app_name) || !text(value.app_version) ||
    !limitKeys.every(key => Number.isSafeInteger(value.limits?.[key]) && value.limits[key] > 0) ||
    !Array.isArray(value.supported_image_types) || value.supported_image_types.length === 0 ||
    new Set(value.supported_image_types).size !== value.supported_image_types.length ||
    !value.supported_image_types.every(type => imageTypes.includes(type))) fail('invalid_response');
  // Explicit projection: arbitrary server fields/messages never leave this layer.
  return { protocol_version: 1, app_name: value.app_name, app_version: value.app_version,
    limits: Object.fromEntries(limitKeys.map(key => [key, value.limits[key]])),
    supported_image_types: [...value.supported_image_types] };
}

export function createTransport(settings, { fetch = globalThis.fetch } = {}) {
  const endpoint = validateEndpoint(settings.endpoint);
  const token = validateToken(settings.token);
  // Deliberately no public request(url) primitive. Delivery will extend this
  // allowlist with typed v1 operations, not a client-controlled URL proxy.
  async function read(route, validate, { signal } = {}) {
    const controller = new AbortController();
    let timer;
    let onAbort;
    let rejectStop;
    const stopped = new Promise((_, reject) => { rejectStop = reject; });
    function stop(code) {
      rejectStop(new TransportError(code));
      controller.abort();
    }
    try {
      if (signal?.aborted) fail('cancelled');
      onAbort = () => stop('cancelled');
      signal?.addEventListener('abort', onAbort, { once: true });
      timer = setTimeout(() => stop('timeout'), 10000);
      return await Promise.race([stopped, (async () => {
        const response = await fetch(`${endpoint}/api/clipper/v1/${route}`, {
          method: 'GET', headers: { Authorization: `Bearer ${token}` },
          credentials: 'omit', redirect: 'error', cache: 'no-store', signal: controller.signal,
        });
        if (response.redirected) fail('unavailable');
        if (response.status === 401 || response.status === 403) fail('unauthorized');
        if (response.status === 428) fail('setup_required');
        if (!response.ok) fail('unavailable');
        if (response.headers.get('Content-Type')?.split(';')[0].trim().toLowerCase() !== 'application/json') fail('invalid_response');
        let value;
        try { value = await response.json(); } catch { fail('invalid_response'); }
        return validate(value);
      })()]);
    } catch (error) {
      // Never retain native cause, URL, server body or reflected credential.
      if (error instanceof TransportError) throw error;
      fail('unavailable');
    } finally {
      clearTimeout(timer);
      if (onAbort) signal?.removeEventListener('abort', onAbort);
      // Release unread error/HTML bodies too, rather than leaving a stream alive
      // after returning an early static error and clearing its deadline.
      controller.abort();
    }
  }
  return Object.freeze({
    info: options => read('info', infoDTO, options),
    bases: options => read('bases', value => {
      if (!strings(value?.bases)) fail('invalid_response');
      return { bases: [...value.bases] };
    }, options),
    folders(base, options) {
      let encoded;
      try {
        if (!text(base) || !base.trim() || new TextEncoder().encode(base).length > 1024) fail('invalid_request');
        encoded = encodeURIComponent(base);
      } catch { return Promise.reject(new TransportError('invalid_request')); }
      return read(`folders?base=${encoded}`, value => {
        if (value?.base !== base || !strings(value.folders) || !text(value.destination_revision)) fail('invalid_response');
        return { base, folders: [...value.folders], destination_revision: value.destination_revision };
      }, options);
    },
  });
}
