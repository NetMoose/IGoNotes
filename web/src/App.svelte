<script>
  import { onMount } from 'svelte'

  import { deleteNote, getConfig, getGitStatus, getNote, renameNote, saveNote, switchBase, syncGit } from './lib/api.js'
  import { openSettingsSafely, switchBaseSafely } from './lib/app-transitions.js'
  import { activeBase } from './lib/base-draft.js'
  import { createGitStatusPoller } from './lib/git/git-status-poller.js'
  import NotesWorkspace from './lib/NotesWorkspace.svelte'
  import SettingsWorkspace from './lib/settings/SettingsWorkspace.svelte'
  import SetupWizard from './lib/setup/SetupWizard.svelte'

  let screen = $state('loading')
  let config = $state(null)
  let loadError = $state('')
  let activeNote = $state(null)
  let markdownContent = $state('')
  let basePath = $state('')
  let saveStatus = $state('idle')
  let transitionError = $state('')
  let dirty = $state(false)
  let transitioning = $state(false)
  let notesWorkspace = $state()
  let gitStatuses = $state([])
  let gitPollError = $state('')
  let gitBusyBase = $state('')
  let gitActionErrors = $state({})

  let saveTimer = null
  let statusTimer = null
  let savePromise = null
  let statusGeneration = 0
  let transitionCount = 0
  let transitionGeneration = 0
  let ignoreNextChange = false
  let mounted = false
  let loadToken = 0
  let noteRequestToken = 0
  let gitPoller = null
  let gitPolling = false
  const workspaceFlushFailures = new WeakSet()

  let currentBase = $derived(activeBase(config))
  let activeGitStatus = $derived(gitStatuses.find((status) => status.base === config?.current_base) ?? null)

  function errorMessage(error, fallback) {
    return typeof error?.message === 'string' && error.message ? error.message : fallback
  }

  function clearSaveTimer() {
    if (saveTimer === null) return
    clearTimeout(saveTimer)
    saveTimer = null
  }

  function clearStatusTimer() {
    statusGeneration += 1
    if (statusTimer === null) return
    clearTimeout(statusTimer)
    statusTimer = null
  }

  function beginTransition() {
    const generation = transitionGeneration
    transitionCount += 1
    transitioning = true
    return generation
  }

  function endTransition(generation) {
    if (generation !== transitionGeneration) return
    transitionCount = Math.max(0, transitionCount - 1)
    transitioning = transitionCount > 0
  }

  function resetTransitionState() {
    transitionGeneration += 1
    transitionCount = 0
    transitioning = false
  }

  function applyConfig(savedConfig) {
    const previous = activeBase(config)
    const current = activeBase(savedConfig)

    if (previous?.name !== current?.name || previous?.path !== current?.path) {
      resetEditorState()
    }

    config = savedConfig
    basePath = typeof current?.path === 'string' ? current.path : ''
    ensureGitPolling()
  }

  function applyGitStatuses(statuses) {
    gitStatuses = Array.isArray(statuses) ? statuses : []
  }

  function ensureGitPolling() {
    if (!mounted || !config?.setup_completed || gitPolling || !gitPoller) return
    gitPolling = true
    gitPoller.start()
  }

  async function refreshGitStatuses() {
    ensureGitPolling()
    return gitPoller?.refresh?.()
  }

  function resetEditorState() {
    noteRequestToken += 1
    resetTransitionState()
    clearSaveTimer()
    clearStatusTimer()
    activeNote = null
    ignoreNextChange = true
    markdownContent = ''
    dirty = false
    saveStatus = 'idle'
    transitionError = ''
  }

  async function loadApplication() {
    const token = ++loadToken
    screen = 'loading'
    loadError = ''

    try {
      const savedConfig = await getConfig()
      if (!mounted || token !== loadToken) return

      applyConfig(savedConfig)
      resetEditorState()
      screen = savedConfig?.setup_completed ? 'editor' : 'setup'
    } catch (error) {
      if (!mounted || token !== loadToken) return
      loadError = errorMessage(error, 'Не удалось загрузить настройки')
      screen = 'error'
    }
  }

  function finishSetup(savedConfig) {
    applyConfig(savedConfig)
    resetEditorState()
    screen = 'editor'
  }

  async function loadNote(node) {
    const token = ++noteRequestToken
    const transition = beginTransition()
    transitionError = ''

    try {
      try {
        await flushWorkspace()
      } catch (error) {
        if (mounted && token === noteRequestToken && saveStatus !== 'error') {
          showSaveError(error)
        }
        return
      }

      if (!mounted || token !== noteRequestToken) return
      if (activeNote?.id === node.id) return

      let note
      try {
        note = await getNote(node.id)
      } catch (error) {
        if (!mounted || token !== noteRequestToken) return
        transitionError = errorMessage(error, 'Не удалось загрузить заметку')
        return
      }

      if (!mounted || token !== noteRequestToken) return

      try {
        await flushEditorUploads()
        await flushPendingSave()
      } catch (error) {
        if (mounted && token === noteRequestToken && saveStatus !== 'error') {
          showSaveError(error)
        }
        return
      }

      if (!mounted || token !== noteRequestToken) return

      clearSaveTimer()
      clearStatusTimer()
      activeNote = node
      ignoreNextChange = true
      markdownContent = typeof note?.content === 'string' ? note.content : ''
      dirty = false
      saveStatus = 'idle'
      transitionError = ''
    } finally {
      endTransition(transition)
    }
  }

  function showSaveError(error) {
    if (!mounted) return
    clearStatusTimer()
    saveStatus = 'error'
    transitionError = `Не удалось сохранить заметку: ${errorMessage(error, 'Неизвестная ошибка')}`
  }

  async function persistCurrentNote() {
    if (!activeNote) return
    if (savePromise) {
      await savePromise
      if (!mounted || !activeNote || !dirty) return
      return persistCurrentNote()
    }

    const operationNoteId = activeNote.id
    const operation = (async () => {
      while (mounted && activeNote?.id === operationNoteId && dirty) {
        const noteId = activeNote.id
        const content = markdownContent
        clearSaveTimer()
        clearStatusTimer()
        saveStatus = 'saving'
        transitionError = ''

        try {
          await saveNote(noteId, content)
        } catch (error) {
          showSaveError(error)
          throw error
        }

        if (!mounted || activeNote?.id !== noteId) return
        if (markdownContent !== content) continue

        dirty = false
        saveStatus = 'saved'
        const generation = statusGeneration
        statusTimer = setTimeout(() => {
          statusTimer = null
          if (mounted && generation === statusGeneration && activeNote?.id === noteId) {
            saveStatus = 'idle'
          }
        }, 3000)
      }
    })()

    savePromise = operation

    try {
      await operation
    } finally {
      if (savePromise === operation) savePromise = null
    }
  }

  async function flushPendingSave() {
    clearSaveTimer()
    if (savePromise) await savePromise
    if (dirty) await persistCurrentNote()
  }

  async function flushEditorUploads() {
    if (!mounted) return
    const uploads = notesWorkspace?.flushPendingUploads?.()
    if (uploads) await uploads
  }

  async function flushWorkspace() {
    try {
      await flushEditorUploads()
      await flushPendingSave()
      await flushEditorUploads()
      await flushPendingSave()
    } catch (error) {
      if (error && typeof error === 'object') workspaceFlushFailures.add(error)
      throw error
    }
  }

  async function runGitSync(baseName) {
    if (!mounted || gitBusyBase !== '') return

    gitBusyBase = baseName
    gitActionErrors = { ...gitActionErrors, [baseName]: '' }

    try {
      await flushWorkspace()
      await syncGit(baseName)
      await refreshGitStatuses()
    } catch (error) {
      const flushError = Boolean(error && typeof error === 'object' && workspaceFlushFailures.has(error))
      if (flushError && saveStatus !== 'error') showSaveError(error)
      if (mounted) {
        gitActionErrors = {
          ...gitActionErrors,
          [baseName]: errorMessage(error, flushError
            ? 'Не удалось сохранить рабочую область перед Git-синхронизацией'
            : 'Не удалось запустить Git-синхронизацию'),
        }
      }
    } finally {
      if (gitBusyBase === baseName) gitBusyBase = ''
    }
  }

  function affectsActiveNote(id) {
    const activeId = activeNote?.id
    return activeId === id || activeId?.startsWith(`${id}/`)
  }

  async function mutateNote(id, request) {
    if (!affectsActiveNote(id)) return request()

    const transition = beginTransition()
    transitionError = ''

    try {
      try {
        await flushWorkspace()
      } catch (error) {
        if (mounted && saveStatus !== 'error') showSaveError(error)
        throw error
      }

      await request()
      if (mounted) resetEditorState()
    } finally {
      endTransition(transition)
    }
  }

  function renameNode(id, newName) {
    return mutateNote(id, () => renameNote(id, newName))
  }

  function deleteNode(id) {
    return mutateNote(id, () => deleteNote(id))
  }

  async function saveNow() {
    if (!activeNote) return
    dirty = true
    transitionError = ''
    try {
      await flushPendingSave()
    } catch (error) {
      showSaveError(error)
    }
  }

  async function openSettings() {
    const transition = beginTransition()
    try {
      await openSettingsSafely({
        flush: flushWorkspace,
        open: () => {
          if (mounted) screen = 'settings'
        },
      })
    } catch (error) {
      showSaveError(error)
    } finally {
      endTransition(transition)
    }
  }

  async function openBase(name) {
    await switchBaseSafely({
      name,
      flush: flushPendingSave,
      switchRequest: switchBase,
      commit: (savedConfig) => {
        if (!mounted) return
        applyConfig(savedConfig)
        screen = 'editor'
      },
    })
  }

  $effect(() => {
    const currentContent = markdownContent
    const currentNote = activeNote

    if (ignoreNextChange) {
      ignoreNextChange = false
      return
    }

    if (currentNote) {
      dirty = true
      clearSaveTimer()
      saveTimer = setTimeout(() => {
        saveTimer = null
        void persistCurrentNote().catch(showSaveError)
      }, 2000)
    }
  })

  onMount(() => {
    mounted = true
    gitPoller = createGitStatusPoller({
      load: getGitStatus,
      onStatuses: applyGitStatuses,
      onError: (error) => {
        gitPollError = error ? errorMessage(error, 'Не удалось получить статус Git') : ''
      },
    })
    void loadApplication()

    return () => {
      mounted = false
      loadToken += 1
      noteRequestToken += 1
      resetTransitionState()
      clearSaveTimer()
      clearStatusTimer()
      gitPoller?.stop()
      gitPoller = null
      gitPolling = false
    }
  })
</script>

<div class="min-h-screen w-full">
  {#if screen === 'loading'}
    <main role="status" class="flex min-h-screen flex-col items-center justify-center gap-4 bg-slate-100 text-slate-700">
      <svg class="h-8 w-8 animate-spin text-blue-600" viewBox="0 0 24 24" fill="none" aria-hidden="true">
        <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
        <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.37 0 0 5.37 0 12h4z"></path>
      </svg>
      <h1 class="text-lg font-semibold">Загрузка настроек...</h1>
    </main>
  {:else if screen === 'error'}
    <main class="flex min-h-screen flex-col items-center justify-center gap-5 bg-slate-100 px-6 text-center">
      <p role="alert" class="max-w-xl rounded-lg border border-red-200 bg-red-50 px-5 py-4 text-red-700">
        {loadError}
      </p>
      <button
        type="button"
        onclick={loadApplication}
        class="rounded-lg bg-blue-600 px-5 py-2.5 text-sm font-semibold text-white shadow-sm transition hover:bg-blue-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2"
      >
        Повторить
      </button>
    </main>
  {:else if screen === 'setup'}
    <SetupWizard {config} onComplete={finishSetup} />
  {:else if screen === 'editor'}
    <NotesWorkspace
      bind:this={notesWorkspace}
      {activeNote}
      bind:content={markdownContent}
      {saveStatus}
      {basePath}
      gitBase={currentBase}
      gitStatus={activeGitStatus}
      gitSyncBusy={gitBusyBase === config?.current_base}
      gitSyncError={gitActionErrors[config?.current_base] || ''}
      {transitioning}
      error={transitionError}
      onSelectNote={loadNote}
      onRenameNote={renameNode}
      onDeleteNote={deleteNode}
      onSave={saveNow}
      onOpenSettings={openSettings}
      onGitSync={runGitSync}
    />
  {:else if screen === 'settings'}
    <SettingsWorkspace
      {config}
      gitStatuses={gitStatuses}
      {gitPollError}
      {gitBusyBase}
      {gitActionErrors}
      onConfigChange={applyConfig}
      onSwitch={openBase}
      onBack={() => screen = 'editor'}
      onGitSync={runGitSync}
      onGitRefresh={refreshGitStatuses}
    />
  {/if}
</div>
