import { render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import DeleteConflictResolver from './DeleteConflictResolver.svelte'

const baseStage = {
  path: 'notes/draft.md', oid: 'base-oid', mode: '100644', size: 5, content: 'base', preview_truncated: false,
}
const localStage = {
  path: 'notes/draft.md', oid: 'local-oid', mode: '100644', size: 6, content: 'local edit', preview_truncated: false,
}
const remoteStage = {
  path: 'notes/draft.md', oid: 'remote-oid', mode: '100644', size: 7, content: 'remote edit', preview_truncated: false,
}
const localRetained = {
  id: 'sha256:local-retained',
  kind: 'modify_delete',
  content_kind: 'text',
  path: 'notes/draft.md',
  actions: ['local', 'manual', 'delete'],
  base: baseStage,
  local: localStage,
  remote: null,
}
const remoteRetained = {
  ...localRetained,
  id: 'sha256:remote-retained',
  actions: ['remote', 'manual', 'delete'],
  local: null,
  remote: remoteStage,
}
const renameDelete = {
  ...localRetained,
  id: 'sha256:rename-delete',
  kind: 'rename_delete',
  path: 'notes/renamed.md',
  original_path: 'notes/draft.md',
  base: { ...baseStage, path: 'notes/renamed.md' },
  local: { ...localStage, path: 'notes/renamed.md' },
}

function resolverProps(overrides = {}) {
  return {
    base: 'work',
    operationId: 'operation-1',
    conflict: localRetained,
    onResolve: vi.fn(),
    ...overrides,
  }
}

describe('DeleteConflictResolver', () => {
  it('presents local retention with clear deletion orientation in responsive stage panels', () => {
    const { container } = render(DeleteConflictResolver, resolverProps())

    expect(screen.getByRole('region', { name: 'Общий предок' })).toBeVisible()
    expect(screen.getByRole('textbox', { name: 'На этом устройстве' })).toHaveValue('local edit')
    expect(screen.getByRole('region', { name: 'В репозитории' })).toHaveTextContent('Файл отсутствует на этой стороне')
    expect(screen.getByText('Версия на этом устройстве будет сохранена, а версия из репозитория была удалена.')).toBeVisible()
    expect(container.querySelector('[data-testid="conflict-stages"]')).toHaveClass('grid', 'grid-cols-1', 'lg:grid-cols-3')
  })

  it('presents remote retention with clear deletion orientation', () => {
    render(DeleteConflictResolver, resolverProps({ conflict: remoteRetained }))

    expect(screen.getByText('Версия из репозитория будет сохранена, а версия на этом устройстве была удалена.')).toBeVisible()
    expect(screen.getByRole('region', { name: 'На этом устройстве' })).toHaveTextContent('Файл отсутствует на этой стороне')
    expect(screen.getByRole('textbox', { name: 'В репозитории' })).toHaveValue('remote edit')
  })

  it('renders only API-provided native radio actions without preselection', () => {
    render(DeleteConflictResolver, resolverProps())

    const options = screen.getAllByRole('radio')
    expect(screen.getByRole('group', { name: 'Способ разрешения' })).toContainElement(options[0])
    expect(options).toEqual(expect.arrayContaining([
      screen.getByRole('radio', { name: 'Сохранить версию на этом устройстве' }),
      screen.getByRole('radio', { name: 'Объединить вручную' }),
      screen.getByRole('radio', { name: 'Удалить итоговый файл' }),
    ]))
    expect(options).toHaveLength(3)
    expect(options.every((option) => !option.checked)).toBe(true)
    expect(screen.queryByRole('radio', { name: 'Сохранить версию из репозитория' })).not.toBeInTheDocument()
    expect(screen.queryByRole('radio', { name: 'Сохранить обе версии' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { pressed: true })).not.toBeInTheDocument()
  })

  it.each([
    [localRetained, 'Сохранить версию на этом устройстве', { local_oid: 'local-oid' }],
    [remoteRetained, 'Сохранить версию из репозитория', { remote_oid: 'remote-oid' }],
  ])('submits the exact retained-side payload for %s', async (conflict, label, source) => {
    const user = userEvent.setup()
    const props = resolverProps({ conflict })
    render(DeleteConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: label }))
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))

    expect(props.onResolve).toHaveBeenCalledWith({
      base: 'work',
      operation_id: 'operation-1',
      conflict_id: conflict.id,
      path: conflict.path,
      action: conflict.actions[0],
      result_path: conflict.path,
      ...source,
    })
  })

  it('shows original and renamed paths and submits an alternate rename result path', async () => {
    const user = userEvent.setup()
    const props = resolverProps({ conflict: renameDelete })
    render(DeleteConflictResolver, props)

    expect(screen.getByText('Исходный путь')).toBeVisible()
    expect(screen.getByText('notes/draft.md')).toBeVisible()
    expect(screen.getByText('Переименованный путь').parentElement).toHaveTextContent('notes/renamed.md')
    await user.click(screen.getByRole('radio', { name: 'Сохранить версию на этом устройстве' }))
    const resultPath = screen.getByRole('textbox', { name: 'Путь результата' })
    expect(resultPath).toHaveValue('notes/renamed.md')
    await user.clear(resultPath)
    await user.type(resultPath, 'notes/final.md')
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))

    expect(props.onResolve).toHaveBeenCalledWith({
      base: 'work',
      operation_id: 'operation-1',
      conflict_id: 'sha256:rename-delete',
      path: 'notes/renamed.md',
      action: 'local',
      result_path: 'notes/final.md',
      local_oid: 'local-oid',
    })
  })

  it('requires a result path and final text for an allowed manual resolution', async () => {
    const user = userEvent.setup()
    const props = resolverProps()
    render(DeleteConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: 'Объединить вручную' }))
    const resultPath = screen.getByRole('textbox', { name: 'Путь результата' })
    const content = screen.getByRole('textbox', { name: 'Итоговый текст' })
    await user.clear(resultPath)
    await user.clear(content)
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Необходимо поле result_path')
    expect(props.onResolve).not.toHaveBeenCalled()

    await user.type(resultPath, 'notes/manual.md')
    await user.type(content, 'итог')
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))
    expect(props.onResolve).toHaveBeenCalledWith({
      base: 'work',
      operation_id: 'operation-1',
      conflict_id: 'sha256:local-retained',
      path: 'notes/draft.md',
      action: 'manual',
      result_path: 'notes/manual.md',
      content: 'итог',
    })
  })

  it('omits manual for a binary conflict and renders keep-both only when provided by the API', () => {
    render(DeleteConflictResolver, resolverProps({
      conflict: {
        ...localRetained,
        content_kind: 'binary',
        actions: ['local', 'keep_both', 'delete'],
        local: { ...localStage, content: undefined },
      },
    }))

    expect(screen.queryByRole('radio', { name: 'Объединить вручную' })).not.toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Сохранить обе версии' })).toBeVisible()
  })

  it('requires a second confirmation before submitting deletion', async () => {
    const user = userEvent.setup()
    const props = resolverProps()
    render(DeleteConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: 'Удалить итоговый файл' }))
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))
    expect(props.onResolve).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Подтвердить удаление' }))

    expect(props.onResolve).toHaveBeenCalledWith({
      base: 'work',
      operation_id: 'operation-1',
      conflict_id: 'sha256:local-retained',
      path: 'notes/draft.md',
      action: 'delete',
    })
  })

  it('retains the manual draft, action, focus, and error after an API rejection', async () => {
    const user = userEvent.setup()
    const props = resolverProps({ onResolve: vi.fn().mockRejectedValue(new Error('Не удалось сохранить')) })
    render(DeleteConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: 'Объединить вручную' }))
    const resultPath = screen.getByRole('textbox', { name: 'Путь результата' })
    const content = screen.getByRole('textbox', { name: 'Итоговый текст' })
    await user.clear(resultPath)
    await user.clear(content)
    await user.type(resultPath, 'notes/retry.md')
    await user.type(content, 'черновик')
    const submit = screen.getByRole('button', { name: 'Применить решение' })
    await user.click(submit)

    expect(await screen.findByRole('alert')).toHaveTextContent('Не удалось сохранить')
    expect(screen.getByRole('radio', { name: 'Объединить вручную' })).toBeChecked()
    expect(resultPath).toHaveValue('notes/retry.md')
    expect(content).toHaveValue('черновик')
    expect(submit).toHaveFocus()
  })

  it('disables every control while busy or submitting', async () => {
    const user = userEvent.setup()
    let finish
    const props = resolverProps({ onResolve: vi.fn(() => new Promise((resolve) => { finish = resolve })) })
    const resolver = render(DeleteConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: 'Сохранить версию на этом устройстве' }))
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))
    screen.getAllByRole('radio').forEach((option) => expect(option).toBeDisabled())
    expect(screen.getByRole('textbox', { name: 'Путь результата' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Применить решение' })).toBeDisabled()
    finish()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Применить решение' })).not.toBeDisabled())

    await resolver.rerender({ ...props, busy: true })
    screen.getAllByRole('radio').forEach((option) => expect(option).toBeDisabled())
    expect(screen.getByRole('textbox', { name: 'Путь результата' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Применить решение' })).toBeDisabled()
  })
})
