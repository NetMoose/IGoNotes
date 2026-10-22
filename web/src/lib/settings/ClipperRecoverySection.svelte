<script>
  import { onMount, onDestroy, tick } from 'svelte'
  import { getClipperImports, abandonClipperImport } from '../api.js'
  import Modal from '../Modal.svelte'

  let imports = $state([])
  let loading = $state(true)
  let loadError = $state('')
  let actionError = $state('')
  let selected = $state(null)
  let busy = $state(false)
  let notice = $state('')
  let recoveryHeading = $state()
  // A confirmed terminal receipt outranks every later unfinished-list snapshot.
  const abandonedIDs = new Set()
  let active = false
  const buttonClass = 'rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-50'
  onMount(() => { active = true; loadImports() })
  onDestroy(() => { active = false })

  function message(error, fallback) { return error instanceof Error && error.message ? error.message : fallback }
  async function loadImports() {
    if (!active || busy) return
    loading = true
    loadError = ''
    try {
      const result = await getClipperImports()
      if (active) imports = result.imports.filter((item) => !abandonedIDs.has(item.import_id))
    } catch (error) {
      if (active) loadError = message(error, 'Не удалось загрузить импорты')
    } finally {
      if (active) loading = false
    }
  }
  function choose(item) {
    if (busy || loading || selected || abandonedIDs.has(item.import_id)) return
    actionError = ''
    selected = item
  }
  function cancel() { if (!busy) { selected = null; actionError = '' } }
  async function abandon() {
    if (!active || busy || !selected) return
    busy = true
    actionError = ''
    try {
      await abandonClipperImport(selected.import_id)
      if (!active) return
      abandonedIDs.add(selected.import_id)
      imports = imports.filter((item) => !abandonedIDs.has(item.import_id))
      notice = `Попытка прекращена: ${selected.import_id}. Файлы могут уже существовать; операция их не удаляет. Старый ID не будет опубликован повторно.`
      selected = null
    } catch (error) {
      if (active) actionError = `${message(error, 'Не удалось прекратить попытку')}. Проверьте список вручную: результат запроса может быть неизвестен.`
      return
    } finally {
      if (active) busy = false
    }
    if (!active) return
    const refresh = loadImports()
    // DialogShell first releases inert backgrounds/restores the old trigger.
    // Success may remove that trigger, so use a stable target after DOM cleanup.
    await tick()
    if (active && recoveryHeading?.isConnected) recoveryHeading.focus()
    await refresh
  }
</script>

<section aria-label="Восстановление импортов" class="mt-8 border-t border-slate-200 pt-8">
  <div class="flex items-center justify-between gap-4">
    <h2 bind:this={recoveryHeading} tabindex="-1" class="text-xl font-bold text-slate-950 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600">Незавершённые импорты</h2>
    <button type="button" onclick={loadImports} disabled={loading || busy || Boolean(selected)} class={buttonClass}>Обновить импорты</button>
  </div>
  <p class="mt-2 text-sm text-slate-600">База и относительный путь относятся к исходной попытке. Сначала проверьте и сохраните нужные файлы самостоятельно. Файлы могут уже существовать.</p>
  {#if notice}<p role="status" class="mt-4 text-sm text-slate-700">{notice}</p>{/if}
  {#if loading}<p role="status" class="mt-4 text-sm text-slate-600">Загрузка незавершённых импортов…</p>{/if}
  {#if loadError}
    <p role="alert" class="mt-4 text-sm text-red-700">{loadError}</p>
    <button type="button" onclick={loadImports} disabled={loading || busy} class={`mt-3 ${buttonClass}`}>Повторить загрузку импортов</button>
  {:else if !loading && imports.length === 0}
    <p class="mt-4 text-slate-600">Незавершённых импортов нет</p>
  {/if}
  <div class="mt-4 grid gap-4">
    {#each imports as item (item.import_id)}
      <article aria-label={`Импорт ${item.import_id}`} class="rounded-2xl border border-slate-200 bg-white p-6">
        <dl class="break-all text-sm text-slate-700">
          <dt>ID попытки</dt><dd>{item.import_id}</dd>
          <dt class="mt-2">База</dt><dd>{item.base}</dd>
          <dt class="mt-2">Относительный путь</dt><dd>{item.path}</dd>
          <dt class="mt-2">Статус</dt><dd>{item.state === 'pending' ? 'Выполняется' : 'Требуется ручное восстановление'}</dd>
        </dl>
        {#if item.error}<p class="mt-2 text-sm text-red-700">{item.error.message}</p>{/if}
        {#if item.state === 'recovery_required'}
          <button type="button" onclick={() => choose(item)} disabled={loading || busy || Boolean(selected)} class={`mt-4 ${buttonClass}`}>Прекратить попытку</button>
        {/if}
      </article>
    {/each}
  </div>
</section>

<Modal show={Boolean(selected)} title="Прекратить попытку импорта?"
  description={selected ? `ID: ${selected.import_id}. База: ${selected.base}. Путь: ${selected.path}. Файлы могут уже существовать. Операция не удаляет файлы, а окончательно отклоняет попытку. Прежний ID больше не публикуется. Подтвердите после самостоятельного разбора и сохранения файлов.` : ''}
  onConfirm={abandon} onCancel={cancel} confirmText="Подтвердить прекращение" error={actionError} {busy} danger />
