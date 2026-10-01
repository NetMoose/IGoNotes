import { render, screen, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import GitPausedAlert from './GitPausedAlert.svelte'

const paused = {
  base: 'work', state: 'paused', ahead: 0, behind: 0, changed_paths: [],
  consecutive_failures: 5,
  repository_path: '/notes/work', operation_id: 'persisted-pause-1', stage: 'push',
  last_attempt: '2026-09-30T12:34:56Z', last_success: '2026-09-29T10:00:00Z',
  remote_oid: '0123456789abcdef0123456789abcdef0123456789',
  error: { code: 'git_network', message: 'Сервер недоступен' },
}

describe('GitPausedAlert', () => {
  it('persistently presents the safe reason, failure count and localized attempt', () => {
    render(GitPausedAlert, { status: paused })
    const alert = screen.getByRole('alert')
    expect(within(alert).getByRole('heading', { name: 'Git-синхронизация приостановлена' })).toBeVisible()
    expect(alert).toHaveTextContent('Сервер недоступен')
    expect(within(alert).getByText('Последовательных ошибок: 5.', { exact: true })).toBeVisible()
    const time = alert.querySelector('time')
    expect(time).toHaveAttribute('datetime', paused.last_attempt)
    expect(time).toHaveTextContent(new Date(paused.last_attempt).toLocaleString('ru-RU'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('names the alert by the heading with the specified id', () => {
    render(GitPausedAlert, { status: paused })
    const alert = screen.getByRole('alert', { name: 'Git-синхронизация приостановлена' })
    expect(alert).toHaveAttribute('aria-labelledby', 'git-paused-title')
    expect(within(alert).getByRole('heading', { name: 'Git-синхронизация приостановлена' })).toHaveAttribute('id', 'git-paused-title')
  })

  it('formats the last attempt explicitly in Russian regardless of the default locale', () => {
    const format = vi.spyOn(Date.prototype, 'toLocaleString')
    try {
      render(GitPausedAlert, { status: paused })
      expect(format).toHaveBeenCalledWith('ru-RU')
      expect(document.querySelector('time')).toHaveTextContent(new Date(paused.last_attempt).toLocaleString('ru-RU'))
    } finally {
      format.mockRestore()
    }
  })

  it.each(['', 'invalid'])('uses reason and unknown attempt fallbacks for %j', (last_attempt) => {
    render(GitPausedAlert, { status: { ...paused, error: undefined, last_attempt } })
    expect(screen.getByText('Автоматическая синхронизация остановлена до явного возобновления.', { exact: true })).toBeVisible()
    expect(screen.getByText('Время последней попытки неизвестно', { exact: true })).toBeVisible()
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
