import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { tick } from 'svelte'

vi.mock('../api.js', () => ({
  getClipperImports: vi.fn().mockResolvedValue({ imports: [] }),
  abandonClipperImport: vi.fn(),
  getClipperConnections: vi.fn(),
  createClipperConnection: vi.fn(),
  revokeClipperConnection: vi.fn(),
}))
import { getClipperConnections, createClipperConnection, revokeClipperConnection } from '../api.js'
import ClipperSettingsSection from './ClipperSettingsSection.svelte'

const connection = { id: 'first', label: 'Browser', created_at: '2026-10-21T00:00:00Z', revoked_at: null }
const secret = '<img src=x onerror=alert(1)>opaque&secret'
function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
async function create(user, label = 'Browser') {
  const input = screen.getByLabelText('Название подключения')
  await fireEvent.input(input, { target: { value: label } })
  await user.click(screen.getByRole('button', { name: 'Создать подключение' }))
}
beforeEach(() => {
  vi.mocked(getClipperConnections).mockReset().mockResolvedValue({ connections: [] })
  vi.mocked(createClipperConnection).mockReset().mockResolvedValue({ connection, secret })
  vi.mocked(revokeClipperConnection).mockReset().mockResolvedValue(undefined)
})

it('loads metadata, shows revoked status and disables revoked actions', async () => {
  const pending = deferred()
  vi.mocked(getClipperConnections).mockReturnValue(pending.promise)
  render(ClipperSettingsSection)
  expect(screen.getByText('Загрузка подключений…')).toBeVisible()
  pending.resolve({ connections: [connection, { ...connection, id: 'old', label: 'Old', revoked_at: '2026-10-22T00:00:00Z' }] })
  const active = await screen.findByRole('article', { name: 'Подключение Browser' })
  expect(within(active).getByText('Активно')).toBeVisible()
  expect(within(active).getByText(connection.created_at)).toBeVisible()
  const revoked = screen.getByRole('article', { name: 'Подключение Old' })
  expect(within(revoked).getByText('Отозвано')).toBeVisible()
  expect(within(revoked).getByText('2026-10-22T00:00:00Z')).toBeVisible()
  expect(within(revoked).getByRole('button', { name: 'Отозвать' })).toBeDisabled()
})

it('retries list failures manually and displays the empty state', async () => {
  vi.mocked(getClipperConnections).mockRejectedValueOnce(new Error('Offline'))
  const user = userEvent.setup()
  render(ClipperSettingsSection)
  expect(await screen.findByRole('alert')).toHaveTextContent('Offline')
  await user.click(screen.getByRole('button', { name: 'Повторить загрузку' }))
  expect(await screen.findByText('Подключений пока нет')).toBeVisible()
  expect(getClipperConnections).toHaveBeenCalledTimes(2)
})

it('shows an escaped one-time secret, keeps it during metadata refresh, and dismisses it', async () => {
  const user = userEvent.setup()
  const storage = vi.spyOn(Storage.prototype, 'setItem')
  const log = vi.spyOn(console, 'log')
  const originalURL = window.location.href
  const { container } = render(ClipperSettingsSection)
  await create(user)
  expect(createClipperConnection).toHaveBeenCalledWith('Browser')
  const code = await screen.findByText(secret)
  expect(code).toBeVisible()
  expect(container.querySelector('img')).toBeNull()
  expect(container.innerHTML).toContain('&lt;img')
  await user.click(screen.getByRole('button', { name: 'Обновить список' }))
  expect(screen.getByText(secret)).toBeVisible()
  expect(storage).not.toHaveBeenCalled()
  expect(log).not.toHaveBeenCalled()
  expect(window.location.href).toBe(originalURL)
  await user.click(screen.getByRole('button', { name: 'Скрыть секрет' }))
  expect(container.textContent).not.toContain(secret)
  storage.mockRestore()
  log.mockRestore()
})

it('clears the previous secret before a later create and prevents duplicate POSTs', async () => {
  const user = userEvent.setup()
  render(ClipperSettingsSection)
  await create(user)
  expect(await screen.findByText(secret)).toBeVisible()
  const pending = deferred()
  vi.mocked(createClipperConnection).mockReturnValue(pending.promise)
  await create(user, 'Second')
  expect(screen.queryByText(secret)).not.toBeInTheDocument()
  const button = screen.getByRole('button', { name: 'Создать подключение' })
  expect(button).toBeDisabled()
  await fireEvent.click(button)
  expect(createClipperConnection).toHaveBeenCalledTimes(2)
  pending.resolve({ connection: { ...connection, id: 'second' }, secret: 'second-secret' })
  expect(await screen.findByText('second-secret')).toBeVisible()
})

it('clears the secret before revoke, allows manual retry and refreshes metadata', async () => {
  const user = userEvent.setup()
  render(ClipperSettingsSection)
  await create(user)
  expect(await screen.findByText(secret)).toBeVisible()
  vi.mocked(revokeClipperConnection).mockRejectedValueOnce(new Error('Revoke offline'))
  const card = screen.getByRole('article', { name: 'Подключение Browser' })
  await user.click(within(card).getByRole('button', { name: 'Отозвать' }))
  expect(screen.queryByText(secret)).not.toBeInTheDocument()
  expect(await within(card).findByRole('alert')).toHaveTextContent('Revoke offline')
  vi.mocked(getClipperConnections).mockResolvedValue({ connections: [{ ...connection, revoked_at: '2026-10-22' }] })
  await user.click(within(card).getByRole('button', { name: 'Отозвать' }))
  expect(await screen.findByText('Отозвано')).toBeVisible()
  expect(within(card).getByRole('button', { name: 'Отозвать' })).toBeDisabled()
  expect(revokeClipperConnection).toHaveBeenCalledTimes(2)
  expect(revokeClipperConnection).toHaveBeenCalledWith('first')
})

it('explains unknown POST outcome without automatic retries and permits metadata check or a new explicit create', async () => {
  const user = userEvent.setup()
  vi.mocked(createClipperConnection).mockRejectedValueOnce(new Error('Offline'))
  render(ClipperSettingsSection)
  await create(user)
  expect(await screen.findByRole('alert')).toHaveTextContent('Результат создания неизвестен')
  expect(screen.getByRole('alert')).toHaveTextContent('секрет')
  expect(screen.getByRole('alert')).toHaveTextContent('новое подключение')
  expect(createClipperConnection).toHaveBeenCalledOnce()
  await user.click(screen.getByRole('button', { name: 'Обновить список' }))
  expect(createClipperConnection).toHaveBeenCalledOnce()
  await create(user)
  expect(await screen.findByText(secret)).toBeVisible()
})

it.each(['', '   ', 'x'.repeat(129), 'has\u0001control', 'has\u007fcontrol', 'has\u0085control'])(
  'validates invalid Unicode label %j without issuing a POST', async (label) => {
    const user = userEvent.setup()
    render(ClipperSettingsSection)
    await create(user, label)
    expect(await screen.findByRole('alert')).toHaveTextContent('от 1 до 128 символов без управляющих символов')
    expect(createClipperConnection).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Название подключения')).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByLabelText('Название подключения')).toHaveFocus()
  },
)

it('accepts 128 Unicode code points rather than limiting UTF-16 code units', async () => {
  const user = userEvent.setup()
  render(ClipperSettingsSection)
  await create(user, '😀'.repeat(128))
  expect(createClipperConnection).toHaveBeenCalledWith('😀'.repeat(128))
  expect(await screen.findByText(secret)).toBeVisible()
})

it('shows server label validation as a field error with manual retry', async () => {
  const user = userEvent.setup()
  vi.mocked(createClipperConnection).mockRejectedValueOnce(Object.assign(new Error('Server label error'), { status: 400, field: 'label' }))
  render(ClipperSettingsSection)
  await create(user)
  expect(await screen.findByRole('alert')).toHaveTextContent('Server label error')
  expect(screen.getByRole('alert')).not.toHaveTextContent('Результат создания неизвестен')
  expect(screen.getByLabelText('Название подключения')).toHaveAttribute('aria-invalid', 'true')
  await waitFor(() => expect(screen.getByLabelText('Название подключения')).toHaveFocus())
  await create(user, 'Corrected')
  expect(await screen.findByText(secret)).toBeVisible()
})

it.each(['resolve', 'reject'])('ignores a late create %s after destroy', async (settlement) => {
  const user = userEvent.setup()
  const pending = deferred()
  vi.mocked(createClipperConnection).mockReturnValue(pending.promise)
  const result = render(ClipperSettingsSection)
  await create(user)
  result.unmount()
  if (settlement === 'resolve') pending.resolve({ connection, secret })
  else pending.reject(new Error('Late error'))
  await pending.promise.catch(() => {})
  await tick()
  expect(document.body.textContent).not.toContain(secret)
  expect(getClipperConnections).toHaveBeenCalledOnce()
})

it('keeps the issued secret when metadata refresh fails and retries only GET', async () => {
  const user = userEvent.setup()
  render(ClipperSettingsSection)
  await create(user)
  expect(await screen.findByText(secret)).toBeVisible()
  vi.mocked(getClipperConnections).mockRejectedValueOnce(new Error('Refresh offline'))
  await user.click(screen.getByRole('button', { name: 'Обновить список' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Refresh offline')
  expect(screen.getByText(secret)).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Повторить загрузку' }))
  expect(screen.getByText(secret)).toBeVisible()
  expect(createClipperConnection).toHaveBeenCalledOnce()
})

it('keeps revoke single-flight and does not refresh after its late completion on destroy', async () => {
  const user = userEvent.setup()
  vi.mocked(getClipperConnections).mockResolvedValue({ connections: [connection] })
  const pending = deferred()
  vi.mocked(revokeClipperConnection).mockReturnValue(pending.promise)
  const result = render(ClipperSettingsSection)
  const card = await screen.findByRole('article', { name: 'Подключение Browser' })
  const revoke = within(card).getByRole('button', { name: 'Отозвать' })
  await user.click(revoke)
  expect(within(card).getByRole('status')).toHaveTextContent('Отзыв подключения')
  expect(revoke).toBeDisabled()
  await fireEvent.click(revoke)
  expect(revokeClipperConnection).toHaveBeenCalledOnce()
  result.unmount()
  pending.resolve()
  await pending.promise
  await tick()
  expect(getClipperConnections).toHaveBeenCalledOnce()
})

it.each(['resolve', 'reject'])('ignores a late list %s after destroy', async (settlement) => {
  const pending = deferred()
  vi.mocked(getClipperConnections).mockReturnValue(pending.promise)
  const result = render(ClipperSettingsSection)
  result.unmount()
  if (settlement === 'resolve') pending.resolve({ connections: [connection] })
  else pending.reject(new Error('Late list error'))
  await pending.promise.catch(() => {})
  await tick()
  expect(screen.queryByText('Browser')).not.toBeInTheDocument()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})
