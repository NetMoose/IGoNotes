<script>
  let {
    base,
    current = false,
    canForget = false,
    busyAction = '',
    error = '',
    onOpen,
    onEdit,
    onForget,
  } = $props()

  let busy = $derived(busyAction !== '')
</script>

<article
  aria-label={`База ${base.name}`}
  aria-busy={busy ? 'true' : 'false'}
  class={`flex min-w-0 flex-col rounded-2xl border bg-white p-5 shadow-sm ${current ? 'border-blue-500 ring-1 ring-blue-100' : 'border-slate-200'}`}
>
  <div class="flex min-w-0 items-start justify-between gap-3">
    <div class="min-w-0">
      <h2 class="truncate text-lg font-bold text-slate-950">{base.name}</h2>
      <p class="mt-1 break-all font-mono text-sm text-slate-600">{base.path}</p>
    </div>
    {#if current}
      <span class="shrink-0 rounded-full bg-blue-100 px-2.5 py-1 text-xs font-semibold text-blue-700">
        Текущая
      </span>
    {/if}
  </div>

  <div class="mt-5 flex flex-wrap gap-2 text-xs font-medium text-slate-500">
    <span class="rounded-full bg-slate-100 px-2.5 py-1">Git не настроен</span>
    <span class="rounded-full bg-slate-100 px-2.5 py-1">Автосинхронизация выключена</span>
  </div>

  {#if error}
    <p role="alert" class="mt-4 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
      {error}
    </p>
  {/if}

  <div class="mt-auto flex flex-wrap justify-end gap-2 pt-6">
    {#if !current}
      <button
        type="button"
        onclick={() => onOpen(base.name)}
        disabled={busy}
        class="rounded-lg bg-blue-600 px-3.5 py-2 text-sm font-semibold text-white transition hover:bg-blue-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
      >
        Открыть
      </button>
    {/if}
    <button
      type="button"
      onclick={() => onEdit(base)}
      disabled={busy}
      class="rounded-lg border border-slate-300 bg-white px-3.5 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
    >
      Изменить
    </button>
    {#if canForget && !current}
      <button
        type="button"
        onclick={(event) => onForget(base, event.currentTarget)}
        disabled={busy}
        class="rounded-lg border border-red-300 bg-white px-3.5 py-2 text-sm font-semibold text-red-700 transition hover:bg-red-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-red-500 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
      >
        Забыть
      </button>
    {/if}
  </div>
</article>
