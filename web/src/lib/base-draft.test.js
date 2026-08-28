import { describe, expect, it } from 'vitest'

import {
  activeBase,
  normalizeBaseDraft,
  resolveBasePath,
  validateBaseDraft,
} from './base-draft.js'

describe('base draft helpers', () => {
  it('trims surrounding whitespace without changing case', () => {
    expect(normalizeBaseDraft({ mode: 'create', name: ' Work ', path: ' /notes ' })).toEqual({
      mode: 'create',
      name: 'Work',
      path: '/notes',
    })
  })

  it.each(['', '   ', '.', '..', 'team/shared', String.raw`team\shared`])(
    'rejects the create base name %j',
    (name) => {
      expect(validateBaseDraft({ mode: 'create', name, path: '/notes' })).toEqual({
        name: name.trim()
          ? 'Имя новой базы не может быть точкой и содержать / или \\'
          : 'Введите имя базы',
      })
    },
  )

  it('rejects an exact duplicate name case-sensitively', () => {
    const bases = [{ name: 'Work' }]

    expect(validateBaseDraft({ mode: 'connect', name: 'Work', path: '/notes' }, bases)).toEqual({
      name: 'База с таким именем уже добавлена',
    })
    expect(validateBaseDraft({ mode: 'connect', name: 'work', path: '/notes' }, bases)).toEqual({})
  })

  it('allows the unchanged original name while editing', () => {
    expect(
      validateBaseDraft(
        { mode: 'connect', name: 'Work', path: '/notes' },
        [{ name: 'Work' }],
        'Work',
      ),
    ).toEqual({})
  })

  it('requires a path', () => {
    expect(validateBaseDraft({ mode: 'connect', name: 'Work', path: '  ' })).toEqual({
      path: 'Укажите каталог',
    })
  })

  it.each([
    [{ mode: 'create', name: 'work', path: '/notes/' }, '/notes/work'],
    [{ mode: 'create', name: 'work', path: 'C:\\Notes\\' }, 'C:\\Notes\\work'],
    [{ mode: 'connect', name: 'work', path: '/notes/work/' }, '/notes/work/'],
  ])('resolves a base path for %o', (draft, expected) => {
    expect(resolveBasePath(draft)).toBe(expected)
  })

  it('returns the configured active base', () => {
    const work = { name: 'Work', path: '/notes/work' }

    expect(activeBase({ current_base: 'Work', bases: [work, { name: 'Home' }] })).toBe(work)
  })

  it.each([
    null,
    {},
    { current_base: 'Missing', bases: [{ name: 'Work' }] },
    { current_base: 'Work', bases: null },
    { current_base: 1, bases: [{ name: 'Work' }] },
  ])('returns null for missing or malformed config %o', (config) => {
    expect(activeBase(config)).toBeNull()
  })
})
