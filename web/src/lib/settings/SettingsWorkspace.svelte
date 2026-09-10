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
    onGitSync = () => {},
    onGitRefresh = () => {},
  } = $props()

  let selectedTab = $state('bases')
  let isDesktop = $state(false)
  let panel = $state('list')
  let editingBase = $state(null)
  let pendingForget = $state(null)
  let forgetTrigger = $state(null)
  let busyAction = $state('')
  let operationErrors = $state({})
  let formError = $state(null)
  let workspaceError = $state('')
  let gitSectionBusy = $state(false)
  let listHeading = $state()
  let panelHeading = $state()
  let active = true

  let bases = $derived(Array.isArray(config?.bases) ? config.bases : [])
  let existingNames = $derived(bases.map((base) => base.name))
  let workspaceBusy = $derived(busyAction !== '' || Boolean(gitBusyBase) || gitSectionBusy)
  let baseBusyAction = $derived(workspaceBusy ? busyAction || `git:${gitBusyBase || 'section'}` : '')

  onDestroy(() => {
    active = false
  })

  onMount(() => {
    if (typeof window.matchMedia !== 'function') return
    const media = window.matchMedia('(min-width: 768px)')
    const updateOrientation = (event) => isDesktop = event.matches
    updateOrientation(media)
    media.addEventListener('change', updateOrientation)
    return () => media.removeEventListener('change', updateOrientation)
  })

  function errorMessage(error, fallback) {
    return error instanceof Error && error.message ? error.message : fallback
  }

  function clearOperationError(name) {
    if (!operationErrors[name]) return
    operationErrors = { ...operationErrors, [name]: '' }
  }

  function selectTab(tab) {
    if (workspaceBusy) return
    selectedTab = tab
  }

  async function navigateTabs(event, tab) {
    if (workspaceBusy) return
    const tabs = ['bases', 'git']
    const current = tabs.indexOf(tab)
    let next = -1
    if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = tabs.length - 1
    else if ((isDesktop && event.key === 'ArrowUp') || (!isDesktop && event.key === 'ArrowLeft')) {
      next = (current + tabs.length - 1) % tabs.length
    } else if ((isDesktop && event.key === 'ArrowDown') || (!isDesktop && event.key === 'ArrowRight')) {
      next = (current + 1) % tabs.length
    } else return

    event.preventDefault()
    selectedTab = tabs[next]
    await tick()
    document.getElementById(`settings-${selectedTab}-tab`)?.focus()
  }

  async function transitionPanel(nextPanel, base = null) {
    if (!active) return
    panel = nextPanel
    editingBase = base
    formError = null
    await tick()
    if (!active || panel !== nextPanel) return
    const heading = nextPanel === 'list' ? listHeading : panelHeading
    heading?.focus()
  }

  async function showAdd() {
    if (workspaceBusy) return
    workspaceError = ''
    await transitionPanel('add')
  }

  async function showEdit(base) {
    if (workspaceBusy) return
    workspaceError = ''
    clearOperationError(base.name)
    await transitionPanel('edit', base)
  }

  async function showList() {
    if (workspaceBusy) return
    await transitionPanel('list')
  }

  async function applyConfig(savedConfig) {
    try {
      await onConfigChange(savedConfig)
    } catch (error) {
      if (active) {
        workspaceError = errorMessage(error, 'Не удалось применить конфигурацию')
      }
      return false
    }
    return true
  }

  async function addBase(draft) {
    if (!active || workspaceBusy) return
    busyAction = 'add'
    formError = null
    workspaceError = ''

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
    await applyConfig(savedConfig)
    if (!active) return
    busyAction = ''
    await transitionPanel('list')
  }

  async function editBase(draft) {
    if (!active || workspaceBusy || !editingBase) return
    const originalName = editingBase.name
    busyAction = `edit:${originalName}`
    formError = null
    workspaceError = ''

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
    await applyConfig(savedConfig)
    if (!active) return
    busyAction = ''
    await transitionPanel('list')
  }

  async function openBase(name) {
    if (!active || workspaceBusy) return
    busyAction = `switch:${name}`
    workspaceError = ''
    clearOperationError(name)

    try {
      await onSwitch(name)
    } catch (error) {
      if (!active) return
      busyAction = ''
      operationErrors = {
        ...operationErrors,
        [name]: errorMessage(error, 'Не удалось открыть базу'),
      }
      return
    }

    if (active) busyAction = ''
  }

  function askForget(base, trigger) {
    if (workspaceBusy) return
    workspaceError = ''
    clearOperationError(base.name)
    forgetTrigger = trigger
    pendingForget = base
  }

  async function restoreForgetFocus(trigger) {
    await tick()
    if (!active) return
    if (trigger?.isConnected) trigger.focus()
    else listHeading?.focus()
  }

  async function cancelForget() {
    if (workspaceBusy) return
    const trigger = forgetTrigger
    pendingForget = null
    forgetTrigger = null
    await restoreForgetFocus(trigger)
  }

  async function confirmForget() {
    if (!active || workspaceBusy || !pendingForget) return
    const base = pendingForget
    const trigger = forgetTrigger
    const latestBase = bases.find((candidate) => candidate.name === base.name)
    if (!latestBase || latestBase.name === config.current_base || bases.length <= 1) {
      pendingForget = null
      forgetTrigger = null
      await restoreForgetFocus(trigger)
      return
    }

    busyAction = `forget:${base.name}`
    workspaceError = ''

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
    await applyConfig(savedConfig)
    if (!active) return
    busyAction = ''
    pendingForget = null
    forgetTrigger = null
    await tick()
    if (!active) return
    listHeading?.focus()
  }
</script>

<div class="min-h-screen bg-slate-100">
  <header class="border-b border-slate-200 bg-white">
    <div class="mx-auto flex max-w-7xl items-center justify-between gap-4 px-4 py-4 sm:px-6 lg:px-8">
      <p class="text-xl font-bold tracking-tight text-slate-950">IGoNotes</p>
      <button
        type="button"
        onclick={onBack}
        disabled={workspaceBusy}
        class="rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
      >
        Назад к заметкам
      </button>
    </div>
  </header>

  <main class="mx-auto grid max-w-7xl md:grid-cols-[15rem_minmax(0,1fr)]">
    <nav class="border-b border-slate-200 bg-white p-4 md:min-h-[calc(100vh-73px)] md:border-b-0 md:border-r md:p-6" aria-label="Настройки">
      <div
        role="tablist"
        aria-label="Разделы настроек"
        aria-orientation={isDesktop ? 'vertical' : 'horizontal'}
        class="flex gap-2 overflow-x-auto md:sticky md:top-6 md:flex-col"
      >
        <button
          id="settings-bases-tab"
          type="button"
          role="tab"
          aria-selected={selectedTab === 'bases'}
          aria-controls="settings-bases-panel"
          tabindex={selectedTab === 'bases' ? 0 : -1}
          onclick={() => selectTab('bases')}
          onkeydown={(event) => navigateTabs(event, 'bases')}
          disabled={workspaceBusy}
          class={`shrink-0 whitespace-nowrap rounded-lg px-3 py-2 text-left text-sm font-semibold focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50 md:w-full ${selectedTab === 'bases' ? 'bg-blue-50 text-blue-700' : 'text-slate-600 hover:bg-slate-50'}`}
        >
          Базы заметок
        </button>
        <button
          id="settings-git-tab"
          type="button"
          role="tab"
          aria-selected={selectedTab === 'git'}
          aria-controls="settings-git-panel"
          tabindex={selectedTab === 'git' ? 0 : -1}
          onclick={() => selectTab('git')}
          onkeydown={(event) => navigateTabs(event, 'git')}
          disabled={workspaceBusy}
          class={`shrink-0 whitespace-nowrap rounded-lg px-3 py-2 text-left text-sm font-semibold focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50 md:w-full ${selectedTab === 'git' ? 'bg-blue-50 text-blue-700' : 'text-slate-600 hover:bg-slate-50'}`}
        >
          Git-синхронизация
        </button>
      </div>
    </nav>

    <div
      id="settings-bases-panel"
      role="tabpanel"
      aria-labelledby="settings-bases-tab"
      hidden={selectedTab !== 'bases'}
      class="min-w-0 p-4 sm:p-6 lg:p-8"
    >
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
            disabled={workspaceBusy}
            class="shrink-0 rounded-lg bg-blue-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm transition hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Добавить базу
          </button>
        </div>

        {#if workspaceError}
          <div role="alert" class="mt-6 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
            {workspaceError}
          </div>
        {/if}

        <div class="mt-8 grid gap-4 xl:grid-cols-2">
          {#each bases as base (base.name)}
            <BaseCard
              {base}
              current={base.name === config.current_base}
              canForget={bases.length > 1}
              busyAction={baseBusyAction}
              error={operationErrors[base.name] || ''}
              onOpen={openBase}
              onEdit={showEdit}
              onForget={askForget}
            />
          {/each}
        </div>
      {:else if panel === 'add'}
        <div class="w-full max-w-2xl">
          <h1
            bind:this={panelHeading}
            tabindex="-1"
            class="text-3xl font-bold tracking-tight text-slate-950 outline-none"
          >Добавить базу</h1>
          <p class="mb-8 mt-2 text-slate-600">Создайте новый каталог или подключите существующий.</p>
          <BaseForm
            formId="settings-add-base"
            mode="create"
            {existingNames}
            submitLabel="Добавить"
            busy={workspaceBusy}
            apiError={formError}
            showMode={true}
            onSubmit={addBase}
            onCancel={showList}
          />
        </div>
      {:else if editingBase}
        <div class="w-full max-w-2xl">
          <h1
            bind:this={panelHeading}
            tabindex="-1"
            class="text-3xl font-bold tracking-tight text-slate-950 outline-none"
          >Изменить базу</h1>
          <p class="mb-8 mt-2 text-slate-600">Обновите имя или каталог базы заметок.</p>
          <BaseForm
            formId="settings-edit-base"
            mode="edit"
            initialName={editingBase.name}
            initialPath={editingBase.path}
            {existingNames}
            originalName={editingBase.name}
            submitLabel="Сохранить"
            busy={workspaceBusy}
            apiError={formError}
            onSubmit={editBase}
            onCancel={showList}
          />
        </div>
      {/if}
    </div>

    <div
      id="settings-git-panel"
      role="tabpanel"
      aria-labelledby="settings-git-tab"
      hidden={selectedTab !== 'git'}
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
  </main>

  <Modal
    show={pendingForget !== null}
    title={pendingForget ? `Забыть базу ${pendingForget.name}?` : 'Забыть базу?'}
    description="Каталог и файлы останутся на диске"
    confirmText="Забыть базу"
    danger={true}
    busy={busyAction.startsWith('forget:')}
    onConfirm={confirmForget}
    onCancel={cancelForget}
  />
</div>
