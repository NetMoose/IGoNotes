import { render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('./api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual, uploadAsset: vi.fn() }
})

import { uploadAsset } from './api.js'
import EditorHost from '../test/EditorHost.svelte'
import Editor from './Editor.svelte'

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

async function renderEditor() {
  const result = render(EditorHost)
  await waitFor(() => expect(result.container.querySelector('.cm-editor')).toBeInTheDocument())
  return result
}

function imageFile(name) {
  return new File(['image'], name, { type: 'image/png' })
}

const focusClasses = [
  'focus-visible:outline-2',
  'focus-visible:outline-offset-2',
  'focus-visible:outline-blue-600',
]

function expectEditorFocusClasses(editor) {
  const controls = [
    ...within(editor).queryAllByRole('button'),
    ...editor.querySelectorAll('input[type="text"], input[type="url"]'),
  ].filter((control) => control.matches(':not([disabled])'))

  expect(controls.length).toBeGreaterThan(0)
  for (const control of controls) {
    expect(control).toHaveClass(...focusClasses)
    expect(control).not.toHaveClass('focus:outline-none', 'focus-visible:outline-none')
  }
}

describe('Editor image uploads', () => {
  beforeEach(() => {
    vi.mocked(uploadAsset).mockReset()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('keeps concurrent image uploads in insertion order when they resolve out of order', async () => {
    const user = userEvent.setup()
    const firstUpload = deferred()
    const secondUpload = deferred()
    vi.mocked(uploadAsset)
      .mockReturnValueOnce(firstUpload.promise)
      .mockReturnValueOnce(secondUpload.promise)
    const { container } = await renderEditor()
    const fileInput = container.querySelector('input[type="file"]')
    const output = screen.getByLabelText('Markdown content')

    await user.upload(fileInput, imageFile('first.png'))
    await user.upload(fileInput, imageFile('second.png'))
    expect(uploadAsset).toHaveBeenCalledTimes(2)

    secondUpload.resolve({ path: 'second.png' })
    await secondUpload.promise
    await waitFor(() => expect(output).toHaveTextContent('![[second.png]]'))

    firstUpload.resolve({ path: 'first.png' })
    await firstUpload.promise
    await waitFor(() => expect(output).toHaveTextContent('![[first.png]]'))

    const markdown = output.textContent
    expect(markdown.indexOf('![[first.png]]')).toBeLessThan(markdown.indexOf('![[second.png]]'))
    expect(markdown).not.toContain('Загрузка...')
    expect(markdown).not.toContain('igonotes-upload')
  })

  it('replaces an invalid upload response with the upload error placeholder', async () => {
    const user = userEvent.setup()
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(uploadAsset).mockResolvedValue({})
    const { container } = await renderEditor()
    const fileInput = container.querySelector('input[type="file"]')
    const output = screen.getByLabelText('Markdown content')

    await user.upload(fileInput, imageFile('invalid.png'))

    await waitFor(() => expect(output).toHaveTextContent('![[Ошибка загрузки]]'))
    expect(output).not.toHaveTextContent('![[undefined]]')
    expect(output).not.toHaveTextContent('Загрузка...')
    expect(consoleError).toHaveBeenCalledOnce()
  })

  it('settles a pending upload after unmount without side effects', async () => {
    const user = userEvent.setup()
    const request = deferred()
    const unhandled = vi.fn((event) => event.preventDefault())
    vi.mocked(uploadAsset).mockReturnValue(request.promise)
    window.addEventListener('unhandledrejection', unhandled)

    try {
      const { container, unmount } = await renderEditor()
      await user.upload(container.querySelector('input[type="file"]'), imageFile('pending.png'))
      expect(uploadAsset).toHaveBeenCalledOnce()

      unmount()
      request.resolve({ path: 'pending.png' })
      await request.promise
      await Promise.resolve()

      expect(unhandled).not.toHaveBeenCalled()
    } finally {
      request.resolve({ path: 'pending.png' })
      window.removeEventListener('unhandledrejection', unhandled)
    }
  })

  it('flushes the final uploaded image before a note transition changes content', async () => {
    const user = userEvent.setup()
    const request = deferred()
    vi.mocked(uploadAsset).mockReturnValue(request.promise)
    const { container } = await renderEditor()
    const output = screen.getByLabelText('Markdown content')
    const flushed = screen.getByLabelText('Flushed markdown')
    const transitionStatus = screen.getByLabelText('Transition status')

    try {
      await user.upload(container.querySelector('input[type="file"]'), imageFile('pending.png'))
      expect(output).toHaveTextContent('Загрузка...')

      await user.click(screen.getByRole('button', { name: 'Transition note' }))

      expect(transitionStatus).toHaveTextContent('note.md')
      expect(flushed.textContent).toBe('')

      request.resolve({ path: 'assets/images/pending.png' })

      await waitFor(() => expect(transitionStatus).toHaveTextContent('next.md'))
      expect(flushed).toHaveTextContent('![[assets/images/pending.png]]')
      expect(flushed).not.toHaveTextContent('Загрузка...')
      expect(flushed).not.toHaveTextContent('igonotes-upload')
    } finally {
      request.resolve({ path: 'assets/images/pending.png' })
    }
  })

  it('keeps toolbar icon buttons named and their SVGs decorative', async () => {
    await renderEditor()
    const names = [
      'Жирный',
      'Курсив',
      'Зачеркнутый',
      'Подчеркнутый',
      'Заголовок',
      'Цитата',
      'Блок кода',
      'Инлайн код',
      'Список',
      'Нумерованный список',
      'Чекбокс',
      'Таблица',
      'Ссылка',
      'Вставить картинку',
      'Режим редактора',
      'Предварительный просмотр',
    ]

    for (const name of names) {
      const button = screen.getByRole('button', { name })
      expect(button).toHaveAttribute('title', name)
      expect(button).toHaveAttribute('aria-label', name)
      expect(button.querySelector('svg')).toHaveAttribute('aria-hidden', 'true')
    }
  })

  it('gives every visible Editor control an explicit keyboard focus outline', async () => {
    const user = userEvent.setup()
    const { container } = await renderEditor()
    const editor = container.firstElementChild

    expectEditorFocusClasses(editor)

    for (const menu of ['Заголовок', 'Блок кода']) {
      await user.click(within(editor).getByRole('button', { name: menu }))
      expectEditorFocusClasses(editor)
      await user.click(within(editor).getByRole('button', { name: menu }))
    }

    await user.click(within(editor).getByRole('button', { name: 'Ссылка' }))
    expectEditorFocusClasses(editor)
  })
})

describe('Editor preview image paths and frontmatter', () => {
  async function preview(content, noteId = 'research/articles/note.md') {
    const result = render(Editor, { noteId, content })
    await userEvent.setup().click(screen.getByRole('button', { name: 'Предварительный просмотр' }))
    return result
  }

  it('resolves nested images, legacy uploads and wiki links without changing external URLs', async () => {
    const { container } = await preview([
      '![clip](../../assets/images/clip.png)',
      '![same](./same.png)',
      '![upload](assets/images/upload.png)',
      '![[assets/images/wiki.png]]',
      '![[./wiki.png]]',
      '![web](https://example.com/a.png?x=1&y=2)',
      '![data](data:image/png;base64,AAAA)',
      '![raw](/api/raw?path=assets%2Fimages%2Fready.png)',
      '![escape](../../../escape.png)',
      '![scheme](javascript:evil)',
    ].join('\n\n'))
    const images = container.querySelector('article').querySelectorAll('img')
    const sources = [...images].map((image) => image.getAttribute('src'))
    expect(sources).toEqual([
      '/api/raw?path=assets%2Fimages%2Fclip.png',
      '/api/raw?path=research%2Farticles%2Fsame.png',
      '/api/raw?path=assets%2Fimages%2Fupload.png',
      '/api/raw?path=assets%2Fimages%2Fwiki.png',
      '/api/raw?path=.%2Fwiki.png',
      'https://example.com/a.png?x=1&y=2',
      'data:image/png;base64,AAAA',
      '/api/raw?path=assets%2Fimages%2Fready.png',
      null, null,
    ])
  })

  it('uses the current note path after switching notes while preview stays open', async () => {
    const content = '![same](./same.png)'
    const { container, rerender } = await preview(content)
    expect(container.querySelector('article img')).toHaveAttribute('src', '/api/raw?path=research%2Farticles%2Fsame.png')
    await rerender({ noteId: 'other/note.md', content })
    expect(container.querySelector('article img')).toHaveAttribute('src', '/api/raw?path=other%2Fsame.png')
  })

  it('renders frontmatter as exact literal text, including HTML-like title and quotes', async () => {
    const frontmatter = '  title: "<img src=x onerror=evil()> & <b>hello</b>"\nsource: \'<&>"\'\n  '
    const { container } = await preview(`---\n${frontmatter}\n---\n# Body`)
    const details = container.querySelector('article details')
    expect(details.querySelector('pre').textContent).toBe(frontmatter)
    expect(details.querySelector('img, b, [onerror]')).toBeNull()
    expect(container.querySelector('article h1')).toHaveTextContent('Body')
  })

  it('round-trips encoded filenames and escaped HTML image attributes without injecting API parameters', async () => {
    const { container } = await preview('<img src="./%E5%9B%BE%20a%2520.png?x=1&amp;path=outside#part" alt="&quot;&lt;alt&gt;" title="&quot; onerror=&quot;evil">')
    const image = container.querySelector('article img')
    const url = new URL(image.getAttribute('src'), 'http://localhost')
    expect([...url.searchParams.keys()]).toEqual(['path'])
    expect(url.searchParams.get('path')).toBe('research/articles/图 a%20.png?x=1&path=outside#part')
    expect(image.getAttribute('alt')).toBe('"<alt>')
    expect(image.getAttribute('title')).toBe('" onerror="evil')
    expect(image.hasAttribute('onerror')).toBe(false)
  })

  it('preserves leading blank lines inside frontmatter text', async () => {
    const frontmatter = '\n\ntitle: "<&>"\n'
    const { container } = await preview(`---\n${frontmatter}\n---\nBody`)
    expect(container.querySelector('article details pre').textContent).toBe(frontmatter)
  })

  it('keeps Markdown image title and alt text escaped while resolving its URL', async () => {
    const { container } = await preview('![&quot;&lt;alt&gt;](./a%20b%2520.png "&quot; onerror=&quot;evil")')
    const image = container.querySelector('article img')
    expect(image).toHaveAttribute('src', '/api/raw?path=research%2Farticles%2Fa%20b%2520.png')
    expect(image).toHaveAttribute('alt', '"<alt>')
    expect(image).toHaveAttribute('title', '" onerror="evil')
    expect(image.hasAttribute('onerror')).toBe(false)
  })

  it('retains highlighted code, interactive tasks and link attributes', async () => {
    const { container } = await preview('```js\nconst answer = 42\n```\n\n- [ ] task\n\n[link](https://example.com)')
    expect(container.querySelector('article code')).toHaveClass('hljs', 'language-js')
    expect(container.querySelector('article code span')).toBeInTheDocument()
    const checkbox = container.querySelector('article input[type="checkbox"]')
    expect(checkbox).not.toBeDisabled()
    expect(container.querySelector('article a')).toHaveAttribute('target', '_blank')
    expect(container.querySelector('article a')).toHaveAttribute('rel', 'noopener noreferrer')
  })
})
