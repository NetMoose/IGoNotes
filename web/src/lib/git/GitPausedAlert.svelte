<script>
  let { status = null, busy = false, error = '', onResume = () => {}, onOpenSettings = () => {} } = $props()
  let attempt = $derived(status?.last_attempt ? new Date(status.last_attempt) : null)
  let validAttempt = $derived(attempt !== null && !Number.isNaN(attempt.getTime()))
</script>

{#if status?.state === 'paused'}
  <section role="alert" aria-labelledby="git-paused-title" class="shrink-0 border-b border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-950">
    <h2 id="git-paused-title" class="font-semibold">Git-синхронизация приостановлена</h2>
    <p class="mt-1">{status.error?.message || 'Автоматическая синхронизация остановлена до явного возобновления.'}</p>
    <p class="mt-1">Последовательных ошибок: {status.consecutive_failures}.</p>
    <p>
      {#if validAttempt}
        Последняя попытка:
        <time datetime={status.last_attempt}>{attempt.toLocaleString('ru-RU')}</time>
      {:else}
        Время последней попытки неизвестно
      {/if}
    </p>
    {#if error}<p role="status" class="mt-2 text-red-700">{error}</p>{/if}
    <div class="mt-3 flex flex-wrap gap-2">
      <button type="button" disabled={busy} aria-busy={busy ? 'true' : 'false'} onclick={onResume} class="rounded-lg bg-blue-600 px-3 py-2 font-semibold text-white hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-50">Повторить и возобновить</button>
      <button type="button" disabled={busy} onclick={onOpenSettings} class="rounded-lg border border-amber-300 px-3 py-2 font-semibold hover:bg-amber-100 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-50">Открыть настройки Git</button>
    </div>
  </section>
{/if}
