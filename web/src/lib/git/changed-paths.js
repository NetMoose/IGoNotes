const terminalStates = new Set(['ready', 'error', 'paused'])

function nonemptyString(value) {
  return typeof value === 'string' && value.length > 0
}

export function terminalChangedPaths(status) {
  if (
    !status
    || !terminalStates.has(status.state)
    || !nonemptyString(status.repository_path)
    || !nonemptyString(status.operation_id)
    || !Array.isArray(status.changed_paths)
  ) {
    return []
  }

  return [...new Set(status.changed_paths.filter(nonemptyString))].sort()
}

export function changedPathKey(status, path) {
  if (!nonemptyString(status?.repository_path) || !nonemptyString(status?.operation_id) || !nonemptyString(path)) {
    return ''
  }
  return `${status.repository_path}\0${status.operation_id}\0${path}`
}

export function noteInChangedPaths(noteId, paths) {
  return nonemptyString(noteId) && Array.isArray(paths) && paths.includes(noteId)
}
