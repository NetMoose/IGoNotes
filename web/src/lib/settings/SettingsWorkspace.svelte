<script>
  import { onDestroy, tick } from 'svelte'

  import Modal from '../Modal.svelte'
  import { createBase, forgetBase, updateBase } from '../api.js'
  import BaseForm from '../setup/BaseForm.svelte'
  import BaseCard from './BaseCard.svelte'

  let { config, onConfigChange, onSwitch, onBack } = $props()

  let panel = $state('list')
  let editingBase = $state(null)
  let pendingForget = $state(null)
  let forgetTrigger = $state(null)
  let busyAction = $state('')
  let operationErrors = $state({})
  let formError = $state(null)
  let listHeading = $state()
  let active = true

  let bases = $derived(Array.isArray(config?.bases) ? config.bases : [])
  let existingNames = $derived(bases.map((base) => base.name))

  onDestroy(() => {
    active = false
  })

  function errorMessage(error, fallback) {
    return error instanceof Error && error.message ? error.message : fallback
  }

  function clearOperationError(name) {
    if (!operationErrors[name]) return
    operationErrors = { ...operationErrors, [name]: '' }
  }

  function showAdd() {
    if (busyAction !== '') return
    editingBase = null
    formError = null
    panel = 'add'
  }

  function showEdit(base) {
    if (busyAction !== '') return
    editingBase = base
    formError = null
    clearOperationError(base.name)
    panel = 'edit'
  }

  function showList() {
    if (busyAction !== '') return
    editingBase = null
    formError = null
    panel = 'list'
  }

  async function addBase(draft) {
    if (!active || busyAction !== '') return
    busyAction = 'add'
    formError = null

    let savedConfig
    try {
      savedConfig = await createBase(draft)
    } catch (error) {
      if (!active) return
      busyAction = ''
      formError = error
      return
    }

    if (!active) return
    busyAction = ''
    panel = 'list'
    onConfigChange(savedConfig)
  }

  async function editBase(draft) {
    if (!active || busyAction !== '' || !editingBase) return
    const originalName = editingBase.name
    busyAction = `edit:${originalName}`
    formError = null

    let savedConfig
    try {
      savedConfig = await updateBase(originalName, draft)
    } catch (error) {
      if (!active) return
      busyAction = ''
      formError = error
      return
    }

    if (!active) return
    busyAction = ''
    panel = 'list'
    editingBase = null
    onConfigChange(savedConfig)
  }

  async function openBase(base) {
    if (!active || busyAction !== '') return
    busyAction = `switch:${base.name}`
    clearOperationError(base.name)

    try {
      await onSwitch(base.name)
    } catch (error) {
      if (!active) return
      busyAction = ''
      operationErrors = {
        ...operationErrors,
        [base.name]: errorMessage(error, 'Не удалось открыть базу'),
      }
      return
    }

    if (active) busyAction = ''
  }

  function askForget(base, trigger) {
    if (busyAction !== '') return
    clearOperationError(base.name)
    forgetTrigger = trigger
    pendingForget = base
  }

  async function restoreForgetFocus(trigger) {
    await tick()
    if (active && trigger?.isConnected) trigger.focus()
  }

  async function cancelForget() {
    if (busyAction !== '') return
    const trigger = forgetTrigger
    pendingForget = null
    forgetTrigger = null
    await restoreForgetFocus(trigger)
  }

  async function confirmForget() {
    if (!active || busyAction !== '' || !pendingForget) return
    const base = pendingForget
    const trigger = forgetTrigger
    busyAction = `forget:${base.name}`

    let savedConfig
    try {
      savedConfig = await forgetBase(base.name)
    } catch (error) {
      if (!active) return
      busyAction = ''
      pendingForget = null
      forgetTrigger = null
      operationErrors = {
        ...operationErrors,
        [base.name]: errorMessage(error, 'Не удалось забыть базу'),
      }
      await restoreForgetFocus(trigger)
      return
    }

    if (!active) return
    busyAction = ''
    pendingForget = null
    forgetTrigger = null
    await tick()
    if (!active) return
    listHeading?.focus()
    onConfigChange(savedConfig)
  }
</script>

<div class="min-h-screen bg-slate-100">
  <header class="border-b border-slate-200 bg-white">
    <div class="mx-auto flex max-w-7xl items-center justify-between gap-4 px-4 py-4 sm:px-6 lg:px-8">
      <p class="text-xl font-bold tracking-tight text-slate-950">IGoNotes</p>
      <button
        type="button"
        onclick={onBack}
        disabled={busyAction !== ''}
        class="rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
      >
        Назад к заметкам
      </button>
    </div>
  </header>

  <main class="mx-auto grid max-w-7xl md:grid-cols-[15rem_minmax(0,1fr)]">
    <nav class="border-b border-slate-200 bg-white p-4 md:min-h-[calc(100vh-73px)] md:border-b-0 md:border-r md:p-6" aria-label="Настройки">
      <div role="tablist" aria-label="Разделы настроек" class="grid grid-cols-2 gap-2 md:sticky md:top-6 md:grid-cols-1">
        <button
          type="button"
          role="tab"
          aria-selected="true"
          aria-current="page"
          class="rounded-lg bg-blue-50 px-3 py-2 text-left text-sm font-semibold text-blue-700"
        >
          Базы
        </button>
        <button
          type="button"
          role="tab"
          aria-selected="false"
          aria-disabled="true"
          tabindex="-1"
          disabled
          class="cursor-not-allowed rounded-lg px-3 py-2 text-left text-sm font-semibold text-slate-400"
        >
          Git
        </button>
      </div>
    </nav>

    <section class="min-w-0 p-4 sm:p-6 lg:p-8">
      {#if panel === 'list'}
        <div class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
          <div>
            <h1
              bind:this={listHeading}
              tabindex="-1"
              class="text-3xl font-bold tracking-tight text-slate-950 outline-none"
            >
              Базы заметок
            </h1>
            <p class="mt-2 text-slate-600">Управляйте каталогами, в которых хранятся ваши заметки.</p>
          </div>
          <button
            type="button"
            onclick={showAdd}
            disabled={busyAction !== ''}
            class="shrink-0 rounded-lg bg-blue-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm transition hover:bg-blue-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Добавить базу
          </button>
        </div>

        <div class="mt-8 grid gap-4 xl:grid-cols-2">
          {#each bases as base (base.name)}
            <BaseCard
              {base}
              current={base.name === config.current_base}
              canForget={bases.length > 1}
              {busyAction}
              error={operationErrors[base.name] || ''}
              onOpen={openBase}
              onEdit={showEdit}
              onForget={askForget}
            />
          {/each}
        </div>
      {:else if panel === 'add'}
        <div class="w-full max-w-2xl">
          <h1 class="text-3xl font-bold tracking-tight text-slate-950">Добавить базу</h1>
          <p class="mb-8 mt-2 text-slate-600">Создайте новый каталог или подключите существующий.</p>
          <BaseForm
            formId="settings-add-base"
            mode="create"
            {existingNames}
            submitLabel="Добавить"
            busy={busyAction !== ''}
            apiError={formError}
            showMode={true}
            onSubmit={addBase}
            onCancel={showList}
          />
        </div>
      {:else if editingBase}
        <div class="w-full max-w-2xl">
          <h1 class="text-3xl font-bold tracking-tight text-slate-950">Изменить базу</h1>
          <p class="mb-8 mt-2 text-slate-600">Обновите имя или каталог базы заметок.</p>
          <BaseForm
            formId="settings-edit-base"
            mode="edit"
            initialName={editingBase.name}
            initialPath={editingBase.path}
            {existingNames}
            originalName={editingBase.name}
            submitLabel="Сохранить"
            busy={busyAction !== ''}
            apiError={formError}
            onSubmit={editBase}
            onCancel={showList}
          />
        </div>
      {/if}
    </section>
  </main>

  <Modal
    show={pendingForget !== null}
    title="Забыть базу?"
    description="Каталог и файлы останутся на диске"
    confirmText="Забыть базу"
    danger={true}
    busy={busyAction.startsWith('forget:')}
    onConfirm={confirmForget}
    onCancel={cancelForget}
  />
</div>
