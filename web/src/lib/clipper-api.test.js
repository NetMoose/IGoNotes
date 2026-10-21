import { afterEach, expect, it, vi } from 'vitest'
import * as api from './api.js'

const connection = { id: 'id /?', label: 'Browser', created_at: '2026-10-21T00:00:00Z', revoked_at: null }
afterEach(() => vi.unstubAllGlobals())

function respond(payload, status = 200) {
  const fetch = vi.fn().mockResolvedValue(new Response(status === 204 ? null : JSON.stringify(payload), { status }))
  vi.stubGlobal('fetch', fetch)
  return fetch
}

it('lists connection metadata with GET', async () => {
  const fetch = respond({ connections: [connection] })
  expect(typeof api.getClipperConnections).toBe('function')
  expect(await api.getClipperConnections()).toEqual({ connections: [connection] })
  expect(fetch).toHaveBeenCalledWith('/api/clipper/connections', expect.objectContaining({ method: 'GET' }))
})

it('issues a secret with an explicit POST label body', async () => {
  const fetch = respond({ connection, secret: 'opaque' }, 201)
  expect(typeof api.createClipperConnection).toBe('function')
  expect(await api.createClipperConnection('Browser')).toEqual({ connection, secret: 'opaque' })
  expect(fetch).toHaveBeenCalledWith('/api/clipper/connections', expect.objectContaining({ method: 'POST', body: JSON.stringify({ label: 'Browser' }) }))
})

it('revokes with an encoded ID and accepts only an empty 204', async () => {
  const fetch = respond(null, 204)
  expect(typeof api.revokeClipperConnection).toBe('function')
  await expect(api.revokeClipperConnection(connection.id)).resolves.toBeUndefined()
  expect(fetch).toHaveBeenCalledWith('/api/clipper/connections?id=id%20%2F%3F', expect.objectContaining({ method: 'DELETE' }))
  respond({}, 200)
  await expect(api.revokeClipperConnection(connection.id)).rejects.toMatchObject({ code: 'invalid_response' })
})

it('preserves safe API errors without automatically retrying POST', async () => {
  const fetch = respond({ code: 'clipper_invalid_label', message: 'Invalid label', field: 'label' }, 400)
  expect(typeof api.createClipperConnection).toBe('function')
  await expect(api.createClipperConnection('')).rejects.toMatchObject({ status: 400, code: 'clipper_invalid_label', message: 'Invalid label', field: 'label' })
  expect(fetch).toHaveBeenCalledOnce()
})

it.each([
  ['list', { connections: [{ ...connection, revoked_at: false }] }],
  ['list', {}],
  ['create', { connection, secret: '' }],
  ['create', { secret: 'opaque' }],
])('rejects malformed %s responses', async (operation, payload) => {
  respond(payload, operation === 'list' ? 200 : 201)
  const helper = operation === 'list' ? api.getClipperConnections : api.createClipperConnection
  expect(typeof helper).toBe('function')
  await expect(helper('Browser')).rejects.toMatchObject({ code: 'invalid_response' })
})
