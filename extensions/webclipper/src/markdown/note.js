const encoder = new TextEncoder();
const validText = value => typeof value === 'string' && value.isWellFormed();
const invalid = field => { throw new TypeError(`Invalid note ${field}.`); };

// JSON double-quoted scalars are YAML 1.2 scalars. Escape YAML-disallowed
// characters too, and spell Unicode line breaks explicitly (never raw lines).
function scalar(value) {
  return JSON.stringify(value).replace(/[\u007f-\u009f\u2028\u2029\ufffe\uffff]/g,
    character => '\\u' + character.charCodeAt(0).toString(16).padStart(4, '0'));
}

function validateSource(source) {
  if (!validText(source) || encoder.encode(source).length > 8192 ||
      !/^https?:\/\/[^/?#@]+(?:[/?#]|$)/.test(source) || /[\\\p{Cc}\p{White_Space}]/u.test(source)) invalid('source URL');
  try {
    const parsed = new URL(source);
    if (!parsed.hostname || parsed.username || parsed.password || /\p{Cc}/u.test(decodeURIComponent(source))) invalid('source URL');
  } catch { invalid('source URL'); }
}

// Pure export boundary, also usable by a future immutable upload snapshot.
// Draft markdown is always the BODY, even when it begins with its own ---.
export function validateNoteMetadata({ title, sourceUrl, capturedAt }) {
  if (!validText(title)) invalid('title');
  validateSource(sourceUrl);
  if (!validText(capturedAt) || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(capturedAt)) invalid('capture time');
  const date = new Date(capturedAt);
  if (!Number.isFinite(date.getTime()) || date.toISOString().slice(0, 19) !== capturedAt.slice(0, 19)) invalid('capture time');
}

export function formatNote({ title, sourceUrl, capturedAt, markdown }) {
  validateNoteMetadata({ title, sourceUrl, capturedAt });
  if (!validText(markdown)) invalid('Markdown');
  return `---\ntitle: ${scalar(title)}\nsource: ${scalar(sourceUrl)}\nclipped_at: ${scalar(capturedAt)}\n---\n\n` + markdown;
}

function boundedStem(stem, maxBytes) {
  let bounded = '';
  let bytes = 0;
  for (const character of stem) {
    const size = encoder.encode(character).length;
    if (bytes + size > maxBytes) break;
    bounded += character;
    bytes += size;
  }
  return bounded.replace(/[. ]+$/g, '');
}

// A suggestion, not a collision guarantee or a sanitizer for protocol filenames.
// Normalize only the filename to NFC; never modify title metadata.
export function proposeFilename(title) {
  if (!validText(title)) invalid('title');
  const stem = title.normalize('NFC').replace(/[<>:"/\\|?*\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/gu, '_')
    .trim().replace(/[. ]+$/g, '').replace(/\.md$/i, '').replace(/^[. ]+|[. ]+$/g, '');
  let finalStem = boundedStem(stem, 252) || 'clip';
  // Truncation and trailing trimming can expose a device alias. Check only
  // after both; reserve a byte for '_' even for a full-length CON.ext… name.
  // The protected stem starts with '_', so a second truncation cannot expose
  // another alias or empty stem. No retry loop is needed.
  if (/^(?:CON|PRN|AUX|NUL|CONIN\$|CONOUT\$|(?:COM|LPT)[1-9¹²³]) *(?:\.|$)/i.test(finalStem)) {
    finalStem = '_' + boundedStem(finalStem, 251);
  }
  return finalStem + '.md';
}
