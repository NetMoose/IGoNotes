import { render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual, completeSetup: vi.fn() }
})

import { ApiError, completeSetup } from '../api.js'
import SetupWizard from './SetupWizard.svelte'

const firstRunConfig = {
  base_dir: '',
  bases: [],
  current_base: '',
  setup_completed: false,
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

function renderWizard(overrides = {}) {
  const props = {
    config: firstRunConfig,
    onComplete: vi.fn(),
    ...overrides,
  }
  return { props, ...render(SetupWizard, props) }
}

async function openDetails(user, mode) {
  await user.click(screen.getByRole('button', { name: mode }))
  await screen.findByRole('heading', { name: 'Укажите имя и каталог' })
}

async function enterDetails(user, { name, path, pathLabel }) {
  await user.type(screen.getByLabelText('Имя базы'), name)
  await user.type(screen.getByLabelText(pathLabel), path)
  await user.click(screen.getByRole('button', { name: 'Продолжить' }))
  await screen.findByRole('heading', { name: 'Проверьте настройки' })
}

describe('SetupWizard', () => {
  beforeEach(() => {
    vi.mocked(completeSetup).mockReset()
  })

  it('starts with only the two first-run mode cards', () => {
    renderWizard()

    expect(screen.getByRole('heading', { name: 'Настройте первую базу' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'Создать новую' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'Подключить существующую' })).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Назад' })).not.toBeInTheDocument()
    expect(screen.queryByText('Выберите заметку')).not.toBeInTheDocument()
    expect(screen.queryByText('Выберите файл в меню слева')).not.toBeInTheDocument()
    expect(document.querySelector('.cm-editor')).not.toBeInTheDocument()
  })

  it('uses one main landmark and keeps focus and current-step semantics in sync', async () => {
    const user = userEvent.setup()
    renderWizard()

    expect(screen.getAllByRole('main')).toHaveLength(1)
    const initialHeading = screen.getByRole('heading', { name: 'Настройте первую базу' })
    await waitFor(() => expect(initialHeading).toHaveFocus())
    expect(screen.getByText('1')).toHaveAttribute('aria-current', 'step')
    expect(screen.getByText('2')).not.toHaveAttribute('aria-current')
    expect(screen.getByText('3')).not.toHaveAttribute('aria-current')

    await openDetails(user, 'Создать новую')
    const detailsHeading = screen.getByRole('heading', { name: 'Укажите имя и каталог' })
    await waitFor(() => expect(detailsHeading).toHaveFocus())
    expect(screen.getByText('1')).not.toHaveAttribute('aria-current')
    expect(screen.getByText('2')).toHaveAttribute('aria-current', 'step')

    await enterDetails(user, {
      name: 'work',
      path: '/home/user/notes',
      pathLabel: 'Родительский каталог',
    })
    const reviewHeading = screen.getByRole('heading', { name: 'Проверьте настройки' })
    await waitFor(() => expect(reviewHeading).toHaveFocus())
    expect(screen.getByText('2')).not.toHaveAttribute('aria-current')
    expect(screen.getByText('3')).toHaveAttribute('aria-current', 'step')

    await user.click(screen.getByRole('button', { name: 'Назад' }))
    await waitFor(() => expect(
      screen.getByRole('heading', { name: 'Укажите имя и каталог' }),
    ).toHaveFocus())
    expect(screen.getByText('2')).toHaveAttribute('aria-current', 'step')
  })

  it.each([
    ['Создать новую', 'Родительский каталог'],
    ['Подключить существующую', 'Каталог существующей базы'],
  ])('labels the name and path controls in %s mode', async (mode, pathLabel) => {
    const user = userEvent.setup()
    renderWizard()

    await openDetails(user, mode)

    expect(screen.getByLabelText('Имя базы')).toBeInTheDocument()
    expect(screen.getByLabelText(pathLabel)).toBeInTheDocument()
  })

  it('reviews a create draft with the resolved base path', async () => {
    const user = userEvent.setup()
    renderWizard()
    await openDetails(user, 'Создать новую')

    await enterDetails(user, {
      name: 'work',
      path: '/home/user/notes',
      pathLabel: 'Родительский каталог',
    })

    expect(screen.getByText('/home/user/notes/work')).toBeVisible()
  })

  it('reviews a connect draft with the exact existing path', async () => {
    const user = userEvent.setup()
    renderWizard()
    await openDetails(user, 'Подключить существующую')

    await enterDetails(user, {
      name: 'work',
      path: '/srv/existing',
      pathLabel: 'Каталог существующей базы',
    })

    expect(screen.getByText('/srv/existing')).toBeVisible()
  })

  it('returns from review with the name and path preserved', async () => {
    const user = userEvent.setup()
    renderWizard()
    await openDetails(user, 'Создать новую')
    await enterDetails(user, {
      name: 'work',
      path: '/home/user/notes',
      pathLabel: 'Родительский каталог',
    })

    await user.click(screen.getByRole('button', { name: 'Назад' }))

    expect(await screen.findByLabelText('Имя базы')).toHaveValue('work')
    expect(screen.getByLabelText('Родительский каталог')).toHaveValue('/home/user/notes')
  })

  it('returns from details without an internal cancel button and preserves the draft mode', async () => {
    const user = userEvent.setup()
    renderWizard()
    await openDetails(user, 'Подключить существующую')
    await user.type(screen.getByLabelText('Имя базы'), 'work')
    await user.type(screen.getByLabelText('Каталог существующей базы'), '/srv/existing')

    expect(screen.queryByRole('button', { name: 'Отмена' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Назад' }))

    expect(screen.getByRole('heading', { name: 'Настройте первую базу' })).toBeVisible()
    await openDetails(user, 'Подключить существующую')
    expect(screen.getByLabelText('Имя базы')).toHaveValue('work')
    expect(screen.getByLabelText('Каталог существующей базы')).toHaveValue('/srv/existing')
    expect(screen.queryByRole('button', { name: 'Отмена' })).not.toBeInTheDocument()
  })

  it('returns a field API error to the form without losing the other field', async () => {
    const user = userEvent.setup()
    vi.mocked(completeSetup).mockRejectedValue(new ApiError({
      field: 'name',
      message: 'База с таким именем уже добавлена',
    }))
    renderWizard()
    await openDetails(user, 'Создать новую')
    await enterDetails(user, {
      name: 'work',
      path: '/home/user/notes',
      pathLabel: 'Родительский каталог',
    })

    await user.click(screen.getByRole('button', { name: 'Завершить настройку' }))

    expect(await screen.findByRole('heading', { name: 'Укажите имя и каталог' })).toBeVisible()
    expect(screen.getByText('База с таким именем уже добавлена')).toBeVisible()
    expect(screen.getByLabelText('Родительский каталог')).toHaveValue('/home/user/notes')
  })

  it('keeps a nonfield completion error on review as an alert', async () => {
    const user = userEvent.setup()
    vi.mocked(completeSetup).mockRejectedValue(new ApiError({
      message: 'Не удалось завершить настройку',
    }))
    renderWizard()
    await openDetails(user, 'Подключить существующую')
    await enterDetails(user, {
      name: 'work',
      path: '/srv/existing',
      pathLabel: 'Каталог существующей базы',
    })

    await user.click(screen.getByRole('button', { name: 'Завершить настройку' }))

    expect(screen.getByRole('heading', { name: 'Проверьте настройки' })).toBeVisible()
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Не удалось завершить настройку')
    expect(alert).toHaveAttribute('tabindex', '-1')
    await waitFor(() => expect(alert).toHaveFocus())
  })

  it('completes once and protects a pending finish from repeated clicks', async () => {
    const user = userEvent.setup()
    const request = deferred()
    const savedConfig = {
      base_dir: '/home/user/notes',
      bases: [{ name: 'work', path: '/home/user/notes/work', auto_sync: false }],
      current_base: 'work',
      setup_completed: true,
    }
    vi.mocked(completeSetup).mockReturnValue(request.promise)
    const { props } = renderWizard()
    await openDetails(user, 'Создать новую')
    await enterDetails(user, {
      name: 'work',
      path: '/home/user/notes',
      pathLabel: 'Родительский каталог',
    })
    const finish = screen.getByRole('button', { name: 'Завершить настройку' })

    await user.dblClick(finish)

    expect(completeSetup).toHaveBeenCalledOnce()
    expect(completeSetup).toHaveBeenCalledWith({
      mode: 'create',
      name: 'work',
      path: '/home/user/notes',
    })
    expect(finish).toBeDisabled()
    expect(finish).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByRole('button', { name: 'Назад' })).toBeDisabled()

    request.resolve(savedConfig)
    await waitFor(() => expect(props.onComplete).toHaveBeenCalledOnce())
    expect(props.onComplete).toHaveBeenCalledWith(savedConfig)
  })

  it.each(['resolve', 'reject'])(
    'ignores a pending completion %s after unmount',
    async (settlement) => {
      const user = userEvent.setup()
      const request = deferred()
      const onComplete = vi.fn()
      const unhandled = vi.fn((event) => event.preventDefault())
      vi.mocked(completeSetup).mockReturnValue(request.promise)
      window.addEventListener('unhandledrejection', unhandled)

      try {
        const { unmount } = renderWizard({ onComplete })
        await openDetails(user, 'Создать новую')
        await enterDetails(user, {
          name: 'work',
          path: '/home/user/notes',
          pathLabel: 'Родительский каталог',
        })
        await user.click(screen.getByRole('button', { name: 'Завершить настройку' }))
        expect(completeSetup).toHaveBeenCalledOnce()

        unmount()
        if (settlement === 'resolve') {
          request.resolve({ ...firstRunConfig, setup_completed: true })
        } else {
          request.reject(new ApiError({ message: 'Поздняя ошибка' }))
        }
        await request.promise.catch(() => {})
        await Promise.resolve()

        expect(onComplete).not.toHaveBeenCalled()
        expect(unhandled).not.toHaveBeenCalled()
        expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      } finally {
        window.removeEventListener('unhandledrejection', unhandled)
      }
    },
  )

  it('moves focus to the heading after each step transition', async () => {
    const user = userEvent.setup()
    renderWizard()

    await openDetails(user, 'Создать новую')
    await waitFor(() => expect(
      screen.getByRole('heading', { name: 'Укажите имя и каталог' }),
    ).toHaveFocus())

    await enterDetails(user, {
      name: 'work',
      path: '/home/user/notes',
      pathLabel: 'Родительский каталог',
    })
    await waitFor(() => expect(
      screen.getByRole('heading', { name: 'Проверьте настройки' }),
    ).toHaveFocus())
  })
})
