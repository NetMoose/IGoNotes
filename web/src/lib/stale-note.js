function responseField(value, field) {
  if (value === null || typeof value !== 'object' || Array.isArray(value) || typeof value[field] !== 'string') {
    throw new Error(`Некорректный ответ: ${field}`)
  }
  return value[field]
}

export function readNoteResponse(response) {
  const content = responseField(response, 'content')
  const revision = responseField(response, 'revision')
  if (revision.length === 0) {
    throw new Error('Некорректный ответ: revision')
  }
  return { content, revision }
}

export function readSaveResponse(response) {
  const status = responseField(response, 'status')
  const revision = responseField(response, 'revision')
  if (status !== 'saved') {
    throw new Error('Некорректный ответ: status')
  }
  if (revision.length === 0) {
    throw new Error('Некорректный ответ: revision')
  }
  return { status, revision }
}

export function makeStaleNote({ noteId, mine, disk }) {
  return {
    noteId,
    mine: String(mine),
    diskContent: typeof disk?.content === 'string' ? disk.content : '',
    diskRevision: typeof disk?.revision === 'string' ? disk.revision : '',
    diskMissing: disk === null,
  }
}
