import { render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual, selectDirectory: vi.fn() }
})

import { ApiError, selectDirectory } from '../api.js'
import BaseForm from './BaseForm.svelte'

function formProps(overrides = {}) {
  return {
    formId: 'base-form',
    mode: 'create',
    submitLabel: 'Продолжить',
    onSubmit: vi.fn(),
    ...overrides,
  }
}

describe('BaseForm', () => {
  beforeEach(() => {
    vi.mocked(selectDirectory).mockReset()
  })

  it('validates an empty create draft and focuses the name field', async () => {
    const user = userEvent.setup()
    const props = formProps()
    render(BaseForm, props)

    await user.click(screen.getByRole('button', { name: 'Продолжить' }))

    const nameError = screen.getByText('Введите имя базы')
    const pathError = screen.getByText('Укажите каталог')
    const name = screen.getByLabelText('Имя базы')
    const path = screen.getByLabelText('Родительский каталог')
    expect(nameError).toBeVisible()
    expect(pathError).toBeVisible()
    expect(name).toHaveAttribute('aria-describedby', nameError.id)
    expect(path).toHaveAttribute('aria-describedby', pathError.id)
    await waitFor(() => expect(name).toHaveFocus())
    expect(props.onSubmit).not.toHaveBeenCalled()
  })

  it('submits an exactly normalized create draft', async () => {
    const user = userEvent.setup()
    const props = formProps()
    render(BaseForm, props)

    await user.type(screen.getByLabelText('Имя базы'), ' work ')
    await user.type(screen.getByLabelText('Родительский каталог'), ' /home/user/notes ')
    await user.click(screen.getByRole('button', { name: 'Продолжить' }))

    expect(props.onSubmit).toHaveBeenCalledOnce()
    expect(props.onSubmit).toHaveBeenCalledWith({
      mode: 'create',
      name: 'work',
      path: '/home/user/notes',
    })
  })

  it('maps a new field API error without discarding the current draft', async () => {
    const user = userEvent.setup()
    const props = formProps()
    const { rerender } = render(BaseForm, props)
    const nameInput = screen.getByLabelText('Имя базы')
    const pathInput = screen.getByLabelText('Родительский каталог')
    await user.type(nameInput, 'work')
    await user.type(pathInput, '/home/user/notes')

    await rerender({
      ...props,
      apiError: new ApiError({ field: 'path', message: 'Каталог не найден' }),
    })

    const error = screen.getByText('Каталог не найден')
    expect(error).toBeVisible()
    expect(pathInput).toHaveAttribute('aria-invalid', 'true')
    expect(pathInput).toHaveAttribute('aria-describedby', expect.stringContaining(error.id))
    expect(nameInput).toHaveValue('work')
    expect(pathInput).toHaveValue('/home/user/notes')
    await waitFor(() => expect(pathInput).toHaveFocus())

    await user.type(pathInput, '/updated')

    expect(screen.queryByText('Каталог не найден')).not.toBeInTheDocument()
    expect(pathInput).not.toHaveAttribute('aria-invalid')
    expect(nameInput).toHaveValue('work')
    expect(pathInput).toHaveValue('/home/user/notes/updated')
  })

  it('adopts a changed mode prop without resetting the current draft', async () => {
    const user = userEvent.setup()
    const props = formProps({ showMode: true })
    const { rerender } = render(BaseForm, props)
    await user.type(screen.getByLabelText('Имя базы'), 'team/shared')
    await user.type(screen.getByLabelText('Родительский каталог'), '/home/user/notes')

    await rerender({ ...props, mode: 'connect' })

    expect(screen.getByRole('radio', { name: 'Подключить существующую' })).toBeChecked()
    expect(screen.getByLabelText('Имя базы')).toHaveValue('team/shared')
    expect(screen.getByLabelText('Каталог существующей базы')).toHaveValue('/home/user/notes')

    await user.click(screen.getByRole('button', { name: 'Продолжить' }))

    expect(screen.queryByText('Имя новой базы не может быть точкой и содержать / или \\')).not.toBeInTheDocument()
    expect(props.onSubmit).toHaveBeenCalledOnce()
    expect(props.onSubmit).toHaveBeenCalledWith({
      mode: 'connect',
      name: 'team/shared',
      path: '/home/user/notes',
    })
  })

  it('does not focus a replacement form from stale scheduled work', async () => {
    const user = userEvent.setup()
    let scheduledFocus
    vi.stubGlobal('requestAnimationFrame', vi.fn((callback) => {
      scheduledFocus = callback
      return 1
    }))

    try {
      const first = render(BaseForm, formProps())
      await user.click(screen.getByRole('button', { name: 'Продолжить' }))
      expect(scheduledFocus).toBeTypeOf('function')
      first.unmount()

      render(BaseForm, formProps())
      const replacementName = screen.getByLabelText('Имя базы')
      scheduledFocus()

      expect(replacementName).not.toHaveFocus()
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('submits edit fields without mode and disables controls while busy', async () => {
    const user = userEvent.setup()
    const props = formProps({
      mode: 'edit',
      initialName: 'work',
      initialPath: '/home/user/notes',
      submitLabel: 'Сохранить',
    })
    const { rerender } = render(BaseForm, props)

    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    expect(props.onSubmit).toHaveBeenCalledOnce()
    expect(props.onSubmit).toHaveBeenCalledWith({
      name: 'work',
      path: '/home/user/notes',
    })

    await rerender({ ...props, busy: true })

    expect(screen.getByLabelText('Имя базы')).toBeDisabled()
    expect(screen.getByLabelText('Каталог существующей базы')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Обзор' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Сохранить' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Сохранить' })).toHaveAttribute('aria-busy', 'true')
  })

  it('shows accessible mode cards and changes the directory label for connect mode', async () => {
    const user = userEvent.setup()
    render(BaseForm, formProps({ showMode: true }))

    const create = screen.getByRole('radio', { name: 'Создать новую' })
    const connect = screen.getByRole('radio', { name: 'Подключить существующую' })
    expect(create).toBeChecked()
    expect(connect).not.toBeChecked()
    expect(screen.getByLabelText('Родительский каталог')).toBeInTheDocument()

    await user.click(connect)

    expect(connect).toBeChecked()
    expect(screen.getByLabelText('Каталог существующей базы')).toBeInTheDocument()
  })

  it('shows a general API alert and a nonblocking directory picker hint', async () => {
    const user = userEvent.setup()
    vi.mocked(selectDirectory).mockRejectedValue(new ApiError({
      status: 501,
      code: 'directory_picker_unavailable',
      message: 'Выбор каталога не поддерживается',
    }))
    render(BaseForm, formProps({
      apiError: new ApiError({ message: 'Не удалось сохранить базу' }),
    }))

    expect(screen.getByRole('alert')).toHaveTextContent('Не удалось сохранить базу')
    await user.click(screen.getByRole('button', { name: 'Обзор' }))

    const notice = await screen.findByText(
      'Системный выбор каталога недоступен. Введите путь вручную.',
    )
    expect(notice).toBeVisible()
    expect(screen.getAllByRole('alert')).toHaveLength(1)
    expect(screen.getByLabelText('Родительский каталог')).toHaveAttribute(
      'aria-describedby',
      expect.stringContaining(notice.id),
    )
  })

  it('focuses a newly received general API alert', async () => {
    const props = formProps()
    const { rerender } = render(BaseForm, props)

    await rerender({
      ...props,
      apiError: new ApiError({ message: 'Не удалось сохранить базу' }),
    })

    const alert = screen.getByRole('alert')
    expect(alert).toHaveAttribute('tabindex', '-1')
    await waitFor(() => expect(alert).toHaveFocus())
  })
})
