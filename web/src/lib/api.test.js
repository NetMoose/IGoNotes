import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  ApiError,
  completeSetup,
  createBase,
  createNote,
  deleteNote,
  forgetBase,
  getConfig,
  getInfo,
  getNote,
  getNotes,
  renameNote,
  saveNote,
  selectDirectory,
  switchBase,
  syncNotes,
  updateBase,
  updateConfig,
  uploadAsset,
} from './api.js'

function response(body = '', status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: vi.fn().mockResolvedValue(body),
  }
}

function jsonResponse(body, status = 200) {
  return response(JSON.stringify(body), status)
}

function requestAt(fetchMock, index = 0) {
  const [path, options] = fetchMock.mock.calls[index]
  return { path, options }
}

function expectJSONRequest(fetchMock, index, path, method, body) {
  const request = requestAt(fetchMock, index)
  expect(request.path).toBe(path)
  expect(request.options.method).toBe(method)
  expect(request.options.body).toBe(JSON.stringify(body))
  expect(request.options.headers).toBeInstanceOf(Headers)
  expect(request.options.headers.get('Content-Type')).toBe('application/json')
}

describe('frontend API client', () => {
  let fetchMock

  beforeEach(() => {
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('constructs ApiError with stable defaults', () => {
    const error = new ApiError()

    expect(error).toBeInstanceOf(Error)
    expect(error).toMatchObject({
      name: 'ApiError',
      status: 0,
      code: 'network_error',
      field: '',
      message: '',
    })
  })

  it('gets and decodes config using a Headers instance', async () => {
    const config = { active_base: 'work', bases: [{ name: 'work' }] }
    fetchMock.mockResolvedValue(jsonResponse(config))

    await expect(getConfig()).resolves.toEqual(config)

    const { path, options } = requestAt(fetchMock)
    expect(path).toBe('/api/config')
    expect(options.method).toBe('GET')
    expect(options.headers).toBeInstanceOf(Headers)
    expect(options.headers.has('Content-Type')).toBe(false)
  })

  it('preserves a structured 409 API error', async () => {
    fetchMock.mockResolvedValue(jsonResponse({
      code: 'name_conflict',
      message: 'База уже существует',
      field: 'name',
    }, 409))

    await expect(createBase({ name: 'work' })).rejects.toMatchObject({
      name: 'ApiError',
      status: 409,
      code: 'name_conflict',
      field: 'name',
      message: 'База уже существует',
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('returns null when the directory picker is cancelled with 204', async () => {
    const cancelled = response('', 204)
    fetchMock.mockResolvedValue(cancelled)

    await expect(selectDirectory()).resolves.toBeNull()
    expect(cancelled.text).not.toHaveBeenCalled()
    expect(requestAt(fetchMock)).toMatchObject({
      path: '/api/system/select-directory',
      options: { method: 'POST' },
    })
  })

  it('keeps a directory picker 501 response as a typed error', async () => {
    fetchMock.mockResolvedValue(jsonResponse({
      code: 'not_supported',
      message: 'Выбор каталога не поддерживается',
    }, 501))

    const promise = selectDirectory()
    await expect(promise).rejects.toBeInstanceOf(ApiError)
    await expect(promise).rejects.toMatchObject({
      status: 501,
      code: 'not_supported',
      message: 'Выбор каталога не поддерживается',
    })
  })

  it('updates an encoded base, switches base, and refreshes config after each mutation', async () => {
    const updated = { active_base: 'old/name' }
    const switched = { active_base: 'next' }
    fetchMock
      .mockResolvedValueOnce(response('', 204))
      .mockResolvedValueOnce(jsonResponse(updated))
      .mockResolvedValueOnce(response('', 204))
      .mockResolvedValueOnce(jsonResponse(switched))

    await expect(updateBase('old/name', { name: 'renamed', path: '/notes' })).resolves.toEqual(updated)
    await expect(switchBase('next')).resolves.toEqual(switched)

    expectJSONRequest(fetchMock, 0, '/api/bases?name=old%2Fname', 'PUT', {
      name: 'renamed',
      path: '/notes',
    })
    expect(requestAt(fetchMock, 1)).toMatchObject({ path: '/api/config', options: { method: 'GET' } })
    expectJSONRequest(fetchMock, 2, '/api/bases/switch', 'POST', { name: 'next' })
    expect(requestAt(fetchMock, 3)).toMatchObject({ path: '/api/config', options: { method: 'GET' } })
  })

  it('normalizes network failures and supplies a fallback message', async () => {
    fetchMock
      .mockRejectedValueOnce(new Error('connection reset'))
      .mockRejectedValueOnce('offline')

    await expect(getInfo()).rejects.toMatchObject({
      name: 'ApiError',
      status: 0,
      code: 'network_error',
      field: '',
      message: 'connection reset',
    })
    await expect(getInfo()).rejects.toMatchObject({
      name: 'ApiError',
      status: 0,
      code: 'network_error',
      message: 'Не удалось связаться с приложением',
    })
  })

  it('uses the HTTP fallback for a non-JSON error response', async () => {
    fetchMock.mockResolvedValue(response('<h1>failure</h1>', 500))

    await expect(getInfo()).rejects.toMatchObject({
      name: 'ApiError',
      status: 500,
      code: 'http_error',
      field: '',
      message: 'Ошибка запроса (500)',
    })
  })

  it('sends the complete config without mutation and returns refreshed config', async () => {
    const config = {
      active_base: 'personal',
      bases: [{ name: 'personal', path: '/tmp/personal' }],
      git: { enabled: false },
    }
    const original = structuredClone(config)
    const refreshed = { ...config, saved: true }
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ ignored: true }))
      .mockResolvedValueOnce(jsonResponse(refreshed))

    await expect(updateConfig(config)).resolves.toEqual(refreshed)

    expect(config).toEqual(original)
    expectJSONRequest(fetchMock, 0, '/api/config', 'PUT', original)
    expect(requestAt(fetchMock, 1)).toMatchObject({ path: '/api/config', options: { method: 'GET' } })
  })

  it('rejects malformed JSON from a successful response as invalid_response', async () => {
    fetchMock.mockResolvedValue(response('{broken', 200))

    await expect(getConfig()).rejects.toMatchObject({
      name: 'ApiError',
      status: 200,
      code: 'invalid_response',
      field: '',
      message: 'Приложение вернуло некорректный JSON',
    })
  })

  it('creates, completes setup, and forgets bases before refreshing config', async () => {
    const draft = { name: 'team', path: '/notes/team' }
    fetchMock
      .mockResolvedValueOnce(response('', 204))
      .mockResolvedValueOnce(jsonResponse({ step: 'created' }))
      .mockResolvedValueOnce(response('', 204))
      .mockResolvedValueOnce(jsonResponse({ step: 'complete' }))
      .mockResolvedValueOnce(response('', 204))
      .mockResolvedValueOnce(jsonResponse({ step: 'forgotten' }))

    await createBase(draft)
    await completeSetup(draft)
    await forgetBase('team/shared')

    expectJSONRequest(fetchMock, 0, '/api/bases', 'POST', draft)
    expect(requestAt(fetchMock, 1).path).toBe('/api/config')
    expectJSONRequest(fetchMock, 2, '/api/setup', 'POST', draft)
    expect(requestAt(fetchMock, 3).path).toBe('/api/config')
    expect(requestAt(fetchMock, 4)).toMatchObject({
      path: '/api/bases?name=team%2Fshared',
      options: { method: 'DELETE' },
    })
    expect(requestAt(fetchMock, 4).options.body).toBeUndefined()
    expect(requestAt(fetchMock, 5).path).toBe('/api/config')
  })

  it('gets info, an encoded note, and the notes tree from exact paths', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ base_path: '/notes' }))
      .mockResolvedValueOnce(jsonResponse({ content: '# Note' }))
      .mockResolvedValueOnce(jsonResponse([{ id: 'folder/note.md' }]))

    await getInfo()
    await getNote('folder/note name.md')
    await getNotes()

    expect(fetchMock.mock.calls.map(([path, options]) => [path, options.method])).toEqual([
      ['/api/info', 'GET'],
      ['/api/note?id=folder%2Fnote%20name.md', 'GET'],
      ['/api/notes', 'GET'],
    ])
  })

  it('wraps note mutations and normalizes empty successes to null', async () => {
    fetchMock.mockResolvedValue(response('', 204))

    await expect(saveNote('topic/note.md', '# Updated')).resolves.toBeNull()
    await expect(syncNotes()).resolves.toBeNull()
    await expect(createNote({ parent: 'topic', name: 'new.md', type: 'file' })).resolves.toBeNull()
    await expect(renameNote('topic/old.md', 'new.md')).resolves.toBeNull()
    await expect(deleteNote('topic/new note.md')).resolves.toBeNull()

    expectJSONRequest(fetchMock, 0, '/api/save', 'POST', {
      id: 'topic/note.md',
      content: '# Updated',
    })
    expect(requestAt(fetchMock, 1)).toMatchObject({ path: '/api/sync', options: { method: 'POST' } })
    expect(requestAt(fetchMock, 1).options.body).toBeUndefined()
    expectJSONRequest(fetchMock, 2, '/api/notes', 'POST', {
      parent: 'topic',
      name: 'new.md',
      type: 'file',
    })
    expectJSONRequest(fetchMock, 3, '/api/rename', 'PUT', {
      id: 'topic/old.md',
      new_name: 'new.md',
    })
    expect(requestAt(fetchMock, 4)).toMatchObject({
      path: '/api/note?id=topic%2Fnew%20note.md',
      options: { method: 'DELETE' },
    })
  })

  it('uploads the same file as multipart data without setting Content-Type', async () => {
    const file = new File(['image'], 'note.png', { type: 'image/png' })
    fetchMock.mockResolvedValue(jsonResponse({ path: 'assets/images/note.png' }))

    await uploadAsset(file)

    const { path, options } = requestAt(fetchMock)
    expect(path).toBe('/api/assets')
    expect(options.method).toBe('POST')
    expect(options.body).toBeInstanceOf(FormData)
    expect(options.body.get('file')).toBe(file)
    expect(options.headers).toBeInstanceOf(Headers)
    expect(options.headers.has('Content-Type')).toBe(false)
  })
})
