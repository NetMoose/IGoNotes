<script>
  import { onDestroy } from 'svelte'

  import { ApiError, selectDirectory } from '../api.js'

  let {
    id,
    label = 'Каталог',
    value = $bindable(''),
    hint = '',
    error = '',
    disabled = false,
    onPickerNotice = () => {},
  } = $props()

  let picking = $state(false)
  let requestToken = 0
  let contextInitialized = false
  let contextId
  let contextLabel
  let contextValue

  $effect(() => {
    const nextId = id
    const nextLabel = label
    const nextValue = value
    if (!contextInitialized) {
      contextInitialized = true
      contextId = nextId
      contextLabel = nextLabel
      contextValue = nextValue
      return
    }
    if (
      nextId !== contextId
      || nextLabel !== contextLabel
      || nextValue !== contextValue
    ) {
      contextId = nextId
      contextLabel = nextLabel
      contextValue = nextValue
      requestToken += 1
      picking = false
    }
  })

  onDestroy(() => {
    requestToken += 1
  })

  async function browse() {
    const token = ++requestToken
    picking = true

    try {
      onPickerNotice('')

      let selected
      try {
        selected = await selectDirectory()
      } catch (pickerError) {
        if (token !== requestToken) return

        if (
          pickerError instanceof ApiError
          && pickerError.status === 501
          && pickerError.code === 'directory_picker_unavailable'
        ) {
          onPickerNotice('Системный выбор каталога недоступен. Введите путь вручную.')
        } else {
          onPickerNotice(
            pickerError instanceof Error && pickerError.message
              ? pickerError.message
              : 'Не удалось выбрать каталог',
          )
        }
        return
      }

      if (token === requestToken && selected) {
        value = selected
      }
    } finally {
      if (token === requestToken) {
        picking = false
      }
    }
  }
</script>

<div class="space-y-2">
  <label for={id} class="block text-sm font-medium text-slate-700">{label}</label>
  <div class="flex flex-col gap-2 sm:flex-row">
    <input
      {id}
      type="text"
      bind:value
      {disabled}
      aria-invalid={error ? 'true' : undefined}
      aria-describedby={[
        hint ? `${id}-hint` : '',
        error ? `${id}-error` : '',
      ].filter(Boolean).join(' ') || undefined}
      class="min-w-0 flex-1 rounded-lg border border-slate-300 bg-white px-3 py-2 text-slate-900 shadow-sm transition placeholder:text-slate-400 focus-visible:border-blue-500 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:bg-slate-100 disabled:text-slate-500"
    />
    <button
      type="button"
      onclick={browse}
      disabled={disabled || picking}
      class="shrink-0 rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white shadow-sm transition hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
    >
      Обзор
    </button>
  </div>
  {#if picking}
    <p role="status" class="break-words text-sm text-slate-500">Выбор каталога...</p>
  {/if}
  {#if hint}
    <p id={`${id}-hint`} class="break-words text-sm text-slate-500">{hint}</p>
  {/if}
  {#if error}
    <p id={`${id}-error`} class="break-words text-sm text-red-600">{error}</p>
  {/if}
</div>
