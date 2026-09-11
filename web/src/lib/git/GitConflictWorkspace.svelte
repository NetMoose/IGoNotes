<script>
  import { tick } from 'svelte'

  import DialogShell from '../DialogShell.svelte'
  import {
    abortGitConflict,
    completeGitConflict,
    getGitConflicts,
    resolveGitConflict,
  } from '../api.js'
  import BinaryConflictResolver from './BinaryConflictResolver.svelte'
  import DeleteConflictResolver from './DeleteConflictResolver.svelte'
  import TextConflictResolver from './TextConflictResolver.svelte'

  let {
    base,
    status,
    bases = [],
    busy = false,
    onSwitchBase = () => {},
    onOperationAccepted = () => {},
  } = $props()

  let workspace = $state(null)
  let selectedId = $state('')
  let error = $state('')
  let loading = $state(false)
  let actionPending = $state(false)
  let terminalPending = $state(false)
  let abortDialogOpen = $state(false)
  let switchTarget = $state('')
  let loadedKey = $state('')
  let requestToken = 0
  let completeButton = $state()

  let baseName = $derived(typeof base === 'string' ? base : base?.name ?? '')
  let operationId = $derived(status?.operation_id ?? '')
  let conflictStatus = $derived(!status?.state || status.state === 'conflict')
  let conflicts = $derived(workspace?.conflicts ?? [])
  let switchableBases = $derived(bases.filter((candidate) => candidate.name !== baseName))
  let selectedConflict = $derived(conflicts.find((conflict) => conflict.id === selectedId) ?? null)
  let controlsDisabled = $derived(busy || loading || actionPending || terminalPending)

  function messageFor(error, fallback) {
    return error instanceof Error && error.message ? error.message : fallback
  }

  function conflictButton(id) {
    return document.getElementById(`git-conflict-${id}`)
  }

  async function focusSelectedConflict() {
    await tick()
    conflictButton(selectedId)?.focus()
  }

  async function focusAfterResolution() {
    await tick()
    if (conflicts.length === 0) {
      completeButton?.focus()
      return
    }
    conflictButton(selectedId)?.focus()
  }

  function applyWorkspace(result) {
    if (result.base !== baseName || result.operation_id !== operationId) {
      workspace = null
      selectedId = ''
      error = 'Операция разрешения конфликтов изменилась'
      return false
    }

    const previousId = selectedId
    const previousIndex = conflicts.findIndex((conflict) => conflict.id === previousId)
    workspace = result
    const nextIndex = result.conflicts.findIndex((conflict) => conflict.id === previousId)
    if (nextIndex >= 0) {
      selectedId = previousId
    } else {
      const nearestIndex = Math.min(Math.max(previousIndex, 0), result.conflicts.length - 1)
      selectedId = result.conflicts[nearestIndex]?.id ?? ''
    }
    return true
  }

  async function loadConflicts() {
    const expectedBase = baseName
    const expectedOperation = operationId
    if (!expectedBase || !expectedOperation) {
      workspace = null
      selectedId = ''
      error = ''
      return
    }

    const token = ++requestToken
    loading = true
    error = ''

    try {
      const result = await getGitConflicts(expectedBase)
      if (token !== requestToken || expectedBase !== baseName || expectedOperation !== operationId) return
      applyWorkspace(result)
    } catch (cause) {
      if (token === requestToken) error = messageFor(cause, 'Не удалось загрузить конфликты')
      throw cause
    } finally {
      if (token === requestToken) loading = false
    }
  }

  $effect(() => {
    if (!conflictStatus) return
    const nextKey = `${baseName}:${operationId}`
    if (nextKey === loadedKey) return
    loadedKey = nextKey
    loadConflicts().catch(() => {})
  })

  $effect(() => {
    if (switchableBases.some((candidate) => candidate.name === switchTarget)) return
    switchTarget = ''
  })

  async function selectConflict(id, focus = false) {
    if (controlsDisabled || id === selectedId) return
    selectedId = id
    if (focus) await focusSelectedConflict()
  }

  function navigateConflicts(event, index) {
    let nextIndex = index
    if (event.key === 'ArrowUp') nextIndex = Math.max(index - 1, 0)
    else if (event.key === 'ArrowDown') nextIndex = Math.min(index + 1, conflicts.length - 1)
    else if (event.key === 'Home') nextIndex = 0
    else if (event.key === 'End') nextIndex = conflicts.length - 1
    else return

    event.preventDefault()
    selectConflict(conflicts[nextIndex]?.id ?? '', true)
  }

  async function resolve(resolution) {
    if (controlsDisabled) return
    actionPending = true
    error = ''
    let resolved = false
    try {
      const result = await resolveGitConflict(resolution)
      resolved = applyWorkspace(result.remaining)
    } catch (cause) {
      if (cause?.code !== 'git_conflict_stale') throw cause
      await loadConflicts()
      resolved = true
    } finally {
      actionPending = false
    }
    if (resolved) await focusAfterResolution()
  }

  async function switchBase() {
    if (controlsDisabled || !switchTarget) return
    actionPending = true
    error = ''
    try {
      await onSwitchBase(switchTarget)
    } catch (cause) {
      error = messageFor(cause, 'Не удалось переключить базу')
    } finally {
      actionPending = false
    }
  }

  function retryLoad() {
    if (controlsDisabled) return
    loadConflicts().catch(() => {})
  }

  async function beginTerminal(action) {
    if (controlsDisabled) return
    actionPending = true
    error = ''
    let operation
    try {
      operation = action === 'complete'
        ? await completeGitConflict(baseName)
        : await abortGitConflict(baseName)
    } catch (cause) {
      error = messageFor(cause, action === 'complete' ? 'Не удалось завершить слияние' : 'Не удалось отменить слияние')
      actionPending = false
      return
    }

    terminalPending = true
    actionPending = false
    abortDialogOpen = false
    try {
      await onOperationAccepted(operation.operation_id)
    } catch (cause) {
      error = messageFor(cause, 'Не удалось передать состояние операции')
    }
  }

  function resolverKind(conflict) {
    if (conflict.kind === 'modify_delete' || conflict.kind === 'rename_delete') return 'delete'
    return conflict.content_kind === 'binary' ? 'binary' : 'text'
  }
</script>

<main class="grid min-h-screen min-w-0 bg-slate-100 lg:grid-cols-[18rem_minmax(0,1fr)]" aria-busy={controlsDisabled}>
  <aside class="border-b border-slate-200 bg-white p-4 lg:min-h-screen lg:border-b-0 lg:border-r lg:p-6">
    <p class="text-sm text-slate-600">Текущая база: <span class="font-semibold text-slate-900">{baseName}</span></p>
    <label class="mt-4 block text-sm font-semibold text-slate-800">
      База для переключения
      <select
        bind:value={switchTarget}
        disabled={controlsDisabled}
        class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 disabled:cursor-not-allowed disabled:opacity-50"
      >
        <option value="" disabled>Выберите базу</option>
        {#each switchableBases as candidate (candidate.name)}
          <option value={candidate.name}>{candidate.name}</option>
        {/each}
      </select>
    </label>
    <button
      type="button"
      disabled={controlsDisabled || !switchTarget}
      onclick={switchBase}
      class="mt-2 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
    >
      Открыть базу
    </button>

    <nav class="mt-6" aria-label="Конфликтующие файлы">
      <h1 class="text-lg font-bold text-slate-950">Конфликты Git</h1>
      {#if conflicts.length > 0}
        <ul class="mt-3 space-y-1">
          {#each conflicts as conflict, index (conflict.id)}
            <li>
              <button
                id={`git-conflict-${conflict.id}`}
                type="button"
                aria-current={conflict.id === selectedId ? 'true' : undefined}
                disabled={controlsDisabled}
                onclick={() => selectConflict(conflict.id)}
                onkeydown={(event) => navigateConflicts(event, index)}
                class={`w-full rounded-lg px-3 py-2 text-left text-sm font-medium break-all focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50 ${conflict.id === selectedId ? 'bg-blue-50 text-blue-800' : 'text-slate-700 hover:bg-slate-50'}`}
              >
                {conflict.path}
              </button>
            </li>
          {/each}
        </ul>
      {:else if workspace}
        <p class="mt-3 text-sm text-slate-600">Все конфликты разрешены.</p>
      {/if}
    </nav>

    <div class="mt-6 space-y-2">
      <button
        bind:this={completeButton}
        type="button"
        disabled={controlsDisabled || !workspace?.can_complete || conflicts.length > 0}
        onclick={() => beginTerminal('complete')}
        class="w-full rounded-lg bg-blue-600 px-3 py-2 text-sm font-semibold text-white hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
      >
        Завершить слияние
      </button>
      <button
        type="button"
        disabled={controlsDisabled}
        onclick={() => abortDialogOpen = true}
        class="w-full rounded-lg border border-red-300 bg-white px-3 py-2 text-sm font-semibold text-red-700 hover:bg-red-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
      >
        Отменить слияние
      </button>
    </div>
  </aside>

  <section class="min-w-0 overflow-x-hidden p-4 sm:p-6 lg:p-8" aria-label="Разрешение конфликта">
    {#if terminalPending}
      <p role="status" class="rounded-lg border border-blue-200 bg-blue-50 px-4 py-3 text-sm text-blue-800">
        Ожидание завершения операции Git
      </p>
    {/if}

    {#if error}
      <p role="alert" class="mt-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</p>
      {#if !workspace}
        <button
          type="button"
          disabled={controlsDisabled}
          onclick={retryLoad}
          class="mt-3 rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
        >
          Повторить загрузку
        </button>
      {/if}
    {/if}

    {#if loading && !workspace}
      <p role="status" class="text-sm text-slate-600">Загрузка конфликтов...</p>
    {:else if selectedConflict}
      <div class="min-w-0 overflow-x-hidden">
        {#key selectedConflict.id}
          {#if resolverKind(selectedConflict) === 'delete'}
            <DeleteConflictResolver
              base={baseName}
              operationId={workspace.operation_id}
              conflict={selectedConflict}
              busy={controlsDisabled}
              onResolve={resolve}
            />
          {:else if resolverKind(selectedConflict) === 'binary'}
            <BinaryConflictResolver
              base={baseName}
              operationId={workspace.operation_id}
              conflict={selectedConflict}
              busy={controlsDisabled}
              onResolve={resolve}
            />
          {:else}
            <TextConflictResolver
              base={baseName}
              operationId={workspace.operation_id}
              conflict={selectedConflict}
              busy={controlsDisabled}
              onResolve={resolve}
            />
          {/if}
        {/key}
      </div>
    {:else if workspace}
      <p class="text-sm text-slate-600">Выберите конфликт в списке.</p>
    {/if}
  </section>
</main>

<DialogShell
  show={abortDialogOpen}
  title="Отменить слияние?"
  description="Неразрешенные изменения слияния будут отменены."
  busy={controlsDisabled}
  onCancel={() => abortDialogOpen = false}
>
  {#snippet actions()}
    <button
      type="button"
      disabled={controlsDisabled}
      onclick={() => abortDialogOpen = false}
      class="rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 disabled:cursor-not-allowed disabled:opacity-50"
    >
      Продолжить разрешение
    </button>
    <button
      type="button"
      disabled={controlsDisabled}
      onclick={() => beginTerminal('abort')}
      class="rounded-lg bg-red-600 px-4 py-2 text-sm font-semibold text-white disabled:cursor-not-allowed disabled:opacity-50"
    >
      Подтвердить отмену
    </button>
  {/snippet}
</DialogShell>
