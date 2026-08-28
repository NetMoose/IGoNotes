import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import Modal from './Modal.svelte'

function modalProps(overrides = {}) {
  return {
    show: true,
    title: 'Редактировать',
    onConfirm: vi.fn(),
    onCancel: vi.fn(),
    ...overrides,
  }
}

describe('Modal', () => {
  it('labels and describes the dialog and text input', () => {
    render(Modal, modalProps({
      input: true,
      description: 'Укажите новое значение',
      error: 'Значение недоступно',
    }))

    const dialog = screen.getByRole('dialog', { name: 'Редактировать' })
    const title = screen.getByRole('heading', { name: 'Редактировать' })
    const description = screen.getByText('Укажите новое значение')
    const error = screen.getByRole('alert')
    const input = screen.getByRole('textbox', { name: 'Редактировать' })

    expect(dialog).toHaveAttribute('aria-labelledby', title.id)
    expect(dialog.getAttribute('aria-describedby').split(' ')).toEqual([
      description.id,
      error.id,
    ])
    expect(input.id).not.toBe('')
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(input).toHaveAttribute('aria-describedby', error.id)
  })

  it('uses unique stable IDs for simultaneous instances', async () => {
    const firstProps = modalProps({
      title: 'Первый',
      input: true,
      description: 'Первое описание',
      error: 'Первая ошибка',
    })
    const first = render(Modal, firstProps)
    const firstDialog = within(first.container).getByRole('dialog', { name: 'Первый' })
    const firstTitle = within(first.container).getByRole('heading', { name: 'Первый' })
    const firstDescription = within(first.container).getByText('Первое описание')
    const firstError = within(first.container).getByRole('alert')
    const firstInput = within(first.container).getByRole('textbox', { name: 'Первый' })
    const firstIds = [firstTitle.id, firstDescription.id, firstError.id, firstInput.id]

    const second = render(Modal, modalProps({
      title: 'Второй',
      input: true,
      description: 'Второе описание',
      error: 'Вторая ошибка',
    }))
    const secondDialog = within(second.container).getByRole('dialog', { name: 'Второй' })
    const secondTitle = within(second.container).getByRole('heading', { name: 'Второй' })
    const secondDescription = within(second.container).getByText('Второе описание')
    const secondError = within(second.container).getByRole('alert')
    const secondInput = within(second.container).getByRole('textbox', { name: 'Второй' })
    const secondIds = [secondTitle.id, secondDescription.id, secondError.id, secondInput.id]

    expect(new Set([...firstIds, ...secondIds]).size).toBe(8)
    expect(firstDialog).toHaveAttribute('aria-labelledby', firstTitle.id)
    expect(firstDialog.getAttribute('aria-describedby').split(' ')).toEqual([
      firstDescription.id,
      firstError.id,
    ])
    expect(firstInput).toHaveAttribute('aria-describedby', firstError.id)
    expect(secondDialog).toHaveAttribute('aria-labelledby', secondTitle.id)
    expect(secondDialog.getAttribute('aria-describedby').split(' ')).toEqual([
      secondDescription.id,
      secondError.id,
    ])
    expect(secondInput).toHaveAttribute('aria-describedby', secondError.id)

    await first.rerender({ ...firstProps, error: 'Обновленная ошибка' })

    expect([firstTitle.id, firstDescription.id, firstError.id, firstInput.id]).toEqual(firstIds)
  })

  it('focuses the input or first available dialog action', async () => {
    const inputModal = render(Modal, modalProps({ input: true }))
    await waitFor(() => expect(screen.getByRole('textbox')).toHaveFocus())
    inputModal.unmount()

    const confirmModal = render(Modal, modalProps({ confirmText: 'Продолжить' }))
    await waitFor(() => expect(
      screen.getByRole('button', { name: 'Продолжить' }),
    ).toHaveFocus())
    confirmModal.unmount()

    render(Modal, modalProps({ confirmText: 'Продолжить', confirmDisabled: true }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Отмена' })).toHaveFocus())
  })

  it('blocks cancellation while busy and allows Escape after pending work', async () => {
    const props = modalProps({ input: true, busy: true })
    const { rerender } = render(Modal, props)
    const dialog = screen.getByRole('dialog')
    const input = screen.getByRole('textbox')
    const cancel = screen.getByRole('button', { name: 'Отмена' })
    const confirm = screen.getByRole('button', { name: 'OK' })

    expect(input).toBeDisabled()
    expect(cancel).toBeDisabled()
    expect(confirm).toBeDisabled()
    expect(confirm).toHaveAttribute('aria-busy', 'true')
    await fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(props.onCancel).not.toHaveBeenCalled()

    await rerender({ ...props, busy: false })
    await fireEvent.keyDown(dialog, { key: 'Escape' })

    expect(props.onCancel).toHaveBeenCalledOnce()
  })

  it('moves focus to the dialog when pending work disables every control', async () => {
    const props = modalProps({ input: true, confirmText: 'Сохранить' })
    const { rerender } = render(Modal, props)
    const dialog = screen.getByRole('dialog')
    const input = screen.getByRole('textbox')
    const cancel = screen.getByRole('button', { name: 'Отмена' })
    const confirm = screen.getByRole('button', { name: 'Сохранить' })
    await waitFor(() => expect(input).toHaveFocus())
    confirm.focus()
    expect(confirm).toHaveFocus()

    await rerender({ ...props, busy: true })

    expect(input).toBeDisabled()
    expect(cancel).toBeDisabled()
    expect(confirm).toBeDisabled()
    expect(dialog).toHaveAttribute('tabindex', '-1')
    await waitFor(() => expect(dialog).toHaveFocus())
    await fireEvent.keyDown(dialog, { key: 'Tab' })
    expect(dialog).toHaveFocus()
  })

  it('confirms once for an available non-composing input Enter', async () => {
    const props = modalProps({ input: true })
    const { rerender } = render(Modal, props)
    const input = screen.getByRole('textbox')

    await fireEvent.keyDown(input, { key: 'Enter' })
    expect(props.onConfirm).toHaveBeenCalledOnce()

    await fireEvent.keyDown(input, { key: 'Enter', repeat: true })
    await fireEvent.keyDown(input, { key: 'Enter', isComposing: true })
    expect(props.onConfirm).toHaveBeenCalledOnce()

    await rerender({ ...props, busy: true })
    await fireEvent.keyDown(input, { key: 'Enter' })
    expect(props.onConfirm).toHaveBeenCalledOnce()

    await rerender({ ...props, busy: false, confirmDisabled: true })
    await fireEvent.keyDown(input, { key: 'Enter' })
    expect(props.onConfirm).toHaveBeenCalledOnce()
  })

  it.each(['close', 'unmount'])(
    'traps focus, makes the background inert, and restores both on %s',
    async (settlement) => {
      const user = userEvent.setup()
      const trigger = document.createElement('button')
      const preservedInert = document.createElement('div')
      trigger.textContent = 'Открыть'
      preservedInert.setAttribute('inert', 'preserved')
      document.body.append(trigger, preservedInert)
      trigger.focus()
      const props = modalProps({ input: true, confirmText: 'Готово' })
      const result = render(Modal, props)

      try {
        const input = screen.getByRole('textbox')
        const cancel = screen.getByRole('button', { name: 'Отмена' })
        const confirm = screen.getByRole('button', { name: 'Готово' })
        await waitFor(() => expect(input).toHaveFocus())
        expect(trigger).toHaveAttribute('inert')
        expect(preservedInert).toHaveAttribute('inert')

        await user.tab()
        expect(cancel).toHaveFocus()
        await user.tab()
        expect(confirm).toHaveFocus()
        await user.tab()
        expect(input).toHaveFocus()
        await user.tab({ shift: true })
        expect(confirm).toHaveFocus()

        if (settlement === 'close') {
          await result.rerender({ ...props, show: false })
        } else {
          result.unmount()
        }

        await waitFor(() => expect(trigger).toHaveFocus())
        expect(trigger).not.toHaveAttribute('inert')
        expect(preservedInert).toHaveAttribute('inert', 'preserved')
      } finally {
        result.unmount()
        trigger.remove()
        preservedInert.remove()
      }
    },
  )

  it.each(['close', 'unmount'])(
    'inerts every ancestor sibling and restores exact prior state on %s',
    async (settlement) => {
      const bodySibling = document.createElement('button')
      const outer = document.createElement('div')
      const preservedSibling = document.createElement('div')
      const modalBranch = document.createElement('div')
      const innerSibling = document.createElement('button')
      const target = document.createElement('div')
      preservedSibling.setAttribute('inert', 'preserved')
      document.body.append(bodySibling, outer)
      outer.append(preservedSibling, modalBranch)
      modalBranch.append(innerSibling, target)
      const props = modalProps()
      const result = render(Modal, { target, props })

      try {
        await waitFor(() => expect(screen.getByRole('dialog')).toBeVisible())
        expect(bodySibling).toHaveAttribute('inert')
        expect(preservedSibling).toHaveAttribute('inert')
        expect(innerSibling).toHaveAttribute('inert')

        if (settlement === 'close') {
          await result.rerender({ ...props, show: false })
        } else {
          result.unmount()
        }

        expect(bodySibling).not.toHaveAttribute('inert')
        expect(preservedSibling).toHaveAttribute('inert', 'preserved')
        expect(innerSibling).not.toHaveAttribute('inert')
      } finally {
        result.unmount()
        outer.remove()
        bodySibling.remove()
      }
    },
  )

  it('uses red danger styling and blue default styling', async () => {
    const props = modalProps({ danger: true, confirmText: 'Удалить' })
    const { rerender } = render(Modal, props)
    const confirm = screen.getByRole('button', { name: 'Удалить' })

    expect(confirm).toHaveClass('bg-red-600')
    expect(confirm).not.toHaveClass('bg-blue-600')

    await rerender({ ...props, danger: false })

    expect(confirm).toHaveClass('bg-blue-600')
    expect(confirm).not.toHaveClass('bg-red-600')
  })

  it('stacks actions primary-first on narrow screens and gives controls focus outlines', () => {
    render(Modal, modalProps({ input: true, confirmText: 'Продолжить' }))
    const input = screen.getByRole('textbox')
    const cancel = screen.getByRole('button', { name: 'Отмена' })
    const confirm = screen.getByRole('button', { name: 'Продолжить' })

    expect(confirm.parentElement).toHaveClass('flex-col-reverse', 'sm:flex-row')
    for (const control of [input, cancel, confirm]) {
      expect(control).toHaveClass(
        'focus-visible:outline-2',
        'focus-visible:outline-offset-2',
        'focus-visible:outline-blue-600',
      )
      expect(control).not.toHaveClass('focus-visible:outline-none')
    }
  })
})
