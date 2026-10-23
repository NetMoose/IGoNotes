export function sourceLink(value) {
  try {
    const parsed = new URL(value);
    return ['https:', 'http:'].includes(parsed.protocol) && !parsed.username && !parsed.password ? parsed.href : null;
  } catch { return null; }
}

export function markdownFilename(value) {
  // A download name is a single basename, never a path or an HTML document.
  let name = value.split(/[\\/]/).at(-1).replace(/[<>:"|?*\u0000-\u001f\u007f]/g, '_').replace(/[. ]+$/g, '').trim();
  if (!name || /^\.+$/.test(name)) name = 'draft';
  name = name.slice(0, 180);
  if (/^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(name)) name = '_' + name;
  if (!/\.md$/i.test(name)) name += '.md';
  return name;
}

export function downloadMarkdown(markdown, filename) {
  const url = URL.createObjectURL(new Blob([markdown], { type: 'text/markdown;charset=utf-8' }));
  const anchor = document.createElement('a');
  try {
    anchor.href = url;
    anchor.download = markdownFilename(filename);
    document.body.append(anchor);
    anchor.click();
  } finally {
    anchor.remove();
    // Give the browser a task to consume the link before releasing its Blob.
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }
}
