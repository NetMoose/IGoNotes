<script>
  import { onDestroy } from 'svelte'

  import { normalizeBaseDraft, validateBaseDraft } from '../base-draft.js'
  import DirectoryField from './DirectoryField.svelte'

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
  } = $props()

  function getInitialMode() {
    return ['create', 'connect', 'edit'].includes(mode) ? mode : 'create'
  }

  function getInitialName() {
    return initialName
  }

  function getInitialPath() {
    return initialPath
  }

  function getInitialModeProp() {
    return mode
  }

  let selectedMode = $state(getInitialMode())
  let name = $state(getInitialName())
  let path = $state(getInitialPath())
  let clientErrors = $state({})
  let pickerNotice = $state('')
  let generalError = $state('')
  let previousApiError
  let backendField = ''
  let backendMessage = ''
  let previousMode = getInitialModeProp()
  let previousPath = getInitialPath()
  let formElement
  let focusHandle
  let focusToken = 0
  let active = true

  function cancelScheduledFocus() {
    focusToken += 1
    if (focusHandle === undefined) return
    if (typeof cancelAnimationFrame === 'function') {
      cancelAnimationFrame(focusHandle)
    } else {
      clearTimeout(focusHandle)
    }
    focusHandle = undefined
  }

  function scheduleFocus(field) {
    cancelScheduledFocus()
    const token = focusToken
    const focus = () => {
      focusHandle = undefined
      if (!active || token !== focusToken || !formElement) return
      const fieldId = `${formId}-${field}`
      const target = Array.from(formElement.elements).find((element) => element.id === fieldId)
      target?.focus()
    }
    if (typeof requestAnimationFrame === 'function') {
      focusHandle = requestAnimationFrame(focus)
    } else {
      focusHandle = setTimeout(focus, 0)
    }
  }

  onDestroy(() => {
    active = false
    cancelScheduledFocus()
  })

  function clearBackendFieldError() {
    if (!backendField || clientErrors[backendField] !== backendMessage) return
    const nextErrors = { ...clientErrors }
    delete nextErrors[backendField]
    clientErrors = nextErrors
    backendField = ''
    backendMessage = ''
  }

  $effect(() => {
    const nextApiError = apiError
    if (nextApiError === previousApiError) return

    previousApiError = nextApiError
    clearBackendFieldError()
    generalError = ''

    if (!nextApiError) return
    if (nextApiError.field === 'name' || nextApiError.field === 'path') {
      backendField = nextApiError.field
      backendMessage = nextApiError.message
      clientErrors = { ...clientErrors, [backendField]: backendMessage }
      scheduleFocus(backendField)
    } else {
      generalError = nextApiError.message
    }
  })

  $effect(() => {
    const nextMode = mode
    if (nextMode === previousMode) return
    previousMode = nextMode
    if (['create', 'connect', 'edit'].includes(nextMode)) {
      selectedMode = nextMode
    }
  })

  $effect(() => {
    const nextPath = path
    if (nextPath === previousPath) return
    previousPath = nextPath
    clearFieldError('path')
  })

  function clearFieldError(field) {
    if (!clientErrors[field]) return
    const nextErrors = { ...clientErrors }
    delete nextErrors[field]
    clientErrors = nextErrors
    if (backendField === field) {
      backendField = ''
      backendMessage = ''
    }
  }

  function submit(event) {
    event.preventDefault()
    if (busy) return

    const normalized = normalizeBaseDraft({ mode: selectedMode, name, path })
    const errors = validateBaseDraft(normalized, { existingNames, originalName })
    backendField = ''
    backendMessage = ''
    clientErrors = errors
    generalError = ''

    if (errors.name || errors.path) {
      scheduleFocus(errors.name ? 'name' : 'path')
      return
    }

    name = normalized.name
    path = normalized.path
    onSubmit(
      selectedMode === 'edit'
        ? { name: normalized.name, path: normalized.path }
        : normalized,
    )
  }
</script>

<form bind:this={formElement} id={formId} class="space-y-6" onsubmit={submit} novalidate>
  {#if showMode}
    <fieldset class="space-y-3" disabled={busy}>
      <legend class="text-sm font-semibold text-slate-900">Способ добавления базы</legend>
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="flex cursor-pointer items-center gap-3 rounded-xl border border-slate-200 p-4 text-sm font-semibold text-slate-800 transition has-checked:border-blue-600 has-checked:bg-blue-50 focus-within:ring-2 focus-within:ring-blue-500 focus-within:ring-offset-2 has-disabled:cursor-not-allowed has-disabled:opacity-60">
          <input
            type="radio"
            name={`${formId}-mode`}
            value="create"
            bind:group={selectedMode}
            disabled={busy}
            class="size-4 border-slate-300 text-blue-600 focus-visible:ring-blue-500"
          />
          Создать новую
        </label>
        <label class="flex cursor-pointer items-center gap-3 rounded-xl border border-slate-200 p-4 text-sm font-semibold text-slate-800 transition has-checked:border-blue-600 has-checked:bg-blue-50 focus-within:ring-2 focus-within:ring-blue-500 focus-within:ring-offset-2 has-disabled:cursor-not-allowed has-disabled:opacity-60">
          <input
            type="radio"
            name={`${formId}-mode`}
            value="connect"
            bind:group={selectedMode}
            disabled={busy}
            class="size-4 border-slate-300 text-blue-600 focus-visible:ring-blue-500"
          />
          Подключить существующую
        </label>
      </div>
    </fieldset>
  {/if}

  <div class="space-y-2">
    <label for={`${formId}-name`} class="block text-sm font-medium text-slate-700">Имя базы</label>
    <input
      id={`${formId}-name`}
      type="text"
      bind:value={name}
      oninput={() => clearFieldError('name')}
      disabled={busy}
      aria-invalid={clientErrors.name ? 'true' : undefined}
      aria-describedby={clientErrors.name ? `${formId}-name-error` : undefined}
      class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-slate-900 shadow-sm outline-none transition placeholder:text-slate-400 focus-visible:border-blue-500 focus-visible:ring-2 focus-visible:ring-blue-500/30 disabled:cursor-not-allowed disabled:bg-slate-100 disabled:text-slate-500"
    />
    {#if clientErrors.name}
      <p id={`${formId}-name-error`} class="text-sm text-red-600">{clientErrors.name}</p>
    {/if}
  </div>

  <DirectoryField
    id={`${formId}-path`}
    label={selectedMode === 'create' ? 'Родительский каталог' : 'Каталог существующей базы'}
    bind:value={path}
    hint={pickerNotice}
    error={clientErrors.path || ''}
    disabled={busy}
    onPickerNotice={(notice) => pickerNotice = notice}
  />

  <div class="space-y-4 rounded-xl border border-slate-200 bg-slate-50 p-4">
    <div class="space-y-2">
      <div class="flex items-center justify-between gap-3">
        <label for={`${formId}-git-url`} class="text-sm font-medium text-slate-700">Git URL</label>
        <span class="rounded-full bg-slate-200 px-2 py-0.5 text-xs font-semibold text-slate-600">Git, скоро</span>
      </div>
      <input
        id={`${formId}-git-url`}
        type="url"
        disabled
        class="w-full cursor-not-allowed rounded-lg border border-slate-200 bg-slate-100 px-3 py-2 text-slate-500"
      />
    </div>
    <label class="flex cursor-not-allowed items-start gap-3 text-sm text-slate-500">
      <input type="checkbox" disabled class="mt-0.5 size-4 rounded border-slate-300" />
      <span>Автосинхронизация будет доступна позже</span>
    </label>
  </div>

  {#if generalError}
    <div role="alert" class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
      {generalError}
    </div>
  {/if}

  <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
    {#if onCancel !== null}
      <button
        type="button"
        onclick={onCancel}
        disabled={busy}
        class="rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
      >
        Отмена
      </button>
    {/if}
    <button
      type="submit"
      disabled={busy}
      aria-busy={busy ? 'true' : 'false'}
      class="rounded-lg bg-blue-600 px-5 py-2.5 text-sm font-semibold text-white shadow-sm transition hover:bg-blue-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
    >
      {submitLabel}
    </button>
  </div>
</form>
