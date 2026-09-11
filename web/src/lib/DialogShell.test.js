import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import DialogShellHost from '../test/DialogShellHost.svelte'

function shellProps(overrides = {}) {
  return {
    show: true,
    title: 'Подтвердить действие',
    onCancel: vi.fn(),
    ...overrides,
  }
}

describe('DialogShell', () => {
  it('labels and describes the dialog, exposes its busy state, and accepts a custom width', () => {
    render(DialogShellHost, shellProps({
      description: 'Это действие нельзя отменить',
      error: 'Попробуйте снова',
      busy: true,
      maxWidth: 'max-w-6xl',
    }))

    const dialog = screen.getByRole('dialog', { name: 'Подтвердить действие' })
    const title = screen.getByRole('heading', { name: 'Подтвердить действие' })
    const description = screen.getByText('Это действие нельзя отменить')
    const error = screen.getByRole('alert')

    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveAttribute('aria-labelledby', title.id)
    expect(dialog.getAttribute('aria-describedby').split(' ')).toEqual([description.id, error.id])
    expect(dialog).toHaveAttribute('aria-busy', 'true')
    expect(dialog).toHaveAttribute('tabindex', '-1')
    expect(dialog).toHaveClass('max-w-6xl')
    expect(screen.getByRole('button', { name: 'Подтвердить' }).parentElement)
      .toHaveClass('flex-col-reverse', 'sm:flex-row')
  })

  it('focuses the host initial target, traps focus, inerts the background, and restores the trigger', async () => {
    const user = userEvent.setup()
    const trigger = document.createElement('button')
    trigger.textContent = 'Внешний триггер'
    document.body.append(trigger)
    const props = shellProps({ show: false })
    const result = render(DialogShellHost, props)

    try {
      trigger.focus()
      await result.rerender({ ...props, show: true })
      const input = screen.getByRole('textbox', { name: 'Значение' })
      const cancel = screen.getByRole('button', { name: 'Отмена' })
      const confirm = screen.getByRole('button', { name: 'Подтвердить' })

      await waitFor(() => expect(input).toHaveFocus())
      expect(trigger).toHaveAttribute('inert')
      await user.tab()
      expect(cancel).toHaveFocus()
      await user.tab()
      expect(confirm).toHaveFocus()
      await user.tab()
      expect(input).toHaveFocus()

      await result.rerender({ ...props, show: false })
      await waitFor(() => expect(trigger).toHaveFocus())
      expect(trigger).not.toHaveAttribute('inert')
    } finally {
      result.unmount()
      trigger.remove()
    }
  })

  it('includes contenteditable elements in the sequential focus trap', async () => {
    const user = userEvent.setup()
    render(DialogShellHost, shellProps({ includeContenteditable: true }))
    const input = screen.getByRole('textbox', { name: 'Значение' })
    const cancel = screen.getByRole('button', { name: 'Отмена' })
    const confirm = screen.getByRole('button', { name: 'Подтвердить' })
    const editor = screen.getByRole('textbox', { name: 'Редактор' })

    await waitFor(() => expect(input).toHaveFocus())
    await user.tab()
    expect(cancel).toHaveFocus()
    await user.tab()
    expect(confirm).toHaveFocus()
    await user.tab()
    expect(editor).toHaveFocus()
    await user.tab()
    expect(input).toHaveFocus()
  })

  it('excludes tabindex minus one controls from focus trap ordering', async () => {
    render(DialogShellHost, shellProps({ includeTabindexMinusOne: true }))
    const input = screen.getByRole('textbox', { name: 'Значение' })
    const confirm = screen.getByRole('button', { name: 'Подтвердить' })
    const skipped = screen.getByRole('button', { name: 'Пропустить', hidden: true })

    await waitFor(() => expect(input).toHaveFocus())
    confirm.focus()
    await fireEvent.keyDown(confirm, { key: 'Tab' })

    expect(input).toHaveFocus()
    expect(skipped).not.toHaveFocus()
  })

  it('does not cancel with Escape while busy', async () => {
    const props = shellProps({ busy: true })
    const { rerender } = render(DialogShellHost, props)
    const dialog = screen.getByRole('dialog')

    await fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(props.onCancel).not.toHaveBeenCalled()

    await rerender({ ...props, busy: false })
    await fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(props.onCancel).toHaveBeenCalledOnce()
  })

  it('keeps shared backgrounds inert until every nested dialog closes', async () => {
    const trigger = document.createElement('button')
    trigger.textContent = 'Внешний триггер'
    document.body.append(trigger)
    const firstProps = shellProps()
    const first = render(DialogShellHost, firstProps)
    const secondProps = shellProps({ title: 'Второй диалог' })
    const second = render(DialogShellHost, secondProps)

    try {
      await waitFor(() => expect(screen.getAllByRole('dialog')).toHaveLength(2))
      expect(trigger).toHaveAttribute('inert')

      await first.rerender({ ...firstProps, show: false })
      expect(trigger).toHaveAttribute('inert')

      await second.rerender({ ...secondProps, show: false })
      expect(trigger).not.toHaveAttribute('inert')
    } finally {
      first.unmount()
      second.unmount()
      trigger.remove()
    }
  })

  it('restores focus to the host trigger after closing', async () => {
    const props = shellProps({ show: false })
    const { rerender } = render(DialogShellHost, props)
    const trigger = screen.getByRole('button', { name: 'Открыть' })
    trigger.focus()

    await rerender({ ...props, show: true })
    await waitFor(() => expect(screen.getByRole('textbox')).toHaveFocus())
    await rerender({ ...props, show: false })

    await waitFor(() => expect(trigger).toHaveFocus())
  })
})
