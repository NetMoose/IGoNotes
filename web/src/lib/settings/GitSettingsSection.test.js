import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  configureGit: vi.fn(),
  disableGit: vi.fn(),
  probeGit: vi.fn(),
}))

import { configureGit, disableGit, probeGit } from '../api.js'
import GitSettingsSection from './GitSettingsSection.svelte'

const config = {
  current_base: 'work',
  setup_completed: true,
  bases: [
    { name: 'work', path: '/notes/work', git_url: 'origin', git_branch: 'main', auto_sync: false },
    { name: 'plain', path: '/notes/plain', auto_sync: false },
  ],
}

const statuses = [
  { base: 'work', state: 'ready', ahead: 1, behind: 0, changed_paths: [] },
  { base: 'plain', state: 'unconfigured', ahead: 0, behind: 0, changed_paths: [] },
]

const discovery = {
  base: 'plain',
  can_configure: false,
  empty_remote: false,
  remote_branches: ['main'],
  warnings: [],
  required_mutations: {
    create_repository: false,
    add_origin: false,
    replace_origin: false,
    create_branch: false,
    merge_histories: false,
  },
}

const selected = { ...discovery, can_configure: true }

function renderSection(overrides = {}) {
  const props = {
    config,
    statuses,
    pollError: '',
    busyBase: '',
    actionErrors: {},
    onConfigChange: vi.fn(),
    onSync: vi.fn(),
    onRefresh: vi.fn(),
    onBusyChange: vi.fn(),
    ...overrides,
  }
  return { props, ...render(GitSettingsSection, props) }
}

describe('GitSettingsSection', () => {
  beforeEach(() => {
    vi.mocked(probeGit).mockReset().mockImplementation(({ git_branch }) => (
      Promise.resolve(git_branch ? selected : discovery)
    ))
    vi.mocked(configureGit).mockReset()
    vi.mocked(disableGit).mockReset()
  })

  it('matches unordered statuses by exact name and delegates sync unchanged', async () => {
    const user = userEvent.setup()
    const { props } = renderSection({ statuses: [...statuses].reverse() })
    const work = screen.getByRole('article', { name: 'Git для базы work' })
    const plain = screen.getByRole('article', { name: 'Git для базы plain' })

    expect(work).toHaveTextContent('Есть локальные изменения')
    expect(plain).toHaveTextContent('Git не настроен')
    await user.click(within(work).getByRole('button', { name: 'Синхронизировать сейчас' }))
    expect(props.onSync).toHaveBeenCalledOnce()
    expect(props.onSync).toHaveBeenCalledWith('work')
  })

  it('publishes only the configured response base, refreshes, and returns focus to the list', async () => {
    const user = userEvent.setup()
    const savedBase = {
      ...config.bases[1],
      git_url: 'origin',
      git_branch: 'main',
      auto_sync_interval_minutes: 15,
      git_commit_message_template: 'sync {{base}}',
    }
    vi.mocked(configureGit).mockResolvedValue({
      base: savedBase,
      status: { base: 'plain', state: 'initializing', ahead: 0, behind: 0, changed_paths: [] },
      operation: { operation_id: 'op-2', status: 'queued', deduplicated: false },
    })
    const { props } = renderSection()

    await user.click(within(screen.getByRole('article', { name: 'Git для базы plain' }))
      .getByRole('button', { name: 'Настроить Git' }))
    await user.type(screen.getByLabelText('URL репозитория'), 'origin')
    await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))
    await user.selectOptions(screen.getByLabelText('Ветка'), 'main')
    await user.click(screen.getByRole('button', { name: 'Продолжить' }))
    await user.click(screen.getByRole('button', { name: 'Проверить настройки' }))
    await user.click(screen.getByRole('button', { name: 'Сохранить Git-настройки' }))

    await waitFor(() => expect(props.onConfigChange).toHaveBeenCalledWith({
      ...config,
      bases: [config.bases[0], savedBase],
    }))
    expect(config.bases[1].git_url).toBeUndefined()
    expect(props.onRefresh).toHaveBeenCalledOnce()
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Git-синхронизация' })).toHaveFocus())
  })

  it('confirms disable once, retains files, publishes the response base, and refreshes once', async () => {
    const user = userEvent.setup()
    const disabledBase = { name: 'work', path: '/notes/work', auto_sync: false }
    vi.mocked(disableGit).mockResolvedValue({
      base: disabledBase,
      status: { base: 'work', state: 'unconfigured', ahead: 0, behind: 0, changed_paths: [] },
    })
    const { props } = renderSection()
    const trigger = within(screen.getByRole('article', { name: 'Git для базы work' }))
      .getByRole('button', { name: 'Отключить Git' })

    await user.click(trigger)
    const dialog = screen.getByRole('dialog', { name: 'Отключить Git для work?' })
    expect(dialog).toHaveTextContent('Репозиторий и файлы на диске останутся без изменений')
    await user.click(within(dialog).getByRole('button', { name: 'Отключить Git' }))

    expect(disableGit).toHaveBeenCalledTimes(1)
    expect(disableGit).toHaveBeenCalledWith('work')
    await waitFor(() => expect(props.onConfigChange).toHaveBeenCalledWith({
      ...config,
      bases: [disabledBase, config.bases[1]],
    }))
    expect(props.onRefresh).toHaveBeenCalledOnce()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('keeps polling and parent action errors visible and keeps disable confirmation single-flight', async () => {
    const request = Promise.withResolvers()
    vi.mocked(disableGit).mockReturnValue(request.promise)
    const user = userEvent.setup()
    renderSection({
      pollError: 'Статус временно недоступен',
      actionErrors: { work: 'Не удалось синхронизировать' },
    })

    expect(screen.getByRole('alert', { name: 'Ошибка опроса Git' })).toHaveTextContent('Статус временно недоступен')
    expect(screen.getByText('Не удалось синхронизировать')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Отключить Git' }))
    const confirm = within(screen.getByRole('dialog')).getByRole('button', { name: 'Отключить Git' })
    await fireEvent.click(confirm)
    await fireEvent.click(confirm)

    expect(disableGit).toHaveBeenCalledOnce()
    expect(confirm).toHaveAttribute('aria-busy', 'true')
    request.resolve({
      base: { name: 'work', path: '/notes/work', auto_sync: false },
      status: { base: 'work', state: 'unconfigured', ahead: 0, behind: 0, changed_paths: [] },
    })
    await request.promise
  })
})
