export const DEFAULT_ENDPOINT = 'http://127.0.0.1:8080';
const KEY = 'clipperConnection';
const messages = {
  invalid_endpoint: 'Use an HTTP localhost or 127.0.0.1 address with a valid port and no path, credentials, query or fragment.',
  invalid_token: 'Paste a nonempty connection token (at most 4096 visible ASCII characters, without spaces).',
  storage_unavailable: 'Trusted connection storage is unavailable. Reopen the extension and try again.',
};

export class ConnectionError extends Error {
  constructor(code) { super(messages[code]); this.name = 'ConnectionError'; this.code = code; }
}

export function validateEndpoint(value) {
  // Check the raw grammar before URL normalization can discard empty userinfo,
  // dot paths, empty query/fragment delimiters or backslashes.
  if (typeof value !== 'string' || !/^http:\/\/[^\s/@?#\\]+\/?$/i.test(value)) throw new ConnectionError('invalid_endpoint');
  try {
    const authority = value.replace(/^http:\/\//i, '').replace(/\/$/, '');
    if (authority.includes(':') && !/^[^:]+:[0-9]+$/.test(authority)) throw new Error();
    const url = new URL(value);
    if (url.protocol !== 'http:' || !['127.0.0.1', 'localhost'].includes(url.hostname) ||
      url.username || url.password || url.pathname !== '/' || url.search || url.hash ||
      (url.port && (Number(url.port) < 1 || Number(url.port) > 65535))) throw new Error();
    return url.origin;
  } catch { throw new ConnectionError('invalid_endpoint'); }
}

export function validateToken(value) {
  // Opaque: do not duplicate the server's token encoding or parsing rules.
  if (typeof value !== 'string' || !/^[\x21-\x7e]{1,4096}$/.test(value)) throw new ConnectionError('invalid_token');
  return value;
}

export function createConnectionStore({ chrome = globalThis.chrome } = {}) {
  async function trusted(action) {
    try {
      const local = chrome?.storage?.local;
      if (typeof local?.setAccessLevel !== 'function') throw new Error();
      // Every operation awaits its own restriction, including other UI contexts.
      await local.setAccessLevel({ accessLevel: 'TRUSTED_CONTEXTS' });
      return await action(local);
    } catch { throw new ConnectionError('storage_unavailable'); }
  }
  return Object.freeze({
    protect: () => trusted(() => undefined),
    async load() {
      const rows = await trusted(local => local.get(KEY));
      const saved = rows?.[KEY];
      if (saved === undefined) return { endpoint: DEFAULT_ENDPOINT, token: '' };
      return { endpoint: validateEndpoint(saved?.endpoint), token: validateToken(saved?.token) };
    },
    async save({ endpoint, token }) {
      const settings = { endpoint: validateEndpoint(endpoint), token: validateToken(token) };
      await trusted(local => local.set({ [KEY]: settings }));
    },
    clear: () => trusted(local => local.remove(KEY)),
  });
}
