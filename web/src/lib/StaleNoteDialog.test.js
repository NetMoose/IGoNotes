import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import StaleNoteDialog from './StaleNoteDialog.svelte'

function dialogProps(overrides = {}) {
  return {
    stale: {
      mine: '# Моя заметка',
      diskContent: '# Версия на диске',
      diskRevision: 'revision-2',
      diskMissing: false,
    },
    onLoadDisk: vi.fn(),
    onOverwrite: vi.fn(),
    onManualMerge: vi.fn(),
    ...overrides,
  }
}

describe('StaleNoteDialog', () => {
  it('compares exact readonly local and disk versions in a responsive layout', () => {
    render(StaleNoteDialog, dialogProps())

    const mine = screen.getByRole('textbox', { name: 'Моя версия' })
    const disk = screen.getByRole('textbox', { name: 'Версия на диске' })

    expect(mine).toHaveAttribute('readonly')
    expect(mine).toHaveValue('# Моя заметка')
    expect(disk).toHaveAttribute('readonly')
    expect(disk).toHaveValue('# Версия на диске')
    expect(mine).toHaveClass('min-h-56')
    expect(mine.parentElement.parentElement).toHaveClass('grid-cols-1', 'lg:grid-cols-2')
  })

  it('focuses an action and cannot be dismissed with Escape', async () => {
    const props = dialogProps()
    render(StaleNoteDialog, props)
    const dialog = screen.getByRole('dialog')
    const load = screen.getByRole('button', { name: 'Загрузить версию с диска' })

    await waitFor(() => expect(load).toHaveFocus())
    await fireEvent.keyDown(dialog, { key: 'Escape' })

    expect(screen.getByRole('dialog')).toBeVisible()
    expect(props.onLoadDisk).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: 'Отмена' })).not.toBeInTheDocument()
  })

  it('loads the disk version when requested', async () => {
    const user = userEvent.setup()
    const props = dialogProps()
    render(StaleNoteDialog, props)

    await user.click(screen.getByRole('button', { name: 'Загрузить версию с диска' }))

    expect(props.onLoadDisk).toHaveBeenCalledOnce()
  })

  it('requires confirmation before overwriting with the local version', async () => {
    const user = userEvent.setup()
    const props = dialogProps()
    render(StaleNoteDialog, props)

    await user.click(screen.getByRole('button', { name: 'Оставить мою версию' }))

    expect(screen.getByText('Перезапись заменит актуальный файл на диске.')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Подтвердить перезапись' }))

    expect(props.onOverwrite).toHaveBeenCalledWith({
      content: '# Моя заметка',
      revision: 'revision-2',
    })
  })

  it('preserves a manual draft and reports a rejected save', async () => {
    const user = userEvent.setup()
    const error = new Error('Конфликт не разрешен')
    const props = dialogProps({ onManualMerge: vi.fn().mockRejectedValue(error) })
    render(StaleNoteDialog, props)

    await user.click(screen.getByRole('button', { name: 'Объединить вручную' }))
    const merge = screen.getByRole('textbox', { name: 'Итоговый текст' })
    await user.clear(merge)
    await user.type(merge, '# Объединенная заметка')
    await user.click(screen.getByRole('button', { name: 'Сохранить объединение' }))

    expect(props.onManualMerge).toHaveBeenCalledWith({
      content: '# Объединенная заметка',
      revision: 'revision-2',
    })
    expect(await screen.findByRole('alert')).toHaveTextContent('Конфликт не разрешен')
    expect(merge).toHaveValue('# Объединенная заметка')
  })

  it('resets recovery state for a different stale conflict', async () => {
    const user = userEvent.setup()
    const first = {
      noteId: 'first.md',
      mine: '# Первая версия',
      diskContent: '# Первая версия на диске',
      diskRevision: 'revision-1',
      diskMissing: false,
    }
    const props = dialogProps({
      stale: first,
      onManualMerge: vi.fn().mockRejectedValue(new Error('Первая ошибка')),
    })
    const result = render(StaleNoteDialog, props)

    await user.click(screen.getByRole('button', { name: 'Объединить вручную' }))
    const merge = screen.getByRole('textbox', { name: 'Итоговый текст' })
    await user.clear(merge)
    await user.type(merge, '# Черновик первой версии')
    await user.click(screen.getByRole('button', { name: 'Сохранить объединение' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Первая ошибка')

    const second = {
      noteId: 'second.md',
      mine: '# Вторая версия',
      diskContent: '# Вторая версия на диске',
      diskRevision: 'revision-2',
      diskMissing: false,
    }
    await result.rerender({ ...props, stale: second })

    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Моя версия' })).toHaveValue('# Вторая версия')
    expect(screen.queryByRole('textbox', { name: 'Итоговый текст' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Объединить вручную' }))
    expect(screen.getByRole('textbox', { name: 'Итоговый текст' })).toHaveValue('# Вторая версия')

    await result.rerender({ ...props, stale: { ...second, mine: '# Новая вторая версия' } })

    expect(screen.getByRole('textbox', { name: 'Моя версия' })).toHaveValue('# Новая вторая версия')
    await user.click(screen.getByRole('button', { name: 'Объединить вручную' }))
    expect(screen.getByRole('textbox', { name: 'Итоговый текст' })).toHaveValue('# Новая вторая версия')
  })

  it('blocks duplicate actions while a local action is pending', async () => {
    const user = userEvent.setup()
    let resolveLoad
    const props = dialogProps({
      onLoadDisk: vi.fn(() => new Promise((resolve) => { resolveLoad = resolve })),
    })
    render(StaleNoteDialog, props)
    const load = screen.getByRole('button', { name: 'Загрузить версию с диска' })

    await user.click(load)
    await user.click(load)

    expect(props.onLoadDisk).toHaveBeenCalledOnce()
    expect(load).toBeDisabled()
    resolveLoad()
    await waitFor(() => expect(load).not.toBeDisabled())
  })

  it('disables overwrite and manual merge when the disk note is missing', () => {
    render(StaleNoteDialog, dialogProps({
      stale: {
        mine: '# Моя заметка',
        diskContent: '',
        diskRevision: '',
        diskMissing: true,
      },
    }))

    expect(screen.getByRole('button', { name: 'Закрыть заметку' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Оставить мою версию' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Объединить вручную' })).toBeDisabled()
  })
})
