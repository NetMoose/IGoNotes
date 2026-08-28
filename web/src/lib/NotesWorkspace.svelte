<script>
  import Sidebar from './Sidebar.svelte';
  import Editor from './Editor.svelte';

  let {
    activeNote,
    content = $bindable(''),
    saveStatus = 'idle',
    basePath = '',
    error = '',
    transitioning = false,
    onSelectNote,
    onDeleteNote,
    onSave,
    onOpenSettings,
  } = $props();

  let editor = $state();

  export function flushPendingUploads() {
    return editor?.flushPendingUploads?.();
  }

  function runAfterUploads(callback, ...args) {
    const uploads = flushPendingUploads();
    if (!uploads) return callback(...args);
    return Promise.resolve(uploads).then(() => callback(...args));
  }
</script>

<div
  inert={transitioning}
  aria-busy={transitioning ? 'true' : 'false'}
  class="flex flex-col h-screen w-full bg-white text-gray-800 font-sans overflow-hidden"
>
  <div class="flex-1 flex overflow-hidden">
    <Sidebar onSelect={(...args) => runAfterUploads(onSelectNote, ...args)} onDelete={onDeleteNote} />

    <main class="flex-1 flex flex-col h-full overflow-hidden min-w-0">
      <header class="bg-white border-b border-gray-200 px-4 py-2 flex items-center justify-between shrink-0 h-14">
        <div class="min-w-0">
          <div class="font-semibold text-gray-800 truncate">
            {activeNote ? activeNote.name : 'Выберите заметку'}
          </div>
          {#if error}
            <p role="alert" class="text-xs text-red-600 truncate">{error}</p>
          {/if}
        </div>
        <div class="flex gap-3 items-center shrink-0">
          {#if saveStatus === 'saving'}
            <span class="text-sm text-blue-500 font-medium flex items-center gap-1">
              <svg class="animate-spin h-4 w-4" viewBox="0 0 24 24" fill="none" aria-hidden="true"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>
              Сохранение...
            </span>
          {:else if saveStatus === 'saved'}
            <span class="text-sm text-green-600 font-medium flex items-center gap-1">
              <svg class="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"></path></svg>
              Сохранено
            </span>
          {:else if saveStatus === 'error'}
            <span class="text-sm text-red-500 font-medium">Ошибка сохранения</span>
          {/if}

          <button
            type="button"
            onclick={(...args) => runAfterUploads(onOpenSettings, ...args)}
            disabled={saveStatus === 'saving'}
            aria-label="Открыть настройки"
            title="Настройки"
            class="rounded-md p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-blue-600 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-50"
          >
            <svg class="h-5 w-5" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15.5a3.5 3.5 0 100-7 3.5 3.5 0 000 7z"></path><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19.4 15a1.7 1.7 0 00.34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 00-1.88-.34 1.7 1.7 0 00-1.03 1.56V21h-4v-.08A1.7 1.7 0 008.97 19.4a1.7 1.7 0 00-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 004.6 15a1.7 1.7 0 00-1.52-1H3v-4h.08A1.7 1.7 0 004.6 8.97a1.7 1.7 0 00-.34-1.88l-.06-.06L7.03 4.2l.06.06A1.7 1.7 0 008.97 4.6 1.7 1.7 0 0010 3.08V3h4v.08a1.7 1.7 0 001.03 1.52 1.7 1.7 0 001.88-.34l.06-.06 2.83 2.83-.06.06a1.7 1.7 0 00-.34 1.88A1.7 1.7 0 0020.92 10H21v4h-.08A1.7 1.7 0 0019.4 15z"></path></svg>
          </button>

          <button
            type="button"
            onclick={(...args) => runAfterUploads(onSave, ...args)}
            disabled={!activeNote || saveStatus === 'saving'}
            class="px-4 py-1.5 bg-blue-600 text-white font-medium text-sm rounded-md hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-50 transition-colors shadow-sm cursor-pointer"
          >
            Сохранить
          </button>
        </div>
      </header>

      <div class="flex-1 overflow-hidden">
        {#if activeNote}
          <Editor bind:this={editor} noteId={activeNote.id} bind:content />
        {:else}
          <div class="h-full flex items-center justify-center text-gray-400 bg-gray-50">
            Выберите файл в меню слева
          </div>
        {/if}
      </div>
    </main>
  </div>

  <footer class="bg-gray-100 border-t border-gray-200 px-3 py-1 flex items-center shrink-0 h-6">
    <span class="text-[11px] text-gray-500 font-mono truncate flex items-center gap-1" title="Текущая база заметок">
      {#if basePath}
        <svg class="h-3 w-3 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"></path></svg>
        {basePath}
      {:else}
        Загрузка информации о базе...
      {/if}
    </span>
  </footer>
</div>
