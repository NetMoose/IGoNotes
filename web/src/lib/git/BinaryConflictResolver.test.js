import { render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import BinaryConflictResolver from './BinaryConflictResolver.svelte'

const binaryBody = 'BINARY_BODY_MUST_NEVER_RENDER'
const conflict = {
  id: 'sha256:binary-conflict',
  path: 'assets/photo.png',
  content_kind: 'binary',
  actions: ['local', 'remote', 'keep_both', 'delete'],
  base: { path: 'assets/photo.png', oid: 'base-oid', mode: '100644', size: 1024, content: binaryBody },
  local: { path: 'assets/photo.png', oid: 'local-oid', mode: '100644', size: 2048, content: binaryBody },
  remote: { path: 'assets/photo.png', oid: 'remote-oid', mode: '100644', size: 4096, content: binaryBody },
}
const addAddConflict = {
  ...conflict,
  kind: 'add_add',
  base: null,
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

describe('BinaryConflictResolver', () => {
  it('renders responsive metadata stages without binary content or textareas', () => {
    const { container } = render(BinaryConflictResolver, resolverProps())

    expect(screen.getByRole('region', { name: 'Общий предок' })).toHaveTextContent('Двоичный файл')
    expect(screen.getByRole('region', { name: 'На этом устройстве' })).toHaveTextContent('Двоичный файл')
    expect(screen.getByRole('region', { name: 'В репозитории' })).toHaveTextContent('Двоичный файл')
    expect(container.querySelector('[data-testid="conflict-stages"]')).toHaveClass('grid', 'grid-cols-1', 'lg:grid-cols-3')
    expect(container).not.toHaveTextContent(binaryBody)
    expect(container.querySelectorAll('textarea')).toHaveLength(0)
  })

  it('offers only local, remote, and keep-both native actions without selecting one', () => {
    render(BinaryConflictResolver, resolverProps())

    const options = screen.getAllByRole('radio')
    expect(screen.getByRole('group', { name: 'Способ разрешения' })).toContainElement(options[0])
    expect(options).toEqual(expect.arrayContaining([
      screen.getByRole('radio', { name: 'Оставить версию на этом устройстве' }),
      screen.getByRole('radio', { name: 'Оставить версию из репозитория' }),
      screen.getByRole('radio', { name: 'Сохранить обе версии' }),
    ]))
    expect(options).toHaveLength(3)
    expect(options.every((option) => !option.checked)).toBe(true)
    expect(screen.queryByRole('radio', { name: 'Объединить вручную' })).not.toBeInTheDocument()
    expect(screen.queryByRole('radio', { name: 'Удалить файл' })).not.toBeInTheDocument()
  })

  it.each([
    ['Оставить версию на этом устройстве', 'local', { result_path: 'assets/photo.png', local_oid: 'local-oid' }],
    ['Оставить версию из репозитория', 'remote', { result_path: 'assets/photo.png', remote_oid: 'remote-oid' }],
  ])('submits the exact %s resolution', async (label, action, expected) => {
    const user = userEvent.setup()
    const props = resolverProps()
    render(BinaryConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: label }))
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))

    expect(props.onResolve).toHaveBeenCalledWith({
      base: 'work',
      operation_id: 'operation-1',
      conflict_id: 'sha256:binary-conflict',
      path: 'assets/photo.png',
      action,
      ...expected,
    })
  })

  it('uses distinct suggested paths and safety guidance for binary add/add keep-both', async () => {
    const user = userEvent.setup()
    const props = resolverProps({ conflict: addAddConflict })
    render(BinaryConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: 'Сохранить обе версии' }))
    const localPath = screen.getByRole('textbox', { name: 'Путь версии на этом устройстве' })
    const remotePath = screen.getByRole('textbox', { name: 'Путь версии из репозитория' })
    expect(localPath).toHaveValue('assets/photo-local.png')
    expect(remotePath).toHaveValue('assets/photo-remote.png')
    expect(screen.getByText('Оба файла будут записаны под явно заданными разными именами; существующие несвязанные файлы не будут перезаписаны.')).toBeVisible()
    await user.clear(localPath)
    await user.type(localPath, 'assets/photo-local.png')
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))
    expect(props.onResolve).toHaveBeenCalledWith({
      base: 'work',
      operation_id: 'operation-1',
      conflict_id: 'sha256:binary-conflict',
      path: 'assets/photo.png',
      action: 'keep_both',
      local_path: 'assets/photo-local.png',
      remote_path: 'assets/photo-remote.png',
      local_oid: 'local-oid',
      remote_oid: 'remote-oid',
    })
  })

  it('keeps drafts and shows errors after rejection while disabling busy controls', async () => {
    const user = userEvent.setup()
    const props = resolverProps({ onResolve: vi.fn().mockRejectedValue(new Error('Не удалось сохранить')) })
    const resolver = render(BinaryConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: 'Оставить версию на этом устройстве' }))
    const resultPath = screen.getByRole('textbox', { name: 'Путь результата' })
    await user.clear(resultPath)
    await user.type(resultPath, 'assets/final.png')
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Не удалось сохранить')
    expect(screen.getByRole('radio', { name: 'Оставить версию на этом устройстве' })).toBeChecked()
    expect(resultPath).toHaveValue('assets/final.png')

    await resolver.rerender({ ...props, busy: true })
    screen.getAllByRole('radio').forEach((option) => expect(option).toBeDisabled())
    expect(resultPath).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Применить решение' })).toBeDisabled()
  })

  it('disables controls while a resolution is pending', async () => {
    const user = userEvent.setup()
    let resolve
    const props = resolverProps({ onResolve: vi.fn(() => new Promise((done) => { resolve = done })) })
    render(BinaryConflictResolver, props)

    await user.click(screen.getByRole('radio', { name: 'Оставить версию на этом устройстве' }))
    await user.click(screen.getByRole('button', { name: 'Применить решение' }))

    screen.getAllByRole('radio').forEach((option) => expect(option).toBeDisabled())
    expect(screen.getByRole('textbox', { name: 'Путь результата' })).toBeDisabled()
    resolve()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Применить решение' })).not.toBeDisabled())
  })
})
