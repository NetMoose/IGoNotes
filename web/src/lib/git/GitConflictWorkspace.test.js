import { render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  abortGitConflict,
  completeGitConflict,
  getGitConflicts,
  resolveGitConflict,
} from '../api.js'
import GitConflictWorkspace from './GitConflictWorkspace.svelte'

vi.mock('../api.js', () => ({
  getGitConflicts: vi.fn(),
  resolveGitConflict: vi.fn(),
  completeGitConflict: vi.fn(),
  abortGitConflict: vi.fn(),
}))

const base = { name: 'work', path: '/notes/work' }
const otherBase = { name: 'personal', path: '/notes/personal' }
const status = { state: 'conflict', operation_id: 'operation-1' }

const textConflict = {
  id: 'text-conflict',
  kind: 'content',
  content_kind: 'text',
  path: 'notes/idea.md',
  actions: ['local', 'manual'],
  base: { path: 'notes/idea.md', oid: 'base', mode: '100644', size: 4, content: 'base', preview_truncated: false },
  local: { path: 'notes/idea.md', oid: 'local', mode: '100644', size: 5, content: 'local', preview_truncated: false },
  remote: { path: 'notes/idea.md', oid: 'remote', mode: '100644', size: 6, content: 'remote', preview_truncated: false },
}

const binaryConflict = {
  ...textConflict,
  id: 'binary-conflict',
  kind: 'add_add',
  content_kind: 'binary',
  path: 'assets/photo.png',
  actions: ['local', 'remote'],
}

const deleteConflict = {
  ...textConflict,
  id: 'delete-conflict',
  kind: 'modify_delete',
  path: 'notes/deleted.md',
  actions: ['local', 'delete'],
  remote: null,
}

function conflictList(conflicts = [textConflict], overrides = {}) {
  return {
    base: 'work',
    operation_id: 'operation-1',
    head_oid: 'head',
    merge_head_oid: 'merge-head',
    can_complete: false,
    conflicts,
    ...overrides,
  }
}

function workspaceProps(overrides = {}) {
  return {
    base,
    status,
    bases: [base, otherBase],
    onSwitchBase: vi.fn(),
    onOperationAccepted: vi.fn(),
    ...overrides,
  }
}

describe('GitConflictWorkspace', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('loads the active base and routes text, binary, and delete conflicts to their resolvers', async () => {
    const user = userEvent.setup()
    getGitConflicts.mockResolvedValue(conflictList([textConflict, binaryConflict, deleteConflict]))
    render(GitConflictWorkspace, workspaceProps())

    expect(await screen.findByRole('textbox', { name: 'На этом устройстве' })).toHaveValue('local')
    expect(getGitConflicts).toHaveBeenCalledWith('work')
    await user.click(screen.getByRole('button', { name: 'assets/photo.png' }))
    expect(screen.getAllByText('Двоичный файл')).toHaveLength(3)
    await user.click(screen.getByRole('button', { name: 'notes/deleted.md' }))
    expect(screen.getByText('Версия на этом устройстве будет сохранена, а версия из репозитория была удалена.')).toBeVisible()
  })

  it('labels the conflict navigation and moves selected focus with keyboard keys', async () => {
    const user = userEvent.setup()
    getGitConflicts.mockResolvedValue(conflictList([textConflict, binaryConflict, deleteConflict]))
    render(GitConflictWorkspace, workspaceProps())

    const navigation = await screen.findByRole('navigation', { name: 'Конфликтующие файлы' })
    const first = screen.getByRole('button', { name: 'notes/idea.md' })
    const second = screen.getByRole('button', { name: 'assets/photo.png' })
    const last = screen.getByRole('button', { name: 'notes/deleted.md' })
    expect(navigation).toContainElement(first)
    expect(first).toHaveAttribute('aria-current', 'true')

    first.focus()
    await user.keyboard('{ArrowDown}')
    expect(second).toHaveFocus()
    expect(second).toHaveAttribute('aria-current', 'true')
    await user.keyboard('{End}')
    expect(last).toHaveFocus()
    await user.keyboard('{Home}')
    expect(first).toHaveFocus()
    await user.keyboard('{ArrowUp}')
    expect(first).toHaveFocus()
  })

  it('consumes the returned remaining list and focuses Complete after the final resolution', async () => {
    const user = userEvent.setup()
    getGitConflicts.mockResolvedValueOnce(conflictList([binaryConflict]))
    resolveGitConflict.mockResolvedValue({
      resolved_path: 'assets/photo.png',
      remaining: conflictList([], { can_complete: true }),
    })
    render(GitConflictWorkspace, workspaceProps())

    await screen.findByRole('button', { name: 'assets/photo.png' })
    await user.click(screen.getByRole('button', { name: 'assets/photo.png' }))
    await user.click(screen.getByRole('radio', { name: 'Оставить версию на этом устройстве' }))
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))

    await waitFor(() => expect(resolveGitConflict).toHaveBeenCalledWith({
      base: 'work',
      operation_id: 'operation-1',
      conflict_id: 'binary-conflict',
      path: 'assets/photo.png',
      action: 'local',
      result_path: 'assets/photo.png',
      local_oid: 'local',
    }))
    expect(getGitConflicts).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('button', { name: 'Завершить слияние' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Завершить слияние' })).toHaveFocus()
  })

  it('reloads conflicts only after a stale resolution error', async () => {
    const user = userEvent.setup()
    const staleError = Object.assign(new Error('Конфликт уже изменился'), { code: 'git_conflict_stale' })
    getGitConflicts
      .mockResolvedValueOnce(conflictList([binaryConflict]))
      .mockResolvedValueOnce(conflictList([textConflict]))
    resolveGitConflict.mockRejectedValue(staleError)
    render(GitConflictWorkspace, workspaceProps())

    await screen.findByRole('button', { name: 'assets/photo.png' })
    await user.click(screen.getByRole('radio', { name: 'Оставить версию на этом устройстве' }))
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))

    await waitFor(() => expect(getGitConflicts).toHaveBeenCalledTimes(2))
    expect(screen.getByRole('button', { name: 'notes/idea.md' })).toHaveAttribute('aria-current', 'true')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('keeps the selected resolver draft after a failed resolution request', async () => {
    const user = userEvent.setup()
    getGitConflicts.mockResolvedValue(conflictList())
    resolveGitConflict.mockRejectedValue(new Error('Не удалось сохранить решение'))
    render(GitConflictWorkspace, workspaceProps())

    await screen.findByRole('radio', { name: 'Объединить вручную' })
    await user.click(screen.getByRole('radio', { name: 'Объединить вручную' }))
    const resultPath = screen.getByRole('textbox', { name: 'Путь результата' })
    await user.clear(resultPath)
    await user.type(resultPath, 'notes/merged.md')
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Не удалось сохранить решение')
    expect(screen.getByRole('radio', { name: 'Объединить вручную' })).toBeChecked()
    expect(resultPath).toHaveValue('notes/merged.md')
  })

  it('enables completion only when allowed and retains the workspace after an accepted completion', async () => {
    const props = workspaceProps()
    getGitConflicts
      .mockResolvedValueOnce(conflictList([textConflict], { can_complete: true }))
      .mockResolvedValueOnce(conflictList([], { can_complete: true, operation_id: 'operation-2' }))
    const workspace = render(GitConflictWorkspace, props)

    const complete = await screen.findByRole('button', { name: 'Завершить слияние' })
    expect(complete).toBeDisabled()

    await workspace.rerender({ ...props, busy: true })
    expect(complete).toBeDisabled()
    await workspace.rerender({ ...props, busy: false })
    await workspace.rerender({ ...props, status: { ...status, operation_id: 'operation-2' } })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Завершить слияние' })).toBeEnabled())
  })

  it('keeps the workspace pending after accepted complete and abort operations until App receives terminal status', async () => {
    const user = userEvent.setup()
    const props = workspaceProps()
    getGitConflicts.mockResolvedValue(conflictList([], { can_complete: true }))
    const completeOperation = { operation_id: 'complete-1', status: 'queued', deduplicated: false }
    completeGitConflict.mockResolvedValue(completeOperation)
    const completedWorkspace = render(GitConflictWorkspace, props)

    const complete = await screen.findByRole('button', { name: 'Завершить слияние' })
    await waitFor(() => expect(complete).toBeEnabled())
    await user.click(complete)
    expect(completeGitConflict).toHaveBeenCalledWith('work')
    expect(props.onOperationAccepted).toHaveBeenCalledWith('complete', completeOperation)
    expect(screen.getByRole('button', { name: 'Отменить слияние' })).toBeDisabled()
    expect(screen.getByText('Ожидание завершения операции Git')).toBeVisible()
    await completedWorkspace.rerender({ ...props, status: { state: 'needs_reconnect', operation_id: 'complete-1' } })
    expect(props.onOperationAccepted).toHaveBeenCalledTimes(1)
    await completedWorkspace.rerender({ ...props, status: { state: 'ready', operation_id: 'another-operation' } })
    expect(props.onOperationAccepted).toHaveBeenCalledTimes(1)
    await completedWorkspace.rerender({ ...props, status: { state: 'ready', operation_id: 'complete-1' } })
    expect(screen.getByText('Ожидание завершения операции Git')).toBeVisible()

    completedWorkspace.unmount()

    getGitConflicts.mockResolvedValue(conflictList())
    const abortOperation = { operation_id: 'abort-1', status: 'queued', deduplicated: false }
    abortGitConflict.mockResolvedValue(abortOperation)
    const abortProps = workspaceProps()
    const abortWorkspace = render(GitConflictWorkspace, abortProps)
    await user.click(await screen.findByRole('button', { name: 'Отменить слияние' }))
    await user.click(screen.getByRole('button', { name: 'Подтвердить отмену' }))
    expect(abortGitConflict).toHaveBeenCalledWith('work')
    expect(abortProps.onOperationAccepted).toHaveBeenCalledWith('abort', abortOperation)
    expect(abortWorkspace.container).toHaveTextContent('Ожидание завершения операции Git')
    await abortWorkspace.rerender({ ...abortProps, status: { state: 'ready', operation_id: 'abort-1' } })
    expect(abortWorkspace.container).toHaveTextContent('Ожидание завершения операции Git')
  })

  it('retains an explicit base-switch target and error after parent rejection without invoking a conflict mutation', async () => {
    const user = userEvent.setup()
    const props = workspaceProps({ onSwitchBase: vi.fn().mockRejectedValue(new Error('База недоступна')) })
    getGitConflicts.mockResolvedValue(conflictList())
    render(GitConflictWorkspace, props)

    const target = await screen.findByRole('combobox', { name: 'База для переключения' })
    expect(screen.queryByRole('option', { name: 'work' })).not.toBeInTheDocument()
    await user.selectOptions(target, 'personal')
    expect(props.onSwitchBase).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Открыть базу' }))
    expect(props.onSwitchBase).toHaveBeenCalledWith('personal')
    expect(await screen.findByRole('alert')).toHaveTextContent('База недоступна')
    expect(target).toHaveValue('personal')
    expect(resolveGitConflict).not.toHaveBeenCalled()
    expect(completeGitConflict).not.toHaveBeenCalled()
    expect(abortGitConflict).not.toHaveBeenCalled()
  })

  it('retries initial load errors and disables competing controls while externally busy', async () => {
    const user = userEvent.setup()
    getGitConflicts
      .mockRejectedValueOnce(new Error('Не удалось загрузить конфликты'))
      .mockResolvedValueOnce(conflictList())
    const props = workspaceProps({ busy: true })
    const workspace = render(GitConflictWorkspace, props)
    const { container } = workspace

    expect(await screen.findByRole('alert')).toHaveTextContent('Не удалось загрузить конфликты')
    expect(screen.getByRole('combobox', { name: 'База для переключения' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Завершить слияние' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Отменить слияние' })).toBeDisabled()
    expect(container.querySelector('main')).toHaveClass('lg:grid-cols-[18rem_minmax(0,1fr)]')

    await workspace.rerender({ ...props, busy: false })
    await user.click(screen.getByRole('button', { name: 'Повторить загрузку' }))
    await waitFor(() => expect(getGitConflicts).toHaveBeenCalledTimes(2))
    expect(screen.getByRole('radio', { name: 'Оставить версию на этом устройстве' })).toBeVisible()
  })
})
