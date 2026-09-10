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

async function requestWithStatus(path, options = {}) {
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
    return { status: response.status, payload: null }
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

  return { status: response.status, payload }
}

async function request(path, options = {}) {
  const result = await requestWithStatus(path, options)
  return result.payload
}

function jsonBody(value) {
  return JSON.stringify(value)
}

function invalidResponse(status) {
  return new ApiError({
    status,
    code: 'invalid_response',
    message: 'Приложение вернуло некорректный JSON',
  })
}

function isObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function hasString(value, key, { optional = false } = {}) {
  return (optional && value[key] === undefined) || typeof value[key] === 'string'
}

function hasInteger(value, key) {
  return Number.isInteger(value[key])
}

function validAPIError(value) {
  return isObject(value)
    && typeof value.code === 'string'
    && typeof value.message === 'string'
    && hasString(value, 'field', { optional: true })
}

function validGitBase(value) {
  return isObject(value)
    && typeof value.name === 'string'
    && typeof value.path === 'string'
    && typeof value.auto_sync === 'boolean'
    && hasString(value, 'git_url')
    && hasString(value, 'git_branch')
    && hasString(value, 'git_commit_message_template')
    && hasInteger(value, 'auto_sync_interval_minutes')
}

const gitStates = new Set([
  'unconfigured',
  'initializing',
  'ready',
  'syncing',
  'error',
  'paused',
  'conflict',
  'needs_reconnect',
])

const gitOperationStates = new Set(['queued', 'running', 'succeeded', 'failed', 'conflict'])
const gitHistoryRelations = new Set(['none', 'shared', 'unrelated', 'unknown'])

function validGitStatus(value) {
  return isObject(value)
    && typeof value.base === 'string'
    && gitStates.has(value.state)
    && hasInteger(value, 'ahead')
    && hasInteger(value, 'behind')
    && hasInteger(value, 'consecutive_failures')
    && Array.isArray(value.changed_paths)
    && value.changed_paths.every((path) => typeof path === 'string')
    && hasString(value, 'repository_path', { optional: true })
    && hasString(value, 'operation_id', { optional: true })
    && hasString(value, 'stage', { optional: true })
    && hasString(value, 'last_attempt', { optional: true })
    && hasString(value, 'last_success', { optional: true })
    && hasString(value, 'remote_oid', { optional: true })
    && (value.error === undefined || value.error === null || validAPIError(value.error))
}

function validGitOperation(value) {
  return isObject(value)
    && typeof value.operation_id === 'string'
    && gitOperationStates.has(value.status)
    && typeof value.deduplicated === 'boolean'
}

function validGitProbe(value) {
  const mutations = value?.required_mutations
  return isObject(value)
    && typeof value.base === 'string'
    && typeof value.git_version === 'string'
    && typeof value.has_repository === 'boolean'
    && typeof value.repository_root_matches === 'boolean'
    && typeof value.detached_head === 'boolean'
    && typeof value.working_tree_clean === 'boolean'
    && Array.isArray(value.remote_branches)
    && value.remote_branches.every((branch) => typeof branch === 'string')
    && typeof value.empty_remote === 'boolean'
    && typeof value.identity_configured === 'boolean'
    && gitHistoryRelations.has(value.history_relation)
    && typeof value.can_configure === 'boolean'
    && isObject(mutations)
    && ['create_repository', 'add_origin', 'replace_origin', 'create_branch', 'merge_histories']
      .every((key) => typeof mutations[key] === 'boolean')
    && Array.isArray(value.warnings)
    && value.warnings.every((warning) => typeof warning === 'string')
    && hasString(value, 'repository_root', { optional: true })
    && hasString(value, 'current_branch', { optional: true })
    && hasString(value, 'existing_origin_url', { optional: true })
    && hasString(value, 'pending_operation', { optional: true })
    && (value.blocking_error === undefined || value.blocking_error === null || validAPIError(value.blocking_error))
}

async function requestGit(path, options, validate) {
  const { status, payload } = await requestWithStatus(path, options)
  if (!validate(payload)) {
    throw invalidResponse(status)
  }
  return payload
}

async function mutateConfig(path, options) {
  const { status, payload } = await requestWithStatus(path, options)
  if (
    payload === null
    || typeof payload !== 'object'
    || Array.isArray(payload)
    || payload.config === null
    || typeof payload.config !== 'object'
    || Array.isArray(payload.config)
    || typeof payload.base_path !== 'string'
  ) {
    throw new ApiError({
      status,
      code: 'invalid_response',
      message: 'Приложение вернуло некорректный JSON',
    })
  }
  return payload.config
}

export function getConfig() {
  return request('/api/config', { method: 'GET' })
}

export function probeGit({ base, git_url, git_branch = '' }) {
  return requestGit('/api/git/probe', {
    method: 'POST',
    body: jsonBody({ base, git_url, git_branch }),
  }, validGitProbe)
}

export function configureGit(base, request) {
  return requestGit(`/api/git/config?base=${encodeURIComponent(base)}`, {
    method: 'PUT',
    body: jsonBody(request),
  }, (payload) => isObject(payload)
    && validGitBase(payload.base)
    && validGitStatus(payload.status)
    && (payload.operation === undefined || payload.operation === null || validGitOperation(payload.operation)))
}

export function disableGit(base) {
  return requestGit(`/api/git/config?base=${encodeURIComponent(base)}`, {
    method: 'DELETE',
  }, (payload) => isObject(payload)
    && validGitBase(payload.base)
    && validGitStatus(payload.status)
    && (payload.operation === undefined || payload.operation === null || validGitOperation(payload.operation)))
}

export async function getGitStatus(base = '') {
  const query = base ? `?base=${encodeURIComponent(base)}` : ''
  const payload = await requestGit(`/api/git/status${query}`, { method: 'GET' }, (value) => isObject(value)
    && Array.isArray(value.statuses)
    && value.statuses.every(validGitStatus))
  return payload.statuses
}

export function syncGit(base) {
  return requestGit(`/api/git/sync?base=${encodeURIComponent(base)}`, {
    method: 'POST',
  }, validGitOperation)
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
  const { status, payload } = await requestWithStatus('/api/system/select-directory', { method: 'POST' })
  if (status === 204) {
    return null
  }
  if (
    payload === null
    || typeof payload !== 'object'
    || Array.isArray(payload)
    || typeof payload.path !== 'string'
    || payload.path.length === 0
  ) {
    throw new ApiError({
      status,
      code: 'invalid_response',
      message: 'Приложение вернуло некорректный JSON',
    })
  }
  return payload.path
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
