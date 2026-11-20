import { validateEndpoint } from '../connection/settings.js';
import { formatNote } from '../markdown/note.js';
import { isPortableComponent as component, isVisibleDirectory } from '../markdown/portable-paths.js';
import { inspectImageBlob, normalizeImageSource } from './acquire.js';
import { scanImageReferences } from './references.js';

const encoder = new TextEncoder();
const bytes = value => encoder.encode(value).length;
const ascii = (a, b) => a < b ? -1 : a > b ? 1 : 0;
const invalid = message => { throw new TypeError(`Invalid import snapshot: ${message}.`); };
const text = value => typeof value === 'string' && value.isWellFormed();
// Go strings.TrimSpace uses Unicode White_Space (includes NEL, excludes BOM).
const trimSpace = value => value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '');
const hashPattern = /^[0-9a-f]{64}$/;
const validID = id => typeof id === 'string' && /^[A-Za-z0-9_-]{1,64}$/.test(id) && id !== 'manifest';
const fields = ['import_id', 'base', 'destination_revision', 'parent', 'filename', 'markdown', 'source_url', 'captured_at', 'assets', 'external_images'];
const formats = { 'image/png': 'png', 'image/jpeg': 'jpg', 'image/gif': 'gif', 'image/webp': 'webp' };
function shape(value, keys) {
  if (!value || Object.getPrototypeOf(value) !== Object.prototype || Object.keys(value).length !== keys.length || keys.some(k => !Object.hasOwn(value, k))) invalid('object fields');
}
function safeURL(value) {
  if (!text(value) || !value || bytes(value) > 8192 || /[\\\p{Cc}\p{White_Space}]/u.test(value)) return false;
  try {
    const u = new URL(value);
    return /^https?:\/\//i.test(value) && !!u.hostname && !u.username && !u.password &&
      !/^[^/]*:\/\/[^/]*@/.test(value) && !/\p{Cc}/u.test(decodeURIComponent(value));
  } catch { return false; }
}
function validateManifest(m) {
  shape(m, fields);
  if (!fields.slice(0, 8).every(k => text(m[k]))) invalid('string or Unicode');
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(m.import_id) || !m.base || trimSpace(m.base) !== m.base || !trimSpace(m.destination_revision)) invalid('identity');
  if (m.parent && !m.parent.split('/').every(component)) invalid('parent');
  if (!component(m.filename) || !m.filename.endsWith('.md') || m.filename === '.md') invalid('filename');
  if (!trimSpace(m.markdown) || bytes(m.markdown) > 5 * 1024 ** 2) invalid('Markdown size');
  if (!safeURL(m.source_url)) invalid('source URL');
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(m.captured_at)) invalid('timestamp');
  const date = new Date(m.captured_at);
  if (!Number.isFinite(date.getTime()) || date.toISOString().slice(0, 19) !== m.captured_at.slice(0, 19)) invalid('timestamp');
  if (!Array.isArray(m.assets) || !Array.isArray(m.external_images) || m.assets.length > 100 || m.external_images.length > 100) invalid('arrays or count');
  const ids = new Set(); let total = 0;
  for (const a of m.assets) {
    shape(a, ['id', 'sha256', 'media_type', 'size']);
    if (!validID(a.id) || ids.has(a.id) || !text(a.sha256) || !hashPattern.test(a.sha256) || !text(a.media_type) || !Object.hasOwn(formats, a.media_type) || !Number.isSafeInteger(a.size) || a.size <= 0 || a.size > 10 * 1024 ** 2) invalid('asset');
    ids.add(a.id); total += a.size;
  }
  if (total > 50 * 1024 ** 2) invalid('total asset bytes');
  for (const e of m.external_images) {
    shape(e, ['url', 'reason']);
    if (!safeURL(e.url) || !text(e.reason) || !trimSpace(e.reason) || bytes(e.reason) > 1024 || /[<>\p{Cc}]/u.test(e.reason)) invalid('external image');
  }
}

// Exact v1 ordered arrays. JSON.stringify leaves HTML and U+2028/2029 literal;
// validation rejects lone surrogates and numbers that would be coerced/rounded.
export function canonicalManifest(manifest) {
  validateManifest(manifest);
  return JSON.stringify(['igonotes-clipper', 1, ...fields.slice(0, 8).map(k => manifest[k]),
    [...manifest.assets].sort((a, b) => ascii(a.id, b.id)).map(a => [a.id, a.sha256, a.media_type, a.size]),
    manifest.external_images.map(e => [e.url, e.reason])]);
}
async function sha256(value) {
  const digest = await globalThis.crypto.subtle.digest('SHA-256', encoder.encode(value));
  return [...new Uint8Array(digest)].map(n => n.toString(16).padStart(2, '0')).join('');
}
export function manifestDigest(manifest) { return sha256(canonicalManifest(manifest)); }

// JSON-only metadata cloning rejects exotic objects, cycles, undefined, secrets
// by schema at the boundary. Blob handles are immutable and retained separately.
function cloneJSON(value) {
  if (value === null || typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value))) return value;
  if (text(value)) return value;
  if (Array.isArray(value)) return value.map(cloneJSON);
  if (!value || Object.getPrototypeOf(value) !== Object.prototype) invalid('JSON metadata');
  return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, cloneJSON(v)]));
}
function freeze(value) {
  if (value && typeof value === 'object' && !(value instanceof Blob)) {
    for (const entry of Object.values(value)) freeze(entry);
    Object.freeze(value);
  }
  return value;
}
const sourceKey = value => normalizeImageSource(value);
const escapeDestination = value => value.replace(/[\\()<>\s]/g, c => /\s/.test(c) ? encodeURIComponent(c) : `\\${c}`).replace(/&/g, '&amp;');
// A decoded reference title can contain entity-produced blank lines and CRLF.
// Physical line endings would be normalized (or terminate the inline image).
// One pass protects original ampersands without re-escaping our new references.
const escapeTitle = value => value.replace(/[&"\\\r\n\t\u2028\u2029]/gu, c => {
  if (c === '&') return '&amp;';
  if (c === '"' || c === '\\') return `\\${c}`;
  return `&#${c.codePointAt(0)};`;
});

// Caller passes one coherent persisted readBundle (or equivalent detached
// snapshot), after flushing edits. Never acquires, writes, sends, or generates IDs.
export async function buildImportSnapshot({ draft: inputDraft, assets: inputAssets, confirmedFallbacks = [], endpoint: inputEndpoint, importId }) {
  // Everything caller-mutable is detached synchronously BEFORE any await.
  const draft = cloneJSON(inputDraft);
  if (!Array.isArray(inputAssets)) invalid('assets');
  const assets = inputAssets.map(row => {
    if (!text(row.normalizedUrl) || !Number.isSafeInteger(row.size) || row.size < 0 || (row.blob !== null && !(row.blob instanceof Blob))) invalid('cached asset scalar fields');
    return { id: row.id, normalizedUrl: row.normalizedUrl, size: row.size, descriptor: cloneJSON(row.descriptor), blob: row.blob };
  });
  const acks = cloneJSON(confirmedFallbacks);
  const endpoint = validateEndpoint(inputEndpoint);
  const originalDraft = cloneJSON(draft);
  if (!text(draft.markdown) || !text(draft.title)) invalid('draft text');
  if (['sending', 'uncertain', 'pending'].includes(draft.status)) invalid('frozen draft');
  if (!text(draft.parent) || (draft.parent && !draft.parent.split('/').every(c => component(c) && isVisibleDirectory(c)))) invalid('invisible parent');
  const manifest = { import_id: importId, base: draft.base, destination_revision: draft.destinationRevision, parent: draft.parent, filename: draft.filename,
    // Only the final formatted note has the protocol Markdown budget. A large
    // managed data URL can become a small local destination after rewriting.
    markdown: 'preflight', source_url: draft.sourceUrl, captured_at: draft.capturedAt, assets: [], external_images: [] };
  validateManifest(manifest);
  const refs = scanImageReferences(draft.markdown, draft.sourceUrl);
  const byID = new Map();
  for (const row of assets) {
    if (!validID(row.id) || byID.has(row.id)) invalid('cached asset ID');
    byID.set(row.id, row);
  }
  if (!Array.isArray(acks)) invalid('fallback acknowledgements');
  const ackByURL = new Map();
  for (const ack of acks) {
    shape(ack, ['url', 'reason']);
    // Same acquisition policy: never turn unsafe/data/private edits into HTTP
    // fallback via a different URL. Intent must identify the current exact URL.
    if (!safeURL(ack.url)) invalid('fallback URL');
    sourceKey(ack.url);
    if (ackByURL.has(ack.url) || !refs.some(r => r.url === ack.url)) invalid('stale fallback acknowledgement');
    ackByURL.set(ack.url, ack);
  }
  const verified = new Map(), contentOwners = new Map(), assetBlobs = {}, sourceAliases = [], edits = [];
  const externalSeen = new Set(); let total = 0;
  for (const ref of refs) {
    const key = sourceKey(ref.url); // policy checked even if a cached asset exists
    const ack = ackByURL.get(ref.url);
    let destination, assetId = null;
    if (ack) {
      destination = ref.url;
      if (!externalSeen.has(ref.url)) { manifest.external_images.push({ ...ack }); externalSeen.add(ref.url); }
    } else {
      const matches = assets.filter(row => {
        try { return sourceKey(row.normalizedUrl) === key; } catch { return false; }
      });
      const row = matches.find(row => row.descriptor.status === 'ready');
      if (!row) invalid('active image unavailable; explicit URL/reason acknowledgement required');
      const ownerID = row.descriptor.canonicalAssetId ?? row.id;
      const owner = byID.get(ownerID);
      // 8.2 writes direct aliases only. Chains/cycles are not evidence of bytes.
      if (!owner || (owner.descriptor.canonicalAssetId ?? owner.id) !== owner.id || owner.descriptor.status !== 'ready' || !(owner.blob instanceof Blob)) invalid('dangling or chained alias');
      let actual = verified.get(ownerID);
      if (!actual) {
        actual = await inspectImageBlob(owner.blob);
        if (owner.size !== owner.blob.size) invalid('cached Blob size');
        verified.set(ownerID, actual);
      }
      for (const candidate of [owner, row]) {
        if (!['sha256', 'size', 'mediaType', 'extension'].every(k => candidate.descriptor[k] === actual[k])) invalid('cached descriptor');
        if (candidate.blob && candidate.size !== candidate.blob.size) invalid('cached size');
      }
      const contentKey = `${actual.sha256}:${actual.mediaType}:${actual.extension}`;
      assetId = contentOwners.get(contentKey);
      if (!assetId) {
        assetId = owner.id; contentOwners.set(contentKey, assetId);
        total += actual.size;
        if (manifest.assets.length >= 100 || total > 50 * 1024 ** 2) invalid('batch asset budget');
        manifest.assets.push({ id: assetId, sha256: actual.sha256, media_type: actual.mediaType, size: actual.size });
        Object.defineProperty(assetBlobs, assetId, { value: owner.blob, enumerable: true });
      }
      destination = '../'.repeat(draft.parent ? draft.parent.split('/').length : 0) + `assets/images/clip-${importId}-${actual.sha256}.${actual.extension}`;
    }
    sourceAliases.push({ url: ref.url, destination: ref.destination, assetId, rewrittenDestination: destination });
    const encoded = escapeDestination(destination);
    if (ref.kind === 'inline') {
      if (!ref.destinationPosition) invalid('missing destination token range');
      edits.push({ start: ref.destinationPosition.start.offset, end: ref.destinationPosition.end.offset, value: encoded });
    } else {
      const rawLabel = ref.labelPosition ? draft.markdown.slice(ref.labelPosition.start.offset, ref.labelPosition.end.offset) : '';
      edits.push({ start: ref.position.start.offset, end: ref.position.end.offset, value: `![${rawLabel}](${encoded}${ref.title === null || ref.title === undefined ? '' : ` "${escapeTitle(ref.title)}"`})` });
    }
  }
  // Ascending nonoverlapping source edits; one join, no global substitution or
  // repeated whole-document copies. Everything outside these spans is literal.
  edits.sort((a, b) => a.start - b.start);
  const chunks = []; let at = 0;
  for (const edit of edits) {
    if (edit.start < at) invalid('overlapping image spans');
    chunks.push(draft.markdown.slice(at, edit.start), edit.value); at = edit.end;
  }
  chunks.push(draft.markdown.slice(at));
  manifest.markdown = formatNote({ ...draft, markdown: chunks.join('') });
  manifest.assets.sort((a, b) => ascii(a.id, b.id));
  const digest = await manifestDigest(manifest);
  // CAS precondition includes exact original metadata plus all saved descriptors
  // and Blob sizes, including inactive rows. 8.5 must re-read atomically and
  // compare before persisting/freeze; this fingerprint is NOT a durable journal.
  const preparationState = { draft: originalDraft, assets: assets.map(({ id, normalizedUrl, size, descriptor, blob }) => ({ id, normalizedUrl, size, descriptor, blobSize: blob?.size ?? null })).sort((a, b) => ascii(a.id, b.id)) };
  const preparationFingerprint = await sha256(JSON.stringify(preparationState));
  return freeze({ endpoint, importId, manifest, digest, assetBlobs, sourceAliases, originalDraft, preparationState, preparationFingerprint,
    autonomous: manifest.external_images.length === 0, warnings: manifest.external_images.map(e => ({ ...e })) });
}
