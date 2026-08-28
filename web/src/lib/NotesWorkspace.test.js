import { render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { tick } from 'svelte'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('./Editor.svelte', async () => ({
  default: (await import('../test/EditorStub.svelte')).default,
}))

vi.mock('./api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    getNotes: vi.fn(),
    syncNotes: vi.fn(),
    createNote: vi.fn(),
    renameNote: vi.fn(),
    deleteNote: vi.fn(),
  }
})

import {
  ApiError,
  createNote,
  deleteNote,
  getNotes,
  renameNote,
  syncNotes,
} from './api.js'
import NotesWorkspace from './NotesWorkspace.svelte'
import { setEditorFlush } from '../test/EditorStub.svelte'
import NotesWorkspaceHost from '../test/NotesWorkspaceHost.svelte'

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

function workspaceProps(overrides = {}) {
  return {
    activeNote: null,
    content: '',
    saveStatus: 'idle',
    basePath: '/notes/work',
    error: '',
    onSelectNote: vi.fn(),
    onDeleteNote: vi.fn(),
    onSave: vi.fn(),
    onOpenSettings: vi.fn(),
    ...overrides,
  }
}

async function renderWorkspace(overrides = {}) {
  const props = workspaceProps(overrides)
  const result = render(NotesWorkspace, props)
  await waitFor(() => expect(getNotes).toHaveBeenCalledOnce())
  return { props, ...result }
}

describe('NotesWorkspace', () => {
  beforeEach(() => {
    setEditorFlush()
    vi.mocked(getNotes).mockReset().mockResolvedValue([])
    vi.mocked(syncNotes).mockReset().mockResolvedValue(null)
    vi.mocked(createNote).mockReset()
    vi.mocked(renameNote).mockReset()
    vi.mocked(deleteNote).mockReset().mockResolvedValue(null)
  })

  it('shows the empty workspace, base path, and available settings action', async () => {
    const user = userEvent.setup()
    const { props } = await renderWorkspace()

    expect(screen.getByText('Выберите заметку')).toBeVisible()
    const basePath = screen.getByTitle('Текущая база заметок')
    expect(basePath).toHaveTextContent('/notes/work')
    const settings = screen.getByRole('button', { name: 'Открыть настройки' })
    expect(settings).toHaveAttribute('title', 'Настройки')
    expect(settings).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Сохранить' })).toBeDisabled()

    await user.click(settings)

    expect(props.onOpenSettings).toHaveBeenCalledOnce()
  })

  it('locks the workspace while a transition is pending', async () => {
    const { container, props, rerender } = await renderWorkspace({ transitioning: true })
    const root = container.firstElementChild

    expect(root).toHaveProperty('inert', true)
    expect(root).toHaveAttribute('aria-busy', 'true')

    await rerender({ ...props, transitioning: false })

    expect(root).toHaveProperty('inert', false)
    expect(root).toHaveAttribute('aria-busy', 'false')
  })

  it('forwards a selected file from the real sidebar', async () => {
    const user = userEvent.setup()
    const note = {
      id: 'topic/note.md',
      name: 'note.md',
      type: 'file',
      parent_id: 'topic',
    }
    vi.mocked(getNotes).mockResolvedValue([note])
    const { props } = await renderWorkspace()

    await user.click(screen.getByRole('button', { name: 'note.md' }))

    expect(props.onSelectNote).toHaveBeenCalledOnce()
    expect(props.onSelectNote).toHaveBeenCalledWith(note)
  })

  it('waits for editor uploads before forwarding a selected file', async () => {
    const user = userEvent.setup()
    const request = deferred()
    const flush = vi.fn(() => request.promise)
    const note = fileNode('next.md')
    setEditorFlush(flush)
    vi.mocked(getNotes).mockResolvedValue([note])
    const { props } = await renderWorkspace({
      activeNote: fileNode('current.md'),
      content: '# Current',
    })

    await user.click(screen.getByRole('button', { name: 'next.md' }))

    expect(flush).toHaveBeenCalledOnce()
    expect(props.onSelectNote).not.toHaveBeenCalled()

    request.resolve()

    await waitFor(() => expect(props.onSelectNote).toHaveBeenCalledOnce())
    expect(props.onSelectNote).toHaveBeenCalledWith(note)
  })

  it('exposes editor upload flushing through the workspace instance', async () => {
    const request = deferred()
    const flush = vi.fn(() => request.promise)
    setEditorFlush(flush)
    const { component } = await renderWorkspace({
      activeNote: fileNode('current.md'),
      content: '# Current',
    })

    const pending = component.flushPendingUploads()

    expect(flush).toHaveBeenCalledOnce()
    expect(pending).toBe(request.promise)
    request.resolve()
    await pending
  })

  it('waits for editor uploads before opening settings', async () => {
    const user = userEvent.setup()
    const request = deferred()
    const flush = vi.fn(() => request.promise)
    setEditorFlush(flush)
    const { props } = await renderWorkspace({
      activeNote: fileNode('current.md'),
      content: '# Current',
    })

    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))

    expect(flush).toHaveBeenCalledOnce()
    expect(props.onOpenSettings).not.toHaveBeenCalled()

    request.resolve()

    await waitFor(() => expect(props.onOpenSettings).toHaveBeenCalledOnce())
  })

  it('keeps settings and save synchronous without pending uploads', async () => {
    const { props } = await renderWorkspace({
      activeNote: fileNode('current.md'),
      content: '# Current',
    })

    screen.getByRole('button', { name: 'Открыть настройки' }).click()
    screen.getByRole('button', { name: 'Сохранить' }).click()

    expect(props.onOpenSettings).toHaveBeenCalledOnce()
    expect(props.onSave).toHaveBeenCalledOnce()
  })

  it('forwards a deleted file id from the real sidebar and modal', async () => {
    const user = userEvent.setup()
    const note = {
      id: 'topic/note.md',
      name: 'note.md',
      type: 'file',
      parent_id: 'topic',
    }
    vi.mocked(getNotes).mockResolvedValue([note])
    const { props } = await renderWorkspace()
    await user.click(screen.getByRole('button', { name: 'note.md' }))
    await user.click(screen.getByRole('button', { name: 'Удалить выбранное' }))

    await user.click(screen.getByRole('button', { name: 'Удалить' }))

    expect(deleteNote).toHaveBeenCalledOnce()
    expect(deleteNote).toHaveBeenCalledWith(note.id)
    expect(props.onDeleteNote).toHaveBeenCalledOnce()
    expect(props.onDeleteNote).toHaveBeenCalledWith(note.id)
    await waitFor(() => expect(getNotes).toHaveBeenCalledTimes(2))
  })

  it('keeps the newest tree when an older load settles last', async () => {
    const user = userEvent.setup()
    const initialLoad = deferred()
    const oldNote = fileNode('old.md')
    const newNote = fileNode('new.md')
    vi.mocked(getNotes)
      .mockReturnValueOnce(initialLoad.promise)
      .mockResolvedValueOnce([newNote])
    render(NotesWorkspace, workspaceProps())
    await waitFor(() => expect(getNotes).toHaveBeenCalledOnce())

    await user.click(screen.getByTitle('Обновить дерево (Синхронизировать с диском)'))

    expect(syncNotes).toHaveBeenCalledOnce()
    expect(await screen.findByRole('button', { name: 'new.md' })).toBeVisible()
    expect(screen.queryByRole('button', { name: 'old.md' })).not.toBeInTheDocument()

    initialLoad.resolve([oldNote])
    await initialLoad.promise
    await tick()

    expect(screen.getByRole('button', { name: 'new.md' })).toBeVisible()
    expect(screen.queryByRole('button', { name: 'old.md' })).not.toBeInTheDocument()
  })

  it('allows only one pending create and disables its dialog controls', async () => {
    const user = userEvent.setup()
    const request = deferred()
    const unhandled = vi.fn((event) => event.preventDefault())
    vi.mocked(createNote).mockReturnValue(request.promise)
    window.addEventListener('unhandledrejection', unhandled)

    try {
      await renderWorkspace()
      await user.click(screen.getByTitle('Создать новую заметку'))
      const dialog = screen.getByRole('dialog')
      const input = within(dialog).getByRole('textbox')
      const confirm = within(dialog).getByRole('button', { name: 'Создать' })
      await user.type(input, 'new.md')

      await user.dblClick(confirm)

      expect(createNote).toHaveBeenCalledOnce()
      expect(input).toBeDisabled()
      expect(confirm).toBeDisabled()
      expect(confirm).toHaveAttribute('aria-busy', 'true')
      await user.keyboard('{Enter}')
      expect(createNote).toHaveBeenCalledOnce()

      request.resolve(null)
      await request.promise
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
      expect(unhandled).not.toHaveBeenCalled()
    } finally {
      request.resolve(null)
      window.removeEventListener('unhandledrejection', unhandled)
    }
  })

  it('shows a rename failure inside the dialog and enables retry', async () => {
    const user = userEvent.setup()
    const note = fileNode('note.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(renameNote).mockRejectedValue(new ApiError({ message: 'Не удалось переименовать' }))
    await renderWorkspace()
    await user.click(screen.getByRole('button', { name: 'note.md' }))
    await user.click(screen.getByTitle('Переименовать выбранное'))
    const dialog = screen.getByRole('dialog')
    const input = within(dialog).getByRole('textbox')
    await user.clear(input)
    await user.type(input, 'renamed.md')

    await user.click(within(dialog).getByRole('button', { name: 'Сохранить' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Не удалось переименовать')
    expect(input).toBeEnabled()
    expect(within(dialog).getByRole('button', { name: 'Сохранить' })).toBeEnabled()
    expect(within(dialog).getByRole('button', { name: 'Сохранить' })).toHaveAttribute('aria-busy', 'false')
  })

  it('does not continue a pending deletion after unmount', async () => {
    const user = userEvent.setup()
    const note = fileNode('note.md')
    const request = deferred()
    const unhandled = vi.fn((event) => event.preventDefault())
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(deleteNote).mockReturnValue(request.promise)
    window.addEventListener('unhandledrejection', unhandled)

    try {
      const { props, unmount } = await renderWorkspace()
      await user.click(screen.getByRole('button', { name: 'note.md' }))
      await user.click(screen.getByTitle('Удалить выбранное'))
      await user.click(screen.getByRole('button', { name: 'Удалить' }))
      expect(deleteNote).toHaveBeenCalledOnce()

      unmount()
      request.resolve(null)
      await request.promise
      await tick()

      expect(props.onDeleteNote).not.toHaveBeenCalled()
      expect(getNotes).toHaveBeenCalledOnce()
      expect(unhandled).not.toHaveBeenCalled()
    } finally {
      request.resolve(null)
      window.removeEventListener('unhandledrejection', unhandled)
    }
  })

  it.each([
    ['saving', 'Сохранение...'],
    ['saved', 'Сохранено'],
    ['error', 'Ошибка сохранения'],
  ])('shows the %s save status', async (saveStatus, text) => {
    await renderWorkspace({ saveStatus })

    expect(screen.getByText(text)).toBeVisible()
  })

  it('disables settings and save while saving', async () => {
    await renderWorkspace({
      activeNote: { id: 'daily.md', name: 'daily.md' },
      content: '# Daily',
      saveStatus: 'saving',
    })

    expect(screen.getByRole('button', { name: 'Открыть настройки' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Сохранить' })).toBeDisabled()
  })

  it('shows a header error as an alert', async () => {
    await renderWorkspace({ error: 'Не удалось загрузить заметку' })

    expect(screen.getByRole('alert')).toHaveTextContent('Не удалось загрузить заметку')
  })

  it('renders the active note editor with bound content and saves on request', async () => {
    const user = userEvent.setup()
    const { props } = await renderWorkspace({
      activeNote: { id: 'topic/note.md', name: 'Заметка' },
      content: '# Начало',
    })

    expect(screen.getByText('Заметка')).toBeVisible()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Начало')
    const save = screen.getByRole('button', { name: 'Сохранить' })
    expect(save).toBeEnabled()

    await user.click(save)

    expect(props.onSave).toHaveBeenCalledOnce()
  })

  it('binds editor changes back to parent state', async () => {
    const user = userEvent.setup()
    render(NotesWorkspaceHost)
    await waitFor(() => expect(getNotes).toHaveBeenCalledOnce())

    const editor = screen.getByLabelText('Markdown')
    expect(editor).toHaveValue('# Parent')
    expect(screen.getByLabelText('Parent markdown')).toHaveTextContent('# Parent')

    await user.clear(editor)
    await user.type(editor, '# Updated')

    expect(screen.getByLabelText('Parent markdown')).toHaveTextContent('# Updated')
  })

  it('shows the no-note placeholder only without an active note', async () => {
    const { rerender, props } = await renderWorkspace()

    expect(screen.getByText('Выберите файл в меню слева')).toBeVisible()

    await rerender({
      ...props,
      activeNote: { id: 'topic/note.md', name: 'Активная заметка' },
      content: 'Текст',
    })

    expect(screen.getByText('Активная заметка')).toBeVisible()
    expect(screen.queryByText('Выберите файл в меню слева')).not.toBeInTheDocument()
  })
})
