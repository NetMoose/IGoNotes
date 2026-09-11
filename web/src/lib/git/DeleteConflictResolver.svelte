<script>
  import ConflictStagePanel from './ConflictStagePanel.svelte'
  import { buildResolution, suggestSidePath } from './conflict-resolution.js'

  const actionLabels = {
    local: 'Сохранить версию на этом устройстве',
    remote: 'Сохранить версию из репозитория',
    manual: 'Объединить вручную',
    keep_both: 'Сохранить обе версии',
    delete: 'Удалить итоговый файл',
  }

  let {
    base,
    operationId,
    conflict,
    busy = false,
    onResolve = () => {},
  } = $props()

  let action = $state('')
  let resultPath = $state('')
  let manualContent = $state('')
  let localPath = $state('')
  let remotePath = $state('')
  let confirmingDelete = $state(false)
  let error = $state('')
  let pending = $state(false)
  let initialized = $state(false)
  let existingAction = $derived(conflict.actions?.includes('local') ? 'local' : 'remote')
  let controlsDisabled = $derived(busy || pending)

  $effect(() => {
    if (initialized) return
    const existingStage = conflict[existingAction]
    resultPath = existingStage?.path ?? conflict.path ?? ''
    manualContent = existingStage?.content ?? ''
    localPath = suggestSidePath(conflict.path ?? '', 'local')
    remotePath = suggestSidePath(conflict.path ?? '', 'remote')
    initialized = true
  })

  function selectAction() {
    confirmingDelete = false
  }

  async function resolve() {
    if (action === 'delete' && !confirmingDelete) {
      confirmingDelete = true
      error = ''
      return
    }

    let resolution
    try {
      resolution = buildResolution({ base, operationId }, conflict, {
        action,
        resultPath,
        content: manualContent,
        localPath,
        remotePath,
      })
    } catch (cause) {
      error = cause instanceof Error ? cause.message : String(cause)
      return
    }

    error = ''
    pending = true
    try {
      await onResolve(resolution)
    } catch (cause) {
      error = cause instanceof Error ? cause.message : String(cause)
    } finally {
      pending = false
    }
  }
</script>

<div class="space-y-5">
  <div data-testid="conflict-stages" class="grid grid-cols-1 gap-4 lg:grid-cols-3">
    <ConflictStagePanel side="base" stage={conflict.base} contentKind={conflict.content_kind} />
    <ConflictStagePanel side="local" stage={conflict.local} contentKind={conflict.content_kind} />
    <ConflictStagePanel side="remote" stage={conflict.remote} contentKind={conflict.content_kind} />
  </div>

  {#if existingAction === 'local'}
    <p class="text-sm text-slate-600">Версия на этом устройстве будет сохранена, а версия из репозитория была удалена.</p>
  {:else}
    <p class="text-sm text-slate-600">Версия из репозитория будет сохранена, а версия на этом устройстве была удалена.</p>
  {/if}

  {#if conflict.kind === 'rename_delete' && conflict.original_path}
    <dl class="grid grid-cols-1 gap-2 rounded-lg bg-slate-50 p-3 text-sm sm:grid-cols-2">
      <div>
        <dt class="font-medium text-slate-700">Исходный путь</dt>
        <dd class="break-all font-mono text-slate-600">{conflict.original_path}</dd>
      </div>
      <div>
        <dt class="font-medium text-slate-700">Переименованный путь</dt>
        <dd class="break-all font-mono text-slate-600">{conflict.path}</dd>
      </div>
    </dl>
  {/if}

  <form class="space-y-4" onsubmit={(event) => { event.preventDefault(); resolve() }}>
    <fieldset disabled={controlsDisabled} class="space-y-2">
      <legend class="text-sm font-semibold text-slate-900">Способ разрешения</legend>
      {#each conflict.actions ?? [] as option}
        {#if actionLabels[option]}
          <label class="flex min-h-11 items-center gap-3 rounded-lg border border-slate-200 px-3 py-2 text-sm text-slate-700">
            <input
              type="radio"
              name={`delete-resolution-${conflict.id}`}
              value={option}
              bind:group={action}
              onchange={selectAction}
            />
            <span>{actionLabels[option]}</span>
          </label>
        {/if}
      {/each}
    </fieldset>

    {#if action === 'local' || action === 'remote' || action === 'manual'}
      <label class="block text-sm font-medium text-slate-700">
        Путь результата
        <input
          type="text"
          bind:value={resultPath}
          disabled={controlsDisabled}
          class="mt-1 block w-full rounded border border-slate-300 px-3 py-2 text-sm"
        />
      </label>
    {/if}

    {#if action === 'manual'}
      <label class="block text-sm font-medium text-slate-700">
        Итоговый текст
        <textarea
          bind:value={manualContent}
          disabled={controlsDisabled}
          class="mt-1 block min-h-40 w-full rounded border border-slate-300 p-3 font-mono text-sm"
        ></textarea>
      </label>
    {/if}

    {#if action === 'keep_both'}
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <label class="block text-sm font-medium text-slate-700">
          Путь версии на этом устройстве
          <input
            type="text"
            bind:value={localPath}
            disabled={controlsDisabled}
            class="mt-1 block w-full rounded border border-slate-300 px-3 py-2 text-sm"
          />
        </label>
        <label class="block text-sm font-medium text-slate-700">
          Путь версии из репозитория
          <input
            type="text"
            bind:value={remotePath}
            disabled={controlsDisabled}
            class="mt-1 block w-full rounded border border-slate-300 px-3 py-2 text-sm"
          />
        </label>
      </div>
    {/if}

    {#if confirmingDelete}
      <p class="rounded bg-amber-50 px-3 py-2 text-sm text-amber-800">Файл будет удалён с обеих сторон конфликта.</p>
    {/if}

    {#if error}
      <p role="alert" class="rounded bg-red-50 px-3 py-2 text-sm text-red-700">{error}</p>
    {/if}

    <button
      type="submit"
      disabled={controlsDisabled || !action}
      class="rounded bg-blue-600 px-4 py-2 text-sm font-semibold text-white disabled:cursor-not-allowed disabled:opacity-50"
    >
      {confirmingDelete ? 'Подтвердить удаление' : 'Применить решение'}
    </button>
  </form>
</div>
