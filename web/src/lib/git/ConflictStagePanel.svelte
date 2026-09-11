<script module>
  let nextTextareaId = 0

  const sideLabels = {
    base: 'Общий предок',
    local: 'На этом устройстве',
    remote: 'В репозитории',
  }

  function formatBytes(size) {
    const value = Number.isFinite(size) && size >= 0 ? size : 0
    const units = ['B', 'KB', 'MB', 'GB']
    const unit = value === 0 ? 0 : Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
    const amount = value / (1024 ** unit)
    return `${new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 1 }).format(amount)} ${units[unit]}`
  }
</script>

<script>
  let { side, stage = null, contentKind } = $props()

  const textareaId = `conflict-stage-${++nextTextareaId}`
  let label = $derived(sideLabels[side] ?? '')
</script>

<section class="min-w-0 rounded-lg border border-slate-200 bg-white" aria-label={label}>
  <header class="border-b border-slate-200 px-3 py-2">
    <h3 class="text-sm font-semibold text-slate-900">{label}</h3>
    {#if stage}
      <p class="truncate font-mono text-xs text-slate-500" title={stage.path}>{stage.path}</p>
    {/if}
  </header>
  <div class="p-3">
    {#if !stage}
      <p class="text-sm text-slate-500">Файл отсутствует на этой стороне</p>
    {:else if contentKind === 'binary'}
      <p class="text-sm font-medium text-slate-700">Двоичный файл</p>
    {:else if stage.preview_truncated}
      <p class="text-sm text-amber-700">Текст больше 1 MiB; выберите сторону целиком или итоговый путь.</p>
    {:else}
      <label class="sr-only" for={textareaId}>{label}</label>
      <textarea
        id={textareaId}
        readonly
        value={stage.content ?? ''}
        class="min-h-40 w-full resize-y rounded border border-slate-200 bg-slate-50 p-2 font-mono text-xs"
      ></textarea>
    {/if}
    {#if stage}
      <p class="mt-2 text-xs text-slate-500">{formatBytes(stage.size)} · режим {stage.mode}</p>
    {/if}
  </div>
</section>
