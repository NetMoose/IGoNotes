# Git Synchronization Plan 6: Conflict Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add revision-safe note editing, incoming Git change handling, stale-note recovery, and a complete accessible conflict-resolution workspace without losing browser buffers or silently choosing a Git side.

**Architecture:** Keep `App.svelte` as the single screen and note-save state machine, consume the Git status polling established by Plan 5, and deduplicate each terminal changed path by repository identity, operation, and path before refreshing the tree or active note. Extract the existing modal focus/inert machinery into a generic `DialogShell`, then build stale-note and conflict components over the exact Plan 2 and Plan 4 REST contracts; all `App.svelte` integration remains serial after independently owned components are merged.

**Tech Stack:** Svelte 5.56 runes and snippets, JavaScript, Vite 8, Tailwind CSS 4, CodeMirror 6, Vitest 4.1, jsdom 30, Testing Library for Svelte, Node.js 24.

---

## Mandatory Plan-Start Gate

Run this gate before reading task ownership, dispatching a worker, or editing implementation files:

```bash
node -e "const major = Number(process.versions.node.split('.')[0]); if (major !== 24) { console.error('Node.js 24 is required; found ' + process.version); process.exit(1) }"
npm --prefix web ci
```

Expected: the first command exits `0` and prints nothing; `npm ci` exits `0` from the lockfile. If either command fails, stop. Do not run tests, dispatch workers, or change dependencies to accommodate another Node major.

## Scope and Dependencies

This is Plan 6 of `docs/superpowers/specs/2026-09-01-git-synchronization-design.md`. Execute it only after Plans 1-5 have landed in the implementation worktree and the frontend has been installed with Node.js 24.

Required dependencies:

- Plan 2, `docs/superpowers/plans/2026-09-01-git-worktree-safety.md`, supplies `GET /api/note` responses `{id, content, revision}`, revision-aware `POST /api/save`, successful `{status, revision}`, and `409 note_changed`.
- Plan 3, `docs/superpowers/plans/2026-09-01-git-manual-sync.md`, supplies terminal Git status, operation IDs, sorted/deduplicated `changed_paths`, manual sync, and operation deduplication.
- Plan 4, `docs/superpowers/plans/2026-09-01-git-conflict-backend.md`, supplies conflict list/resolve/complete/abort DTOs and exact conflict identity checks.
- Plan 5 Git settings frontend must have landed `getGitStatus`, Git status polling, manual-sync controls, the active-base status indicator, and the working Git settings section. This plan consumes that polling and does not create a second timer.
- The existing setup/settings frontend supplies `App.svelte` screen routing, serialized `flushWorkspace()`, `NotesWorkspace`, `Sidebar`, `Modal`, and component tests.

Plan 5's required frontend seam is normative for this plan:

```js
// web/src/lib/api.js
export function getGitStatus(base = '')
export function syncGit(base)

// web/src/App.svelte
let gitStatuses = $state([])
let activeGitStatus = $derived(
  gitStatuses.find((status) => status.base === config?.current_base) ?? null,
)

// The one Plan 5 polling success path awaits this function.
async function applyGitStatuses(statuses) {
  gitStatuses = Array.isArray(statuses) ? statuses : []
}

// web/src/lib/git/git-status-poller.js
// load, onStatuses, and onError may be async. A cycle awaits each callback,
// catches callback failures, and schedules no next cycle until all work settles.
export function createGitStatusPoller({ load, onStatuses, onError, interval, schedule, cancel })
```

Task 1 hardens Plan 5's existing poller to this async callback contract before Task 8 makes `applyGitStatuses` perform asynchronous tree/note work. Do not compensate by adding polling to `GitConflictWorkspace`, `NotesWorkspace`, or a second module.

This plan includes:

- Revision-aware frontend note reads and saves.
- A stale-note dialog that preserves the browser buffer and offers load disk, separately confirmed overwrite, and manual merge.
- Incoming terminal `changed_paths` deduplication by `repository_path + operation_id + path`.
- Automatic reload of a clean changed note and comparison instead of replacement for a dirty changed note.
- Tree refresh after incoming changes, conflict completion, and abort.
- A generic accessible `DialogShell` while retaining `Modal`'s public API and behavior.
- A full-screen conflict workspace for text, binary, add/add, modify/delete, and rename/delete conflicts.
- Local, remote, manual, delete, and keep-both resolution requests using current conflict IDs/OIDs.
- Complete/abort actions and `App.svelte` routing into and out of conflict state.
- An accessible base switcher that delegates to App's existing serialized switch path and leaves the conflict unresolved in the inactive repository.
- Preservation of resolver drafts, focus, and user input after API errors.
- Keyboard navigation, explicit side labels, and responsive mobile layouts.

Explicitly excluded:

- Autosync scheduler UI, five-failure circuit breaker behavior, resume implementation, timers, jitter, or breaker tests.
- Any automatic conflict-side selection.
- Diff algorithms, three-way merge libraries, binary preview/download endpoints, or syntax-aware merging.
- Backend, model, handler, repository, Git command, or SQLite changes.
- URL routing; `App.svelte` remains a local screen state machine.

## Starting-State Verification

The release workflow uses Node 24 at `.github/workflows/release.yml:18-23`. Node 20 is not a supported execution environment for this plan: the current lockfile's jsdom/undici stack fails there with `webidl.util.markAsUncloneable is not a function`.

After the mandatory Node/npm gate and before Task 1:

```bash
npm --prefix web test -- --run
npm --prefix web run build
```

Expected:

```text
Test Files  13 passed
Tests       all passed
vite ... built in ...
```

The exact test count may increase after Plan 5, but there must be no failed file, unhandled worker error, Svelte accessibility warning, or unhandled rejection. Do not edit dependencies in this plan to make Node 20 work.

## REST Contracts

```js
/** @typedef {{ id: string, content: string, revision: string }} NoteResponse */
/** @typedef {{ status: 'saved', revision: string }} SaveNoteResponse */

/** @typedef {{
 *   path: string,
 *   oid: string,
 *   mode: string,
 *   size: number,
 *   content?: string,
 *   preview_truncated: boolean,
 * }} GitConflictStage */

/** @typedef {{
 *   id: string,
 *   kind: 'content'|'add_add'|'modify_delete'|'rename_delete',
 *   content_kind: 'text'|'binary',
 *   path: string,
 *   original_path?: string,
 *   base?: GitConflictStage,
 *   local?: GitConflictStage,
 *   remote?: GitConflictStage,
 *   actions: Array<'local'|'remote'|'manual'|'delete'|'keep_both'>,
 * }} GitConflict */

/** @typedef {{
 *   base: string,
 *   operation_id: string,
 *   head_oid: string,
 *   merge_head_oid: string,
 *   conflicts: GitConflict[],
 *   can_complete: boolean,
 * }} GitConflictList */
```

Exact requests:

```text
GET  /api/git/conflicts?base=<encoded base>
PUT  /api/git/conflicts/resolve
POST /api/git/conflicts/complete?base=<encoded base>
POST /api/git/conflicts/abort?base=<encoded base>
```

`local` means Git stage 2, labeled `На этом устройстве`. `remote` means Git stage 3, labeled `В репозитории`. Never display “ours/theirs” as the primary user labels.

## State Invariants

`App.svelte` must preserve these invariants:

```text
noteRevision is the revision of bytes currently known to be on disk.
dirty means markdownContent differs from those known disk bytes.
Every official save sends expected_revision.
Only a successful save replaces noteRevision.
409 note_changed never changes markdownContent or clears dirty.
staleNote owns a cloned browser buffer and latest disk response.
While staleNote exists, debounce saves are disabled.
While active Git status is conflict, ordinary editor UI is not mounted.
Conflict resolve API errors do not recreate the resolver component.
Only terminal ready/error/paused statuses consume changed_paths.
Each unseen repository_path/operation_id/path key is recorded only after that path batch applies successfully.
The cache retains at most 128 operation buckets and survives base switches, so all paths for retained operations remain deduplicated when returning to a repository.
After complete/abort is accepted, queued/running/syncing status for that operation remains in the conflict workspace.
Conflict completion/abort returns to the editor only for ready/error/paused with the accepted operation_id.
Switching to another base delegates to App's serialized switch path and never calls resolve, complete, or abort.
```

For an incoming deletion of a dirty open note, preserve the browser text in the stale dialog, mark disk as missing, disable overwrite/manual-save actions because no current revision exists, and offer `Закрыть заметку` as the load-disk action. Do not fall back to an unconditional save.

## File Map

Create:

- `web/src/lib/DialogShell.svelte`: generic dialog semantics, focus trap, inert background, Escape, focus restoration, responsive shell, and body/action snippets.
- `web/src/lib/DialogShell.test.js`: generic accessibility, nesting, busy state, and focus tests.
- `web/src/test/DialogShellHost.svelte`: focused snippet/action host for `DialogShell` component tests.
- `web/src/lib/git/changed-paths.js`: terminal status filtering, stable repository/operation/path key generation, path dedupe, and active-note matching.
- `web/src/lib/git/changed-paths.test.js`: duplicate/out-of-order/status/path tests.
- `web/src/lib/stale-note.js`: stale state constructors and safe response validation.
- `web/src/lib/stale-note.test.js`: buffer/revision/deletion tests.
- `web/src/lib/StaleNoteDialog.svelte`: load disk, confirmed overwrite, and manual merge UI.
- `web/src/lib/StaleNoteDialog.test.js`: buffer preservation, retry, focus, and mobile markup tests.
- `web/src/lib/git/conflict-resolution.js`: exact action payload construction and deterministic output-name suggestions.
- `web/src/lib/git/conflict-resolution.test.js`: all conflict/action payload and validation tests.
- `web/src/lib/git/ConflictStagePanel.svelte`: explicit ancestor/local/remote stage presentation.
- `web/src/lib/git/ConflictStagePanel.test.js`: labels, text/binary/truncated content tests.
- `web/src/lib/git/TextConflictResolver.svelte`: content/add-add text choices and manual merge draft.
- `web/src/lib/git/TextConflictResolver.test.js`: local/remote/manual/keep-both and error-retention tests.
- `web/src/lib/git/BinaryConflictResolver.svelte`: binary metadata and local/remote/keep-both choices.
- `web/src/lib/git/BinaryConflictResolver.test.js`: no binary body rendering and exact requests.
- `web/src/lib/git/DeleteConflictResolver.svelte`: modify-delete/rename-delete keep/manual/delete choices.
- `web/src/lib/git/DeleteConflictResolver.test.js`: both retained-side orientations and rename paths.
- `web/src/lib/git/GitConflictWorkspace.svelte`: conflict loading, selection, keyboard navigation, resolve lifecycle, complete, abort, and accessible base switching.
- `web/src/lib/git/GitConflictWorkspace.test.js`: complete workspace behavior, base-switch delegation, and responsive/accessibility assertions.

Modify:

- `web/src/lib/api.js`: revision save signature and Plan 4 conflict wrappers.
- `web/src/lib/api.test.js`: exact note/conflict request and response tests.
- `web/src/lib/git/git-status-poller.js`: await async status/error callbacks and serialize refreshes with scheduled cycles.
- `web/src/lib/git/git-status-poller.test.js`: deferred callback, callback rejection, cadence, stale response, refresh, and stop tests.
- `web/src/lib/Modal.svelte`: render through `DialogShell` without changing existing props or behavior.
- `web/src/lib/Modal.test.js`: retain all regression tests and prove wrapper compatibility.
- `web/src/lib/Sidebar.svelte`: export a tree-only refresh method that does not call `/api/sync`.
- `web/src/lib/NotesWorkspace.svelte`: expose the sidebar refresh method to `App.svelte`.
- `web/src/lib/NotesWorkspace.test.js`: refresh delegation and no-rescan tests.
- `web/src/test/NotesWorkspaceHost.svelte`: pass any required Plan 5 status props unchanged.
- `web/src/App.svelte`: serial revision state, stale handling, changed-path consumption, conflict routing, and refresh coordination.
- `web/src/App.test.js`: revision races, stale choices, incoming changes, conflict routing, completion, abort, and preserved buffers.
- `docs/user.md`: stale-note and conflict workflow.
- `docs/developer.md`: frontend state/path-key/dialog architecture and test runtime.

Do not modify:

- Any `internal/`, `cmd/`, repository, migration, or Go test file.
- `web/src/lib/settings/**`; Plan 5 owns Git settings components.
- Plan 5's single-owner polling architecture; Task 1 hardens that existing module in place and must not create another poller or timer.
- `web/package.json` or `web/package-lock.json`.

## Parallel Dispatch Waves

All workers start from the same commit after Plan 5. They may read any file but edit only their exclusive ownership set.

### Wave 0: API Contract Freeze

- Worker 0 executes Task 1 serially.
- Exclusive files: `web/src/lib/api.js`, `web/src/lib/api.test.js`, `web/src/lib/git/git-status-poller.js`, `web/src/lib/git/git-status-poller.test.js`, `web/src/lib/git/changed-paths.js`, `web/src/lib/git/changed-paths.test.js`, `web/src/lib/stale-note.js`, `web/src/lib/stale-note.test.js`, `web/src/lib/git/conflict-resolution.js`, `web/src/lib/git/conflict-resolution.test.js`.
- Merge and run Task 1 tests before component dispatch.

### Wave 1: Independent Foundations

Run concurrently after Wave 0:

- Worker A executes Task 2 and owns `DialogShell.svelte`, `DialogShell.test.js`, `DialogShellHost.svelte`, `Modal.svelte`, and `Modal.test.js`.
- Worker B executes Task 3 and owns `StaleNoteDialog.svelte` and `StaleNoteDialog.test.js`.
- Worker C executes Task 4 and owns `ConflictStagePanel.svelte` and `ConflictStagePanel.test.js`.
- Worker D executes Task 7 and owns `Sidebar.svelte`, `NotesWorkspace.svelte`, `NotesWorkspace.test.js`, and `NotesWorkspaceHost.svelte`.

### Wave 2: Resolver Components

Run concurrently after Workers A and C merge:

- Worker E executes Task 5A and owns `TextConflictResolver.svelte` and `TextConflictResolver.test.js`.
- Worker F executes Task 5B and owns `BinaryConflictResolver.svelte` and `BinaryConflictResolver.test.js`.
- Worker G executes Task 5C and owns `DeleteConflictResolver.svelte` and `DeleteConflictResolver.test.js`.

### Wave 3: Conflict Workspace

- Worker H executes Task 6 after all resolver workers merge.
- Exclusive files: `GitConflictWorkspace.svelte`, `GitConflictWorkspace.test.js`.

### Wave 4: Serial App Integration

- The coordinator alone executes Task 8.
- Exclusive files: `web/src/App.svelte`, `web/src/App.test.js`.
- No worker may modify `App.svelte` or `App.test.js` in an earlier wave.

### Wave 5: Documentation and Verification

- The coordinator executes Task 9 after all code is integrated.
- Exclusive files: `docs/user.md`, `docs/developer.md`.

After every wave, review each diff before merge and run:

```bash
npm --prefix web test -- --run
npm --prefix web run build
```

Expected: all tests pass under Node 24; production build exits `0`; no Svelte accessibility warning is emitted.

### Task 1: Freeze Frontend API and Pure State Contracts

**Files:**
- Modify: `web/src/lib/api.js:171-216` plus Plan 5 Git methods
- Modify: `web/src/lib/api.test.js:318-395` plus Plan 5 tests
- Modify: `web/src/lib/git/git-status-poller.js`
- Modify: `web/src/lib/git/git-status-poller.test.js`
- Create: `web/src/lib/git/changed-paths.js`
- Create: `web/src/lib/git/changed-paths.test.js`
- Create: `web/src/lib/stale-note.js`
- Create: `web/src/lib/stale-note.test.js`
- Create: `web/src/lib/git/conflict-resolution.js`
- Create: `web/src/lib/git/conflict-resolution.test.js`

- [ ] **Step 1: Write RED API tests for revisions and conflicts**

Add imports for `abortGitConflict`, `completeGitConflict`, `getGitConflicts`, and `resolveGitConflict`, then add:

```js
it('sends the expected revision and returns the new revision', async () => {
  fetchMock.mockResolvedValue(jsonResponse({ status: 'saved', revision: 'sha256:new' }))

  await expect(saveNote('topic/note.md', '# Updated', 'sha256:old')).resolves.toEqual({
    status: 'saved',
    revision: 'sha256:new',
  })

  expectJSONRequest(fetchMock, 0, '/api/save', 'POST', {
    id: 'topic/note.md',
    content: '# Updated',
    expected_revision: 'sha256:old',
  })
})

it('uses exact conflict routes and bodies', async () => {
  const remaining = {
    base: 'work', operation_id: 'op-1', head_oid: 'a', merge_head_oid: 'b',
    conflicts: [], can_complete: true,
  }
  fetchMock
    .mockResolvedValueOnce(jsonResponse(remaining))
    .mockResolvedValueOnce(jsonResponse({ resolved_path: 'note.md', remaining }))
    .mockResolvedValueOnce(jsonResponse({ operation_id: 'complete-1', status: 'queued', deduplicated: false }, 202))
    .mockResolvedValueOnce(jsonResponse({ operation_id: 'abort-1', status: 'queued', deduplicated: false }, 202))

  const request = {
    base: 'work', operation_id: 'op-1', conflict_id: 'sha256:id', path: 'note.md',
    action: 'manual', result_path: 'note.md', content: '# merged',
  }
  await getGitConflicts('team/work')
  await resolveGitConflict(request)
  await completeGitConflict('team/work')
  await abortGitConflict('team/work')

  expect(requestAt(fetchMock, 0).path).toBe('/api/git/conflicts?base=team%2Fwork')
  expectJSONRequest(fetchMock, 1, '/api/git/conflicts/resolve', 'PUT', request)
  expect(requestAt(fetchMock, 2)).toMatchObject({
    path: '/api/git/conflicts/complete?base=team%2Fwork', options: { method: 'POST' },
  })
  expect(requestAt(fetchMock, 3)).toMatchObject({
    path: '/api/git/conflicts/abort?base=team%2Fwork', options: { method: 'POST' },
  })
})
```

- [ ] **Step 2: Run API tests RED**

Run:

```bash
npm --prefix web test -- --run src/lib/api.test.js
```

Expected: FAIL because `saveNote` omits `expected_revision` and the four conflict exports do not exist.

- [ ] **Step 3: Implement exact API wrappers**

Replace the note wrappers and append conflict wrappers:

```js
export function getNote(id) {
  return request(`/api/note?id=${encodeURIComponent(id)}`, { method: 'GET' })
}

export function saveNote(id, content, expectedRevision) {
  return request('/api/save', {
    method: 'POST',
    body: jsonBody({ id, content, expected_revision: expectedRevision }),
  })
}

export function getGitConflicts(base) {
  return request(`/api/git/conflicts?base=${encodeURIComponent(base)}`, { method: 'GET' })
}

export function resolveGitConflict(resolution) {
  return request('/api/git/conflicts/resolve', {
    method: 'PUT',
    body: jsonBody(resolution),
  })
}

export function completeGitConflict(base) {
  return request(`/api/git/conflicts/complete?base=${encodeURIComponent(base)}`, { method: 'POST' })
}

export function abortGitConflict(base) {
  return request(`/api/git/conflicts/abort?base=${encodeURIComponent(base)}`, { method: 'POST' })
}
```

Do not retain a two-argument `saveNote` fallback. Plan 2 keeps backend compatibility for external clients; the official frontend always supplies a revision.

- [ ] **Step 4: Write RED async poller callback tests**

In `web/src/lib/git/git-status-poller.test.js`, replace its local deferred helper so callback rejection can also be controlled:

```js
function deferred() {
  let resolve
  let reject
  const promise = new Promise((done, fail) => {
    resolve = done
    reject = fail
  })
  return { promise, resolve, reject }
}
```

Add tests proving a scheduled cycle cannot overlap async status application and callback failures are contained until the async error callback also settles:

```js
it('does not schedule or overlap while the async status callback is unsettled', async () => {
  vi.useFakeTimers()
  const applied = deferred()
  const load = vi.fn()
    .mockResolvedValueOnce({ statuses: [{ base: 'work', state: 'ready' }] })
    .mockResolvedValueOnce({ statuses: [{ base: 'work', state: 'syncing' }] })
  const onStatuses = vi.fn(() => applied.promise)
  const poller = createGitStatusPoller({ load, onStatuses, onError: vi.fn() })

  poller.start()
  await vi.advanceTimersByTimeAsync(0)
  expect(load).toHaveBeenCalledOnce()
  expect(onStatuses).toHaveBeenCalledOnce()
  expect(vi.getTimerCount()).toBe(0)

  await vi.advanceTimersByTimeAsync(10_000)
  expect(load).toHaveBeenCalledOnce()

  applied.resolve()
  await applied.promise
  await vi.advanceTimersByTimeAsync(0)
  expect(vi.getTimerCount()).toBe(1)
  await vi.advanceTimersByTimeAsync(2_000)
  expect(load).toHaveBeenCalledTimes(2)
})

it('catches a rejected status callback and waits for async error handling', async () => {
  vi.useFakeTimers()
  const reported = deferred()
  const applyError = new Error('apply failed')
  const onError = vi.fn(() => reported.promise)
  const poller = createGitStatusPoller({
    load: vi.fn().mockResolvedValue({ statuses: [{ base: 'work', state: 'ready' }] }),
    onStatuses: vi.fn().mockRejectedValue(applyError),
    onError,
  })

  poller.start()
  await vi.advanceTimersByTimeAsync(0)
  expect(onError).toHaveBeenCalledWith(applyError)
  expect(vi.getTimerCount()).toBe(0)

  reported.resolve()
  await reported.promise
  await vi.advanceTimersByTimeAsync(0)
  expect(vi.getTimerCount()).toBe(1)
})
```

Update Plan 5's stale-generation test so `refresh()` queues rather than overlaps the old request:

```js
poller.start()
await Promise.resolve()
expect(load).toHaveBeenCalledOnce()
const refresh = poller.refresh()
oldRequest.resolve({ statuses: [{ base: 'work', state: 'ready' }] })
await oldRequest.promise
await refresh

expect(load).toHaveBeenCalledTimes(2)
expect(onStatuses).toHaveBeenCalledOnce()
expect(onStatuses).toHaveBeenCalledWith([{ base: 'work', state: 'syncing' }])
```

- [ ] **Step 5: Run async poller tests RED**

Run: `npm --prefix web test -- --run src/lib/git/git-status-poller.test.js`

Expected: FAIL because Plan 5 does not await `onStatuses`, schedules while its promise is pending, and allows `refresh()` to overlap an unsettled cycle.

- [ ] **Step 6: Serialize poller loads and await callbacks**

Replace Plan 5's poller implementation with:

```js
export function createGitStatusPoller({
  load,
  onStatuses,
  onError,
  interval = 2000,
  schedule = setTimeout,
  cancel = clearTimeout,
}) {
  let active = false
  let generation = 0
  let timer = null
  let chain = Promise.resolve(null)

  function clearTimer() {
    if (timer !== null) cancel(timer)
    timer = null
  }

  async function report(error, current) {
    if (!active || current !== generation) return
    try {
      await onError(error)
    } catch {
      // Poller callbacks are an application boundary; never leak their rejection.
    }
  }

  async function run(current) {
    try {
      const payload = await load()
      if (!active || current !== generation) return null
      await onStatuses(payload.statuses)
      if (!active || current !== generation) return null
      await report(null, current)
      return payload.statuses
    } catch (error) {
      if (!active || current !== generation) return null
      await report(error, current)
      return null
    } finally {
      if (active && current === generation) {
        timer = schedule(() => { void enqueue(current) }, interval)
      }
    }
  }

  function enqueue(current) {
    const next = chain.then(() => {
      if (!active || current !== generation) return null
      return run(current)
    })
    chain = next.catch(() => null)
    return next
  }

  function start() {
    clearTimer()
    active = true
    generation += 1
    void enqueue(generation)
  }

  function refresh() {
    clearTimer()
    if (!active) return Promise.resolve(null)
    generation += 1
    return enqueue(generation)
  }

  function stop() {
    active = false
    generation += 1
    clearTimer()
  }

  return { start, refresh, stop }
}
```

Every `load`, `onStatuses`, and `onError` invocation is part of the same serialized cycle. A refresh invalidates an older response immediately but waits for its load/callback to settle before starting another load; `finally` schedules only after callback settlement. Rejected consumer callbacks are reported when possible and never escape as unhandled rejections.

- [ ] **Step 7: Write RED changed-path tests**

Create `web/src/lib/git/changed-paths.test.js`:

```js
import { describe, expect, it } from 'vitest'
import { changedPathKey, noteInChangedPaths, terminalChangedPaths } from './changed-paths.js'

describe('changed path keys', () => {
  it('accepts only terminal coherent states and sorts unique paths', () => {
    const status = {
      base: 'work', repository_path: '/notes/work', operation_id: 'op-1', state: 'ready',
      changed_paths: ['z.md', 'a.md', 'z.md', '', 42],
    }
    expect(terminalChangedPaths(status)).toEqual(['a.md', 'z.md'])
    expect(changedPathKey(status, 'a.md')).toBe('/notes/work\0op-1\0a.md')
  })

  it.each(['unconfigured', 'initializing', 'syncing', 'conflict', 'needs_reconnect'])(
    'ignores %s status changes',
    (state) => expect(terminalChangedPaths({
      base: 'work', repository_path: '/notes/work', operation_id: 'op-1', state,
      changed_paths: ['a.md'],
    })).toEqual([]),
  )

  it('keys each path by repository and operation rather than mutable base name', () => {
    const status = { base: 'renamed', repository_path: '/notes/work', operation_id: 'op-1' }
    expect(changedPathKey(status, 'a.md')).toBe('/notes/work\0op-1\0a.md')
    expect(changedPathKey(status, 'b.md')).toBe('/notes/work\0op-1\0b.md')
    expect(changedPathKey({ ...status, operation_id: 'op-2' }, 'a.md'))
      .toBe('/notes/work\0op-2\0a.md')
  })

  it('matches only the exact active note path', () => {
    expect(noteInChangedPaths('topic/note.md', ['topic/note.md'])).toBe(true)
    expect(noteInChangedPaths('topic/note.md', ['topic', 'topic/note.md.bak'])).toBe(false)
  })
})
```

- [ ] **Step 8: Implement changed-path normalization**

Create `web/src/lib/git/changed-paths.js`:

```js
const TERMINAL_STATES = new Set(['ready', 'error', 'paused'])

export function terminalChangedPaths(status) {
  if (!status || !TERMINAL_STATES.has(status.state)) return []
  if (!status.repository_path || !status.operation_id || !Array.isArray(status.changed_paths)) return []
  return [...new Set(status.changed_paths.filter((path) => typeof path === 'string' && path !== ''))].sort()
}

export function changedPathKey(status, path) {
  if (!status?.repository_path || !status?.operation_id || typeof path !== 'string' || path === '') return ''
  return [status.repository_path, status.operation_id, path].join('\0')
}

export function noteInChangedPaths(noteId, paths) {
  return typeof noteId === 'string' && noteId !== '' && paths.includes(noteId)
}
```

- [ ] **Step 9: Write RED stale-state unit tests**

Create `web/src/lib/stale-note.test.js`:

```js
import { expect, it } from 'vitest'
import { makeStaleNote, readNoteResponse, readSaveResponse } from './stale-note.js'

it('clones the dirty browser buffer and current disk response', () => {
  const stale = makeStaleNote({
    noteId: 'idea.md', mine: '# Mine', disk: { id: 'idea.md', content: '# Disk', revision: 'sha256:disk' },
  })
  expect(stale).toEqual({
    noteId: 'idea.md', mine: '# Mine', diskContent: '# Disk', diskRevision: 'sha256:disk',
    diskMissing: false,
  })
})

it('represents deletion without inventing a revision', () => {
  expect(makeStaleNote({ noteId: 'idea.md', mine: '# Mine', disk: null })).toMatchObject({
    mine: '# Mine', diskContent: '', diskRevision: '', diskMissing: true,
  })
})

it('rejects malformed successful responses', () => {
  expect(() => readNoteResponse({ content: '# no revision' })).toThrow('revision')
  expect(() => readSaveResponse({ status: 'saved' })).toThrow('revision')
})
```

- [ ] **Step 10: Run stale-state tests RED**

Run:

```bash
npm --prefix web test -- --run src/lib/stale-note.test.js
```

Expected: FAIL because `stale-note.js` does not exist.

- [ ] **Step 11: Implement stale-state helpers**

Create `web/src/lib/stale-note.js`:

```js
export function readNoteResponse(value) {
  if (!value || typeof value.content !== 'string' || typeof value.revision !== 'string' || !value.revision) {
    throw new Error('Приложение вернуло заметку без content/revision')
  }
  return value
}

export function readSaveResponse(value) {
  if (!value || value.status !== 'saved' || typeof value.revision !== 'string' || !value.revision) {
    throw new Error('Приложение вернуло сохранение без новой revision')
  }
  return value
}

export function makeStaleNote({ noteId, mine, disk }) {
  return {
    noteId,
    mine: String(mine),
    diskContent: disk?.content ?? '',
    diskRevision: disk?.revision ?? '',
    diskMissing: disk === null,
  }
}
```

- [ ] **Step 12: Write RED conflict payload tests**

Create `web/src/lib/git/conflict-resolution.test.js`:

```js
import { describe, expect, it } from 'vitest'
import { buildResolution, suggestSidePath } from './conflict-resolution.js'

const conflict = {
  id: 'sha256:conflict', path: 'notes/idea.md', kind: 'add_add', content_kind: 'text',
  actions: ['local', 'remote', 'manual', 'keep_both'],
  local: { oid: 'local-oid', path: 'notes/idea.md', content: '# Local' },
  remote: { oid: 'remote-oid', path: 'notes/idea.md', content: '# Remote' },
}
const identity = { base: 'work', operationId: 'op-1' }

describe('conflict resolution payloads', () => {
  it('builds local, remote, manual and keep-both without irrelevant fields', () => {
    expect(buildResolution(identity, conflict, { action: 'local', resultPath: 'notes/idea.md' })).toEqual({
      base: 'work', operation_id: 'op-1', conflict_id: conflict.id, path: conflict.path,
      action: 'local', result_path: 'notes/idea.md', local_oid: 'local-oid',
    })
    expect(buildResolution(identity, conflict, {
      action: 'manual', resultPath: 'notes/idea.md', content: '# Merged',
    })).toMatchObject({ action: 'manual', result_path: 'notes/idea.md', content: '# Merged' })
    expect(buildResolution(identity, conflict, {
      action: 'keep_both', localPath: 'notes/idea-local.md', remotePath: 'notes/idea-remote.md',
    })).toMatchObject({
      action: 'keep_both', local_path: 'notes/idea-local.md', remote_path: 'notes/idea-remote.md',
      local_oid: 'local-oid', remote_oid: 'remote-oid',
    })
  })

  it('builds delete with identity only and rejects unsupported actions', () => {
    const deleting = { ...conflict, actions: ['local', 'manual', 'delete'] }
    expect(buildResolution(identity, deleting, { action: 'delete' })).toEqual({
      base: 'work', operation_id: 'op-1', conflict_id: conflict.id,
      path: conflict.path, action: 'delete',
    })
    expect(() => buildResolution(identity, deleting, { action: 'remote', resultPath: conflict.path }))
      .toThrow('Недоступное действие')
  })

  it('suggests deterministic side names before the extension', () => {
    expect(suggestSidePath('assets/photo.png', 'local')).toBe('assets/photo-local.png')
    expect(suggestSidePath('README', 'remote')).toBe('README-remote')
  })
})
```

- [ ] **Step 13: Run conflict payload tests RED**

Run:

```bash
npm --prefix web test -- --run src/lib/git/conflict-resolution.test.js
```

Expected: FAIL because `conflict-resolution.js` does not exist.

- [ ] **Step 14: Implement exact resolution construction**

Create `web/src/lib/git/conflict-resolution.js`:

```js
function required(value, label) {
  if (typeof value !== 'string' || value.trim() === '') throw new Error(`Заполните поле «${label}»`)
  return value
}

export function suggestSidePath(path, side) {
  const slash = path.lastIndexOf('/')
  const directory = slash < 0 ? '' : path.slice(0, slash + 1)
  const name = path.slice(slash + 1)
  const dot = name.lastIndexOf('.')
  return dot > 0
    ? `${directory}${name.slice(0, dot)}-${side}${name.slice(dot)}`
    : `${directory}${name}-${side}`
}

export function buildResolution({ base, operationId }, conflict, draft) {
  if (!conflict.actions.includes(draft.action)) throw new Error('Недоступное действие для этого конфликта')
  const request = {
    base,
    operation_id: operationId,
    conflict_id: conflict.id,
    path: conflict.path,
    action: draft.action,
  }
  if (draft.action === 'local' || draft.action === 'remote') {
    request.result_path = required(draft.resultPath, 'Итоговый путь')
    request[`${draft.action}_oid`] = required(conflict[draft.action]?.oid, 'OID стороны')
  } else if (draft.action === 'manual') {
    request.result_path = required(draft.resultPath, 'Итоговый путь')
    if (typeof draft.content !== 'string') throw new Error('Введите итоговый текст')
    request.content = draft.content
  } else if (draft.action === 'keep_both') {
    request.local_path = required(draft.localPath, 'Путь версии на этом устройстве')
    request.remote_path = required(draft.remotePath, 'Путь версии из репозитория')
    if (request.local_path === request.remote_path) throw new Error('Итоговые пути должны отличаться')
    request.local_oid = required(conflict.local?.oid, 'OID локальной стороны')
    request.remote_oid = required(conflict.remote?.oid, 'OID remote-стороны')
  }
  return request
}
```

- [ ] **Step 15: Run Task 1 tests GREEN**

Run:

```bash
npm --prefix web test -- --run src/lib/api.test.js src/lib/git/git-status-poller.test.js src/lib/git/changed-paths.test.js src/lib/stale-note.test.js src/lib/git/conflict-resolution.test.js
```

Expected: PASS; requests contain exact query/body fields and pure helpers cover all actions without extra action-specific fields.

- [ ] **Step 16: Commit the contract freeze**

```bash
git add web/src/lib/api.js web/src/lib/api.test.js web/src/lib/git/git-status-poller.js web/src/lib/git/git-status-poller.test.js web/src/lib/git/changed-paths.js web/src/lib/git/changed-paths.test.js web/src/lib/stale-note.js web/src/lib/stale-note.test.js web/src/lib/git/conflict-resolution.js web/src/lib/git/conflict-resolution.test.js
git commit -m "feat: define git conflict frontend contracts"
```

### Task 2: Extract a Generic Accessible DialogShell and Preserve Modal

**Files:**
- Create: `web/src/lib/DialogShell.svelte`
- Create: `web/src/lib/DialogShell.test.js`
- Create: `web/src/test/DialogShellHost.svelte`
- Modify: `web/src/lib/Modal.svelte:1-239`
- Modify: `web/src/lib/Modal.test.js:1-283`

- [ ] **Step 1: Write RED shell accessibility tests**

Create `web/src/test/DialogShellHost.svelte`:

```svelte
<script>
  import DialogShell from '../lib/DialogShell.svelte'

  let { show = true, busy = false, onCancel = () => {} } = $props()
</script>

<DialogShell
  {show}
  title="Конфликт"
  description="Выберите безопасное действие"
  {busy}
  maxWidth="max-w-6xl"
  {onCancel}
>
  <button type="button" data-dialog-initial-focus>Первое действие</button>
  {#snippet actions()}
    <button type="button" onclick={onCancel} disabled={busy}>Отмена</button>
  {/snippet}
</DialogShell>
```

Import that host in `DialogShell.test.js` and assert:

```js
it('labels, describes, traps, inerts and restores focus', async () => {
  const trigger = document.createElement('button')
  trigger.textContent = 'Open'
  document.body.append(trigger)
  trigger.focus()
  const result = render(DialogShellHost, { show: true })
  const dialog = screen.getByRole('dialog', { name: 'Конфликт' })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Первое действие' })).toHaveFocus())
  expect(dialog).toHaveAttribute('aria-modal', 'true')
  expect(trigger).toHaveAttribute('inert')
  await userEvent.setup().tab({ shift: true })
  expect(screen.getByRole('button', { name: 'Отмена' })).toHaveFocus()
  await result.rerender({ show: false })
  await waitFor(() => expect(trigger).toHaveFocus())
  trigger.remove()
})
```

Also test Escape is ignored while `busy`, nested shells reference-count inert state, `maxWidth="max-w-6xl"` is applied, and the action row stacks with `flex-col-reverse sm:flex-row`.

- [ ] **Step 2: Run shell tests RED**

Run:

```bash
npm --prefix web test -- --run src/lib/DialogShell.test.js
```

Expected: FAIL because `DialogShell.svelte` does not exist.

- [ ] **Step 3: Create the generic shell**

Use Svelte 5 snippets and move the module-level inert reference counting, `backgroundElements`, `focusableElements`, focus trapping, busy-focus correction, and restoration code from `Modal.svelte` into `DialogShell.svelte`. Its public contract is:

```svelte
<script>
  let {
    show = false,
    title = '',
    description = '',
    error = '',
    busy = false,
    maxWidth = 'max-w-lg',
    onCancel = () => {},
    children,
    actions,
  } = $props()
</script>
```

The rendered shell must use this exact structure after applying the moved focus action to the panel:

```svelte
{#if show}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-3 sm:p-6" role="presentation">
    <section
      bind:this={dialogElement}
      use:manageDialog
      class="flex max-h-[calc(100dvh-1.5rem)] w-full flex-col overflow-hidden rounded-xl bg-white shadow-2xl sm:max-h-[calc(100dvh-3rem)] {maxWidth}"
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      aria-describedby={descriptionIds || undefined}
      aria-busy={busy ? 'true' : undefined}
      tabindex="-1"
      onkeydown={handleDialogKeydown}
    >
      <header class="shrink-0 border-b border-slate-200 px-4 py-4 sm:px-6">
        <h2 id={titleId} class="text-lg font-semibold text-slate-950">{title}</h2>
        {#if description}<p id={descriptionId} class="mt-1 text-sm text-slate-600">{description}</p>{/if}
        {#if error}<p id={errorId} role="alert" class="mt-2 text-sm text-red-700">{error}</p>{/if}
      </header>
      <div class="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-6">
        {@render children?.({ descriptionId, errorId })}
      </div>
      {#if actions}
        <footer class="flex shrink-0 flex-col-reverse gap-2 border-t border-slate-200 px-4 py-3 sm:flex-row sm:justify-end sm:px-6">
          {@render actions()}
        </footer>
      {/if}
    </section>
  </div>
{/if}
```

`manageDialog` chooses `[data-dialog-initial-focus]:not(:disabled)` first, then the first focusable item, then the panel. `handleDialogKeydown` calls `onCancel()` for Escape only when `busy === false`; all other moved behavior stays byte-for-byte equivalent where practical.

- [ ] **Step 4: Refactor Modal through DialogShell**

Keep every current `Modal` prop. Remove duplicated inert/focus code and render:

```svelte
<DialogShell
  {show}
  {title}
  {description}
  {error}
  {busy}
  maxWidth="max-w-sm"
  {onCancel}
>
  {#snippet children({ errorId: shellErrorId })}
    {#if input}
      <label for={inputId} class="sr-only">{title}</label>
      <input
        data-dialog-initial-focus
        id={inputId}
        type="text"
        bind:value={inputValue}
        class="w-full rounded border border-gray-300 px-3 py-2 focus:border-blue-500 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
        aria-invalid={error ? 'true' : undefined}
        aria-describedby={error ? shellErrorId : undefined}
        disabled={busy}
        onkeydown={handleInputKeydown}
      />
    {/if}
  {/snippet}
  {#snippet actions()}
    <button type="button" onclick={onCancel} disabled={busy} class={secondaryButtonClass}>{cancelText}</button>
    <button
      data-dialog-initial-focus={!input ? '' : undefined}
      type="button"
      onclick={onConfirm}
      disabled={busy || confirmDisabled}
      aria-busy={busy}
      class={danger ? dangerButtonClass : primaryButtonClass}
    >{confirmText}</button>
  {/snippet}
</DialogShell>
```

Define the three complete class strings in the script from the current button classes. Preserve input Enter semantics, danger styling, labels, busy behavior, and all existing Sidebar/Settings call sites without editing those callers.

- [ ] **Step 5: Run shell and Modal regressions GREEN**

Run:

```bash
npm --prefix web test -- --run src/lib/DialogShell.test.js src/lib/Modal.test.js src/lib/NotesWorkspace.test.js src/lib/settings/SettingsWorkspace.test.js
```

Expected: PASS; existing `Modal` tests remain unchanged except selectors that now find descriptions/errors in `DialogShell`'s header.

- [ ] **Step 6: Commit the dialog refactor**

```bash
git add web/src/lib/DialogShell.svelte web/src/lib/DialogShell.test.js web/src/test/DialogShellHost.svelte web/src/lib/Modal.svelte web/src/lib/Modal.test.js
git commit -m "refactor: extract accessible dialog shell"
```

### Task 3: Build the Stale Note Dialog

**Files:**
- Create: `web/src/lib/StaleNoteDialog.svelte`
- Create: `web/src/lib/StaleNoteDialog.test.js`

- [ ] **Step 1: Write RED interaction tests**

Use a stale fixture with `mine`, `diskContent`, and `diskRevision`. Add exact cases:

```js
it('requires a second confirmation before overwriting', async () => {
  const onOverwrite = vi.fn()
  render(StaleNoteDialog, { stale, onLoadDisk: vi.fn(), onOverwrite, onManualMerge: vi.fn() })
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Оставить мою версию' }))
  expect(onOverwrite).not.toHaveBeenCalled()
  expect(screen.getByText('Перезапись заменит актуальный файл на диске.')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Подтвердить перезапись' }))
  expect(onOverwrite).toHaveBeenCalledWith({ content: '# Mine', revision: 'sha256:disk' })
})

it('preserves a manual merge draft after an API error', async () => {
  const onManualMerge = vi.fn().mockRejectedValue(new Error('Файл снова изменился'))
  render(StaleNoteDialog, { stale, onLoadDisk: vi.fn(), onOverwrite: vi.fn(), onManualMerge })
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Объединить вручную' }))
  const result = screen.getByLabelText('Итоговый текст')
  await user.clear(result)
  await user.type(result, '# My merge')
  await user.click(screen.getByRole('button', { name: 'Сохранить объединение' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Файл снова изменился')
  expect(result).toHaveValue('# My merge')
})
```

Also assert load-disk callback, exact readonly labels `Моя версия`/`Версия на диске`, deletion disables overwrite/manual actions, Escape cannot discard the required decision, and comparison columns use `grid-cols-1 lg:grid-cols-2`.

- [ ] **Step 2: Run stale dialog tests RED**

Run:

```bash
npm --prefix web test -- --run src/lib/StaleNoteDialog.test.js
```

Expected: FAIL because the component does not exist.

- [ ] **Step 3: Implement stale modes without resetting drafts**

Use this state and handlers:

```svelte
<script>
  import DialogShell from './DialogShell.svelte'

  let { stale, busy = false, onLoadDisk, onOverwrite, onManualMerge } = $props()
  let mode = $state('compare')
  let mergeContent = $state(stale.mine)
  let actionBusy = $state(false)
  let error = $state('')

  async function run(action) {
    if (actionBusy) return
    actionBusy = true
    error = ''
    try {
      await action()
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Не удалось применить решение'
    } finally {
      actionBusy = false
    }
  }

  const loadDisk = () => run(() => onLoadDisk())
  const confirmOverwrite = () => run(() => onOverwrite({
    content: stale.mine,
    revision: stale.diskRevision,
  }))
  const saveMerge = () => run(() => onManualMerge({
    content: mergeContent,
    revision: stale.diskRevision,
  }))
</script>
```

Render `DialogShell` with no-op cancellation because the user must make an explicit safe choice. Compare mode has two readonly `<textarea>` controls, load-disk, overwrite-entry, and manual-entry buttons. Confirm mode contains the explicit destructive warning and back/confirm buttons. Manual mode keeps `mergeContent` in one mounted textarea until success and contains back/save buttons. Use `busy={busy || actionBusy}`, `maxWidth="max-w-6xl"`, minimum textarea height `min-h-56`, and stack all action rows on narrow screens.

- [ ] **Step 4: Run stale dialog tests GREEN**

Run:

```bash
npm --prefix web test -- --run src/lib/StaleNoteDialog.test.js src/lib/DialogShell.test.js
```

Expected: PASS; errors remain in the dialog and do not clear `mergeContent`.

- [ ] **Step 5: Commit stale-note UI**

```bash
git add web/src/lib/StaleNoteDialog.svelte web/src/lib/StaleNoteDialog.test.js
git commit -m "feat: add stale note recovery dialog"
```

### Task 4: Present Conflict Stages with Explicit Side Labels

**Files:**
- Create: `web/src/lib/git/ConflictStagePanel.svelte`
- Create: `web/src/lib/git/ConflictStagePanel.test.js`

- [ ] **Step 1: Write RED stage panel tests**

```js
it.each([
  ['base', 'Общий предок'],
  ['local', 'На этом устройстве'],
  ['remote', 'В репозитории'],
])('labels %s without Git jargon', (side, label) => {
  render(ConflictStagePanel, { side, stage: textStage, contentKind: 'text' })
  expect(screen.getByRole('heading', { name: label })).toBeVisible()
  expect(screen.queryByText(/ours|theirs/i)).not.toBeInTheDocument()
})

it('shows metadata but never binary content', () => {
  render(ConflictStagePanel, {
    side: 'remote', contentKind: 'binary',
    stage: { path: 'assets/photo.png', oid: 'abc', mode: '100644', size: 4096, preview_truncated: false },
  })
  expect(screen.getByText('4 KB')).toBeVisible()
  expect(screen.getByText('Двоичный файл')).toBeVisible()
  expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
})
```

Add absent-stage and `preview_truncated` cases.

- [ ] **Step 2: Run stage panel tests RED**

Run:

```bash
npm --prefix web test -- --run src/lib/git/ConflictStagePanel.test.js
```

Expected: FAIL because the component is absent.

- [ ] **Step 3: Implement the stage panel**

Use a fixed label map, `Intl.NumberFormat('ru-RU')` byte formatting, a readonly textarea only for non-truncated text content, and this shell:

```svelte
<section class="min-w-0 rounded-lg border border-slate-200 bg-white" aria-label={label}>
  <header class="border-b border-slate-200 px-3 py-2">
    <h3 class="text-sm font-semibold text-slate-900">{label}</h3>
    {#if stage}<p class="truncate font-mono text-xs text-slate-500" title={stage.path}>{stage.path}</p>{/if}
  </header>
  <div class="p-3">
    {#if !stage}
      <p class="text-sm text-slate-500">Файл отсутствует на этой стороне</p>
    {:else if contentKind === 'binary'}
      <p class="text-sm font-medium text-slate-700">Двоичный файл</p>
    {:else if stage.preview_truncated}
      <p class="text-sm text-amber-700">Текст больше 1 MiB; выберите сторону целиком или итоговый путь.</p>
    {:else}
      <label class="sr-only" for={textareaId}>{label}</label>
      <textarea id={textareaId} readonly value={stage.content ?? ''} class="min-h-40 w-full resize-y rounded border border-slate-200 bg-slate-50 p-2 font-mono text-xs"></textarea>
    {/if}
    {#if stage}<p class="mt-2 text-xs text-slate-500">{formatBytes(stage.size)} · режим {stage.mode}</p>{/if}
  </div>
</section>
```

- [ ] **Step 4: Run stage panel tests GREEN**

```bash
npm --prefix web test -- --run src/lib/git/ConflictStagePanel.test.js
```

Expected: PASS.

- [ ] **Step 5: Commit the stage panel**

```bash
git add web/src/lib/git/ConflictStagePanel.svelte web/src/lib/git/ConflictStagePanel.test.js
git commit -m "feat: present git conflict stages"
```

### Task 5A: Build the Text Conflict Resolver

**Files:**
- Create: `web/src/lib/git/TextConflictResolver.svelte`
- Create: `web/src/lib/git/TextConflictResolver.test.js`

- [ ] **Step 1: Write RED text resolver tests**

Render content and add/add fixtures. Assert native local/remote/manual/keep-both radios live in one labeled fieldset, local/remote selections submit `buildResolution` payloads, manual starts from local content then preserves edits after rejected `onResolve`, keep-both appears only when listed in `actions`, path fields remain after errors, all controls disable while busy, and columns collapse below `lg`.

Use this exact rejection assertion:

```js
const onResolve = vi.fn().mockRejectedValue(new Error('Конфликт изменился'))
render(TextConflictResolver, { base: 'work', operationId: 'op-1', conflict, onResolve })
expect(screen.getByRole('group', { name: 'Способ разрешения' })).toBeVisible()
await user.click(screen.getByRole('radio', { name: 'Объединить вручную' }))
await user.clear(screen.getByLabelText('Итоговый текст'))
await user.type(screen.getByLabelText('Итоговый текст'), '# Kept draft')
await user.click(screen.getByRole('button', { name: 'Применить решение' }))
expect(await screen.findByRole('alert')).toHaveTextContent('Конфликт изменился')
expect(screen.getByLabelText('Итоговый текст')).toHaveValue('# Kept draft')
```

- [ ] **Step 2: Run tests RED**

Run: `npm --prefix web test -- --run src/lib/git/TextConflictResolver.test.js`

Expected: FAIL because the component does not exist.

- [ ] **Step 3: Implement stable resolver state**

Use one mounted component keyed by `conflict.id` in the parent. Initialize only once:

```svelte
<script>
  import ConflictStagePanel from './ConflictStagePanel.svelte'
  import { buildResolution, suggestSidePath } from './conflict-resolution.js'

  let { base, operationId, conflict, busy = false, onResolve } = $props()
  let action = $state('')
  let resultPath = $state(conflict.path)
  let manualContent = $state(conflict.local?.content ?? conflict.remote?.content ?? '')
  let localPath = $state(suggestSidePath(conflict.local?.path ?? conflict.path, 'local'))
  let remotePath = $state(suggestSidePath(conflict.remote?.path ?? conflict.path, 'remote'))
  let localBusy = $state(false)
  let error = $state('')

  async function submit() {
    if (busy || localBusy) return
    error = ''
    localBusy = true
    try {
      await onResolve(buildResolution({ base, operationId }, conflict, {
        action, resultPath, content: manualContent, localPath, remotePath,
      }))
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Не удалось разрешить конфликт'
    } finally {
      localBusy = false
    }
  }
</script>
```

Render three `ConflictStagePanel` components in `grid grid-cols-1 gap-3 lg:grid-cols-3`. Put every available mutually exclusive action in one native fieldset; do not use action buttons or custom radio roles:

```svelte
<fieldset disabled={busy || localBusy} class="space-y-2">
  <legend class="text-sm font-semibold text-slate-900">Способ разрешения</legend>
  {#each conflict.actions as choice}
    <label class="flex min-h-11 items-center gap-3 rounded-lg border border-slate-200 px-3 py-2">
      <input
        type="radio"
        name={`text-resolution-${conflict.id}`}
        value={choice}
        bind:group={action}
      />
      <span>{actionLabel(choice)}</span>
    </label>
  {/each}
</fieldset>
```

`actionLabel` returns the explicit device/repository/manual/keep-both labels used by the tests. Manual textarea and required path labels are always explicit; submit has `aria-busy={localBusy}` and remains disabled while `action === ''`. Do not preselect a side/action, and do not add a `$effect` that resets action, paths, or manual content after a prop/error update.

- [ ] **Step 4: Run text resolver tests GREEN**

```bash
npm --prefix web test -- --run src/lib/git/TextConflictResolver.test.js src/lib/git/ConflictStagePanel.test.js
```

Expected: PASS.

- [ ] **Step 5: Commit the text resolver**

```bash
git add web/src/lib/git/TextConflictResolver.svelte web/src/lib/git/TextConflictResolver.test.js
git commit -m "feat: resolve text git conflicts"
```

### Task 5B: Build the Binary Conflict Resolver

**Files:**
- Create: `web/src/lib/git/BinaryConflictResolver.svelte`
- Create: `web/src/lib/git/BinaryConflictResolver.test.js`

- [ ] **Step 1: Write RED binary tests**

Assert no stage body bytes/textareas render, local/remote selection requires `result_path`, keep-both requires two distinct paths and both OIDs, add/add defaults use `photo-local.png`/`photo-remote.png`, API errors retain both paths, and unavailable manual/delete buttons do not render.

- [ ] **Step 2: Run RED**

Run: `npm --prefix web test -- --run src/lib/git/BinaryConflictResolver.test.js`

Expected: FAIL because the component does not exist.

- [ ] **Step 3: Implement metadata-only binary choices**

Use the same stable busy/error pattern as the text component but only allow `local`, `remote`, and `keep_both` from `conflict.actions`. Render stage panels with `contentKind="binary"`. Build the request only through:

```js
buildResolution({ base, operationId }, conflict, {
  action,
  resultPath,
  localPath,
  remotePath,
})
```

Use native labeled radio inputs for the action, not clickable cards with custom roles. The keep-both explanation must say both files are written under explicit different names and no existing unrelated file is overwritten.

- [ ] **Step 4: Run binary resolver tests GREEN**

```bash
npm --prefix web test -- --run src/lib/git/BinaryConflictResolver.test.js src/lib/git/conflict-resolution.test.js
```

Expected: PASS.

- [ ] **Step 5: Commit the binary resolver**

```bash
git add web/src/lib/git/BinaryConflictResolver.svelte web/src/lib/git/BinaryConflictResolver.test.js
git commit -m "feat: resolve binary git conflicts"
```

### Task 5C: Build Modify/Delete and Rename/Delete Resolution

**Files:**
- Create: `web/src/lib/git/DeleteConflictResolver.svelte`
- Create: `web/src/lib/git/DeleteConflictResolver.test.js`

- [ ] **Step 1: Write RED delete-family tests**

Cover `UD`-derived local retained content, `DU`-derived remote retained content, text manual action, binary omission of manual action, confirmed delete, rename/delete display of `original_path -> path`, alternate result path, and draft retention after an API error. Assert every available keep/manual/delete choice is a native radio in one fieldset named `Способ разрешения`; there are no action buttons with `aria-pressed`.

The delete confirmation test must assert two clicks:

```js
await user.click(screen.getByRole('radio', { name: 'Удалить итоговый файл' }))
await user.click(screen.getByRole('button', { name: 'Применить решение' }))
expect(onResolve).not.toHaveBeenCalled()
await user.click(screen.getByRole('button', { name: 'Подтвердить удаление' }))
expect(onResolve).toHaveBeenCalledWith(expect.objectContaining({ action: 'delete' }))
```

- [ ] **Step 2: Run RED**

Run: `npm --prefix web test -- --run src/lib/git/DeleteConflictResolver.test.js`

Expected: FAIL because the component does not exist.

- [ ] **Step 3: Implement retained-side and delete flows**

Derive the existing action without choosing it automatically for the user:

```js
const existingAction = conflict.actions.includes('local') ? 'local' : 'remote'
let action = $state('')
let confirmingDelete = $state(false)
let resultPath = $state(conflict[existingAction]?.path ?? conflict.path)
let manualContent = $state(conflict[existingAction]?.content ?? '')
```

Require the user to select `Сохранить версию на этом устройстве`, `Сохранить версию из репозитория`, `Объединить вручную`, or `Удалить итоговый файл` through native controls with this structure:

```svelte
<fieldset disabled={busy || localBusy} class="space-y-2">
  <legend class="text-sm font-semibold text-slate-900">Способ разрешения</legend>
  {#each conflict.actions as choice}
    <label class="flex min-h-11 items-center gap-3 rounded-lg border border-slate-200 px-3 py-2">
      <input
        type="radio"
        name={`delete-resolution-${conflict.id}`}
        value={choice}
        bind:group={action}
      />
      <span>{actionLabel(choice)}</span>
    </label>
  {/each}
</fieldset>
```

Render only actions listed by the backend and do not use clickable cards, `role="radio"`, or `aria-pressed`. Show original/final rename paths in a `<dl>`. Submitting a selected `delete` enters confirmation state and calls `onResolve` only after the second, `Подтвердить удаление`, click. All non-delete requests go through `buildResolution`; preserve selection, paths, text, and confirmation state after errors.

- [ ] **Step 4: Run delete-family resolver tests GREEN**

```bash
npm --prefix web test -- --run src/lib/git/DeleteConflictResolver.test.js src/lib/git/conflict-resolution.test.js
```

Expected: PASS.

- [ ] **Step 5: Commit the delete-family resolver**

```bash
git add web/src/lib/git/DeleteConflictResolver.svelte web/src/lib/git/DeleteConflictResolver.test.js
git commit -m "feat: resolve delete git conflicts"
```

### Task 6: Build the Full Conflict Workspace

**Files:**
- Create: `web/src/lib/git/GitConflictWorkspace.svelte`
- Create: `web/src/lib/git/GitConflictWorkspace.test.js`

- [ ] **Step 1: Write RED loading and routing tests**

Mock conflict API wrappers and cover loading, retry, all four kind/content combinations, resolve response replacement, no component remount after rejected resolve, stale `git_conflict_stale` reload, and base-switch delegation without any conflict mutation request.

```js
it('updates from remaining conflicts after one resolution', async () => {
  getGitConflicts.mockResolvedValue(conflictList([textConflict, binaryConflict]))
  resolveGitConflict.mockResolvedValue({
    resolved_path: textConflict.path,
    remaining: conflictList([binaryConflict]),
  })
  render(GitConflictWorkspace, { base: 'work', onOperationAccepted: vi.fn() })
  await user.click(await screen.findByRole('button', { name: /note.md/ }))
  await user.click(screen.getByRole('button', { name: 'Применить решение' }))
  expect(await screen.findByText('1 конфликт остался')).toBeVisible()
  expect(screen.queryByText(textConflict.path)).not.toBeInTheDocument()
})

it('switches base through its App callback without resolving the conflict', async () => {
  const onSwitchBase = vi.fn().mockResolvedValue(undefined)
  render(GitConflictWorkspace, {
    base: 'work',
    bases: [{ name: 'work' }, { name: 'personal' }],
    onOperationAccepted: vi.fn(),
    onSwitchBase,
  })
  await screen.findByRole('heading', { name: 'Конфликты Git' })

  await user.selectOptions(screen.getByLabelText('Другая база'), 'personal')
  await user.click(screen.getByRole('button', { name: 'Переключить базу' }))

  expect(onSwitchBase).toHaveBeenCalledWith('personal')
  expect(resolveGitConflict).not.toHaveBeenCalled()
  expect(completeGitConflict).not.toHaveBeenCalled()
  expect(abortGitConflict).not.toHaveBeenCalled()
})
```

- [ ] **Step 2: Write RED keyboard/accessibility tests**

Assert:

- The path list is a labeled navigation region.
- Up/Down, Home, and End move focus and selection among path buttons.
- Selected path has `aria-current="true"`.
- Focus moves to the next path after resolution and to the Complete button when none remain.
- Mobile root uses one column and desktop uses `lg:grid-cols-[18rem_minmax(0,1fr)]`.
- Every icon is decorative or has an accessible name.
- Complete is disabled until `can_complete` and no conflicts remain.
- Abort opens a `DialogShell` confirmation and retains focus after an API error.
- The native `Другая база` select and `Переключить базу` button have associated labels, disable during complete/abort acceptance, preserve selection/error on rejection, and call only `onSwitchBase`.

- [ ] **Step 3: Run workspace tests RED**

Run:

```bash
npm --prefix web test -- --run src/lib/git/GitConflictWorkspace.test.js
```

Expected: FAIL because the workspace does not exist.

- [ ] **Step 4: Implement one snapshot owner and stable selection**

Use:

```svelte
<script>
  import { onMount, tick } from 'svelte'
  import DialogShell from '../DialogShell.svelte'
  import { abortGitConflict, completeGitConflict, getGitConflicts, resolveGitConflict } from '../api.js'
  import BinaryConflictResolver from './BinaryConflictResolver.svelte'
  import DeleteConflictResolver from './DeleteConflictResolver.svelte'
  import TextConflictResolver from './TextConflictResolver.svelte'

  let { base, bases = [], onOperationAccepted, onSwitchBase } = $props()
  let snapshot = $state(null)
  let selectedId = $state('')
  let loading = $state(true)
  let workspaceError = $state('')
  let operationBusy = $state(false)
  let showAbort = $state(false)
  let switchTarget = $state('')
  let switchBusy = $state(false)
  let pathButtons = $state([])
  let completeButton = $state()
  let mounted = true

  let selected = $derived(snapshot?.conflicts.find((item) => item.id === selectedId) ?? null)
  let switchableBases = $derived(
    bases.filter((item) => typeof item?.name === 'string' && item.name !== base),
  )

  async function load() {
    loading = true
    workspaceError = ''
    try {
      const next = await getGitConflicts(base)
      if (!mounted) return
      snapshot = next
      if (!next.conflicts.some((item) => item.id === selectedId)) selectedId = next.conflicts[0]?.id ?? ''
    } catch (cause) {
      if (mounted) workspaceError = cause instanceof Error ? cause.message : 'Не удалось загрузить конфликты'
    } finally {
      if (mounted) loading = false
    }
  }

  async function resolve(request) {
    try {
      const response = await resolveGitConflict(request)
      if (!mounted) return
      snapshot = response.remaining
      selectedId = response.remaining.conflicts[0]?.id ?? ''
      await tick()
      if (selectedId) pathButtons[0]?.focus()
      else completeButton?.focus()
    } catch (cause) {
      if (cause?.status === 409 && cause?.code === 'git_conflict_stale') await load()
      throw cause
    }
  }

  async function switchToBase() {
    if (operationBusy || switchBusy || !switchTarget) return
    switchBusy = true
    workspaceError = ''
    try {
      await onSwitchBase(switchTarget)
    } catch (cause) {
      if (mounted) workspaceError = cause instanceof Error ? cause.message : 'Не удалось переключить базу'
    } finally {
      if (mounted) switchBusy = false
    }
  }

  onMount(() => {
    void load()
    return () => { mounted = false }
  })
</script>
```

Set `mounted=false` on destroy. The selected resolver is wrapped in `{#key selected.id}` so a different conflict receives fresh drafts, while an API error for the same conflict does not remount it.

- [ ] **Step 5: Render exact resolver dispatch**

```svelte
{#if selected}
  {#key selected.id}
    {#if selected.kind === 'modify_delete' || selected.kind === 'rename_delete'}
      <DeleteConflictResolver {base} operationId={snapshot.operation_id} conflict={selected} busy={operationBusy} onResolve={resolve} />
    {:else if selected.content_kind === 'binary'}
      <BinaryConflictResolver {base} operationId={snapshot.operation_id} conflict={selected} busy={operationBusy} onResolve={resolve} />
    {:else}
      <TextConflictResolver {base} operationId={snapshot.operation_id} conflict={selected} busy={operationBusy} onResolve={resolve} />
    {/if}
  {/key}
{/if}
```

The full-screen shell uses `min-h-screen bg-slate-100`, sticky mobile header, responsive two-column main area, path buttons large enough for touch, and a bottom action bar. Add this native base switcher to the header or bottom action bar; do not render the normal editor/sidebar in this component:

```svelte
{#if switchableBases.length > 0}
  <form class="flex flex-col gap-2 sm:flex-row sm:items-end" onsubmit={(event) => { event.preventDefault(); void switchToBase() }}>
    <div>
      <label for="conflict-base-switch" class="block text-sm font-medium text-slate-700">Другая база</label>
      <select
        id="conflict-base-switch"
        bind:value={switchTarget}
        disabled={operationBusy || switchBusy}
        class="min-h-11 rounded-lg border border-slate-300 bg-white px-3"
      >
        <option value="">Выберите базу</option>
        {#each switchableBases as item}
          <option value={item.name}>{item.name}</option>
        {/each}
      </select>
    </div>
    <button
      type="submit"
      disabled={operationBusy || switchBusy || !switchTarget}
      aria-busy={switchBusy}
      class="min-h-11 rounded-lg border border-slate-300 bg-white px-4 font-medium"
    >Переключить базу</button>
  </form>
{/if}
```

`onSwitchBase` is the only switcher callback. The workspace never calls `switchBase`, `resolveGitConflict`, `completeGitConflict`, or `abortGitConflict` from this action.

- [ ] **Step 6: Implement complete and abort acceptance**

```js
async function queueOperation(kind) {
  if (operationBusy) return
  operationBusy = true
  workspaceError = ''
  try {
    const response = kind === 'complete'
      ? await completeGitConflict(base)
      : await abortGitConflict(base)
    if (!mounted) return
    showAbort = false
    await onOperationAccepted(kind, response)
  } catch (cause) {
    if (!mounted) return
    workspaceError = cause instanceof Error ? cause.message : 'Не удалось изменить состояние конфликта'
    operationBusy = false
  }
}
```

Keep `operationBusy=true` after successful acceptance; Plan 5 polling and `App.svelte` routing settle the operation. Complete is enabled only for `snapshot.can_complete && snapshot.conflicts.length === 0`. Abort confirmation states that local pre-merge snapshot will be restored, remote remains unchanged, and synchronization becomes paused.

- [ ] **Step 7: Run workspace tests GREEN**

Run:

```bash
npm --prefix web test -- --run src/lib/git/GitConflictWorkspace.test.js src/lib/git/*Resolver.test.js
```

Expected: PASS for every resolver dispatch, retry, complete/abort, keyboard, and responsive assertion.

- [ ] **Step 8: Commit conflict workspace**

```bash
git add web/src/lib/git/GitConflictWorkspace.svelte web/src/lib/git/GitConflictWorkspace.test.js
git commit -m "feat: add git conflict workspace"
```

### Task 7: Expose Tree Refresh Without Filesystem Rescan

**Files:**
- Modify: `web/src/lib/Sidebar.svelte:48-123`
- Modify: `web/src/lib/NotesWorkspace.svelte:19-29,38-42`
- Modify: `web/src/lib/NotesWorkspace.test.js:202-217,308-331`
- Modify: `web/src/test/NotesWorkspaceHost.svelte`

- [ ] **Step 1: Write RED instance refresh test**

```js
it('refreshes the indexed tree without calling filesystem sync', async () => {
  vi.mocked(getNotes)
    .mockResolvedValueOnce([fileNode('old.md')])
    .mockResolvedValueOnce([fileNode('incoming.md')])
  const { component } = await renderWorkspace()
  await component.refreshTree()
  expect(getNotes).toHaveBeenCalledTimes(2)
  expect(syncNotes).not.toHaveBeenCalled()
  expect(await screen.findByRole('button', { name: 'incoming.md' })).toBeVisible()
})
```

- [ ] **Step 2: Run RED**

Run: `npm --prefix web test -- --run src/lib/NotesWorkspace.test.js`

Expected: FAIL with `component.refreshTree is not a function`.

- [ ] **Step 3: Export the existing load path through both components**

In `Sidebar.svelte`, add:

```js
export function refreshTree() {
  return loadTree()
}
```

Do not call `syncNotes`; the backend Git operation has already rebuilt SQLite before publishing changed paths.

Bind the sidebar instance and expose delegation in `NotesWorkspace.svelte`:

```svelte
let sidebar = $state()

export function refreshTree() {
  return sidebar?.refreshTree?.()
}

<Sidebar
  bind:this={sidebar}
  onSelect={(...args) => runAfterUploads(onSelectNote, ...args)}
  onRename={onRenameNote}
  onDelete={onDeleteNote}
/>
```

Keep `flushPendingUploads` unchanged. Update `NotesWorkspaceHost` only for props added by Plan 5; do not introduce test-only production props.

- [ ] **Step 4: Run tree refresh tests GREEN**

```bash
npm --prefix web test -- --run src/lib/NotesWorkspace.test.js
```

Expected: PASS; the existing manual `Обновить дерево` test still calls `/api/sync`, while programmatic refresh does not.

- [ ] **Step 5: Commit the tree refresh seam**

```bash
git add web/src/lib/Sidebar.svelte web/src/lib/NotesWorkspace.svelte web/src/lib/NotesWorkspace.test.js web/src/test/NotesWorkspaceHost.svelte
git commit -m "feat: expose indexed tree refresh"
```

### Task 8: Integrate Revisions, Incoming Changes, Stale Notes, and Conflict Routing in App

**Files:**
- Modify: `web/src/App.svelte:1-404` after Plan 5 integration
- Modify: `web/src/App.test.js:1-1055` after Plan 5 tests

This task is strictly serial. Re-read the integrated `App.svelte` immediately before editing and retain all Plan 5 settings/manual-sync/status-indicator behavior.

- [ ] **Step 1: Update App API mocks and revision fixtures RED**

Mock the four conflict API functions and Plan 5 status API. Change every successful `getNote` fixture to include `id` and `revision`, and every successful `saveNote` fixture to return `{status:'saved', revision:'sha256:...'}`.

Add this core assertion:

```js
it('saves with the loaded revision and chains the returned revision', async () => {
  const note = fileNode('draft.md')
  vi.mocked(getNotes).mockResolvedValue([note])
  vi.mocked(getNote).mockResolvedValue({ id: note.id, content: '# Original', revision: 'sha256:r1' })
  vi.mocked(saveNote)
    .mockResolvedValueOnce({ status: 'saved', revision: 'sha256:r2' })
    .mockResolvedValueOnce({ status: 'saved', revision: 'sha256:r3' })
  render(App)
  const user = userEvent.setup()
  await user.click(await screen.findByRole('button', { name: 'draft.md' }))
  await user.clear(screen.getByLabelText('Markdown'))
  await user.type(screen.getByLabelText('Markdown'), '# First')
  await user.click(screen.getByRole('button', { name: 'Сохранить' }))
  await user.type(screen.getByLabelText('Markdown'), ' again')
  await user.click(screen.getByRole('button', { name: 'Сохранить' }))
  expect(saveNote).toHaveBeenNthCalledWith(1, note.id, '# First', 'sha256:r1')
  expect(saveNote).toHaveBeenNthCalledWith(2, note.id, '# First again', 'sha256:r2')
})
```

- [ ] **Step 2: Add RED stale-save tests**

Cover:

```js
it('keeps the newest browser buffer after note_changed', async () => {
  const user = userEvent.setup()
  const note = fileNode('draft.md')
  vi.mocked(getNotes).mockResolvedValue([note])
  vi.mocked(saveNote).mockRejectedValueOnce(new ApiError({ status: 409, code: 'note_changed', message: 'note changed' }))
  vi.mocked(getNote)
    .mockResolvedValueOnce({ id: note.id, content: '# Original', revision: 'sha256:r1' })
    .mockResolvedValueOnce({ id: note.id, content: '# Disk', revision: 'sha256:r2' })

  render(App)
  await user.click(await screen.findByRole('button', { name: 'draft.md' }))
  const editor = screen.getByLabelText('Markdown')
  await user.clear(editor)
  await user.type(editor, '# Browser')
  await user.click(screen.getByRole('button', { name: 'Сохранить' }))

  const dialog = await screen.findByRole('dialog', { name: 'Заметка изменилась на диске' })
  expect(editor).toHaveValue('# Browser')
  expect(within(dialog).getByLabelText('Моя версия')).toHaveValue('# Browser')
  expect(within(dialog).getByLabelText('Версия на диске')).toHaveValue('# Disk')
  expect(saveNote).toHaveBeenCalledWith(note.id, '# Browser', 'sha256:r1')
})
```

Add complete tests for load disk, two-step overwrite using `sha256:r2`, manual merge using `sha256:r2`, second `note_changed` refreshing disk while preserving the typed merge, transition/settings flush blocked while stale, and deleted dirty note preserving text with no unconditional save.

- [ ] **Step 3: Add RED incoming changed-path tests**

Drive Plan 5's polling success callback with status snapshots and assert:

- A clean active changed note reloads once and adopts its new revision.
- A dirty active changed note opens stale comparison and keeps the browser buffer.
- A changed non-active path refreshes the tree but does not call `getNote`.
- Identical `repository_path + operation_id + path` keys are ignored on repeated polls even if the base was renamed.
- A later path set for the same operation processes only previously unseen paths; already consumed paths are not passed to `applyIncomingPaths` again.
- Paths are inserted into the bounded cache only after `applyIncomingPaths` fulfills; a refresh/note failure leaves every unseen path retryable on the next poll.
- Switching to another base and back does not clear the cache or replay already consumed repository/operation/path keys.
- `syncing` and `conflict` changed paths are not consumed.
- `ready`, `error`, and abort `paused` changed paths are consumed.
- A late old status response cannot overwrite a newer note response because note request tokens still apply.

- [ ] **Step 4: Add RED conflict routing tests**

Assert active status `conflict` clears the save timer, leaves App's dirty buffer in memory, unmounts the normal editor, and mounts `GitConflictWorkspace`. Assert another base's conflict does not route the active base. After accepted complete/abort, drive the accepted operation through the queued response, stale old-conflict polls, and public `syncing` snapshots with queued/running stages; assert the conflict workspace remains mounted. Then publish `ready`, `error`, or `paused` with the accepted `operation_id` and assert editor route, tree refresh, clean-note reload or dirty stale comparison, and retained Plan 5 status indicator. A terminal snapshot for another operation must not exit the conflict workspace.

Add an App integration test that selects another base in `GitConflictWorkspace` and clicks `Переключить базу`. Assert it delegates to the existing `openBase`/`switchBaseSafely` path, preserves its flush-before-switch ordering, mounts the selected base's editor after success, and never calls `resolveGitConflict`, `completeGitConflict`, or `abortGitConflict`. A rejected flush/switch leaves the conflict workspace and its unresolved repository unchanged.

- [ ] **Step 5: Run App tests RED**

Run:

```bash
npm --prefix web test -- --run src/App.test.js
```

Expected: FAIL because App does not track revisions/stale state, consume changed paths, or render conflict workspace.

- [ ] **Step 6: Add revision and stale state**

Add imports and state:

```js
import GitConflictWorkspace from './lib/git/GitConflictWorkspace.svelte'
import { changedPathKey, noteInChangedPaths, terminalChangedPaths } from './lib/git/changed-paths.js'
import StaleNoteDialog from './lib/StaleNoteDialog.svelte'
import { makeStaleNote, readNoteResponse, readSaveResponse } from './lib/stale-note.js'

let noteRevision = $state('')
let staleNote = $state(null)
let pendingConflictOperation = $state(null)
const consumedChangedPaths = new Map() // repository/operation bucket -> Set of full path keys
```

Reset `noteRevision` and `staleNote` in `resetEditorState`. Do not clear `pendingConflictOperation` from generic editor reset code, and never clear `consumedChangedPaths` from editor reset, `applyConfig`, settings navigation, or base switching. Clear the pending operation only after its matching terminal status or a successful explicit switch to another base. On note load:

```js
const loaded = readNoteResponse(await getNote(node.id))
// After all existing token/flush checks:
activeNote = node
ignoreNextChange = true
markdownContent = loaded.content
noteRevision = loaded.revision
dirty = false
```

In the save loop capture `revision = noteRevision`, call `saveNote(noteId, content, revision)`, validate with `readSaveResponse`, and assign the returned revision before deciding whether a newer browser edit requires another loop.

- [ ] **Step 7: Handle note_changed without generic save failure**

Add:

```js
async function loadDiskForStale(noteId, mine) {
  try {
    const disk = readNoteResponse(await getNote(noteId))
    if (!mounted || activeNote?.id !== noteId) return
    staleNote = makeStaleNote({ noteId, mine, disk })
  } catch (error) {
    if (!mounted || activeNote?.id !== noteId) return
    if (error?.status === 404) {
      staleNote = makeStaleNote({ noteId, mine, disk: null })
      return
    }
    throw error
  }
}

async function handleStaleSave(error, noteId, content) {
  if (error?.status !== 409 || error?.code !== 'note_changed') return false
  clearSaveTimer()
  clearStatusTimer()
  saveStatus = 'error'
  dirty = true
  await loadDiskForStale(noteId, markdownContent === content ? content : markdownContent)
  transitionError = 'Заметка изменилась на диске. Выберите безопасное действие.'
  return true
}
```

In the save catch, call this first; rethrow after it returns true so `flushWorkspace` remains blocked. Add `if (error?.code === 'note_changed') return` at the start of `showSaveError`, so existing outer `.catch(showSaveError)` and transition catches cannot replace the stale-specific message with a generic one. The debounce `$effect` must schedule only when `currentNote && staleNote === null && activeGitStatus?.state !== 'conflict'`.

- [ ] **Step 8: Implement stale actions with current revision**

```js
function loadStaleDisk() {
  if (!staleNote) return
  if (staleNote.diskMissing) {
    resetEditorState()
    return
  }
  ignoreNextChange = true
  markdownContent = staleNote.diskContent
  noteRevision = staleNote.diskRevision
  dirty = false
  saveStatus = 'idle'
  transitionError = ''
  staleNote = null
}

async function saveStaleResult({ content, revision }) {
  if (!staleNote || staleNote.diskMissing) throw new Error('Файл отсутствует на диске')
  try {
    const saved = readSaveResponse(await saveNote(staleNote.noteId, content, revision))
    if (!mounted || activeNote?.id !== staleNote.noteId) return
    ignoreNextChange = true
    markdownContent = content
    noteRevision = saved.revision
    dirty = false
    saveStatus = 'saved'
    transitionError = ''
    staleNote = null
  } catch (error) {
    if (error?.status === 409 && error?.code === 'note_changed') {
      const preserved = content
      await loadDiskForStale(staleNote.noteId, preserved)
    }
    throw error
  }
}
```

Pass `saveStaleResult` to both overwrite and manual merge. Render `StaleNoteDialog` only on the editor screen and after `NotesWorkspace`, so `DialogShell` inerts the workspace.

- [ ] **Step 9: Consume Plan 5 status snapshots once**

Extend Plan 5's single `applyGitStatuses` success path:

```js
const CONFLICT_TERMINAL_STATES = new Set(['ready', 'error', 'paused'])

function changedPathBucket(status) {
  return [status.repository_path, status.operation_id].join('\0')
}

function rememberChangedPath(bucket, key) {
  let keys = consumedChangedPaths.get(bucket)
  if (!keys) {
    keys = new Set()
    consumedChangedPaths.set(bucket, keys)
  }
  keys.add(key)
  while (consumedChangedPaths.size > 128) {
    consumedChangedPaths.delete(consumedChangedPaths.keys().next().value)
  }
}

async function applyGitStatuses(statuses) {
  const next = Array.isArray(statuses) ? statuses : []
  gitStatuses = next
  const activeStatus = next.find((status) => status.base === config?.current_base) ?? null
  if (!activeStatus) return

  if (activeStatus.state === 'conflict') {
    clearSaveTimer()
    screen = 'conflict'
    return
  }

  const accepted = pendingConflictOperation?.base === activeStatus.base
    ? pendingConflictOperation
    : null
  if (accepted) {
    const acceptedTerminal = activeStatus.operation_id === accepted.operationId
      && CONFLICT_TERMINAL_STATES.has(activeStatus.state)
    if (!acceptedTerminal) {
      clearSaveTimer()
      screen = 'conflict'
      return
    }
    pendingConflictOperation = null
    screen = 'editor'
  } else if (screen === 'conflict') {
    if (!CONFLICT_TERMINAL_STATES.has(activeStatus.state)) return
    screen = 'editor'
  }

  const bucket = changedPathBucket(activeStatus)
  const consumedKeys = consumedChangedPaths.get(bucket)
  const unseen = terminalChangedPaths(activeStatus)
    .map((path) => ({ path, key: changedPathKey(activeStatus, path) }))
    .filter(({ key }) => key && !consumedKeys?.has(key))
  if (unseen.length === 0) return

  await applyIncomingPaths(activeStatus, unseen.map(({ path }) => path))
  for (const { key } of unseen) rememberChangedPath(bucket, key)
}
```

`applyIncomingPaths` receives only unseen paths. It first awaits an existing `savePromise`, then awaits `notesWorkspace?.refreshTree?.()`. If the active note is not an exact changed path, stop. If it is dirty or stale, fetch disk into stale state without changing `markdownContent`. If clean, token-guard `getNote`; apply content/revision, or reset editor on `404`. On any save wait, tree refresh, or note read failure, set `transitionError` and rethrow so the poller contains/reports the callback failure and the path keys remain absent for retry. Do not mark a key from a partial or rejected application.

The FIFO retains at most 128 repository/operation buckets across the App lifetime and deliberately survives base switches, base renames, path edits, settings round trips, and ordinary rerenders. Each bucket contains full repository/operation/path keys, so an operation with more than 128 paths does not evict and replay its own earlier paths. Repository identity prevents cross-base collisions; operation and path identity allows a later poll for the same operation to process only newly appearing paths.

- [ ] **Step 10: Render conflict workspace and settle accepted operations**

Add to screen routing before editor:

```svelte
{:else if screen === 'conflict'}
  <GitConflictWorkspace
    base={config.current_base}
    bases={config.bases}
    onSwitchBase={openBase}
    onOperationAccepted={(kind, operation) => {
      pendingConflictOperation = {
        base: config.current_base,
        kind,
        operationId: operation.operation_id,
      }
    }}
  />
{:else if screen === 'editor'}
```

The accepted operation identity does not optimistically route to the editor. It pins the conflict route through stale conflict snapshots and that operation's queued/running/`syncing` progression. Only a polled `ready`, `error`, or `paused` snapshot carrying the same `operation_id` clears it and returns to the editor. This handles deduplicated complete/abort responses without mistaking an older terminal operation for completion.

Pass App's existing `openBase` function directly as `onSwitchBase`; do not import or call `switchBase` from `GitConflictWorkspace`. `openBase` remains the sole `switchBaseSafely` path and preserves its existing flush, switch request, config commit, editor reset, error propagation, and transition serialization. In its successful commit, clear `pendingConflictOperation` because the user explicitly left the conflicted repository; do not call a conflict resolution endpoint before or after switching.

After `NotesWorkspace`, render:

```svelte
{#if staleNote}
  <StaleNoteDialog
    stale={staleNote}
    onLoadDisk={loadStaleDisk}
    onOverwrite={saveStaleResult}
    onManualMerge={saveStaleResult}
  />
{/if}
```

- [ ] **Step 11: Preserve all existing transition races**

Update every current App assertion from `saveNote(id, content)` to `saveNote(id, content, revision)`. Keep `savePromise`, note tokens, upload flushing, transition generation, debounce duration, settings flush, switch ordering, rename/delete ordering, and unmount guards intact. Add one regression proving an edit made during a pending revision save uses the returned revision on the next serialized save.

- [ ] **Step 12: Run App and workspace tests GREEN**

Run:

```bash
npm --prefix web test -- --run src/App.test.js src/lib/StaleNoteDialog.test.js src/lib/git/GitConflictWorkspace.test.js src/lib/NotesWorkspace.test.js
```

Expected: PASS; no unhandled rejection, stale buffer loss, duplicate refresh, or editor mount during conflict.

- [ ] **Step 13: Commit serial integration**

```bash
git add web/src/App.svelte web/src/App.test.js
git commit -m "feat: integrate stale notes and git conflicts"
```

### Task 9: Document and Verify the Complete Frontend

**Files:**
- Modify: `docs/user.md`
- Modify: `docs/developer.md`

- [ ] **Step 1: Add exact user workflow documentation**

Add sections that state:

```markdown
## Заметка изменилась на диске

IGoNotes сравнивает revision открытой заметки перед каждым сохранением. Если файл изменился после загрузки, введённый текст остаётся в браузере. Можно загрузить версию с диска, отдельно подтвердить перезапись своей версией или вручную собрать итоговый текст. Перезапись и ручное объединение повторно проверяют актуальную revision.

## Разрешение Git-конфликтов

При незавершённом merge обычный редактор базы заменяется рабочей областью конфликтов. Стороны обозначены как «Общий предок», «На этом устройстве» и «В репозитории». Для текста доступны выбор стороны и ручной итог; для двоичных файлов — выбор стороны или сохранение обеих версий под разными именами; для modify/delete и rename/delete — сохранение существующей версии, ручной текстовый итог или подтверждённое удаление.

«Завершить» доступно только после разрешения всех путей. После принятия «Завершить» или «Отложить» рабочая область остаётся открытой, пока именно эта операция не завершится статусом ready, error или paused. «Отложить» отменяет merge, восстанавливает локальный снимок и оставляет Git-синхронизацию приостановленной. Доступный выбор другой базы использует обычное безопасное переключение приложения и не разрешает конфликт, который остаётся в неактивной базе. Возобновление autosync относится к отдельной реализации circuit breaker и не входит в этот экран.
```

- [ ] **Step 2: Add developer architecture documentation**

Add this section to `docs/developer.md`:

````markdown
## Git conflict frontend state

`App.svelte` owns `noteRevision`, `dirty`, `staleNote`, Git screen routing and a cache bounded to 128 repository/operation buckets. Each bucket stores full `repository_path + operation_id + path` keys; only unseen paths from terminal `ready`, `error` and `paused` statuses are applied, and each key is stored only after successful application. The cache survives base switches and renames, while `syncing` and `conflict` never refresh editor bytes.

Plan 5 remains the sole owner of the status polling timer. Its poller awaits async `applyGitStatuses` and async error reporting, catches callback rejections, serializes explicit refresh with scheduled cycles, and schedules the next cycle only after the current load and callbacks settle.

After complete/abort acceptance, App keeps the conflict workspace mounted through stale conflict snapshots and the accepted operation's queued/running/`syncing` progression. It returns to the editor only when `ready`, `error`, or `paused` carries that accepted `operation_id`. The conflict workspace's accessible base switcher delegates to App's existing serialized `openBase` path and does not resolve or abort the conflict left in the inactive repository.

Every save from the official frontend sends `expected_revision`. A `note_changed` response preserves `markdownContent`, fetches the current disk revision and blocks debounce saves until the stale dialog loads disk, completes a confirmed overwrite or saves a manual merge. Conflict resolution components keep action/path/text state mounted after ordinary API errors and reset only when `conflict.id` changes.

`DialogShell.svelte` owns dialog labeling, inert background reference counting, focus trapping, Escape handling and focus restoration. `Modal.svelte`, the stale-note dialog and conflict abort confirmation reuse it instead of implementing parallel focus managers.

Frontend tests require the same Node.js 24 major as the release workflow:

```bash
npm --prefix web test -- --run
npm --prefix web run build
```
````

- [ ] **Step 3: Run every focused frontend test**

```bash
npm --prefix web test -- --run src/lib/api.test.js src/lib/DialogShell.test.js src/lib/Modal.test.js src/lib/stale-note.test.js src/lib/StaleNoteDialog.test.js src/lib/git/git-status-poller.test.js src/lib/git/changed-paths.test.js src/lib/git/conflict-resolution.test.js src/lib/git/ConflictStagePanel.test.js src/lib/git/TextConflictResolver.test.js src/lib/git/BinaryConflictResolver.test.js src/lib/git/DeleteConflictResolver.test.js src/lib/git/GitConflictWorkspace.test.js src/lib/NotesWorkspace.test.js src/App.test.js
```

Expected: PASS for every listed file with no unhandled errors.

- [ ] **Step 4: Run the complete frontend suite twice**

```bash
npm --prefix web test -- --run
npm --prefix web test -- --run
```

Expected: both runs PASS; deferred polling/path-key and focus tests are stable.

- [ ] **Step 5: Run the production build**

```bash
npm --prefix web run build
```

Expected: exit `0`; no Svelte accessibility, invalid rune/snippet, or unknown prop warning. The existing bundle-size advisory may remain because code splitting is outside this plan.

- [ ] **Step 6: Run repository verification**

```bash
go test ./...
git diff --check
git status --short
```

Expected: Go tests PASS; `git diff --check` emits no output; status lists only intended frontend/docs changes and no generated `web/dist` files unless the repository already tracks an intentional build artifact update.

- [ ] **Step 7: Commit documentation**

```bash
git add docs/user.md docs/developer.md
git commit -m "docs: explain git conflict recovery"
```

## Manual Acceptance Matrix

Run with two temporary clones connected to one bare remote after automated verification:

| Scenario | Required result |
|---|---|
| Clean open note changed by sync | Tree and editor reload once; new revision is used by the next save |
| Dirty open note changed by sync | Browser text remains; stale dialog compares disk and browser |
| Stale load disk | Disk text/revision replace editor; dirty clears |
| Stale overwrite | First click only warns; confirmed save uses latest disk revision |
| Stale manual merge | Typed result survives API failure; successful retry updates editor/revision |
| Repeated status poll | Same repository/operation/path keys cause no second tree/note fetch |
| Expanded status path set | Only newly seen paths for the same repository/operation are applied and then acknowledged |
| Failed changed-path apply | Keys remain unseen and the next poll retries them |
| Switch away and back | Bounded path-key cache survives; old operation paths are not replayed |
| Text content conflict | Local, remote, and manual each resolve exactly one path |
| Binary add/add | Keep-both writes two explicitly named files |
| Modify/delete | Existing side or confirmed deletion resolves the path |
| Rename/delete | Original and renamed paths are visible; alternate final path works |
| Resolution API error | Current conflict, selected action, paths, text, and focus remain |
| All paths resolved | Complete enables; accepted operation stays in conflict workspace through queued/running/syncing |
| Complete success | Editor returns, tree refreshes, active clean note reloads |
| Abort success | Editor returns, restored tree/note refresh, Git status remains paused |
| Terminal status for old operation | Conflict workspace stays mounted until the accepted operation itself is terminal |
| Switch base from conflict | Labeled native selector uses App's serialized switch path; conflict remains unresolved in the inactive base |
| Narrow viewport | Path list, resolver stages, fields, and actions fit at 320 CSS px without horizontal page scroll |
| Keyboard only | Path navigation, resolver actions, dialog focus trap, complete, and abort are operable |

## Self-Review Checklist

- [x] Every approved stale-editor requirement maps to Tasks 1, 3, and 8.
- [x] Every Plan 4 conflict kind/action maps to a resolver and API payload test.
- [x] Incoming `changed_paths` refresh tree and exact active note, with per-repository/operation/path dedupe after successful apply.
- [x] Dirty buffers are never replaced automatically.
- [x] Confirmed overwrite obtains and uses the latest revision.
- [x] Generic dialog extraction preserves every existing `Modal` prop/test.
- [x] Conflict workspace uses explicit ancestor/device/repository labels.
- [x] Resolver state survives API errors and only resets when conflict ID changes.
- [x] Plan 5 polling awaits async status application, contains callback failures, and never overlaps cycles.
- [x] Complete/abort routing waits for the accepted operation's authoritative terminal polled status.
- [x] Conflict base switching uses App's existing serialized switch path without resolving the conflict.
- [x] `App.svelte` integration is serial and no parallel ownership overlaps.
- [x] Svelte 5 `$props`, `$state`, `$derived`, snippets, and exported instance methods match current project patterns.
- [x] Accessibility and 320 px mobile behavior have component and manual coverage.
- [x] Text and delete-family mutually exclusive actions use native radios in labeled fieldsets.
- [x] Autosync breaker/resume implementation is absent.
- [x] No backend, dependency, lockfile, or settings-component edit is included.
- [x] All commands state expected output and every implementation task ends with a focused commit.
