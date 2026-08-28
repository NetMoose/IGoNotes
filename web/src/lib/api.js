export class ApiError extends Error {
  constructor({ status = 0, code = 'network_error', message = '', field = '' } = {}) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.field = field
  }
}

function networkError(error) {
  return new ApiError({
    message: error instanceof Error && error.message
      ? error.message
      : 'Не удалось связаться с приложением',
  })
}

async function request(path, options = {}) {
  const headers = new Headers(options.headers)
  const body = options.body
  const isFormData = typeof FormData !== 'undefined' && body instanceof FormData

  if (body && !isFormData && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  let response
  try {
    response = await fetch(path, { ...options, headers })
  } catch (error) {
    throw networkError(error)
  }

  if (response.status === 204) {
    return null
  }

  let text
  try {
    text = await response.text()
  } catch (error) {
    throw networkError(error)
  }

  let payload = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      if (response.ok) {
        throw new ApiError({
          status: response.status,
          code: 'invalid_response',
          message: 'Приложение вернуло некорректный JSON',
        })
      }
    }
  }

  if (!response.ok) {
    const details = payload && typeof payload === 'object' && !Array.isArray(payload)
      ? payload
      : {}
    throw new ApiError({
      status: response.status,
      code: details.code || 'http_error',
      message: details.message || `Ошибка запроса (${response.status})`,
      field: details.field || '',
    })
  }

  return payload
}

function jsonBody(value) {
  return JSON.stringify(value)
}

async function mutateConfig(path, options) {
  await request(path, options)
  return getConfig()
}

export function getConfig() {
  return request('/api/config', { method: 'GET' })
}

export function updateConfig(config) {
  return mutateConfig('/api/config', {
    method: 'PUT',
    body: jsonBody(config),
  })
}

export function completeSetup(draft) {
  return mutateConfig('/api/setup', {
    method: 'POST',
    body: jsonBody(draft),
  })
}

export function createBase(draft) {
  return mutateConfig('/api/bases', {
    method: 'POST',
    body: jsonBody(draft),
  })
}

export function updateBase(name, draft) {
  return mutateConfig(`/api/bases?name=${encodeURIComponent(name)}`, {
    method: 'PUT',
    body: jsonBody(draft),
  })
}

export function forgetBase(name) {
  return mutateConfig(`/api/bases?name=${encodeURIComponent(name)}`, {
    method: 'DELETE',
  })
}

export function switchBase(name) {
  return mutateConfig('/api/bases/switch', {
    method: 'POST',
    body: jsonBody({ name }),
  })
}

export async function selectDirectory() {
  const result = await request('/api/system/select-directory', { method: 'POST' })
  if (result === null) {
    return null
  }
  if (
    typeof result !== 'object'
    || Array.isArray(result)
    || typeof result.path !== 'string'
    || result.path.length === 0
  ) {
    throw new ApiError({
      status: 200,
      code: 'invalid_response',
      message: 'Приложение вернуло некорректный JSON',
    })
  }
  return result.path
}

export function getInfo() {
  return request('/api/info', { method: 'GET' })
}

export function getNote(id) {
  return request(`/api/note?id=${encodeURIComponent(id)}`, { method: 'GET' })
}

export function saveNote(id, content) {
  return request('/api/save', {
    method: 'POST',
    body: jsonBody({ id, content }),
  })
}

export function getNotes() {
  return request('/api/notes', { method: 'GET' })
}

export function syncNotes() {
  return request('/api/sync', { method: 'POST' })
}

export function createNote(payload) {
  return request('/api/notes', {
    method: 'POST',
    body: jsonBody(payload),
  })
}

export function renameNote(id, newName) {
  return request('/api/rename', {
    method: 'PUT',
    body: jsonBody({ id, new_name: newName }),
  })
}

export function deleteNote(id) {
  return request(`/api/note?id=${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function uploadAsset(file) {
  const body = new FormData()
  body.append('file', file)
  return request('/api/assets', { method: 'POST', body })
}
