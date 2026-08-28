import { render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
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
  createNote,
  deleteNote,
  getNotes,
  renameNote,
  syncNotes,
} from './api.js'
import NotesWorkspace from './NotesWorkspace.svelte'
import NotesWorkspaceHost from '../test/NotesWorkspaceHost.svelte'

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
    vi.mocked(getNotes).mockReset().mockResolvedValue([])
    vi.mocked(syncNotes).mockReset()
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
