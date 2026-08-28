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
      class="min-w-0 flex-1 rounded-lg border border-slate-300 bg-white px-3 py-2 text-slate-900 shadow-sm outline-none transition placeholder:text-slate-400 focus-visible:border-blue-500 focus-visible:ring-2 focus-visible:ring-blue-500/30 disabled:cursor-not-allowed disabled:bg-slate-100 disabled:text-slate-500"
    />
    <button
      type="button"
      onclick={browse}
      disabled={disabled || picking}
      class="shrink-0 rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white shadow-sm transition hover:bg-blue-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
    >
      Обзор
    </button>
  </div>
  {#if picking}
    <p role="status" class="text-sm text-slate-500">Выбор каталога...</p>
  {/if}
  {#if hint}
    <p id={`${id}-hint`} class="text-sm text-slate-500">{hint}</p>
  {/if}
  {#if error}
    <p id={`${id}-error`} class="text-sm text-red-600">{error}</p>
  {/if}
</div>
