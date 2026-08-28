import { render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual, selectDirectory: vi.fn() }
})

import { ApiError, selectDirectory } from '../api.js'
import DirectoryField from './DirectoryField.svelte'

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
    render(DirectoryField, { id: 'path', error: 'Каталог не найден' })

    const input = screen.getByLabelText('Каталог')
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(input).toHaveAttribute('aria-describedby', 'path-error')
    expect(screen.getByText('Каталог не найден')).toHaveAttribute('id', 'path-error')
  })

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
