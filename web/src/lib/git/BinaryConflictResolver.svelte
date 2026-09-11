<script>
  import ConflictStagePanel from './ConflictStagePanel.svelte'
  import { buildResolution, suggestSidePath } from './conflict-resolution.js'

  const actionLabels = {
    local: 'Оставить версию на этом устройстве',
    remote: 'Оставить версию из репозитория',
    keep_both: 'Сохранить обе версии',
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
  let localPath = $state('')
  let remotePath = $state('')
  let error = $state('')
  let pending = $state(false)
  let initialized = $state(false)
  let controlsDisabled = $derived(busy || pending)

  $effect(() => {
    if (initialized) return
    resultPath = conflict.path ?? ''
    localPath = suggestSidePath(conflict.path ?? '', 'local')
    remotePath = suggestSidePath(conflict.path ?? '', 'remote')
    initialized = true
  })

  async function resolve() {
    let resolution
    try {
      resolution = buildResolution({ base, operationId }, conflict, {
        action,
        resultPath,
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
    <ConflictStagePanel side="base" stage={conflict.base} contentKind="binary" />
    <ConflictStagePanel side="local" stage={conflict.local} contentKind="binary" />
    <ConflictStagePanel side="remote" stage={conflict.remote} contentKind="binary" />
  </div>

  <form class="space-y-4" onsubmit={(event) => { event.preventDefault(); resolve() }}>
    <fieldset disabled={controlsDisabled} class="space-y-2">
      <legend class="text-sm font-semibold text-slate-900">Способ разрешения</legend>
      {#each conflict.actions ?? [] as option}
        {#if actionLabels[option]}
          <label class="flex items-center gap-2 text-sm text-slate-700">
            <input type="radio" name="conflict-resolution" value={option} bind:group={action} />
            {actionLabels[option]}
          </label>
        {/if}
      {/each}
    </fieldset>

    {#if action === 'local' || action === 'remote'}
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

    {#if action === 'keep_both'}
      <div class="space-y-2">
        <p class="text-sm text-slate-600">Оба файла будут записаны под явно заданными разными именами; существующие несвязанные файлы не будут перезаписаны.</p>
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
      </div>
    {/if}

    {#if error}
      <p role="alert" class="rounded bg-red-50 px-3 py-2 text-sm text-red-700">{error}</p>
    {/if}

    <button
      type="submit"
      disabled={controlsDisabled || !action}
      class="rounded bg-blue-600 px-4 py-2 text-sm font-semibold text-white disabled:cursor-not-allowed disabled:opacity-50"
    >
      Применить решение
    </button>
  </form>
</div>
