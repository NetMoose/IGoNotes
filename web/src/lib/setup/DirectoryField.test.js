import { render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual, selectDirectory: vi.fn() }
})

import { ApiError, selectDirectory } from '../api.js'
import DirectoryField from './DirectoryField.svelte'

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

describe('DirectoryField', () => {
  beforeEach(() => {
    vi.mocked(selectDirectory).mockReset()
  })

  it('writes a selected directory to the bindable input and clears the notice', async () => {
    const user = userEvent.setup()
    const onPickerNotice = vi.fn()
    vi.mocked(selectDirectory).mockResolvedValue('/home/user/notes')
    render(DirectoryField, {
      id: 'path',
      value: '',
      onPickerNotice,
    })

    await user.click(screen.getByRole('button', { name: 'Обзор' }))

    await waitFor(() => expect(screen.getByLabelText('Каталог')).toHaveValue('/home/user/notes'))
    expect(onPickerNotice).toHaveBeenCalledOnce()
    expect(onPickerNotice).toHaveBeenCalledWith('')
  })

  it('keeps manual input when directory selection is cancelled', async () => {
    const user = userEvent.setup()
    const onPickerNotice = vi.fn()
    vi.mocked(selectDirectory).mockResolvedValue(null)
    render(DirectoryField, { id: 'path', value: '/manual/path', onPickerNotice })

    await user.click(screen.getByRole('button', { name: 'Обзор' }))

    expect(screen.getByLabelText('Каталог')).toHaveValue('/manual/path')
    expect(onPickerNotice).toHaveBeenCalledOnce()
    expect(onPickerNotice).toHaveBeenCalledWith('')
  })

  it('falls back to manual input when the system picker is unavailable', async () => {
    const user = userEvent.setup()
    const onPickerNotice = vi.fn()
    vi.mocked(selectDirectory).mockRejectedValue(new ApiError({
      status: 501,
      code: 'directory_picker_unavailable',
      message: 'Выбор каталога не поддерживается',
    }))
    render(DirectoryField, { id: 'path', value: '/manual/path', onPickerNotice })

    await user.click(screen.getByRole('button', { name: 'Обзор' }))

    await waitFor(() => expect(onPickerNotice).toHaveBeenLastCalledWith(
      'Системный выбор каталога недоступен. Введите путь вручную.',
    ))
    expect(screen.getByLabelText('Каталог')).toBeEnabled()
    expect(screen.getByLabelText('Каталог')).toHaveValue('/manual/path')
  })

  it('connects a visible path error to the input accessibility state', () => {
    render(DirectoryField, {
      id: 'path',
      hint: 'Выберите существующий каталог',
      error: 'Каталог не найден',
    })

    const input = screen.getByLabelText('Каталог')
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(input).toHaveAttribute('aria-describedby', 'path-hint path-error')
    expect(screen.getByText('Выберите существующий каталог')).toHaveAttribute('id', 'path-hint')
    expect(screen.getByText('Каталог не найден')).toHaveAttribute('id', 'path-error')
  })

  it('connects a visible hint to the input without marking it invalid', () => {
    render(DirectoryField, { id: 'path', hint: 'Можно ввести путь вручную' })

    const input = screen.getByLabelText('Каталог')
    expect(input).not.toHaveAttribute('aria-invalid')
    expect(input).toHaveAttribute('aria-describedby', 'path-hint')
    expect(screen.getByText('Можно ввести путь вручную')).toHaveAttribute('id', 'path-hint')
  })

  it('shows pending picker state and restores controls after selection', async () => {
    const user = userEvent.setup()
    const request = deferred()
    vi.mocked(selectDirectory).mockReturnValue(request.promise)
    render(DirectoryField, { id: 'path', value: '' })
    const button = screen.getByRole('button', { name: 'Обзор' })

    await user.click(button)

    expect(screen.getByRole('status')).toHaveTextContent('Выбор каталога...')
    expect(button).toBeDisabled()
    expect(screen.getByLabelText('Каталог')).toBeEnabled()

    request.resolve('/home/user/selected')

    await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument())
    expect(button).toBeEnabled()
    expect(screen.getByLabelText('Каталог')).toHaveValue('/home/user/selected')
  })

  it('discards a pending selection after the field context changes', async () => {
    const user = userEvent.setup()
    const request = deferred()
    const onPickerNotice = vi.fn()
    vi.mocked(selectDirectory).mockReturnValue(request.promise)
    const { rerender } = render(DirectoryField, {
      id: 'path',
      value: '/draft-a',
      onPickerNotice,
    })
    const button = screen.getByRole('button', { name: 'Обзор' })

    await user.click(button)
    expect(screen.getByRole('status')).toHaveTextContent('Выбор каталога...')

    await rerender({ id: 'path', value: '/draft-b', onPickerNotice })
    expect(screen.getByLabelText('Каталог')).toHaveValue('/draft-b')

    request.resolve('/picked-for-a')

    await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument())
    expect(button).toBeEnabled()
    expect(screen.getByLabelText('Каталог')).toHaveValue('/draft-b')
    expect(onPickerNotice).toHaveBeenCalledOnce()
    expect(onPickerNotice).toHaveBeenCalledWith('')
  })

  it.each(['resolve', 'reject'])(
    'ignores a stale picker %s after unmount',
    async (settlement) => {
      const user = userEvent.setup()
      const request = deferred()
      const onPickerNotice = vi.fn()
      const setValue = vi.fn()
      let value = '/manual/path'
      vi.mocked(selectDirectory).mockReturnValue(request.promise)
      const { unmount } = render(DirectoryField, {
        id: 'path',
        get value() { return value },
        set value(nextValue) {
          setValue(nextValue)
          value = nextValue
        },
        onPickerNotice,
      })

      await user.click(screen.getByRole('button', { name: 'Обзор' }))
      expect(onPickerNotice).toHaveBeenCalledOnce()
      expect(onPickerNotice).toHaveBeenCalledWith('')

      unmount()
      if (settlement === 'resolve') {
        request.resolve('/late/path')
      } else {
        request.reject(new Error('late picker failure'))
      }
      await request.promise.catch(() => {})
      await Promise.resolve()

      expect(onPickerNotice).toHaveBeenCalledOnce()
      expect(setValue).not.toHaveBeenCalled()
    },
  )

  it('forwards operational picker errors and re-enables the browse button', async () => {
    const user = userEvent.setup()
    const onPickerNotice = vi.fn()
    vi.mocked(selectDirectory).mockRejectedValue(new Error('Не удалось открыть выбор каталога'))
    render(DirectoryField, { id: 'path', value: '/manual/path', onPickerNotice })
    const button = screen.getByRole('button', { name: 'Обзор' })

    await user.click(button)

    await waitFor(() => expect(onPickerNotice).toHaveBeenLastCalledWith(
      'Не удалось открыть выбор каталога',
    ))
    expect(button).toBeEnabled()
    expect(screen.getByLabelText('Каталог')).toHaveValue('/manual/path')
  })

  it('disables manual input and directory browsing', () => {
    render(DirectoryField, { id: 'path', disabled: true })

    expect(screen.getByLabelText('Каталог')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Обзор' })).toBeDisabled()
  })
})
