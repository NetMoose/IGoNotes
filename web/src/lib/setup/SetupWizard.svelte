<script>
  import { onDestroy, onMount, tick } from 'svelte'

  import { ApiError, completeSetup } from '../api.js'
  import { normalizeBaseDraft, resolveBasePath } from '../base-draft.js'
  import BaseForm from './BaseForm.svelte'

  let { onComplete } = $props()

  let step = $state(1)
  let draft = $state({ mode: '', name: '', path: '' })
  let busy = $state(false)
  let apiError = $state(null)
  let generalError = $state('')
  let completed = false
  let active = true
  let headingElement = $state()
  let detailsElement = $state()
  let generalErrorElement = $state()

  const stepLabels = ['Выбор базы', 'Параметры', 'Проверка']

  async function focusHeading() {
    await tick()
    if (active) headingElement?.focus()
  }

  onMount(() => {
    focusHeading()
  })

  onDestroy(() => {
    active = false
  })

  async function changeStep(nextStep) {
    if (!active) return
    step = nextStep
    await focusHeading()
  }

  async function selectMode(mode) {
    draft = { ...draft, mode }
    apiError = null
    generalError = ''
    await changeStep(2)
  }

  async function review(nextDraft) {
    draft = nextDraft
    apiError = null
    generalError = ''
    await changeStep(3)
  }

  async function backToModes() {
    if (!active) return
    const nameInput = detailsElement?.querySelector('input[id="setup-base-name"]')
    const pathInput = detailsElement?.querySelector('input[id="setup-base-path"]')
    draft = {
      ...draft,
      name: nameInput?.value ?? draft.name,
      path: pathInput?.value ?? draft.path,
    }
    apiError = null
    generalError = ''
    await changeStep(1)
  }

  async function finish() {
    if (!active || busy || completed) return

    busy = true
    apiError = null
    generalError = ''
    let saved
    try {
      saved = await completeSetup(normalizeBaseDraft(draft))
    } catch (error) {
      if (!active) return
      busy = false
      if (
        error instanceof ApiError
        && (error.field === 'name' || error.field === 'path')
      ) {
        apiError = error
        await changeStep(2)
      } else {
        generalError = error instanceof Error && error.message
          ? error.message
          : 'Не удалось завершить настройку'
        await tick()
        if (active) generalErrorElement?.focus()
      }
      return
    }

    if (!active) return
    busy = false
    completed = true
    try {
      onComplete(saved)
    } catch (error) {
      if (active) completed = false
      throw error
    }
  }
</script>

<div class="min-h-screen bg-slate-100 p-4 sm:p-8">
  <section
    class="mx-auto grid min-h-[calc(100vh-2rem)] max-w-6xl overflow-hidden rounded-2xl bg-white shadow-xl lg:min-h-[720px] lg:grid-cols-[20rem_minmax(0,1fr)]"
    aria-labelledby="setup-title"
  >
    <aside class="bg-blue-700 p-6 text-white sm:p-8 lg:p-10">
      <div class="lg:sticky lg:top-10">
        <p class="text-2xl font-bold tracking-tight">IGoNotes</p>
        <p class="mt-3 max-w-sm text-sm leading-6 text-blue-100">
          Подготовьте локальную базу Markdown-заметок перед началом работы.
        </p>

        <ol class="mt-8 grid grid-cols-3 gap-3 lg:block lg:space-y-6">
          {#each stepLabels as label, index}
            <li class="flex min-w-0 items-center gap-3">
              <span
                class={`flex size-8 shrink-0 items-center justify-center rounded-full border text-sm font-bold ${index + 1 <= step ? 'border-white bg-white text-blue-700' : 'border-blue-300 text-blue-100'}`}
                aria-current={index + 1 === step ? 'step' : undefined}
              >
                {index + 1}
              </span>
              <span class="sr-only min-w-0 text-sm font-medium sm:not-sr-only">{label}</span>
            </li>
          {/each}
        </ol>
      </div>
    </aside>

    <main class="flex min-w-0 flex-col p-6 sm:p-10 lg:p-14" aria-live="polite">
      {#if step === 1}
        <div class="my-auto">
          <p class="text-sm font-semibold uppercase tracking-wider text-blue-600">Шаг 1 из 3</p>
          <h1
            bind:this={headingElement}
            id="setup-title"
            tabindex="-1"
            class="mt-2 text-3xl font-bold tracking-tight text-slate-950 outline-none sm:text-4xl"
          >
            Настройте первую базу
          </h1>
          <p class="mt-4 max-w-2xl text-base leading-7 text-slate-600">
            Создайте отдельный каталог для новой базы или подключите уже существующую папку.
          </p>

          <div class="mt-8 grid gap-4 sm:grid-cols-2">
            <button
              type="button"
              aria-label="Создать новую"
              onclick={() => selectMode('create')}
              class="group rounded-2xl border border-slate-200 p-6 text-left shadow-sm transition hover:-translate-y-0.5 hover:border-blue-500 hover:shadow-md focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
            >
              <span class="block text-lg font-bold text-slate-900 group-hover:text-blue-700">Создать новую</span>
              <span class="mt-2 block text-sm leading-6 text-slate-600">
                IGoNotes создаст каталог базы внутри выбранной папки.
              </span>
            </button>
            <button
              type="button"
              aria-label="Подключить существующую"
              onclick={() => selectMode('connect')}
              class="group rounded-2xl border border-slate-200 p-6 text-left shadow-sm transition hover:-translate-y-0.5 hover:border-blue-500 hover:shadow-md focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
            >
              <span class="block text-lg font-bold text-slate-900 group-hover:text-blue-700">Подключить существующую</span>
              <span class="mt-2 block text-sm leading-6 text-slate-600">
                Используйте каталог, где уже находятся ваши заметки.
              </span>
            </button>
          </div>
        </div>
      {:else if step === 2}
        <div bind:this={detailsElement} class="my-auto w-full max-w-2xl">
          <p class="text-sm font-semibold uppercase tracking-wider text-blue-600">Шаг 2 из 3</p>
          <h1
            bind:this={headingElement}
            id="setup-title"
            tabindex="-1"
            class="mt-2 text-3xl font-bold tracking-tight text-slate-950 outline-none"
          >
            Укажите имя и каталог
          </h1>
          <p class="mb-8 mt-3 text-slate-600">
            Укажите понятное имя и каталог для хранения заметок.
          </p>

          <button
            type="button"
            onclick={backToModes}
            disabled={busy}
            class="mb-6 rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Назад
          </button>

          <BaseForm
            formId="setup-base"
            mode={draft.mode}
            initialName={draft.name}
            initialPath={draft.path}
            submitLabel="Продолжить"
            {busy}
            {apiError}
            onSubmit={review}
          />
        </div>
      {:else}
        <div class="my-auto w-full max-w-2xl">
          <p class="text-sm font-semibold uppercase tracking-wider text-blue-600">Шаг 3 из 3</p>
          <h1
            bind:this={headingElement}
            id="setup-title"
            tabindex="-1"
            class="mt-2 text-3xl font-bold tracking-tight text-slate-950 outline-none"
          >
            Проверьте настройки
          </h1>
          <p class="mt-3 text-slate-600">Убедитесь, что база будет открыта из нужного каталога.</p>

          <dl class="mt-8 divide-y divide-slate-200 overflow-hidden rounded-xl border border-slate-200 bg-slate-50">
            <div class="grid gap-1 px-5 py-4 sm:grid-cols-[10rem_minmax(0,1fr)]">
              <dt class="text-sm font-medium text-slate-500">Режим</dt>
              <dd class="font-semibold text-slate-900">
                {draft.mode === 'create' ? 'Создать новую' : 'Подключить существующую'}
              </dd>
            </div>
            <div class="grid gap-1 px-5 py-4 sm:grid-cols-[10rem_minmax(0,1fr)]">
              <dt class="text-sm font-medium text-slate-500">Имя базы</dt>
              <dd class="break-words font-semibold text-slate-900">{draft.name}</dd>
            </div>
            <div class="grid gap-1 px-5 py-4 sm:grid-cols-[10rem_minmax(0,1fr)]">
              <dt class="text-sm font-medium text-slate-500">Каталог</dt>
              <dd class="break-all font-mono text-sm text-slate-900">{resolveBasePath(draft)}</dd>
            </div>
          </dl>

          {#if draft.mode === 'connect'}
            <p class="mt-4 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
              IGoNotes подключит каталог на месте и никогда не перемещает файлы.
            </p>
          {/if}

          {#if generalError}
            <div
              bind:this={generalErrorElement}
              role="alert"
              tabindex="-1"
              class="mt-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700"
            >
              {generalError}
            </div>
          {/if}

          <div class="mt-8 flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
            <button
              type="button"
              onclick={() => changeStep(2)}
              disabled={busy}
              class="rounded-lg border border-slate-300 bg-white px-5 py-2.5 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
            >
              Назад
            </button>
            <button
              type="button"
              onclick={finish}
              disabled={busy}
              aria-busy={busy ? 'true' : 'false'}
              class="rounded-lg bg-blue-600 px-5 py-2.5 text-sm font-semibold text-white shadow-sm transition hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
            >
              Завершить настройку
            </button>
          </div>
        </div>
      {/if}
    </main>
  </section>
</div>
