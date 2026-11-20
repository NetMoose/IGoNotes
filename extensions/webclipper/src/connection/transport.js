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
  clipper_destination_not_found: 'The base is no longer available. Load and confirm the destination again.',
  clipper_destination_changed: 'The destination changed. Load and confirm it again.',
};
export class TransportError extends Error {
  constructor(code) { super(messages[code]); this.name = 'TransportError'; this.code = code; }
}
const fail = code => { throw new TransportError(code); };
const text = value => typeof value === 'string' && value.length > 0 && value.length <= 1024;
const strings = value => Array.isArray(value) && value.every(item => typeof item === 'string');
const limitKeys = ['markdown_bytes', 'assets', 'asset_bytes', 'total_asset_bytes', 'multipart_bytes', 'image_pixels', 'gif_frames', 'gif_total_pixels'];
const imageTypes = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];
const uuid = value => typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value);
const rejectionStatus = { clipper_invalid_request: 400, clipper_too_large: 413, clipper_unsupported_image: 415,
  clipper_destination_not_found: 404, clipper_destination_changed: 409, note_conflict: 409, clipper_import_abandoned: 409 };
function resultDTO(v) {
  if (!uuid(v?.import_id) || !text(v.base) || typeof v.path !== 'string' || !v.path || v.path.length > 65536 ||
    !/^sha256:[0-9a-f]{64}$/.test(v.revision) || !strings(v.warnings) || v.warnings.length > 100) fail('invalid_response');
  return { import_id: v.import_id, base: v.base, path: v.path, revision: v.revision, warnings: [...v.warnings] };
}
function deliveryDTO(value, status, id) {
  const code = value?.code;
  if (status === 202 && code === 'clipper_import_pending') return { state: 'pending', code };
  if (id && status === 400 && code === 'clipper_invalid_request') return { state: 'unknown' };
  if (!id && rejectionStatus[code] === status) return { state: 'rejected', code };
  if (status === 409 && ['clipper_request_changed', 'clipper_recovery_required', 'git_conflict_pending'].includes(code)) {
    return { state: code === 'clipper_request_changed' ? 'uncertain' : code === 'git_conflict_pending' ? 'pending' : 'recovery_required', code };
  }
  if (status !== 200 && !(status === 201 && !id)) fail('invalid_response');
  if (!id) return { state: 'succeeded', result: resultDTO(value) };
  if (value?.import_id !== id) fail('invalid_response');
  const hasResult = Object.hasOwn(value, 'result'), hasError = Object.hasOwn(value, 'error');
  if (value.state === 'succeeded' && value.result?.import_id === id && !hasError) return { state: 'succeeded', result: resultDTO(value.result) };
  if (value.state === 'pending' && !hasResult && !hasError) return { state: 'pending' };
  if (value.state === 'recovery_required' && !hasResult && value.error?.code === 'clipper_recovery_required') return { state: 'recovery_required', code: value.error.code };
  // GET's matching terminal journal is proof of rejection, including a safely
  // failed import. Never add internal_error to the POST HTTP rejection mapping:
  // an HTTP 500 alone remains ambiguous, irrespective of its body/message.
  if (value.state === 'rejected' && !hasResult && (Object.hasOwn(rejectionStatus, value.error?.code) || value.error?.code === 'internal_error')) return { state: 'rejected', code: value.error.code };
  // A Git gate may be journalled as rejected; conservatively retain the exact ID.
  if (value.state === 'rejected' && !hasResult && value.error?.code === 'git_conflict_pending') return { state: 'pending', code: value.error.code };
  fail('invalid_response');
}

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
  // Only postImport supplies a body; public options cannot change route/method.
  // There is deliberately no public request(url) primitive.
  async function read(route, validate, { signal } = {}, body, delivery = false) {
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
          method: body ? 'POST' : 'GET', ...(body ? { body } : {}), headers: { Authorization: `Bearer ${token}` },
          credentials: 'omit', redirect: 'error', cache: 'no-store', signal: controller.signal,
        });
        if (response.redirected) fail('unavailable');
        if (response.status === 401 || response.status === 403) fail('unauthorized');
        if (response.status === 428) fail('setup_required');
        if (response.status >= 500 || (!delivery && !response.ok)) fail('unavailable');
        if (response.headers.get('Content-Type')?.split(';')[0].trim().toLowerCase() !== 'application/json') fail('invalid_response');
        let value;
        try { value = await response.json(); } catch { fail('invalid_response'); }
        return validate(value, response.status);
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
    postImport(body, options = {}) {
      if (!(body instanceof FormData)) return Promise.reject(new TransportError('invalid_request'));
      return read('imports', (value, status) => deliveryDTO(value, status), options, body, true);
    },
    importStatus(id, options = {}) {
      if (!uuid(id)) return Promise.reject(new TransportError('invalid_request'));
      return read(`imports?id=${encodeURIComponent(id)}`, (value, status) => deliveryDTO(value, status, id), options, undefined, true);
    },
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
      return read(`folders?base=${encoded}`, (value, status) => {
        if ((status === 404 && value?.code === 'clipper_destination_not_found') || (status === 409 && value?.code === 'clipper_destination_changed')) fail(value.code);
        if (status !== 200) fail('unavailable');
        if (value?.base !== base || !strings(value.folders) || !text(value.destination_revision)) fail('invalid_response');
        return { base, folders: [...value.folders], destination_revision: value.destination_revision };
      }, options, undefined, true);
    },
  });
}
