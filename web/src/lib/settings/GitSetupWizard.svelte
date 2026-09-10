<script>
  import { onDestroy, tick } from 'svelte'

  import { configureGit, probeGit } from '../api.js'
  import {
    DEFAULT_GIT_COMMIT_TEMPLATE,
    GIT_INTERVALS,
    GIT_TEMPLATE_VARIABLES,
    buildGitConfigRequest,
    renderGitCommitPreview,
    requiredConfirmationKeys,
    validateGitDraft,
  } from '../git/git-settings.js'

  let { base, onConfigured, onCancel, onBusyChange = () => {} } = $props()

  let step = $state(1)
  let busyAction = $state('')
  let error = $state('')
  let draft = $state(createDraft())
  let discovery = $state(null)
  let configurationProbe = $state(null)
  let confirmations = $state({})
  let branchURL = $state(initialBranchURL())
  let active = true
  let stepOneHeading = $state()
  let stepTwoHeading = $state()
  let stepThreeHeading = $state()
  let stepFourHeading = $state()
  let urlInput = $state()
  let branchInput = $state()
  let intervalInput = $state()
  let templateInput = $state()
  let autoSyncInput = $state()
  let alertElement = $state()

  let busy = $derived(busyAction !== '')
  let remoteBranches = $derived(Array.isArray(discovery?.remote_branches) ? discovery.remote_branches : [])
  let emptyRemote = $derived(discovery?.empty_remote === true)
  let requiredChecks = $derived(requiredConfirmationKeys(configurationProbe))
  let preview = $derived(renderGitCommitPreview(draft.template, {
    base: base?.name ?? '',
    branch: String(draft.branch ?? '').trim(),
  }))
  let templateVariables = $derived(GIT_TEMPLATE_VARIABLES.map((variable) => `{{${variable}}}`).join(', '))

  onDestroy(() => {
    active = false
    onBusyChange(false)
  })

  function createDraft() {
    return {
      gitURL: String(base?.git_url ?? ''),
      branch: String(base?.git_branch ?? ''),
      autoSync: Boolean(base?.auto_sync),
      interval: base?.auto_sync_interval_minutes ?? 15,
      template: String(base?.git_commit_message_template ?? '') || DEFAULT_GIT_COMMIT_TEMPLATE,
    }
  }

  function initialBranchURL() {
    return normalizedURL(base?.git_url)
  }

  function normalizedURL(value) {
    return String(value ?? '').trim()
  }

  function message(error, fallback) {
    return error?.message || fallback
  }

  function setBusy(action) {
    busyAction = action
    onBusyChange(action !== '')
  }

  async function focus(target) {
    await tick()
    const element = typeof target === 'function' ? target() : target
    if (active && element?.isConnected) element.focus()
  }

  function resetError() {
    error = ''
  }

  function validateURL() {
    const errors = validateGitDraft({ ...draft, branch: 'branch', autoSync: false })
    return errors.gitURL
  }

  function validateBranch() {
    const errors = validateGitDraft({ ...draft, autoSync: false })
    return errors.branch
  }

  function discoveryIsValid(result) {
    const branches = result?.remote_branches
    return result?.base === base?.name
      && result?.can_configure === false
      && !result?.blocking_error
      && Array.isArray(branches)
      && (result.empty_remote === true ? branches.length === 0 : result.empty_remote === false && branches.length > 0)
  }

  function selectedProbeIsValid(result) {
    return result?.base === base?.name && result?.can_configure === true && !result?.blocking_error
  }

  function reconcileBranch(result) {
    const current = String(draft.branch ?? '')
    if (result.empty_remote) {
      if (normalizedURL(draft.gitURL) !== branchURL) draft.branch = ''
      return
    }
    if (result.remote_branches.includes(current)) return
    draft.branch = result.remote_branches.length === 1 ? result.remote_branches[0] : ''
  }

  async function checkRepository() {
    if (!active || busy) return
    resetError()
    const validationError = validateURL()
    if (validationError) {
      error = validationError
      await focus(urlInput)
      return
    }

    setBusy('discovery')
    let result
    try {
      result = await probeGit({
        base: base.name,
        git_url: normalizedURL(draft.gitURL),
        git_branch: '',
      })
    } catch (requestError) {
      if (!active) return
      setBusy('')
      await showDiscoveryError(requestError, 'Не удалось проверить репозиторий')
      return
    }

    if (!active) return
    setBusy('')
    if (!discoveryIsValid(result)) {
      await showDiscoveryError(result?.blocking_error, 'Сервер вернул некорректный ответ при поиске веток')
      return
    }
    reconcileBranch(result)
    discovery = result
    branchURL = normalizedURL(draft.gitURL)
    confirmations = {}
    step = 2
    await focus(() => stepTwoHeading)
  }

  async function checkBranch() {
    if (!active || busy) return
    resetError()
    const validationError = validateBranch()
    if (validationError) {
      error = validationError
      await focus(branchInput)
      return
    }

    setBusy('branch')
    let result
    try {
      result = await probeGit({
        base: base.name,
        git_url: normalizedURL(draft.gitURL),
        git_branch: String(draft.branch ?? '').trim(),
      })
    } catch (requestError) {
      if (!active) return
      setBusy('')
      await showProbeError(requestError, 'Не удалось проверить ветку')
      return
    }

    if (!active) return
    setBusy('')
    if (!selectedProbeIsValid(result)) {
      const fallback = result?.base !== base?.name
        ? 'Сервер вернул некорректный ответ проверки ветки'
        : 'Репозиторий нельзя настроить'
      await showProbeError(result?.blocking_error, fallback)
      return
    }
    configurationProbe = result
    confirmations = {}
    step = 3
    await focus(() => stepThreeHeading)
  }

  async function backToRepository() {
    if (busy) return
    resetError()
    step = 1
    await focus(() => stepOneHeading)
  }

  async function review() {
    if (busy) return
    resetError()
    const errors = validateGitDraft(draft)
    if (errors.interval) {
      error = errors.interval
      await focus(intervalInput)
      return
    }
    if (errors.template) {
      error = errors.template
      await focus(templateInput)
      return
    }
    step = 4
    await focus(() => stepFourHeading)
  }

  async function backToSchedule() {
    if (busy) return
    resetError()
    step = 3
    await focus(() => stepThreeHeading)
  }

  async function configure() {
    if (!active || busy) return
    resetError()
    const missing = requiredChecks.some((key) => confirmations[key] !== true)
    if (missing) {
      error = 'Подтвердите обязательные последствия'
      await focus(() => alertElement)
      return
    }

    setBusy('configure')
    let response
    try {
      response = await configureGit(base.name, buildGitConfigRequest(draft, configurationProbe, confirmations))
      if (!active) return
      await onConfigured(response)
    } catch (requestError) {
      if (!active) return
      setBusy('')
      await showConfigurationError(requestError)
      return
    }
    if (!active) return
    setBusy('')
  }

  async function showConfigurationError(requestError) {
    error = message(requestError, 'Не удалось настроить Git')
    const field = requestError?.field
    if (field === 'git_url') {
      step = 1
      await focus(() => urlInput)
    } else if (field === 'git_branch') {
      step = 2
      await focus(() => branchInput)
    } else if (field === 'auto_sync' || field === 'auto_sync_interval_minutes') {
      step = 3
      await focus(() => field === 'auto_sync' ? autoSyncInput : intervalInput)
    } else if (field === 'git_commit_message_template') {
      step = 3
      await focus(() => templateInput)
    } else {
      await focus(() => alertElement)
    }
  }

  async function showProbeError(requestError, fallback) {
    error = message(requestError, fallback)
    if (requestError?.field === 'git_url') {
      step = 1
      await focus(() => urlInput)
    } else if (requestError?.field === 'git_branch') {
      step = 2
      await focus(() => branchInput)
    } else {
      await focus(() => alertElement)
    }
  }

  async function showDiscoveryError(requestError, fallback) {
    error = message(requestError, fallback)
    step = 1
    if (requestError?.field === 'git_url') await focus(() => urlInput)
    else await focus(() => alertElement)
  }
</script>

<section class="mx-auto w-full max-w-2xl" aria-label="Настройка Git">
  {#if step === 1}
    <h1 bind:this={stepOneHeading} tabindex="-1" class="text-3xl font-bold tracking-tight text-slate-950 outline-none">
      Шаг 1 из 4: репозиторий
    </h1>
    <p class="mt-2 text-slate-600">Укажите удаленный Git-репозиторий для базы «{base.name}».</p>
    <form class="mt-8 space-y-5" onsubmit={(event) => { event.preventDefault(); checkRepository() }}>
      <div>
        <label for="git-setup-url" class="block text-sm font-semibold text-slate-800">URL репозитория</label>
        <input
          bind:this={urlInput}
          id="git-setup-url"
          type="text"
          bind:value={draft.gitURL}
          disabled={busy}
          aria-invalid={Boolean(error)}
          class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 text-slate-950 disabled:cursor-not-allowed disabled:bg-slate-100"
        />
      </div>
      {#if error}<div bind:this={alertElement} role="alert" tabindex="-1" class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>{/if}
      <div class="flex justify-end gap-3">
        <button type="button" onclick={onCancel} disabled={busy} aria-busy={busy}>Отмена</button>
        <button type="submit" disabled={busy} aria-busy={busy} class="rounded-lg bg-blue-600 px-4 py-2 font-semibold text-white disabled:cursor-not-allowed disabled:opacity-50">Проверить репозиторий</button>
      </div>
    </form>
  {:else if step === 2}
    <h1 bind:this={stepTwoHeading} tabindex="-1" class="text-3xl font-bold tracking-tight text-slate-950 outline-none">Шаг 2 из 4: ветка</h1>
    <form class="mt-8 space-y-5" onsubmit={(event) => { event.preventDefault(); checkBranch() }}>
      <div>
        {#if emptyRemote}
          <label for="git-setup-branch" class="block text-sm font-semibold text-slate-800">Новая ветка</label>
          <input bind:this={branchInput} id="git-setup-branch" type="text" bind:value={draft.branch} disabled={busy} class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 disabled:bg-slate-100" />
        {:else}
          <label for="git-setup-branch" class="block text-sm font-semibold text-slate-800">Ветка</label>
          <select bind:this={branchInput} id="git-setup-branch" bind:value={draft.branch} disabled={busy} class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 disabled:bg-slate-100">
            <option value="">Выберите ветку</option>
            {#each remoteBranches as branch}
              <option value={branch}>{branch}</option>
            {/each}
          </select>
        {/if}
      </div>
      {#if error}<div bind:this={alertElement} role="alert" tabindex="-1" class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>{/if}
      <div class="flex justify-between gap-3">
        <button type="button" onclick={backToRepository} disabled={busy} aria-busy={busy}>Назад</button>
        <button type="submit" disabled={busy} aria-busy={busy} class="rounded-lg bg-blue-600 px-4 py-2 font-semibold text-white disabled:cursor-not-allowed disabled:opacity-50">Продолжить</button>
      </div>
    </form>
  {:else if step === 3}
    <h1 bind:this={stepThreeHeading} tabindex="-1" class="text-3xl font-bold tracking-tight text-slate-950 outline-none">Шаг 3 из 4: расписание и коммиты</h1>
    <form class="mt-8 space-y-5" onsubmit={(event) => { event.preventDefault(); review() }}>
      <fieldset>
        <legend class="text-sm font-semibold text-slate-800">Синхронизация</legend>
        <div class="mt-2 flex gap-4">
          <label><input type="radio" name="sync-mode" value="manual" checked={!draft.autoSync} onchange={() => { draft.autoSync = false }} disabled={busy} /> Только вручную</label>
          <label><input bind:this={autoSyncInput} type="radio" name="sync-mode" value="automatic" checked={draft.autoSync} onchange={() => { draft.autoSync = true }} disabled={busy} /> Автоматически</label>
        </div>
      </fieldset>
      {#if draft.autoSync}
        <div>
          <label for="git-setup-interval" class="block text-sm font-semibold text-slate-800">Интервал</label>
          <select bind:this={intervalInput} id="git-setup-interval" bind:value={draft.interval} disabled={busy} class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 disabled:bg-slate-100">
            <option value="">Выберите интервал</option>
            {#each GIT_INTERVALS as interval}<option value={interval}>{interval} минут</option>{/each}
          </select>
        </div>
      {/if}
      <div>
        <label for="git-setup-template" class="block text-sm font-semibold text-slate-800">Шаблон сообщения коммита</label>
        <input bind:this={templateInput} id="git-setup-template" type="text" bind:value={draft.template} disabled={busy} class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 disabled:bg-slate-100" />
        <p class="mt-1 text-sm text-slate-600">{templateVariables}</p>
      </div>
      <div>
        <p class="text-sm font-semibold text-slate-800">Предпросмотр сообщения</p>
        <output aria-label="Предпросмотр сообщения" class="mt-1 block rounded-lg bg-slate-100 px-3 py-2 text-sm text-slate-800">{preview}</output>
      </div>
      {#if error}<div bind:this={alertElement} role="alert" tabindex="-1" class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>{/if}
      <div class="flex justify-between gap-3">
        <button type="button" onclick={() => { step = 2; focus(() => stepTwoHeading) }} disabled={busy} aria-busy={busy}>Назад</button>
        <button type="submit" disabled={busy} aria-busy={busy} class="rounded-lg bg-blue-600 px-4 py-2 font-semibold text-white disabled:cursor-not-allowed disabled:opacity-50">Проверить настройки</button>
      </div>
    </form>
  {:else}
    <h1 bind:this={stepFourHeading} tabindex="-1" class="text-3xl font-bold tracking-tight text-slate-950 outline-none">Шаг 4 из 4: подтверждение</h1>
    <section aria-label="Проверка Git-настроек" class="mt-8 space-y-4 rounded-lg border border-slate-200 p-4">
      <dl class="grid gap-2 text-sm sm:grid-cols-[12rem_1fr]">
        <dt class="font-semibold text-slate-700">URL репозитория</dt><dd>{normalizedURL(draft.gitURL)}</dd>
        <dt class="font-semibold text-slate-700">Ветка</dt><dd>{String(draft.branch ?? '').trim()}</dd>
        <dt class="font-semibold text-slate-700">Синхронизация</dt><dd>{draft.autoSync ? `Автоматически, каждые ${draft.interval} минут` : 'Только вручную'}</dd>
        <dt class="font-semibold text-slate-700">Шаблон сообщения коммита</dt><dd>{draft.template}</dd>
      </dl>
      {#if configurationProbe?.required_mutations?.create_repository}
        <label class="flex gap-2"><input type="checkbox" bind:checked={confirmations.create_repository} disabled={busy} /> Создать Git-репозиторий</label>
      {/if}
      {#if configurationProbe?.required_mutations?.add_origin}<p class="rounded bg-amber-50 px-3 py-2 text-sm text-amber-900">Добавить origin</p>{/if}
      {#if configurationProbe?.required_mutations?.replace_origin}
        <label class="flex gap-2"><input type="checkbox" bind:checked={confirmations.replace_origin} disabled={busy} /> Заменить origin</label>
      {/if}
      {#if configurationProbe?.required_mutations?.create_branch}
        <label class="flex gap-2"><input type="checkbox" bind:checked={confirmations.create_branch} disabled={busy} /> Создать ветку</label>
      {/if}
      {#if configurationProbe?.required_mutations?.merge_histories}
        <label class="flex gap-2"><input type="checkbox" bind:checked={confirmations.merge_histories} disabled={busy} /> Объединить несвязанные истории</label>
      {/if}
      {#each configurationProbe?.warnings ?? [] as warning}
        <p class="rounded bg-amber-50 px-3 py-2 text-sm text-amber-900">{warning}</p>
      {/each}
    </section>
    {#if error}<div bind:this={alertElement} role="alert" tabindex="-1" class="mt-5 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>{/if}
    <div class="mt-6 flex justify-between gap-3">
      <button type="button" onclick={backToSchedule} disabled={busy} aria-busy={busy}>Назад</button>
      <button type="button" onclick={configure} disabled={busy} aria-busy={busy} class="rounded-lg bg-blue-600 px-4 py-2 font-semibold text-white disabled:cursor-not-allowed disabled:opacity-50">Сохранить Git-настройки</button>
    </div>
  {/if}
</section>
