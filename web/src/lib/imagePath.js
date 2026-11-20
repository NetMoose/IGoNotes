// src is an HTML-decoded attribute value, not an HTML fragment. Local references
// are path-only: ?/# belong to filenames, percent escapes are decoded once.
// Return null rather than letting a rejected path become a browser-relative URL.
export function resolveImagePath(src, noteId) {
  if (!src || /[\x00-\x1f\x7f]/.test(src)) return null;
  if (/^https?:\/\//i.test(src) || /^data:image\//i.test(src)
    || /^\/api\/raw(?:[?#]|$)/.test(src)) return src;
  if (/^[a-z][a-z\d+.-]*:/i.test(src.trimStart())) return null;

  let path;
  try {
    path = decodeURIComponent(src);
  } catch {
    return null;
  }
  if (path.startsWith('/') || /[\\\x00-\x1f\x7f]/.test(path)) return null;

  // Only explicit dot-relative links use the note directory. Bare paths retain
  // the historical base-root meaning, including assets/images upload paths.
  const parts = /^\.\.?\//.test(src) ? (noteId || '').split('/').slice(0, -1) : [];
  for (const part of path.split('/')) {
    if (!part || part === '.') continue;
    if (part === '..') {
      if (parts.length === 0) return null;
      parts.pop();
    } else {
      parts.push(part);
    }
  }
  return parts.length ? `/api/raw?path=${encodeURIComponent(parts.join('/'))}` : null;
}
