<script>
  import { onDestroy, onMount, tick } from 'svelte'
  import { createClipperConnection, getClipperConnections, revokeClipperConnection } from '../api.js'

  let connections = $state([])
  let loading = $state(true)
  let loadError = $state('')
  let busyAction = $state('')
  let label = $state('')
  let labelError = $state('')
  let createError = $state('')
  let revokeErrors = $state({})
  let secret = $state('')
  let labelInput = $state()
  let active = false
  const buttonClass = 'rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50'

  onMount(() => {
    active = true
    loadConnections()
  })
  onDestroy(() => {
    active = false
    secret = ''
  })

  function message(error, fallback) {
    return error instanceof Error && error.message ? error.message : fallback
  }

  async function loadConnections() {
    if (!active || busyAction) return
    loading = true
    loadError = ''
    try {
      const result = await getClipperConnections()
      if (active) connections = result.connections
    } catch (error) {
      if (active) loadError = message(error, 'Не удалось загрузить подключения')
    } finally {
      if (active) loading = false
    }
  }

  async function createConnection(event) {
    event.preventDefault()
    if (!active || busyAction || loading) return
    secret = ''
    labelError = ''
    createError = ''
    if (!label.trim() || [...label].length > 128 || /\p{Cc}/u.test(label)) {
      labelError = 'Введите от 1 до 128 символов без управляющих символов'
      labelInput?.focus()
      return
    }
    busyAction = 'create'
    try {
      const result = await createClipperConnection(label)
      if (!active) return
      connections = [...connections.filter((item) => item.id !== result.connection.id), result.connection]
      secret = result.secret
      label = ''
    } catch (error) {
      if (!active) return
      if (error?.status === 400 && error?.field === 'label') {
        labelError = message(error, 'Некорректное название подключения')
      } else {
        createError = `${message(error, 'Не удалось создать подключение')}. Результат создания неизвестен: подключение могло быть создано, но секрет не получен. Обновите список и отзовите ненужное подключение. Повторное нажатие создаст новое подключение.`
      }
    } finally {
      if (active) busyAction = ''
    }
    if (active && labelError) {
      await tick()
      if (active) labelInput?.focus()
    }
  }

  async function revokeConnection(connection) {
    if (!active || busyAction || loading || connection.revoked_at !== null) return
    secret = ''
    busyAction = `revoke:${connection.id}`
    revokeErrors = { ...revokeErrors, [connection.id]: '' }
    try {
      await revokeClipperConnection(connection.id)
      if (!active) return
      // Revocation is confirmed; GET supplies its authoritative timestamp.
      connections = connections.map((item) => item.id === connection.id ? { ...item, revoked_at: '' } : item)
    } catch (error) {
      if (active) revokeErrors = { ...revokeErrors, [connection.id]: message(error, 'Не удалось отозвать подключение') }
      return
    } finally {
      if (active) busyAction = ''
    }
    if (active) await loadConnections()
  }
</script>

<section aria-label="Веб-клиппер" class="max-w-3xl">
  <h1 class="text-3xl font-bold tracking-tight text-slate-950">Веб-клиппер</h1>
  <p class="mt-2 text-slate-600">Управляйте подключениями браузера к IGoNotes.</p>

  <form onsubmit={createConnection} novalidate class="mt-8 rounded-2xl border border-slate-200 bg-white p-6 shadow-sm" aria-busy={busyAction === 'create'}>
    <label for="clipper-connection-label" class="block text-sm font-semibold text-slate-700">Название подключения</label>
    <p id="clipper-label-help" class="mt-1 text-sm text-slate-500">От 1 до 128 символов, без управляющих символов.</p>
    <input
      id="clipper-connection-label"
      bind:this={labelInput}
      bind:value={label}
      aria-invalid={Boolean(labelError)}
      aria-describedby={labelError ? 'clipper-label-help clipper-label-error' : 'clipper-label-help'}
      disabled={Boolean(busyAction)}
      autocomplete="off"
      class="mt-2 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-slate-950 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-50"
    />
    {#if labelError}
      <p id="clipper-label-error" role="alert" class="mt-2 text-sm text-red-700">{labelError}</p>
    {/if}
    {#if createError}
      <p role="alert" class="mt-4 text-sm text-red-700">{createError}</p>
    {/if}
    <button type="submit" disabled={loading || Boolean(busyAction)} aria-busy={busyAction === 'create'} class="mt-4 rounded-lg bg-blue-600 px-4 py-2 text-sm font-semibold text-white hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50">Создать подключение</button>
    {#if busyAction === 'create'}
      <p role="status" class="mt-2 text-sm text-slate-600">Создание подключения…</p>
    {/if}
  </form>

  {#if secret}
    <div class="mt-6 rounded-2xl border border-blue-200 bg-blue-50 p-6" role="status">
      <h2 class="text-lg font-bold text-slate-950">Секрет подключения</h2>
      <p class="mt-2 text-sm text-slate-700">Скопируйте секрет в настройки расширения. Он показывается только один раз и исчезнет при уходе из раздела, создании или отзыве подключения.</p>
      <code class="mt-4 block select-all break-all rounded-lg bg-white p-3 text-slate-950">{secret}</code>
      <button type="button" onclick={() => secret = ''} class={`mt-4 ${buttonClass}`}>Скрыть секрет</button>
    </div>
  {/if}

  <div class="mt-8 flex items-center justify-between gap-4">
    <h2 class="text-xl font-bold text-slate-950">Подключения</h2>
    <button type="button" onclick={loadConnections} disabled={loading || Boolean(busyAction)} class={buttonClass}>Обновить список</button>
  </div>
  {#if loading}
    <p role="status" class="mt-4 text-sm text-slate-600">Загрузка подключений…</p>
  {/if}
  {#if loadError}
    <div class="mt-4 rounded-lg border border-red-200 bg-red-50 p-4">
      <p role="alert" class="text-sm text-red-700">{loadError}</p>
      <button type="button" onclick={loadConnections} disabled={loading || Boolean(busyAction)} class={`mt-3 ${buttonClass}`}>Повторить загрузку</button>
    </div>
  {:else if !loading && connections.length === 0}
    <p class="mt-4 text-slate-600">Подключений пока нет</p>
  {/if}
  <div class="mt-4 grid gap-4">
    {#each connections as connection (connection.id)}
      <article aria-label={`Подключение ${connection.label}`} aria-busy={busyAction === `revoke:${connection.id}`} class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm">
        <h3 class="break-words text-lg font-bold text-slate-950">{connection.label}</h3>
        <p class="mt-1 text-sm font-semibold text-slate-700">{connection.revoked_at !== null ? 'Отозвано' : 'Активно'}</p>
        <dl class="mt-3 text-sm text-slate-600">
          <dt>Создано</dt><dd class="break-all">{connection.created_at}</dd>
          {#if connection.revoked_at}
            <dt class="mt-2">Дата отзыва</dt><dd class="break-all">{connection.revoked_at}</dd>
          {/if}
        </dl>
        {#if revokeErrors[connection.id]}
          <p role="alert" class="mt-3 text-sm text-red-700">{revokeErrors[connection.id]}</p>
        {/if}
        <button type="button" onclick={() => revokeConnection(connection)} disabled={loading || Boolean(busyAction) || connection.revoked_at !== null} class={`mt-4 ${buttonClass}`}>Отозвать</button>
        {#if busyAction === `revoke:${connection.id}`}
          <p role="status" class="mt-2 text-sm text-slate-600">Отзыв подключения…</p>
        {/if}
      </article>
    {/each}
  </div>
</section>
