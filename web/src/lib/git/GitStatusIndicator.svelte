<script>
  import { presentGitStatus } from './git-settings.js'

  let {
    base,
    status = null,
    busy = false,
    error = '',
    onSync = () => {},
  } = $props()

  let open = $state(false)
  let presentation = $derived(presentGitStatus(base, status))
</script>

<div class="relative inline-flex">
  <button
    type="button"
    aria-expanded={open ? 'true' : 'false'}
    aria-label={`Открыть детали Git: ${presentation.label}`}
    class={`inline-flex items-center gap-2 rounded-full px-2.5 py-1 text-xs font-semibold ${{
      slate: 'bg-slate-100 text-slate-800',
      blue: 'bg-blue-100 text-blue-800',
      green: 'bg-green-100 text-green-800',
      amber: 'bg-amber-100 text-amber-800',
      red: 'bg-red-100 text-red-800',
    }[presentation.tone] ?? 'bg-slate-100 text-slate-800'}`}
    onclick={() => open = !open}
  >
    <span aria-hidden="true" class="h-2 w-2 rounded-full bg-current"></span>
    {presentation.label}
  </button>

  {#if open}
    <!-- svelte-ignore a11y_no_redundant_roles -->
    <section
      role="region"
      aria-label="Детали Git"
      class="absolute bottom-full right-0 z-20 mb-2 w-[min(22rem,calc(100vw-1.5rem))] rounded-xl border border-slate-200 bg-white p-4 text-sm text-slate-700 shadow-lg"
    >
      <div class="flex items-start justify-between gap-3">
        <div class="min-w-0">
          <p class="font-semibold text-slate-950">{base.name}</p>
          <p class="mt-1 text-slate-600">{base.git_branch || 'Ветка не выбрана'}</p>
        </div>
        <button
          type="button"
          aria-label="Закрыть детали Git"
          class="shrink-0 rounded p-1 text-slate-500 hover:bg-slate-100 hover:text-slate-800 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
          onclick={() => open = false}
        >
          <span aria-hidden="true">×</span>
        </button>
      </div>

      <dl class="mt-4 grid grid-cols-2 gap-x-4 gap-y-2">
        <dt class="text-slate-500">Впереди:</dt>
        <dd class="text-right font-medium text-slate-900">{status?.ahead ?? 0}</dd>
        <dt class="text-slate-500">Позади:</dt>
        <dd class="text-right font-medium text-slate-900">{status?.behind ?? 0}</dd>
        {#if status?.stage}
          <dt class="text-slate-500">Этап:</dt>
          <dd class="text-right font-medium text-slate-900">{status.stage}</dd>
        {/if}
        {#if status?.last_success}
          <dt class="col-span-2 text-slate-500">Последняя успешная синхронизация: {status.last_success}</dt>
        {/if}
      </dl>

      {#if status?.error?.message}
        <p class="mt-3 rounded-lg bg-red-50 px-3 py-2 text-red-700">{status.error.message}</p>
      {/if}
      {#if error}
        <p role="alert" class="mt-3 rounded-lg bg-red-50 px-3 py-2 text-red-700">{error}</p>
      {/if}

      <button
        type="button"
        disabled={busy || !presentation.canSync}
        aria-busy={busy || presentation.busy ? 'true' : 'false'}
        class="mt-4 inline-flex items-center gap-2 rounded-lg bg-blue-600 px-3 py-2 text-sm font-semibold text-white hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
        onclick={() => onSync(base.name)}
      >
        <span aria-hidden="true">↻</span>
        Синхронизировать Git
      </button>
    </section>
  {/if}
</div>
