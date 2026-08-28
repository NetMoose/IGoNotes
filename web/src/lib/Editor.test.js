import { render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('./api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual, uploadAsset: vi.fn() }
})

import { uploadAsset } from './api.js'
import EditorHost from '../test/EditorHost.svelte'

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
      expect(button.querySelector('svg')).toHaveAttribute('aria-hidden', 'true')
    }
  })
})
