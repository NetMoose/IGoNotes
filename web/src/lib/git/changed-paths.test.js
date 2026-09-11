import { describe, expect, it } from 'vitest'

import { changedPathKey, noteInChangedPaths, terminalChangedPaths } from './changed-paths.js'

describe('changed paths', () => {
  it.each(['ready', 'error', 'paused'])('returns sorted unique nonempty paths for terminal %s status', (state) => {
    expect(terminalChangedPaths({
      state,
      repository_path: '/notes/work',
      operation_id: 'operation-1',
      changed_paths: ['z.md', '', 'a.md', 'z.md'],
    })).toEqual(['a.md', 'z.md'])
  })

  it.each([
    { state: 'syncing', repository_path: '/notes/work', operation_id: 'operation-1', changed_paths: ['note.md'] },
    { state: 'ready', repository_path: '', operation_id: 'operation-1', changed_paths: ['note.md'] },
    { state: 'ready', repository_path: '/notes/work', operation_id: '', changed_paths: ['note.md'] },
    { state: 'ready', repository_path: '/notes/work', operation_id: 'operation-1', changed_paths: 'note.md' },
  ])('ignores statuses without all terminal path identity fields', (status) => {
    expect(terminalChangedPaths(status)).toEqual([])
  })

  it('keys a changed path only when every identity component is nonempty', () => {
    expect(changedPathKey({ repository_path: '/notes/work', operation_id: 'operation-1' }, 'note.md'))
      .toBe('/notes/work\0operation-1\0note.md')
    expect(changedPathKey({ repository_path: '/notes/work', operation_id: '' }, 'note.md')).toBe('')
    expect(changedPathKey({ repository_path: '/notes/work', operation_id: 'operation-1' }, '')).toBe('')
  })

  it('matches only nonempty note IDs contained in changed paths', () => {
    expect(noteInChangedPaths('topic/note.md', ['other.md', 'topic/note.md'])).toBe(true)
    expect(noteInChangedPaths('', [''])).toBe(false)
    expect(noteInChangedPaths('topic/note.md', 'topic/note.md')).toBe(false)
  })
})
