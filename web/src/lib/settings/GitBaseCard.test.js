import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import GitBaseCard from './GitBaseCard.svelte'

const configuredBase = {
  name: 'Рабочие заметки',
  path: '/notes/work',
  git_url: 'https://example.test/notes.git',
  git_branch: 'main',
  auto_sync: true,
  auto_sync_interval_minutes: 15,
}

function renderCard(props = {}) {
  const callbacks = {
    onConfigure: vi.fn(),
    onSync: vi.fn(),
    onDisable: vi.fn(),
  }
  return {
    callbacks,
    ...render(GitBaseCard, { base: configuredBase, ...callbacks, ...props }),
  }
}

describe('GitBaseCard', () => {
  it.each([
    ['initializing', { state: 'initializing' }, 'Выполняется', true],
    ['syncing', { state: 'syncing' }, 'Выполняется', true],
    ['ready with changes', { state: 'ready', ahead: 1 }, 'Есть локальные изменения', false],
    ['ready', { state: 'ready', ahead: 0 }, 'Синхронизировано', false],
    ['error', { state: 'error' }, 'Ошибка', false],
    ['paused', { state: 'paused' }, 'Приостановлено', true],
    ['conflict', { state: 'conflict' }, 'Конфликт', true],
    ['needs reconnect', { state: 'needs_reconnect' }, 'Требуется переподключение', true],
    ['unknown', null, 'Статус неизвестен', true],
  ])('presents %s status and its sync availability', (_state, status, label, syncDisabled) => {
    renderCard({ status })

    expect(screen.getByText(label)).toBeVisible()
    const sync = screen.getByRole('button', { name: 'Синхронизировать сейчас' })
    if (syncDisabled) {
      expect(sync).toBeDisabled()
    } else {
      expect(sync).toBeEnabled()
    }
  })

  it('presents configured Git details and delegates every configured action with exact arguments', async () => {
    const user = userEvent.setup()
    const { callbacks } = renderCard({
      status: {
        state: 'ready',
        ahead: 2,
        stage: 'push',
        error: { message: 'Удаленный репозиторий недоступен' },
      },
    })

    const card = screen.getByRole('article', { name: 'Git для базы Рабочие заметки' })
    expect(card).toHaveAttribute('aria-busy', 'false')
    expect(card).toHaveClass('flex', 'flex-col')
    expect(card).toHaveTextContent('Рабочие заметки')
    expect(card).toHaveTextContent('/notes/work')
    expect(card).toHaveTextContent('Есть локальные изменения')
    expect(card).toHaveTextContent('Ветка: main')
    expect(card).toHaveTextContent('Репозиторий: https://example.test/notes.git')
    expect(card).toHaveTextContent('Каждые 15 минут')
    expect(card).toHaveTextContent('Этап: push')
    expect(card).toHaveTextContent('Удаленный репозиторий недоступен')
    expect(screen.getByTestId('git-card-actions')).toHaveClass('flex-wrap')

    await user.click(screen.getByRole('button', { name: 'Изменить настройки' }))
    await user.click(screen.getByRole('button', { name: 'Синхронизировать сейчас' }))
    const disable = screen.getByRole('button', { name: 'Отключить Git' })
    await user.click(disable)

    expect(callbacks.onConfigure).toHaveBeenCalledWith(configuredBase)
    expect(callbacks.onSync).toHaveBeenCalledWith('Рабочие заметки')
    expect(callbacks.onDisable).toHaveBeenCalledWith(configuredBase, disable)
  })

  it('uses the manual schedule and persistent supplied error', () => {
    renderCard({
      base: { ...configuredBase, auto_sync: false },
      status: { state: 'error' },
      error: 'Не удалось запустить синхронизацию',
    })

    expect(screen.getByText('Только вручную')).toBeVisible()
    expect(screen.getByRole('alert')).toHaveTextContent('Не удалось запустить синхронизацию')
  })

  it('explains unconfigured Git and exposes only configuration', () => {
    renderCard({
      base: { ...configuredBase, git_branch: '' },
      status: { state: 'ready' },
    })

    expect(screen.getByText('Git не настроен для этой базы заметок.')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Настроить Git' })).toBeEnabled()
    expect(screen.queryByRole('button', { name: 'Синхронизировать сейчас' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Отключить Git' })).not.toBeInTheDocument()
  })

  it('disables configured controls while busy and prevents a status that cannot sync', () => {
    const { rerender } = renderCard({ status: { state: 'ready', ahead: 0 }, busy: true })

    expect(screen.getByRole('article')).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByRole('button', { name: 'Изменить настройки' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Синхронизировать сейчас' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Отключить Git' })).toBeDisabled()

    rerender({ base: configuredBase, status: { state: 'conflict' }, busy: false })
    expect(screen.getByRole('button', { name: 'Синхронизировать сейчас' })).toBeDisabled()
  })

  it('keeps actions usable without horizontal overflow on a narrow viewport', () => {
    renderCard()

    const actions = screen.getByTestId('git-card-actions')
    expect(actions).toHaveClass('flex', 'flex-wrap')
  })
})
