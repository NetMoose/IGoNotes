import { render, screen, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import GitPausedAlert from './GitPausedAlert.svelte'

const paused = {
  base: 'work', state: 'paused', ahead: 0, behind: 0, changed_paths: [],
  consecutive_failures: 3, last_attempt: '2026-09-30T12:34:56Z',
  error: { code: 'git_network', message: 'Сервер недоступен' },
}

describe('GitPausedAlert', () => {
  it('persistently presents the safe reason, failure count and localized attempt', () => {
    render(GitPausedAlert, { status: paused })
    const alert = screen.getByRole('alert')
    expect(within(alert).getByRole('heading', { name: 'Git-синхронизация приостановлена' })).toBeVisible()
    expect(alert).toHaveTextContent('Сервер недоступен')
    expect(alert).toHaveTextContent('Неудачных попыток подряд: 3')
    const time = alert.querySelector('time')
    expect(time).toHaveAttribute('datetime', paused.last_attempt)
    expect(time).toHaveTextContent(new Date(paused.last_attempt).toLocaleString())
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it.each(['', 'invalid'])('uses reason and unknown attempt fallbacks for %j', (last_attempt) => {
    render(GitPausedAlert, { status: { ...paused, error: undefined, last_attempt } })
    expect(screen.getByRole('alert')).toHaveTextContent('Автоматическая Git-синхронизация приостановлена после повторных ошибок.')
    expect(screen.getByRole('alert')).toHaveTextContent('Последняя попытка: неизвестно')
    expect(document.querySelector('time')).toBeNull()
  })

  it('forwards both actions and shows inline status errors while retaining the alert', async () => {
    const onResume = vi.fn()
    const onOpenSettings = vi.fn()
    const props = { status: paused, onResume, onOpenSettings, error: 'Повторите позже' }
    const { rerender } = render(GitPausedAlert, props)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Повторить и возобновить' }))
    await user.click(screen.getByRole('button', { name: 'Открыть настройки Git' }))
    expect(onResume).toHaveBeenCalledOnce()
    expect(onOpenSettings).toHaveBeenCalledOnce()
    expect(within(screen.getByRole('alert')).getByRole('status')).toHaveTextContent('Повторите позже')
    await rerender({ ...props, busy: true })
    for (const button of within(screen.getByRole('alert')).getAllByRole('button')) expect(button).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Повторить и возобновить' })).toHaveAttribute('aria-busy', 'true')
  })

  it.each([null, ...['ready', 'syncing', 'error', 'conflict', 'needs_reconnect', 'initializing', 'unconfigured'].map((state) => ({ ...paused, state }))])('is absent outside paused: %j', (status) => {
    render(GitPausedAlert, { status })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })
})
