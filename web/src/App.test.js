import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { tick } from 'svelte'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const settingsBoundary = vi.hoisted(() => ({ props: null, workspaceProps: null }))
vi.mock('./lib/NotesWorkspace.svelte', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual, default: (anchor, props) => {
    settingsBoundary.workspaceProps = props
    return actual.default(anchor, props)
  } }
})
vi.mock('./lib/settings/SettingsWorkspace.svelte', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual, default: (anchor, props) => {
    settingsBoundary.props = props
    return actual.default(anchor, props)
  } }
})

vi.mock('./lib/Editor.svelte', async () => ({
  default: (await import('./test/EditorStub.svelte')).default,
}))

vi.mock('./lib/git/git-status-poller.js', () => ({
  createGitStatusPoller: vi.fn(),
}))

vi.mock('./lib/api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    getConfig: vi.fn(),
    getGitStatus: vi.fn(),
    getGitConflicts: vi.fn(),
    completeGitConflict: vi.fn(),
    abortGitConflict: vi.fn(),
    completeSetup: vi.fn(),
    selectDirectory: vi.fn(),
    createBase: vi.fn(),
    updateBase: vi.fn(),
    forgetBase: vi.fn(),
    switchBase: vi.fn(),
    getNote: vi.fn(),
    saveNote: vi.fn(),
    getNotes: vi.fn(),
    syncNotes: vi.fn(),
    syncGit: vi.fn(),
    resumeGit: vi.fn(),
    createNote: vi.fn(),
    renameNote: vi.fn(),
    deleteNote: vi.fn(),
    uploadAsset: vi.fn(),
  }
})

import App from './App.svelte'
import { setEditorFlush } from './test/EditorStub.svelte'
import {
  completeSetup,
  createBase,
  createNote,
  deleteNote,
  forgetBase,
  getConfig,
  getGitStatus,
  getGitConflicts,
  completeGitConflict,
  abortGitConflict,
  getNote,
  getNotes,
  renameNote,
  saveNote,
  selectDirectory,
  switchBase,
  syncNotes,
  syncGit,
  resumeGit,
  updateBase,
  uploadAsset,
} from './lib/api.js'
import { createGitStatusPoller } from './lib/git/git-status-poller.js'

const firstRunConfig = {
  base_dir: '/home/user/.igonotes/bases',
  bases: [{
    name: 'default',
    path: '/home/user/.igonotes/bases/default',
    auto_sync: false,
  }],
  current_base: 'default',
  setup_completed: false,
}

const completedConfig = {
  base_dir: '/notes',
  bases: [
    { name: 'personal', path: '/notes/personal', auto_sync: false },
    { name: 'work', path: '/srv/work', auto_sync: false },
  ],
  current_base: 'personal',
  setup_completed: true,
}

const workConfig = {
  ...completedConfig,
  current_base: 'work',
}

const apiMocks = [
  completeSetup,
  createBase,
  createNote,
  deleteNote,
  forgetBase,
  getConfig,
  getGitStatus,
  getGitConflicts,
  completeGitConflict,
  abortGitConflict,
  getNote,
  getNotes,
  renameNote,
  saveNote,
  selectDirectory,
  switchBase,
  syncNotes,
  syncGit,
  resumeGit,
  updateBase,
  uploadAsset,
]

let gitPoller
let gitPollerOptions

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function fileNode(name) {
  return {
    id: `topic/${name}`,
    name,
    type: 'file',
    parent_id: 'topic',
  }
}

function folderNode(id, children = []) {
  const parts = id.split('/')
  return {
    id,
    name: parts.at(-1),
    type: 'dir',
    parent_id: parts.slice(0, -1).join('/'),
    children,
  }
}

describe('App setup gate', () => {
  const paused = {
    base: 'personal', state: 'paused', ahead: 0, behind: 0, consecutive_failures: 5, changed_paths: [],
    repository_path: '/notes/personal', operation_id: 'persisted-pause-1', stage: 'push',
    last_attempt: '2026-09-30T12:34:56Z', last_success: '2026-09-29T10:00:00Z',
    remote_oid: '0123456789abcdef0123456789abcdef0123456789',
    error: { code: 'git_network', message: 'Сервер недоступен' },
  }

  function expectPausedAlert(status = paused) {
    const alert = screen.getByRole('alert', { name: 'Git-синхронизация приостановлена' })
    expect(alert).toHaveAttribute('aria-labelledby', 'git-paused-title')
    expect(within(alert).getByRole('heading', { name: 'Git-синхронизация приостановлена' })).toHaveAttribute('id', 'git-paused-title')
    expect(within(alert).getByText(status.error.message, { exact: true })).toBeVisible()
    expect(within(alert).getByText(`Последовательных ошибок: ${status.consecutive_failures}.`, { exact: true })).toBeVisible()
    const time = alert.querySelector('time')
    expect(time).toHaveAttribute('datetime', status.last_attempt)
    expect(time.textContent).toBe(new Date(status.last_attempt).toLocaleString('ru-RU'))
    return alert
  }

  async function openPaused() {
    vi.mocked(getConfig).mockResolvedValue({ ...completedConfig, bases: completedConfig.bases.map((base) => ({ ...base, git_url: 'https://example.test/notes.git', git_branch: 'main' })) })
    const result = render(App)
    await screen.findByText('Выберите заметку')
    await gitPollerOptions.onStatuses([paused])
    await tick()
    expectPausedAlert()
    return result
  }

  it('retains a persisted pause through poll failures and resumes only after uploads and save, then refreshes', async () => {
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    await openPaused()
    gitPollerOptions.onError(new Error('Poll failed'))
    await tick()
    expectPausedAlert()
    await userEvent.setup().click(screen.getByRole('button', { name: 'draft.md' }))
    await fireEvent.input(screen.getByLabelText('Markdown'), { target: { value: '# Dirty' } })
    const upload = deferred()
    const flush = vi.fn(() => upload.promise)
    setEditorFlush(flush)
    const resume = deferred()
    vi.mocked(resumeGit).mockReturnValue(resume.promise)
    await userEvent.setup().dblClick(screen.getByRole('button', { name: 'Повторить и возобновить' }))
    expect(flush).toHaveBeenCalledOnce()
    expect(resumeGit).not.toHaveBeenCalled()
    upload.resolve()
    await waitFor(() => expect(resumeGit).toHaveBeenCalledOnce())
    expect(resumeGit).toHaveBeenCalledWith('personal')
    expect(saveNote).toHaveBeenCalledWith(note.id, '# Dirty', 'revision-1')
    expect(saveNote.mock.invocationCallOrder[0]).toBeLessThan(resumeGit.mock.invocationCallOrder[0])
    expect(gitPoller.refresh).not.toHaveBeenCalled()
    resume.resolve({ operation_id: 'resume-1', status: 'queued', deduplicated: false })
    await waitFor(() => expect(gitPoller.refresh).toHaveBeenCalledOnce())
    expect(createGitStatusPoller).toHaveBeenCalledOnce()
    expect(getGitStatus).not.toHaveBeenCalled()
  })

  it('shows the complete persisted pause and resume lifecycle', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const strictAPI = await vi.importActual('./lib/api.js')
    async function publishStatus(status) {
      const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(new Response(
        JSON.stringify({ statuses: [status] }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ))
      let response
      try {
        response = await strictAPI.getGitStatus('personal')
      } finally {
        fetch.mockRestore()
      }
      await gitPollerOptions.onStatuses(response.statuses)
      await tick()
    }
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getConfig).mockResolvedValue({ ...completedConfig, bases: completedConfig.bases.map((base) => ({ ...base, git_url: 'https://example.test/notes.git', git_branch: 'main' })) })
    render(App)
    await screen.findByRole('button', { name: 'draft.md' })
    await publishStatus(paused)
    // The alert action navigates to the existing settings workspace.
    await user.click(within(expectPausedAlert()).getByRole('button', { name: 'Открыть настройки Git' }))
    expect(await screen.findByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Назад к заметкам' }))
    expectPausedAlert()
    await user.click(screen.getByRole('button', { name: 'draft.md' }))
    await fireEvent.input(screen.getByLabelText('Markdown'), { target: { value: '# Resume draft' } })
    const upload = deferred()
    const save = deferred()
    const resume = deferred()
    const flush = vi.fn(() => upload.promise)
    setEditorFlush(flush)
    vi.mocked(saveNote).mockReturnValue(save.promise)
    vi.mocked(resumeGit).mockReturnValue(resume.promise)
    await user.click(screen.getByRole('button', { name: 'Повторить и возобновить' }))
    expect(flush).toHaveBeenCalledOnce()
    expect(saveNote).not.toHaveBeenCalled()
    expect(resumeGit).not.toHaveBeenCalled()
    upload.resolve()
    await waitFor(() => expect(saveNote).toHaveBeenCalledWith(note.id, '# Resume draft', 'revision-1'))
    expect(resumeGit).not.toHaveBeenCalled()
    expectPausedAlert()
    save.resolve({ status: 'saved', revision: 'revision-2' })
    await waitFor(() => expect(resumeGit).toHaveBeenCalledWith('personal'))
    expectPausedAlert()
    resume.resolve({ operation_id: '44444444444444444444444444444444', status: 'queued', deduplicated: false })
    await waitFor(() => expect(gitPoller.refresh).toHaveBeenCalledOnce())
    // Acceptance alone is not proof that the operation succeeded.
    expectPausedAlert()
    const { error, ...snapshot } = paused
    await publishStatus({ ...snapshot, state: 'syncing', consecutive_failures: 0, operation_id: '44444444444444444444444444444444', stage: 'push' })
    expect(settingsBoundary.workspaceProps.gitStatus.state).toBe('syncing')
    await publishStatus({ ...snapshot, state: 'ready', consecutive_failures: 0, operation_id: '44444444444444444444444444444444', stage: 'completed', last_success: '2026-10-01T12:00:00Z' })
    expect(screen.queryByRole('alert', { name: 'Git-синхронизация приостановлена' })).not.toBeInTheDocument()
    expect(settingsBoundary.workspaceProps.gitStatus.state).toBe('ready')
    expect(settingsBoundary.workspaceProps.gitStatus.consecutive_failures).toBe(0)
    expect(createGitStatusPoller).toHaveBeenCalledOnce()
    expect(resumeGit).toHaveBeenCalledOnce()
    expect(saveNote).toHaveBeenCalledOnce()
  })

  it.each(['flush', 'API'])('retains pause and buffer on failed %s with retryable per-base error', async (stage) => {
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    await openPaused()
    await userEvent.setup().click(screen.getByRole('button', { name: 'draft.md' }))
    await fireEvent.input(screen.getByLabelText('Markdown'), { target: { value: '# Keep' } })
    if (stage === 'flush') vi.mocked(saveNote).mockRejectedValue(new Error('Save failed'))
    else vi.mocked(resumeGit).mockRejectedValue(new Error('Resume failed'))
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить и возобновить' }))
    const alert = expectPausedAlert()
    await waitFor(() => expect(within(alert).getByRole('status')).toHaveTextContent(stage === 'flush' ? 'Save failed' : 'Resume failed'))
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Keep')
    expect(gitPoller.refresh).not.toHaveBeenCalled()
    if (stage === 'flush') {
      expect(resumeGit).not.toHaveBeenCalled()
      expect(screen.getByText('Ошибка сохранения')).toBeVisible()
      vi.mocked(saveNote).mockResolvedValue({ status: 'saved', revision: 'revision-2' })
      await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить и возобновить' }))
      await waitFor(() => expect(resumeGit).toHaveBeenCalledOnce())
      expect(saveNote).toHaveBeenCalledTimes(2)
    }
  })

  it.each(['upload', 'save', 'API'])('classifies primitive %s rejection by the resume stage and preserves the draft', async (stage) => {
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    await openPaused()
    await userEvent.setup().click(screen.getByRole('button', { name: 'draft.md' }))
    await fireEvent.input(screen.getByLabelText('Markdown'), { target: { value: '# Keep primitive failure' } })
    if (stage === 'upload') setEditorFlush(() => Promise.reject('upload rejected'))
    else if (stage === 'save') vi.mocked(saveNote).mockRejectedValue('save rejected')
    else vi.mocked(resumeGit).mockRejectedValue('resume rejected')

    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить и возобновить' }))

    const alert = expectPausedAlert()
    const fallback = stage === 'API'
      ? 'Не удалось возобновить Git-синхронизацию'
      : 'Не удалось сохранить рабочую область перед возобновлением Git-синхронизации'
    await waitFor(() => expect(within(alert).getByRole('status').textContent).toBe(fallback))
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Keep primitive failure')
    expect(gitPoller.refresh).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Повторить и возобновить' })).toBeEnabled()
    if (stage !== 'API') {
      expect(resumeGit).not.toHaveBeenCalled()
      expect(screen.getByText('Ошибка сохранения')).toBeVisible()
      expect(screen.getByText('Не удалось сохранить заметку: Неизвестная ошибка', { exact: true })).toBeVisible()
    } else {
      expect(resumeGit).toHaveBeenCalledOnce()
      expect(screen.queryByText('Ошибка сохранения')).not.toBeInTheDocument()
    }
  })

  it('a pending settings-card sync gates workspace resume and rejects its parent callback without another action', async () => {
    await openPaused()
    const resume = settingsBoundary.workspaceProps.onResumeGit
    const workStatus = { ...paused, base: 'work', repository_path: '/srv/work', state: 'ready' }
    await gitPollerOptions.onStatuses([paused, workStatus])
    await userEvent.setup().click(screen.getByRole('button', { name: 'Открыть настройки' }))
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Git-синхронизация' }))
    const workCard = screen.getByRole('article', { name: 'Git для базы work' })
    const pending = deferred()
    vi.mocked(syncGit).mockReturnValue(pending.promise)
    await userEvent.setup().click(within(workCard).getByRole('button', { name: 'Синхронизировать сейчас' }))
    await waitFor(() => expect(syncGit).toHaveBeenCalledWith('work'))
    expect(screen.getByRole('button', { name: 'Назад к заметкам' })).toBeDisabled()
    await resume()
    expect(resumeGit).not.toHaveBeenCalled()
    expect(syncGit).toHaveBeenCalledOnce()
    expect(gitPoller.refresh).not.toHaveBeenCalled()

    // Navigate at the parent boundary to verify the workspace sees the same global lock.
    settingsBoundary.props.onBack()
    await tick()
    expectPausedAlert()
    expect(screen.getByRole('button', { name: 'Повторить и возобновить' })).toBeDisabled()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить и возобновить' }))
    expect(resumeGit).not.toHaveBeenCalled()
    pending.resolve({ operation_id: 'settings-sync-1', status: 'queued', deduplicated: false })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Повторить и возобновить' })).toBeEnabled())
    expect(gitPoller.refresh).toHaveBeenCalledOnce()
  })

  it('a pending resume disables every settings manual-sync card and rejects settings sync callbacks', async () => {
    await openPaused()
    const resume = settingsBoundary.workspaceProps.onResumeGit
    await userEvent.setup().click(screen.getByRole('button', { name: 'Открыть настройки' }))
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Git-синхронизация' }))
    await gitPollerOptions.onStatuses([
      { ...paused, state: 'ready' },
      { ...paused, base: 'work', repository_path: '/srv/work', state: 'ready' },
    ])
    await tick()
    const cards = completedConfig.bases.map((base) => screen.getByRole('article', { name: `Git для базы ${base.name}` }))
    for (const card of cards) expect(within(card).getByRole('button', { name: 'Синхронизировать сейчас' })).toBeEnabled()
    const pending = deferred()
    vi.mocked(resumeGit).mockReturnValue(pending.promise)
    const recovery = resume()
    await waitFor(() => expect(resumeGit).toHaveBeenCalledWith('personal'))
    for (const card of cards) {
      expect(card).toHaveAttribute('aria-busy', 'true')
      const sync = within(card).getByRole('button', { name: 'Синхронизировать сейчас' })
      expect(sync).toBeDisabled()
      await userEvent.setup().click(sync)
    }
    for (const base of completedConfig.bases) await settingsBoundary.props.onGitSync(base.name)
    expect(syncGit).not.toHaveBeenCalled()
    expect(resumeGit).toHaveBeenCalledOnce()
    expect(gitPoller.refresh).not.toHaveBeenCalled()
    pending.resolve({ operation_id: 'resume-1', status: 'queued', deduplicated: false })
    await recovery
    await tick()
    for (const card of cards) expect(within(card).getByRole('button', { name: 'Синхронизировать сейчас' })).toBeEnabled()
    expect(gitPoller.refresh).toHaveBeenCalledOnce()
  })

  it.each(['resume', 'manual'])('shares the action lock when %s starts first', async (first) => {
    await openPaused()
    if (first === 'manual') {
      await gitPollerOptions.onStatuses([{ ...paused, state: 'ready' }])
      await tick()
    }
    const operation = deferred()
    vi.mocked(resumeGit).mockReturnValue(operation.promise)
    vi.mocked(syncGit).mockReturnValue(operation.promise)
    await userEvent.setup().click(screen.getByRole('button', { name: /Открыть детали Git:/ }))
    const manual = screen.getByRole('button', { name: 'Синхронизировать Git' })
    await userEvent.setup().click(first === 'resume' ? screen.getByRole('button', { name: 'Повторить и возобновить' }) : manual)
    await waitFor(() => expect(first === 'resume' ? resumeGit : syncGit).toHaveBeenCalledOnce())
    if (first === 'manual') {
      await gitPollerOptions.onStatuses([paused])
      await tick()
    }
    const resume = screen.getByRole('button', { name: 'Повторить и возобновить' })
    expect(resume).toBeDisabled()
    expect(manual).toBeDisabled()
    await userEvent.setup().click(first === 'resume' ? manual : resume)
    expect(first === 'resume' ? syncGit : resumeGit).not.toHaveBeenCalled()
    operation.resolve({ operation_id: 'op', status: 'queued', deduplicated: false })
    await waitFor(() => expect(resume).toBeEnabled())
  })

  it.each(['resolve', 'reject'])('ignores late resume %s after a base change and keeps global actions busy until settlement', async (settlement) => {
    await openPaused()
    const pending = deferred()
    vi.mocked(resumeGit).mockReturnValue(pending.promise)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить и возобновить' }))
    await waitFor(() => expect(resumeGit).toHaveBeenCalledOnce())
    await userEvent.setup().click(screen.getByRole('button', { name: 'Открыть настройки' }))
    vi.mocked(switchBase).mockResolvedValue({ ...workConfig, bases: workConfig.bases.map((base) => ({ ...base, git_url: 'https://example.test/notes.git', git_branch: 'main' })) })
    // Exercise a late external transition at the parent boundary; the settings UI remains locked.
    expect(within(screen.getByRole('article', { name: 'База work' })).getByRole('button', { name: 'Открыть' })).toBeDisabled()
    await settingsBoundary.props.onSwitch('work')
    await screen.findByText('Выберите заметку')
    await gitPollerOptions.onStatuses([{ ...paused, base: 'work', repository_path: '/srv/work' }])
    await tick()
    expect(screen.getByRole('button', { name: 'Повторить и возобновить' })).toBeDisabled()
    if (settlement === 'resolve') pending.resolve({ operation_id: 'op', status: 'queued', deduplicated: false })
    else pending.reject(new Error('Old base error'))
    await pending.promise.catch(() => {})
    await waitFor(() => expect(screen.getByRole('button', { name: 'Повторить и возобновить' })).toBeEnabled())
    expect(gitPoller.refresh).not.toHaveBeenCalled()
    expect(screen.queryByText('Old base error')).not.toBeInTheDocument()
  })

  it.each([['flush', 'resolve'], ['flush', 'reject'], ['resume', 'resolve'], ['resume', 'reject']])('ignores %s %s settlement after unmount', async (stage, settlement) => {
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    const { unmount } = await openPaused()
    await userEvent.setup().click(screen.getByRole('button', { name: 'draft.md' }))
    const pending = deferred()
    if (stage === 'flush') setEditorFlush(() => pending.promise)
    else vi.mocked(resumeGit).mockReturnValue(pending.promise)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить и возобновить' }))
    unmount()
    if (settlement === 'resolve') pending.resolve({ operation_id: 'op', status: 'queued', deduplicated: false })
    else pending.reject(new Error('Late failure'))
    await pending.promise.catch(() => {})
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(gitPoller.refresh).not.toHaveBeenCalled()
    if (stage === 'flush') expect(resumeGit).not.toHaveBeenCalled()
  })

  it.each(['resolve', 'reject'])('does not resume or publish an old flush %s after the active base changes', async (settlement) => {
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    await openPaused()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Открыть настройки' }))
    const switchActive = settingsBoundary.props.onSwitch
    await userEvent.setup().click(screen.getByRole('button', { name: 'Назад к заметкам' }))
    await userEvent.setup().click(screen.getByRole('button', { name: 'draft.md' }))
    const pending = deferred()
    setEditorFlush(() => pending.promise)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Повторить и возобновить' }))
    vi.mocked(switchBase).mockResolvedValue(workConfig)
    await switchActive('work')
    await gitPollerOptions.onStatuses([{ ...paused, base: 'work', repository_path: '/srv/work' }])
    await tick()
    expect(screen.getByRole('button', { name: 'Повторить и возобновить' })).toBeDisabled()
    if (settlement === 'resolve') pending.resolve()
    else pending.reject(new Error('Old upload error'))
    await pending.promise.catch(() => {})
    await waitFor(() => expect(screen.getByRole('button', { name: 'Повторить и возобновить' })).toBeEnabled())
    expect(resumeGit).not.toHaveBeenCalled()
    expect(gitPoller.refresh).not.toHaveBeenCalled()
    expect(screen.queryByText('Old upload error')).not.toBeInTheDocument()
    expect(screen.queryByText('Ошибка сохранения')).not.toBeInTheDocument()
  })
  beforeEach(() => {
    setEditorFlush()
    for (const mock of apiMocks) vi.mocked(mock).mockReset()
    vi.mocked(getConfig).mockResolvedValue(completedConfig)
    vi.mocked(getGitStatus).mockResolvedValue({ statuses: [] })
    vi.mocked(getGitConflicts).mockResolvedValue({
      base: 'personal', operation_id: 'conflict-1', head_oid: 'head', merge_head_oid: 'merge', can_complete: true, conflicts: [],
    })
    vi.mocked(completeGitConflict).mockResolvedValue({ operation_id: 'complete-1', status: 'queued', deduplicated: false })
    vi.mocked(abortGitConflict).mockResolvedValue({ operation_id: 'abort-1', status: 'queued', deduplicated: false })
    vi.mocked(completeSetup).mockResolvedValue(completedConfig)
    vi.mocked(selectDirectory).mockResolvedValue(null)
    vi.mocked(createBase).mockResolvedValue(completedConfig)
    vi.mocked(updateBase).mockResolvedValue(completedConfig)
    vi.mocked(forgetBase).mockResolvedValue(completedConfig)
    vi.mocked(switchBase).mockResolvedValue(completedConfig)
    vi.mocked(getNote).mockResolvedValue({ content: '', revision: 'revision-1' })
    vi.mocked(saveNote).mockResolvedValue({ status: 'saved', revision: 'revision-2' })
    vi.mocked(getNotes).mockResolvedValue([])
    vi.mocked(syncNotes).mockResolvedValue(null)
    vi.mocked(syncGit).mockResolvedValue({ operation_id: 'sync-1', status: 'queued', deduplicated: false })
    vi.mocked(createNote).mockResolvedValue(null)
    vi.mocked(renameNote).mockResolvedValue(null)
    vi.mocked(deleteNote).mockResolvedValue(null)
    vi.mocked(uploadAsset).mockResolvedValue({ path: '' })
    gitPoller = {
      start: vi.fn(),
      refresh: vi.fn().mockResolvedValue([]),
      stop: vi.fn(),
    }
    gitPollerOptions = null
    vi.mocked(createGitStatusPoller).mockReset().mockImplementation((options) => {
      gitPollerOptions = options
      return gitPoller
    })
  })

  it('blocks the notes workspace while first-run configuration is loading', async () => {
    const request = deferred()
    vi.mocked(getConfig).mockReturnValue(request.promise)

    render(App)

    const loading = screen.getByRole('status')
    expect(loading).toContainElement(
      screen.getByRole('heading', { name: 'Загрузка настроек...' }),
    )
    expect(screen.queryByRole('heading', { name: 'База заметок' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Markdown')).not.toBeInTheDocument()

    request.resolve(firstRunConfig)

    expect(await screen.findByRole('heading', { name: 'Настройте первую базу' })).toBeVisible()
    expect(screen.queryByRole('heading', { name: 'База заметок' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Markdown')).not.toBeInTheDocument()
    expect(getNotes).not.toHaveBeenCalled()
  })

  it('opens the editor for a completed configuration and shows the active base path', async () => {
    render(App)

    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    await waitFor(() => expect(getNotes).toHaveBeenCalledOnce())
    expect(screen.getByTitle('Текущая база заметок')).toHaveTextContent(/^\/notes\/personal$/)
  })

  it('shows a blocking configuration error and retries the application load', async () => {
    const user = userEvent.setup()
    vi.mocked(getConfig)
      .mockRejectedValueOnce(new Error('Настройки недоступны'))
      .mockResolvedValueOnce(completedConfig)

    render(App)

    expect(await screen.findByRole('alert')).toHaveTextContent(/^Настройки недоступны$/)
    expect(screen.queryByRole('heading', { name: 'Настройте первую базу' })).not.toBeInTheDocument()
    expect(screen.queryByText('Выберите заметку')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Повторить' }))

    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    expect(getConfig).toHaveBeenCalledTimes(2)
  })

  it('completes the actual setup wizard before mounting the notes workspace', async () => {
    const user = userEvent.setup()
    const savedConfig = {
      base_dir: '/home/user/notes',
      bases: [{ name: 'work', path: '/home/user/notes/work', auto_sync: false }],
      current_base: 'work',
      setup_completed: true,
    }
    vi.mocked(getConfig).mockResolvedValue(firstRunConfig)
    vi.mocked(completeSetup).mockResolvedValue(savedConfig)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'Создать новую' }))
    await user.type(screen.getByLabelText('Имя базы'), 'work')
    await user.type(screen.getByLabelText('Родительский каталог'), '/home/user/notes')
    await user.click(screen.getByRole('button', { name: 'Продолжить' }))
    await user.click(await screen.findByRole('button', { name: 'Завершить настройку' }))

    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    expect(screen.getByTitle('Текущая база заметок')).toHaveTextContent(/^\/home\/user\/notes\/work$/)
    expect(completeSetup).toHaveBeenCalledOnce()
    expect(completeSetup).toHaveBeenCalledWith({
      mode: 'create',
      name: 'work',
      path: '/home/user/notes',
    })
    await waitFor(() => expect(getNotes).toHaveBeenCalledOnce())
  })

  it('loads and manually saves a note through the API wrappers', async () => {
    const user = userEvent.setup()
    const note = fileNode('encoded name.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Loaded', revision: 'revision-1' })
    vi.mocked(saveNote).mockResolvedValue({ status: 'saved', revision: 'revision-2' })

    render(App)
    await user.click(await screen.findByRole('button', { name: 'encoded name.md' }))

    expect(getNote).toHaveBeenCalledOnce()
    expect(getNote).toHaveBeenCalledWith(note.id)
    expect(await screen.findByLabelText('Markdown')).toHaveValue('# Loaded')

    await user.click(screen.getByRole('button', { name: 'Сохранить' }))

    expect(saveNote).toHaveBeenCalledOnce()
    expect(saveNote).toHaveBeenCalledWith(note.id, '# Loaded', 'revision-1')
  })

  it('flushes same-note edits without refetching stale content', async () => {
    const user = userEvent.setup()
    const note = fileNode('a.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote)
      .mockResolvedValueOnce({ content: '# A', revision: 'revision-1' })
      .mockResolvedValueOnce({ content: '# Stale A', revision: 'revision-2' })

    render(App)
    await user.click(await screen.findByRole('button', { name: 'a.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.clear(textarea)
    await user.type(textarea, '# A latest')
    await user.click(screen.getByRole('button', { name: 'a.md' }))

    await waitFor(() => expect(saveNote).toHaveBeenCalledWith(note.id, '# A latest', 'revision-1'))
    await tick()
    expect(getNote).toHaveBeenCalledOnce()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# A latest')
  })

  it('flushes changed markdown before opening settings and cancels the debounce save', async () => {
    const initialUser = userEvent.setup()
    const note = fileNode('draft.md')
    const saveRequest = deferred()
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(saveNote).mockReturnValue(saveRequest.promise)

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.clear(textarea)
      await user.type(textarea, '# Changed')
      await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))

      expect(saveNote).toHaveBeenCalledOnce()
      expect(saveNote).toHaveBeenCalledWith(note.id, '# Changed', 'revision-1')
      expect(screen.queryByRole('heading', { name: 'Базы заметок' })).not.toBeInTheDocument()

      saveRequest.resolve({ status: 'saved', revision: 'revision-2' })
      await saveRequest.promise
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(screen.getByRole('heading', { name: 'Базы заметок' })).toBeVisible()
      await vi.advanceTimersByTimeAsync(2000)
      expect(saveNote).toHaveBeenCalledOnce()
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('keeps the editor open and reports a failed settings flush', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(saveNote).mockRejectedValue(new Error('Диск недоступен'))

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.clear(textarea)
    await user.type(textarea, '# Changed')
    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      /^Не удалось сохранить заметку: Диск недоступен$/,
    )
    expect(screen.getByText('Ошибка сохранения')).toBeVisible()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Changed')
    expect(screen.queryByRole('heading', { name: 'Базы заметок' })).not.toBeInTheDocument()
  })

  it('flushes before switching bases and remounts an empty editor workspace', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(switchBase).mockResolvedValue(workConfig)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.clear(textarea)
    await user.type(textarea, '# Work in progress')
    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))
    const workCard = screen.getByRole('article', { name: 'База work' })
    await user.click(within(workCard).getByRole('button', { name: 'Открыть' }))

    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    expect(saveNote).toHaveBeenCalledWith(note.id, '# Work in progress', 'revision-1')
    expect(switchBase).toHaveBeenCalledWith('work')
    expect(saveNote.mock.invocationCallOrder[0]).toBeLessThan(switchBase.mock.invocationCallOrder[0])
    expect(screen.queryByLabelText('Markdown')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Сохранить' })).toBeDisabled()
    expect(screen.getByTitle('Текущая база заметок')).toHaveTextContent(/^\/srv\/work$/)
    await waitFor(() => expect(getNotes).toHaveBeenCalledTimes(2))
  })

  it('keeps settings and the previous config when switching bases fails', async () => {
    const user = userEvent.setup()
    vi.mocked(switchBase).mockRejectedValue(new Error('База занята'))

    render(App)
    await screen.findByText('Выберите заметку')
    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))
    const workCard = screen.getByRole('article', { name: 'База work' })
    await user.click(within(workCard).getByRole('button', { name: 'Открыть' }))

    expect(await within(workCard).findByRole('alert')).toHaveTextContent(/^База занята$/)
    expect(screen.getByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    expect(screen.getByRole('article', { name: 'База personal' })).toHaveTextContent('Текущая')

    await user.click(screen.getByRole('button', { name: 'Назад к заметкам' }))
    expect(await screen.findByTitle('Текущая база заметок')).toHaveTextContent(/^\/notes\/personal$/)
  })

  it('resets the active note when settings changes the active base path', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const movedConfig = {
      ...completedConfig,
      bases: [
        { ...completedConfig.bases[0], path: '/mnt/personal' },
        completedConfig.bases[1],
      ],
    }
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(updateBase).mockResolvedValue(movedConfig)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    await screen.findByLabelText('Markdown')
    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))
    const personalCard = screen.getByRole('article', { name: 'База personal' })
    await user.click(within(personalCard).getByRole('button', { name: 'Изменить' }))
    const path = screen.getByLabelText('Каталог существующей базы')
    await user.clear(path)
    await user.type(path, '/mnt/personal')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))

    expect(updateBase).toHaveBeenCalledWith('personal', {
      name: 'personal',
      path: '/mnt/personal',
    })
    expect(await screen.findByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    expect(screen.getByRole('article', { name: 'База personal' })).toHaveTextContent('/mnt/personal')

    await user.click(screen.getByRole('button', { name: 'Назад к заметкам' }))
    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    expect(screen.queryByLabelText('Markdown')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Сохранить' })).toBeDisabled()
    expect(screen.getByTitle('Текущая база заметок')).toHaveTextContent(/^\/mnt\/personal$/)
  })

  it('debounces changed markdown for exactly two seconds', async () => {
    const initialUser = userEvent.setup()
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.clear(textarea)
      await user.type(textarea, '# Debounced')

      expect(saveNote).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(1999)
      expect(saveNote).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(1)
      expect(saveNote).toHaveBeenCalledOnce()
      expect(saveNote).toHaveBeenCalledWith(note.id, '# Debounced', 'revision-1')
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('serializes a newer edit behind a pending save and flushes it only once', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const firstSave = deferred()
    const secondSave = deferred()
    let inFlight = 0
    let maxInFlight = 0
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(saveNote).mockImplementation(() => {
      inFlight += 1
      maxInFlight = Math.max(maxInFlight, inFlight)
      const request = saveNote.mock.calls.length === 1 ? firstSave : secondSave
      return request.promise.finally(() => {
        inFlight -= 1
      })
    })

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.clear(textarea)
    await user.type(textarea, '# First')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    expect(saveNote).toHaveBeenCalledTimes(1)

    await user.clear(textarea)
    await user.type(textarea, '# Latest')
    expect(saveNote).toHaveBeenCalledTimes(1)
    expect(maxInFlight).toBe(1)

    firstSave.resolve({ status: 'saved', revision: 'revision-2' })
    await waitFor(() => expect(saveNote).toHaveBeenCalledTimes(2))
    expect(saveNote).toHaveBeenNthCalledWith(1, note.id, '# First', 'revision-1')
    expect(saveNote).toHaveBeenNthCalledWith(2, note.id, '# Latest', 'revision-2')
    expect(maxInFlight).toBe(1)

    secondSave.resolve({ status: 'saved', revision: 'revision-3' })
    await waitFor(() => expect(screen.getByText('Сохранено')).toBeVisible())
    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))

    expect(await screen.findByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    expect(saveNote).toHaveBeenCalledTimes(2)
  })

  it('saves the active note before loading another note and preserves its debounce', async () => {
    const initialUser = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const saveA = deferred()
    let inFlight = 0
    let maxInFlight = 0
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB])
    vi.mocked(getNote).mockImplementation(async (id) => ({
      content: id === noteA.id ? '# A' : '# B',
      revision: 'revision-1',
    }))
    vi.mocked(saveNote).mockImplementation(() => {
      inFlight += 1
      maxInFlight = Math.max(maxInFlight, inFlight)
      const request = saveNote.mock.calls.length === 1
        ? saveA.promise
        : Promise.resolve({ status: 'saved', revision: 'revision-3' })
      return request.finally(() => {
        inFlight -= 1
      })
    })

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'a.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.clear(textarea)
      await user.type(textarea, '# A changed')
      await vi.advanceTimersByTimeAsync(2000)
      expect(saveNote).toHaveBeenCalledWith(noteA.id, '# A changed', 'revision-1')

      await user.click(screen.getByRole('button', { name: 'b.md' }))
      expect(getNote).not.toHaveBeenCalledWith(noteB.id)
      expect(screen.getByLabelText('Markdown')).toHaveValue('# A changed')

      saveA.resolve({ status: 'saved', revision: 'revision-2' })
      await saveA.promise
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(getNote).toHaveBeenCalledWith(noteB.id)
      expect(screen.getByLabelText('Markdown')).toHaveValue('# B')
      await user.clear(screen.getByLabelText('Markdown'))
      await user.type(screen.getByLabelText('Markdown'), '# B latest')
      await vi.advanceTimersByTimeAsync(2000)

      expect(saveNote).toHaveBeenNthCalledWith(2, noteB.id, '# B latest', 'revision-1')
      expect(maxInFlight).toBe(1)
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('locks the notes workspace while a note transition is pending', async () => {
    const user = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const loadB = deferred()
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB])
    vi.mocked(getNote).mockImplementation((id) => (
      id === noteA.id ? Promise.resolve({ content: '# A', revision: 'revision-1' }) : loadB.promise
    ))

    const { container } = render(App)
    await user.click(await screen.findByRole('button', { name: 'a.md' }))
    await screen.findByLabelText('Markdown')
    await user.click(screen.getByRole('button', { name: 'b.md' }))
    await waitFor(() => expect(getNote).toHaveBeenCalledWith(noteB.id))
    const workspace = container.firstElementChild.firstElementChild

    expect(workspace).toHaveProperty('inert', true)
    expect(workspace).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByLabelText('Markdown')).toHaveValue('# A')

    loadB.resolve({ content: '# B', revision: 'revision-1' })
    await loadB.promise
    await waitFor(() => expect(screen.getByLabelText('Markdown')).toHaveValue('# B'))

    expect(workspace).toHaveProperty('inert', false)
    expect(workspace).toHaveAttribute('aria-busy', 'false')
  })

  it('keeps the active note when its save fails during note navigation', async () => {
    const initialUser = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const saveA = deferred()
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB])
    vi.mocked(getNote).mockImplementation(async (id) => ({
      content: id === noteA.id ? '# A' : '# B',
      revision: 'revision-1',
    }))
    vi.mocked(saveNote).mockReturnValue(saveA.promise)

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'a.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.clear(textarea)
      await user.type(textarea, '# A changed')
      await vi.advanceTimersByTimeAsync(2000)
      await user.click(screen.getByRole('button', { name: 'b.md' }))

      expect(getNote).not.toHaveBeenCalledWith(noteB.id)
      saveA.reject(new Error('Диск недоступен'))
      await saveA.promise.catch(() => {})
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(getNote).not.toHaveBeenCalledWith(noteB.id)
      expect(screen.getByLabelText('Markdown')).toHaveValue('# A changed')
      expect(within(screen.getByRole('main')).getByText('a.md')).toBeVisible()
      expect(screen.getByRole('alert')).toHaveTextContent(
        /^Не удалось сохранить заметку: Диск недоступен$/,
      )
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('loads only the latest rapidly selected note after a pending save', async () => {
    const initialUser = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const noteC = fileNode('c.md')
    const saveA = deferred()
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB, noteC])
    vi.mocked(getNote).mockImplementation(async (id) => ({
      content: id === noteA.id ? '# A' : id === noteB.id ? '# B' : '# C',
      revision: 'revision-1',
    }))
    vi.mocked(saveNote).mockReturnValue(saveA.promise)

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'a.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.clear(textarea)
      await user.type(textarea, '# A changed')
      await vi.advanceTimersByTimeAsync(2000)
      await user.click(screen.getByRole('button', { name: 'b.md' }))
      await user.click(screen.getByRole('button', { name: 'c.md' }))

      expect(getNote).not.toHaveBeenCalledWith(noteB.id)
      expect(getNote).not.toHaveBeenCalledWith(noteC.id)
      saveA.resolve({ status: 'saved', revision: 'revision-2' })
      await saveA.promise
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(getNote).not.toHaveBeenCalledWith(noteB.id)
      expect(getNote).toHaveBeenCalledWith(noteC.id)
      expect(screen.getByLabelText('Markdown')).toHaveValue('# C')
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('saves edits made while the next note is loading before applying its response', async () => {
    const initialUser = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const loadB = deferred()
    const saveA = deferred()
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB])
    vi.mocked(getNote).mockImplementation((id) => (
      id === noteA.id ? Promise.resolve({ content: '# A', revision: 'revision-1' }) : loadB.promise
    ))
    vi.mocked(saveNote).mockReturnValue(saveA.promise)

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'a.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.click(screen.getByRole('button', { name: 'b.md' }))
      expect(getNote).toHaveBeenCalledWith(noteB.id)
      await user.clear(textarea)
      await user.type(textarea, '# A latest')
      expect(saveNote).not.toHaveBeenCalled()

      loadB.resolve({ content: '# B', revision: 'revision-1' })
      await loadB.promise
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(saveNote).toHaveBeenCalledOnce()
      expect(saveNote).toHaveBeenCalledWith(noteA.id, '# A latest', 'revision-1')
      expect(screen.getByLabelText('Markdown')).toHaveValue('# A latest')

      saveA.resolve({ status: 'saved', revision: 'revision-2' })
      await saveA.promise
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(screen.getByLabelText('Markdown')).toHaveValue('# B')
      expect(saveNote).toHaveBeenCalledOnce()
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('keeps late edits when their post-load save fails', async () => {
    const initialUser = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const loadB = deferred()
    const saveA = deferred()
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB])
    vi.mocked(getNote).mockImplementation((id) => (
      id === noteA.id ? Promise.resolve({ content: '# A', revision: 'revision-1' }) : loadB.promise
    ))
    vi.mocked(saveNote).mockReturnValue(saveA.promise)

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'a.md' }))
    const textarea = await screen.findByLabelText('Markdown')

    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.click(screen.getByRole('button', { name: 'b.md' }))
      await user.clear(textarea)
      await user.type(textarea, '# A latest')
      loadB.resolve({ content: '# B', revision: 'revision-1' })
      await loadB.promise
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(getNote).toHaveBeenCalledWith(noteB.id)
      expect(saveNote).toHaveBeenCalledWith(noteA.id, '# A latest', 'revision-1')
      expect(screen.getByLabelText('Markdown')).toHaveValue('# A latest')

      saveA.reject(new Error('Диск недоступен'))
      await saveA.promise.catch(() => {})
      await vi.advanceTimersByTimeAsync(0)
      await tick()

      expect(within(screen.getByRole('main')).getByText('a.md')).toBeVisible()
      expect(screen.getByLabelText('Markdown')).toHaveValue('# A latest')
      expect(screen.getByRole('alert')).toHaveTextContent(
        /^Не удалось сохранить заметку: Диск недоступен$/,
      )
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('waits for a late upload and its note save before applying the target note', async () => {
    const user = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const loadB = deferred()
    const lateUpload = deferred()
    const saveA = deferred()
    const flush = vi.fn()
      .mockReturnValueOnce(undefined)
      .mockReturnValueOnce(undefined)
      .mockReturnValueOnce(undefined)
      .mockReturnValueOnce(lateUpload.promise)
    setEditorFlush(flush)
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB])
    vi.mocked(getNote).mockImplementation((id) => (
      id === noteA.id ? Promise.resolve({ content: '# A', revision: 'revision-1' }) : loadB.promise
    ))
    vi.mocked(saveNote).mockReturnValue(saveA.promise)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'a.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.click(screen.getByRole('button', { name: 'b.md' }))
    await waitFor(() => expect(getNote).toHaveBeenCalledWith(noteB.id))
    expect(flush).toHaveBeenCalledTimes(3)

    loadB.resolve({ content: '# B', revision: 'revision-1' })
    await loadB.promise
    await waitFor(() => expect(flush).toHaveBeenCalledTimes(4))
    expect(screen.getByLabelText('Markdown')).toHaveValue('# A')

    await fireEvent.input(textarea, { target: { value: '# A from upload' } })
    lateUpload.resolve()
    await waitFor(() => expect(saveNote).toHaveBeenCalledWith(noteA.id, '# A from upload', 'revision-1'))
    expect(screen.getByLabelText('Markdown')).toHaveValue('# A from upload')

    saveA.resolve({ status: 'saved', revision: 'revision-2' })
    await saveA.promise
    await waitFor(() => expect(screen.getByLabelText('Markdown')).toHaveValue('# B'))
    expect(saveNote).toHaveBeenCalledOnce()
  })

  it('keeps the active note when a late upload note save fails', async () => {
    const user = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const loadB = deferred()
    const lateUpload = deferred()
    const saveA = deferred()
    const flush = vi.fn()
      .mockReturnValueOnce(undefined)
      .mockReturnValueOnce(undefined)
      .mockReturnValueOnce(undefined)
      .mockReturnValueOnce(lateUpload.promise)
    setEditorFlush(flush)
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB])
    vi.mocked(getNote).mockImplementation((id) => (
      id === noteA.id ? Promise.resolve({ content: '# A', revision: 'revision-1' }) : loadB.promise
    ))
    vi.mocked(saveNote).mockReturnValue(saveA.promise)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'a.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.click(screen.getByRole('button', { name: 'b.md' }))
    await waitFor(() => expect(getNote).toHaveBeenCalledWith(noteB.id))
    loadB.resolve({ content: '# B', revision: 'revision-1' })
    await loadB.promise
    await waitFor(() => expect(flush).toHaveBeenCalledTimes(4))

    await fireEvent.input(textarea, { target: { value: '# A from upload' } })
    lateUpload.resolve()
    await waitFor(() => expect(saveNote).toHaveBeenCalledWith(noteA.id, '# A from upload', 'revision-1'))
    saveA.reject(new Error('Диск недоступен'))
    await saveA.promise.catch(() => {})

    expect(await screen.findByRole('alert')).toHaveTextContent(
      /^Не удалось сохранить заметку: Диск недоступен$/,
    )
    expect(within(screen.getByRole('main')).getByText('a.md')).toBeVisible()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# A from upload')
  })

  it('saves a dirty active note at its old id before renaming and closes it only after rename completes', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const renameRequest = deferred()
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(renameNote).mockReturnValue(renameRequest.promise)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.clear(textarea)
    await user.type(textarea, '# Renamed draft')
    await user.click(screen.getByRole('button', { name: 'Переименовать выбранное' }))
    const dialog = screen.getByRole('dialog')
    const input = within(dialog).getByRole('textbox')
    await user.clear(input)
    await user.type(input, 'renamed.md')
    await user.click(within(dialog).getByRole('button', { name: 'Сохранить' }))

    await waitFor(() => expect(saveNote).toHaveBeenCalledWith(note.id, '# Renamed draft', 'revision-1'))
    await waitFor(() => expect(renameNote).toHaveBeenCalledWith(note.id, 'renamed.md'))
    expect(saveNote.mock.invocationCallOrder[0]).toBeLessThan(renameNote.mock.invocationCallOrder[0])
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Renamed draft')
    expect(within(dialog).getByRole('button', { name: 'Сохранить' })).toHaveAttribute('aria-busy', 'true')

    renameRequest.resolve(null)
    await renameRequest.promise

    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    expect(screen.queryByLabelText('Markdown')).not.toBeInTheDocument()
  })

  it('waits for an ancestor deletion upload and its resulting save before deleting and closing the descendant', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const topic = folderNode('topic', [note])
    const uploadRequest = deferred()
    const saveRequest = deferred()
    const flush = vi.fn()
      .mockReturnValueOnce(uploadRequest.promise)
      .mockReturnValueOnce(undefined)
    setEditorFlush(flush)
    vi.mocked(getNotes).mockResolvedValue([topic])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(saveNote).mockReturnValue(saveRequest.promise)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'topic' }))
    await user.click(screen.getByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.click(screen.getByRole('button', { name: 'topic' }))
    await user.click(screen.getByRole('button', { name: 'Удалить выбранное' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Удалить' }))

    await waitFor(() => expect(flush).toHaveBeenCalledOnce())
    expect(deleteNote).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Original')

    await fireEvent.input(textarea, { target: { value: '# Uploaded image' } })
    uploadRequest.resolve()
    await waitFor(() => expect(saveNote).toHaveBeenCalledWith(note.id, '# Uploaded image', 'revision-1'))
    expect(deleteNote).not.toHaveBeenCalled()

    saveRequest.resolve({ status: 'saved', revision: 'revision-2' })
    await saveRequest.promise
    await waitFor(() => expect(deleteNote).toHaveBeenCalledWith(topic.id))
    expect(flush).toHaveBeenCalledTimes(2)
    expect(await screen.findByText('Выберите заметку')).toBeVisible()
  })

  it.each([
    ['rename', renameNote],
    ['delete', deleteNote],
  ])('does not %s an affected note when its flush fails', async (operation, request) => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(saveNote).mockRejectedValue(new Error('Диск недоступен'))

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    const textarea = await screen.findByLabelText('Markdown')
    await user.clear(textarea)
    await user.type(textarea, '# Keep me')

    if (operation === 'rename') {
      await user.click(screen.getByRole('button', { name: 'Переименовать выбранное' }))
      const dialog = screen.getByRole('dialog')
      const input = within(dialog).getByRole('textbox')
      await user.clear(input)
      await user.type(input, 'renamed.md')
      await user.click(within(dialog).getByRole('button', { name: 'Сохранить' }))
    } else {
      await user.click(screen.getByRole('button', { name: 'Удалить выбранное' }))
      await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Удалить' }))
    }

    expect(await within(screen.getByRole('main')).findByRole('alert')).toHaveTextContent(
      /^Не удалось сохранить заметку: Диск недоступен$/,
    )
    expect(request).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Keep me')
    expect(within(screen.getByRole('main')).getByText('draft.md')).toBeVisible()
    expect(screen.getByRole('dialog')).toBeVisible()
  })

  it.each(['rename', 'delete'])('closes a clean active descendant after ancestor %s', async (operation) => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const topic = folderNode('topic', [note])
    vi.mocked(getNotes).mockResolvedValue([topic])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })

    render(App)
    await user.click(await screen.findByRole('button', { name: 'topic' }))
    await user.click(screen.getByRole('button', { name: 'draft.md' }))
    await screen.findByLabelText('Markdown')
    await user.click(screen.getByRole('button', { name: 'topic' }))

    if (operation === 'rename') {
      await user.click(screen.getByRole('button', { name: 'Переименовать выбранное' }))
      const dialog = screen.getByRole('dialog')
      const input = within(dialog).getByRole('textbox')
      await user.clear(input)
      await user.type(input, 'renamed-topic')
      await user.click(within(dialog).getByRole('button', { name: 'Сохранить' }))
      await waitFor(() => expect(renameNote).toHaveBeenCalledWith(topic.id, 'renamed-topic'))
    } else {
      await user.click(screen.getByRole('button', { name: 'Удалить выбранное' }))
      await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Удалить' }))
      await waitFor(() => expect(deleteNote).toHaveBeenCalledWith(topic.id))
    }

    expect(await screen.findByText('Выберите заметку')).toBeVisible()
    expect(screen.queryByLabelText('Markdown')).not.toBeInTheDocument()
  })

  it.each(['rename', 'delete'])('does not flush or close a segment-prefix sibling during unrelated %s', async (operation) => {
    const user = userEvent.setup()
    const note = {
      id: 'topic-archive/note.md',
      name: 'note.md',
      type: 'file',
      parent_id: 'topic-archive',
    }
    const topic = folderNode('topic')
    const archive = folderNode('topic-archive', [note])
    const flush = vi.fn()
    setEditorFlush(flush)
    vi.mocked(getNotes).mockResolvedValue([topic, archive])
    vi.mocked(getNote).mockResolvedValue({ content: '# Archive', revision: 'revision-1' })

    render(App)
    await user.click(await screen.findByRole('button', { name: 'topic-archive' }))
    await user.click(screen.getByRole('button', { name: 'note.md' }))
    await screen.findByLabelText('Markdown')
    flush.mockClear()
    vi.mocked(saveNote).mockClear()
    await user.click(screen.getByRole('button', { name: 'topic' }))

    if (operation === 'rename') {
      await user.click(screen.getByRole('button', { name: 'Переименовать выбранное' }))
      const dialog = screen.getByRole('dialog')
      const input = within(dialog).getByRole('textbox')
      await user.clear(input)
      await user.type(input, 'renamed-topic')
      await user.click(within(dialog).getByRole('button', { name: 'Сохранить' }))
      await waitFor(() => expect(renameNote).toHaveBeenCalledWith(topic.id, 'renamed-topic'))
    } else {
      await user.click(screen.getByRole('button', { name: 'Удалить выбранное' }))
      await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Удалить' }))
      await waitFor(() => expect(deleteNote).toHaveBeenCalledWith(topic.id))
    }

    expect(flush).not.toHaveBeenCalled()
    expect(saveNote).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Archive')
    expect(within(screen.getByRole('main')).getByText('note.md')).toBeVisible()
  })

  it('manually saves unchanged content and catches save errors in the UI', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const unhandled = vi.fn((event) => event.preventDefault())
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(saveNote).mockRejectedValue(new Error('Нет места'))
    window.addEventListener('unhandledrejection', unhandled)

    try {
      render(App)
      await user.click(await screen.findByRole('button', { name: 'draft.md' }))
      await screen.findByLabelText('Markdown')
      await user.click(screen.getByRole('button', { name: 'Сохранить' }))

      expect(saveNote).toHaveBeenCalledOnce()
      expect(saveNote).toHaveBeenCalledWith(note.id, '# Original', 'revision-1')
      expect(await screen.findByRole('alert')).toHaveTextContent(
        /^Не удалось сохранить заметку: Нет места$/,
      )
      expect(screen.getByText('Ошибка сохранения')).toBeVisible()
      await Promise.resolve()
      expect(unhandled).not.toHaveBeenCalled()
    } finally {
      window.removeEventListener('unhandledrejection', unhandled)
    }
  })

  it('opens settings and returns directly to the editor', async () => {
    const user = userEvent.setup()
    render(App)
    await screen.findByText('Выберите заметку')

    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))

    expect(await screen.findByRole('heading', { name: 'Базы заметок' })).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Назад к заметкам' }))
    expect(await screen.findByText('Выберите заметку')).toBeVisible()
  })

  it('does not apply a pending initial configuration after unmount', async () => {
    const request = deferred()
    vi.mocked(getConfig).mockReturnValue(request.promise)
    const { unmount } = render(App)
    expect(screen.getByRole('status')).toBeVisible()

    unmount()
    request.resolve(completedConfig)
    await request.promise
    await tick()

    expect(getNotes).not.toHaveBeenCalled()
  })

  it('polls all Git bases once, retains statuses through an error, derives the exact current base, and stops on cleanup', async () => {
    const gitConfig = {
      ...completedConfig,
      bases: completedConfig.bases.map((base) => ({
        ...base,
        git_url: `https://example.test/${base.name}.git`,
        git_branch: 'main',
      })),
    }
    vi.mocked(getConfig).mockResolvedValue(gitConfig)

    const { unmount } = render(App)

    await screen.findByText('Выберите заметку')
    expect(createGitStatusPoller).toHaveBeenCalledOnce()
    expect(gitPoller.start).toHaveBeenCalledOnce()
    await gitPollerOptions.load()
    expect(getGitStatus).toHaveBeenCalledWith()

    gitPollerOptions.onStatuses([
      { base: 'work', state: 'ready', ahead: 1, behind: 0, changed_paths: [] },
      { base: 'personal', state: 'ready', ahead: 7, behind: 2, changed_paths: [] },
    ])
    await tick()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Открыть детали Git: Есть локальные изменения' }))
    expect(screen.getByRole('region', { name: 'Детали Git' })).toHaveTextContent('7')
    expect(screen.getByRole('region', { name: 'Детали Git' })).toHaveTextContent('2')

    gitPollerOptions.onError(new Error('Git временно недоступен'))
    await tick()
    expect(screen.getByRole('button', { name: 'Открыть детали Git: Есть локальные изменения' })).toBeVisible()

    unmount()
    expect(gitPoller.stop).toHaveBeenCalledOnce()
  })

  it('flushes a dirty footer edit before syncing Git and refreshes its status', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const gitConfig = {
      ...completedConfig,
      bases: completedConfig.bases.map((base) => ({
        ...base,
        git_url: `https://example.test/${base.name}.git`,
        git_branch: 'main',
      })),
    }
    vi.mocked(getConfig).mockResolvedValue(gitConfig)
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    await user.clear(screen.getByLabelText('Markdown'))
    await user.type(screen.getByLabelText('Markdown'), '# Before sync')
    gitPollerOptions.onStatuses([{ base: 'personal', state: 'ready', ahead: 0, behind: 0, changed_paths: [] }])
    await tick()
    await user.click(screen.getByRole('button', { name: 'Открыть детали Git: Синхронизировано' }))
    await user.click(screen.getByRole('button', { name: 'Синхронизировать Git' }))

    await waitFor(() => expect(syncGit).toHaveBeenCalledWith('personal'))
    expect(saveNote).toHaveBeenCalledWith(note.id, '# Before sync', 'revision-1')
    expect(saveNote.mock.invocationCallOrder[0]).toBeLessThan(syncGit.mock.invocationCallOrder[0])
    expect(gitPoller.refresh).toHaveBeenCalledOnce()
  })

  it('does not start Git sync when the app unmounts while the footer flush is pending', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const flush = deferred()
    const flushUploads = vi.fn(() => flush.promise)
    const gitConfig = {
      ...completedConfig,
      bases: completedConfig.bases.map((base) => ({
        ...base,
        git_url: `https://example.test/${base.name}.git`,
        git_branch: 'main',
      })),
    }
    vi.mocked(getConfig).mockResolvedValue(gitConfig)
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })

    const { unmount } = render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    setEditorFlush(flushUploads)
    gitPollerOptions.onStatuses([{ base: 'personal', state: 'ready', ahead: 0, behind: 0, changed_paths: [] }])
    await tick()
    await user.click(screen.getByRole('button', { name: 'Открыть детали Git: Синхронизировано' }))
    await user.click(screen.getByRole('button', { name: 'Синхронизировать Git' }))

    expect(flushUploads).toHaveBeenCalledOnce()
    unmount()
    flush.resolve()
    await flush.promise
    await Promise.resolve()
    await Promise.resolve()
    await tick()
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(syncGit).not.toHaveBeenCalled()
  })

  it('does not sync Git when flushing a dirty footer edit fails and retains the editor buffer', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const gitConfig = {
      ...completedConfig,
      bases: completedConfig.bases.map((base) => ({
        ...base,
        git_url: `https://example.test/${base.name}.git`,
        git_branch: 'main',
      })),
    }
    vi.mocked(getConfig).mockResolvedValue(gitConfig)
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(saveNote).mockRejectedValue(new Error('Диск недоступен'))

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    await user.clear(screen.getByLabelText('Markdown'))
    await user.type(screen.getByLabelText('Markdown'), '# Keep this')
    gitPollerOptions.onStatuses([{ base: 'personal', state: 'ready', ahead: 0, behind: 0, changed_paths: [] }])
    await tick()
    await user.click(screen.getByRole('button', { name: 'Открыть детали Git: Синхронизировано' }))
    await user.click(screen.getByRole('button', { name: 'Синхронизировать Git' }))

    expect(await screen.findByText('Не удалось сохранить заметку: Диск недоступен')).toBeVisible()
    expect(syncGit).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Keep this')
  })

  it('uses the shared Git sync action from settings while the notes workspace is unmounted', async () => {
    const user = userEvent.setup()
    const gitConfig = {
      ...completedConfig,
      bases: completedConfig.bases.map((base) => ({
        ...base,
        git_url: `https://example.test/${base.name}.git`,
        git_branch: 'main',
      })),
    }
    vi.mocked(getConfig).mockResolvedValue(gitConfig)

    render(App)
    await screen.findByText('Выберите заметку')
    gitPollerOptions.onStatuses([
      { base: 'personal', state: 'ready', ahead: 0, behind: 0, changed_paths: [] },
      { base: 'work', state: 'ready', ahead: 0, behind: 0, changed_paths: [] },
    ])
    await tick()
    await user.click(await screen.findByRole('button', { name: 'Открыть настройки' }))
    await user.click(screen.getByRole('tab', { name: 'Git-синхронизация' }))
    await user.click(within(screen.getByRole('article', { name: 'Git для базы work' }))
      .getByRole('button', { name: 'Синхронизировать сейчас' }))

    await waitFor(() => expect(syncGit).toHaveBeenCalledWith('work'))
    expect(gitPoller.refresh).toHaveBeenCalledOnce()
  })

  it('allows only one Git sync across settings cards', async () => {
    const user = userEvent.setup()
    const request = deferred()
    const gitConfig = {
      ...completedConfig,
      bases: completedConfig.bases.map((base) => ({
        ...base,
        git_url: `https://example.test/${base.name}.git`,
        git_branch: 'main',
      })),
    }
    vi.mocked(getConfig).mockResolvedValue(gitConfig)
    vi.mocked(syncGit).mockReturnValue(request.promise)

    render(App)
    await screen.findByText('Выберите заметку')
    gitPollerOptions.onStatuses([
      { base: 'personal', state: 'ready', ahead: 0, behind: 0, changed_paths: [] },
      { base: 'work', state: 'ready', ahead: 0, behind: 0, changed_paths: [] },
    ])
    await tick()
    await user.click(await screen.findByRole('button', { name: 'Открыть настройки' }))
    await user.click(screen.getByRole('tab', { name: 'Git-синхронизация' }))
    const work = within(screen.getByRole('article', { name: 'Git для базы work' }))
    const personal = within(screen.getByRole('article', { name: 'Git для базы personal' }))
    await user.click(work.getByRole('button', { name: 'Синхронизировать сейчас' }))
    await user.click(personal.getByRole('button', { name: 'Синхронизировать сейчас' }))

    expect(syncGit).toHaveBeenCalledOnce()
    expect(syncGit).toHaveBeenCalledWith('work')
    request.resolve({ operation_id: 'sync-1', status: 'queued', deduplicated: false })
    await request.promise
  })

  it('preserves a 409 draft, then overwrites with the disk revision and advances the revision', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const changed = Object.assign(new Error('Заметка изменилась'), { status: 409, code: 'note_changed' })
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote)
      .mockResolvedValueOnce({ content: '# Original', revision: 'revision-1' })
      .mockResolvedValueOnce({ content: '# Disk', revision: 'revision-2' })
    vi.mocked(saveNote)
      .mockRejectedValueOnce(changed)
      .mockResolvedValueOnce({ status: 'saved', revision: 'revision-3' })
      .mockResolvedValueOnce({ status: 'saved', revision: 'revision-4' })

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    await user.clear(screen.getByLabelText('Markdown'))
    await user.type(screen.getByLabelText('Markdown'), '# Mine')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))

    expect(await screen.findByRole('dialog')).toBeVisible()
    expect(screen.getByRole('textbox', { name: 'Моя версия' })).toHaveValue('# Mine')
    expect(screen.getByRole('textbox', { name: 'Версия на диске' })).toHaveValue('# Disk')
    expect(saveNote).toHaveBeenCalledWith(note.id, '# Mine', 'revision-1')

    await user.click(screen.getByRole('button', { name: 'Оставить мою версию' }))
    await user.click(screen.getByRole('button', { name: 'Подтвердить перезапись' }))
    await waitFor(() => expect(saveNote).toHaveBeenCalledWith(note.id, '# Mine', 'revision-2'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    await user.type(screen.getByLabelText('Markdown'), '!')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    expect(saveNote).toHaveBeenLastCalledWith(note.id, '# Mine!', 'revision-3')
  })

  it('preserves edits made during a stale disk fetch and blocks navigation and Git sync', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const diskRequest = deferred()
    const changed = Object.assign(new Error('Заметка изменилась'), { status: 409, code: 'note_changed' })
    const gitConfig = {
      ...completedConfig,
      bases: completedConfig.bases.map((base) => ({ ...base, git_url: `https://example.test/${base.name}.git`, git_branch: 'main' })),
    }
    vi.mocked(getConfig).mockResolvedValue(gitConfig)
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote)
      .mockResolvedValueOnce({ content: '# Original', revision: 'revision-1' })
      .mockReturnValueOnce(diskRequest.promise)
    vi.mocked(saveNote).mockRejectedValueOnce(changed)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    await gitPollerOptions.onStatuses([{ base: 'personal', state: 'ready', ahead: 0, behind: 0, changed_paths: [] }])
    await user.click(screen.getByRole('button', { name: 'Открыть детали Git: Синхронизировано' }))
    await user.clear(screen.getByLabelText('Markdown'))
    await user.type(screen.getByLabelText('Markdown'), '# Mine')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    await waitFor(() => expect(saveNote).toHaveBeenCalledWith(note.id, '# Mine', 'revision-1'))

    await fireEvent.input(screen.getByLabelText('Markdown'), { target: { value: '# Latest during fetch' } })
    diskRequest.resolve({ content: '# Disk', revision: 'revision-2' })

    expect(await screen.findByRole('dialog')).toBeVisible()
    expect(screen.getByRole('textbox', { name: 'Моя версия' })).toHaveValue('# Latest during fetch')
    await fireEvent.click(screen.getByRole('button', { name: 'Открыть настройки' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Синхронизировать Git' }))
    await tick()

    expect(screen.queryByRole('heading', { name: 'Базы заметок' })).not.toBeInTheDocument()
    expect(syncGit).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Latest during fetch')
  })

  it('keeps edits made while a stale overwrite is in flight', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const overwriteRequest = deferred()
    const changed = Object.assign(new Error('Заметка изменилась'), { status: 409, code: 'note_changed' })
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote)
      .mockResolvedValueOnce({ content: '# Original', revision: 'revision-1' })
      .mockResolvedValueOnce({ content: '# Disk', revision: 'revision-2' })
    vi.mocked(saveNote)
      .mockRejectedValueOnce(changed)
      .mockReturnValueOnce(overwriteRequest.promise)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    await user.clear(screen.getByLabelText('Markdown'))
    await user.type(screen.getByLabelText('Markdown'), '# Mine')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Оставить мою версию' }))
    await user.click(screen.getByRole('button', { name: 'Подтвердить перезапись' }))
    await waitFor(() => expect(saveNote).toHaveBeenCalledWith(note.id, '# Mine', 'revision-2'))

    await fireEvent.input(screen.getByLabelText('Markdown'), { target: { value: '# Later edit' } })
    overwriteRequest.resolve({ status: 'saved', revision: 'revision-3' })

    expect(await screen.findByRole('dialog')).toBeVisible()
    expect(screen.getByRole('textbox', { name: 'Моя версия' })).toHaveValue('# Later edit')
    expect(screen.getByRole('textbox', { name: 'Версия на диске' })).toHaveValue('# Mine')
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Later edit')
  })

  it('retains the submitted manual merge after a repeated note_changed response', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const changed = Object.assign(new Error('Заметка изменилась'), { status: 409, code: 'note_changed' })
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote)
      .mockResolvedValueOnce({ content: '# Original', revision: 'revision-1' })
      .mockResolvedValueOnce({ content: '# Disk one', revision: 'revision-2' })
      .mockResolvedValueOnce({ content: '# Disk two', revision: 'revision-3' })
    vi.mocked(saveNote).mockRejectedValue(changed)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    await user.clear(screen.getByLabelText('Markdown'))
    await user.type(screen.getByLabelText('Markdown'), '# Mine')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Объединить вручную' }))
    const merge = screen.getByRole('textbox', { name: 'Итоговый текст' })
    await user.clear(merge)
    await user.type(merge, '# Submitted merge')
    await user.click(screen.getByRole('button', { name: 'Сохранить объединение' }))

    expect(await screen.findByRole('textbox', { name: 'Моя версия' })).toHaveValue('# Submitted merge')
    expect(screen.getByRole('textbox', { name: 'Версия на диске' })).toHaveValue('# Disk two')
  })

  it('keeps a dirty deleted note in the browser until the explicit close action', async () => {
    const user = userEvent.setup()
    const note = fileNode('deleted.md')
    const missing = Object.assign(new Error('Не найдено'), { status: 404 })
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote)
      .mockResolvedValueOnce({ content: '# Original', revision: 'revision-1' })
      .mockRejectedValueOnce(missing)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'deleted.md' }))
    await user.clear(screen.getByLabelText('Markdown'))
    await user.type(screen.getByLabelText('Markdown'), '# Browser draft')
    await gitPollerOptions.onStatuses([{
      base: 'personal', state: 'ready', repository_path: '/notes/personal', operation_id: 'sync-1',
      ahead: 0, behind: 0, changed_paths: [note.id],
    }])

    expect(await screen.findByRole('button', { name: 'Закрыть заметку' })).toBeVisible()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Browser draft')
    await user.click(screen.getByRole('button', { name: 'Закрыть заметку' }))
    expect(await screen.findByText('Выберите заметку')).toBeVisible()
  })

  it('refreshes the tree once per terminal changed-path operation and reloads a clean active note', async () => {
    const user = userEvent.setup()
    const note = fileNode('changed.md')
    const status = {
      base: 'personal', state: 'ready', repository_path: '/notes/personal', operation_id: 'sync-1',
      ahead: 0, behind: 0, changed_paths: [note.id, note.id],
    }
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote)
      .mockResolvedValueOnce({ content: '# Original', revision: 'revision-1' })
      .mockResolvedValueOnce({ content: '# Changed on disk', revision: 'revision-2' })

    render(App)
    await user.click(await screen.findByRole('button', { name: 'changed.md' }))
    await gitPollerOptions.onStatuses([status])

    expect(screen.getByLabelText('Markdown')).toHaveValue('# Changed on disk')
    expect(getNotes).toHaveBeenCalledTimes(2)
    expect(syncNotes).not.toHaveBeenCalled()
    await gitPollerOptions.onStatuses([status])
    expect(getNotes).toHaveBeenCalledTimes(2)
    expect(getNote).toHaveBeenCalledTimes(2)
  })

  it('waits for an active save before reloading a terminal changed path at its fresh revision', async () => {
    const user = userEvent.setup()
    const note = fileNode('changed.md')
    const saveRequest = deferred()
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote)
      .mockResolvedValueOnce({ content: '# Original', revision: 'revision-1' })
      .mockResolvedValueOnce({ content: '# Fresh disk', revision: 'revision-3' })
    vi.mocked(saveNote).mockReturnValue(saveRequest.promise)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'changed.md' }))
    await user.clear(screen.getByLabelText('Markdown'))
    await user.type(screen.getByLabelText('Markdown'), '# Saving')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    await waitFor(() => expect(saveNote).toHaveBeenCalledWith(note.id, '# Saving', 'revision-1'))

    const changed = gitPollerOptions.onStatuses([{
      base: 'personal', state: 'ready', repository_path: '/notes/personal', operation_id: 'sync-1',
      ahead: 0, behind: 0, changed_paths: [note.id],
    }])
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(getNotes).toHaveBeenCalledOnce()
    expect(getNote).toHaveBeenCalledOnce()

    saveRequest.resolve({ status: 'saved', revision: 'revision-2' })
    await changed

    expect(getNotes).toHaveBeenCalledTimes(2)
    expect(getNote).toHaveBeenCalledTimes(2)
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Fresh disk')
    await user.type(screen.getByLabelText('Markdown'), '!')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    expect(saveNote).toHaveBeenLastCalledWith(note.id, '# Fresh disk!', 'revision-3')
  })

  it.each([
    ['complete', 'Завершить слияние', 'complete-1'],
    ['abort', 'Подтвердить отмену', 'abort-1'],
  ])('keeps the %s conflict workspace until its matching terminal operation arrives', async (action, button, operationId) => {
    const user = userEvent.setup()
    const conflict = {
      base: 'personal', state: 'conflict', operation_id: 'conflict-1', ahead: 0, behind: 0, changed_paths: [],
    }
    render(App)
    await screen.findByText('Выберите заметку')
    await gitPollerOptions.onStatuses([conflict])
    expect(await screen.findByRole('heading', { name: 'Конфликты Git' })).toBeVisible()

    if (action === 'abort') await user.click(screen.getByRole('button', { name: 'Отменить слияние' }))
    await user.click(screen.getByRole('button', { name: button }))
    await waitFor(() => expect(action === 'complete' ? completeGitConflict : abortGitConflict).toHaveBeenCalledWith('personal'))

    await gitPollerOptions.onStatuses([{
      ...conflict, state: 'ready', operation_id: 'other-operation', repository_path: '/notes/personal', changed_paths: [],
    }])
    expect(screen.getByRole('heading', { name: 'Конфликты Git' })).toBeVisible()
    await gitPollerOptions.onStatuses([{
      ...conflict, state: 'ready', operation_id: operationId, repository_path: '/notes/personal', changed_paths: [],
    }])
    expect(await screen.findByText('Выберите заметку')).toBeVisible()
  })

  it('delegates a conflict workspace base switch to the existing safe base switch', async () => {
    const user = userEvent.setup()
    vi.mocked(switchBase).mockResolvedValue(workConfig)
    render(App)
    await screen.findByText('Выберите заметку')
    await gitPollerOptions.onStatuses([{
      base: 'personal', state: 'conflict', operation_id: 'conflict-1', ahead: 0, behind: 0, changed_paths: [],
    }])
    const target = await screen.findByRole('combobox', { name: 'База для переключения' })
    await user.selectOptions(target, 'work')
    await user.click(screen.getByRole('button', { name: 'Открыть базу' }))

    expect(switchBase).toHaveBeenCalledWith('work')
    expect(await screen.findByTitle('Текущая база заметок')).toHaveTextContent('/srv/work')
  })

  it('switches from an active conflict without flushing the retained dirty editor', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(saveNote).mockRejectedValue(new Error('Не должен вызываться'))
    vi.mocked(switchBase).mockResolvedValue(workConfig)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    await user.clear(screen.getByLabelText('Markdown'))
    await user.type(screen.getByLabelText('Markdown'), '# Retained draft')
    await gitPollerOptions.onStatuses([{
      base: 'personal', state: 'conflict', operation_id: 'conflict-1', ahead: 0, behind: 0, changed_paths: [],
    }])

    const target = await screen.findByRole('combobox', { name: 'База для переключения' })
    await user.selectOptions(target, 'work')
    await user.click(screen.getByRole('button', { name: 'Открыть базу' }))

    expect(switchBase).toHaveBeenCalledWith('work')
    expect(saveNote).not.toHaveBeenCalled()
    expect(await screen.findByTitle('Текущая база заметок')).toHaveTextContent('/srv/work')
  })

  it('restores a dirty conflict buffer after switching away and back to its base', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })
    vi.mocked(switchBase)
      .mockResolvedValueOnce(workConfig)
      .mockResolvedValueOnce(completedConfig)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    await user.clear(screen.getByLabelText('Markdown'))
    await user.type(screen.getByLabelText('Markdown'), '# Retained draft')
    await gitPollerOptions.onStatuses([{
      base: 'personal', state: 'conflict', operation_id: 'conflict-1', ahead: 0, behind: 0, changed_paths: [],
    }])
    const conflictTarget = await screen.findByRole('combobox', { name: 'База для переключения' })
    await user.selectOptions(conflictTarget, 'work')
    await user.click(screen.getByRole('button', { name: 'Открыть базу' }))
    await screen.findByText('Выберите заметку')
    await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))
    const personal = screen.getByRole('article', { name: 'База personal' })
    await user.click(within(personal).getByRole('button', { name: 'Открыть' }))

    expect(await screen.findByLabelText('Markdown')).toHaveValue('# Retained draft')
  })

  it('cancels a pending debounce when the active base enters a conflict', async () => {
    const initialUser = userEvent.setup()
    const note = fileNode('draft.md')
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote).mockResolvedValue({ content: '# Original', revision: 'revision-1' })

    render(App)
    await initialUser.click(await screen.findByRole('button', { name: 'draft.md' }))
    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    try {
      await user.clear(screen.getByLabelText('Markdown'))
      await user.type(screen.getByLabelText('Markdown'), '# Pending')
      await gitPollerOptions.onStatuses([{
        base: 'personal', state: 'conflict', operation_id: 'conflict-1', ahead: 0, behind: 0, changed_paths: [],
      }])
      await vi.advanceTimersByTimeAsync(2000)

      expect(screen.getByRole('heading', { name: 'Конфликты Git' })).toBeVisible()
      expect(saveNote).not.toHaveBeenCalled()
    } finally {
      vi.clearAllTimers()
      vi.useRealTimers()
    }
  })

  it('does not apply a changed-path reload after a newer note selection', async () => {
    const user = userEvent.setup()
    const noteA = fileNode('a.md')
    const noteB = fileNode('b.md')
    const changedRequest = deferred()
    vi.mocked(getNotes).mockResolvedValue([noteA, noteB])
    vi.mocked(getNote)
      .mockResolvedValueOnce({ content: '# A', revision: 'revision-1' })
      .mockReturnValueOnce(changedRequest.promise)
      .mockResolvedValueOnce({ content: '# B', revision: 'revision-2' })

    render(App)
    await user.click(await screen.findByRole('button', { name: 'a.md' }))
    const changed = gitPollerOptions.onStatuses([{
      base: 'personal', state: 'ready', repository_path: '/notes/personal', operation_id: 'sync-1',
      ahead: 0, behind: 0, changed_paths: [noteA.id],
    }])
    await waitFor(() => expect(getNote).toHaveBeenCalledTimes(2))
    await user.click(screen.getByRole('button', { name: 'b.md' }))
    expect(await screen.findByLabelText('Markdown')).toHaveValue('# B')

    changedRequest.resolve({ content: '# A changed', revision: 'revision-3' })
    await changed

    expect(screen.getByLabelText('Markdown')).toHaveValue('# B')
  })

  it('does not mount stale recovery over the active conflict workspace', async () => {
    const user = userEvent.setup()
    const note = fileNode('draft.md')
    const changed = Object.assign(new Error('Заметка изменилась'), { status: 409, code: 'note_changed' })
    vi.mocked(getNotes).mockResolvedValue([note])
    vi.mocked(getNote)
      .mockResolvedValueOnce({ content: '# Original', revision: 'revision-1' })
      .mockResolvedValueOnce({ content: '# Disk', revision: 'revision-2' })
    vi.mocked(saveNote).mockRejectedValueOnce(changed)

    render(App)
    await user.click(await screen.findByRole('button', { name: 'draft.md' }))
    await user.clear(screen.getByLabelText('Markdown'))
    await user.type(screen.getByLabelText('Markdown'), '# Mine')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    await screen.findByRole('dialog')
    await gitPollerOptions.onStatuses([{
      base: 'personal', state: 'conflict', operation_id: 'conflict-1', ahead: 0, behind: 0, changed_paths: [],
    }])

    expect(await screen.findByRole('heading', { name: 'Конфликты Git' })).toBeVisible()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
