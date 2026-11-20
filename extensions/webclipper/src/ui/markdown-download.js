import { formatNote, proposeFilename } from '../markdown/note.js';

export function sourceLink(value) {
  try {
    const parsed = new URL(value);
    return ['https:', 'http:'].includes(parsed.protocol) && !parsed.username && !parsed.password ? parsed.href : null;
  } catch { return null; }
}

export function markdownFilename(value) {
  return proposeFilename(value);
}

export function downloadMarkdown(note, filename) {
  const markdown = formatNote(note);
  const safeFilename = markdownFilename(filename);
  const url = URL.createObjectURL(new Blob([markdown], { type: 'text/markdown;charset=utf-8' }));
  const anchor = document.createElement('a');
  try {
    anchor.href = url;
    anchor.download = safeFilename;
    document.body.append(anchor);
    anchor.click();
  } finally {
    anchor.remove();
    // Give the browser a task to consume the link before releasing its Blob.
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }
}
