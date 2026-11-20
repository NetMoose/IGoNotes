import { describe, expect, it } from 'vitest'
import { resolveImagePath } from './imagePath.js'

const note = 'research/articles/note.md'
const raw = (path) => `/api/raw?path=${encodeURIComponent(path)}`

describe('resolveImagePath', () => {
  it.each([
    ['../../assets/images/clip.png', note, 'assets/images/clip.png'],
    ['./same.png', note, 'research/articles/same.png'],
    ['../other/./image.png', note, 'research/other/image.png'],
    ['./same.png', 'note.md', 'same.png'],
    ['assets/images/upload.png', note, 'assets/images/upload.png'],
    ['other/bare.png', note, 'other/bare.png'],
    ['./%E5%9B%BE%20a%26b.png', note, 'research/articles/图 a&b.png'],
    ['./100%25-%252e%252e.png', note, 'research/articles/100%-%2e%2e.png'],
    ['./a?x=1&path=outside#part.png', note, 'research/articles/a?x=1&path=outside#part.png'],
    ['./%2e%2e/%2e%2e/assets/images/clip.png', note, 'assets/images/clip.png'],
  ])('resolves %s from %s', (src, noteId, expected) => {
    const result = resolveImagePath(src, noteId)
    expect(result).toBe(raw(expected))
    const url = new URL(result, 'http://localhost')
    expect([...url.searchParams.keys()]).toEqual(['path'])
    expect(url.searchParams.get('path')).toBe(expected)
    expect(url.hash).toBe('')
  })

  it.each([
    '../../../escape.png', './../../../escape.png',
    './%2e%2e/%2e%2e/%2e%2e/escape.png', './..%2f..%2f..%2fescape.png',
    'javascript:alert(1)', 'vbscript:evil', 'data:text/html,evil',
    'java\nscript:evil', './bad%ZZ.png', './bad%00.png', './..%5cescape.png',
  ])('rejects unsafe or malformed reference %s', (src) => {
    expect(resolveImagePath(src, note)).toBeNull()
  })

  it('rejects parent traversal from a root note', () => {
    expect(resolveImagePath('../escape.png', 'note.md')).toBeNull()
  })

  it.each([
    'https://example.com/a.png?x=1&y=2', 'http://example.com/a.png',
    'data:image/png;base64,AAAA', '/api/raw?path=assets%2Fimages%2Fa%2520.png&v=1',
  ])('preserves ready image URL %s', (src) => {
    expect(resolveImagePath(src, note)).toBe(src)
  })
})
