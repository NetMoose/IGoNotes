// Server parity: clipperSafeComponent uppercases the first dot-delimited stem
// with Go's one-rune Unicode mappings, after trimming ASCII spaces only. For
// these fixed ASCII device targets, keep JS multi-character expansions literal:
// they cannot match a Go ASCII target (e.g. ß must not become SS). Dotless ı does
// map to I. This is deliberately not a general Unicode uppercase implementation.
export function isWindowsDeviceName(value) {
  const stem = value.split('.', 1)[0].replace(/ +$/, '');
  const upper = [...stem].map(c => {
    const mapped = c.toUpperCase();
    return [...mapped].length === 1 ? mapped : c;
  }).join('');
  return /^(?:CON|PRN|AUX|NUL|CONIN\$|CONOUT\$|(?:COM|LPT)[1-9¹²³])$/.test(upper);
}

export function isPortableComponent(value) {
  return typeof value === 'string' && value.isWellFormed() && value !== '' && value !== '.' && value !== '..' &&
    !/^\.git$/iu.test(value) && !/[. ]$|[/\\<>:"|?*\p{Cc}]/u.test(value) && !isWindowsDeviceName(value);
}

// Go EqualFold uses simple folding, not uppercase expansion. Unicode /iu
// likewise matches aſsets to assets, while leaving aßets distinct.
export function isVisibleDirectory(value) {
  return !value.startsWith('.') && !/^assets$/iu.test(value);
}
