import { render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import TextConflictResolver from './TextConflictResolver.svelte'

const conflict = {
  id: 'sha256:conflict',
  path: 'notes/idea.md',
  content_kind: 'text',
  actions: ['local', 'remote', 'manual', 'keep_both'],
  base: { path: 'notes/idea.md', oid: 'base-oid', mode: '100644', size: 5, content: 'base', preview_truncated: false },
  local: { path: 'notes/idea.md', oid: 'local-oid', mode: '100644', size: 6, content: 'local', preview_truncated: false },
  remote: { path: 'notes/idea.md', oid: 'remote-oid', mode: '100644', size: 7, content: 'remote', preview_truncated: false },
}

function resolverProps(overrides = {}) {
  return {
    base: 'work',
    operationId: 'operation-1',
    conflict,
    onResolve: vi.fn(),
    ...overrides,
  }
}

describe('TextConflictResolver', () => {
  it('renders the three conflict stages in a responsive grid', () => {
    const { container } = render(TextConflictResolver, resolverProps())

    expect(screen.getByRole('region', { name: 'Общий предок' })).toBeVisible()
    expect(screen.getByRole('region', { name: 'На этом устройстве' })).toBeVisible()
    expect(screen.getByRole('region', { name: 'В репозитории' })).toBeVisible()
    expect(container.querySelector('[data-testid="conflict-stages"]')).toHaveClass('grid', 'grid-cols-1', 'lg:grid-cols-3')
  })

  it('renders an absent common ancestor for add/add conflicts', () => {
    render(TextConflictResolver, resolverProps({ conflict: { ...conflict, base: null } }))

    expect(screen.getByRole('region', { name: 'Общий предок' })).toHaveTextContent('Файл отсутствует на этой стороне')
    expect(screen.getByRole('textbox', { name: 'На этом устройстве' })).toHaveValue('local')
    expect(screen.getByRole('textbox', { name: 'В репозитории' })).toHaveValue('remote')
  })

  it('uses native radio semantics without preselecting an action', () => {
    render(TextConflictResolver, resolverProps({ conflict: { ...conflict, actions: ['local', 'manual'] } }))

    const options = screen.getAllByRole('radio')
    expect(screen.getByRole('group', { name: 'Способ разрешения' })).toContainElement(options[0])
    expect(options).toHaveLength(2)
    expect(options).toEqual(expect.arrayContaining([
      screen.getByRole('radio', { name: 'Оставить версию на этом устройстве' }),
      screen.getByRole('radio', { name: 'Объединить вручную' }),
    ]))
    expect(options.every((option) => !option.checked)).toBe(true)
    expect(screen.queryByRole('radio', { name: 'Оставить версию из репозитория' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Применить решение' })).toBeDisabled()
  })

  it.each([
    ['Оставить версию на этом устройстве', 'local', { result_path: 'notes/idea.md', local_oid: 'local-oid' }],
    ['Оставить версию из репозитория', 'remote', { result_path: 'notes/idea.md', remote_oid: 'remote-oid' }],
  ])('resolves the %s action with its required payload', async (label, action, expected) => {
    const user = userEvent.setup()
    const props = resolverProps()
    render(TextConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: label }))
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))

    expect(props.onResolve).toHaveBeenCalledWith({
      base: 'work',
      operation_id: 'operation-1',
      conflict_id: 'sha256:conflict',
      path: 'notes/idea.md',
      action,
      ...expected,
    })
  })

  it('preserves the manual draft and selected action after a rejected resolution', async () => {
    const user = userEvent.setup()
    const props = resolverProps({ onResolve: vi.fn().mockRejectedValue(new Error('Не удалось сохранить')) })
    render(TextConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: 'Объединить вручную' }))
    const resultPath = screen.getByRole('textbox', { name: 'Путь результата' })
    const content = screen.getByRole('textbox', { name: 'Итоговый текст' })
    await user.clear(resultPath)
    await user.type(resultPath, 'notes/merged.md')
    await user.type(content, 'объединенный текст')
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))

    expect(props.onResolve).toHaveBeenCalledWith({
      base: 'work',
      operation_id: 'operation-1',
      conflict_id: 'sha256:conflict',
      path: 'notes/idea.md',
      action: 'manual',
      result_path: 'notes/merged.md',
      content: 'объединенный текст',
    })
    expect(await screen.findByRole('alert')).toHaveTextContent('Не удалось сохранить')
    expect(screen.getByRole('radio', { name: 'Объединить вручную' })).toBeChecked()
    expect(resultPath).toHaveValue('notes/merged.md')
    expect(content).toHaveValue('объединенный текст')
  })

  it('shows and retains distinct suggested paths for keeping both versions', async () => {
    const user = userEvent.setup()
    const props = resolverProps()
    render(TextConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: 'Сохранить обе версии' }))
    const localPath = screen.getByRole('textbox', { name: 'Путь версии на этом устройстве' })
    const remotePath = screen.getByRole('textbox', { name: 'Путь версии из репозитория' })
    expect(localPath).toHaveValue('notes/idea-local.md')
    expect(remotePath).toHaveValue('notes/idea-remote.md')
    await user.clear(localPath)
    await user.type(localPath, 'notes/local-copy.md')
    await user.click(screen.getByRole('radio', { name: 'Оставить версию на этом устройстве' }))
    await user.click(screen.getByRole('radio', { name: 'Сохранить обе версии' }))
    expect(localPath).toHaveValue('notes/local-copy.md')

    await user.click(screen.getByRole('button', { name: 'Применить решение' }))
    expect(props.onResolve).toHaveBeenCalledWith(expect.objectContaining({
      action: 'keep_both',
      local_path: 'notes/local-copy.md',
      remote_path: 'notes/idea-remote.md',
    }))
  })

  it('shows validation errors and disables every control while busy', async () => {
    const user = userEvent.setup()
    const props = resolverProps()
    const busyResolver = render(TextConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: 'Оставить версию на этом устройстве' }))
    await busyResolver.rerender({ ...props, busy: true })

    expect(busyResolver.container.querySelector('[data-testid="conflict-stages"]')).toHaveClass('grid-cols-1', 'lg:grid-cols-3')
    screen.getAllByRole('radio').forEach((option) => expect(option).toBeDisabled())
    expect(screen.getByRole('textbox', { name: 'Путь результата' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Применить решение' })).toBeDisabled()

    busyResolver.unmount()

    const validationProps = resolverProps({ conflict: { ...conflict, actions: ['manual'] } })
    render(TextConflictResolver, validationProps)
    await user.click(screen.getByRole('radio', { name: 'Объединить вручную' }))
    await user.clear(screen.getByRole('textbox', { name: 'Путь результата' }))
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Необходимо поле result_path')
    expect(validationProps.onResolve).not.toHaveBeenCalled()
  })

  it('disables controls while an individual resolution is pending', async () => {
    const user = userEvent.setup()
    let resolve
    const props = resolverProps({ onResolve: vi.fn(() => new Promise((done) => { resolve = done })) })
    render(TextConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: 'Оставить версию на этом устройстве' }))
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))

    screen.getAllByRole('radio').forEach((option) => expect(option).toBeDisabled())
    expect(screen.getByRole('textbox', { name: 'Путь результата' })).toBeDisabled()
    resolve()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Применить решение' })).not.toBeDisabled())
  })
})
