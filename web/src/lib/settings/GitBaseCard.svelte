<script>
  import { gitConfigured, presentGitStatus } from '../git/git-settings.js'

  let {
    base,
    status = null,
    busy = false,
    error = '',
    onConfigure,
    onSync,
    onDisable,
  } = $props()

  let configured = $derived(gitConfigured(base))
  let presentation = $derived(presentGitStatus(base, status))
</script>

<article
  aria-label={`Git для базы ${base.name}`}
  aria-busy={busy ? 'true' : 'false'}
  class="flex min-w-0 flex-col rounded-2xl border border-slate-200 bg-white p-5 shadow-sm"
>
  <div class="min-w-0">
    <h2 class="truncate text-lg font-bold text-slate-950">{base.name}</h2>
    <p class="mt-1 break-all font-mono text-sm text-slate-600">{base.path}</p>
  </div>

  <span
    class={`mt-5 inline-flex w-fit rounded-full px-2.5 py-1 text-xs font-semibold ${{
      slate: 'bg-slate-100 text-slate-800',
      blue: 'bg-blue-100 text-blue-800',
      green: 'bg-green-100 text-green-800',
      amber: 'bg-amber-100 text-amber-800',
      red: 'bg-red-100 text-red-800',
    }[presentation.tone] ?? 'bg-slate-100 text-slate-800'}`}
  >
    {presentation.label}
  </span>

  {#if configured}
    <dl class="mt-5 grid gap-2 text-sm text-slate-700">
      <div>
        <dt class="inline font-semibold text-slate-900">Ветка:</dt>
        <dd class="inline"> {base.git_branch}</dd>
      </div>
      <div>
        <dt class="inline font-semibold text-slate-900">Репозиторий:</dt>
        <dd class="break-all inline"> {base.git_url}</dd>
      </div>
      <div>
        <dt class="sr-only">Расписание</dt>
        <dd>{base.auto_sync ? `Каждые ${base.auto_sync_interval_minutes} минут` : 'Только вручную'}</dd>
      </div>
      {#if status?.stage}
        <div>
          <dt class="inline font-semibold text-slate-900">Этап:</dt>
          <dd class="inline"> {status.stage}</dd>
        </div>
      {/if}
    </dl>
    {#if status?.error?.message}
      <p class="mt-4 rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700">{status.error.message}</p>
    {/if}
  {:else}
    <p class="mt-5 text-sm text-slate-600">Git не настроен для этой базы заметок.</p>
  {/if}

  {#if error}
    <p role="alert" class="mt-4 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
      {error}
    </p>
  {/if}

  <div data-testid="git-card-actions" class="mt-auto flex flex-wrap justify-end gap-2 pt-6">
    <button
      type="button"
      onclick={() => onConfigure(base)}
      disabled={busy}
      class="rounded-lg border border-slate-300 bg-white px-3.5 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
    >
      {configured ? 'Изменить настройки' : 'Настроить Git'}
    </button>
    {#if configured}
      <button
        type="button"
        onclick={() => onSync(base.name)}
        disabled={busy || !presentation.canSync}
        class="rounded-lg bg-blue-600 px-3.5 py-2 text-sm font-semibold text-white transition hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
      >
        Синхронизировать сейчас
      </button>
      <button
        type="button"
        onclick={(event) => onDisable(base, event.currentTarget)}
        disabled={busy}
        class="rounded-lg border border-red-300 bg-white px-3.5 py-2 text-sm font-semibold text-red-700 transition hover:bg-red-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
      >
        Отключить Git
      </button>
    {/if}
  </div>
</article>
