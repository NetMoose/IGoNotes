import { describe, expect, it } from 'vitest'

import { buildResolution, suggestSidePath } from './conflict-resolution.js'

const context = { base: 'work', operationId: 'operation-1' }
const conflict = {
  id: 'sha256:conflict',
  path: 'topic/note.tar.gz',
  actions: ['local', 'remote', 'manual', 'keep_both', 'delete'],
  local: { oid: 'local-oid' },
  remote: { oid: 'remote-oid' },
}

describe('conflict resolution payloads', () => {
  it('inserts a side suffix before only a final non-leading extension', () => {
    expect(suggestSidePath('topic/note.tar.gz', 'local')).toBe('topic/note.tar-local.gz')
    expect(suggestSidePath('topic/.env', 'remote')).toBe('topic/.env-remote')
    expect(suggestSidePath('topic/note', 'local')).toBe('topic/note-local')
  })

  it.each([
    ['local', { result_path: 'topic/local.md' }, { result_path: 'topic/local.md', local_oid: 'local-oid' }],
    ['remote', { result_path: 'topic/remote.md' }, { result_path: 'topic/remote.md', remote_oid: 'remote-oid' }],
    ['manual', { result_path: 'topic/manual.md', content: '# Manual' }, { result_path: 'topic/manual.md', content: '# Manual' }],
    ['keep_both', { local_path: 'topic/local.md', remote_path: 'topic/remote.md' }, { local_path: 'topic/local.md', remote_path: 'topic/remote.md', local_oid: 'local-oid', remote_oid: 'remote-oid' }],
    ['delete', {}, {}],
  ])('builds the %s resolution with only its required fields', (action, draft, expected) => {
    expect(buildResolution(context, conflict, { action, ...draft })).toEqual({
      base: 'work',
      operation_id: 'operation-1',
      conflict_id: 'sha256:conflict',
      path: 'topic/note.tar.gz',
      action,
      ...expected,
    })
  })

  it('rejects an action unavailable for the conflict', () => {
    expect(() => buildResolution(context, { ...conflict, actions: ['local'] }, { action: 'remote', result_path: 'topic/remote.md' }))
      .toThrow(/Недоступное действие/)
  })

  it.each([
    [{ action: 'local' }, /result_path|local_oid/],
    [{ action: 'manual', result_path: 'topic/manual.md' }, /content/],
    [{ action: 'keep_both', local_path: 'same.md', remote_path: 'same.md' }, /local_path|remote_path/],
  ])('names required fields in Russian validation errors', (draft, fields) => {
    expect(() => buildResolution(context, conflict, draft)).toThrow(fields)
  })
})
