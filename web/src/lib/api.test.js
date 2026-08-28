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

function configFixture(name = 'work', path = `/notes/${name}`) {
  return {
    base_dir: '/notes',
    bases: [{ name, path, auto_sync: false }],
    current_base: name,
    setup_completed: true,
  }
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
    const config = configFixture()
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
      code: 'base_name_conflict',
      message: 'base name already exists',
      field: 'name',
    }, 409))

    await expect(createBase({ mode: 'create', name: 'work', path: '/notes' })).rejects.toMatchObject({
      name: 'ApiError',
      status: 409,
      code: 'base_name_conflict',
      field: 'name',
      message: 'base name already exists',
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

  it.each([
    ['an empty body', ''],
    ['JSON null', 'null'],
  ])('rejects a successful directory picker response with %s', async (_case, body) => {
    fetchMock.mockResolvedValue(response(body, 200))

    await expect(selectDirectory()).rejects.toMatchObject({
      name: 'ApiError',
      status: 200,
      code: 'invalid_response',
      message: 'Приложение вернуло некорректный JSON',
    })
  })

  it('rejects a successful directory picker response without a non-empty path', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ path: '' }))

    const promise = selectDirectory()
    await expect(promise).rejects.toBeInstanceOf(ApiError)
    await expect(promise).rejects.toMatchObject({
      status: 200,
      code: 'invalid_response',
      message: 'Приложение вернуло некорректный JSON',
    })
  })

  it('returns the selected directory path from a successful response', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ path: '/notes/team' }))

    await expect(selectDirectory()).resolves.toBe('/notes/team')
  })

  it('keeps a directory picker 501 response as a typed error', async () => {
    fetchMock.mockResolvedValue(jsonResponse({
      code: 'directory_picker_unavailable',
      message: 'Выбор каталога не поддерживается',
    }, 501))

    const promise = selectDirectory()
    await expect(promise).rejects.toBeInstanceOf(ApiError)
    await expect(promise).rejects.toMatchObject({
      status: 501,
      code: 'directory_picker_unavailable',
      message: 'Выбор каталога не поддерживается',
    })
  })

  const updateConfigInput = configFixture('personal', '/tmp/personal')
  const originalUpdateConfigInput = structuredClone(updateConfigInput)

  it.each([
    {
      name: 'updateConfig',
      call: () => updateConfig(updateConfigInput),
      path: '/api/config',
      method: 'PUT',
      body: updateConfigInput,
      callerInput: updateConfigInput,
      originalInput: originalUpdateConfigInput,
    },
    {
      name: 'completeSetup',
      call: () => completeSetup({ mode: 'create', name: 'team', path: '/notes/team' }),
      path: '/api/setup',
      method: 'POST',
      body: { mode: 'create', name: 'team', path: '/notes/team' },
    },
    {
      name: 'createBase',
      call: () => createBase({ mode: 'connect', name: 'shared', path: '/notes/shared' }),
      path: '/api/bases',
      method: 'POST',
      body: { mode: 'connect', name: 'shared', path: '/notes/shared' },
    },
    {
      name: 'updateBase',
      call: () => updateBase('old/name', { name: 'renamed', path: '/notes/renamed' }),
      path: '/api/bases?name=old%2Fname',
      method: 'PUT',
      body: { name: 'renamed', path: '/notes/renamed' },
    },
    {
      name: 'forgetBase',
      call: () => forgetBase('team/shared'),
      path: '/api/bases?name=team%2Fshared',
      method: 'DELETE',
    },
    {
      name: 'switchBase',
      call: () => switchBase('next'),
      path: '/api/bases/switch',
      method: 'POST',
      body: { name: 'next' },
    },
  ])('$name returns the committed config without a refresh', async ({
    call,
    path,
    method,
    body,
    callerInput,
    originalInput,
  }) => {
    const committed = configFixture(`${method.toLowerCase()}-result`)
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ config: committed, base_path: '/committed/base' }))
      .mockRejectedValueOnce(new Error('hypothetical refresh failed'))

    await expect(call()).resolves.toEqual(committed)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    if (body === undefined) {
      const request = requestAt(fetchMock)
      expect(request).toMatchObject({ path, options: { method } })
      expect(request.options.body).toBeUndefined()
      expect(request.options.headers.has('Content-Type')).toBe(false)
    } else {
      expectJSONRequest(fetchMock, 0, path, method, body)
    }
    if (callerInput !== undefined) {
      expect(callerInput).toEqual(originalInput)
    }
  })

  it.each([
    ['JSON null', null],
    ['an array payload', []],
    ['a missing config', { base_path: '/notes/work' }],
    ['a null config', { config: null, base_path: '/notes/work' }],
    ['an array config', { config: [], base_path: '/notes/work' }],
    ['a primitive config', { config: 'invalid', base_path: '/notes/work' }],
    ['a missing base_path', { config: configFixture() }],
    ['a null base_path', { config: configFixture(), base_path: null }],
    ['a non-string base_path', { config: configFixture(), base_path: 42 }],
  ])('rejects a successful settings mutation response with %s', async (_case, payload) => {
    fetchMock.mockResolvedValue(jsonResponse(payload))

    const promise = switchBase('work')
    await expect(promise).rejects.toBeInstanceOf(ApiError)
    await expect(promise).rejects.toMatchObject({
      name: 'ApiError',
      status: 200,
      code: 'invalid_response',
      field: '',
      message: 'Приложение вернуло некорректный JSON',
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
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

  it('wraps note mutations and decodes their handler responses', async () => {
    const saved = { status: 'saved' }
    const synced = { status: 'ok' }
    const created = {
      id: 'topic/new.md',
      name: 'new.md',
      type: 'file',
      path: 'topic/new.md',
      parent_id: 'topic',
    }
    const renamed = { status: 'renamed' }
    fetchMock
      .mockResolvedValueOnce(jsonResponse(saved))
      .mockResolvedValueOnce(jsonResponse(synced))
      .mockResolvedValueOnce(jsonResponse(created))
      .mockResolvedValueOnce(jsonResponse(renamed))
      .mockResolvedValueOnce(response('', 200))

    await expect(saveNote('topic/note.md', '# Updated')).resolves.toEqual(saved)
    await expect(syncNotes()).resolves.toEqual(synced)
    await expect(createNote({ parent_id: 'topic', name: 'new.md', type: 'file' })).resolves.toEqual(created)
    await expect(renameNote('topic/old.md', 'new.md')).resolves.toEqual(renamed)
    await expect(deleteNote('topic/new note.md')).resolves.toBeNull()

    expectJSONRequest(fetchMock, 0, '/api/save', 'POST', {
      id: 'topic/note.md',
      content: '# Updated',
    })
    expect(requestAt(fetchMock, 1)).toMatchObject({ path: '/api/sync', options: { method: 'POST' } })
    expect(requestAt(fetchMock, 1).options.body).toBeUndefined()
    expectJSONRequest(fetchMock, 2, '/api/notes', 'POST', {
      parent_id: 'topic',
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
    expect(requestAt(fetchMock, 4).options.body).toBeUndefined()
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
