import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    createBase: vi.fn(),
    updateBase: vi.fn(),
    forgetBase: vi.fn(),
  }
})

import { ApiError, createBase, forgetBase, updateBase } from '../api.js'
import BaseCard from './BaseCard.svelte'
import SettingsWorkspace from './SettingsWorkspace.svelte'

const config = {
  base_dir: '/notes',
  bases: [
    { name: 'personal', path: '/notes/personal', auto_sync: false },
    { name: 'work', path: '/srv/work', auto_sync: false },
  ],
  current_base: 'personal',
  setup_completed: true,
}

const oneBaseConfig = {
  ...config,
  bases: [config.bases[0]],
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function renderWorkspace(overrides = {}) {
  const props = {
    config,
    onConfigChange: vi.fn(),
    onSwitch: vi.fn(),
    onBack: vi.fn(),
    ...overrides,
  }
  return { props, ...render(SettingsWorkspace, props) }
}

function baseArticle(name) {
  return screen.getByRole('article', { name: `База ${name}` })
}

describe('BaseCard', () => {
  it('passes only the exact base name to onOpen', async () => {
    const user = userEvent.setup()
    const onOpen = vi.fn()
    render(BaseCard, {
      base: config.bases[1],
      onOpen,
      onEdit: vi.fn(),
      onForget: vi.fn(),
    })

    await user.click(screen.getByRole('button', { name: 'Открыть' }))

    expect(onOpen).toHaveBeenCalledOnce()
    expect(onOpen).toHaveBeenCalledWith('work')
  })

  it('passes the base and exact Forget trigger to onForget', async () => {
    const user = userEvent.setup()
    const onForget = vi.fn()
    render(BaseCard, {
      base: config.bases[1],
      canForget: true,
      onOpen: vi.fn(),
      onEdit: vi.fn(),
      onForget,
    })
    const trigger = screen.getByRole('button', { name: 'Забыть' })

    await user.click(trigger)

    expect(onForget).toHaveBeenCalledOnce()
    expect(onForget).toHaveBeenCalledWith(config.bases[1], trigger)
  })
})

describe('SettingsWorkspace', () => {
  beforeEach(() => {
    vi.mocked(createBase).mockReset()
    vi.mocked(updateBase).mockReset()
    vi.mocked(forgetBase).mockReset()
  })

  it('renders the bases tab, paths, current state, and only an eligible Forget action', () => {
    renderWorkspace()

    expect(screen.getByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    expect(screen.getByText('/notes/personal')).toBeVisible()
    expect(screen.getByText('/srv/work')).toBeVisible()
    expect(within(baseArticle('personal')).getByText('Текущая')).toBeVisible()
    const basesTab = screen.getByRole('tab', { name: 'Базы заметок' })
    const gitTab = screen.getByRole('tab', { name: 'Git, скоро' })
    const panel = screen.getByRole('tabpanel')
    expect(basesTab).toHaveAttribute('aria-current', 'page')
    expect(basesTab.id).not.toBe('')
    expect(panel.id).not.toBe('')
    expect(basesTab).toHaveAttribute('aria-controls', panel.id)
    expect(panel).toHaveAttribute('aria-labelledby', basesTab.id)
    expect(gitTab).toHaveAttribute('aria-disabled', 'true')
    expect(gitTab).toHaveAttribute('tabindex', '-1')
    expect(screen.getAllByRole('button', { name: 'Забыть' })).toHaveLength(1)
    expect(screen.getAllByText('Git не настроен')).toHaveLength(2)
    expect(screen.getAllByText('Автосинхронизация выключена')).toHaveLength(2)
  })

  it('uses responsive horizontal-to-sidebar settings navigation', () => {
    renderWorkspace()

    const tablist = screen.getByRole('tablist', { name: 'Разделы настроек' })
    expect(tablist).toHaveClass('flex', 'overflow-x-auto', 'md:flex-col')
    expect(tablist).not.toHaveClass('grid-cols-2', 'md:grid-cols-1')
    for (const tab of screen.getAllByRole('tab')) {
      expect(tab).toHaveClass('shrink-0')
    }
  })

  it('keeps only Edit actions when there is one active base', () => {
    renderWorkspace({ config: oneBaseConfig })
    const card = baseArticle('personal')

    expect(within(card).queryByRole('button', { name: 'Открыть' })).not.toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: 'Забыть' })).not.toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Изменить' })).toBeEnabled()
  })

  it('focuses Add and Edit panel headings and returns focus to the list on Cancel', async () => {
    const user = userEvent.setup()
    renderWorkspace()

    await user.click(screen.getByRole('button', { name: 'Добавить базу' }))
    const addHeading = screen.getByRole('heading', { name: 'Добавить базу' })
    expect(addHeading).toHaveAttribute('tabindex', '-1')
    await waitFor(() => expect(addHeading).toHaveFocus())

    await user.click(screen.getByRole('button', { name: 'Отмена' }))
    const listHeading = screen.getByRole('heading', { name: 'Базы заметок' })
    await waitFor(() => expect(listHeading).toHaveFocus())

    await user.click(within(baseArticle('personal')).getByRole('button', { name: 'Изменить' }))
    const editHeading = screen.getByRole('heading', { name: 'Изменить базу' })
    expect(editHeading).toHaveAttribute('tabindex', '-1')
    await waitFor(() => expect(editHeading).toHaveFocus())

    await user.click(screen.getByRole('button', { name: 'Отмена' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Базы заметок' })).toHaveFocus())
  })

  it('adds a base and returns to the list with the saved config', async () => {
    const user = userEvent.setup()
    const savedConfig = {
      ...oneBaseConfig,
      bases: [...oneBaseConfig.bases, config.bases[1]],
    }
    vi.mocked(createBase).mockResolvedValue(savedConfig)
    const { props } = renderWorkspace({ config: oneBaseConfig })

    await user.click(screen.getByRole('button', { name: 'Добавить базу' }))
    expect(screen.getByRole('radio', { name: 'Создать новую' })).toBeChecked()
    expect(screen.getByRole('radio', { name: 'Подключить существующую' })).not.toBeChecked()
    await user.type(screen.getByLabelText('Имя базы'), 'work')
    await user.type(screen.getByLabelText('Родительский каталог'), '/notes')
    await user.click(screen.getByRole('button', { name: 'Добавить' }))

    expect(createBase).toHaveBeenCalledOnce()
    expect(createBase).toHaveBeenCalledWith({ mode: 'create', name: 'work', path: '/notes' })
    await waitFor(() => expect(props.onConfigChange).toHaveBeenCalledOnce())
    expect(props.onConfigChange).toHaveBeenCalledWith(savedConfig)
    expect(screen.getByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    expect(screen.queryByRole('radio', { name: 'Создать новую' })).not.toBeInTheDocument()
  })

  it('edits the active base by its original name and remains in settings', async () => {
    const user = userEvent.setup()
    const savedConfig = {
      ...config,
      current_base: 'renamed',
      bases: [{ name: 'renamed', path: '/other', auto_sync: false }, config.bases[1]],
    }
    vi.mocked(updateBase).mockResolvedValue(savedConfig)
    const { props } = renderWorkspace()

    await user.click(within(baseArticle('personal')).getByRole('button', { name: 'Изменить' }))
    const name = screen.getByLabelText('Имя базы')
    const path = screen.getByLabelText('Каталог существующей базы')
    expect(name).toHaveValue('personal')
    expect(path).toHaveValue('/notes/personal')
    await user.clear(name)
    await user.type(name, 'renamed')
    await user.clear(path)
    await user.type(path, '/other')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))

    expect(updateBase).toHaveBeenCalledOnce()
    expect(updateBase).toHaveBeenCalledWith('personal', { name: 'renamed', path: '/other' })
    await waitFor(() => expect(props.onConfigChange).toHaveBeenCalledOnce())
    expect(props.onConfigChange).toHaveBeenCalledWith(savedConfig)
    expect(screen.getByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'Назад к заметкам' })).toBeVisible()
  })

  it('applies forgotten config before closing and then focuses the list heading', async () => {
    const user = userEvent.setup()
    const savedConfig = oneBaseConfig
    vi.mocked(forgetBase).mockResolvedValue(savedConfig)
    let result
    const onConfigChange = vi.fn(async (nextConfig) => {
      expect(screen.getByRole('heading', { name: 'Базы заметок' })).not.toHaveFocus()
      expect(screen.getByRole('dialog')).toBeInTheDocument()
      await result.rerender({ ...result.props, config: nextConfig })
    })
    result = renderWorkspace({ onConfigChange })
    const trigger = within(baseArticle('work')).getByRole('button', { name: 'Забыть' })

    await user.click(trigger)
    expect(screen.getByRole('dialog', { name: /work/ })).toHaveTextContent(
      'Каталог и файлы останутся на диске',
    )
    await user.click(screen.getByRole('button', { name: 'Отмена' }))
    await waitFor(() => expect(trigger).toHaveFocus())

    await user.click(trigger)
    expect(screen.getByRole('dialog')).toHaveTextContent('Каталог и файлы останутся на диске')
    await user.click(screen.getByRole('button', { name: 'Забыть базу' }))

    expect(forgetBase).toHaveBeenCalledOnce()
    expect(forgetBase).toHaveBeenCalledWith('work')
    await waitFor(() => expect(onConfigChange).toHaveBeenCalledWith(savedConfig))
    expect(screen.queryByRole('article', { name: 'База work' })).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    const heading = screen.getByRole('heading', { name: 'Базы заметок' })
    await waitFor(() => expect(heading).toHaveFocus())
    expect(heading).toHaveAttribute('tabindex', '-1')
  })

  it('names the Forget dialog exactly and Escape restores the Forget trigger', async () => {
    const user = userEvent.setup()
    renderWorkspace()
    const trigger = within(baseArticle('work')).getByRole('button', { name: 'Забыть' })

    await user.click(trigger)

    const dialog = screen.getByRole('dialog', { name: 'Забыть базу work?' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    await waitFor(() => expect(
      within(dialog).getByRole('button', { name: 'Забыть базу' }),
    ).toHaveFocus())
    await user.keyboard('{Escape}')

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(trigger).toHaveFocus())
  })

  it('keeps a pending Forget modal locked and calls the API once', async () => {
    const user = userEvent.setup()
    const request = deferred()
    vi.mocked(forgetBase).mockReturnValue(request.promise)
    renderWorkspace()
    await user.click(within(baseArticle('work')).getByRole('button', { name: 'Забыть' }))
    const dialog = screen.getByRole('dialog', { name: 'Забыть базу work?' })
    const cancel = within(dialog).getByRole('button', { name: 'Отмена' })
    const confirm = within(dialog).getByRole('button', { name: 'Забыть базу' })

    await fireEvent.click(confirm)
    await fireEvent.click(confirm)

    expect(forgetBase).toHaveBeenCalledOnce()
    expect(cancel).toBeDisabled()
    expect(confirm).toBeDisabled()
    expect(confirm).toHaveAttribute('aria-busy', 'true')
    await fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.getByRole('dialog', { name: 'Забыть базу work?' })).toBeInTheDocument()

    request.resolve(oneBaseConfig)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('closes to a workspace alert when an add config callback rejects', async () => {
    const user = userEvent.setup()
    const savedConfig = config
    const onConfigChange = vi.fn().mockRejectedValue(new Error('Родитель отклонил конфигурацию'))
    const unhandled = vi.fn((event) => event.preventDefault())
    vi.mocked(createBase).mockResolvedValue(savedConfig)
    window.addEventListener('unhandledrejection', unhandled)

    try {
      renderWorkspace({ config: oneBaseConfig, onConfigChange })
      await user.click(screen.getByRole('button', { name: 'Добавить базу' }))
      await user.type(screen.getByLabelText('Имя базы'), 'work')
      await user.type(screen.getByLabelText('Родительский каталог'), '/notes')
      await user.click(screen.getByRole('button', { name: 'Добавить' }))

      expect(await screen.findByRole('alert')).toHaveTextContent('Родитель отклонил конфигурацию')
      expect(screen.queryByLabelText('Имя базы')).not.toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Добавить базу' })).toBeEnabled()
      expect(createBase).toHaveBeenCalledOnce()
      expect(onConfigChange).toHaveBeenCalledOnce()
      await Promise.resolve()
      expect(unhandled).not.toHaveBeenCalled()
    } finally {
      window.removeEventListener('unhandledrejection', unhandled)
    }
  })

  it('closes and focuses the list with an alert when a forget config callback throws', async () => {
    const user = userEvent.setup()
    const onConfigChange = vi.fn(() => {
      throw new Error('Не удалось применить конфигурацию')
    })
    const unhandled = vi.fn((event) => event.preventDefault())
    vi.mocked(forgetBase).mockResolvedValue(oneBaseConfig)
    window.addEventListener('unhandledrejection', unhandled)

    try {
      renderWorkspace({ onConfigChange })
      await user.click(within(baseArticle('work')).getByRole('button', { name: 'Забыть' }))
      await user.click(screen.getByRole('button', { name: 'Забыть базу' }))

      expect(await screen.findByRole('alert')).toHaveTextContent('Не удалось применить конфигурацию')
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      await waitFor(() => expect(
        screen.getByRole('heading', { name: 'Базы заметок' }),
      ).toHaveFocus())
      expect(screen.getByRole('button', { name: 'Добавить базу' })).toBeEnabled()
      expect(forgetBase).toHaveBeenCalledOnce()
      expect(onConfigChange).toHaveBeenCalledOnce()
      await Promise.resolve()
      expect(unhandled).not.toHaveBeenCalled()
    } finally {
      window.removeEventListener('unhandledrejection', unhandled)
    }
  })

  it('invalidates Forget against changed config without calling the API', async () => {
    const user = userEvent.setup()
    const result = renderWorkspace()
    await user.click(within(baseArticle('work')).getByRole('button', { name: 'Забыть' }))

    await result.rerender({ ...result.props, config: oneBaseConfig })
    await user.click(screen.getByRole('button', { name: 'Забыть базу' }))

    expect(forgetBase).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(
      screen.getByRole('heading', { name: 'Базы заметок' }),
    ).toHaveFocus())
  })

  it('puts a failed Forget error on its card and restores the trigger focus', async () => {
    const user = userEvent.setup()
    vi.mocked(forgetBase).mockRejectedValue(new ApiError({ message: 'Не удалось забыть базу' }))
    renderWorkspace()
    const card = baseArticle('work')
    const trigger = within(card).getByRole('button', { name: 'Забыть' })

    await user.click(trigger)
    await user.click(screen.getByRole('button', { name: 'Забыть базу' }))

    await waitFor(() => expect(within(card).getByRole('alert')).toHaveTextContent('Не удалось забыть базу'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(trigger).toHaveFocus())
  })

  it('calls only onSwitch, disables all card actions while pending, and recovers in-card', async () => {
    const user = userEvent.setup()
    const request = deferred()
    const onSwitch = vi.fn(() => request.promise)
    renderWorkspace({ onSwitch })
    const workCard = baseArticle('work')
    const open = within(workCard).getByRole('button', { name: 'Открыть' })

    await user.click(open)

    expect(onSwitch).toHaveBeenCalledOnce()
    expect(onSwitch).toHaveBeenCalledWith('work')
    expect(createBase).not.toHaveBeenCalled()
    expect(updateBase).not.toHaveBeenCalled()
    expect(forgetBase).not.toHaveBeenCalled()
    for (const action of screen.getAllByRole('button').filter((button) => (
      ['Открыть', 'Изменить', 'Забыть'].includes(button.textContent.trim())
    ))) {
      expect(action).toBeDisabled()
    }

    request.reject(new Error('Не удалось открыть базу'))
    await waitFor(() => expect(within(workCard).getByRole('alert')).toHaveTextContent('Не удалось открыть базу'))
    expect(open).toBeEnabled()
  })

  it.each([
    ['add', oneBaseConfig, createBase, 'Добавить базу', 'Добавить', 'work', '/notes'],
    ['edit', config, updateBase, 'Изменить', 'Сохранить', 'renamed', '/other'],
  ])('keeps the %s form draft after a path API error', async (
    operation,
    fixture,
    apiCall,
    openLabel,
    submitLabel,
    nextName,
    nextPath,
  ) => {
    const user = userEvent.setup()
    vi.mocked(apiCall).mockRejectedValue(new ApiError({
      field: 'path',
      message: 'Каталог недоступен',
    }))
    renderWorkspace({ config: fixture })

    if (operation === 'add') {
      await user.click(screen.getByRole('button', { name: openLabel }))
    } else {
      await user.click(within(baseArticle('personal')).getByRole('button', { name: openLabel }))
    }
    const name = screen.getByLabelText('Имя базы')
    const path = screen.getByLabelText(operation === 'add' ? 'Родительский каталог' : 'Каталог существующей базы')
    await user.clear(name)
    await user.type(name, nextName)
    await user.clear(path)
    await user.type(path, nextPath)
    await user.click(screen.getByRole('button', { name: submitLabel }))

    expect(await screen.findByText('Каталог недоступен')).toBeVisible()
    expect(name).toHaveValue(nextName)
    expect(path).toHaveValue(nextPath)
    expect(screen.queryByRole('heading', { name: 'Базы заметок' })).not.toBeInTheDocument()
  })

  it('shows a general mutation error as an alert without closing the form', async () => {
    const user = userEvent.setup()
    vi.mocked(createBase).mockRejectedValue(new ApiError({ message: 'Не удалось добавить базу' }))
    renderWorkspace({ config: oneBaseConfig })
    await user.click(screen.getByRole('button', { name: 'Добавить базу' }))
    await user.type(screen.getByLabelText('Имя базы'), 'work')
    await user.type(screen.getByLabelText('Родительский каталог'), '/notes')

    await user.click(screen.getByRole('button', { name: 'Добавить' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Не удалось добавить базу')
    expect(screen.getByLabelText('Имя базы')).toHaveValue('work')
    expect(screen.getByLabelText('Родительский каталог')).toHaveValue('/notes')
  })

  it('guards repeated mutation and switch attempts with a single flight', async () => {
    const user = userEvent.setup()
    const mutation = deferred()
    vi.mocked(createBase).mockReturnValue(mutation.promise)
    const first = renderWorkspace({ config: oneBaseConfig })
    await user.click(screen.getByRole('button', { name: 'Добавить базу' }))
    await user.type(screen.getByLabelText('Имя базы'), 'work')
    await user.type(screen.getByLabelText('Родительский каталог'), '/notes')
    const add = screen.getByRole('button', { name: 'Добавить' })

    await fireEvent.click(add)
    await fireEvent.click(add)
    expect(createBase).toHaveBeenCalledOnce()
    mutation.resolve(config)
    await waitFor(() => expect(first.props.onConfigChange).toHaveBeenCalledOnce())
    first.unmount()

    const switching = deferred()
    const onSwitch = vi.fn(() => switching.promise)
    renderWorkspace({ onSwitch })
    const open = within(baseArticle('work')).getByRole('button', { name: 'Открыть' })
    await fireEvent.click(open)
    await fireEvent.click(open)

    expect(onSwitch).toHaveBeenCalledOnce()
    switching.resolve()
    await switching.promise
  })

  it.each(['resolve', 'reject'])('ignores a pending add %s after unmount', async (settlement) => {
    const user = userEvent.setup()
    const request = deferred()
    const onConfigChange = vi.fn()
    const unhandled = vi.fn((event) => event.preventDefault())
    vi.mocked(createBase).mockReturnValue(request.promise)
    window.addEventListener('unhandledrejection', unhandled)

    try {
      const { unmount } = renderWorkspace({ config: oneBaseConfig, onConfigChange })
      await user.click(screen.getByRole('button', { name: 'Добавить базу' }))
      await user.type(screen.getByLabelText('Имя базы'), 'work')
      await user.type(screen.getByLabelText('Родительский каталог'), '/notes')
      await user.click(screen.getByRole('button', { name: 'Добавить' }))
      unmount()

      if (settlement === 'resolve') request.resolve(config)
      else request.reject(new ApiError({ message: 'Поздняя ошибка' }))
      await request.promise.catch(() => {})
      await Promise.resolve()

      expect(onConfigChange).not.toHaveBeenCalled()
      expect(unhandled).not.toHaveBeenCalled()
    } finally {
      window.removeEventListener('unhandledrejection', unhandled)
    }
  })
})
