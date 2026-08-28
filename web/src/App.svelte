<script>
  import { onMount } from 'svelte'

  import { getConfig, getNote, saveNote, switchBase } from './lib/api.js'
  import { activeBase } from './lib/base-draft.js'
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

  let saveTimer = null
  let statusTimer = null
  let ignoreNextChange = false
  let mounted = false
  let loadToken = 0
  let noteRequestToken = 0

  function errorMessage(error, fallback) {
    return error instanceof Error && error.message ? error.message : fallback
  }

  function clearSaveTimer() {
    if (saveTimer === null) return
    clearTimeout(saveTimer)
    saveTimer = null
  }

  function clearStatusTimer() {
    if (statusTimer === null) return
    clearTimeout(statusTimer)
    statusTimer = null
  }

  function applyConfig(savedConfig) {
    config = savedConfig
    const current = activeBase(savedConfig)
    basePath = typeof current?.path === 'string' ? current.path : ''
  }

  function resetEditorState() {
    noteRequestToken += 1
    clearSaveTimer()
    clearStatusTimer()
    activeNote = null
    markdownContent = ''
    saveStatus = 'idle'
    transitionError = ''
    ignoreNextChange = false
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

    try {
      const note = await getNote(node.id)
      if (!mounted || token !== noteRequestToken) return

      clearSaveTimer()
      clearStatusTimer()
      activeNote = node
      ignoreNextChange = true
      markdownContent = typeof note?.content === 'string' ? note.content : ''
      saveStatus = 'idle'
      transitionError = ''
    } catch (error) {
      if (!mounted || token !== noteRequestToken) return
      transitionError = errorMessage(error, 'Не удалось загрузить заметку')
    }
  }

  async function saveCurrentNote() {
    if (!activeNote) return

    const noteId = activeNote.id
    const content = markdownContent
    clearSaveTimer()
    clearStatusTimer()
    saveStatus = 'saving'
    transitionError = ''

    try {
      await saveNote(noteId, content)
      if (!mounted) return
      saveStatus = 'saved'
      statusTimer = setTimeout(() => {
        statusTimer = null
        if (mounted) saveStatus = 'idle'
      }, 3000)
    } catch (error) {
      if (mounted) {
        saveStatus = 'error'
        transitionError = errorMessage(error, 'Не удалось сохранить заметку')
      }
      throw error
    }
  }

  function handleDeleted(id) {
    if (activeNote?.id === id) resetEditorState()
  }

  async function saveNow() {
    await saveCurrentNote()
  }

  function openSettings() {
    screen = 'settings'
  }

  async function openBase(name) {
    const savedConfig = await switchBase(name)
    if (!mounted) return
    applyConfig(savedConfig)
    resetEditorState()
    screen = 'editor'
  }

  $effect(() => {
    const currentContent = markdownContent
    const currentNote = activeNote

    if (ignoreNextChange) {
      ignoreNextChange = false
      return
    }

    if (currentNote) {
      clearSaveTimer()
      saveTimer = setTimeout(() => {
        saveTimer = null
        void saveCurrentNote().catch(() => {})
      }, 2000)
    }
  })

  onMount(() => {
    mounted = true
    void loadApplication()

    return () => {
      mounted = false
      loadToken += 1
      noteRequestToken += 1
      clearSaveTimer()
      clearStatusTimer()
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
      {activeNote}
      bind:content={markdownContent}
      {saveStatus}
      {basePath}
      error={transitionError}
      onSelectNote={loadNote}
      onDeleteNote={handleDeleted}
      onSave={saveNow}
      onOpenSettings={openSettings}
    />
  {:else if screen === 'settings'}
    <SettingsWorkspace
      {config}
      onConfigChange={applyConfig}
      onSwitch={openBase}
      onBack={() => screen = 'editor'}
    />
  {/if}
</div>
