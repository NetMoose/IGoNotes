import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  configureGit: vi.fn(),
  probeGit: vi.fn(),
}))

import { ApiError, configureGit, probeGit } from '../api.js'
import GitSetupWizard from './GitSetupWizard.svelte'

const base = {
  name: 'work',
  path: '/notes/work',
  git_url: 'https://example.test/notes.git',
  git_branch: 'main',
  auto_sync: true,
  auto_sync_interval_minutes: 15,
  git_commit_message_template: 'sync {{base}}/{{branch}} {{date}} {{datetime}} {{count}}',
}

function discovery(overrides = {}) {
  return {
    base: 'work',
    can_configure: false,
    remote_branches: ['main', 'release'],
    empty_remote: false,
    warnings: [],
    required_mutations: {},
    ...overrides,
  }
}

function selected(overrides = {}) {
  return {
    ...discovery(),
    can_configure: true,
    ...overrides,
  }
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function renderWizard(overrides = {}) {
  const props = {
    base,
    onConfigured: vi.fn(),
    onCancel: vi.fn(),
    onBusyChange: vi.fn(),
    ...overrides,
  }
  return { props, ...render(GitSetupWizard, props) }
}

async function completeDiscovery(user, result = discovery()) {
  vi.mocked(probeGit).mockResolvedValueOnce(result)
  await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))
  await screen.findByRole('heading', { name: 'Шаг 2 из 4: ветка' })
}

async function completeBranch(user, result = selected()) {
  vi.mocked(probeGit).mockResolvedValueOnce(result)
  await user.click(screen.getByRole('button', { name: 'Продолжить' }))
  await screen.findByRole('heading', { name: 'Шаг 3 из 4: расписание и коммиты' })
}

async function reachReview(user) {
  await completeDiscovery(user)
  await completeBranch(user)
  await user.click(screen.getByRole('button', { name: 'К подтверждению' }))
  await screen.findByRole('heading', { name: 'Шаг 4 из 4: подтверждение' })
}

describe('GitSetupWizard', () => {
  beforeEach(() => {
    vi.mocked(configureGit).mockReset()
    vi.mocked(probeGit).mockReset()
  })

  it('uses exact two-pass probes and reconciles an absent saved branch before selection', async () => {
    const user = userEvent.setup()
    renderWizard()

    await completeDiscovery(user, discovery({ remote_branches: ['release', 'trunk'] }))
    expect(probeGit).toHaveBeenNthCalledWith(1, {
      base: 'work',
      git_url: 'https://example.test/notes.git',
      git_branch: '',
    })
    expect(screen.getByLabelText('Ветка')).toHaveValue('')

    await user.selectOptions(screen.getByLabelText('Ветка'), 'release')
    await completeBranch(user)
    expect(probeGit).toHaveBeenNthCalledWith(2, {
      base: 'work',
      git_url: 'https://example.test/notes.git',
      git_branch: 'release',
    })
  })

  it('preserves a saved branch only when it is present and resets it after URL change or Back', async () => {
    const user = userEvent.setup()
    renderWizard()
    await completeDiscovery(user, discovery({ remote_branches: ['main'] }))
    expect(screen.getByLabelText('Ветка')).toHaveValue('main')
    await user.click(screen.getByRole('button', { name: 'Назад' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Шаг 1 из 4: репозиторий' })).toHaveFocus())
    await user.clear(screen.getByLabelText('URL репозитория'))
    await user.type(screen.getByLabelText('URL репозитория'), 'https://example.test/other.git')
    await completeDiscovery(user, discovery({ remote_branches: ['release', 'trunk'] }))
    expect(screen.getByLabelText('Ветка')).toHaveValue('')
  })

  it.each([
    ['wrong base', discovery({ base: 'other' })],
    ['configurable discovery', discovery({ can_configure: true })],
    ['empty remote with refs', discovery({ empty_remote: true, remote_branches: ['main'] })],
    ['nonempty remote without refs', discovery({ empty_remote: false, remote_branches: [] })],
  ])('keeps discovery on step one for %s', async (_case, result) => {
    const user = userEvent.setup()
    vi.mocked(probeGit).mockResolvedValueOnce(result)
    renderWizard()

    await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))

    expect(await screen.findByRole('alert')).toBeVisible()
    expect(screen.getByRole('heading', { name: 'Шаг 1 из 4: репозиторий' })).toBeVisible()
    await waitFor(() => expect(screen.getByRole('alert')).toHaveFocus())
  })

  it('focuses a blocking discovery error without a field', async () => {
    const user = userEvent.setup()
    vi.mocked(probeGit).mockResolvedValueOnce(discovery({ blocking_error: { message: 'Git недоступен' } }))
    renderWizard()

    await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Git недоступен')
    await waitFor(() => expect(screen.getByRole('alert')).toHaveFocus())
  })

  it('lets an empty remote use an editable branch and clears it when the URL changes', async () => {
    const user = userEvent.setup()
    renderWizard()
    await completeDiscovery(user, discovery({ empty_remote: true, remote_branches: [] }))
    const branch = screen.getByLabelText('Новая ветка')
    expect(branch).toHaveValue('main')
    await user.click(screen.getByRole('button', { name: 'Назад' }))
    await user.clear(screen.getByLabelText('URL репозитория'))
    await user.type(screen.getByLabelText('URL репозитория'), 'https://example.test/new.git')
    await completeDiscovery(user, discovery({ empty_remote: true, remote_branches: [] }))
    expect(screen.getByLabelText('Новая ветка')).toHaveValue('')
  })

  it('trims the branch entered for an empty remote before probing', async () => {
    const user = userEvent.setup()
    renderWizard()
    await completeDiscovery(user, discovery({ empty_remote: true, remote_branches: [] }))
    const branch = screen.getByLabelText('Новая ветка')
    await user.clear(branch)
    await user.type(branch, ' feature/editor ')
    await completeBranch(user, selected({ empty_remote: true, remote_branches: [] }))

    expect(probeGit).toHaveBeenLastCalledWith({
      base: 'work',
      git_url: 'https://example.test/notes.git',
      git_branch: 'feature/editor',
    })
  })

  it('preserves an empty-remote branch when its newly discovered URL is checked again', async () => {
    const user = userEvent.setup()
    renderWizard()
    const url = screen.getByLabelText('URL репозитория')
    await user.clear(url)
    await user.type(url, 'https://example.test/new.git')
    await completeDiscovery(user, discovery({ empty_remote: true, remote_branches: [] }))
    await user.clear(screen.getByLabelText('Новая ветка'))
    await user.type(screen.getByLabelText('Новая ветка'), 'topic')
    await user.click(screen.getByRole('button', { name: 'Назад' }))
    await completeDiscovery(user, discovery({ empty_remote: true, remote_branches: [] }))

    expect(screen.getByLabelText('Новая ветка')).toHaveValue('topic')
  })

  it('resets confirmations after a successful selected-branch probe', async () => {
    const user = userEvent.setup()
    const mutations = { create_repository: true, add_origin: false, replace_origin: false, create_branch: false, merge_histories: false }
    renderWizard()
    await completeDiscovery(user)
    await completeBranch(user, selected({ required_mutations: mutations }))
    await user.click(screen.getByRole('button', { name: 'К подтверждению' }))
    await user.click(screen.getByLabelText('Создать Git-репозиторий'))
    await user.click(screen.getByRole('button', { name: 'Назад' }))
    await user.click(screen.getByRole('button', { name: 'Назад' }))
    await completeBranch(user, selected({ required_mutations: mutations }))
    await user.click(screen.getByRole('button', { name: 'К подтверждению' }))

    expect(screen.getByLabelText('Создать Git-репозиторий')).not.toBeChecked()
  })

  it('resets confirmations after a successful repository discovery', async () => {
    const user = userEvent.setup()
    const mutations = { create_repository: true, add_origin: false, replace_origin: false, create_branch: false, merge_histories: false }
    renderWizard()
    await completeDiscovery(user)
    await completeBranch(user, selected({ required_mutations: mutations }))
    await user.click(screen.getByRole('button', { name: 'К подтверждению' }))
    await user.click(screen.getByLabelText('Создать Git-репозиторий'))
    await user.click(screen.getByRole('button', { name: 'Назад' }))
    await user.click(screen.getByRole('button', { name: 'Назад' }))
    await user.click(screen.getByRole('button', { name: 'Назад' }))
    await completeDiscovery(user)
    await completeBranch(user, selected({ required_mutations: mutations }))
    await user.click(screen.getByRole('button', { name: 'К подтверждению' }))

    expect(screen.getByLabelText('Создать Git-репозиторий')).not.toBeChecked()
  })

  it.each([
    ['a branch field error from discovery', new ApiError({ field: 'git_branch', message: 'Выберите ветку' }), 'Шаг 2 из 4: ветка', 'Ветка'],
    ['a URL field error from branch selection', new ApiError({ field: 'git_url', message: 'Проверьте URL' }), 'Шаг 1 из 4: репозиторий', 'URL репозитория'],
  ])('moves probe %s to its field', async (_case, apiError, heading, label) => {
    const user = userEvent.setup()
    renderWizard()
    if (apiError.field === 'git_url') await completeDiscovery(user)
    vi.mocked(probeGit).mockRejectedValueOnce(apiError)

    await user.click(screen.getByRole('button', { name: apiError.field === 'git_url' ? 'Продолжить' : 'Проверить репозиторий' }))

    expect(await screen.findByRole('heading', { name: heading })).toBeVisible()
    await waitFor(() => expect(screen.getByLabelText(label)).toHaveFocus())
  })

  it('focuses an alert for a fieldless probe error', async () => {
    const user = userEvent.setup()
    vi.mocked(probeGit).mockRejectedValueOnce(new ApiError({ message: 'Сеть недоступна' }))
    renderWizard()

    await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Сеть недоступна')
    await waitFor(() => expect(alert).toHaveFocus())
  })

  it.each([
    ['wrong base', selected({ base: 'other' })],
    ['not configurable', selected({ can_configure: false })],
    ['blocking error', selected({ blocking_error: { message: 'Ветка недоступна' } })],
  ])('keeps selected branch on step two for %s', async (_case, result) => {
    const user = userEvent.setup()
    renderWizard()
    await completeDiscovery(user)
    vi.mocked(probeGit).mockResolvedValueOnce(result)

    await user.click(screen.getByRole('button', { name: 'Продолжить' }))

    expect(await screen.findByRole('alert')).toBeVisible()
    await waitFor(() => expect(screen.getByRole('alert')).toHaveFocus())
  })

  it('renders preview and every supported template variable', async () => {
    const user = userEvent.setup()
    renderWizard()
    await completeDiscovery(user, discovery({ remote_branches: ['main'] }))
    await completeBranch(user)
    const template = screen.getByLabelText('Шаблон сообщения коммита')

    expect(screen.getByText('{{base}}, {{branch}}, {{date}}, {{datetime}}, {{count}}')).toBeVisible()
    expect(screen.getByLabelText('Предпросмотр сообщения')).toHaveTextContent('sync work/main')
    await fireEvent.input(template, { target: { value: '{{base}}/{{branch}} {{date}} {{datetime}} {{count}}' } })
    expect(screen.getByLabelText('Предпросмотр сообщения')).toHaveTextContent('work/main')
  })

  it('validates automatic scheduling and template before showing the confirmation step', async () => {
    const user = userEvent.setup()
    renderWizard()
    await completeDiscovery(user)
    await completeBranch(user)
    await user.click(screen.getByRole('radio', { name: 'Автоматически' }))
    await user.selectOptions(screen.getByLabelText('Интервал'), '')
    await user.click(screen.getByRole('button', { name: 'К подтверждению' }))

    expect(await screen.findByRole('alert')).toBeVisible()
    await waitFor(() => expect(screen.getByLabelText('Интервал')).toHaveFocus())
  })

  it('shows every mutation, warning, and gates configuration on required confirmations', async () => {
    const user = userEvent.setup()
    renderWizard()
    await completeDiscovery(user)
    await completeBranch(user, selected({
      required_mutations: {
        create_repository: true,
        add_origin: true,
        replace_origin: true,
        create_branch: true,
        merge_histories: true,
      },
      warnings: ['Локальные коммиты будут синхронизированы', 'Проверьте права доступа'],
    }))
    await user.click(screen.getByRole('button', { name: 'К подтверждению' }))

    expect(screen.getByRole('region', { name: 'Проверка Git-настроек' })).toHaveTextContent('Создать Git-репозиторий')
    expect(screen.getByText('Добавить origin')).toBeVisible()
    expect(screen.getByLabelText('Заменить origin')).toBeVisible()
    expect(screen.getByLabelText('Создать ветку')).toBeVisible()
    expect(screen.getByLabelText('Объединить несвязанные истории')).toBeVisible()
    expect(screen.getByText('Локальные коммиты будут синхронизированы')).toBeVisible()
    expect(screen.getByText('Проверьте права доступа')).toBeVisible()

    await user.click(screen.getByRole('button', { name: 'Настроить Git' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Подтвердите обязательные последствия')
    await waitFor(() => expect(screen.getByRole('alert')).toHaveFocus())
  })

  it('calls configure with the helper-built exact request and awaits onConfigured', async () => {
    const user = userEvent.setup()
    const response = { base: { ...base, git_branch: 'main' }, operation: { operation_id: 'op' } }
    const onConfigured = vi.fn().mockResolvedValue(undefined)
    vi.mocked(configureGit).mockResolvedValue(response)
    renderWizard({ onConfigured })
    await reachReview(user)

    await user.click(screen.getByRole('button', { name: 'Настроить Git' }))

    expect(configureGit).toHaveBeenCalledOnce()
    expect(configureGit).toHaveBeenCalledWith('work', {
      git_url: 'https://example.test/notes.git',
      git_branch: 'main',
      auto_sync: true,
      auto_sync_interval_minutes: 15,
      git_commit_message_template: 'sync {{base}}/{{branch}} {{date}} {{datetime}} {{count}}',
      confirmations: {
        create_repository: false,
        replace_origin: false,
        create_branch: false,
        merge_histories: false,
      },
    })
    await waitFor(() => expect(onConfigured).toHaveBeenCalledWith(response))
  })

  it.each([
    ['git_url', 'URL репозитория', 'Шаг 1 из 4: репозиторий'],
    ['git_branch', 'Ветка', 'Шаг 2 из 4: ветка'],
    ['auto_sync_interval_minutes', 'Интервал', 'Шаг 3 из 4: расписание и коммиты'],
    ['git_commit_message_template', 'Шаблон сообщения коммита', 'Шаг 3 из 4: расписание и коммиты'],
  ])('preserves the draft and maps %s API errors to its field and step', async (field, label, heading) => {
    const user = userEvent.setup()
    vi.mocked(configureGit).mockRejectedValue(new ApiError({ field, message: 'Сервер отклонил значение' }))
    renderWizard()
    await reachReview(user)

    await user.click(screen.getByRole('button', { name: 'Настроить Git' }))

    expect(await screen.findByRole('heading', { name: heading })).toBeVisible()
    expect(screen.getByLabelText(label)).toBeVisible()
    expect(screen.getByText('Сервер отклонил значение')).toBeVisible()
    await waitFor(() => expect(screen.getByLabelText(label)).toHaveFocus())
  })

  it('keeps every pending action single-flight and ignores its late settlement after unmount', async () => {
    const user = userEvent.setup()
    const request = deferred()
    const onConfigured = vi.fn()
    const onBusyChange = vi.fn()
    vi.mocked(configureGit).mockReturnValue(request.promise)
    const { unmount } = renderWizard({ onConfigured, onBusyChange })
    await reachReview(user)
    const submit = screen.getByRole('button', { name: 'Настроить Git' })

    await fireEvent.click(submit)
    await fireEvent.click(submit)
    expect(configureGit).toHaveBeenCalledOnce()
    expect(submit).toBeDisabled()
    expect(submit).toHaveAttribute('aria-busy', 'true')
    unmount()
    request.resolve({ base, operation: {} })
    await request.promise
    await Promise.resolve()

    expect(onConfigured).not.toHaveBeenCalled()
    expect(onBusyChange).toHaveBeenLastCalledWith(false)
  })
})
