import { render, screen } from '@testing-library/svelte'
import { describe, expect, it } from 'vitest'

import ConflictStagePanel from './ConflictStagePanel.svelte'

const textStage = {
  path: 'notes/idea.md',
  oid: 'text-oid',
  mode: '100644',
  size: 32,
  content: '# Идея',
  preview_truncated: false,
}

describe('ConflictStagePanel', () => {
  it.each([
    ['base', 'Общий предок'],
    ['local', 'На этом устройстве'],
    ['remote', 'В репозитории'],
  ])('labels %s without Git jargon', (side, label) => {
    render(ConflictStagePanel, { side, stage: textStage, contentKind: 'text' })

    expect(screen.getByRole('heading', { name: label })).toBeVisible()
    expect(screen.getByRole('region', { name: label })).toBeVisible()
    expect(screen.getByText('notes/idea.md')).toBeVisible()
    expect(screen.getByRole('textbox', { name: label })).toHaveValue('# Идея')
    expect(screen.getByRole('textbox', { name: label })).toHaveAttribute('readonly')
    expect(screen.queryByText(/ours|theirs/i)).not.toBeInTheDocument()
  })

  it('shows binary metadata but never a textbox', () => {
    render(ConflictStagePanel, {
      side: 'remote',
      contentKind: 'binary',
      stage: {
        path: 'assets/photo.png',
        oid: 'abc',
        mode: '100644',
        size: 4096,
        preview_truncated: false,
      },
    })

    expect(screen.getByText('Двоичный файл')).toBeVisible()
    expect(screen.getByText(/4 KB/)).toBeVisible()
    expect(screen.getByText(/режим 100644/)).toBeVisible()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('identifies an absent stage without presenting content', () => {
    render(ConflictStagePanel, { side: 'base', stage: null, contentKind: 'text' })

    expect(screen.getByText('Файл отсутствует на этой стороне')).toBeVisible()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('requires a whole-side or output-path decision for truncated text', () => {
    render(ConflictStagePanel, {
      side: 'local',
      contentKind: 'text',
      stage: { ...textStage, size: 1_048_577, preview_truncated: true },
    })

    expect(screen.getByText('Текст больше 1 MiB; выберите сторону целиком или итоговый путь.')).toBeVisible()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('keeps the content area responsive and scrollable', () => {
    const { container } = render(ConflictStagePanel, {
      side: 'local',
      stage: textStage,
      contentKind: 'text',
    })

    expect(container.querySelector('section')).toHaveClass('min-w-0')
    expect(screen.getByRole('textbox')).toHaveClass('min-h-40', 'w-full', 'resize-y')
  })
})
