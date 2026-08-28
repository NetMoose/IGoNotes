# Setup and Settings Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Реализовать на Svelte 5 обязательный трёхшаговый мастер первого запуска, полноэкранные настройки баз и безопасное переключение активной базы с немедленным сохранением заметки.

**Architecture:** `App.svelte` становится небольшой state machine без router и выбирает один из экранов `loading/error/setup/editor/settings`. Все HTTP-вызовы проходят через единый `api.js`, общая форма базы используется мастером и настройками, а переходы из редактора координируются через один `flushPendingSave`, который отменяет debounce и не допускает открытия настроек или переключения базы после ошибки сохранения.

**Tech Stack:** Svelte 5.56 с runes (`$state`, `$props`, `$bindable`, `$effect`), Vite 8, Tailwind CSS 4, Vitest 4.1, jsdom, Testing Library for Svelte, user-event, jest-dom.

---

## Границы задачи

- Backend уже предоставляет `GET/PUT /api/config`, `POST /api/setup`, `POST/PUT/DELETE /api/bases`, `POST /api/bases/switch` и `POST /api/system/select-directory` в соответствии со спецификацией.
- Frontend не создаёт router: экран хранится в локальном состоянии `App.svelte`, URL не меняется.
- Git URL и автосинхронизация только показываются отключёнными. Frontend не отправляет Git-настройки и не меняет `auto_sync`.
- Забывание базы удаляет только запись из config; текст подтверждения прямо сообщает, что каталог и файлы останутся на диске.
- Физическое перемещение каталогов, удаление файлов базы и Git-синхронизация не реализуются.
- Все команды `git commit` ниже являются шагами будущего выполнения плана. При создании этого документа коммиты не выполняются.

## Карта файлов

### Создать

- `web/vitest-setup.js`: подключить matchers `@testing-library/jest-dom/vitest`.
- `web/src/lib/api.js`: единый `request`, класс `ApiError`, settings/base/picker API и обёртки существующего note API.
- `web/src/lib/api.test.js`: проверить JSON, `204`, структурированные и не-JSON ошибки, URL/query и тела запросов.
- `web/src/lib/base-draft.js`: нормализация формы, клиентская validation и вычисление итогового пути без обращения к файловой системе.
- `web/src/lib/base-draft.test.js`: unit-тесты имён, обязательного пути, duplicate name и Windows/POSIX отображения пути.
- `web/src/lib/setup/DirectoryField.svelte`: label, ручное поле пути, кнопка нативного picker и ручной fallback при `501`.
- `web/src/lib/setup/DirectoryField.test.js`: выбор, отмена, unavailable и field error.
- `web/src/lib/setup/BaseForm.svelte`: общая форма create/connect/edit с field errors, busy-state и переводом фокуса.
- `web/src/lib/setup/BaseForm.test.js`: validation, сохранение введённых данных и callback payload.
- `web/src/lib/setup/SetupWizard.svelte`: выбранная компоновка C и три обязательных шага.
- `web/src/lib/setup/SetupWizard.test.js`: переходы, create/connect, итоговый путь, API errors и успешное завершение.
- `web/src/lib/NotesWorkspace.svelte`: существующий editor layout, header, settings button и status bar.
- `web/src/lib/NotesWorkspace.test.js`: контракт выбора/удаления, settings button и отображение save status/base path.
- `web/src/lib/settings/BaseCard.svelte`: карточка базы и допустимые действия.
- `web/src/lib/settings/SettingsWorkspace.svelte`: полноэкранная навигация, список баз, add/edit/forget/switch и отключённый Git.
- `web/src/lib/settings/SettingsWorkspace.test.js`: карточки, CRUD, ограничения forget, disabled Git и ошибки по месту операции.
- `web/src/lib/app-transitions.js`: последовательные операции `flush -> open settings` и `flush -> switch -> commit`.
- `web/src/lib/app-transitions.test.js`: порядок вызовов и отмена переходов после save/API error.
- `web/src/test/EditorStub.svelte`: доступный textarea-stub CodeMirror для component-тестов `App`.
- `web/src/App.test.js`: loading/retry, блокирующий setup, editor/settings state и очистка после switch.

### Изменить

- `web/package.json`: добавить test scripts и пять devDependencies с зафиксированными текущими major/minor версиями.
- `web/package-lock.json`: зафиксировать npm dependency graph после `npm install`.
- `web/vite.config.js`: добавить официальный Vitest `jsdom` environment, setup file и browser resolve condition для Svelte.
- `web/src/App.svelte`: заменить прямой монтаж editor на state machine, хранить config/note/save state и координировать безопасные переходы.
- `web/src/lib/Sidebar.svelte`: заменить пять прямых `fetch` на функции `api.js`, добавить доступное сообщение об ошибке и `aria-busy`.
- `web/src/lib/Editor.svelte`: заменить upload `fetch` на `uploadAsset` из `api.js`.
- `web/src/lib/Modal.svelte`: добавить dialog semantics, description, busy/disabled/danger props, Escape и предсказуемый initial focus.
- `web/src/app.css`: добавить только глобальные focus/disabled правила, которые невозможно выразить локально без дублирования.
- `AGENTS.md`: отметить мастер реализованным, описать settings/base operations и актуализировать API/status.
- `docs/user.md`: описать первый запуск, создание/подключение, обычные настройки, переключение и безопасное забывание.
- `docs/developer.md`: описать frontend state, API client, Vitest-команды и новые endpoints.
- `site/docs/user.md`: опубликовать пользовательское описание мастера и управления базами.
- `site/docs/developer.md`: опубликовать архитектуру frontend и тестовые команды.

### Не изменять

- `docs/superpowers/specs/2026-08-27-setup-settings-ui-design.md`.
- Другие файлы в `docs/superpowers/plans/`.
- Backend-файлы: этот план принимает контракт backend как готовую зависимость.

## Контракты до начала реализации

Эти имена и сигнатуры фиксируются до компонентных задач и используются без переименований дальше по плану.

### Модели и API

```js
/** @typedef {{ name: string, path: string, auto_sync: boolean }} Base */
/** @typedef {{ base_dir: string, bases: Base[], current_base: string, setup_completed: boolean }} Config */
/** @typedef {'create' | 'connect'} BaseMode */
/** @typedef {'create' | 'connect' | 'edit'} FormMode */
/** @typedef {{ mode: BaseMode, name: string, path: string }} BaseDraft */
/** @typedef {{ name: string, path: string }} EditBaseDraft */

export class ApiError extends Error {
  constructor({ status = 0, code = 'network_error', message, field = '' })
}

export function getConfig()                         // Promise<Config>
export function updateConfig(config)                // Promise<Config>
export function completeSetup(draft)                // Promise<Config>
export function createBase(draft)                   // Promise<Config>
export function updateBase(originalName, draft)     // Promise<Config>
export function forgetBase(name)                    // Promise<Config>
export function switchBase(name)                    // Promise<Config>
export function selectDirectory()                   // Promise<string | null>
export function getInfo()                           // Promise<{base_path: string}>
export function getNote(id)                         // Promise<{content: string}>
export function saveNote(id, content)               // Promise<null | object>
export function getNotes()                          // Promise<object[]>
export function syncNotes()                         // Promise<null | object>
export function createNote(payload)                 // Promise<null | object>
export function renameNote(id, newName)             // Promise<null | object>
export function deleteNote(id)                      // Promise<null | object>
export function uploadAsset(file)                   // Promise<{path: string, url?: string}>
```

После каждой успешной settings-мутации `completeSetup/createBase/updateBase/forgetBase/switchBase` клиент выполняет `GET /api/config` и возвращает именно `Config`. Это не связывает UI с необязательной формой body мутационного ответа и гарантирует актуальный `current_base`. `selectDirectory` возвращает `null` только для `204`; `501` остаётся `ApiError` с `code === 'directory_picker_unavailable'`.

### Общие функции формы

```js
export function normalizeBaseDraft(draft)
// => { mode, name: draft.name.trim(), path: draft.path.trim() }

export function validateBaseDraft(draft, { existingNames = [], originalName = '' } = {})
// => объект вида { name?: string, path?: string }; пустой объект означает успех

export function resolveBasePath({ mode, name, path })
// create: отображаемый <parent>/<trimmed name>; connect: точный trimmed path

export function activeBase(config)
// => Base | null по config.current_base
```

Frontend проверяет обязательность полей, duplicate name с учётом регистра и запрещённые для `create` имена `.`, `..`, `/`, `\`. Существование/доступность каталогов проверяет backend; его `field` привязывается к соответствующему полю.

### Props и callbacks Svelte-компонентов

`DirectoryField.svelte`:

```svelte
<script>
  let {
    id,
    label = 'Каталог',
    value = $bindable(''),
    error = '',
    hint = '',
    disabled = false,
    onPickerNotice = () => {},
  } = $props();
</script>
```

`onPickerNotice(message: string)` устанавливает или очищает неблокирующее сообщение о ручном вводе. Отмена picker вызывает `onPickerNotice('')` и не меняет `value`.

`BaseForm.svelte`:

```svelte
<script>
  let {
    formId,
    mode,
    initialName = '',
    initialPath = '',
    existingNames = [],
    originalName = '',
    submitLabel,
    busy = false,
    apiError = null,
    showMode = false,
    onSubmit,
    onCancel = null,
  } = $props();
</script>
```

`onSubmit(draft: BaseDraft | EditBaseDraft)` вызывается только после client validation. Для `mode === 'edit'` payload равен `{name, path}`; `showMode` применяется только в add-form и переключает `create/connect`.

`SetupWizard.svelte`:

```svelte
<script>
  let { config, onComplete } = $props();
</script>
```

`onComplete(config: Config)` вызывается после успешного `completeSetup`. Назад с шага 1 отсутствует; закрытие wizard отсутствует.

`NotesWorkspace.svelte`:

```svelte
<script>
  let {
    activeNote,
    content = $bindable(''),
    saveStatus = 'idle',
    basePath = '',
    error = '',
    onSelectNote,
    onDeleteNote,
    onSave,
    onOpenSettings,
  } = $props();
</script>
```

`onSelectNote(node)` загружает заметку в `App`; `onDeleteNote(id)` очищает выбранную заметку при совпадении; `onSave()` запускает немедленное сохранение; `onOpenSettings()` возвращает promise и сама кнопка блокируется при `saveStatus === 'saving'`. Непустой `error` показывается в header через `role="alert"`.

`BaseCard.svelte`:

```svelte
<script>
  let {
    base,
    current = false,
    canForget = false,
    busyAction = '',
    error = '',
    onOpen,
    onEdit,
    onForget,
  } = $props();
</script>
```

`onForget(base, triggerElement)` передаёт DOM-кнопку, чтобы после отмены dialog вернуть focus в исходное действие. Активная карточка не рендерит `Открыть` и `Забыть`; при одной базе `Забыть` не рендерится ни для одной карточки.

`SettingsWorkspace.svelte`:

```svelte
<script>
  let {
    config,
    onConfigChange,
    onSwitch,
    onBack,
  } = $props();
</script>
```

`onConfigChange(config: Config)` применяется после add/edit/forget. `onSwitch(name: string): Promise<void>` полностью принадлежит `App`, потому что только `App` знает о dirty note. `onBack()` возвращает editor без сетевого запроса.

`Modal.svelte` сохраняет существующие props и добавляет:

```svelte
<script>
  let {
    show = false,
    title = '',
    description = '',
    onConfirm,
    onCancel,
    confirmText = 'OK',
    cancelText = 'Отмена',
    input = false,
    inputValue = $bindable(''),
    error = '',
    busy = false,
    confirmDisabled = false,
    danger = false,
  } = $props();
</script>
```

### State machine `App.svelte`

```js
let screen = $state('loading'); // loading | error | setup | editor | settings
let config = $state(null);
let loadError = $state('');
let activeNote = $state(null);
let markdownContent = $state('');
let basePath = $state('');
let dirty = $state(false);
let saveStatus = $state('idle'); // idle | saving | saved | error
let transitionError = $state('');
```

Единственные переходы:

```text
loading -> setup       GET config: setup_completed=false
loading -> editor      GET config: setup_completed=true
loading -> error       GET config failed
error -> loading       retry
setup -> editor        POST setup succeeded, затем GET config
editor -> settings     flushPendingSave succeeded
settings -> editor     back
settings -> editor     flushPendingSave + POST switch + GET config succeeded
```

`flushPendingSave()` очищает `saveTimer`, ждёт уже выполняющееся сохранение, при `dirty === true` сохраняет текущие `{id, content}`, сбрасывает `dirty` только после успеха и пробрасывает ошибку. При ошибке экран не меняется.

### Task 1: Настроить Vitest 4 и Svelte component testing

**Files:**
- Modify: `web/package.json:6-20`
- Modify: `web/package-lock.json`
- Modify: `web/vite.config.js:1-8`
- Create: `web/vitest-setup.js`

- [ ] **Step 1: Установить test dependencies точных текущих линий версий**

Run:

```bash
cd web && npm install --save-dev vitest@^4.1.11 jsdom@^30.0.1 @testing-library/svelte@^5.4.2 @testing-library/user-event@^14.6.6 @testing-library/jest-dom@^7.0.1
```

Expected: `package.json` и `package-lock.json` обновлены; npm завершается с кодом 0 без peer dependency error.

- [ ] **Step 2: Добавить scripts в `web/package.json`**

Секция `scripts` должна стать:

```json
"scripts": {
  "dev": "vite",
  "build": "vite build",
  "preview": "vite preview",
  "test": "vitest run",
  "test:watch": "vitest"
}
```

В `devDependencies` должны присутствовать ровно эти новые записи:

```json
"@testing-library/jest-dom": "^7.0.1",
"@testing-library/svelte": "^5.4.2",
"@testing-library/user-event": "^14.6.6",
"jsdom": "^30.0.1",
"vitest": "^4.1.11"
```

- [ ] **Step 3: Настроить официальный jsdom/browser test environment**

Полностью привести `web/vite.config.js` к виду:

```js
import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  resolve: {
    conditions: ['browser'],
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./vitest-setup.js'],
    clearMocks: true,
    restoreMocks: true,
  },
})
```

`test.environment = 'jsdom'` предоставляет DOM, а top-level `resolve.conditions = ['browser']` заставляет Vite/Vitest выбирать browser export Svelte. Это соответствует текущей рекомендации Svelte Testing Library; не добавлять отдельный test config и не дублировать plugins.

- [ ] **Step 4: Подключить jest-dom matchers**

Создать `web/vitest-setup.js`:

```js
import '@testing-library/jest-dom/vitest'
```

- [ ] **Step 5: Запустить пустой test suite и build**

Run: `cd web && npm test -- --passWithNoTests`

Expected: Vitest 4 завершается с кодом 0 и сообщает, что test files не найдены.

Run: `cd web && npm run build`

Expected: Vite production build завершается с кодом 0.

- [ ] **Step 6: Зафиксировать test harness**

```bash
git add web/package.json web/package-lock.json web/vite.config.js web/vitest-setup.js
git commit -m "test: configure svelte component tests"
```

### Task 2: Создать единый API client и нормализацию ошибок

**Files:**
- Create: `web/src/lib/api.js`
- Create: `web/src/lib/api.test.js`

- [ ] **Step 1: Написать RED-тесты базового request contract**

Создать `web/src/lib/api.test.js` с fetch mock и следующими обязательными случаями:

```js
import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  ApiError,
  completeSetup,
  getConfig,
  selectDirectory,
  switchBase,
  updateBase,
  updateConfig,
} from './api.js'

afterEach(() => vi.unstubAllGlobals())

function response(body, init = {}) {
  return new Response(body === null ? null : JSON.stringify(body), {
    status: 200,
    headers: body === null ? {} : { 'Content-Type': 'application/json' },
    ...init,
  })
}

test('getConfig returns decoded config', async () => {
  const config = { base_dir: '/bases', bases: [], current_base: '', setup_completed: false }
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(config)))
  await expect(getConfig()).resolves.toEqual(config)
  expect(fetch).toHaveBeenCalledWith('/api/config', expect.objectContaining({ headers: expect.any(Headers) }))
})

test('structured API error retains status, code, field and message', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(
    { code: 'base_name_conflict', message: 'Имя уже используется', field: 'name' },
    { status: 409 },
  )))
  await expect(completeSetup({ mode: 'create', name: 'work', path: '/notes' })).rejects.toEqual(
    expect.objectContaining({
      name: 'ApiError', status: 409, code: 'base_name_conflict', field: 'name', message: 'Имя уже используется',
    }),
  )
})

test('picker cancel resolves null without parsing JSON', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })))
  await expect(selectDirectory()).resolves.toBeNull()
})

test('picker unavailable remains a typed error', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(
    { code: 'directory_picker_unavailable', message: 'Диалог выбора недоступен' },
    { status: 501 },
  )))
  await expect(selectDirectory()).rejects.toBeInstanceOf(ApiError)
})

test('update and switch encode names and refresh config', async () => {
  const config = {
    base_dir: '/bases',
    bases: [{ name: 'new', path: '/notes', auto_sync: false }],
    current_base: 'new',
    setup_completed: true,
  }
  vi.stubGlobal('fetch', vi.fn()
    .mockResolvedValueOnce(new Response(null, { status: 204 }))
    .mockResolvedValueOnce(response(config))
    .mockResolvedValueOnce(new Response(null, { status: 204 }))
    .mockResolvedValueOnce(response(config)))

  await updateBase('old/name', { name: 'new', path: '/notes' })
  await switchBase('new')

  expect(fetch.mock.calls[0][0]).toBe('/api/bases?name=old%2Fname')
  expect(fetch.mock.calls[0][1]).toMatchObject({ method: 'PUT', body: JSON.stringify({ name: 'new', path: '/notes' }) })
  expect(fetch.mock.calls[2]).toEqual(['/api/bases/switch', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ name: 'new' }),
  })])
})
```

Добавить рядом точные тесты network rejection (`code: network_error`, `status: 0`), non-JSON `500` (`code: http_error`) и `PUT /api/config` без изменения переданного object:

```js
test('network rejection becomes ApiError with status zero', async () => {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('connection refused')))
  await expect(getConfig()).rejects.toEqual(expect.objectContaining({
    name: 'ApiError', status: 0, code: 'network_error', message: 'connection refused',
  }))
})

test('non-JSON HTTP failure gets a stable fallback error', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('proxy failed', {
    status: 500,
    headers: { 'Content-Type': 'text/plain' },
  })))
  await expect(getConfig()).rejects.toEqual(expect.objectContaining({
    name: 'ApiError', status: 500, code: 'http_error', message: 'Ошибка запроса (500)',
  }))
})

test('updateConfig sends the complete config without mutating it', async () => {
  const config = {
    base_dir: '/bases',
    bases: [{ name: 'work', path: '/notes/work', auto_sync: false }],
    current_base: 'work',
    setup_completed: true,
  }
  const before = JSON.parse(JSON.stringify(config))
  vi.stubGlobal('fetch', vi.fn()
    .mockResolvedValueOnce(response({ config, base_path: '/notes/work' }))
    .mockResolvedValueOnce(response(config)))

  await expect(updateConfig(config)).resolves.toEqual(config)
  expect(fetch.mock.calls[0][0]).toBe('/api/config')
  expect(fetch.mock.calls[0][1]).toMatchObject({ method: 'PUT', body: JSON.stringify(before) })
  expect(config).toEqual(before)
})
```

- [ ] **Step 2: Запустить тест и подтвердить RED**

Run: `cd web && npm test -- src/lib/api.test.js`

Expected: FAIL с `Failed to resolve import "./api.js"`.

- [ ] **Step 3: Реализовать request и typed error**

Основа `web/src/lib/api.js`:

```js
export class ApiError extends Error {
  constructor({ status = 0, code = 'network_error', message, field = '' }) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.field = field
  }
}

async function request(path, options = {}) {
  const headers = new Headers(options.headers)
  if (options.body && !(options.body instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  let response
  try {
    response = await fetch(path, { ...options, headers })
  } catch (error) {
    throw new ApiError({
      message: error instanceof Error ? error.message : 'Не удалось связаться с приложением',
    })
  }

  const text = response.status === 204 ? '' : await response.text()
  let payload = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = null
    }
  }

  if (!response.ok) {
    throw new ApiError({
      status: response.status,
      code: payload?.code || 'http_error',
      message: payload?.message || `Ошибка запроса (${response.status})`,
      field: payload?.field || '',
    })
  }

  if (!text) return null
  if (payload === null) {
    throw new ApiError({
      status: response.status,
      code: 'invalid_response',
      message: 'Приложение вернуло некорректный JSON',
    })
  }
  return payload
}

const jsonBody = (value) => JSON.stringify(value)

export const getConfig = () => request('/api/config')

async function mutateConfig(path, options) {
  await request(path, options)
  return getConfig()
}

export const updateConfig = (config) => mutateConfig('/api/config', { method: 'PUT', body: jsonBody(config) })
export const completeSetup = (draft) => mutateConfig('/api/setup', { method: 'POST', body: jsonBody(draft) })
export const createBase = (draft) => mutateConfig('/api/bases', { method: 'POST', body: jsonBody(draft) })
export const updateBase = (originalName, draft) => mutateConfig(`/api/bases?name=${encodeURIComponent(originalName)}`, {
  method: 'PUT', body: jsonBody(draft),
})
export const forgetBase = (name) => mutateConfig(`/api/bases?name=${encodeURIComponent(name)}`, { method: 'DELETE' })
export const switchBase = (name) => mutateConfig('/api/bases/switch', {
  method: 'POST', body: jsonBody({ name }),
})

export async function selectDirectory() {
  const result = await request('/api/system/select-directory', { method: 'POST' })
  return result?.path ?? null
}

export const getInfo = () => request('/api/info')
export const getNote = (id) => request(`/api/note?id=${encodeURIComponent(id)}`)
export const saveNote = (id, content) => request('/api/save', {
  method: 'POST', body: jsonBody({ id, content }),
})
export const getNotes = () => request('/api/notes')
export const syncNotes = () => request('/api/sync', { method: 'POST' })
export const createNote = (payload) => request('/api/notes', {
  method: 'POST', body: jsonBody(payload),
})
export const renameNote = (id, newName) => request('/api/rename', {
  method: 'PUT', body: jsonBody({ id, new_name: newName }),
})
export const deleteNote = (id) => request(`/api/note?id=${encodeURIComponent(id)}`, {
  method: 'DELETE',
})
export function uploadAsset(file) {
  const body = new FormData()
  body.append('file', file)
  return request('/api/assets', { method: 'POST', body })
}
```

Пустой успешный `200` от текущего `DELETE /api/note` нормализуется в `null`; multipart boundary для `uploadAsset` выставляет browser, поэтому `Content-Type` вручную не добавляется.

- [ ] **Step 4: Запустить API tests и подтвердить GREEN**

Run: `cd web && npm test -- src/lib/api.test.js`

Expected: PASS для JSON, network/non-JSON error, `204`, `501`, query encoding и всех wrapper payloads.

- [ ] **Step 5: Зафиксировать API client**

```bash
git add web/src/lib/api.js web/src/lib/api.test.js
git commit -m "feat: add frontend api client"
```

### Task 3: Реализовать общую validation и поле выбора каталога

**Files:**
- Create: `web/src/lib/base-draft.js`
- Create: `web/src/lib/base-draft.test.js`
- Create: `web/src/lib/setup/DirectoryField.svelte`
- Create: `web/src/lib/setup/DirectoryField.test.js`

- [ ] **Step 1: Написать RED unit-тесты формы базы**

Создать `web/src/lib/base-draft.test.js`:

```js
import { describe, expect, test } from 'vitest'
import { activeBase, normalizeBaseDraft, resolveBasePath, validateBaseDraft } from './base-draft.js'

test('normalizes surrounding whitespace without changing case', () => {
  expect(normalizeBaseDraft({ mode: 'create', name: ' Work ', path: ' /notes ' }))
    .toEqual({ mode: 'create', name: 'Work', path: '/notes' })
})

test.each(['', ' ', '.', '..', 'work/team', 'work\\team'])(
  'rejects invalid create name %j',
  (name) => expect(validateBaseDraft({ mode: 'create', name, path: '/notes' })).toHaveProperty('name'),
)

test('detects case-sensitive duplicate but allows unchanged edit name', () => {
  expect(validateBaseDraft(
    { mode: 'connect', name: 'work', path: '/notes/work' },
    { existingNames: ['Work', 'work'] },
  )).toHaveProperty('name')
  expect(validateBaseDraft(
    { mode: 'edit', name: 'work', path: '/notes/work' },
    { existingNames: ['work'], originalName: 'work' },
  )).toEqual({})
})

test('requires a path and computes POSIX and Windows display paths', () => {
  expect(validateBaseDraft({ mode: 'connect', name: 'work', path: ' ' })).toHaveProperty('path')
  expect(resolveBasePath({ mode: 'create', name: 'work', path: '/notes/' })).toBe('/notes/work')
  expect(resolveBasePath({ mode: 'create', name: 'work', path: 'C:\\Notes\\' })).toBe('C:\\Notes\\work')
  expect(resolveBasePath({ mode: 'connect', name: 'work', path: '/notes/work' })).toBe('/notes/work')
})

test('returns the configured active base', () => {
  const work = { name: 'work', path: '/notes/work', auto_sync: false }
  expect(activeBase({ current_base: 'work', bases: [work] })).toBe(work)
  expect(activeBase({ current_base: 'missing', bases: [work] })).toBeNull()
})
```

- [ ] **Step 2: Подтвердить RED**

Run: `cd web && npm test -- src/lib/base-draft.test.js`

Expected: FAIL с отсутствующим `base-draft.js`.

- [ ] **Step 3: Реализовать чистые helpers**

Создать `web/src/lib/base-draft.js`:

```js
export function normalizeBaseDraft(draft) {
  return {
    mode: draft.mode,
    name: draft.name.trim(),
    path: draft.path.trim(),
  }
}

export function validateBaseDraft(draft, { existingNames = [], originalName = '' } = {}) {
  const value = normalizeBaseDraft(draft)
  const errors = {}
  if (!value.name) errors.name = 'Введите имя базы'
  if (value.mode === 'create' && (value.name === '.' || value.name === '..' || /[\\/]/.test(value.name))) {
    errors.name = 'Имя новой базы не может быть точкой и содержать / или \\'
  }
  if (value.name !== originalName && existingNames.includes(value.name)) {
    errors.name = 'База с таким именем уже добавлена'
  }
  if (!value.path) errors.path = 'Укажите каталог'
  return errors
}

export function resolveBasePath(draft) {
  const value = normalizeBaseDraft(draft)
  if (value.mode !== 'create') return value.path
  const windowsOnly = value.path.includes('\\') && !value.path.includes('/')
  const separator = windowsOnly ? '\\' : '/'
  return `${value.path.replace(/[\\/]+$/, '')}${separator}${value.name}`
}

export function activeBase(config) {
  return config?.bases.find((base) => base.name === config.current_base) ?? null
}
```

- [ ] **Step 4: Написать RED component-тесты picker**

Создать `web/src/lib/setup/DirectoryField.test.js`, mock `selectDirectory` и проверить четыре сценария:

```js
import { render, screen } from '@testing-library/svelte'
import { userEvent } from '@testing-library/user-event'
import { beforeEach, expect, test, vi } from 'vitest'
import { ApiError, selectDirectory } from '../api.js'
import DirectoryField from './DirectoryField.svelte'

vi.mock('../api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  selectDirectory: vi.fn(),
}))

beforeEach(() => selectDirectory.mockReset())

test('writes selected path into the bindable input', async () => {
  selectDirectory.mockResolvedValue('/home/user/notes')
  const user = userEvent.setup()
  render(DirectoryField, { id: 'path', value: '', onPickerNotice: vi.fn() })
  await user.click(screen.getByRole('button', { name: 'Обзор' }))
  expect(screen.getByLabelText('Каталог')).toHaveValue('/home/user/notes')
})

test('cancel keeps manual input and does not show an error', async () => {
  selectDirectory.mockResolvedValue(null)
  const notice = vi.fn()
  const user = userEvent.setup()
  render(DirectoryField, { id: 'path', value: '/manual', onPickerNotice: notice })
  await user.click(screen.getByRole('button', { name: 'Обзор' }))
  expect(screen.getByLabelText('Каталог')).toHaveValue('/manual')
  expect(notice).toHaveBeenLastCalledWith('')
})

test('501 explains manual fallback and keeps input enabled', async () => {
  selectDirectory.mockRejectedValue(new ApiError({
    status: 501, code: 'directory_picker_unavailable', message: 'Picker unavailable',
  }))
  const notice = vi.fn()
  const user = userEvent.setup()
  render(DirectoryField, { id: 'path', value: '/manual', onPickerNotice: notice })
  await user.click(screen.getByRole('button', { name: 'Обзор' }))
  expect(notice).toHaveBeenCalledWith('Системный выбор каталога недоступен. Введите путь вручную.')
  expect(screen.getByLabelText('Каталог')).toBeEnabled()
})
```

Четвёртый тест передаёт `error="Каталог не найден"`, проверяет `aria-invalid="true"`, `aria-describedby="path-error"` и видимый текст под полем.

- [ ] **Step 5: Реализовать `DirectoryField.svelte`**

Использовать зафиксированный `$props` contract. `browse()` устанавливает локальный `picking = $state(false)`, вызывает `selectDirectory`, записывает непустой result, отдельно обрабатывает `directory_picker_unavailable`, а остальные ошибки передаёт в `onPickerNotice(error.message)`. Markup обязан содержать связанный `<label for={id}>`, input с `aria-invalid/aria-describedby`, кнопку `type="button"`, status text с `role="status"` и disabled state на время picker.

Ключевой обработчик:

```js
async function browse() {
  picking = true
  onPickerNotice('')
  try {
    const selected = await selectDirectory()
    if (selected) value = selected
  } catch (error) {
    onPickerNotice(error?.code === 'directory_picker_unavailable'
      ? 'Системный выбор каталога недоступен. Введите путь вручную.'
      : error.message)
  } finally {
    picking = false
  }
}
```

- [ ] **Step 6: Подтвердить GREEN и зафиксировать**

Run: `cd web && npm test -- src/lib/base-draft.test.js src/lib/setup/DirectoryField.test.js`

Expected: PASS для helpers и четырёх picker-сценариев.

```bash
git add web/src/lib/base-draft.js web/src/lib/base-draft.test.js web/src/lib/setup/DirectoryField.svelte web/src/lib/setup/DirectoryField.test.js
git commit -m "feat: add shared base form primitives"
```

### Task 4: Реализовать общую форму и трёхшаговый мастер

**Files:**
- Create: `web/src/lib/setup/BaseForm.svelte`
- Create: `web/src/lib/setup/BaseForm.test.js`
- Create: `web/src/lib/setup/SetupWizard.svelte`
- Create: `web/src/lib/setup/SetupWizard.test.js`

- [ ] **Step 1: Написать RED-тесты `BaseForm`**

Тесты через Testing Library должны:

```js
const user = userEvent.setup()
const onSubmit = vi.fn()
render(BaseForm, {
  formId: 'base', mode: 'create', submitLabel: 'Продолжить', onSubmit,
  existingNames: ['personal'],
})
await user.click(screen.getByRole('button', { name: 'Продолжить' }))
expect(screen.getByText('Введите имя базы')).toBeInTheDocument()
expect(screen.getByText('Укажите каталог')).toBeInTheDocument()
expect(screen.getByLabelText('Имя базы')).toHaveFocus()
expect(onSubmit).not.toHaveBeenCalled()
```

Второй тест вводит `work` и `/home/user/notes`, отправляет форму и ожидает `{mode:'create', name:'work', path:'/home/user/notes'}`. Третий rerender передаёт `apiError={new ApiError({field:'path', message:'Каталог не найден'})}`, проверяет field error и сохранение обоих input values. Четвёртый проверяет edit payload без `mode` и disabled controls при `busy=true`.

- [ ] **Step 2: Подтвердить RED и реализовать `BaseForm`**

Run: `cd web && npm test -- src/lib/setup/BaseForm.test.js`

Expected: FAIL с отсутствующим компонентом.

В компоненте использовать `$state` для `selectedMode/name/path/clientErrors/pickerNotice`, `$effect` для преобразования нового `apiError.field` в field/general error и `requestAnimationFrame(() => field.focus())` после failed submit. Не очищать draft после backend error. Форма содержит:

- radio cards `Создать новую`/`Подключить существующую`, только если `showMode`;
- label `Имя базы`;
- `DirectoryField` с label `Родительский каталог` для create и `Каталог существующей базы` для connect/edit;
- disabled Git URL input с текстом `Git, скоро`;
- disabled checkbox `Автосинхронизация будет доступна позже`;
- general error с `role="alert"`;
- cancel button только при непустом `onCancel`;
- submit button с `aria-busy={busy}`.

Отправка:

```js
function submit(event) {
  event.preventDefault()
  const draft = normalizeBaseDraft({ mode: selectedMode, name, path })
  clientErrors = validateBaseDraft(draft, { existingNames, originalName })
  if (Object.keys(clientErrors).length > 0) {
    focusFirstError()
    return
  }
  onSubmit(mode === 'edit' ? { name: draft.name, path: draft.path } : draft)
}
```

- [ ] **Step 3: Подтвердить GREEN формы**

Run: `cd web && npm test -- src/lib/setup/BaseForm.test.js`

Expected: PASS для validation, focus, payload, persisted values и busy state.

- [ ] **Step 4: Написать RED-тесты wizard**

Mock `completeSetup`. Покрыть:

1. Шаг 1 показывает две mode cards, `Назад` и editor/sidebar отсутствуют.
2. Create: шаг 2 сохраняет `work` и `/home/user/notes`; шаг 3 показывает `/home/user/notes/work`.
3. Connect: шаг 3 показывает точный `/srv/existing`.
4. `Назад` с шага 3 возвращает заполненный шаг 2.
5. `ApiError({field:'name'})` возвращает на шаг 2, показывает ошибку у имени и сохраняет path.
6. Успех вызывает `onComplete` с config, возвращённым API, ровно один раз.

Пример перехода:

```js
const user = userEvent.setup()
render(SetupWizard, { config: firstRunConfig, onComplete: vi.fn() })
await user.click(screen.getByRole('button', { name: 'Создать новую' }))
await user.type(screen.getByLabelText('Имя базы'), 'work')
await user.type(screen.getByLabelText('Родительский каталог'), '/home/user/notes')
await user.click(screen.getByRole('button', { name: 'Продолжить' }))
expect(screen.getByRole('heading', { name: 'Проверьте настройки' })).toBeInTheDocument()
expect(screen.getByText('/home/user/notes/work')).toBeInTheDocument()
```

- [ ] **Step 5: Подтвердить RED и реализовать wizard в компоновке C**

Run: `cd web && npm test -- src/lib/setup/SetupWizard.test.js`

Expected: FAIL с отсутствующим компонентом.

`SetupWizard.svelte` использует:

```js
let step = $state(1)
let draft = $state({ mode: '', name: '', path: '' })
let busy = $state(false)
let apiError = $state(null)

async function finish() {
  busy = true
  apiError = null
  try {
    const saved = await completeSetup(normalizeBaseDraft(draft))
    onComplete(saved)
  } catch (error) {
    apiError = error
    if (error.field === 'name' || error.field === 'path') step = 2
  } finally {
    busy = false
  }
}
```

Если `finish()` получает error без `field`, шаг 3 остаётся открыт и показывает `error.message` в общей панели с `role="alert"` над кнопками подтверждения.

Компоновка C задаётся точно так:

- full-screen `min-h-screen bg-slate-100 p-4 sm:p-8`;
- shell `mx-auto grid min-h-[calc(100vh-2rem)] max-w-6xl overflow-hidden rounded-2xl bg-white shadow-xl lg:min-h-[720px] lg:grid-cols-[20rem_minmax(0,1fr)]`;
- слева `bg-blue-700 text-white` с названием IGoNotes, коротким описанием и нумерованным progress list из трёх шагов;
- справа `flex min-w-0 flex-col p-6 sm:p-10 lg:p-14` с `aria-live="polite"` heading и содержимым шага;
- до `lg` синяя rail становится верхней областью, progress list горизонтальный и labels скрываются только визуально через `sr-only sm:not-sr-only`;
- шаг 1 содержит две большие button cards с пояснениями;
- шаг 2 рендерит `BaseForm` без cancel и отдельные `Назад`/`Продолжить`;
- шаг 3 рендерит definition list mode/name/path, warning о том, что connect не перемещает файлы, и кнопки `Назад`/`Завершить настройку`.

На shell использовать `aria-labelledby="setup-title"`; при каждом изменении `step` переводить focus на heading через `tick()`.
Точные headings шагов: `Настройте первую базу`, `Укажите имя и каталог`, `Проверьте настройки`.

- [ ] **Step 6: Подтвердить GREEN и зафиксировать wizard**

Run: `cd web && npm test -- src/lib/setup/BaseForm.test.js src/lib/setup/SetupWizard.test.js`

Expected: PASS для всех переходов, обоих modes, path preview и API errors.

```bash
git add web/src/lib/setup/BaseForm.svelte web/src/lib/setup/BaseForm.test.js web/src/lib/setup/SetupWizard.svelte web/src/lib/setup/SetupWizard.test.js
git commit -m "feat: add first run setup wizard"
```

### Task 5: Выделить editor workspace и перевести существующий UI на API client

**Files:**
- Create: `web/src/test/EditorStub.svelte`
- Create: `web/src/lib/NotesWorkspace.svelte`
- Create: `web/src/lib/NotesWorkspace.test.js`
- Modify: `web/src/lib/Sidebar.svelte:1-297`
- Modify: `web/src/lib/Editor.svelte:175-197`
- Modify: `web/src/lib/Modal.svelte:1-37`

- [ ] **Step 1: Создать Editor stub и написать RED-тест `NotesWorkspace`**

Создать `web/src/test/EditorStub.svelte`:

```svelte
<script>
  let { content = $bindable('') } = $props();
</script>

<label for="editor-stub">Markdown</label>
<textarea id="editor-stub" bind:value={content}></textarea>
```

В test mock `Editor.svelte` этим stub, mock функции `api.js`, вернуть `[]` из `getNotes` и использовать настоящий `Sidebar.svelte`. Проверить:

```js
render(NotesWorkspace, {
  activeNote: null,
  content: '',
  saveStatus: 'idle',
  basePath: '/notes/work',
  error: '',
  onSelectNote: vi.fn(),
  onDeleteNote: vi.fn(),
  onSave: vi.fn(),
  onOpenSettings: vi.fn(),
})
expect(screen.getByText('Выберите заметку')).toBeInTheDocument()
expect(screen.getByTitle('Текущая база заметок')).toHaveTextContent('/notes/work')
await user.click(screen.getByRole('button', { name: 'Открыть настройки' }))
expect(onOpenSettings).toHaveBeenCalledOnce()
```

Отдельные cases проверяют `Сохранение...`, `Сохранено`, `Ошибка сохранения`, disabled save без active note и рендер Editor при active note.

- [ ] **Step 2: Подтвердить RED и создать `NotesWorkspace.svelte`**

Run: `cd web && npm test -- src/lib/NotesWorkspace.test.js`

Expected: FAIL с отсутствующим компонентом.

Перенести из текущего `App.svelte` только markup editor shell. Не переносить загрузку, timers или HTTP. Добавить рядом с save button settings icon button:

```svelte
<button
  type="button"
  onclick={onOpenSettings}
  disabled={saveStatus === 'saving'}
  aria-label="Открыть настройки"
  title="Настройки"
  class="rounded-md p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-blue-600 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-50"
>
  <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" class="h-5 w-5">
    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z" />
    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.09A1.7 1.7 0 0 0 8.97 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.56-1.03H3v-4h.09A1.7 1.7 0 0 0 4.6 8.94a1.7 1.7 0 0 0-.34-1.88L4.2 7l2.83-2.83.06.06a1.7 1.7 0 0 0 1.88.34H9A1.7 1.7 0 0 0 10 3.09V3h4v.09a1.7 1.7 0 0 0 1.03 1.56 1.7 1.7 0 0 0 1.88-.34l.06-.06L19.8 7l-.06.06a1.7 1.7 0 0 0-.34 1.88v.03A1.7 1.7 0 0 0 20.91 10H21v4h-.09A1.7 1.7 0 0 0 19.4 15Z" />
  </svg>
</button>
```

Заменить emoji папки в status bar на inline SVG с `aria-hidden="true"`, чтобы output оставался доступным и ASCII-friendly в source.

- [ ] **Step 3: Перевести Sidebar и Editor на `api.js` без изменения поведения**

В `Sidebar.svelte` импортировать `getNotes/syncNotes/createNote/renameNote/deleteNote`. Каждую текущую проверку `res.ok` заменить на прямой await wrapper; в catch присваивать `operationError = error.message`, показывать `<p role="alert">` в sidebar header и очищать перед новой операцией. На scroll container поставить `aria-busy={isRefreshing}`. Существующие callbacks `onSelect/onDelete` не менять.

В `Editor.svelte` импортировать `uploadAsset`; заменить `fetch('/api/assets', ...)` на `const data = await uploadAsset(file)` и сохранить текущую вставку markdown URL без изменения.

- [ ] **Step 4: Сделать существующий `Modal` доступным**

При `show` overlay получает `role="presentation"`, panel получает `role="dialog"`, `aria-modal="true"`, `aria-labelledby="modal-title"`, условный `aria-describedby="modal-description"`. Title id стабилен в рамках компонента. `Escape` вызывает `onCancel`, Enter не подтверждает при `busy || confirmDisabled`. Confirm button получает `disabled={busy || confirmDisabled}`, `aria-busy={busy}` и red classes при `danger`.

Фокус: input остаётся первым при `input=true`, иначе confirm button. Сохранить bindable `inputValue` и существующие три использования Sidebar без изменения их поведения.

- [ ] **Step 5: Запустить целевые тесты и build**

Run: `cd web && npm test -- src/lib/NotesWorkspace.test.js`

Expected: PASS.

Run: `cd web && npm run build`

Expected: production build проходит; Svelte не сообщает invalid event directive/prop warning.

- [ ] **Step 6: Зафиксировать editor shell и API migration**

```bash
git add web/src/test/EditorStub.svelte web/src/lib/NotesWorkspace.svelte web/src/lib/NotesWorkspace.test.js web/src/lib/Sidebar.svelte web/src/lib/Editor.svelte web/src/lib/Modal.svelte
git commit -m "refactor: isolate notes workspace"
```

### Task 6: Реализовать карточки и полноэкранные настройки баз

**Files:**
- Create: `web/src/lib/settings/BaseCard.svelte`
- Create: `web/src/lib/settings/SettingsWorkspace.svelte`
- Create: `web/src/lib/settings/SettingsWorkspace.test.js`

- [ ] **Step 1: Написать RED-тесты списка и ограничений**

С fixture из двух баз `personal` (active) и `work` проверить:

```js
render(SettingsWorkspace, {
  config,
  onConfigChange: vi.fn(),
  onSwitch: vi.fn(),
  onBack: vi.fn(),
})
expect(screen.getByRole('heading', { name: 'Базы заметок' })).toBeInTheDocument()
expect(screen.getByText('/notes/personal')).toBeInTheDocument()
expect(screen.getByText('/notes/work')).toBeInTheDocument()
expect(screen.getByText('Текущая')).toBeInTheDocument()
expect(screen.getByRole('tab', { name: 'Базы заметок' })).toHaveAttribute('aria-current', 'page')
expect(screen.getByRole('tab', { name: 'Git, скоро' })).toHaveAttribute('aria-disabled', 'true')
expect(screen.getAllByRole('button', { name: 'Забыть' })).toHaveLength(1)
```

С fixture одной active базы `Забыть` и `Открыть` отсутствуют.

- [ ] **Step 2: Написать RED-тесты add/edit/forget/switch**

Mock `createBase/updateBase/forgetBase`; проверить точные вызовы и callbacks:

- `Добавить базу` открывает `BaseForm` с mode selector;
- успешный create вызывает `createBase({mode:'create', name:'work', path:'/notes'})`, затем `onConfigChange(newConfig)` и закрывает форму;
- edit active base вызывает `updateBase('personal',{name:'renamed',path:'/other'})`, обновляет config и оставляет settings открытыми;
- forget показывает `Modal` с фразой `Каталог и файлы останутся на диске`, затем вызывает `forgetBase('work')`;
- `Открыть` вызывает только `onSwitch('work')`, не вызывает API прямо из компонента;
- rejected API error остаётся внутри соответствующей form/card и не очищает ввод.

- [ ] **Step 3: Подтвердить RED**

Run: `cd web && npm test -- src/lib/settings/SettingsWorkspace.test.js`

Expected: FAIL с отсутствующим `SettingsWorkspace.svelte`.

- [ ] **Step 4: Реализовать `BaseCard.svelte` по зафиксированному contract**

Карточка представляет `<article aria-label={`База ${base.name}`}>`, показывает name/path, badge `Текущая`, disabled labels `Git не настроен` и `Автосинхронизация выключена`. Action row:

```svelte
{#if !current}
  <button type="button" onclick={() => onOpen(base.name)} disabled={busyAction !== ''}>Открыть</button>
{/if}
<button type="button" onclick={() => onEdit(base)} disabled={busyAction !== ''}>Изменить</button>
{#if canForget && !current}
  <button type="button" onclick={(event) => onForget(base, event.currentTarget)} disabled={busyAction !== ''}>Забыть</button>
{/if}
{#if error}<p role="alert" class="mt-3 text-sm text-red-700">{error}</p>{/if}
```

Использовать текущие blue/gray Tailwind classes, border-blue для active, red только для forget.

- [ ] **Step 5: Реализовать `SettingsWorkspace.svelte`**

Локальное состояние:

```js
import { tick } from 'svelte'

let panel = $state('list') // list | add | edit
let editingBase = $state(null)
let pendingForget = $state(null)
let forgetTrigger = $state(null)
let busyAction = $state('')
let operationErrors = $state({})
let formError = $state(null)
let listHeading
```

Операции:

```js
async function addBase(draft) {
  busyAction = 'add'
  formError = null
  try {
    onConfigChange(await createBase(draft))
    panel = 'list'
  } catch (error) {
    formError = error
  } finally {
    busyAction = ''
  }
}

async function editBase(draft) {
  busyAction = `edit:${editingBase.name}`
  formError = null
  try {
    onConfigChange(await updateBase(editingBase.name, draft))
    panel = 'list'
    editingBase = null
  } catch (error) {
    formError = error
  } finally {
    busyAction = ''
  }
}

async function confirmForget() {
  const name = pendingForget.name
  busyAction = `forget:${name}`
  try {
    onConfigChange(await forgetBase(name))
    pendingForget = null
    await tick()
    listHeading?.focus()
    forgetTrigger = null
  } catch (error) {
    operationErrors = { ...operationErrors, [name]: error.message }
    pendingForget = null
    await tick()
    forgetTrigger?.focus()
    forgetTrigger = null
  } finally {
    busyAction = ''
  }
}

async function cancelForget() {
  pendingForget = null
  await tick()
  forgetTrigger?.focus()
  forgetTrigger = null
}
```

При открытии dialog callback карточки сохраняет `pendingForget = base` и `forgetTrigger = triggerElement`; `Modal` получает `onCancel={cancelForget}`. Heading списка получает `bind:this={listHeading}` и `tabindex="-1"`. После успешного забывания исходная карточка удалена, поэтому focus переходит на этот heading, а не на отсоединённую кнопку.

Для `openBase(name)` использовать точный обработчик; successful callback закроет settings через `App`:

```js
async function openBase(name) {
  busyAction = `switch:${name}`
  operationErrors = { ...operationErrors, [name]: '' }
  try {
    await onSwitch(name)
  } catch (error) {
    operationErrors = { ...operationErrors, [name]: error.message }
  } finally {
    busyAction = ''
  }
}
```

Layout settings:

- full screen `min-h-screen bg-slate-100`;
- header с IGoNotes и `Назад к заметкам`;
- `mx-auto grid max-w-7xl md:grid-cols-[15rem_minmax(0,1fr)]`;
- nav слева на `md+`, сверху как две tabs на narrow;
- `Базы заметок` имеет `aria-current="page"`;
- disabled `Git, скоро` имеет `aria-disabled="true"`, `tabindex="-1"` и не имеет click handler;
- cards grid `grid gap-4 xl:grid-cols-2`;
- add/edit form занимает одну колонку с max width `max-w-2xl`;
- `BaseForm` получает `existingNames={config.bases.map(({name}) => name)}` и `originalName` при edit.

- [ ] **Step 6: Подтвердить GREEN и зафиксировать settings workspace**

Run: `cd web && npm test -- src/lib/settings/SettingsWorkspace.test.js`

Expected: PASS для list, Git, add, edit, forget, switch delegation и локальных errors.

```bash
git add web/src/lib/settings/BaseCard.svelte web/src/lib/settings/SettingsWorkspace.svelte web/src/lib/settings/SettingsWorkspace.test.js
git commit -m "feat: add notes base settings workspace"
```

### Task 7: Подключить loading/error/setup/editor state machine в App

**Files:**
- Create: `web/src/App.test.js`
- Modify: `web/src/App.svelte:1-131`

- [ ] **Step 1: Подключить созданный в Task 5 CodeMirror stub к App tests**

Проверить, что `web/src/test/EditorStub.svelte` имеет следующий контракт:

```svelte
<script>
  let { content = $bindable('') } = $props();
</script>

<label for="editor-stub">Markdown</label>
<textarea id="editor-stub" bind:value={content}></textarea>
```

- [ ] **Step 2: Написать RED-тесты первоначальной загрузки**

В `App.test.js` mock `./lib/api.js` и `./lib/Editor.svelte`. Обязательные cases:

```js
vi.mock('./lib/Editor.svelte', async () => ({
  default: (await import('./test/EditorStub.svelte')).default,
}))

test('blocks notes UI with wizard until setup is complete', async () => {
  getConfig.mockResolvedValue(firstRunConfig)
  getNotes.mockResolvedValue([])
  render(App)
  expect(screen.getByText('Загрузка настроек...')).toBeInTheDocument()
  expect(await screen.findByRole('heading', { name: 'Настройте первую базу' })).toBeInTheDocument()
  expect(screen.queryByText('База заметок')).not.toBeInTheDocument()
  expect(screen.queryByText('Выберите заметку')).not.toBeInTheDocument()
  expect(getNotes).not.toHaveBeenCalled()
})

test('loads editor for completed setup', async () => {
  getConfig.mockResolvedValue(config)
  getNotes.mockResolvedValue([])
  render(App)
  expect(await screen.findByText('Выберите заметку')).toBeInTheDocument()
  expect(screen.getByTitle('Текущая база заметок')).toHaveTextContent('/notes/personal')
})
```

Третий test отклоняет первый `getConfig`, проверяет blocking `role="alert"`, отсутствие editor/wizard и кнопку `Повторить`; второй click успешно загружает config и открывает editor. Четвёртый проходит wizard mock success и проверяет переход setup -> editor с новым path.

- [ ] **Step 3: Подтвердить RED**

Run: `cd web && npm test -- src/App.test.js -t "blocks|loads editor|Повторить|setup"`

Expected: FAIL, потому что текущий App сразу монтирует Sidebar/editor shell и не запрашивает config.

- [ ] **Step 4: Реализовать загрузку config и screen branches**

В `App.svelte` импортировать `onMount`, API, `activeBase`, `SetupWizard`, `NotesWorkspace`, `SettingsWorkspace`. Реализовать:

```js
async function loadApplication() {
  screen = 'loading'
  loadError = ''
  try {
    config = await getConfig()
    basePath = activeBase(config)?.path ?? ''
    screen = config.setup_completed ? 'editor' : 'setup'
  } catch (error) {
    loadError = error.message
    screen = 'error'
  }
}

function finishSetup(savedConfig) {
  config = savedConfig
  basePath = activeBase(savedConfig)?.path ?? ''
  resetEditorState()
  screen = 'editor'
}

onMount(loadApplication)
```

Перенести текущее note state и заменить прямые fetch на `getNote/saveNote`. До безопасной переработки Task 8 использовать следующие функции, чтобы Task 7 собирался самостоятельно:

```js
async function loadNote(node) {
  try {
    const data = await getNote(node.id)
    activeNote = node
    ignoreNextChange = true
    markdownContent = data.content
    saveStatus = 'idle'
    transitionError = ''
  } catch (error) {
    transitionError = error.message
  }
}

async function saveCurrentNote() {
  if (!activeNote) return
  saveStatus = 'saving'
  try {
    await saveNote(activeNote.id, markdownContent)
    saveStatus = 'saved'
  } catch (error) {
    saveStatus = 'error'
    transitionError = error.message
    throw error
  }
}

function resetEditorState() {
  activeNote = null
  ignoreNextChange = true
  markdownContent = ''
  saveStatus = 'idle'
  transitionError = ''
}

function handleDeletedNote(id) {
  if (activeNote?.id === id) resetEditorState()
}

async function saveNow() {
  if (!activeNote) return
  await saveCurrentNote()
}

function applyConfig(savedConfig) {
  config = savedConfig
  basePath = activeBase(savedConfig)?.path ?? ''
}

function openSettings() {
  screen = 'settings'
}

async function openBase(name) {
  applyConfig(await switchBase(name))
  resetEditorState()
  screen = 'editor'
}
```

В существующем `$effect` timer callback заменить на `void saveCurrentNote().catch(() => {})`; `ignoreNextChange` и задержку 2000 ms оставить без изменения до RED-тестов Task 8.

Task 8 сначала фиксирует RED-тестами, почему два прямых перехода должны быть заменены orchestration через `flushPendingSave`.

Markup использует один root и взаимоисключающие `{#if screen === ...}` branches. Editor/settings ветви передают все зафиксированные props без сокращений:

- loading: centered spinner, heading `Загрузка настроек...`, `role="status"`;
- error: blocking panel, `role="alert"`, exact backend message, button `Повторить`;
- setup: `<SetupWizard {config} onComplete={finishSetup} />`;
- editor:

```svelte
<NotesWorkspace
  {activeNote}
  bind:content={markdownContent}
  {saveStatus}
  {basePath}
  error={transitionError}
  onSelectNote={loadNote}
  onDeleteNote={handleDeletedNote}
  onSave={saveNow}
  onOpenSettings={openSettings}
/>
```

- settings:

```svelte
<SettingsWorkspace
  {config}
  onConfigChange={applyConfig}
  onSwitch={openBase}
  onBack={() => { screen = 'editor' }}
/>
```

На setup branch Sidebar/Editor физически не монтируются, а не скрываются CSS.

- [ ] **Step 5: Подтвердить GREEN состояния загрузки**

Run: `cd web && npm test -- src/App.test.js -t "blocks|loads editor|Повторить|setup"`

Expected: PASS для loading, retry, setup gating и editor.

- [ ] **Step 6: Зафиксировать App state machine**

```bash
git add web/src/App.svelte web/src/App.test.js
git commit -m "feat: gate editor behind setup state"
```

### Task 8: Реализовать debounce flush и безопасные переходы

**Files:**
- Create: `web/src/lib/app-transitions.js`
- Create: `web/src/lib/app-transitions.test.js`
- Modify: `web/src/App.svelte`
- Modify: `web/src/App.test.js`

- [ ] **Step 1: Написать RED unit-тесты порядка переходов**

Создать `app-transitions.test.js`:

```js
import { expect, test, vi } from 'vitest'
import { openSettingsSafely, switchBaseSafely } from './app-transitions.js'

test('opens settings only after flush', async () => {
  const order = []
  await openSettingsSafely({
    flush: vi.fn(async () => order.push('flush')),
    open: vi.fn(() => order.push('open')),
  })
  expect(order).toEqual(['flush', 'open'])
})

test('save error prevents settings', async () => {
  const open = vi.fn()
  await expect(openSettingsSafely({ flush: vi.fn().mockRejectedValue(new Error('save failed')), open }))
    .rejects.toThrow('save failed')
  expect(open).not.toHaveBeenCalled()
})

test('switch runs flush, request and commit in order', async () => {
  const order = []
  const config = { current_base: 'work' }
  await switchBaseSafely({
    name: 'work',
    flush: vi.fn(async () => order.push('flush')),
    switchRequest: vi.fn(async () => { order.push('switch'); return config }),
    commit: vi.fn((value) => { order.push('commit'); expect(value).toBe(config) }),
  })
  expect(order).toEqual(['flush', 'switch', 'commit'])
})

test.each(['flush', 'switch'])('%s error prevents commit', async (failedStep) => {
  const commit = vi.fn()
  await expect(switchBaseSafely({
    name: 'work',
    flush: failedStep === 'flush' ? vi.fn().mockRejectedValue(new Error('save failed')) : vi.fn(),
    switchRequest: failedStep === 'switch' ? vi.fn().mockRejectedValue(new Error('switch failed')) : vi.fn(),
    commit,
  })).rejects.toThrow()
  expect(commit).not.toHaveBeenCalled()
})
```

- [ ] **Step 2: Подтвердить RED и реализовать orchestration helpers**

Run: `cd web && npm test -- src/lib/app-transitions.test.js`

Expected: FAIL с отсутствующим модулем.

Создать:

```js
export async function openSettingsSafely({ flush, open }) {
  await flush()
  open()
}

export async function switchBaseSafely({ name, flush, switchRequest, commit }) {
  await flush()
  const config = await switchRequest(name)
  commit(config)
}
```

- [ ] **Step 3: Написать RED App tests сохранения и switch cleanup**

Через real Sidebar + `EditorStub`:

1. `getNotes` возвращает file node, `getNote` возвращает content; user меняет textarea и нажимает `Открыть настройки`; `saveNote(id,newContent)` вызывается до heading settings.
2. rejected `saveNote` оставляет editor, показывает `Не удалось сохранить заметку: ...`, settings heading отсутствует.
3. settings `Открыть` вызывает orchestration; successful `switchBase('work')` обновляет status path, закрывает settings, очищает active note/content и новый Sidebar вызывает `getNotes` заново.
4. rejected switch оставляет settings открытыми и показывает error в карточке.
5. edit path active базы через `onConfigChange` оставляет settings открытыми, обновляет `basePath` и очищает stale active note.

Для debounce использовать fake timers:

```js
vi.useFakeTimers()
await user.type(screen.getByLabelText('Markdown'), ' changed')
expect(saveNote).not.toHaveBeenCalled()
await vi.advanceTimersByTimeAsync(2000)
expect(saveNote).toHaveBeenCalledWith('note.md', '# Note changed')
vi.useRealTimers()
```

- [ ] **Step 4: Реализовать единый save lifecycle в `App.svelte`**

```js
let saveTimer = null
let statusTimer = null
let savePromise = null
let ignoreNextChange = false

async function persistCurrentNote() {
  if (!activeNote || !dirty) return
  const noteId = activeNote.id
  const content = markdownContent
  saveStatus = 'saving'
  savePromise = saveNote(noteId, content)
  try {
    await savePromise
    if (activeNote?.id === noteId && markdownContent === content) dirty = false
    saveStatus = 'saved'
    clearTimeout(statusTimer)
    statusTimer = setTimeout(() => { saveStatus = 'idle' }, 3000)
  } catch (error) {
    saveStatus = 'error'
    throw error
  } finally {
    savePromise = null
  }
}

async function flushPendingSave() {
  clearTimeout(saveTimer)
  saveTimer = null
  if (savePromise) await savePromise
  if (dirty) await persistCurrentNote()
}

async function saveNow() {
  if (!activeNote) return
  dirty = true
  transitionError = ''
  try {
    await flushPendingSave()
  } catch (error) {
    showSaveError(error)
  }
}

function showSaveError(error) {
  saveStatus = 'error'
  transitionError = `Не удалось сохранить заметку: ${error.message}`
}
```

`$effect` считывает `markdownContent`; после `loadNote` один раз пропускает change, иначе ставит `dirty=true`, очищает старый timer и вызывает `persistCurrentNote().catch(showSaveError)` через 2 секунды:

```js
$effect(() => {
  const note = activeNote
  const content = markdownContent
  if (ignoreNextChange) {
    ignoreNextChange = false
    return
  }
  if (!note) return
  dirty = true
  clearTimeout(saveTimer)
  saveTimer = setTimeout(() => {
    void persistCurrentNote().catch(showSaveError)
  }, 2000)
})

onDestroy(() => {
  clearTimeout(saveTimer)
  clearTimeout(statusTimer)
})
```

`loadNote(node)` вызывает `getNote(node.id)`, затем в одном synchronous block задаёт `activeNote = node`, `ignoreNextChange = true`, `markdownContent = data.content`, `dirty = false`, `saveStatus = 'idle'`, `transitionError = ''`. `handleDeletedNote(id)` вызывает `resetEditorState()` только при совпадении active id.

`openSettings`:

```js
async function openSettings() {
  transitionError = ''
  try {
    await openSettingsSafely({ flush: flushPendingSave, open: () => { screen = 'settings' } })
  } catch (error) {
    transitionError = `Не удалось сохранить заметку: ${error.message}`
  }
}
```

`openBase`:

```js
async function openBase(name) {
  await switchBaseSafely({
    name,
    flush: flushPendingSave,
    switchRequest: switchBase,
    commit: (savedConfig) => {
      config = savedConfig
      basePath = activeBase(savedConfig)?.path ?? ''
      resetEditorState()
      screen = 'editor'
    },
  })
}
```

`resetEditorState` и `applyConfig` имеют точную реализацию:

```js
function resetEditorState() {
  clearTimeout(saveTimer)
  activeNote = null
  ignoreNextChange = true
  markdownContent = ''
  dirty = false
  saveStatus = 'idle'
  transitionError = ''
}

function applyConfig(savedConfig) {
  const before = activeBase(config)
  const after = activeBase(savedConfig)
  if (before?.name !== after?.name || before?.path !== after?.path) {
    resetEditorState()
  }
  config = savedConfig
  basePath = after?.path ?? ''
}
```

Это покрывает rename/path edit активной базы без закрытия settings.

- [ ] **Step 5: Подтвердить GREEN целевых переходов**

Run: `cd web && npm test -- src/lib/app-transitions.test.js src/App.test.js`

Expected: PASS; mock call order доказывает `flush -> switch`, failed save/switch не меняет screen, successful switch очищает editor и reloads tree.

- [ ] **Step 6: Зафиксировать безопасные переходы**

```bash
git add web/src/lib/app-transitions.js web/src/lib/app-transitions.test.js web/src/App.svelte web/src/App.test.js
git commit -m "feat: flush notes before settings transitions"
```

### Task 9: Завершить responsive и accessibility поведение

**Files:**
- Modify: `web/src/lib/setup/SetupWizard.svelte`
- Modify: `web/src/lib/setup/BaseForm.svelte`
- Modify: `web/src/lib/setup/DirectoryField.svelte`
- Modify: `web/src/lib/settings/BaseCard.svelte`
- Modify: `web/src/lib/settings/SettingsWorkspace.svelte`
- Modify: `web/src/lib/NotesWorkspace.svelte`
- Modify: `web/src/lib/Modal.svelte`
- Modify: `web/src/app.css:1-12`
- Modify: `web/src/lib/setup/SetupWizard.test.js`
- Modify: `web/src/lib/settings/SettingsWorkspace.test.js`

- [ ] **Step 1: Добавить RED accessibility assertions**

Добавить Testing Library tests:

- wizard имеет один `main`, heading получает focus после next/back, progress текущего шага имеет `aria-current="step"`;
- все `Имя базы`/path controls доступны через `getByLabelText`;
- field/general errors имеют `role="alert"` или входят в `aria-describedby`;
- disabled Git tab имеет `aria-disabled="true"` и `tabindex="-1"`;
- current base card определяется не только цветом, но и текстом `Текущая`;
- forget dialog имеет `role="dialog"`, `aria-modal="true"`, Escape закрывает его;
- busy form/picker/card actions disabled и не вызываются повторно;
- icon-only editor/settings/sidebar buttons имеют accessible names.

Пример keyboard test:

```js
await user.click(screen.getByRole('button', { name: 'Забыть' }))
expect(screen.getByRole('dialog', { name: 'Забыть базу work?' })).toBeInTheDocument()
await user.keyboard('{Escape}')
expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
```

- [ ] **Step 2: Запустить tests и зафиксировать RED assertions**

Run: `cd web && npm test -- src/lib/setup/SetupWizard.test.js src/lib/settings/SettingsWorkspace.test.js`

Expected: FAIL только для ещё отсутствующих focus/ARIA/busy assertions; функциональные tests Task 4/6 остаются зелёными.

- [ ] **Step 3: Добавить точные responsive/focus стили**

Во всех интерактивных controls использовать `focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600`. В `web/src/app.css` добавить общий fallback:

```css
button:focus-visible,
input:focus-visible,
[tabindex]:focus-visible {
  outline: 2px solid #2563eb;
  outline-offset: 2px;
}

button:disabled,
input:disabled {
  cursor: not-allowed;
}
```

Responsive acceptance classes:

- wizard shell: one column below `lg`, two columns from `lg`;
- settings nav: `flex overflow-x-auto md:flex-col`, workspace grid starts at `md`;
- base cards: one column through `lg`, `xl:grid-cols-2`;
- form actions: `flex-col-reverse sm:flex-row sm:justify-end` so primary action remains first visually on mobile;
- editor Sidebar remains current desktop width; settings/wizard do not introduce horizontal page scroll at 320px.

- [ ] **Step 4: Реализовать focus management**

После wizard step change и settings panel change:

```js
import { tick } from 'svelte'

async function focusHeading() {
  await tick()
  headingElement?.focus()
}
```

Headings получают `tabindex="-1"`. После backend field error `BaseForm` фокусирует field; после general error фокусирует alert. Отмена forget dialog возвращает focus в кнопку `Забыть` через `forgetTrigger`; успешное забывание фокусирует heading списка, потому что исходная карточка уже удалена из DOM.

- [ ] **Step 5: Подтвердить GREEN и production build**

Run: `cd web && npm test`

Expected: все Vitest suites PASS без unhandled rejection и act warning.

Run: `cd web && npm run build`

Expected: Vite build завершается без Svelte accessibility warnings.

- [ ] **Step 6: Зафиксировать accessibility/responsive polish**

```bash
git add web/src/lib/setup web/src/lib/settings web/src/lib/NotesWorkspace.svelte web/src/lib/Modal.svelte web/src/app.css
git commit -m "fix: complete settings accessibility"
```

### Task 10: Синхронизировать пользовательскую и developer документацию

**Files:**
- Modify: `AGENTS.md`
- Modify: `docs/user.md`
- Modify: `docs/developer.md`
- Modify: `site/docs/user.md`
- Modify: `site/docs/developer.md`

- [ ] **Step 1: Обновить статус и API в `AGENTS.md`**

Заменить незавершённый пункт мастера на:

```markdown
- [x] При первом запуске — обязательный трёхшаговый мастер создания или подключения базы; до его завершения редактор недоступен
```

Добавить к текущему состоянию:

```markdown
- **Настройки баз**: Полноэкранный экран позволяет добавлять, подключать, переименовывать, забывать и переключать базы без перезапуска. Перед открытием настроек и переключением активная заметка сохраняется немедленно; забывание не удаляет каталог и файлы.
```

В API table добавить строки `POST /api/setup`, `POST/PUT/DELETE /api/bases`, `POST /api/bases/switch`, `POST /api/system/select-directory`; `GET/PUT /api/config` оставить одной строкой.

- [ ] **Step 2: Переписать первый запуск и интерфейс в `docs/user.md`**

Раздел `Настройка при первом запуске` должен сообщать:

```markdown
## Настройка при первом запуске

При новой или пустой конфигурации редактор заметок остаётся заблокированным, пока не завершён мастер:

1. Выберите «Создать новую» или «Подключить существующую».
2. Введите имя базы и каталог. Кнопка «Обзор» открывает системный выбор каталога; если он недоступен, путь можно ввести вручную.
3. Проверьте итоговый путь и завершите настройку.

Для новой базы выбранный каталог является родительским: база создаётся как `<каталог>/<имя>`. Для подключаемой базы укажите точный путь существующего каталога. Подключение не перемещает и не изменяет существующие файлы.
```

Добавить раздел:

```markdown
## Настройки баз

Кнопка с шестерёнкой в заголовке редактора открывает полноэкранные настройки. Здесь можно:

- добавить новую или подключить существующую базу;
- изменить отображаемое имя или путь базы без перемещения каталога;
- открыть другую базу без перезапуска приложения;
- забыть неактивную базу, если настроено больше одной базы.

Перед открытием настроек и переключением базы IGoNotes немедленно сохраняет изменения текущей заметки. Если сохранение завершилось ошибкой, переход отменяется. «Забыть» удаляет только запись из конфигурации: каталог и пользовательские файлы остаются на диске. Активную или последнюю базу забыть нельзя. Раздел Git отображается отключённым и будет реализован отдельно.
```

Убрать старое утверждение об автоматическом открытии `default` в editor.

- [ ] **Step 3: Обновить `docs/developer.md`**

В frontend section явно указать Svelte 5/Vite/Tailwind, screen state без router, `web/src/lib/api.js` и component tests. В API table добавить новые endpoints. Раздел тестирования заменить на:

````markdown
## Тестирование

Frontend unit/component tests используют Vitest 4, jsdom и Testing Library:

```bash
cd web
npm test
npm run test:watch
```

Полная проверка проекта:

```bash
go test -race ./...
go vet ./...
make all
```
````

- [ ] **Step 4: Синхронизировать опубликованные `site/docs`**

В `site/docs/user.md` повторить фактические разделы first-run/settings из `docs/user.md`, сохранив Jekyll frontmatter. В `site/docs/developer.md` повторить архитектуру state/API client и команды Vitest/full build, сохранив frontmatter. Не оставлять старые ссылки на `app.js`, `style.css`, Bootstrap, BoltDB или отсутствие Vite.

- [ ] **Step 5: Проверить документацию**

Run:

```bash
git grep -n -E 'автоматически.*default.*редактор|app\.js|style\.css|без Webpack/Vite|Планируется добавление.*тест' -- AGENTS.md docs site/docs
```

Expected: нет совпадений с устаревшими утверждениями.

Run: `git diff --check`

Expected: код 0 без whitespace errors.

- [ ] **Step 6: Зафиксировать документацию**

```bash
git add AGENTS.md docs/user.md docs/developer.md site/docs/user.md site/docs/developer.md
git commit -m "docs: describe setup and base settings"
```

### Task 11: Выполнить полную автоматическую и ручную проверку

**Files:**
- Verify: `web/package.json`
- Verify: `web/vite.config.js`
- Verify: `web/src/App.svelte`
- Verify: `web/src/lib/api.js`
- Verify: `web/src/lib/setup/*.svelte`
- Verify: `web/src/lib/settings/*.svelte`
- Verify: `web/src/**/*.test.js`
- Verify: `AGENTS.md`
- Verify: `docs/user.md`
- Verify: `docs/developer.md`
- Verify: `site/docs/user.md`
- Verify: `site/docs/developer.md`

- [ ] **Step 1: Запустить frontend tests одним process**

Run: `cd web && npm test`

Expected: все unit/component tests PASS; process завершается с кодом 0, нет unhandled errors и зависших fake timers.

- [ ] **Step 2: Запустить backend race tests и vet**

Run: `go test -race ./...`

Expected: PASS во всех Go packages без race report.

Run: `go vet ./...`

Expected: код 0 без diagnostics.

- [ ] **Step 3: Выполнить полную embedded build**

Run: `make all`

Expected: npm install, Vite build и `go build -o builds/igonotes ./cmd/...` завершаются успешно; `web/dist` и `builds/igonotes` созданы.

- [ ] **Step 4: Smoke-test первого запуска в изоляции**

Run:

```bash
tmp_home=$(mktemp -d) && HOME="$tmp_home" XDG_CONFIG_HOME="$tmp_home/config" ./builds/igonotes --no-browser --port 18080
```

Expected: приложение запускается на `http://localhost:18080`; browser вручную показывает wizard, а `/api/notes` до setup возвращает `428 setup_required`. Завершить create flow с существующим parent directory внутри `$tmp_home`, убедиться, что editor открылся и status bar показывает выбранную базу. Остановить процесс `Ctrl+C` и удалить временный каталог после проверки.

- [ ] **Step 5: Проверить desktop и narrow viewport**

В browser DevTools проверить 1440x900 и 320x800:

- wizard rail слева на desktop и сверху на narrow;
- settings nav слева на desktop и tabs сверху на narrow;
- формы и cards в одну колонку на narrow;
- отсутствует горизонтальный page scroll;
- Tab/Shift+Tab проходят controls в визуальном порядке;
- focus ring виден, errors связаны с fields, Escape закрывает forget dialog.

Expected: все пункты выполняются на обоих viewport.

- [ ] **Step 6: Проверить picker и безопасные переходы**

На системе с picker выбрать каталог и отменить dialog: выбранный path заполняется, отмена не показывает error. Для точной Linux-проверки unavailable остановить первый process и запустить отдельный instance без доступных picker executables:

```bash
picker_home=$(mktemp -d) && PATH=/nonexistent HOME="$picker_home" XDG_CONFIG_HOME="$picker_home/config" ./builds/igonotes --no-browser --port 18081
```

Открыть `http://localhost:18081`, на втором шаге wizard нажать `Обзор`: UI показывает ручной fallback после `501`, path input остаётся enabled. Остановить process и удалить `$picker_home`.

Создать заметку, изменить текст и сразу открыть settings: network показывает `POST /api/save` до settings. Изменить текст и инициировать switch в сценарии с pending save: `POST /api/bases/switch` отправляется только после успешного save. Имитировать failed save: settings/switch не выполняется, пользователь видит error и введённый markdown остаётся в editor state.

- [ ] **Step 7: Проверить итоговый diff и scope**

Run: `git diff --check`

Expected: код 0 без вывода.

Run:

```bash
git status --short
```

Expected: только файлы из карты этого плана; spec и другие plans не изменены.

Run:

```bash
git diff --name-only -- docs/superpowers/specs docs/superpowers/plans ':!docs/superpowers/plans/2026-08-27-setup-settings-frontend.md'
```

Expected: нет вывода.

## Матрица покрытия спецификации

| Требование | Реализация | Проверка |
|:---|:---|:---|
| Loading/error/retry и setup gating | Task 7 | `App.test.js` |
| Три шага, create/connect, layout C | Task 4 | `SetupWizard.test.js` |
| Structured errors и 204/501 picker | Tasks 2-3 | `api.test.js`, `DirectoryField.test.js` |
| Full-screen settings без router | Tasks 6-7 | `SettingsWorkspace.test.js`, `App.test.js` |
| Cards, add/edit/forget/switch | Task 6 | `SettingsWorkspace.test.js` |
| Disabled Git | Tasks 4, 6 | component tests |
| Debounce flush перед settings/switch | Task 8 | `app-transitions.test.js`, `App.test.js` |
| Очистка editor и reload tree после switch | Task 8 | `App.test.js` |
| Responsive и keyboard/a11y | Task 9 | assertions и manual viewport check |
| Frontend tests и production build | Tasks 1, 9, 11 | `npm test`, `npm run build`, `make all` |
| User/developer/published docs | Task 10 | grep и `git diff --check` |

## Критерий завершения

План считается выполненным, когда новый config всегда открывает обязательный wizard без Sidebar/Editor, завершённый config открывает editor, настройки управляют базами в текущей вкладке, failed save блокирует settings/switch, successful switch очищает stale editor state, picker корректно различает select/cancel/unavailable, все автоматические команды Task 11 проходят и ручные desktop/narrow проверки не выявляют accessibility или overflow regressions.
