<script>
  import { onMount } from 'svelte'

  import { getInfo } from '../api.js'

  let info = $state(null)
  let loading = $state(true)
  let error = $state('')
  let active = false

  onMount(() => {
    active = true
    loadInfo()
    return () => { active = false }
  })

  async function loadInfo() {
    loading = true
    error = ''
    try {
      const result = await getInfo()
      if (!active) return
      if (typeof result?.app_name !== 'string' || !result.app_name.trim()
          || typeof result?.app_version !== 'string' || !result.app_version.trim()) {
        throw new Error('Некорректные сведения о программе')
      }
      info = result
    } catch (cause) {
      if (active) {
        error = cause instanceof Error && cause.message ? cause.message : 'Не удалось загрузить сведения о программе'
      }
    } finally {
      if (active) loading = false
    }
  }
</script>

<section aria-label="О программе">
  <h1 class="text-3xl font-bold tracking-tight text-slate-950">О программе</h1>
  <div class="mt-8 max-w-2xl rounded-2xl border border-slate-200 bg-white p-6 shadow-sm">
    {#if loading}
      <p role="status" class="text-sm text-slate-600">Загрузка сведений о программе…</p>
    {:else if error}
      <p role="alert" class="text-sm text-red-700">{error}</p>
      <button
        type="button"
        onclick={loadInfo}
        class="mt-4 rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
      >Повторить загрузку</button>
    {:else if info}
      <h2 class="break-words text-2xl font-bold tracking-tight text-slate-950">{info.app_name}</h2>
      <dl class="mt-6">
        <dt class="text-sm font-medium text-slate-500">Версия</dt>
        <dd class="mt-1 break-all text-base text-slate-800">
          {info.app_version === 'dev' ? 'Сборка разработки' : info.app_version}
        </dd>
      </dl>
    {/if}
  </div>
</section>
