import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import GitStatusIndicator from './GitStatusIndicator.svelte'

const base = { name: 'Рабочие заметки', git_url: 'https://example.test/notes.git', git_branch: 'main' }

function renderIndicator(props = {}) {
  return render(GitStatusIndicator, { base, ...props })
}

describe('GitStatusIndicator', () => {
  it.each([
    ['ready ahead', { state: 'ready', ahead: 2 }, 'Есть локальные изменения', 'amber', false],
    ['syncing', { state: 'syncing' }, 'Выполняется', 'blue', true],
    ['error', { state: 'error' }, 'Ошибка', 'red', false],
    ['paused', { state: 'paused' }, 'Приостановлено', 'amber', true],
    ['conflict', { state: 'conflict' }, 'Конфликт', 'red', true],
    ['needs reconnect', { state: 'needs_reconnect' }, 'Требуется переподключение', 'amber', true],
  ])('presents %s status with its tone and safe sync state', async (_state, status, label, tone, syncDisabled) => {
    const user = userEvent.setup()
    renderIndicator({ status })

    const details = screen.getByRole('button', { name: `Открыть детали Git: ${label}` })
    expect(details).toHaveAttribute('aria-expanded', 'false')
    expect(details).toHaveClass(`bg-${tone}-100`, `text-${tone}-800`)
    await user.click(details)
    const sync = screen.getByRole('button', { name: 'Синхронизировать Git' })
    if (syncDisabled) {
      expect(sync).toBeDisabled()
    } else {
      expect(sync).toBeEnabled()
    }
  })

  it('opens prop-provided details and syncs the exact base name', async () => {
    const user = userEvent.setup()
    const onSync = vi.fn()
    renderIndicator({
      status: {
        state: 'error',
        ahead: 3,
        behind: 2,
        stage: 'push',
        last_success: '2026-09-10T12:00:00Z',
        error: { message: 'Удаленный репозиторий недоступен' },
      },
      error: 'Не удалось запустить синхронизацию',
      onSync,
    })

    await user.click(screen.getByRole('button', { name: 'Открыть детали Git: Ошибка' }))

    const panel = screen.getByRole('region', { name: 'Детали Git' })
    expect(panel).toHaveClass('w-[min(22rem,calc(100vw-1.5rem))]')
    expect(panel).toHaveTextContent('Рабочие заметки')
    expect(panel).toHaveTextContent('main')
    expect(panel).toHaveTextContent('Впереди: 3')
    expect(panel).toHaveTextContent('Позади: 2')
    expect(panel).toHaveTextContent('Этап: push')
    expect(panel).toHaveTextContent('Последняя успешная синхронизация: 2026-09-10T12:00:00Z')
    expect(panel).toHaveTextContent('Удаленный репозиторий недоступен')
    expect(screen.getByRole('alert')).toHaveTextContent('Не удалось запустить синхронизацию')

    await user.click(screen.getByRole('button', { name: 'Синхронизировать Git' }))
    expect(onSync).toHaveBeenCalledOnce()
    expect(onSync).toHaveBeenCalledWith('Рабочие заметки')

    await user.click(screen.getByRole('button', { name: 'Закрыть детали Git' }))
    expect(screen.queryByRole('region', { name: 'Детали Git' })).not.toBeInTheDocument()
  })

  it('uses safe fallbacks and surfaces supplied busy and action errors', async () => {
    const user = userEvent.setup()
    renderIndicator({
      base: { ...base, git_branch: '' },
      status: { state: 'ready' },
      busy: true,
      error: 'Синхронизация уже выполняется',
    })

    await user.click(screen.getByRole('button', { name: 'Открыть детали Git: Git не настроен' }))
    const sync = screen.getByRole('button', { name: 'Синхронизировать Git' })
    expect(sync).toBeDisabled()
    expect(sync).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByRole('alert')).toHaveTextContent('Синхронизация уже выполняется')
  })

  it('shows zero counts and a missing branch fallback in details', async () => {
    const user = userEvent.setup()
    renderIndicator({ base: { ...base, git_branch: '' }, status: { state: 'ready' } })

    await user.click(screen.getByRole('button', { name: 'Открыть детали Git: Git не настроен' }))

    const panel = screen.getByRole('region', { name: 'Детали Git' })
    expect(panel).toHaveTextContent('Ветка не выбрана')
    expect(panel).toHaveTextContent('Впереди: 0')
    expect(panel).toHaveTextContent('Позади: 0')
  })
})
