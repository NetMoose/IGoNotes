# Git Settings Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an accessible Git settings experience for every notes base, four-step probe/configuration flow, live status polling, and flush-safe manual synchronization from settings and the editor footer.

**Architecture:** Keep `App.svelte` as the sole owner of the all-base Git status collection, polling lifecycle, and manual-sync boundary; one pure poller calls the Plan 1-3 REST wrappers and fans immutable status snapshots into settings and the active-base footer. Focused Svelte components own Git setup/configuration presentation, while pure JavaScript modules own validation, request construction, and status labels so component tests stay deterministic. `App.svelte` integration is strictly serial after all disjoint components have landed.

**Tech Stack:** Svelte 5.56 runes, JavaScript with JSDoc contracts, Vite 8, Tailwind CSS 4, Vitest 4.1, jsdom 30, Testing Library for Svelte, Node.js 24.

---

## Scope And Dependencies

This is Plan 5 of `docs/superpowers/specs/2026-09-01-git-synchronization-design.md`. Execute it after Plans 1-3 have landed:

- `docs/superpowers/plans/2026-09-01-git-foundation.md` supplies probe, config, disable, and status DTOs/routes.
- `docs/superpowers/plans/2026-09-01-git-worktree-safety.md` supplies the backend worktree/revision safety required before Git mutates an active base. This plan does not consume note revisions in the frontend.
- `docs/superpowers/plans/2026-09-01-git-manual-sync.md` changes configure to `202 Accepted`, adds operation DTOs, initialization, status progress, and manual sync.
- `docs/superpowers/plans/2026-09-01-git-conflict-backend.md` may execute in parallel. Basic settings consume the already-defined public `conflict` status only and do not call conflict routes.

The current frontend baseline is `web/src/App.svelte:1-404`, `web/src/lib/NotesWorkspace.svelte:1-113`, and `web/src/lib/settings/SettingsWorkspace.svelte:1-362`. It uses Svelte 5 runes, direct callback props, `onclick`/`onsubmit`, explicit focus transfer with `tick`, responsive Tailwind breakpoints, and Testing Library queries by accessible role/name. Preserve those patterns.

The release workflow pins Node.js 24 in `.github/workflows/release.yml:18-23`. The planning machine currently reports Node `v20.20.2`; `npm --prefix web test` starts one non-jsdom test but reports twelve worker errors ending in `webidl.util.markAsUncloneable is not a function`. Implementation and verification must use Node 24 and must not change dependencies to support Node 20. Every frontend RED/GREEN command block below starts with the hard major-version assertion and `npm --prefix web ci`; do not run the test command if either prerequisite fails, and do not elide those prerequisites because an earlier task ran them.

Included:

- Frontend wrappers for probe, configure, disable, all/one-base status, and manual sync.
- Strict successful-payload shape checks that raise the existing `ApiError` with `invalid_response`.
- Pure Git URL/branch/schedule/template validation, request construction, config replacement, and status presentation.
- One cancelable, stale-response-safe, all-base status poller owned by `App.svelte`.
- A working Git settings tab with one card per configured notes base.
- A four-step URL/probe, branch, schedule/template, and review/confirmation wizard.
- Consequences for create repository, add/replace origin, create branch, and merge histories, plus every backend warning.
- Live commit-template preview and the supported `{{base}}`, `{{branch}}`, `{{date}}`, `{{datetime}}`, and `{{count}}` variables.
- Configure/reconfigure, disable-with-confirmation, and manual-sync actions.
- Compact active-base Git status/details/manual-sync control in the existing editor footer.
- Mandatory `flushWorkspace()` before every manual Git sync, including a sync started from settings while the editor screen is unmounted.
- Removal of the disabled `Git, скоро` tab and the disabled Git controls/status chips in `BaseForm` and `BaseCard`.

Excluded:

- Revision-aware note saves, `note_changed`, stale-note dialogs, changed-path consumption, or automatic note/tree reloads.
- Conflict list/resolve/complete/abort calls or a conflict workspace.
- Autosync timers in the frontend, circuit-breaker alerts, pause details, resume calls, or resume controls.
- Backend, config schema, Git command, repository, SQLite, Go handler, or Go test changes.
- URL routing, a new state-management library, or a second polling owner.

Scope terminology is strict: `conflict` below means only rendering the existing public status as read-only/unsyncable, and stale-response language means only discarding superseded poll responses. It does not authorize conflict routes or workflows, revision/stale-note behavior, resume controls or calls, or circuit-breaker state/UI.

## Frozen REST And Frontend Contracts

Use these exact wrapper names. Plan 6 and the autosync-resilience plan consume this seam and must not add `getGitStatuses` or another timer:

```js
// web/src/lib/api.js
export function probeGit({ base, git_url, git_branch = '' })
export function configureGit(base, request)
export function disableGit(base)
export function getGitStatus(base = '')
export function syncGit(base)

// web/src/App.svelte
let gitStatuses = $state([])
let activeGitStatus = $derived(
  gitStatuses.find((status) => status.base === config?.current_base) ?? null,
)

function applyGitStatuses(statuses) {
  gitStatuses = Array.isArray(statuses) ? statuses : []
}
```

Exact HTTP requests:

```text
POST   /api/git/probe
PUT    /api/git/config?base=<encodeURIComponent(base)>
DELETE /api/git/config?base=<encodeURIComponent(base)>
GET    /api/git/status
GET    /api/git/status?base=<encodeURIComponent(base)>
POST   /api/git/sync?base=<encodeURIComponent(base)>
```

The Plan 1-3 successful payloads are:

```js
/** @typedef {{
 *   name: string,
 *   path: string,
 *   git_url?: string,
 *   git_branch?: string,
 *   auto_sync: boolean,
 *   auto_sync_interval_minutes?: number,
 *   git_commit_message_template?: string,
 * }} GitBase */

/** @typedef {{
 *   base: string,
 *   repository_path?: string,
 *   state: 'unconfigured'|'initializing'|'ready'|'syncing'|'error'|'paused'|'conflict'|'needs_reconnect',
 *   operation_id?: string,
 *   stage?: string,
 *   ahead: number,
 *   behind: number,
 *   consecutive_failures: number,
 *   last_attempt?: string,
 *   last_success?: string,
 *   changed_paths: string[],
 *   remote_oid?: string,
 *   error?: {code: string, message: string, field?: string},
 * }} GitStatus */

/** @typedef {{operation_id: string, status: string, deduplicated: boolean}} GitOperation */

/** @typedef {{
 *   base: string,
 *   git_version: string,
 *   has_repository: boolean,
 *   repository_root?: string,
 *   repository_root_matches: boolean,
 *   current_branch?: string,
 *   detached_head: boolean,
 *   working_tree_clean: boolean,
 *   existing_origin_url?: string,
 *   remote_branches: string[],
 *   empty_remote: boolean,
 *   pending_operation?: string,
 *   identity_configured: boolean,
 *   history_relation: string,
 *   can_configure: boolean,
 *   required_mutations: {
 *     create_repository: boolean,
 *     add_origin: boolean,
 *     replace_origin: boolean,
 *     create_branch: boolean,
 *     merge_histories: boolean,
 *   },
 *   warnings: string[],
 *   blocking_error?: {code: string, message: string, field?: string},
 * }} GitProbe */

/** @typedef {{base: GitBase, status: GitStatus, operation: GitOperation}} GitConfigResponse */
```

`configureGit` must send all fields, including all four confirmation booleans:

```json
{
  "git_url": "git@github.com:user/work-notes.git",
  "git_branch": "main",
  "auto_sync": true,
  "auto_sync_interval_minutes": 15,
  "git_commit_message_template": "IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)",
  "confirmations": {
    "create_repository": true,
    "replace_origin": false,
    "create_branch": false,
    "merge_histories": true
  }
}
```

Status polling always requests all bases. The optional `base` wrapper argument exists for focused tests and future dialogs. Transient polling errors preserve the last successful `gitStatuses`; only a successful payload calls `applyGitStatuses`.

Probe is a strict two-pass contract. The URL step always calls `probeGit` with `git_branch: ''`, even when a reconfiguration draft contains a branch from the old URL. A normal branchless Plan 1 response has `can_configure: false`, no `blocking_error`, and either a nonempty `remote_branches` array with `empty_remote: false` or an empty array with `empty_remote: true`; it must advance to branch selection. A present `blocking_error`, a mismatched `base`, `can_configure: true` on a branchless response, or a contradictory empty/nonempty branch-discovery result is branch-independent failure and keeps the wizard on the URL step.

After every successful branchless probe, reconcile the draft branch with that response rather than trusting persisted or back-navigation state. For a nonempty remote, retain the branch only when it occurs literally in the newly returned refs, clear it when multiple refs do not contain it, and replace it when exactly one ref is returned. For an empty remote, retain a draft branch only while the normalized URL is unchanged; changing the URL clears the stale branch before the new-branch step. The branch step then calls `probeGit` again with the selected normalized literal branch and may advance to schedule/review only when that selected-branch response has no `blocking_error`, matches the base, and has `can_configure: true`.

## State And Accessibility Invariants

```text
App.svelte owns exactly one GitStatusPoller and one gitStatuses array.
activeGitStatus is derived by exact current-base name, never array position.
Every manual sync awaits flushWorkspace before syncGit.
A failed flush does not call syncGit and leaves the dirty editor buffer intact.
Settings configure/disable replaces only the returned base in config.
Probe and configure errors preserve URL, branch, schedule, template, and confirmations.
URL discovery never submits a persisted/draft branch; it reconciles stale branch state only after receiving the new refs.
Branchless `can_configure: false` without `blocking_error` is discovery success, not a wizard error.
Only a successful selected-branch probe with `can_configure: true` can reach schedule or review.
Only required mutation confirmations gate final submission.
No frontend control calls resume or a conflict endpoint.
The conflict/paused/needs_reconnect states are presented but manual sync is disabled.
Wizard step changes focus the new h1; API field errors focus the corresponding control or alert.
Tabs expose tablist/tab/tabpanel relationships. Their `aria-orientation` is horizontal below `md` and vertical at `md` and above, matching the existing responsive sidebar: Left/Right navigate only horizontally, Up/Down navigate only vertically, and Home/End navigate in both orientations.
Every icon-only button has an accessible name and decorative SVGs use aria-hidden=true.
Every pending action is single-flight, disables competing controls, and ignores late settlement after unmount.
Mobile cards/actions remain one-column; the editor footer can wrap without covering the editor.
```

## File Map

Create:

- `web/src/lib/git/git-settings.js`: pure validation, request construction, config replacement, and status presentation.
- `web/src/lib/git/git-settings.test.js`: table-driven URL/template/status/config tests.
- `web/src/lib/git/git-status-poller.js`: one timer, generation guard, refresh, and cleanup.
- `web/src/lib/git/git-status-poller.test.js`: cadence, retry, stale response, refresh, and stop tests.
- `web/src/lib/git/GitStatusIndicator.svelte`: compact editor-footer status/details/manual-sync UI.
- `web/src/lib/git/GitStatusIndicator.test.js`: all states, details, actions, busy, and responsive semantics.
- `web/src/lib/settings/GitSetupWizard.svelte`: four-step probe/config flow.
- `web/src/lib/settings/GitSetupWizard.test.js`: probe, branch, review, confirmations, focus, busy, and errors.
- `web/src/lib/settings/GitBaseCard.svelte`: per-base Git status/config/sync/disable card.
- `web/src/lib/settings/GitBaseCard.test.js`: status/config/actions and single-flight delegation.
- `web/src/lib/settings/GitSettingsSection.svelte`: all-base grid, wizard routing, and disable confirmation.
- `web/src/lib/settings/GitSettingsSection.test.js`: all-base orchestration, config publication, disable, focus, and errors.

Modify:

- `web/src/lib/api.js:19-83,216`: payload validators and five Git wrappers.
- `web/src/lib/api.test.js:3-22,318-395`: exact methods, encoded queries, bodies, `202`, errors, and malformed success payloads.
- `web/src/lib/setup/BaseForm.svelte:230-247`: remove the disabled Git URL/autosync placeholder block.
- `web/src/lib/setup/BaseForm.test.js`: assert base forms no longer render Git controls.
- `web/src/lib/settings/BaseCard.svelte:33-36`: remove static Git/autosync chips from base-management cards.
- `web/src/lib/settings/SettingsWorkspace.svelte:1-362`: working tabs and Git panel integration.
- `web/src/lib/settings/SettingsWorkspace.test.js:5-17,95-133`: enabled tab, keyboard behavior, Git props, and placeholder removal.
- `web/src/lib/NotesWorkspace.svelte:1-113`: indicator props and responsive footer composition.
- `web/src/lib/NotesWorkspace.test.js:53-101,420-493`: footer states and callback delegation.
- `web/src/test/NotesWorkspaceHost.svelte:7-18`: pass stable Git props through the host.
- `web/src/App.svelte:1-404`: single poller/status owner and serial flush-safe manual sync integration.
- `web/src/App.test.js:10-94,126-404,1002-1055`: polling, stale guards, config updates, and flush order.

Do not modify `web/package.json`, `web/package-lock.json`, `web/src/lib/Editor.svelte`, `web/src/lib/Sidebar.svelte`, any backend file, or documentation outside this plan.

## Parallel Dispatch Waves

All workers begin from the same commit after Plans 1-3 and Node 24 baseline verification. Workers may read any file but edit only their exclusive ownership set. The coordinator reviews every diff before merge and runs the listed focused tests after each wave.

### Wave 0: Frontend Contract Freeze

- Worker 0 executes Task 1 serially.
- Exclusive ownership: `web/src/lib/api.js`, `web/src/lib/api.test.js`, `web/src/lib/git/git-settings.js`, `web/src/lib/git/git-settings.test.js`.
- Merge Task 1 and run its complete focused suite before Wave 1.

### Wave 1: Independent Foundations

Dispatch concurrently after Wave 0:

| Worker | Task | Exclusive ownership |
|---|---|---|
| Worker A | Task 2 | `web/src/lib/git/git-status-poller.js`, `web/src/lib/git/git-status-poller.test.js` |
| Worker B | Task 3 | `web/src/lib/git/GitStatusIndicator.svelte`, `web/src/lib/git/GitStatusIndicator.test.js` |
| Worker C | Task 4 | `web/src/lib/settings/GitSetupWizard.svelte`, `web/src/lib/settings/GitSetupWizard.test.js` |
| Worker D | Task 5 | `web/src/lib/settings/GitBaseCard.svelte`, `web/src/lib/settings/GitBaseCard.test.js` |

### Wave 2: Disjoint UI Integration

- After Workers C and D merge, Worker E executes Task 6 and exclusively owns `GitSettingsSection.svelte` and `GitSettingsSection.test.js`.
- After Worker B merges, Worker F may concurrently execute Task 8 and exclusively owns `NotesWorkspace.svelte`, `NotesWorkspace.test.js`, and `NotesWorkspaceHost.svelte`.
- After Worker E merges, Worker G executes Task 7 and exclusively owns `SettingsWorkspace.svelte`, `SettingsWorkspace.test.js`, `BaseCard.svelte`, `BaseForm.svelte`, and `BaseForm.test.js`.
- Workers F and G may run concurrently because their ownership does not overlap.

### Wave 3: Serial App Integration

- After Tasks 1-8 are merged, the coordinator alone executes Task 9.
- Exclusive ownership: `web/src/App.svelte`, `web/src/App.test.js`.
- No earlier worker may edit either App file. Re-read every integrated child prop before changing App.

### Wave 4: Verification

- The coordinator executes Task 10 without parallel edits.
- Review all merged diffs, run the entire frontend suite/build under Node 24, and scan scope/API names.

After every implementation wave:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run
npm --prefix web run build
```

Expected: the Node assertion and locked install exit `0`, `web/package-lock.json` remains unchanged, Vitest reports no failed file, unhandled worker error, or unhandled rejection, and Vite exits `0` without a Svelte accessibility warning.

### Task 1: Freeze Git API And Pure Presentation Contracts

**Files:**
- Modify: `web/src/lib/api.js:19-83,216`
- Modify: `web/src/lib/api.test.js:3-22,318-395`
- Create: `web/src/lib/git/git-settings.js`
- Create: `web/src/lib/git/git-settings.test.js`

- [ ] **Step 1: Write RED API wrapper tests**

Add the five imports and these tests to `web/src/lib/api.test.js`:

```js
import {
  configureGit,
  disableGit,
  getGitStatus,
  probeGit,
  syncGit,
} from './api.js'

it('uses the exact Git routes, methods, encoded bases, and bodies', async () => {
  const probe = {
    base: 'team/work', git_version: '2.43.0', has_repository: false,
    repository_root_matches: true, detached_head: false, working_tree_clean: true,
    remote_branches: ['main'], empty_remote: false, identity_configured: true,
    history_relation: 'none', can_configure: false,
    required_mutations: {
      create_repository: true, add_origin: true, replace_origin: false,
      create_branch: false, merge_histories: false,
    },
    warnings: ['Git commits include every non-ignored file in the base directory.'],
  }
  const status = {
    base: 'team/work', state: 'initializing', operation_id: 'op-1', stage: 'queued',
    ahead: 0, behind: 0, consecutive_failures: 0, changed_paths: [],
  }
  const operation = { operation_id: 'op-1', status: 'queued', deduplicated: false }
  const savedBase = {
    name: 'team/work', path: '/notes/work', git_url: 'ssh://host/repo',
    git_branch: 'main', auto_sync: false, auto_sync_interval_minutes: 15,
    git_commit_message_template: 'sync {{base}}',
  }
  const request = {
    git_url: 'ssh://host/repo', git_branch: 'main', auto_sync: false,
    auto_sync_interval_minutes: 15, git_commit_message_template: 'sync {{base}}',
    confirmations: {
      create_repository: true, replace_origin: false,
      create_branch: false, merge_histories: false,
    },
  }
  fetchMock
    .mockResolvedValueOnce(jsonResponse(probe))
    .mockResolvedValueOnce(jsonResponse({ base: savedBase, status, operation }, 202))
    .mockResolvedValueOnce(jsonResponse({
      base: { ...savedBase, git_url: '', git_branch: '', auto_sync_interval_minutes: 0 },
      status: { ...status, state: 'unconfigured', operation_id: '', stage: '' },
    }))
    .mockResolvedValueOnce(jsonResponse({ statuses: [status] }))
    .mockResolvedValueOnce(jsonResponse(operation, 202))

  await expect(probeGit({ base: 'team/work', git_url: 'ssh://host/repo' })).resolves.toEqual(probe)
  await expect(configureGit('team/work', request)).resolves.toMatchObject({ operation })
  await expect(disableGit('team/work')).resolves.toMatchObject({ status: { state: 'unconfigured' } })
  await expect(getGitStatus('team/work')).resolves.toEqual({ statuses: [status] })
  await expect(syncGit('team/work')).resolves.toEqual(operation)

  expectJSONRequest(fetchMock, 0, '/api/git/probe', 'POST', {
    base: 'team/work', git_url: 'ssh://host/repo', git_branch: '',
  })
  expectJSONRequest(fetchMock, 1, '/api/git/config?base=team%2Fwork', 'PUT', request)
  expect(requestAt(fetchMock, 2)).toMatchObject({
    path: '/api/git/config?base=team%2Fwork', options: { method: 'DELETE' },
  })
  expect(requestAt(fetchMock, 2).options.body).toBeUndefined()
  expect(requestAt(fetchMock, 3)).toMatchObject({
    path: '/api/git/status?base=team%2Fwork', options: { method: 'GET' },
  })
  expect(requestAt(fetchMock, 4)).toMatchObject({
    path: '/api/git/sync?base=team%2Fwork', options: { method: 'POST' },
  })
  expect(requestAt(fetchMock, 4).options.body).toBeUndefined()
})

it('requests all Git statuses without a query', async () => {
  fetchMock.mockResolvedValue(jsonResponse({ statuses: [] }))
  await expect(getGitStatus()).resolves.toEqual({ statuses: [] })
  expect(requestAt(fetchMock)).toMatchObject({ path: '/api/git/status', options: { method: 'GET' } })
})

it.each([
  ['probe', () => probeGit({ base: 'work', git_url: 'origin' }), {}],
  ['configure', () => configureGit('work', {}), { base: {}, status: {} }],
  ['disable', () => disableGit('work'), { base: {}, status: {} }],
  ['status', () => getGitStatus(), { statuses: [{ base: 42, state: 'ready' }] }],
  ['sync', () => syncGit('work'), { operation_id: '', status: 'queued', deduplicated: false }],
])('rejects a malformed successful %s payload', async (_name, call, payload) => {
  fetchMock.mockResolvedValue(jsonResponse(payload, 200))
  await expect(call()).rejects.toMatchObject({
    name: 'ApiError', status: 200, code: 'invalid_response',
  })
})
```

- [ ] **Step 2: Run API tests RED**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/api.test.js
```

Expected: FAIL to import `probeGit`, `configureGit`, `disableGit`, `getGitStatus`, and `syncGit`.

- [ ] **Step 3: Implement strict successful-payload wrappers**

Add below `jsonBody` in `web/src/lib/api.js`:

```js
function object(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function optionalString(value) {
  return value === undefined || typeof value === 'string'
}

function nonnegativeInteger(value) {
  return Number.isInteger(value) && value >= 0
}

function validAPIError(value) {
  return object(value)
    && typeof value.code === 'string'
    && typeof value.message === 'string'
    && optionalString(value.field)
}

function validBase(value) {
  return object(value)
    && typeof value.name === 'string'
    && typeof value.path === 'string'
    && typeof value.auto_sync === 'boolean'
    && optionalString(value.git_url)
    && optionalString(value.git_branch)
    && (value.auto_sync_interval_minutes === undefined
      || nonnegativeInteger(value.auto_sync_interval_minutes))
    && optionalString(value.git_commit_message_template)
}

function validStatus(value) {
  const states = new Set([
    'unconfigured', 'initializing', 'ready', 'syncing',
    'error', 'paused', 'conflict', 'needs_reconnect',
  ])
  return object(value)
    && typeof value.base === 'string'
    && value.base.length > 0
    && states.has(value.state)
    && optionalString(value.repository_path)
    && optionalString(value.operation_id)
    && optionalString(value.stage)
    && nonnegativeInteger(value.ahead)
    && nonnegativeInteger(value.behind)
    && nonnegativeInteger(value.consecutive_failures)
    && optionalString(value.last_attempt)
    && optionalString(value.last_success)
    && Array.isArray(value.changed_paths)
    && value.changed_paths.every((path) => typeof path === 'string')
    && optionalString(value.remote_oid)
    && (value.error === undefined || validAPIError(value.error))
}

function validOperation(value) {
  return object(value)
    && typeof value.operation_id === 'string'
    && value.operation_id.length > 0
    && typeof value.status === 'string'
    && typeof value.deduplicated === 'boolean'
}

function validProbe(value) {
  const mutations = value?.required_mutations
  return object(value)
    && typeof value.base === 'string'
    && value.base.length > 0
    && typeof value.git_version === 'string'
    && typeof value.has_repository === 'boolean'
    && optionalString(value.repository_root)
    && typeof value.repository_root_matches === 'boolean'
    && optionalString(value.current_branch)
    && typeof value.detached_head === 'boolean'
    && typeof value.working_tree_clean === 'boolean'
    && optionalString(value.existing_origin_url)
    && Array.isArray(value.remote_branches)
    && value.remote_branches.every((branch) => typeof branch === 'string')
    && typeof value.empty_remote === 'boolean'
    && optionalString(value.pending_operation)
    && typeof value.identity_configured === 'boolean'
    && typeof value.history_relation === 'string'
    && typeof value.can_configure === 'boolean'
    && object(mutations)
    && ['create_repository', 'add_origin', 'replace_origin', 'create_branch', 'merge_histories']
      .every((key) => typeof mutations[key] === 'boolean')
    && Array.isArray(value.warnings)
    && value.warnings.every((warning) => typeof warning === 'string')
    && (value.blocking_error === undefined || validAPIError(value.blocking_error))
}

async function requestChecked(path, options, validate) {
  const { status, payload } = await requestWithStatus(path, options)
  if (!validate(payload)) {
    throw new ApiError({
      status,
      code: 'invalid_response',
      message: 'Приложение вернуло некорректный JSON',
    })
  }
  return payload
}
```

Append the wrappers:

```js
export function probeGit({ base, git_url, git_branch = '' }) {
  return requestChecked('/api/git/probe', {
    method: 'POST',
    body: jsonBody({ base, git_url, git_branch }),
  }, validProbe)
}

export function configureGit(base, config) {
  return requestChecked(`/api/git/config?base=${encodeURIComponent(base)}`, {
    method: 'PUT',
    body: jsonBody(config),
  }, (payload) => object(payload)
    && validBase(payload.base)
    && validStatus(payload.status)
    && validOperation(payload.operation))
}

export function disableGit(base) {
  return requestChecked(`/api/git/config?base=${encodeURIComponent(base)}`, {
    method: 'DELETE',
  }, (payload) => object(payload) && validBase(payload.base) && validStatus(payload.status))
}

export function getGitStatus(base = '') {
  const query = base ? `?base=${encodeURIComponent(base)}` : ''
  return requestChecked(`/api/git/status${query}`, { method: 'GET' }, (payload) => (
    object(payload)
    && Array.isArray(payload.statuses)
    && payload.statuses.every(validStatus)
  ))
}

export function syncGit(base) {
  return requestChecked(`/api/git/sync?base=${encodeURIComponent(base)}`, {
    method: 'POST',
  }, validOperation)
}
```

Do not export `requestChecked` or add a plural status alias.

- [ ] **Step 4: Write RED pure-helper tests**

Create `web/src/lib/git/git-settings.test.js`:

```js
import { describe, expect, it } from 'vitest'

import {
  DEFAULT_GIT_COMMIT_TEMPLATE,
  buildGitConfigRequest,
  gitConfigured,
  gitStatusFor,
  presentGitStatus,
  renderGitCommitPreview,
  replaceConfigBase,
  requiredConfirmationKeys,
  validateGitDraft,
} from './git-settings.js'

describe('Git settings helpers', () => {
  it.each([
    ['https://example.com/repo.git', ''],
    ['ssh://git@example.com/repo.git', ''],
    ['git@example.com:repo.git', ''],
    ['../repo.git', ''],
    ['', 'Укажите URL репозитория'],
    ['-upload-pack=evil', 'URL не может начинаться с дефиса'],
    ['https://token@example.com/repo.git', 'Не добавляйте логин или токен в URL'],
    ['https://example.com/repo.git?token=secret', 'URL не должен содержать query или fragment'],
    ['https://example.com/repo.git#main', 'URL не должен содержать query или fragment'],
    ['origin\nnext', 'URL должен быть одной строкой'],
  ])('validates remote %s', (gitURL, expected) => {
    expect(validateGitDraft({ gitURL, branch: 'main', template: 'sync' }).gitURL).toBe(expected)
  })

  it('validates branch, interval, and every template rule', () => {
    expect(validateGitDraft({ gitURL: 'origin', branch: '', template: 'sync' }).branch)
      .toBe('Выберите ветку')
    expect(validateGitDraft({ gitURL: 'origin', branch: '-main', template: 'sync' }).branch)
      .toBe('Ветка не может начинаться с дефиса')
    expect(validateGitDraft({
      gitURL: 'origin', branch: 'main', autoSync: true, interval: 10, template: 'sync',
    }).interval).toBe('Выберите интервал 5, 15, 30 или 60 минут')
    expect(validateGitDraft({ gitURL: 'origin', branch: 'main', template: 'x\ny' }).template)
      .toBe('Шаблон должен быть одной строкой')
    expect(validateGitDraft({ gitURL: 'origin', branch: 'main', template: '{{unknown}}' }).template)
      .toBe('Неизвестная переменная {{unknown}}')
    expect(validateGitDraft({ gitURL: 'origin', branch: 'main', template: '{{base' }).template)
      .toBe('Проверьте парные фигурные скобки')
    expect(validateGitDraft({ gitURL: 'origin', branch: 'main', template: 'x'.repeat(201) }).template)
      .toBe('Шаблон должен содержать не более 200 символов')
  })

  it('renders all supported variables with local RFC 3339 time', () => {
    const at = new Date('2026-09-01T11:30:00.000Z')
    expect(renderGitCommitPreview(
      '{{base}} {{branch}} {{date}} {{datetime}} {{count}}',
      { base: 'work', branch: 'main', count: 3, at },
    )).toMatch(/^work main 2026-09-01 2026-09-01T\d{2}:30:00(?:Z|[+-]\d{2}:\d{2}) 3$/)
  })

  it('builds the exact config request and only confirmed required consequences', () => {
    const probe = { required_mutations: {
      create_repository: true, add_origin: true, replace_origin: false,
      create_branch: false, merge_histories: true,
    } }
    const confirmations = { create_repository: true, merge_histories: true }
    expect(requiredConfirmationKeys(probe)).toEqual(['create_repository', 'merge_histories'])
    expect(buildGitConfigRequest({
      gitURL: ' origin ', branch: ' main ', autoSync: false, interval: 15,
      template: '',
    }, probe, confirmations)).toEqual({
      git_url: 'origin', git_branch: 'main', auto_sync: false,
      auto_sync_interval_minutes: 15,
      git_commit_message_template: DEFAULT_GIT_COMMIT_TEMPLATE,
      confirmations: {
        create_repository: true, replace_origin: false,
        create_branch: false, merge_histories: true,
      },
    })
  })

  it('replaces one returned base without mutating config', () => {
    const config = { current_base: 'work', bases: [
      { name: 'work', path: '/work', auto_sync: false },
      { name: 'home', path: '/home', auto_sync: false },
    ] }
    const saved = { ...config.bases[0], git_url: 'origin', git_branch: 'main' }
    const next = replaceConfigBase(config, saved)
    expect(next.bases).toEqual([saved, config.bases[1]])
    expect(config.bases[0].git_url).toBeUndefined()
  })

  it('finds exact statuses and presents every public state', () => {
    const base = { name: 'work', git_url: 'origin', git_branch: 'main' }
    const statuses = [{ base: 'other', state: 'ready' }, { base: 'work', state: 'syncing' }]
    expect(gitStatusFor('work', statuses)).toBe(statuses[1])
    expect(gitConfigured(base)).toBe(true)
    expect(presentGitStatus(base, statuses[1])).toMatchObject({
      label: 'Выполняется', tone: 'blue', busy: true, canSync: false,
    })
    expect(presentGitStatus(base, { state: 'ready', ahead: 2 })).toMatchObject({
      label: 'Есть локальные изменения', canSync: true,
    })
    expect(presentGitStatus(base, { state: 'conflict' })).toMatchObject({
      label: 'Конфликт', canSync: false,
    })
    expect(presentGitStatus(base, { state: 'paused' })).toMatchObject({
      label: 'Приостановлено', canSync: false,
    })
    expect(presentGitStatus(base, { state: 'needs_reconnect' })).toMatchObject({
      label: 'Требуется переподключение', canSync: false,
    })
    expect(presentGitStatus({ name: 'plain' }, null).label).toBe('Git не настроен')
  })
})
```

- [ ] **Step 5: Run pure-helper tests RED**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/git/git-settings.test.js
```

Expected: FAIL because `git-settings.js` does not exist.

- [ ] **Step 6: Implement complete pure helpers**

Create `web/src/lib/git/git-settings.js`:

```js
export const DEFAULT_GIT_COMMIT_TEMPLATE = 'IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)'
export const GIT_INTERVALS = [5, 15, 30, 60]
export const GIT_TEMPLATE_VARIABLES = ['base', 'branch', 'date', 'datetime', 'count']

const CONFIRMATION_KEYS = [
  'create_repository', 'replace_origin', 'create_branch', 'merge_histories',
]

export function gitConfigured(base) {
  return typeof base?.git_url === 'string' && base.git_url.trim() !== ''
    && typeof base?.git_branch === 'string' && base.git_branch.trim() !== ''
}

function remoteError(value) {
  const remote = String(value ?? '').trim()
  if (!remote) return 'Укажите URL репозитория'
  if (/\0|\r|\n/.test(remote)) return 'URL должен быть одной строкой'
  if (remote.startsWith('-')) return 'URL не может начинаться с дефиса'
  if (/^https?:\/\//i.test(remote)) {
    let parsed
    try {
      parsed = new URL(remote)
    } catch {
      return 'Укажите корректный HTTP(S) URL'
    }
    if (parsed.username || parsed.password) return 'Не добавляйте логин или токен в URL'
    if (parsed.search || parsed.hash) return 'URL не должен содержать query или fragment'
  }
  return ''
}

function branchError(value) {
  const branch = String(value ?? '').trim()
  if (!branch) return 'Выберите ветку'
  if (/\0|\r|\n/.test(branch)) return 'Имя ветки должно быть одной строкой'
  if (branch.startsWith('-')) return 'Ветка не может начинаться с дефиса'
  return ''
}

function templateError(value) {
  const template = String(value ?? '') || DEFAULT_GIT_COMMIT_TEMPLATE
  if (/\0|\r|\n/.test(template)) return 'Шаблон должен быть одной строкой'
  if ([...template].length > 200) return 'Шаблон должен содержать не более 200 символов'
  const stripped = template.replace(/\{\{([^{}]+)\}\}/g, (_token, variable) => {
    return GIT_TEMPLATE_VARIABLES.includes(variable) ? '' : `\u0000${variable}\u0000`
  })
  const unknown = stripped.match(/\u0000([^\u0000]+)\u0000/)
  if (unknown) return `Неизвестная переменная {{${unknown[1]}}}`
  if (stripped.includes('{{') || stripped.includes('}}')) return 'Проверьте парные фигурные скобки'
  return ''
}

export function validateGitDraft(draft) {
  return {
    gitURL: remoteError(draft?.gitURL),
    branch: branchError(draft?.branch),
    interval: draft?.autoSync && !GIT_INTERVALS.includes(Number(draft?.interval))
      ? 'Выберите интервал 5, 15, 30 или 60 минут'
      : '',
    template: templateError(draft?.template),
  }
}

function pad(value) {
  return String(value).padStart(2, '0')
}

function localRFC3339(at) {
  const offset = -at.getTimezoneOffset()
  const suffix = offset === 0
    ? 'Z'
    : `${offset < 0 ? '-' : '+'}${pad(Math.floor(Math.abs(offset) / 60))}:${pad(Math.abs(offset) % 60)}`
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}`
    + `T${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}${suffix}`
}

export function renderGitCommitPreview(template, { base, branch, count = 3, at = new Date() }) {
  const normalized = String(template ?? '') || DEFAULT_GIT_COMMIT_TEMPLATE
  const date = `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}`
  return normalized
    .replaceAll('{{base}}', base)
    .replaceAll('{{branch}}', branch)
    .replaceAll('{{date}}', date)
    .replaceAll('{{datetime}}', localRFC3339(at))
    .replaceAll('{{count}}', String(count))
}

export function requiredConfirmationKeys(probe) {
  const mutations = probe?.required_mutations ?? {}
  return CONFIRMATION_KEYS.filter((key) => mutations[key] === true)
}

export function buildGitConfigRequest(draft, probe, confirmations = {}) {
  const required = new Set(requiredConfirmationKeys(probe))
  return {
    git_url: String(draft.gitURL ?? '').trim(),
    git_branch: String(draft.branch ?? '').trim(),
    auto_sync: Boolean(draft.autoSync),
    auto_sync_interval_minutes: GIT_INTERVALS.includes(Number(draft.interval))
      ? Number(draft.interval)
      : 15,
    git_commit_message_template: String(draft.template ?? '').trim() || DEFAULT_GIT_COMMIT_TEMPLATE,
    confirmations: Object.fromEntries(CONFIRMATION_KEYS.map((key) => [
      key, required.has(key) && confirmations[key] === true,
    ])),
  }
}

export function replaceConfigBase(config, savedBase) {
  return {
    ...config,
    bases: config.bases.map((base) => base.name === savedBase.name ? savedBase : base),
  }
}

export function gitStatusFor(baseName, statuses) {
  return Array.isArray(statuses)
    ? statuses.find((status) => status?.base === baseName) ?? null
    : null
}

export function presentGitStatus(base, status) {
  if (!gitConfigured(base) || status?.state === 'unconfigured') {
    return { label: 'Git не настроен', tone: 'slate', busy: false, canSync: false }
  }
  if (status?.state === 'initializing' || status?.state === 'syncing') {
    return { label: 'Выполняется', tone: 'blue', busy: true, canSync: false }
  }
  if (status?.state === 'ready') {
    return Number(status.ahead) > 0
      ? { label: 'Есть локальные изменения', tone: 'amber', busy: false, canSync: true }
      : { label: 'Синхронизировано', tone: 'green', busy: false, canSync: true }
  }
  if (status?.state === 'error') {
    return { label: 'Ошибка', tone: 'red', busy: false, canSync: true }
  }
  if (status?.state === 'paused') {
    return { label: 'Приостановлено', tone: 'amber', busy: false, canSync: false }
  }
  if (status?.state === 'conflict') {
    return { label: 'Конфликт', tone: 'red', busy: false, canSync: false }
  }
  if (status?.state === 'needs_reconnect') {
    return { label: 'Требуется переподключение', tone: 'amber', busy: false, canSync: false }
  }
  return { label: 'Статус неизвестен', tone: 'slate', busy: false, canSync: false }
}
```

- [ ] **Step 7: Run Task 1 GREEN**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/api.test.js src/lib/git/git-settings.test.js
```

Expected: both files PASS; malformed `200`/`202` payloads become `ApiError(code='invalid_response')`, all query values are encoded, and helper inputs remain unchanged.

- [ ] **Step 8: Commit the frozen frontend contract**

```bash
git add web/src/lib/api.js web/src/lib/api.test.js web/src/lib/git/git-settings.js web/src/lib/git/git-settings.test.js
git commit -m "feat: add git frontend contracts"
```

### Task 2: Add The Single-Owner Git Status Poller

**Files:**
- Create: `web/src/lib/git/git-status-poller.js`
- Create: `web/src/lib/git/git-status-poller.test.js`

- [ ] **Step 1: Write RED poller tests**

Create `web/src/lib/git/git-status-poller.test.js`:

```js
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createGitStatusPoller } from './git-status-poller.js'

function deferred() {
  let resolve
  const promise = new Promise((done) => { resolve = done })
  return { promise, resolve }
}

describe('Git status poller', () => {
  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  it('loads immediately, polls every two seconds, and recovers after an error', async () => {
    vi.useFakeTimers()
    const load = vi.fn()
      .mockResolvedValueOnce({ statuses: [{ base: 'work', state: 'ready' }] })
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce({ statuses: [{ base: 'work', state: 'syncing' }] })
    const onStatuses = vi.fn()
    const onError = vi.fn()
    const poller = createGitStatusPoller({ load, onStatuses, onError })

    poller.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(onStatuses).toHaveBeenLastCalledWith([{ base: 'work', state: 'ready' }])
    expect(onError).toHaveBeenLastCalledWith(null)

    await vi.advanceTimersByTimeAsync(2000)
    expect(onError).toHaveBeenLastCalledWith(expect.objectContaining({ message: 'offline' }))
    expect(onStatuses).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(2000)
    expect(onStatuses).toHaveBeenLastCalledWith([{ base: 'work', state: 'syncing' }])
  })

  it('invalidates an older response when refresh starts a new generation', async () => {
    vi.useFakeTimers()
    const oldRequest = deferred()
    const load = vi.fn()
      .mockReturnValueOnce(oldRequest.promise)
      .mockResolvedValueOnce({ statuses: [{ base: 'work', state: 'syncing' }] })
    const onStatuses = vi.fn()
    const poller = createGitStatusPoller({ load, onStatuses, onError: vi.fn() })

    poller.start()
    const refresh = poller.refresh()
    await refresh
    oldRequest.resolve({ statuses: [{ base: 'work', state: 'ready' }] })
    await oldRequest.promise
    await Promise.resolve()

    expect(onStatuses).toHaveBeenCalledOnce()
    expect(onStatuses).toHaveBeenCalledWith([{ base: 'work', state: 'syncing' }])
  })

  it('stops timers and ignores a late response', async () => {
    vi.useFakeTimers()
    const request = deferred()
    const onStatuses = vi.fn()
    const poller = createGitStatusPoller({
      load: vi.fn(() => request.promise), onStatuses, onError: vi.fn(),
    })
    poller.start()
    poller.stop()
    request.resolve({ statuses: [{ base: 'work', state: 'ready' }] })
    await request.promise
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(10000)
    expect(onStatuses).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run poller tests RED**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/git/git-status-poller.test.js
```

Expected: FAIL because `git-status-poller.js` does not exist.

- [ ] **Step 3: Implement the complete poller**

Create `web/src/lib/git/git-status-poller.js`:

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

  function clearTimer() {
    if (timer !== null) cancel(timer)
    timer = null
  }

  async function run(current) {
    try {
      const payload = await load()
      if (!active || current !== generation) return null
      onStatuses(payload.statuses)
      onError(null)
      return payload.statuses
    } catch (error) {
      if (!active || current !== generation) return null
      onError(error)
      return null
    } finally {
      if (active && current === generation) {
        timer = schedule(() => { void run(current) }, interval)
      }
    }
  }

  function start() {
    clearTimer()
    active = true
    generation += 1
    void run(generation)
  }

  function refresh() {
    clearTimer()
    if (!active) return Promise.resolve(null)
    generation += 1
    return run(generation)
  }

  function stop() {
    active = false
    generation += 1
    clearTimer()
  }

  return { start, refresh, stop }
}
```

The poller never imports `api.js`, never stores statuses, and never clears the consumer's last successful data on error.

- [ ] **Step 4: Run poller tests GREEN**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/git/git-status-poller.test.js
```

Expected: PASS; fake timers report one timer only, errors retry after exactly 2000 ms, and stale/late responses invoke no callback.

- [ ] **Step 5: Commit the poller**

```bash
git add web/src/lib/git/git-status-poller.js web/src/lib/git/git-status-poller.test.js
git commit -m "feat: add git status poller"
```

### Task 3: Add The Compact Git Status Indicator

**Files:**
- Create: `web/src/lib/git/GitStatusIndicator.svelte`
- Create: `web/src/lib/git/GitStatusIndicator.test.js`

This component is presentation-only. It never imports `api.js`, never polls, and receives the exact active-base status from `App.svelte`.

- [ ] **Step 1: Write RED indicator tests**

Create `web/src/lib/git/GitStatusIndicator.test.js`:

```js
import { render, screen, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import GitStatusIndicator from './GitStatusIndicator.svelte'

const base = {
  name: 'work', path: '/notes/work', git_url: 'origin', git_branch: 'main',
  auto_sync: false,
}

function renderIndicator(status, overrides = {}) {
  const props = {
    base,
    status: { base: 'work', ahead: 0, behind: 0, changed_paths: [], ...status },
    busy: false,
    error: '',
    onSync: vi.fn(),
    ...overrides,
  }
  return { props, ...render(GitStatusIndicator, props) }
}

describe('GitStatusIndicator', () => {
  it.each([
    ['ready', {}, 'Синхронизировано'],
    ['ready', { ahead: 2 }, 'Есть локальные изменения'],
    ['syncing', { stage: 'push' }, 'Выполняется'],
    ['error', { error: { message: 'Сеть недоступна' } }, 'Ошибка'],
    ['paused', {}, 'Приостановлено'],
    ['conflict', {}, 'Конфликт'],
    ['needs_reconnect', {}, 'Требуется переподключение'],
  ])('presents %s without inventing another state owner', (state, extra, label) => {
    renderIndicator({ state, ...extra })
    expect(screen.getByRole('button', { name: `Открыть детали Git: ${label}` })).toBeVisible()
  })

  it('opens details, renders progress and delegates the exact base name', async () => {
    const user = userEvent.setup()
    const { props } = renderIndicator({
      state: 'ready', ahead: 2, behind: 1, stage: 'fetch',
      last_success: '2026-09-01T11:30:00Z',
    })

    await user.click(screen.getByRole('button', {
      name: 'Открыть детали Git: Есть локальные изменения',
    }))

    const region = screen.getByRole('region', { name: 'Детали Git' })
    expect(region).toHaveTextContent('main')
    expect(region).toHaveTextContent('Впереди: 2')
    expect(region).toHaveTextContent('Позади: 1')
    expect(region).toHaveTextContent('Этап: fetch')
    await user.click(within(region).getByRole('button', { name: 'Синхронизировать Git' }))
    expect(props.onSync).toHaveBeenCalledOnce()
    expect(props.onSync).toHaveBeenCalledWith('work')
  })

  it.each(['paused', 'conflict', 'needs_reconnect', 'syncing'])(
    'disables manual sync for %s',
    async (state) => {
      const user = userEvent.setup()
      renderIndicator({ state })
      await user.click(screen.getByRole('button', { name: /Открыть детали Git:/ }))
      expect(screen.getByRole('button', { name: 'Синхронизировать Git' })).toBeDisabled()
    },
  )

  it('shows action errors, busy state, and a viewport-bounded details surface', async () => {
    const user = userEvent.setup()
    renderIndicator({ state: 'ready' }, { busy: true, error: 'Не удалось запустить Git' })
    await user.click(screen.getByRole('button', { name: /Открыть детали Git:/ }))
    const region = screen.getByRole('region', { name: 'Детали Git' })
    expect(within(region).getByRole('alert')).toHaveTextContent('Не удалось запустить Git')
    expect(within(region).getByRole('button', { name: 'Синхронизировать Git' }))
      .toHaveAttribute('aria-busy', 'true')
    expect(region).toHaveClass('w-[min(22rem,calc(100vw-1.5rem))]')
  })
})
```

- [ ] **Step 2: Run indicator tests RED under Node 24**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/git/GitStatusIndicator.test.js
```

Expected: the Node gate and locked install exit `0`; Vitest FAILS because `GitStatusIndicator.svelte` does not exist.

- [ ] **Step 3: Implement the complete indicator**

Create `web/src/lib/git/GitStatusIndicator.svelte`:

```svelte
<script>
  import { presentGitStatus } from './git-settings.js'

  let {
    base,
    status = null,
    busy = false,
    error = '',
    onSync,
  } = $props()

  let open = $state(false)
  let presentation = $derived(presentGitStatus(base, status))

  const toneClasses = {
    slate: 'bg-slate-200 text-slate-700',
    blue: 'bg-blue-100 text-blue-700',
    green: 'bg-emerald-100 text-emerald-700',
    amber: 'bg-amber-100 text-amber-800',
    red: 'bg-red-100 text-red-700',
  }
</script>

<div class="relative min-w-0 text-xs">
  <button
    type="button"
    aria-label={`Открыть детали Git: ${presentation.label}`}
    aria-expanded={open}
    onclick={() => open = !open}
    class={`flex max-w-full items-center gap-1.5 rounded px-2 py-1 font-semibold focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 ${toneClasses[presentation.tone]}`}
  >
    <span class={`size-2 shrink-0 rounded-full ${presentation.busy ? 'animate-pulse bg-current' : 'bg-current'}`}></span>
    <span class="truncate">{presentation.label}</span>
  </button>

  {#if open}
    <section
      role="region"
      aria-label="Детали Git"
      class="absolute bottom-full right-0 z-30 mb-2 w-[min(22rem,calc(100vw-1.5rem))] rounded-xl border border-slate-200 bg-white p-4 text-slate-700 shadow-xl"
    >
      <div class="flex items-start justify-between gap-3">
        <div class="min-w-0">
          <p class="font-bold text-slate-950">Git: {base.name}</p>
          <p class="mt-1 break-all font-mono text-[11px]">{base.git_branch || 'Ветка не выбрана'}</p>
        </div>
        <button
          type="button"
          aria-label="Закрыть детали Git"
          onclick={() => open = false}
          class="rounded p-1 text-slate-500 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
        >
          <span aria-hidden="true">x</span>
        </button>
      </div>

      <dl class="mt-3 grid grid-cols-2 gap-x-4 gap-y-1">
        <div><dt class="inline text-slate-500">Впереди:</dt> <dd class="inline">{status?.ahead ?? 0}</dd></div>
        <div><dt class="inline text-slate-500">Позади:</dt> <dd class="inline">{status?.behind ?? 0}</dd></div>
        {#if status?.stage}
          <div class="col-span-2"><dt class="inline text-slate-500">Этап:</dt> <dd class="inline">{status.stage}</dd></div>
        {/if}
        {#if status?.last_success}
          <div class="col-span-2"><dt class="inline text-slate-500">Последний успех:</dt> <dd class="inline">{status.last_success}</dd></div>
        {/if}
      </dl>

      {#if status?.error?.message}
        <p class="mt-3 rounded-lg bg-red-50 px-3 py-2 text-red-700">{status.error.message}</p>
      {/if}
      {#if error}
        <p role="alert" class="mt-3 rounded-lg bg-red-50 px-3 py-2 text-red-700">{error}</p>
      {/if}

      <button
        type="button"
        onclick={() => onSync(base.name)}
        disabled={busy || !presentation.canSync}
        aria-busy={busy ? 'true' : 'false'}
        class="mt-4 w-full rounded-lg bg-blue-600 px-3 py-2 font-semibold text-white focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
      >
        Синхронизировать Git
      </button>
    </section>
  {/if}
</div>
```

Do not add a resume button, conflict navigation, operation timer, or API import.

- [ ] **Step 4: Run indicator tests GREEN**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/git/GitStatusIndicator.test.js
```

Expected: PASS with seven public states, exact callback delegation, disabled unsafe states, an action alert, and no accessibility warning.

- [ ] **Step 5: Commit the indicator**

```bash
git add web/src/lib/git/GitStatusIndicator.svelte web/src/lib/git/GitStatusIndicator.test.js
git commit -m "feat: add git status indicator"
```

### Task 4: Build The Four-Step Git Setup Wizard

**Files:**
- Create: `web/src/lib/settings/GitSetupWizard.svelte`
- Create: `web/src/lib/settings/GitSetupWizard.test.js`

The wizard owns only its draft and the probe/configure requests. The parent publishes the returned base into application config and requests a poll refresh through `onConfigured`.

- [ ] **Step 1: Write RED wizard tests**

Create `web/src/lib/settings/GitSetupWizard.test.js` with the exact API mock and fixtures below:

```js
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual, probeGit: vi.fn(), configureGit: vi.fn() }
})

import { ApiError, configureGit, probeGit } from '../api.js'
import GitSetupWizard from './GitSetupWizard.svelte'

const base = { name: 'work', path: '/notes/work', auto_sync: false }
const branchlessProbe = {
  base: 'work', git_version: '2.48.0', has_repository: false,
  repository_root_matches: true, detached_head: false, working_tree_clean: true,
  remote_branches: ['main', 'release'], empty_remote: false,
  identity_configured: true, history_relation: 'unknown', can_configure: false,
  required_mutations: {
    create_repository: false, add_origin: false, replace_origin: false,
    create_branch: false, merge_histories: false,
  },
  warnings: [],
}
const probe = {
  ...branchlessProbe,
  history_relation: 'unrelated', can_configure: true,
  required_mutations: {
    create_repository: true, add_origin: true, replace_origin: false,
    create_branch: false, merge_histories: true,
  },
  warnings: [
    'Git включит все неигнорируемые файлы базы.',
    'Локальная и удаленная истории будут объединены.',
  ],
}
const status = {
  base: 'work', state: 'initializing', ahead: 0, behind: 0,
  consecutive_failures: 0, changed_paths: [],
}
const response = {
  base: {
    ...base, git_url: 'origin', git_branch: 'main', auto_sync: true,
    auto_sync_interval_minutes: 15,
    git_commit_message_template: 'sync {{base}} {{count}}',
  },
  status,
  operation: { operation_id: 'op-1', status: 'queued', deduplicated: false },
}

function renderWizard(overrides = {}) {
  const props = {
    base,
    onConfigured: vi.fn(),
    onCancel: vi.fn(),
    onBusyChange: vi.fn(),
    ...overrides,
  }
  return { props, ...render(GitSetupWizard, props) }
}

async function reachSchedule(user) {
  await user.type(screen.getByLabelText('URL репозитория'), 'origin')
  await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))
  await user.selectOptions(screen.getByLabelText('Ветка'), 'main')
  await user.click(screen.getByRole('button', { name: 'Продолжить' }))
}

describe('GitSetupWizard', () => {
  beforeEach(() => {
    vi.mocked(probeGit).mockReset().mockImplementation(({ git_branch }) => (
      Promise.resolve(git_branch ? probe : branchlessProbe)
    ))
    vi.mocked(configureGit).mockReset().mockResolvedValue(response)
  })

  it('probes URL, re-probes the selected branch, and focuses each step heading', async () => {
    const user = userEvent.setup()
    renderWizard()
    await user.type(screen.getByLabelText('URL репозитория'), ' origin ')
    await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))

    expect(probeGit).toHaveBeenNthCalledWith(1, {
      base: 'work', git_url: 'origin', git_branch: '',
    })
    expect(branchlessProbe.can_configure).toBe(false)
    const branchHeading = screen.getByRole('heading', { name: 'Шаг 2 из 4: ветка' })
    await waitFor(() => expect(branchHeading).toHaveFocus())
    await user.selectOptions(screen.getByLabelText('Ветка'), 'main')
    await user.click(screen.getByRole('button', { name: 'Продолжить' }))
    expect(probeGit).toHaveBeenNthCalledWith(2, {
      base: 'work', git_url: 'origin', git_branch: 'main',
    })
    await waitFor(() => expect(
      screen.getByRole('heading', { name: 'Шаг 3 из 4: расписание и коммиты' }),
    ).toHaveFocus())
  })

  it('clears and replaces stale branches after Back and URL changes', async () => {
    const user = userEvent.setup()
    vi.mocked(probeGit)
      .mockResolvedValueOnce(branchlessProbe)
      .mockResolvedValueOnce({ ...branchlessProbe, remote_branches: ['release'] })
    renderWizard({
      base: { ...base, git_url: 'origin-one', git_branch: 'legacy' },
    })

    await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))
    expect(probeGit).toHaveBeenNthCalledWith(1, {
      base: 'work', git_url: 'origin-one', git_branch: '',
    })
    expect(screen.getByLabelText('Ветка')).toHaveValue('')
    await user.selectOptions(screen.getByLabelText('Ветка'), 'main')
    await user.click(screen.getByRole('button', { name: 'Назад' }))
    const url = screen.getByLabelText('URL репозитория')
    await user.clear(url)
    await user.type(url, 'origin-two')
    await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))

    expect(probeGit).toHaveBeenNthCalledWith(2, {
      base: 'work', git_url: 'origin-two', git_branch: '',
    })
    expect(screen.getByLabelText('Ветка')).toHaveValue('release')
  })

  it.each([
    [
      'a blocking error',
      {
        ...branchlessProbe,
        blocking_error: { code: 'auth_failed', message: 'Доступ запрещен', field: 'git_branch' },
      },
      'Доступ запрещен',
    ],
    [
      'an invalid branch-independent discovery response',
      { ...branchlessProbe, remote_branches: [] },
      'Сервер вернул некорректный ответ при поиске веток',
    ],
    [
      'a branchless response that claims it can configure',
      { ...branchlessProbe, can_configure: true },
      'Сервер вернул некорректный ответ при поиске веток',
    ],
  ])('stays on the URL step for %s', async (_name, result, expected) => {
    const user = userEvent.setup()
    vi.mocked(probeGit).mockResolvedValue(result)
    renderWizard()
    await user.type(screen.getByLabelText('URL репозитория'), 'origin')
    await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(expected)
    expect(screen.getByRole('heading', { name: 'Шаг 1 из 4: репозиторий' })).toBeVisible()
    expect(screen.queryByRole('heading', { name: 'Шаг 2 из 4: ветка' })).not.toBeInTheDocument()
  })

  it('requires can_configure true from the selected literal branch probe', async () => {
    const user = userEvent.setup()
    vi.mocked(probeGit).mockImplementation(({ git_branch }) => Promise.resolve(
      git_branch ? { ...probe, can_configure: false } : branchlessProbe,
    ))
    renderWizard()
    await user.type(screen.getByLabelText('URL репозитория'), 'origin')
    await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))
    await user.selectOptions(screen.getByLabelText('Ветка'), 'main')
    await user.click(screen.getByRole('button', { name: 'Продолжить' }))

    expect(probeGit).toHaveBeenNthCalledWith(2, {
      base: 'work', git_url: 'origin', git_branch: 'main',
    })
    expect(await screen.findByRole('alert')).toHaveTextContent('Репозиторий нельзя настроить')
    expect(screen.getByRole('heading', { name: 'Шаг 2 из 4: ветка' })).toBeVisible()
    expect(screen.queryByRole('heading', {
      name: 'Шаг 3 из 4: расписание и коммиты',
    })).not.toBeInTheDocument()
  })

  it('supports an empty remote branch, validates schedule/template, and previews every variable', async () => {
    const user = userEvent.setup()
    const emptyDiscovery = {
      ...branchlessProbe,
      empty_remote: true,
      remote_branches: [],
    }
    const emptyBranchProbe = {
      ...probe,
      empty_remote: true,
      remote_branches: [],
      required_mutations: { ...probe.required_mutations, create_branch: true },
    }
    vi.mocked(probeGit).mockImplementation(({ git_branch }) => (
      Promise.resolve(git_branch ? emptyBranchProbe : emptyDiscovery)
    ))
    renderWizard()
    await user.type(screen.getByLabelText('URL репозитория'), 'origin')
    await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))
    await user.type(screen.getByLabelText('Новая ветка'), 'main')
    await user.click(screen.getByRole('button', { name: 'Продолжить' }))

    await user.click(screen.getByRole('radio', { name: 'Автоматически' }))
    await user.selectOptions(screen.getByLabelText('Интервал'), '15')
    const template = screen.getByLabelText('Шаблон сообщения коммита')
    await user.clear(template)
    await user.type(template, '{{base}} {{branch}} {{date}} {{datetime}} {{count}}')
    expect(screen.getByLabelText('Предпросмотр сообщения')).toHaveTextContent(/work main \d{4}-\d{2}-\d{2}/)
    expect(screen.getByText('{{base}}, {{branch}}, {{date}}, {{datetime}}, {{count}}')).toBeVisible()
  })

  it('shows all consequences and warnings, gates required confirmations, and submits exact fields', async () => {
    const user = userEvent.setup()
    const { props } = renderWizard()
    await reachSchedule(user)
    await user.click(screen.getByRole('radio', { name: 'Автоматически' }))
    await user.selectOptions(screen.getByLabelText('Интервал'), '15')
    const template = screen.getByLabelText('Шаблон сообщения коммита')
    await user.clear(template)
    await user.type(template, 'sync {{base}} {{count}}')
    await user.click(screen.getByRole('button', { name: 'Проверить настройки' }))

    const review = screen.getByRole('region', { name: 'Проверка Git-настроек' })
    expect(review).toHaveTextContent('Создать Git-репозиторий')
    expect(review).toHaveTextContent('Добавить origin')
    expect(review).toHaveTextContent('Объединить несвязанные истории')
    for (const warning of probe.warnings) expect(review).toHaveTextContent(warning)
    expect(within(review).queryByText('Заменить origin')).not.toBeInTheDocument()

    await user.click(within(review).getByRole('button', { name: 'Сохранить Git-настройки' }))
    expect(await within(review).findByRole('alert')).toHaveTextContent('Подтвердите обязательные последствия')
    expect(configureGit).not.toHaveBeenCalled()

    await user.click(within(review).getByRole('checkbox', { name: /Создать Git-репозиторий/ }))
    await user.click(within(review).getByRole('checkbox', { name: /Объединить несвязанные истории/ }))
    await user.click(within(review).getByRole('button', { name: 'Сохранить Git-настройки' }))

    expect(configureGit).toHaveBeenCalledWith('work', {
      git_url: 'origin', git_branch: 'main', auto_sync: true,
      auto_sync_interval_minutes: 15,
      git_commit_message_template: 'sync {{base}} {{count}}',
      confirmations: {
        create_repository: true, replace_origin: false,
        create_branch: false, merge_histories: true,
      },
    })
    await waitFor(() => expect(props.onConfigured).toHaveBeenCalledWith(response))
  })

  it('renders every possible backend mutation consequence', async () => {
    const user = userEvent.setup()
    vi.mocked(probeGit).mockImplementation(({ git_branch }) => Promise.resolve(
      git_branch
        ? {
            ...probe,
            required_mutations: {
              create_repository: true, add_origin: true, replace_origin: true,
              create_branch: true, merge_histories: true,
            },
          }
        : branchlessProbe,
    ))
    renderWizard()
    await reachSchedule(user)
    await user.click(screen.getByRole('button', { name: 'Проверить настройки' }))
    const review = screen.getByRole('region', { name: 'Проверка Git-настроек' })
    for (const label of [
      'Создать Git-репозиторий', 'Добавить origin', 'Заменить origin',
      'Создать ветку', 'Объединить несвязанные истории',
    ]) expect(review).toHaveTextContent(label)
  })

  it('preserves the whole draft and focuses a field after configure rejection', async () => {
    const user = userEvent.setup()
    vi.mocked(configureGit).mockRejectedValue(new ApiError({
      field: 'git_branch', message: 'Ветка больше не существует',
    }))
    renderWizard()
    await reachSchedule(user)
    await user.click(screen.getByRole('button', { name: 'Проверить настройки' }))
    await user.click(screen.getByRole('checkbox', { name: /Создать Git-репозиторий/ }))
    await user.click(screen.getByRole('checkbox', { name: /Объединить несвязанные истории/ }))
    await user.click(screen.getByRole('button', { name: 'Сохранить Git-настройки' }))

    const branch = await screen.findByLabelText('Ветка')
    expect(branch).toHaveValue('main')
    expect(screen.getByText('Ветка больше не существует')).toBeVisible()
    await waitFor(() => expect(branch).toHaveFocus())
  })

  it('is single-flight and ignores late probe settlement after unmount', async () => {
    const request = Promise.withResolvers()
    vi.mocked(probeGit).mockReturnValue(request.promise)
    const user = userEvent.setup()
    const result = renderWizard()
    await user.type(screen.getByLabelText('URL репозитория'), 'origin')
    const submit = screen.getByRole('button', { name: 'Проверить репозиторий' })
    await fireEvent.click(submit)
    await fireEvent.click(submit)
    expect(probeGit).toHaveBeenCalledOnce()
    expect(submit).toHaveAttribute('aria-busy', 'true')
    expect(result.props.onBusyChange).toHaveBeenCalledWith(true)
    result.unmount()
    request.resolve(branchlessProbe)
    await request.promise
    expect(result.props.onConfigured).not.toHaveBeenCalled()
    expect(result.props.onBusyChange).toHaveBeenLastCalledWith(false)
  })
})
```

- [ ] **Step 2: Run wizard tests RED under Node 24**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/settings/GitSetupWizard.test.js
```

Expected: FAIL because `GitSetupWizard.svelte` does not exist.

- [ ] **Step 3: Implement wizard state, request boundaries, and focus mapping**

Create `web/src/lib/settings/GitSetupWizard.svelte`. Use this complete script; it is the canonical state/API contract for the markup in the next step:

```svelte
<script>
  import { onDestroy, tick } from 'svelte'

  import { configureGit, probeGit } from '../api.js'
  import {
    DEFAULT_GIT_COMMIT_TEMPLATE,
    GIT_INTERVALS,
    buildGitConfigRequest,
    renderGitCommitPreview,
    requiredConfirmationKeys,
    validateGitDraft,
  } from '../git/git-settings.js'

  let { base, onConfigured, onCancel, onBusyChange = () => {} } = $props()

  let step = $state(1)
  let draft = $state({
    gitURL: base.git_url || '',
    branch: base.git_branch || '',
    autoSync: base.auto_sync === true,
    interval: base.auto_sync_interval_minutes || 15,
    template: base.git_commit_message_template || DEFAULT_GIT_COMMIT_TEMPLATE,
  })
  let probe = $state(null)
  let confirmations = $state({})
  let errors = $state({ gitURL: '', branch: '', interval: '', template: '' })
  let apiError = $state('')
  let busy = $state(false)
  let heading = $state()
  let urlInput = $state()
  let branchInput = $state()
  let scheduleInput = $state()
  let intervalInput = $state()
  let templateInput = $state()
  let alert = $state()
  let active = true
  let branchURL = String(base.git_url ?? '').trim()

  let requiredKeys = $derived(requiredConfirmationKeys(probe))
  let preview = $derived(renderGitCommitPreview(draft.template, {
    base: base.name,
    branch: draft.branch || 'main',
    count: 3,
  }))

  const mutationLabels = {
    create_repository: 'Создать Git-репозиторий',
    add_origin: 'Добавить origin',
    replace_origin: 'Заменить origin',
    create_branch: 'Создать ветку',
    merge_histories: 'Объединить несвязанные истории',
  }

  onDestroy(() => {
    active = false
    onBusyChange(false)
  })

  function message(error, fallback) {
    return typeof error?.message === 'string' && error.message ? error.message : fallback
  }

  function validBranchDiscovery(result) {
    if (result?.base !== base.name || result.can_configure !== false) return false
    if (!Array.isArray(result.remote_branches)) return false
    return result.empty_remote === true
      ? result.remote_branches.length === 0
      : result.empty_remote === false && result.remote_branches.length > 0
  }

  function setBusy(value) {
    busy = value
    onBusyChange(value)
  }

  async function focusStep(nextStep, target = null) {
    if (!active) return
    step = nextStep
    await tick()
    if (!active || step !== nextStep) return
    ;(target?.() || heading)?.focus()
  }

  function clearError(field) {
    errors = { ...errors, [field]: '' }
    apiError = ''
  }

  async function focusAPIError(error) {
    apiError = message(error, 'Не удалось сохранить Git-настройки')
    const targets = {
      git_url: [1, () => urlInput],
      git_branch: [2, () => branchInput],
      auto_sync: [3, () => scheduleInput],
      auto_sync_interval_minutes: [3, () => intervalInput],
      git_commit_message_template: [3, () => templateInput],
    }
    const target = targets[error?.field]
    if (target) {
      await focusStep(target[0], target[1])
      return
    }
    await tick()
    if (active) alert?.focus()
  }

  async function finishRequestError(error) {
    if (!active) return
    setBusy(false)
    await focusAPIError(error)
  }

  async function finishDiscoveryError(error) {
    if (!active) return
    setBusy(false)
    apiError = message(error, 'Не удалось проверить репозиторий')
    await tick()
    if (!active) return
    ;(error?.field === 'git_url' ? urlInput : alert)?.focus()
  }

  async function inspectURL() {
    if (!active || busy) return
    const validation = validateGitDraft({ ...draft, branch: draft.branch || 'probe' })
    errors = { ...errors, gitURL: validation.gitURL }
    if (validation.gitURL) {
      await tick()
      urlInput?.focus()
      return
    }
    setBusy(true)
    apiError = ''
    try {
      const normalizedURL = draft.gitURL.trim()
      const result = await probeGit({
        base: base.name,
        git_url: normalizedURL,
        git_branch: '',
      })
      if (!active) return
      if (result.blocking_error) {
        await finishDiscoveryError(result.blocking_error)
        return
      }
      if (!validBranchDiscovery(result)) {
        await finishDiscoveryError(new Error('Сервер вернул некорректный ответ при поиске веток'))
        return
      }
      probe = result
      confirmations = {}
      errors = { ...errors, branch: '' }
      const selected = draft.branch.trim()
      if (result.empty_remote) {
        if (normalizedURL !== branchURL) draft.branch = ''
      } else if (!result.remote_branches.includes(selected)) {
        draft.branch = result.remote_branches.length === 1 ? result.remote_branches[0] : ''
      }
      branchURL = normalizedURL
      await focusStep(2)
    } catch (error) {
      await finishDiscoveryError(error)
    } finally {
      if (active && busy) setBusy(false)
    }
  }

  async function inspectBranch() {
    if (!active || busy) return
    const validation = validateGitDraft(draft)
    errors = { ...errors, branch: validation.branch }
    if (validation.branch) {
      await tick()
      branchInput?.focus()
      return
    }
    setBusy(true)
    apiError = ''
    try {
      const result = await probeGit({
        base: base.name,
        git_url: draft.gitURL.trim(),
        git_branch: draft.branch.trim(),
      })
      if (!active) return
      if (result.base !== base.name) {
        await finishRequestError(new Error('Сервер вернул некорректный ответ проверки ветки'))
        return
      }
      if (result.blocking_error || result.can_configure !== true) {
        await finishRequestError(result.blocking_error || new Error('Репозиторий нельзя настроить'))
        return
      }
      probe = result
      confirmations = {}
      await focusStep(3)
    } catch (error) {
      await finishRequestError(error)
    } finally {
      if (active && busy) setBusy(false)
    }
  }

  async function review() {
    const validation = validateGitDraft(draft)
    errors = validation
    const first = ['interval', 'template'].find((field) => validation[field])
    if (first) {
      await tick()
      ;(first === 'interval' ? intervalInput : templateInput)?.focus()
      return
    }
    apiError = ''
    await focusStep(4)
  }

  async function submit() {
    if (!active || busy) return
    if (requiredKeys.some((key) => confirmations[key] !== true)) {
      apiError = 'Подтвердите обязательные последствия'
      await tick()
      alert?.focus()
      return
    }
    setBusy(true)
    apiError = ''
    try {
      const response = await configureGit(
        base.name,
        buildGitConfigRequest(draft, probe, confirmations),
      )
      if (!active) return
      await onConfigured(response)
    } catch (error) {
      await finishRequestError(error)
    } finally {
      if (active && busy) setBusy(false)
    }
  }
</script>
```

- [ ] **Step 4: Add the complete four-step accessible markup**

Append this markup in the same `GitSetupWizard.svelte` file:

```svelte
<section class="w-full max-w-3xl">
  <p class="text-sm font-semibold text-blue-700">Git для {base.name}</p>

  {#if step === 1}
    <h1 bind:this={heading} tabindex="-1" class="mt-2 text-3xl font-bold outline-none">
      Шаг 1 из 4: репозиторий
    </h1>
    <form class="mt-6 space-y-5" onsubmit={(event) => { event.preventDefault(); inspectURL() }}>
      <div>
        <label for="git-url" class="block text-sm font-semibold">URL репозитория</label>
        <input
          id="git-url"
          bind:this={urlInput}
          bind:value={draft.gitURL}
          oninput={() => clearError('gitURL')}
          disabled={busy}
          aria-invalid={errors.gitURL ? 'true' : undefined}
          aria-describedby={errors.gitURL ? 'git-url-error' : undefined}
          class="mt-2 w-full rounded-lg border border-slate-300 px-3 py-2 font-mono focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
        />
        {#if errors.gitURL}<p id="git-url-error" class="mt-1 text-sm text-red-600">{errors.gitURL}</p>{/if}
      </div>
      <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
        <button type="button" onclick={onCancel} disabled={busy} class="rounded-lg border px-4 py-2">Отмена</button>
        <button type="submit" disabled={busy} aria-busy={busy ? 'true' : 'false'} class="rounded-lg bg-blue-600 px-4 py-2 font-semibold text-white disabled:opacity-50">Проверить репозиторий</button>
      </div>
    </form>
  {:else if step === 2}
    <h1 bind:this={heading} tabindex="-1" class="mt-2 text-3xl font-bold outline-none">Шаг 2 из 4: ветка</h1>
    <form class="mt-6 space-y-5" onsubmit={(event) => { event.preventDefault(); inspectBranch() }}>
      <div>
        {#if probe?.empty_remote}
          <label for="git-branch" class="block text-sm font-semibold">Новая ветка</label>
          <input id="git-branch" bind:this={branchInput} bind:value={draft.branch} oninput={() => clearError('branch')} disabled={busy} aria-invalid={errors.branch ? 'true' : undefined} class="mt-2 w-full rounded-lg border border-slate-300 px-3 py-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600" />
        {:else}
          <label for="git-branch" class="block text-sm font-semibold">Ветка</label>
          <select id="git-branch" bind:this={branchInput} bind:value={draft.branch} onchange={() => clearError('branch')} disabled={busy} aria-invalid={errors.branch ? 'true' : undefined} class="mt-2 w-full rounded-lg border border-slate-300 px-3 py-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600">
            <option value="">Выберите ветку</option>
            {#each probe?.remote_branches || [] as branchName}
              <option value={branchName}>{branchName}</option>
            {/each}
          </select>
        {/if}
        {#if errors.branch}<p class="mt-1 text-sm text-red-600">{errors.branch}</p>{/if}
      </div>
      <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
        <button type="button" onclick={() => focusStep(1)} disabled={busy} class="rounded-lg border px-4 py-2">Назад</button>
        <button type="submit" disabled={busy} aria-busy={busy ? 'true' : 'false'} class="rounded-lg bg-blue-600 px-4 py-2 font-semibold text-white disabled:opacity-50">Продолжить</button>
      </div>
    </form>
  {:else if step === 3}
    <h1 bind:this={heading} tabindex="-1" class="mt-2 text-3xl font-bold outline-none">Шаг 3 из 4: расписание и коммиты</h1>
    <div class="mt-6 space-y-6">
      <fieldset>
        <legend class="text-sm font-semibold">Режим синхронизации</legend>
        <div class="mt-2 flex flex-col gap-2 sm:flex-row sm:gap-6">
          <label class="flex items-center gap-2"><input type="radio" name="git-schedule" checked={!draft.autoSync} onchange={() => draft.autoSync = false} disabled={busy} />Только вручную</label>
          <label class="flex items-center gap-2"><input bind:this={scheduleInput} type="radio" name="git-schedule" checked={draft.autoSync} onchange={() => draft.autoSync = true} disabled={busy} />Автоматически</label>
        </div>
      </fieldset>
      {#if draft.autoSync}
        <div>
          <label for="git-interval" class="block text-sm font-semibold">Интервал</label>
          <select id="git-interval" bind:this={intervalInput} bind:value={draft.interval} onchange={() => clearError('interval')} disabled={busy} class="mt-2 w-full rounded-lg border border-slate-300 px-3 py-2 sm:w-64">
            {#each GIT_INTERVALS as minutes}<option value={minutes}>{minutes} минут</option>{/each}
          </select>
          {#if errors.interval}<p class="mt-1 text-sm text-red-600">{errors.interval}</p>{/if}
        </div>
      {/if}
      <div>
        <label for="git-template" class="block text-sm font-semibold">Шаблон сообщения коммита</label>
        <input id="git-template" bind:this={templateInput} bind:value={draft.template} oninput={() => clearError('template')} disabled={busy} aria-invalid={errors.template ? 'true' : undefined} class="mt-2 w-full rounded-lg border border-slate-300 px-3 py-2 font-mono" />
        <p class="mt-2 text-xs text-slate-500">Переменные: <span class="font-mono">{'{{base}}, {{branch}}, {{date}}, {{datetime}}, {{count}}'}</span></p>
        {#if errors.template}<p class="mt-1 text-sm text-red-600">{errors.template}</p>{/if}
      </div>
      <div class="rounded-xl border border-slate-200 bg-slate-50 p-4">
        <p class="text-xs font-semibold uppercase tracking-wide text-slate-500">Предпросмотр</p>
        <output aria-label="Предпросмотр сообщения" class="mt-2 block break-words font-mono text-sm">{preview}</output>
      </div>
      <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
        <button type="button" onclick={() => focusStep(2)} disabled={busy} class="rounded-lg border px-4 py-2">Назад</button>
        <button type="button" onclick={review} disabled={busy} class="rounded-lg bg-blue-600 px-4 py-2 font-semibold text-white">Проверить настройки</button>
      </div>
    </div>
  {:else}
    <h1 bind:this={heading} tabindex="-1" class="mt-2 text-3xl font-bold outline-none">Шаг 4 из 4: подтверждение</h1>
    <section role="region" aria-label="Проверка Git-настроек" class="mt-6 space-y-6">
      <dl class="grid gap-2 rounded-xl border border-slate-200 bg-white p-4 sm:grid-cols-[10rem_1fr]">
        <dt class="font-semibold">URL</dt><dd class="break-all font-mono">{draft.gitURL.trim()}</dd>
        <dt class="font-semibold">Ветка</dt><dd>{draft.branch.trim()}</dd>
        <dt class="font-semibold">Расписание</dt><dd>{draft.autoSync ? `Каждые ${draft.interval} минут` : 'Только вручную'}</dd>
        <dt class="font-semibold">Шаблон</dt><dd class="break-all font-mono">{draft.template}</dd>
      </dl>
      <div>
        <h2 class="text-lg font-bold">Последствия</h2>
        <div class="mt-3 space-y-3">
          {#each Object.entries(probe?.required_mutations || {}).filter((entry) => entry[1]) as [key]}
            {#if key === 'add_origin'}
              <p class="rounded-lg bg-slate-100 px-3 py-2">{mutationLabels[key]}</p>
            {:else}
              <label class="flex items-start gap-3 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2">
                <input type="checkbox" bind:checked={confirmations[key]} disabled={busy} class="mt-1" />
                <span>Подтверждаю: {mutationLabels[key]}</span>
              </label>
            {/if}
          {/each}
        </div>
      </div>
      {#if probe?.warnings?.length}
        <div><h2 class="text-lg font-bold">Предупреждения</h2><ul class="mt-2 list-disc space-y-1 pl-5">{#each probe.warnings as warning}<li>{warning}</li>{/each}</ul></div>
      {/if}
      {#if apiError}<p bind:this={alert} role="alert" tabindex="-1" class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-red-700">{apiError}</p>{/if}
      <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
        <button type="button" onclick={() => focusStep(3)} disabled={busy} class="rounded-lg border px-4 py-2">Назад</button>
        <button type="button" onclick={submit} disabled={busy} aria-busy={busy ? 'true' : 'false'} class="rounded-lg bg-blue-600 px-4 py-2 font-semibold text-white disabled:opacity-50">Сохранить Git-настройки</button>
      </div>
    </section>
  {/if}

  {#if apiError && step !== 4}
    <p bind:this={alert} role="alert" tabindex="-1" class="mt-5 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-red-700">{apiError}</p>
  {/if}
</section>
```

The semicolons before parenthesized focus expressions in the script are intentional ASI protection. Do not convert the step content into a dialog: this is an in-page settings flow with focus moved to each new `h1`.

- [ ] **Step 5: Run wizard tests GREEN**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/settings/GitSetupWizard.test.js
```

Expected: PASS; URL discovery always sends an empty branch and accepts branchless `can_configure: false`, Back plus URL changes reconcile stale refs, only the literal selected-branch probe with `can_configure: true` reaches schedule/review, one exact configure request is sent, busy requests do not duplicate, and unmount produces no unhandled rejection.

- [ ] **Step 6: Commit the wizard**

```bash
git add web/src/lib/settings/GitSetupWizard.svelte web/src/lib/settings/GitSetupWizard.test.js
git commit -m "feat: add git setup wizard"
```

### Task 5: Add One Git Card Per Notes Base

**Files:**
- Create: `web/src/lib/settings/GitBaseCard.svelte`
- Create: `web/src/lib/settings/GitBaseCard.test.js`

The card delegates all mutations. It may derive labels, but it must not import `api.js`, create a timer, or maintain a second busy state.

- [ ] **Step 1: Write RED card tests**

Create `web/src/lib/settings/GitBaseCard.test.js`:

```js
import { render, screen, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import GitBaseCard from './GitBaseCard.svelte'

const configured = {
  name: 'work', path: '/notes/work', git_url: 'origin', git_branch: 'main',
  auto_sync: true, auto_sync_interval_minutes: 15,
}

function renderCard(overrides = {}) {
  const props = {
    base: configured,
    status: {
      base: 'work', state: 'ready', ahead: 0, behind: 0,
      consecutive_failures: 0, changed_paths: [],
    },
    busy: false,
    error: '',
    onConfigure: vi.fn(),
    onSync: vi.fn(),
    onDisable: vi.fn(),
    ...overrides,
  }
  return { props, ...render(GitBaseCard, props) }
}

describe('GitBaseCard', () => {
  it('shows config/status and delegates each exact value', async () => {
    const user = userEvent.setup()
    const { props } = renderCard()
    const card = screen.getByRole('article', { name: 'Git для базы work' })
    expect(card).toHaveTextContent('Синхронизировано')
    expect(card).toHaveTextContent('main')
    expect(card).toHaveTextContent('Каждые 15 минут')

    await user.click(within(card).getByRole('button', { name: 'Изменить настройки' }))
    await user.click(within(card).getByRole('button', { name: 'Синхронизировать сейчас' }))
    const disable = within(card).getByRole('button', { name: 'Отключить Git' })
    await user.click(disable)

    expect(props.onConfigure).toHaveBeenCalledWith(configured)
    expect(props.onSync).toHaveBeenCalledWith('work')
    expect(props.onDisable).toHaveBeenCalledWith(configured, disable)
  })

  it('offers setup only for an unconfigured base', () => {
    renderCard({
      base: { name: 'plain', path: '/notes/plain', auto_sync: false },
      status: { base: 'plain', state: 'unconfigured', changed_paths: [] },
    })
    const card = screen.getByRole('article', { name: 'Git для базы plain' })
    expect(card).toHaveTextContent('Git не настроен')
    expect(within(card).getByRole('button', { name: 'Настроить Git' })).toBeEnabled()
    expect(within(card).queryByRole('button', { name: 'Отключить Git' })).not.toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: 'Синхронизировать сейчас' })).not.toBeInTheDocument()
  })

  it.each(['initializing', 'syncing', 'paused', 'conflict', 'needs_reconnect'])(
    'disables sync for %s',
    (state) => {
      renderCard({ status: { base: 'work', state, changed_paths: [] } })
      expect(screen.getByRole('button', { name: 'Синхронизировать сейчас' })).toBeDisabled()
    },
  )

  it('disables competing actions and renders a persistent card error', () => {
    renderCard({ busy: true, error: 'Не удалось выполнить действие' })
    const card = screen.getByRole('article', { name: 'Git для базы work' })
    expect(card).toHaveAttribute('aria-busy', 'true')
    expect(within(card).getByRole('alert')).toHaveTextContent('Не удалось выполнить действие')
    for (const button of within(card).getAllByRole('button')) expect(button).toBeDisabled()
  })

  it('uses a one-column mobile card with wrapping actions', () => {
    renderCard()
    const card = screen.getByRole('article', { name: 'Git для базы work' })
    expect(card).toHaveClass('flex', 'flex-col')
    expect(within(card).getByTestId('git-card-actions')).toHaveClass('flex-wrap')
  })
})
```

- [ ] **Step 2: Run card tests RED under Node 24**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/settings/GitBaseCard.test.js
```

Expected: FAIL because `GitBaseCard.svelte` does not exist.

- [ ] **Step 3: Implement the complete card**

Create `web/src/lib/settings/GitBaseCard.svelte`:

```svelte
<script>
  import { gitConfigured, presentGitStatus } from '../git/git-settings.js'

  let {
    base,
    status = null,
    busy = false,
    error = '',
    onConfigure,
    onSync,
    onDisable,
  } = $props()

  let configured = $derived(gitConfigured(base))
  let presentation = $derived(presentGitStatus(base, status))
</script>

<article
  aria-label={`Git для базы ${base.name}`}
  aria-busy={busy ? 'true' : 'false'}
  class="flex min-w-0 flex-col rounded-2xl border border-slate-200 bg-white p-5 shadow-sm"
>
  <div class="flex min-w-0 items-start justify-between gap-3">
    <div class="min-w-0">
      <h2 class="truncate text-lg font-bold text-slate-950">{base.name}</h2>
      <p class="mt-1 break-all font-mono text-xs text-slate-500">{base.path}</p>
    </div>
    <span class="shrink-0 rounded-full bg-slate-100 px-2.5 py-1 text-xs font-semibold">
      {presentation.label}
    </span>
  </div>

  {#if configured}
    <dl class="mt-5 grid gap-2 text-sm sm:grid-cols-[7rem_1fr]">
      <dt class="font-semibold text-slate-500">Ветка</dt><dd class="break-all font-mono">{base.git_branch}</dd>
      <dt class="font-semibold text-slate-500">Репозиторий</dt><dd class="break-all font-mono">{base.git_url}</dd>
      <dt class="font-semibold text-slate-500">Расписание</dt><dd>{base.auto_sync ? `Каждые ${base.auto_sync_interval_minutes} минут` : 'Только вручную'}</dd>
      {#if status?.stage}<dt class="font-semibold text-slate-500">Этап</dt><dd>{status.stage}</dd>{/if}
    </dl>
    {#if status?.error?.message}
      <p class="mt-4 rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700">{status.error.message}</p>
    {/if}
  {:else}
    <p class="mt-5 text-sm text-slate-600">Подключите remote и выберите ветку отдельно от настройки базы.</p>
  {/if}

  {#if error}
    <p role="alert" class="mt-4 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">{error}</p>
  {/if}

  <div data-testid="git-card-actions" class="mt-auto flex flex-wrap justify-end gap-2 pt-6">
    <button type="button" onclick={() => onConfigure(base)} disabled={busy} class="rounded-lg border border-slate-300 px-3.5 py-2 text-sm font-semibold disabled:opacity-50">
      {configured ? 'Изменить настройки' : 'Настроить Git'}
    </button>
    {#if configured}
      <button type="button" onclick={() => onSync(base.name)} disabled={busy || !presentation.canSync} class="rounded-lg bg-blue-600 px-3.5 py-2 text-sm font-semibold text-white disabled:opacity-50">Синхронизировать сейчас</button>
      <button type="button" onclick={(event) => onDisable(base, event.currentTarget)} disabled={busy} class="rounded-lg border border-red-300 px-3.5 py-2 text-sm font-semibold text-red-700 disabled:opacity-50">Отключить Git</button>
    {/if}
  </div>
</article>
```

- [ ] **Step 4: Run card tests GREEN**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/settings/GitBaseCard.test.js
```

Expected: PASS; no card imports an API wrapper or exposes sync in an unsafe state.

- [ ] **Step 5: Commit the cards**

```bash
git add web/src/lib/settings/GitBaseCard.svelte web/src/lib/settings/GitBaseCard.test.js
git commit -m "feat: add git base cards"
```

### Task 6: Orchestrate The Git Settings Section

**Files:**
- Create: `web/src/lib/settings/GitSettingsSection.svelte`
- Create: `web/src/lib/settings/GitSettingsSection.test.js`

The section owns wizard routing and the disable confirmation only. It delegates manual sync to `App.svelte`, replaces only the backend-returned base in config, and asks the sole poller owner to refresh.

- [ ] **Step 1: Write RED section tests**

Create `web/src/lib/settings/GitSettingsSection.test.js`:

```js
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    probeGit: vi.fn(),
    configureGit: vi.fn(),
    disableGit: vi.fn(),
  }
})

import { configureGit, disableGit, probeGit } from '../api.js'
import GitSettingsSection from './GitSettingsSection.svelte'

const config = {
  current_base: 'work', setup_completed: true,
  bases: [
    { name: 'work', path: '/notes/work', git_url: 'origin', git_branch: 'main', auto_sync: false },
    { name: 'plain', path: '/notes/plain', auto_sync: false },
  ],
}
const statuses = [
  { base: 'work', state: 'ready', ahead: 1, behind: 0, changed_paths: [] },
  { base: 'plain', state: 'unconfigured', ahead: 0, behind: 0, changed_paths: [] },
]
const discoveryProbe = {
  base: 'plain', can_configure: false, empty_remote: false,
  remote_branches: ['main'], warnings: [],
  required_mutations: {
    create_repository: false, add_origin: false, replace_origin: false,
    create_branch: false, merge_histories: false,
  },
}
const probe = {
  ...discoveryProbe,
  can_configure: true,
  warnings: ['Будут добавлены все неигнорируемые файлы.'],
  required_mutations: {
    create_repository: false, add_origin: true, replace_origin: false,
    create_branch: false, merge_histories: false,
  },
}

function renderSection(overrides = {}) {
  const props = {
    config,
    statuses,
    pollError: '',
    busyBase: '',
    actionErrors: {},
    onConfigChange: vi.fn(),
    onSync: vi.fn(),
    onRefresh: vi.fn(),
    onBusyChange: vi.fn(),
    ...overrides,
  }
  return { props, ...render(GitSettingsSection, props) }
}

describe('GitSettingsSection', () => {
  beforeEach(() => {
    vi.mocked(probeGit).mockReset().mockImplementation(({ git_branch }) => (
      Promise.resolve(git_branch ? probe : discoveryProbe)
    ))
    vi.mocked(configureGit).mockReset()
    vi.mocked(disableGit).mockReset()
  })

  it('pairs statuses by exact base name and delegates manual sync', async () => {
    const user = userEvent.setup()
    const { props } = renderSection({ statuses: [...statuses].reverse() })
    const work = screen.getByRole('article', { name: 'Git для базы work' })
    const plain = screen.getByRole('article', { name: 'Git для базы plain' })
    expect(work).toHaveTextContent('Есть локальные изменения')
    expect(plain).toHaveTextContent('Git не настроен')

    await user.click(within(work).getByRole('button', { name: 'Синхронизировать сейчас' }))
    expect(props.onSync).toHaveBeenCalledOnce()
    expect(props.onSync).toHaveBeenCalledWith('work')
  })

  it('publishes only the configured base and refreshes the owner poller', async () => {
    const user = userEvent.setup()
    const savedBase = {
      ...config.bases[1], git_url: 'origin', git_branch: 'main', auto_sync: false,
      auto_sync_interval_minutes: 15,
      git_commit_message_template: 'sync {{base}}',
    }
    vi.mocked(configureGit).mockResolvedValue({
      base: savedBase,
      status: { base: 'plain', state: 'initializing', changed_paths: [] },
      operation: { operation_id: 'op-2', status: 'queued', deduplicated: false },
    })
    const { props } = renderSection()
    await user.click(within(
      screen.getByRole('article', { name: 'Git для базы plain' }),
    ).getByRole('button', { name: 'Настроить Git' }))
    await user.type(screen.getByLabelText('URL репозитория'), 'origin')
    await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))
    await user.selectOptions(screen.getByLabelText('Ветка'), 'main')
    await user.click(screen.getByRole('button', { name: 'Продолжить' }))
    await user.click(screen.getByRole('button', { name: 'Проверить настройки' }))
    await user.click(screen.getByRole('button', { name: 'Сохранить Git-настройки' }))

    const expected = { ...config, bases: [config.bases[0], savedBase] }
    await waitFor(() => expect(props.onConfigChange).toHaveBeenCalledWith(expected))
    expect(config.bases[1].git_url).toBeUndefined()
    expect(props.onRefresh).toHaveBeenCalledOnce()
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Git-синхронизация' })).toHaveFocus())
  })

  it('confirms disable, retains files on disk, and replaces only the returned base', async () => {
    const user = userEvent.setup()
    const disabledBase = { name: 'work', path: '/notes/work', auto_sync: false }
    vi.mocked(disableGit).mockResolvedValue({
      base: disabledBase,
      status: { base: 'work', state: 'unconfigured', changed_paths: [] },
    })
    const { props } = renderSection()
    const trigger = within(
      screen.getByRole('article', { name: 'Git для базы work' }),
    ).getByRole('button', { name: 'Отключить Git' })

    await user.click(trigger)
    const dialog = screen.getByRole('dialog', { name: 'Отключить Git для work?' })
    expect(dialog).toHaveTextContent('Репозиторий и файлы на диске останутся без изменений')
    await user.click(within(dialog).getByRole('button', { name: 'Отключить Git' }))

    expect(disableGit).toHaveBeenCalledWith('work')
    await waitFor(() => expect(props.onConfigChange).toHaveBeenCalledWith({
      ...config, bases: [disabledBase, config.bases[1]],
    }))
    expect(props.onRefresh).toHaveBeenCalledOnce()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('keeps disable single-flight and surfaces polling/action errors', async () => {
    const request = Promise.withResolvers()
    vi.mocked(disableGit).mockReturnValue(request.promise)
    const user = userEvent.setup()
    renderSection({
      pollError: 'Статус временно недоступен',
      actionErrors: { work: 'Не удалось синхронизировать' },
    })
    expect(screen.getByRole('alert', { name: 'Ошибка опроса Git' }))
      .toHaveTextContent('Статус временно недоступен')
    expect(screen.getByText('Не удалось синхронизировать')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Отключить Git' }))
    const confirm = within(screen.getByRole('dialog')).getByRole('button', { name: 'Отключить Git' })
    await fireEvent.click(confirm)
    await fireEvent.click(confirm)
    expect(disableGit).toHaveBeenCalledOnce()
    expect(confirm).toHaveAttribute('aria-busy', 'true')
    request.resolve({
      base: { name: 'work', path: '/notes/work', auto_sync: false },
      status: { base: 'work', state: 'unconfigured', changed_paths: [] },
    })
    await request.promise
  })
})
```

- [ ] **Step 2: Run section tests RED under Node 24**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/settings/GitSettingsSection.test.js
```

Expected: FAIL because `GitSettingsSection.svelte` does not exist.

- [ ] **Step 3: Implement complete section orchestration**

Create `web/src/lib/settings/GitSettingsSection.svelte`:

```svelte
<script>
  import { onDestroy, tick } from 'svelte'

  import Modal from '../Modal.svelte'
  import { disableGit } from '../api.js'
  import { gitStatusFor, replaceConfigBase } from '../git/git-settings.js'
  import GitBaseCard from './GitBaseCard.svelte'
  import GitSetupWizard from './GitSetupWizard.svelte'

  let {
    config,
    statuses = [],
    pollError = '',
    busyBase = '',
    actionErrors = {},
    onConfigChange,
    onSync,
    onRefresh,
    onBusyChange = () => {},
  } = $props()

  let view = $state('list')
  let editingBase = $state(null)
  let pendingDisable = $state(null)
  let disableTrigger = $state(null)
  let localBusy = $state('')
  let wizardBusy = $state(false)
  let localErrors = $state({})
  let heading = $state()
  let active = true

  let bases = $derived(Array.isArray(config?.bases) ? config.bases : [])
  let anyBusy = $derived(localBusy !== '' || wizardBusy || busyBase !== '')

  onDestroy(() => {
    active = false
    onBusyChange(false)
  })

  function errorMessage(error, fallback) {
    return error instanceof Error && error.message ? error.message : fallback
  }

  async function focusHeading() {
    await tick()
    if (active) heading?.focus()
  }

  function setWizardBusy(value) {
    wizardBusy = value
    onBusyChange(value || localBusy !== '')
  }

  function setLocalBusy(value) {
    localBusy = value
    onBusyChange(value !== '' || wizardBusy)
  }

  async function openWizard(base) {
    if (anyBusy) return
    localErrors = { ...localErrors, [base.name]: '' }
    editingBase = base
    view = 'wizard'
    await tick()
  }

  async function closeWizard() {
    if (anyBusy) return
    editingBase = null
    view = 'list'
    await focusHeading()
  }

  async function publishBase(savedBase) {
    await onConfigChange(replaceConfigBase(config, savedBase))
    if (!active) return
    await onRefresh()
  }

  async function configured(response) {
    await publishBase(response.base)
    if (!active) return
    editingBase = null
    view = 'list'
    await focusHeading()
  }

  function askDisable(base, trigger) {
    if (anyBusy) return
    localErrors = { ...localErrors, [base.name]: '' }
    pendingDisable = base
    disableTrigger = trigger
  }

  async function cancelDisable() {
    if (localBusy) return
    const trigger = disableTrigger
    pendingDisable = null
    disableTrigger = null
    await tick()
    if (active && trigger?.isConnected) trigger.focus()
  }

  async function confirmDisable() {
    if (!active || localBusy || !pendingDisable) return
    const base = pendingDisable
    const trigger = disableTrigger
    let restoreFocus = false
    setLocalBusy(`disable:${base.name}`)
    try {
      const response = await disableGit(base.name)
      if (!active) return
      await publishBase(response.base)
      if (!active) return
      pendingDisable = null
      disableTrigger = null
    } catch (error) {
      if (!active) return
      pendingDisable = null
      disableTrigger = null
      localErrors = {
        ...localErrors,
        [base.name]: errorMessage(error, 'Не удалось отключить Git'),
      }
      restoreFocus = true
    } finally {
      if (active) setLocalBusy('')
    }
    if (active && restoreFocus) {
      await tick()
      if (trigger?.isConnected) trigger.focus()
    }
  }
</script>

{#if view === 'wizard' && editingBase}
  <GitSetupWizard
    base={editingBase}
    onConfigured={configured}
    onCancel={closeWizard}
    onBusyChange={setWizardBusy}
  />
{:else}
  <section>
    <div class="flex flex-col gap-2">
      <h1 bind:this={heading} tabindex="-1" class="text-3xl font-bold tracking-tight text-slate-950 outline-none">Git-синхронизация</h1>
      <p class="text-slate-600">Настройте remote, ветку и расписание отдельно для каждой базы.</p>
    </div>

    {#if pollError}
      <p role="alert" aria-label="Ошибка опроса Git" class="mt-5 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">{pollError}</p>
    {/if}

    <div class="mt-8 grid gap-4 xl:grid-cols-2">
      {#each bases as base (base.name)}
        <GitBaseCard
          {base}
          status={gitStatusFor(base.name, statuses)}
          busy={anyBusy}
          error={localErrors[base.name] || actionErrors[base.name] || ''}
          onConfigure={openWizard}
          {onSync}
          onDisable={askDisable}
        />
      {/each}
    </div>
  </section>
{/if}

<Modal
  show={pendingDisable !== null}
  title={pendingDisable ? `Отключить Git для ${pendingDisable.name}?` : 'Отключить Git?'}
  description="Репозиторий и файлы на диске останутся без изменений"
  confirmText="Отключить Git"
  danger={true}
  busy={localBusy.startsWith('disable:')}
  onConfirm={confirmDisable}
  onCancel={cancelDisable}
/>
```

Do not optimistically splice `response.status` into a local array. `onRefresh` routes to the one `App.svelte` poller, so only `applyGitStatuses` publishes status snapshots.

- [ ] **Step 4: Run section tests GREEN**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/settings/GitSetupWizard.test.js src/lib/settings/GitBaseCard.test.js src/lib/settings/GitSettingsSection.test.js
```

Expected: all three files PASS; configure and disable each replace one base, refresh once, preserve unrelated bases, and never invoke a sync API directly.

- [ ] **Step 5: Commit the section**

```bash
git add web/src/lib/settings/GitSettingsSection.svelte web/src/lib/settings/GitSettingsSection.test.js
git commit -m "feat: add git settings section"
```

### Task 7: Enable The Settings Git Tab And Remove Placeholders

**Files:**
- Modify: `web/src/lib/settings/SettingsWorkspace.svelte:1-362`
- Modify: `web/src/lib/settings/SettingsWorkspace.test.js:5-17,44-133`
- Modify: `web/src/lib/settings/BaseCard.svelte:33-36`
- Modify: `web/src/lib/setup/BaseForm.svelte:230-247`
- Modify: `web/src/lib/setup/BaseForm.test.js:23-222`

- [ ] **Step 1: Write RED enabled-tab and placeholder-removal tests**

Extend `renderWorkspace` in `web/src/lib/settings/SettingsWorkspace.test.js` with the frozen Git props:

```js
const gitStatuses = [
  { base: 'personal', state: 'unconfigured', ahead: 0, behind: 0, changed_paths: [] },
  { base: 'work', state: 'ready', ahead: 1, behind: 0, changed_paths: [] },
]

function renderWorkspace(overrides = {}) {
  const props = {
    config,
    gitStatuses,
    gitPollError: '',
    gitBusyBase: '',
    gitActionErrors: {},
    onConfigChange: vi.fn(),
    onSwitch: vi.fn(),
    onBack: vi.fn(),
    onGitSync: vi.fn(),
    onGitRefresh: vi.fn(),
    ...overrides,
  }
  return { props, ...render(SettingsWorkspace, props) }
}
```

Replace the existing `../api.js` partial mock/import with this version and add the reset line to `beforeEach`; `probeGit` is used only to hold a wizard request pending in the shell-lock test below:

```js
vi.mock('../api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    createBase: vi.fn(),
    updateBase: vi.fn(),
    forgetBase: vi.fn(),
    probeGit: vi.fn(),
  }
})

import { ApiError, createBase, forgetBase, probeGit, updateBase } from '../api.js'

// Inside the existing beforeEach:
vi.mocked(probeGit).mockReset()
```

Add this responsive `matchMedia` test helper near `renderWorkspace`; it models the same Tailwind `md` breakpoint used by the settings sidebar:

```js
function installDesktopTabsMedia(initialMatches = false) {
  const listeners = new Set()
  const media = {
    matches: initialMatches,
    media: '(min-width: 768px)',
    addEventListener: (_type, listener) => listeners.add(listener),
    removeEventListener: (_type, listener) => listeners.delete(listener),
  }
  vi.stubGlobal('matchMedia', vi.fn(() => media))
  return {
    setMatches(matches) {
      media.matches = matches
      for (const listener of listeners) listener({ matches, media: media.media })
    },
  }
}
```

Extend the Vitest import with `afterEach`, then add this setup around the suite's existing `beforeEach` (keep its current mock resets):

```js
let tabsMedia

// Inside the existing beforeEach, before any component render:
tabsMedia = installDesktopTabsMedia(false)

afterEach(() => {
  vi.unstubAllGlobals()
})
```

Replace the old disabled-tab assertions in the first settings test and append keyboard/integration coverage:

```js
it('enables linked Bases and Git tabpanels without soon placeholders', async () => {
  const user = userEvent.setup()
  renderWorkspace()
  const basesTab = screen.getByRole('tab', { name: 'Базы заметок' })
  const gitTab = screen.getByRole('tab', { name: 'Git' })
  expect(basesTab).toHaveAttribute('aria-selected', 'true')
  expect(gitTab).toBeEnabled()
  expect(gitTab).toHaveAttribute('aria-selected', 'false')
  expect(screen.queryByText('Git, скоро')).not.toBeInTheDocument()
  expect(screen.queryByLabelText('Git URL')).not.toBeInTheDocument()
  expect(screen.queryByText('Автосинхронизация выключена')).not.toBeInTheDocument()

  await user.click(gitTab)
  const panel = screen.getByRole('tabpanel', { name: 'Git' })
  expect(gitTab).toHaveAttribute('aria-controls', panel.id)
  expect(panel).toHaveAttribute('aria-labelledby', gitTab.id)
  expect(screen.getByRole('heading', { name: 'Git-синхронизация' })).toBeVisible()
  expect(screen.getByRole('article', { name: 'Git для базы work' }))
    .toHaveTextContent('Есть локальные изменения')
})

it('matches responsive tab orientation and keyboard navigation to the sidebar', async () => {
  const user = userEvent.setup()
  renderWorkspace()
  const tablist = screen.getByRole('tablist', { name: 'Разделы настроек' })
  const bases = screen.getByRole('tab', { name: 'Базы заметок' })
  const git = screen.getByRole('tab', { name: 'Git' })

  expect(tablist).toHaveAttribute('aria-orientation', 'horizontal')
  expect(tablist).toHaveClass('md:sticky', 'md:flex-col')
  bases.focus()
  await user.keyboard('{ArrowRight}')
  expect(git).toHaveFocus()
  expect(git).toHaveAttribute('aria-selected', 'true')
  await user.keyboard('{ArrowLeft}')
  expect(bases).toHaveFocus()
  await user.keyboard('{End}')
  expect(git).toHaveFocus()
  await user.keyboard('{Home}')
  expect(bases).toHaveFocus()

  tabsMedia.setMatches(true)
  await waitFor(() => expect(tablist).toHaveAttribute('aria-orientation', 'vertical'))
  await user.keyboard('{ArrowDown}')
  expect(git).toHaveFocus()
  await user.keyboard('{ArrowUp}')
  expect(bases).toHaveFocus()
  await user.keyboard('{ArrowRight}')
  expect(bases).toHaveFocus()
  await user.keyboard('{End}')
  expect(git).toHaveFocus()
  await user.keyboard('{Home}')
  expect(bases).toHaveFocus()
})

it('passes the Git owner callbacks through the enabled section', async () => {
  const user = userEvent.setup()
  const { props } = renderWorkspace()
  await user.click(screen.getByRole('tab', { name: 'Git' }))
  await user.click(within(
    screen.getByRole('article', { name: 'Git для базы work' }),
  ).getByRole('button', { name: 'Синхронизировать сейчас' }))
  expect(props.onGitSync).toHaveBeenCalledWith('work')
})

it('locks Back, tabs, and competing settings actions during a Git request', async () => {
  const request = Promise.withResolvers()
  vi.mocked(probeGit).mockReturnValue(request.promise)
  const user = userEvent.setup()
  renderWorkspace()
  await user.click(screen.getByRole('tab', { name: 'Git' }))
  await user.click(within(
    screen.getByRole('article', { name: 'Git для базы personal' }),
  ).getByRole('button', { name: 'Настроить Git' }))
  await user.type(screen.getByLabelText('URL репозитория'), 'origin')
  await user.click(screen.getByRole('button', { name: 'Проверить репозиторий' }))

  expect(screen.getByRole('button', { name: 'Назад к заметкам' })).toBeDisabled()
  for (const tab of screen.getAllByRole('tab')) expect(tab).toBeDisabled()
  request.resolve({
    base: 'personal', can_configure: false, empty_remote: false,
    remote_branches: ['main'], warnings: [],
    required_mutations: {
      create_repository: false, add_origin: true, replace_origin: false,
      create_branch: false, merge_histories: false,
    },
  })
  await request.promise
  await waitFor(() => expect(
    screen.getByRole('button', { name: 'Назад к заметкам' }),
  ).toBeEnabled())
})
```

Append to `web/src/lib/setup/BaseForm.test.js`:

```js
it('does not render Git fields or future autosync placeholders', () => {
  render(BaseForm, formProps())
  expect(screen.queryByLabelText('Git URL')).not.toBeInTheDocument()
  expect(screen.queryByText('Git, скоро')).not.toBeInTheDocument()
  expect(screen.queryByText(/Автосинхронизация будет доступна/)).not.toBeInTheDocument()
})
```

- [ ] **Step 2: Run settings tests RED under Node 24**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/settings/SettingsWorkspace.test.js src/lib/setup/BaseForm.test.js
```

Expected: FAIL because the Git tab remains disabled and both placeholder blocks still render.

- [ ] **Step 3: Add working tab state and keyboard behavior**

In `web/src/lib/settings/SettingsWorkspace.svelte`, import the section and expand props at the top:

```svelte
<script>
  import { onDestroy, onMount, tick } from 'svelte'

  import Modal from '../Modal.svelte'
  import { createBase, forgetBase, updateBase } from '../api.js'
  import BaseForm from '../setup/BaseForm.svelte'
  import BaseCard from './BaseCard.svelte'
  import GitSettingsSection from './GitSettingsSection.svelte'

  let {
    config,
    gitStatuses = [],
    gitPollError = '',
    gitBusyBase = '',
    gitActionErrors = {},
    onConfigChange,
    onSwitch,
    onBack,
    onGitSync,
    onGitRefresh,
  } = $props()

  let activeTab = $state('bases')
  let basesTab = $state()
  let gitTab = $state()
  let tabOrientation = $state('horizontal')
```

Keep all existing base-management state and functions after these declarations. Immediately after the existing `let busyAction = $state('')`, add the shared shell lock so `busyAction` is initialized before the derived expression:

```js
  let gitSectionBusy = $state(false)
  let workspaceBusy = $derived(busyAction !== '' || gitBusyBase !== '' || gitSectionBusy)
```

Add this breakpoint observer and these functions before `</script>`:

```js
  onMount(() => {
    const media = window.matchMedia('(min-width: 768px)')
    const updateOrientation = () => {
      tabOrientation = media.matches ? 'vertical' : 'horizontal'
    }
    updateOrientation()
    media.addEventListener('change', updateOrientation)
    return () => media.removeEventListener('change', updateOrientation)
  })

  function tabElement(name) {
    return name === 'bases' ? basesTab : gitTab
  }

  async function selectTab(name, focus = false) {
    if (workspaceBusy) return
    activeTab = name
    await tick()
    if (active && focus) tabElement(name)?.focus()
  }

  function handleTabKeydown(event) {
    const order = ['bases', 'git']
    const current = order.indexOf(activeTab)
    let next = null
    if (tabOrientation === 'horizontal' && event.key === 'ArrowRight') {
      next = order[(current + 1) % order.length]
    }
    if (tabOrientation === 'horizontal' && event.key === 'ArrowLeft') {
      next = order[(current - 1 + order.length) % order.length]
    }
    if (tabOrientation === 'vertical' && event.key === 'ArrowDown') {
      next = order[(current + 1) % order.length]
    }
    if (tabOrientation === 'vertical' && event.key === 'ArrowUp') {
      next = order[(current - 1 + order.length) % order.length]
    }
    if (event.key === 'Home') next = order[0]
    if (event.key === 'End') next = order.at(-1)
    if (next === null) return
    event.preventDefault()
    void selectTab(next, true)
  }
```

Replace the current tablist with this complete tablist:

```svelte
<div
  role="tablist"
  aria-label="Разделы настроек"
  aria-orientation={tabOrientation}
  class="flex gap-2 overflow-x-auto md:sticky md:top-6 md:flex-col"
>
  <button
    bind:this={basesTab}
    id="settings-bases-tab"
    type="button"
    role="tab"
    aria-selected={activeTab === 'bases'}
    aria-controls="settings-bases-panel"
    tabindex={activeTab === 'bases' ? 0 : -1}
    disabled={workspaceBusy}
    onclick={() => selectTab('bases')}
    onkeydown={handleTabKeydown}
    class={`shrink-0 whitespace-nowrap rounded-lg px-3 py-2 text-left text-sm font-semibold focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 md:w-full ${activeTab === 'bases' ? 'bg-blue-50 text-blue-700' : 'text-slate-600 hover:bg-slate-50'}`}
  >Базы заметок</button>
  <button
    bind:this={gitTab}
    id="settings-git-tab"
    type="button"
    role="tab"
    aria-selected={activeTab === 'git'}
    aria-controls="settings-git-panel"
    tabindex={activeTab === 'git' ? 0 : -1}
    disabled={workspaceBusy}
    onclick={() => selectTab('git')}
    onkeydown={handleTabKeydown}
    class={`shrink-0 whitespace-nowrap rounded-lg px-3 py-2 text-left text-sm font-semibold focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 md:w-full ${activeTab === 'git' ? 'bg-blue-50 text-blue-700' : 'text-slate-600 hover:bg-slate-50'}`}
  >Git</button>
</div>
```

Keep the full existing base-management content inside its current panel, but add `hidden={activeTab !== 'bases'}` and remove `aria-current`. Immediately after that panel's closing `</div>`, add the Git panel:

```svelte
<div
  id="settings-git-panel"
  role="tabpanel"
  aria-labelledby="settings-git-tab"
  hidden={activeTab !== 'git'}
  class="min-w-0 p-4 sm:p-6 lg:p-8"
>
  <GitSettingsSection
    {config}
    statuses={gitStatuses}
    pollError={gitPollError}
    busyBase={gitBusyBase}
    actionErrors={gitActionErrors}
    {onConfigChange}
    onSync={onGitSync}
    onRefresh={onGitRefresh}
    onBusyChange={(busy) => gitSectionBusy = busy}
  />
</div>
```

Both tabpanels remain mounted and are hidden through the native `hidden` attribute. This preserves an in-progress base form if the user switches tabs; it does not create another poller.

Use these exact replacements for the header Back button, Add button, both `BaseForm` invocations, and `BaseCard`; leave existing non-busy props unchanged:

```svelte
<!-- Header Back and Add buttons -->
disabled={workspaceBusy}

<!-- Both BaseForm invocations -->
busy={workspaceBusy}

<!-- BaseCard invocation -->
busyAction={workspaceBusy ? (busyAction || 'git') : ''}
```

This preserves every existing base-operation key while preventing navigation or competing base actions during pending wizard/disable/manual-sync work.

- [ ] **Step 4: Delete the obsolete placeholders exactly**

Delete `web/src/lib/setup/BaseForm.svelte:230-247`, the entire disabled `Git URL`/future autosync block. Delete `web/src/lib/settings/BaseCard.svelte:33-36`, the static `Git не настроен` and `Автосинхронизация выключена` chips. Do not replace either block with new markup; Git belongs only in `GitSettingsSection`.

- [ ] **Step 5: Run Task 7 GREEN**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/settings/SettingsWorkspace.test.js src/lib/setup/BaseForm.test.js src/lib/settings/GitSettingsSection.test.js
```

Expected: all files PASS; `Git` is enabled and linked to its panel; the tablist is horizontal with Left/Right below `md`, vertical with Up/Down at `md` and above, and supports Home/End in both modes; the `md:sticky md:flex-col` desktop sidebar remains intact; base forms contain no Git controls; and base-management cards contain no fake status.

- [ ] **Step 6: Commit settings integration**

```bash
git add web/src/lib/settings/SettingsWorkspace.svelte web/src/lib/settings/SettingsWorkspace.test.js web/src/lib/settings/BaseCard.svelte web/src/lib/setup/BaseForm.svelte web/src/lib/setup/BaseForm.test.js
git commit -m "feat: enable git settings tab"
```

### Task 8: Put Git Status In The Notes Footer

**Files:**
- Modify: `web/src/lib/NotesWorkspace.svelte:1-113`
- Modify: `web/src/lib/NotesWorkspace.test.js:53-101,420-493`
- Modify: `web/src/test/NotesWorkspaceHost.svelte:7-18`

- [ ] **Step 1: Write RED footer integration tests**

Add stable Git defaults to `workspaceProps` in `web/src/lib/NotesWorkspace.test.js`:

```js
gitBase: {
  name: 'work', path: '/notes/work', git_url: 'origin', git_branch: 'main',
  auto_sync: false,
},
gitStatus: {
  base: 'work', state: 'ready', ahead: 0, behind: 0,
  consecutive_failures: 0, changed_paths: [],
},
gitSyncBusy: false,
gitSyncError: '',
onGitSync: vi.fn(),
```

Append these tests:

```js
it('renders active-base Git status in a wrapping footer and delegates sync', async () => {
  const user = userEvent.setup()
  const { props } = await renderWorkspace({
    gitStatus: {
      base: 'work', state: 'ready', ahead: 2, behind: 0,
      consecutive_failures: 0, changed_paths: [],
    },
  })
  const footer = screen.getByRole('contentinfo')
  expect(footer).toHaveClass('flex-wrap', 'min-h-6')
  expect(footer).not.toHaveClass('h-6')
  expect(within(footer).getByText('/notes/work')).toBeVisible()
  await user.click(within(footer).getByRole('button', {
    name: 'Открыть детали Git: Есть локальные изменения',
  }))
  await user.click(within(footer).getByRole('button', { name: 'Синхронизировать Git' }))
  expect(props.onGitSync).toHaveBeenCalledOnce()
  expect(props.onGitSync).toHaveBeenCalledWith('work')
})

it('passes footer busy/error state and disables unsafe status sync', async () => {
  const user = userEvent.setup()
  await renderWorkspace({
    gitStatus: { base: 'work', state: 'conflict', changed_paths: [] },
    gitSyncBusy: true,
    gitSyncError: 'Сначала сохраните заметку',
  })
  await user.click(screen.getByRole('button', { name: 'Открыть детали Git: Конфликт' }))
  expect(screen.getByRole('alert')).toHaveTextContent('Сначала сохраните заметку')
  expect(screen.getByRole('button', { name: 'Синхронизировать Git' })).toBeDisabled()
})
```

- [ ] **Step 2: Run footer tests RED under Node 24**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/NotesWorkspace.test.js
```

Expected: FAIL because `NotesWorkspace` does not accept or render the Git props and the footer still has fixed `h-6`.

- [ ] **Step 3: Integrate the indicator without moving save ownership**

Add the import in `web/src/lib/NotesWorkspace.svelte`:

```js
import GitStatusIndicator from './git/GitStatusIndicator.svelte'
```

Add these props after `basePath`:

```js
gitBase = null,
gitStatus = null,
gitSyncBusy = false,
gitSyncError = '',
onGitSync,
```

Replace only the existing footer with this complete responsive footer:

```svelte
<footer class="min-h-6 shrink-0 border-t border-gray-200 bg-gray-100 px-3 py-1 flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
  <span class="min-w-0 flex-1 text-[11px] text-gray-500 font-mono truncate flex items-center gap-1" title="Текущая база заметок">
    {#if basePath}
      <svg class="h-3 w-3 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"></path></svg>
      {basePath}
    {:else}
      Загрузка информации о базе...
    {/if}
  </span>
  {#if gitBase}
    <GitStatusIndicator
      base={gitBase}
      status={gitStatus}
      busy={gitSyncBusy}
      error={gitSyncError}
      onSync={onGitSync}
    />
  {/if}
</footer>
```

Do not call `runAfterUploads` here. The child delegates directly to the mandatory `App.svelte` `flushWorkspace` boundary added in Task 9; wrapping only in `flushPendingUploads` would be insufficient because it would omit the dirty note save.

- [ ] **Step 4: Keep the bindable host contract stable**

Add these props to `web/src/test/NotesWorkspaceHost.svelte` before its closing `/>`:

```svelte
gitBase={{ name: 'work', path: '/notes/work', git_url: 'origin', git_branch: 'main', auto_sync: false }}
gitStatus={{ base: 'work', state: 'ready', ahead: 0, behind: 0, changed_paths: [] }}
gitSyncBusy={false}
gitSyncError=""
onGitSync={() => {}}
```

- [ ] **Step 5: Run Task 8 GREEN**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/git/GitStatusIndicator.test.js src/lib/NotesWorkspace.test.js
```

Expected: both files PASS; the footer wraps, the path remains visible/truncated, and the callback carries only the exact active base name.

- [ ] **Step 6: Commit footer integration**

```bash
git add web/src/lib/NotesWorkspace.svelte web/src/lib/NotesWorkspace.test.js web/src/test/NotesWorkspaceHost.svelte
git commit -m "feat: show git status in notes footer"
```

### Task 9: Make App The Single Polling And Manual-Sync Owner

**Files:**
- Modify: `web/src/App.svelte:1-404`
- Modify: `web/src/App.test.js:10-94,126-404,1002-1055`

This task is deliberately serial. Re-read the landed child prop names before editing; do not add a store, context, second `getGitStatus` caller, or child-level `syncGit` import.

- [ ] **Step 1: Write RED App polling tests**

Add `getGitStatus` and `syncGit` to the `api.js` mock, imports, and `apiMocks` in `web/src/App.test.js`:

```js
vi.mock('./lib/api.js', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    getConfig: vi.fn(),
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
    createNote: vi.fn(),
    renameNote: vi.fn(),
    deleteNote: vi.fn(),
    uploadAsset: vi.fn(),
    getGitStatus: vi.fn(),
    syncGit: vi.fn(),
  }
})
```

Add both names to the existing API import and `apiMocks`, then add these defaults in `beforeEach`:

```js
vi.mocked(getGitStatus).mockResolvedValue({ statuses: [] })
vi.mocked(syncGit).mockResolvedValue({
  operation_id: 'op-sync', status: 'queued', deduplicated: false,
})
```

Add this configured fixture and polling test:

```js
const gitConfig = {
  ...completedConfig,
  bases: [
    {
      ...completedConfig.bases[0],
      git_url: 'origin-personal', git_branch: 'main',
      auto_sync_interval_minutes: 15,
      git_commit_message_template: 'sync {{base}}',
    },
    {
      ...completedConfig.bases[1],
      git_url: 'origin-work', git_branch: 'release',
      auto_sync_interval_minutes: 15,
      git_commit_message_template: 'sync {{base}}',
    },
  ],
}

it('owns one all-base poll cadence and derives active status by exact name', async () => {
  vi.useFakeTimers()
  vi.mocked(getConfig).mockResolvedValue(gitConfig)
  vi.mocked(getGitStatus)
    .mockResolvedValueOnce({ statuses: [
      { base: 'work', state: 'conflict', ahead: 0, behind: 0, changed_paths: [] },
      { base: 'personal', state: 'ready', ahead: 0, behind: 0, changed_paths: [] },
    ] })
    .mockRejectedValueOnce(new Error('temporary polling failure'))
    .mockResolvedValueOnce({ statuses: [
      { base: 'personal', state: 'syncing', ahead: 0, behind: 0, changed_paths: [] },
      { base: 'work', state: 'ready', ahead: 0, behind: 0, changed_paths: [] },
    ] })

  try {
    const result = render(App)
    await vi.advanceTimersByTimeAsync(0)
    expect(getGitStatus).toHaveBeenCalledOnce()
    expect(getGitStatus).toHaveBeenLastCalledWith()
    expect(screen.getByRole('button', {
      name: 'Открыть детали Git: Синхронизировано',
    })).toBeVisible()

    await vi.advanceTimersByTimeAsync(2000)
    expect(getGitStatus).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('button', {
      name: 'Открыть детали Git: Синхронизировано',
    })).toBeVisible()

    await vi.advanceTimersByTimeAsync(2000)
    expect(getGitStatus).toHaveBeenCalledTimes(3)
    expect(screen.getByRole('button', {
      name: 'Открыть детали Git: Выполняется',
    })).toBeVisible()

    result.unmount()
    await vi.advanceTimersByTimeAsync(10000)
    expect(getGitStatus).toHaveBeenCalledTimes(3)
  } finally {
    vi.clearAllTimers()
    vi.useRealTimers()
  }
})
```

Expected behavior encoded here: every request is `getGitStatus()` with no base argument, a failed poll keeps the last successful status visible, active status follows `config.current_base`, and unmount stops the sole timer.

- [ ] **Step 2: Write RED shared manual-sync boundary tests**

Append these tests to `web/src/App.test.js`:

```js
it('flushes the dirty editor before footer manual sync and then refreshes status', async () => {
  const user = userEvent.setup()
  const note = fileNode('draft.md')
  vi.mocked(getConfig).mockResolvedValue(gitConfig)
  vi.mocked(getNotes).mockResolvedValue([note])
  vi.mocked(getNote).mockResolvedValue({ content: '# Original' })
  vi.mocked(getGitStatus).mockResolvedValue({ statuses: [
    { base: 'personal', state: 'ready', ahead: 1, behind: 0, changed_paths: [] },
    { base: 'work', state: 'ready', ahead: 0, behind: 0, changed_paths: [] },
  ] })

  render(App)
  await user.click(await screen.findByRole('button', { name: 'draft.md' }))
  const editor = await screen.findByLabelText('Markdown')
  await user.clear(editor)
  await user.type(editor, '# Must be saved')
  await user.click(screen.getByRole('button', {
    name: 'Открыть детали Git: Есть локальные изменения',
  }))
  await user.click(screen.getByRole('button', { name: 'Синхронизировать Git' }))

  await waitFor(() => expect(saveNote).toHaveBeenCalledWith(note.id, '# Must be saved'))
  await waitFor(() => expect(syncGit).toHaveBeenCalledWith('personal'))
  expect(saveNote.mock.invocationCallOrder[0]).toBeLessThan(syncGit.mock.invocationCallOrder[0])
  await waitFor(() => expect(getGitStatus).toHaveBeenCalledTimes(2))
})

it('does not call Git sync when footer flush fails and preserves the dirty buffer', async () => {
  const user = userEvent.setup()
  const note = fileNode('draft.md')
  vi.mocked(getConfig).mockResolvedValue(gitConfig)
  vi.mocked(getNotes).mockResolvedValue([note])
  vi.mocked(getNote).mockResolvedValue({ content: '# Original' })
  vi.mocked(getGitStatus).mockResolvedValue({ statuses: [
    { base: 'personal', state: 'ready', ahead: 0, behind: 0, changed_paths: [] },
  ] })
  vi.mocked(saveNote).mockRejectedValue(new Error('Диск недоступен'))

  render(App)
  await user.click(await screen.findByRole('button', { name: 'draft.md' }))
  const editor = await screen.findByLabelText('Markdown')
  await user.clear(editor)
  await user.type(editor, '# Keep this buffer')
  await user.click(screen.getByRole('button', {
    name: 'Открыть детали Git: Синхронизировано',
  }))
  await user.click(screen.getByRole('button', { name: 'Синхронизировать Git' }))

  await waitFor(() => expect(
    screen.getAllByRole('alert').some((alert) => alert.textContent.includes('Диск недоступен')),
  ).toBe(true))
  expect(syncGit).not.toHaveBeenCalled()
  expect(editor).toHaveValue('# Keep this buffer')
  expect(screen.getByText('Ошибка сохранения')).toBeVisible()
})

it('routes settings manual sync through the same owner while NotesWorkspace is unmounted', async () => {
  const user = userEvent.setup()
  vi.mocked(getConfig).mockResolvedValue(gitConfig)
  vi.mocked(getGitStatus).mockResolvedValue({ statuses: [
    { base: 'personal', state: 'ready', ahead: 0, behind: 0, changed_paths: [] },
    { base: 'work', state: 'ready', ahead: 1, behind: 0, changed_paths: [] },
  ] })
  render(App)
  await screen.findByText('Выберите заметку')
  await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))
  await user.click(screen.getByRole('tab', { name: 'Git' }))
  expect(screen.queryByLabelText('Markdown')).not.toBeInTheDocument()

  await user.click(within(
    screen.getByRole('article', { name: 'Git для базы work' }),
  ).getByRole('button', { name: 'Синхронизировать сейчас' }))

  await waitFor(() => expect(syncGit).toHaveBeenCalledOnce())
  expect(syncGit).toHaveBeenCalledWith('work')
  await waitFor(() => expect(getGitStatus).toHaveBeenCalledTimes(2))
})

it('keeps manual sync globally single-flight across settings cards', async () => {
  const user = userEvent.setup()
  const request = Promise.withResolvers()
  vi.mocked(getConfig).mockResolvedValue(gitConfig)
  vi.mocked(getGitStatus).mockResolvedValue({ statuses: [
    { base: 'personal', state: 'ready', ahead: 1, behind: 0, changed_paths: [] },
    { base: 'work', state: 'ready', ahead: 1, behind: 0, changed_paths: [] },
  ] })
  vi.mocked(syncGit).mockReturnValue(request.promise)
  render(App)
  await screen.findByText('Выберите заметку')
  await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))
  await user.click(screen.getByRole('tab', { name: 'Git' }))
  const first = within(screen.getByRole('article', { name: 'Git для базы personal' }))
    .getByRole('button', { name: 'Синхронизировать сейчас' })
  await fireEvent.click(first)
  await fireEvent.click(first)

  expect(syncGit).toHaveBeenCalledOnce()
  for (const button of screen.getAllByRole('button', { name: 'Синхронизировать сейчас' })) {
    expect(button).toBeDisabled()
  }
  request.resolve({ operation_id: 'op-sync', status: 'queued', deduplicated: false })
  await request.promise
})
```

The settings test proves that the settings origin does not gain a separate handler: it reaches the same `runGitSync` function even when `notesWorkspace` is absent. The implementation step below contains the mandatory unconditional `await flushWorkspace()` immediately before every `syncGit(baseName)`.

- [ ] **Step 3: Run App tests RED under Node 24**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/App.test.js
```

Expected: FAIL because `App.svelte` neither starts the Git poller nor supplies Git props/callbacks to either workspace.

- [ ] **Step 4: Add the sole poller state and exact active-base derivation**

Update imports in `web/src/App.svelte`:

```js
import {
  deleteNote,
  getConfig,
  getGitStatus,
  getNote,
  renameNote,
  saveNote,
  switchBase,
  syncGit,
} from './lib/api.js'
import { createGitStatusPoller } from './lib/git/git-status-poller.js'
```

Add this state next to the existing workspace state. Keep the existing save-status `statusTimer` and `statusGeneration`; these new names are deliberately Git-specific:

```js
let gitStatuses = $state([])
let gitPollError = $state('')
let gitBusyBase = $state('')
let gitActionErrors = $state({})
let gitPoller = null
let gitPolling = false

let currentBase = $derived(activeBase(config))
let activeGitStatus = $derived(
  gitStatuses.find((status) => status.base === config?.current_base) ?? null,
)

function applyGitStatuses(statuses) {
  gitStatuses = Array.isArray(statuses) ? statuses : []
}

function ensureGitPolling() {
  if (!mounted || gitPolling || !gitPoller || config?.setup_completed !== true) return
  gitPolling = true
  gitPoller.start()
}

function refreshGitStatuses() {
  ensureGitPolling()
  return gitPoller?.refresh() ?? Promise.resolve(null)
}
```

At the end of `applyConfig`, after assigning `config` and `basePath`, call only:

```js
ensureGitPolling()
```

Do not clear `gitStatuses` during editor reset, screen changes, transient poll errors, or a base switch. The next successful all-base snapshot atomically replaces it.

- [ ] **Step 5: Implement the one mandatory manual-sync boundary**

Add this function immediately after `flushWorkspace` in `web/src/App.svelte`:

```js
async function runGitSync(baseName) {
  if (!mounted || gitBusyBase !== '') return
  gitBusyBase = baseName
  gitActionErrors = { ...gitActionErrors, [baseName]: '' }
  let flushComplete = false

  try {
    try {
      await flushWorkspace()
      flushComplete = true
    } catch (error) {
      if (mounted && saveStatus !== 'error') showSaveError(error)
      throw error
    }

    if (!mounted) return
    await syncGit(baseName)
    if (!mounted) return
    await refreshGitStatuses()
  } catch (error) {
    if (!mounted) return
    const fallback = flushComplete
      ? 'Не удалось запустить Git-синхронизацию'
      : 'Не удалось сохранить рабочую область перед Git-синхронизацией'
    gitActionErrors = {
      ...gitActionErrors,
      [baseName]: errorMessage(error, fallback),
    }
  } finally {
    if (mounted && gitBusyBase === baseName) gitBusyBase = ''
  }
}
```

There is exactly one `syncGit(` call in all frontend component code, and it is textually dominated by `await flushWorkspace()`. A failed upload/save reaches the catch before `syncGit`, preserves `markdownContent`/`dirty`, and reports both the existing save error and the Git action error. Do not move `syncGit` into either child callback.

- [ ] **Step 6: Start and stop exactly one poller instance**

Replace the existing `onMount` block with this complete lifecycle block, retaining all existing cleanup:

```js
onMount(() => {
  mounted = true
  gitPoller = createGitStatusPoller({
    load: getGitStatus,
    onStatuses: applyGitStatuses,
    onError: (error) => {
      if (!mounted) return
      gitPollError = error ? errorMessage(error, 'Не удалось обновить статус Git') : ''
    },
  })
  void loadApplication()

  return () => {
    mounted = false
    loadToken += 1
    noteRequestToken += 1
    gitPolling = false
    gitPoller?.stop()
    gitPoller = null
    resetTransitionState()
    clearSaveTimer()
    clearStatusTimer()
  }
})
```

`loadApplication`, `finishSetup`, and every settings config publication already call `applyConfig`; therefore `ensureGitPolling` starts after the first completed configuration and never restarts for editor/settings navigation. Do not add a `$effect` that constructs or starts a poller.

- [ ] **Step 7: Wire both workspaces to the same owner**

Add these props to the existing `NotesWorkspace` invocation:

```svelte
gitBase={currentBase}
gitStatus={activeGitStatus}
gitSyncBusy={gitBusyBase === config?.current_base}
gitSyncError={gitActionErrors[config?.current_base] || ''}
onGitSync={runGitSync}
```

Add these props to the existing `SettingsWorkspace` invocation:

```svelte
gitStatuses={gitStatuses}
gitPollError={gitPollError}
gitBusyBase={gitBusyBase}
gitActionErrors={gitActionErrors}
onGitSync={runGitSync}
onGitRefresh={refreshGitStatuses}
```

Keep `onConfigChange={applyConfig}`. Configure and disable responses flow through `replaceConfigBase` in `GitSettingsSection`, while status responses flow only through `applyGitStatuses` in App.

- [ ] **Step 8: Run Task 9 GREEN**

Run:

```bash
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/App.test.js src/lib/settings/SettingsWorkspace.test.js src/lib/NotesWorkspace.test.js src/lib/git/git-status-poller.test.js
```

Expected: all four files PASS; only no-argument `getGitStatus()` polls, failed polls retain rendered data, both manual-sync origins call the same App function, save precedes sync, failed flush prevents sync, and cleanup leaves zero Git timers.

- [ ] **Step 9: Commit serial App integration**

```bash
git add web/src/App.svelte web/src/App.test.js
git commit -m "feat: integrate git status and manual sync"
```

### Task 10: Run Focused And Full Frontend Verification

**Files:**
- Verify only; no planned source changes

- [ ] **Step 1: Activate and prove Node.js 24 before npm runs**

Run in a shell with `nvm` available:

```bash
nvm install 24
nvm use 24
node --version
node -e "if (process.versions.node.split('.')[0] !== '24') process.exit(1)"
```

Expected: `nvm` selects Node 24, `node --version` prints `v24.x.x`, and the assertion exits `0`. If the repository runner provisions Node through another version manager, activate its Node 24 environment first and still run the two exact `node` checks above. Do not continue under Node 20 and do not edit dependencies.

- [ ] **Step 2: Install the locked frontend dependency graph with Node 24**

Run:

```bash
npm --prefix web ci
```

Expected: exit `0`; `web/package-lock.json` remains unchanged.

- [ ] **Step 3: Run the complete focused Git frontend suite**

Run:

```bash
npm --prefix web test -- --run \
  src/lib/api.test.js \
  src/lib/git/git-settings.test.js \
  src/lib/git/git-status-poller.test.js \
  src/lib/git/GitStatusIndicator.test.js \
  src/lib/settings/GitSetupWizard.test.js \
  src/lib/settings/GitBaseCard.test.js \
  src/lib/settings/GitSettingsSection.test.js \
  src/lib/settings/SettingsWorkspace.test.js \
  src/lib/setup/BaseForm.test.js \
  src/lib/NotesWorkspace.test.js \
  src/App.test.js
```

Expected: Vitest exits `0`; all eleven listed test files PASS with no unhandled worker error, unhandled rejection, timeout, or accessibility warning.

- [ ] **Step 4: Run the full frontend suite**

Run:

```bash
npm --prefix web test -- --run
```

Expected: Vitest exits `0`; every frontend test file passes under Node 24 and the Node 20 `webidl.util.markAsUncloneable` worker failure does not appear.

- [ ] **Step 5: Build the production frontend**

Run:

```bash
npm --prefix web run build
```

Expected: Vite prints `built in`, exits `0`, and reports no Svelte compile or accessibility warning. Generated `web/dist/` output is build evidence, not an additional source change to stage unless repository policy already tracks a required artifact.

- [ ] **Step 6: Prove the single-owner and scope constraints**

Run:

```bash
git grep -nE "createGitStatusPoller|getGitStatus\(|syncGit\(" -- web/src
git grep -nE "resumeGit|/api/git/resume|/api/git/conflicts?|resolveGitConflict|completeGitConflict|abortGitConflict|note_changed|stale_note|staleNote|circuit.?breaker" -- \
  web/src ':(exclude,glob)web/src/**/*.test.js' ':(exclude,glob)web/src/test/**'
git grep -nE "Git, скоро|Автосинхронизация будет доступна позже" -- \
  web/src/lib/setup web/src/lib/settings \
  ':(exclude,glob)web/src/**/*.test.js'
git diff --check
git status --short
```

Expected:

```text
createGitStatusPoller appears only in git-status-poller.js, its unit test, and App.svelte.
The production getGitStatus call is only the App-owned poller's load callback.
The production syncGit call is only inside App.svelte runGitSync after await flushWorkspace().
The excluded-feature scan prints no production match.
The stale-placeholder scan prints no match.
git diff --check prints nothing and exits 0.
git status lists only files named in this plan plus expected build artifacts, if tracked by repository policy.
```

Tests may contain the searched wrapper names as mocks/assertions; inspect each result and reject any second production owner.

- [ ] **Step 7: Perform the final accessibility and responsive review**

At 320 CSS px and at a desktop width, verify these exact outcomes in the Vite preview or the embedded application:

```text
Settings tabs are reachable by Tab. Below `md`, `aria-orientation=horizontal` and Left/Right change both focus and panel; at `md` and above, `aria-orientation=vertical` and Up/Down do so while preserving the desktop sidebar; Home/End work in both orientations.
Every wizard step focuses its h1; URL, branch, interval/template, and general API errors receive focus.
All mutation consequences and every backend warning remain readable without horizontal page scroll.
Git cards stay one-column until xl and their actions wrap instead of overflowing.
The notes footer grows beyond one line when needed and never covers the editor.
Indicator details fit within calc(100vw - 1.5rem), close by keyboard, and expose named controls.
Pending probe/configure/disable/manual-sync operations cannot be submitted twice.
Conflict, paused, and needs_reconnect expose no enabled manual-sync button.
No resume, conflict-resolution, stale-note, autosync breaker, or autosync-resume UI is present.
```

- [ ] **Step 8: Review the final diff and retain the task commits**

Run:

```bash
git status --short
git diff --stat
git log --oneline -10
```

Expected: the diff is limited to the frontend files in the File Map, no dependency/backend file changed, and Tasks 1-9 have one focused commit each. Task 10 creates no empty commit; if verification required a correction, rerun the failing focused/full command and commit only the corrected task's owned files with a specific non-amend commit.

## Completion Checklist

- [ ] `App.svelte` owns one poller, one status array, and one `runGitSync` boundary.
- [ ] All-base polling survives transient errors without clearing the last snapshot.
- [ ] Git settings show one exact-name status card per configured notes base.
- [ ] The wizard performs URL probe, branch re-probe, schedule/template preview, and review/confirmation in four focused steps.
- [ ] URL probes always send `git_branch: ''`; valid branchless `can_configure: false` discovery advances, stale branches are reconciled after Back/URL changes, and only a selected-branch `can_configure: true` probe reaches review.
- [ ] Review lists create repository, add/replace origin, create branch, merge histories, and every backend warning when applicable.
- [ ] Configure/reconfigure replaces one returned base; disable confirms that on-disk Git data remains.
- [ ] Base setup/edit forms and base-management cards contain no Git placeholders.
- [ ] The active-base footer indicator wraps, opens details, and delegates manual sync.
- [ ] Both settings and footer manual sync await `flushWorkspace`; failed flush never calls `syncGit` and preserves the editor buffer.
- [ ] Conflict is read-only status presentation only; conflict/stale-note workflows and autosync circuit-breaker/resume API, state, and UI remain outside this plan.
- [ ] Focused tests, full tests, production build, ownership scans, scope scans, and whitespace checks pass under Node 24.
