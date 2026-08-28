import { render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { tick } from 'svelte'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('./lib/Editor.svelte', async () => ({
  default: (await import('./test/EditorStub.svelte')).default,
}))

vi.mock('./lib/api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    getConfig: vi.fn(),
    completeSetup: vi.fn(),
    selectDirectory: vi.fn(),
    createBase: vi.fn(),
    updateBase: vi.fn(),
    forgetBase: vi.fn(),
    switchBase: vi.fn(),
    getNote: vi.fn(),
    saveNote: vi.fn(),
    getNotes: vi.fn(),
    syncNotes: vi.fn(),
    createNote: vi.fn(),
    renameNote: vi.fn(),
    deleteNote: vi.fn(),
    uploadAsset: vi.fn(),
  }
})

import App from './App.svelte'
import {
  completeSetup,
  createBase,
  createNote,
  deleteNote,
  forgetBase,
  getConfig,
  getNote,
  getNotes,
  renameNote,
  saveNote,
  selectDirectory,
  switchBase,
  syncNotes,
  updateBase,
  uploadAsset,
} from './lib/api.js'

const firstRunConfig = {
  base_dir: '',
  bases: [],
  current_base: '',
  setup_completed: false,
}

const completedConfig = {
  base_dir: '/notes',
  bases: [
    { name: 'personal', path: '/notes/personal', auto_sync: false },
    { name: 'work', path: '/srv/work', auto_sync: false },
  ],
  current_base: 'personal',
  setup_completed: true,
}

const workConfig = {
  ...completedConfig,
  current_base: 'work',
}

const apiMocks = [
  completeSetup,
  createBase,
  createNote,
  deleteNote,
  forgetBase,
  getConfig,
  getNote,
  getNotes,
  renameNote,
  saveNote,
  selectDirectory,
  switchBase,
  syncNotes,
  updateBase,
  uploadAsset,
]

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function fileNode(name) {
  return {
    id: `topic/${name}`,
    name,
    type: 'file',
    parent_id: 'topic',
  }
}

describe('App setup gate', () => {
  beforeEach(() => {
    for (const mock of apiMocks) vi.mocked(mock).mockReset()
    vi.mocked(getConfig).mockResolvedValue(completedConfig)
    vi.mocked(completeSetup).mockResolvedValue(completedConfig)
    vi.mocked(selectDirectory).mockResolvedValue(null)
    vi.mocked(createBase).mockResolvedValue(completedConfig)
    vi.mocked(updateBase).mockResolvedValue(completedConfig)
    vi.mocked(forgetBase).mockResolvedValue(completedConfig)
    vi.mocked(switchBase).mockResolvedValue(completedConfig)
    vi.mocked(getNote).mockResolvedValue({ content: '' })
    vi.mocked(saveNote).mockResolvedValue(null)
    vi.mocked(getNotes).mockResolvedValue([])
    vi.mocked(syncNotes).mockResolvedValue(null)
    vi.mocked(createNote).mockResolvedValue(null)
    vi.mocked(renameNote).mockResolvedValue(null)
    vi.mocked(deleteNote).mockResolvedValue(null)
    vi.mocked(uploadAsset).mockResolvedValue({ path: '' })
  })

  it('blocks the notes workspace while first-run configuration is loading', async () => {
    const request = deferred()
    vi.mocked(getConfig).mockReturnValue(request.promise)

    render(App)

    const loading = screen.getByRole('status')
    expect(loading).toContainElement(
      screen.getByRole('heading', { name: 'Загрузка настроек...' }),
    )
    expect(screen.queryByRole('heading', { name: 'База заметок' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Markdown')).not.toBeInTheDocument()

    request.resolve(firstRunConfig)

    expect(await screen.findByRole('heading', { name: 'Настройте первую базу' })).toBeVisible()
    expect(screen.queryByRole('heading', { name: 'База заметок' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Markdown')).not.toBeInTheDocument()
    expect(getNotes).not.toHaveBeenCalled()
  })

  it('opens the editor for a completed configuration and shows the active base path', async () => {
    render(App)

    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    await waitFor(() => expect(getNotes).toHaveBeenCalledOnce())
    expect(screen.getByTitle('Текущая база заметок')).toHaveTextContent(/^\/notes\/personal$/)
  })

  it('shows a blocking configuration error and retries the application load', async () => {
    const user = userEvent.setup()
    vi.mocked(getConfig)
      .mockRejectedValueOnce(new Error('Настройки недоступны'))
      .mockResolvedValueOnce(completedConfig)

    render(App)

    expect(await screen.findByRole('alert')).toHaveTextContent(/^Настройки недоступны$/)
    expect(screen.queryByRole('heading', { name: 'Настройте первую базу' })).not.toBeInTheDocument()
    expect(screen.queryByText('Выберите заметку')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Повторить' }))

    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    expect(getConfig).toHaveBeenCalledTimes(2)
  })

  it('completes the actual setup wizard before mounting the notes workspace', async () => {
    const user = userEvent.setup()
    const savedConfig = {
      base_dir: '/home/user/notes',
      bases: [{ name: 'work', path: '/home/user/notes/work', auto_sync: false }],
      current_base: 'work',
      setup_completed: true,
    }
    vi.mocked(getConfig).mockResolvedValue(firstRunConfig)
    vi.mocked(completeSetup).mockResolvedValue(savedConfig)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'Создать новую' }))
    await user.type(screen.getByLabelText('Имя базы'), 'work')
    await user.type(screen.getByLabelText('Родительский каталог'), '/home/user/notes')
    await user.click(screen.getByRole('button', { name: 'Продолжить' }))
    await user.click(await screen.findByRole('button', { name: 'Завершить настройку' }))

    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    expect(screen.getByTitle('Текущая база заметок')).toHaveTextContent(/^\/home\/user\/notes\/work$/)
    expect(completeSetup).toHaveBeenCalledOnce()
    expect(completeSetup).toHaveBeenCalledWith({
      mode: 'create',
      name: 'work',
      path: '/home/user/notes',
    })
    await waitFor(() => expect(getNotes).toHaveBeenCalledOnce())
  })

  it('loads and manually saves a note through the API wrappers', async () => {
    const user = userEvent.setup()
    const note = fileNode('encoded name.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Loaded' })

    render(App)
    await user.click(await screen.findByRole('button', { name: 'encoded name.md' }))

    expect(getNote).toHaveBeenCalledOnce()
    expect(getNote).toHaveBeenCalledWith(note.id)
    expect(await screen.findByLabelText('Markdown')).toHaveValue('# Loaded')

    await user.click(screen.getByRole('button', { name: 'Сохранить' }))

    expect(saveNote).toHaveBeenCalledOnce()
    expect(saveNote).toHaveBeenCalledWith(note.id, '# Loaded')
  })

  it('flushes changed markdown before opening settings and cancels the debounce save', async () => {
    const initialUser = userEvent.setup()
    const note = fileNode('draft.md')
    const saveRequest = deferred()
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original' })
    vi.mocked(saveNote).mockReturnValue(saveRequest.promise)

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.clear(textarea)
      await user.type(textarea, '# Changed')
      await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))

      expect(saveNote).toHaveBeenCalledOnce()
      expect(saveNote).toHaveBeenCalledWith(note.id, '# Changed')
      expect(screen.queryByRole('heading', { name: 'Базы заметок' })).not.toBeInTheDocument()

      saveRequest.resolve(null)
      await saveRequest.promise
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(screen.getByRole('heading', { name: 'Базы заметок' })).toBeVisible()
      await vi.advanceTimersByTimeAsync(2000)
      expect(saveNote).toHaveBeenCalledOnce()
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('keeps the editor open and reports a failed settings flush', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original' })
    vi.mocked(saveNote).mockRejectedValue(new Error('Диск недоступен'))

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.clear(textarea)
    await user.type(textarea, '# Changed')
    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      /^Не удалось сохранить заметку: Диск недоступен$/,
    )
    expect(screen.getByText('Ошибка сохранения')).toBeVisible()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Changed')
    expect(screen.queryByRole('heading', { name: 'Базы заметок' })).not.toBeInTheDocument()
  })

  it('flushes before switching bases and remounts an empty editor workspace', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original' })
    vi.mocked(switchBase).mockResolvedValue(workConfig)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.clear(textarea)
    await user.type(textarea, '# Work in progress')
    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))
    const workCard = screen.getByRole('article', { name: 'База work' })
    await user.click(within(workCard).getByRole('button', { name: 'Открыть' }))

    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    expect(saveNote).toHaveBeenCalledWith(note.id, '# Work in progress')
    expect(switchBase).toHaveBeenCalledWith('work')
    expect(saveNote.mock.invocationCallOrder[0]).toBeLessThan(switchBase.mock.invocationCallOrder[0])
    expect(screen.queryByLabelText('Markdown')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Сохранить' })).toBeDisabled()
    expect(screen.getByTitle('Текущая база заметок')).toHaveTextContent(/^\/srv\/work$/)
    await waitFor(() => expect(getNotes).toHaveBeenCalledTimes(2))
  })

  it('keeps settings and the previous config when switching bases fails', async () => {
    const user = userEvent.setup()
    vi.mocked(switchBase).mockRejectedValue(new Error('База занята'))

    render(App)
    await screen.findByText('Выберите заметку')
    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))
    const workCard = screen.getByRole('article', { name: 'База work' })
    await user.click(within(workCard).getByRole('button', { name: 'Открыть' }))

    expect(await within(workCard).findByRole('alert')).toHaveTextContent(/^База занята$/)
    expect(screen.getByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    expect(screen.getByRole('article', { name: 'База personal' })).toHaveTextContent('Текущая')

    await user.click(screen.getByRole('button', { name: 'Назад к заметкам' }))
    expect(await screen.findByTitle('Текущая база заметок')).toHaveTextContent(/^\/notes\/personal$/)
  })

  it('resets the active note when settings changes the active base path', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const movedConfig = {
      ...completedConfig,
      bases: [
        { ...completedConfig.bases[0], path: '/mnt/personal' },
        completedConfig.bases[1],
      ],
    }
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original' })
    vi.mocked(updateBase).mockResolvedValue(movedConfig)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    await screen.findByLabelText('Markdown')
    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))
    const personalCard = screen.getByRole('article', { name: 'База personal' })
    await user.click(within(personalCard).getByRole('button', { name: 'Изменить' }))
    const path = screen.getByLabelText('Каталог существующей базы')
    await user.clear(path)
    await user.type(path, '/mnt/personal')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))

    expect(updateBase).toHaveBeenCalledWith('personal', {
      name: 'personal',
      path: '/mnt/personal',
    })
    expect(await screen.findByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    expect(screen.getByRole('article', { name: 'База personal' })).toHaveTextContent('/mnt/personal')

    await user.click(screen.getByRole('button', { name: 'Назад к заметкам' }))
    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    expect(screen.queryByLabelText('Markdown')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Сохранить' })).toBeDisabled()
    expect(screen.getByTitle('Текущая база заметок')).toHaveTextContent(/^\/mnt\/personal$/)
  })

  it('debounces changed markdown for exactly two seconds', async () => {
    const initialUser = userEvent.setup()
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original' })

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.clear(textarea)
      await user.type(textarea, '# Debounced')

      expect(saveNote).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(1999)
      expect(saveNote).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(1)
      expect(saveNote).toHaveBeenCalledOnce()
      expect(saveNote).toHaveBeenCalledWith(note.id, '# Debounced')
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('serializes a newer edit behind a pending save and flushes it only once', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const firstSave = deferred()
    const secondSave = deferred()
    let inFlight = 0
    let maxInFlight = 0
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original' })
    vi.mocked(saveNote).mockImplementation(() => {
      inFlight += 1
      maxInFlight = Math.max(maxInFlight, inFlight)
      const request = saveNote.mock.calls.length === 1 ? firstSave : secondSave
      return request.promise.finally(() => {
        inFlight -= 1
      })
    })

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.clear(textarea)
    await user.type(textarea, '# First')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    expect(saveNote).toHaveBeenCalledTimes(1)

    await user.clear(textarea)
    await user.type(textarea, '# Latest')
    expect(saveNote).toHaveBeenCalledTimes(1)
    expect(maxInFlight).toBe(1)

    firstSave.resolve(null)
    await waitFor(() => expect(saveNote).toHaveBeenCalledTimes(2))
    expect(saveNote).toHaveBeenNthCalledWith(2, note.id, '# Latest')
    expect(maxInFlight).toBe(1)

    secondSave.resolve(null)
    await waitFor(() => expect(screen.getByText('Сохранено')).toBeVisible())
    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))

    expect(await screen.findByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    expect(saveNote).toHaveBeenCalledTimes(2)
  })

  it('saves the active note before loading another note and preserves its debounce', async () => {
    const initialUser = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const saveA = deferred()
    let inFlight = 0
    let maxInFlight = 0
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB])
    vi.mocked(getNote).mockImplementation(async (id) => ({
      content: id === noteA.id ? '# A' : '# B',
    }))
    vi.mocked(saveNote).mockImplementation(() => {
      inFlight += 1
      maxInFlight = Math.max(maxInFlight, inFlight)
      const request = saveNote.mock.calls.length === 1 ? saveA.promise : Promise.resolve(null)
      return request.finally(() => {
        inFlight -= 1
      })
    })

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'a.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.clear(textarea)
      await user.type(textarea, '# A changed')
      await vi.advanceTimersByTimeAsync(2000)
      expect(saveNote).toHaveBeenCalledWith(noteA.id, '# A changed')

      await user.click(screen.getByRole('button', { name: 'b.md' }))
      expect(getNote).not.toHaveBeenCalledWith(noteB.id)
      expect(screen.getByLabelText('Markdown')).toHaveValue('# A changed')

      saveA.resolve(null)
      await saveA.promise
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(getNote).toHaveBeenCalledWith(noteB.id)
      expect(screen.getByLabelText('Markdown')).toHaveValue('# B')
      await user.clear(screen.getByLabelText('Markdown'))
      await user.type(screen.getByLabelText('Markdown'), '# B latest')
      await vi.advanceTimersByTimeAsync(2000)

      expect(saveNote).toHaveBeenNthCalledWith(2, noteB.id, '# B latest')
      expect(maxInFlight).toBe(1)
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('keeps the active note when its save fails during note navigation', async () => {
    const initialUser = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const saveA = deferred()
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB])
    vi.mocked(getNote).mockImplementation(async (id) => ({
      content: id === noteA.id ? '# A' : '# B',
    }))
    vi.mocked(saveNote).mockReturnValue(saveA.promise)

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'a.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.clear(textarea)
      await user.type(textarea, '# A changed')
      await vi.advanceTimersByTimeAsync(2000)
      await user.click(screen.getByRole('button', { name: 'b.md' }))

      expect(getNote).not.toHaveBeenCalledWith(noteB.id)
      saveA.reject(new Error('Диск недоступен'))
      await saveA.promise.catch(() => {})
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(getNote).not.toHaveBeenCalledWith(noteB.id)
      expect(screen.getByLabelText('Markdown')).toHaveValue('# A changed')
      expect(within(screen.getByRole('main')).getByText('a.md')).toBeVisible()
      expect(screen.getByRole('alert')).toHaveTextContent(
        /^Не удалось сохранить заметку: Диск недоступен$/,
      )
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('loads only the latest rapidly selected note after a pending save', async () => {
    const initialUser = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const noteC = fileNode('c.md')
    const saveA = deferred()
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB, noteC])
    vi.mocked(getNote).mockImplementation(async (id) => ({
      content: id === noteA.id ? '# A' : id === noteB.id ? '# B' : '# C',
    }))
    vi.mocked(saveNote).mockReturnValue(saveA.promise)

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'a.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.clear(textarea)
      await user.type(textarea, '# A changed')
      await vi.advanceTimersByTimeAsync(2000)
      await user.click(screen.getByRole('button', { name: 'b.md' }))
      await user.click(screen.getByRole('button', { name: 'c.md' }))

      expect(getNote).not.toHaveBeenCalledWith(noteB.id)
      expect(getNote).not.toHaveBeenCalledWith(noteC.id)
      saveA.resolve(null)
      await saveA.promise
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(getNote).not.toHaveBeenCalledWith(noteB.id)
      expect(getNote).toHaveBeenCalledWith(noteC.id)
      expect(screen.getByLabelText('Markdown')).toHaveValue('# C')
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('manually saves unchanged content and catches save errors in the UI', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const unhandled = vi.fn((event) => event.preventDefault())
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original' })
    vi.mocked(saveNote).mockRejectedValue(new Error('Нет места'))
    window.addEventListener('unhandledrejection', unhandled)

    try {
      render(App)
      await user.click(await screen.findByRole('button', { name: 'draft.md' }))
      await screen.findByLabelText('Markdown')
      await user.click(screen.getByRole('button', { name: 'Сохранить' }))

      expect(saveNote).toHaveBeenCalledOnce()
      expect(saveNote).toHaveBeenCalledWith(note.id, '# Original')
      expect(await screen.findByRole('alert')).toHaveTextContent(
        /^Не удалось сохранить заметку: Нет места$/,
      )
      expect(screen.getByText('Ошибка сохранения')).toBeVisible()
      await Promise.resolve()
      expect(unhandled).not.toHaveBeenCalled()
    } finally {
      window.removeEventListener('unhandledrejection', unhandled)
    }
  })

  it('opens settings and returns directly to the editor', async () => {
    const user = userEvent.setup()
    render(App)
    await screen.findByText('Выберите заметку')

    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))

    expect(await screen.findByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Назад к заметкам' }))
    expect(await screen.findByText('Выберите заметку')).toBeVisible()
  })

  it('does not apply a pending initial configuration after unmount', async () => {
    const request = deferred()
    vi.mocked(getConfig).mockReturnValue(request.promise)
    const { unmount } = render(App)
    expect(screen.getByRole('status')).toBeVisible()

    unmount()
    request.resolve(completedConfig)
    await request.promise
    await tick()

    expect(getNotes).not.toHaveBeenCalled()
  })
})
