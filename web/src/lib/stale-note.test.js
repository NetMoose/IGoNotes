import { describe, expect, it } from 'vitest'

import { makeStaleNote, readNoteResponse, readSaveResponse } from './stale-note.js'

describe('stale note helpers', () => {
  it('reads a note response with content and a revision', () => {
    expect(readNoteResponse({ content: '# Note', revision: 'revision-1' }))
      .toEqual({ content: '# Note', revision: 'revision-1' })
  })

  it.each([
    { content: '# Note' },
    { content: '# Note', revision: '' },
    { content: '# Note', revision: 1 },
  ])('rejects note responses without a revision', (response) => {
    expect(() => readNoteResponse(response)).toThrow(/revision/)
  })

  it('reads only saved responses with a nonempty revision', () => {
    expect(readSaveResponse({ status: 'saved', revision: 'revision-2' }))
      .toEqual({ status: 'saved', revision: 'revision-2' })
  })

  it.each([
    { revision: 'revision-2' },
    { status: 'saved', revision: '' },
    { status: 'queued', revision: 'revision-2' },
  ])('rejects invalid save responses', (response) => {
    expect(() => readSaveResponse(response)).toThrow(/revision|status/)
  })

  it('normalizes stale note details and records a missing disk note', () => {
    expect(makeStaleNote({ noteId: 'topic/note.md', mine: 42, disk: null })).toEqual({
      noteId: 'topic/note.md',
      mine: '42',
      diskContent: '',
      diskRevision: '',
      diskMissing: true,
    })
    expect(makeStaleNote({ noteId: 'topic/note.md', mine: '# Mine', disk: { content: '# Disk', revision: 'revision-2' } }))
      .toMatchObject({ diskContent: '# Disk', diskRevision: 'revision-2', diskMissing: false })
  })
})
