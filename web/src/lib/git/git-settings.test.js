import { describe, expect, it } from 'vitest'

import {
  DEFAULT_GIT_COMMIT_TEMPLATE,
  GIT_INTERVALS,
  GIT_TEMPLATE_VARIABLES,
  buildGitConfigRequest,
  gitConfigured,
  gitStatusFor,
  presentGitStatus,
  renderGitCommitPreview,
  replaceConfigBase,
  requiredConfirmationKeys,
  validateGitDraft,
} from './git-settings.js'

const validDraft = {
  git_url: ' https://example.test/notes.git ',
  git_branch: ' main ',
  auto_sync: true,
  auto_sync_interval_minutes: 15,
  git_commit_message_template: ' Sync {{base}} ',
}

function probe(required_mutations = {}) {
  return {
    required_mutations: {
      create_repository: false,
      add_origin: false,
      replace_origin: false,
      create_branch: false,
      merge_histories: false,
      ...required_mutations,
    },
  }
}

function gitStatus(base = 'work', state = 'ready', ahead = 0) {
  return { base, state, ahead }
}

describe('Git settings helpers', () => {
  it('exports the backend-supported constants', () => {
    expect(DEFAULT_GIT_COMMIT_TEMPLATE).toBe('IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)')
    expect(GIT_INTERVALS).toEqual([5, 15, 30, 60])
    expect(GIT_TEMPLATE_VARIABLES).toEqual(['base', 'branch', 'date', 'datetime', 'count'])
  })

  it('recognizes configured bases after trimming Git values', () => {
    expect(gitConfigured({ git_url: ' https://example.test/notes.git ', git_branch: ' main ' })).toBe(true)
    expect(gitConfigured({ git_url: 'https://example.test/notes.git', git_branch: ' ' })).toBe(false)
    expect(gitConfigured(null)).toBe(false)
  })

  it.each([
    ['an empty URL', { ...validDraft, git_url: ' ' }, { git_url: 'Укажите URL Git-репозитория' }],
    ['a malformed URL', { ...validDraft, git_url: 'https://' }, { git_url: 'Укажите корректный URL Git-репозитория' }],
    ['a URL with credentials', { ...validDraft, git_url: 'https://user:secret@example.test/repo.git' }, { git_url: 'Укажите корректный URL Git-репозитория' }],
    ['a URL with a query', { ...validDraft, git_url: 'https://example.test/repo.git?token=secret' }, { git_url: 'Укажите корректный URL Git-репозитория' }],
    ['an empty branch', { ...validDraft, git_branch: ' ' }, { git_branch: 'Укажите ветку Git' }],
    ['an invalid branch', { ...validDraft, git_branch: 'feature..broken' }, { git_branch: 'Укажите корректное имя ветки Git' }],
    ['an unsupported interval', { ...validDraft, auto_sync_interval_minutes: 10 }, { auto_sync_interval_minutes: 'Выберите интервал автосинхронизации' }],
    ['a missing enabled interval', { ...validDraft, auto_sync_interval_minutes: 0 }, { auto_sync_interval_minutes: 'Выберите интервал автосинхронизации' }],
    ['a blank template', { ...validDraft, git_commit_message_template: '   ' }, { git_commit_message_template: 'Введите шаблон сообщения коммита' }],
    ['an unsupported template variable', { ...validDraft, git_commit_message_template: '{{repository}}' }, { git_commit_message_template: 'Шаблон содержит неподдерживаемую переменную' }],
    ['unbalanced template braces', { ...validDraft, git_commit_message_template: '{{base}' }, { git_commit_message_template: 'Шаблон содержит неподдерживаемую переменную' }],
  ])('validates %s', (_case, draft, expected) => {
    expect(validateGitDraft(draft)).toEqual(expected)
  })

  it('allows supported Git URLs, branch names, intervals, and templates', () => {
    for (const git_url of ['https://example.test/repo.git', 'ssh://git@example.test/repo.git', 'git@example.test:repo.git', 'file:///notes/repo']) {
      expect(validateGitDraft({ ...validDraft, git_url })).toEqual({})
    }
    expect(validateGitDraft({ ...validDraft, git_branch: 'feature/topic-1', git_commit_message_template: '{{base}} {{branch}} {{date}} {{datetime}} {{count}}' })).toEqual({})
    expect(validateGitDraft({ ...validDraft, auto_sync: false, auto_sync_interval_minutes: 0 })).toEqual({})
  })

  it('renders every commit template variable with local RFC3339 time', () => {
    const now = new Date('2026-09-10T12:34:56+03:00')
    const offset = -now.getTimezoneOffset()
    const localDateTime = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}T${String(now.getHours()).padStart(2, '0')}:${String(now.getMinutes()).padStart(2, '0')}:${String(now.getSeconds()).padStart(2, '0')}${offset === 0 ? 'Z' : `${offset < 0 ? '-' : '+'}${String(Math.floor(Math.abs(offset) / 60)).padStart(2, '0')}:${String(Math.abs(offset) % 60).padStart(2, '0')}`}`

    expect(renderGitCommitPreview('{{base}}/{{branch}} {{date}} {{datetime}} {{count}}', {
      base: 'work',
      branch: 'main',
      date: now,
      count: 3,
    })).toBe(`work/main ${localDateTime.slice(0, 10)} ${localDateTime} 3`)
    expect(renderGitCommitPreview('', { base: 'work', branch: 'main', date: now, count: 3 })).toBe(
      `IGoNotes: sync work at ${localDateTime} (3 files)`,
    )
  })

  it('returns only confirmation keys required by the probe in API order', () => {
    expect(requiredConfirmationKeys(probe({
      create_repository: true,
      add_origin: true,
      create_branch: true,
      merge_histories: true,
    }))).toEqual(['create_repository', 'create_branch', 'merge_histories'])
  })

  it('builds a trimmed request with every confirmation field', () => {
    expect(buildGitConfigRequest(validDraft, probe({ create_repository: true, replace_origin: true }), {
      create_repository: true,
      replace_origin: false,
      create_branch: true,
    })).toEqual({
      git_url: 'https://example.test/notes.git',
      git_branch: 'main',
      auto_sync: true,
      auto_sync_interval_minutes: 15,
      git_commit_message_template: 'Sync {{base}}',
      confirmations: {
        create_repository: true,
        replace_origin: false,
        create_branch: false,
        merge_histories: false,
      },
    })
  })

  it('replaces exactly one config base immutably by its name', () => {
    const config = { bases: [{ name: 'work', path: '/old' }, { name: 'Work', path: '/case-sensitive' }] }
    const updated = { name: 'work', path: '/new' }

    expect(replaceConfigBase(config, updated)).toEqual({ bases: [updated, { name: 'Work', path: '/case-sensitive' }] })
    expect(config).toEqual({ bases: [{ name: 'work', path: '/old' }, { name: 'Work', path: '/case-sensitive' }] })
  })

  it('looks up status by exact base name only', () => {
    const statuses = [gitStatus('work'), gitStatus('Work', 'error')]

    expect(gitStatusFor(statuses, 'work')).toEqual(gitStatus('work'))
    expect(gitStatusFor(statuses, 'WORK')).toBeNull()
  })

  it.each([
    [null, 'Git не настроен', 'slate', false, false],
    [gitStatus('work', 'unconfigured'), 'Git не настроен', 'slate', false, false],
    [gitStatus('work', 'initializing'), 'Выполняется', 'blue', true, false],
    [gitStatus('work', 'syncing'), 'Выполняется', 'blue', true, false],
    [gitStatus('work', 'ready', 1), 'Есть локальные изменения', 'amber', false, true],
    [gitStatus('work', 'ready'), 'Синхронизировано', 'green', false, true],
    [gitStatus('work', 'error'), 'Ошибка', 'red', false, true],
    [gitStatus('work', 'paused'), 'Приостановлено', 'amber', false, false],
    [gitStatus('work', 'conflict'), 'Конфликт', 'red', false, false],
    [gitStatus('work', 'needs_reconnect'), 'Требуется переподключение', 'amber', false, false],
    [gitStatus('work', 'other'), 'Статус неизвестен', 'slate', false, false],
  ])('presents %o as public status', (status, label, tone, busy, canSync) => {
    expect(presentGitStatus(status)).toEqual({ label, tone, busy, canSync })
  })
})
