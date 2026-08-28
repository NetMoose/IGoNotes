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
    expect(screen.getByRole('tab', { name: 'Базы' })).toHaveAttribute('aria-current', 'page')
    const gitTab = screen.getByRole('tab', { name: 'Git' })
    expect(gitTab).toHaveAttribute('aria-disabled', 'true')
    expect(gitTab).toHaveAttribute('tabindex', '-1')
    expect(screen.getAllByRole('button', { name: 'Забыть' })).toHaveLength(1)
    expect(screen.getAllByText('Git не настроен')).toHaveLength(2)
    expect(screen.getAllByText('Автосинхронизация выключена')).toHaveLength(2)
  })

  it('keeps only Edit actions when there is one active base', () => {
    renderWorkspace({ config: oneBaseConfig })
    const card = baseArticle('personal')

    expect(within(card).queryByRole('button', { name: 'Открыть' })).not.toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: 'Забыть' })).not.toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Изменить' })).toBeEnabled()
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

  it('cancels Forget back to its trigger, then forgets and focuses the list heading', async () => {
    const user = userEvent.setup()
    const savedConfig = oneBaseConfig
    vi.mocked(forgetBase).mockResolvedValue(savedConfig)
    const { props } = renderWorkspace()
    const trigger = within(baseArticle('work')).getByRole('button', { name: 'Забыть' })

    await user.click(trigger)
    expect(screen.getByRole('dialog')).toHaveTextContent('Каталог и файлы останутся на диске')
    await user.click(screen.getByRole('button', { name: 'Отмена' }))
    await waitFor(() => expect(trigger).toHaveFocus())

    await user.click(trigger)
    await user.click(screen.getByRole('button', { name: 'Забыть базу' }))

    expect(forgetBase).toHaveBeenCalledOnce()
    expect(forgetBase).toHaveBeenCalledWith('work')
    await waitFor(() => expect(props.onConfigChange).toHaveBeenCalledWith(savedConfig))
    const heading = screen.getByRole('heading', { name: 'Базы заметок' })
    await waitFor(() => expect(heading).toHaveFocus())
    expect(heading).toHaveAttribute('tabindex', '-1')
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
