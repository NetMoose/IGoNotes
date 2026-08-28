import { render, screen, waitFor } from '@testing-library/svelte'
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
