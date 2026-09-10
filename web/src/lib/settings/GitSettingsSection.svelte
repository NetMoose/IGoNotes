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

  let panel = $state('list')
  let wizardBase = $state(null)
  let pendingDisable = $state(null)
  let disableTrigger = $state(null)
  let busyAction = $state('')
  let localErrors = $state({})
  let listHeading = $state()
  let active = true

  let bases = $derived(Array.isArray(config?.bases) ? config.bases : [])
  let sectionBusy = $derived(busyAction !== '' || Boolean(busyBase))

  onDestroy(() => {
    active = false
    onBusyChange(false)
  })

  function message(error, fallback) {
    return error instanceof Error && error.message ? error.message : fallback
  }

  function setBusy(action) {
    busyAction = action
    onBusyChange(action !== '')
  }

  async function focusList() {
    await tick()
    if (active && listHeading?.isConnected) listHeading.focus()
  }

  async function showList() {
    panel = 'list'
    wizardBase = null
    await focusList()
  }

  function showWizard(base) {
    if (!active || sectionBusy) return
    panel = 'wizard'
    wizardBase = base
  }

  function wizardBusy(busy) {
    if (!active) return
    setBusy(busy ? 'wizard' : '')
  }

  async function configured(response) {
    if (!active) return
    await onConfigChange(replaceConfigBase(config, response.base))
    if (!active) return
    await onRefresh()
    if (!active) return
    await showList()
  }

  function askDisable(base, trigger) {
    if (!active || sectionBusy) return
    pendingDisable = base
    disableTrigger = trigger
  }

  async function restoreDisableFocus(trigger) {
    await tick()
    if (!active) return
    if (trigger?.isConnected) trigger.focus()
    else listHeading?.focus()
  }

  async function cancelDisable() {
    if (busyAction !== '') return
    const trigger = disableTrigger
    pendingDisable = null
    disableTrigger = null
    await restoreDisableFocus(trigger)
  }

  async function confirmDisable() {
    if (!active || busyAction !== '' || !pendingDisable) return
    const base = pendingDisable
    const trigger = disableTrigger
    setBusy(`disable:${base.name}`)
    localErrors = { ...localErrors, [base.name]: '' }

    let response
    try {
      response = await disableGit(base.name)
      if (!active) return
      await onConfigChange(replaceConfigBase(config, response.base))
      if (!active) return
      await onRefresh()
    } catch (error) {
      if (!active) return
      setBusy('')
      pendingDisable = null
      disableTrigger = null
      localErrors = { ...localErrors, [base.name]: message(error, 'Не удалось отключить Git') }
      await restoreDisableFocus(trigger)
      return
    }

    if (!active) return
    setBusy('')
    pendingDisable = null
    disableTrigger = null
    await focusList()
  }
</script>

{#if panel === 'wizard' && wizardBase}
  <GitSetupWizard
    base={wizardBase}
    onConfigured={configured}
    onCancel={showList}
    onBusyChange={wizardBusy}
  />
{:else}
  <section aria-label="Git-синхронизация">
    <h1
      bind:this={listHeading}
      tabindex="-1"
      class="text-3xl font-bold tracking-tight text-slate-950 outline-none"
    >Git-синхронизация</h1>
    <p class="mt-2 text-slate-600">Настройте репозитории и синхронизацию для каждой базы заметок.</p>

    {#if pollError}
      <div
        role="alert"
        aria-label="Ошибка опроса Git"
        class="mt-6 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700"
      >
        {pollError}
      </div>
    {/if}

    <div class="mt-8 grid gap-4 xl:grid-cols-2">
      {#each bases as base (base.name)}
        <GitBaseCard
          {base}
          status={gitStatusFor(base.name, statuses)}
          busy={sectionBusy}
          error={localErrors[base.name] || actionErrors[base.name] || ''}
          onConfigure={showWizard}
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
  busy={busyAction.startsWith('disable:')}
  onConfirm={confirmDisable}
  onCancel={cancelDisable}
/>
