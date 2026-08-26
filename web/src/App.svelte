<script>
  import { onMount } from 'svelte';
  import Sidebar from './lib/Sidebar.svelte';
  import Editor from './lib/Editor.svelte';

  let activeNote = $state(null);
  let markdownContent = $state('');
  let basePath = $state('');
  
  // Переменные для debounce
  let saveTimer = null;
  let saveStatus = $state('idle'); // 'idle', 'saving', 'saved', 'error'
  let statusTimer = null;
  let ignoreNextChange = false;

  onMount(async () => {
    try {
      const res = await fetch('/api/info');
      if (res.ok) {
        const data = await res.json();
        basePath = data.base_path;
      }
    } catch (e) {
      console.error("Failed to load app info:", e);
    }
  });

  async function loadNote(node) {
    try {
      const res = await fetch(`/api/note?id=${encodeURIComponent(node.id)}`);
      if (res.ok) {
        const data = await res.json();
        activeNote = node;
        ignoreNextChange = true;
        markdownContent = data.content;
        saveStatus = 'idle';
      }
    } catch (err) {
      console.error("Failed to load note:", err);
    }
  }

  async function saveNote() {
    if (!activeNote) return;
    saveStatus = 'saving';
    try {
      const res = await fetch('/api/save', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: activeNote.id, content: markdownContent })
      });
      if (!res.ok) throw new Error("Server returned error");
      saveStatus = 'saved';
      if (statusTimer) clearTimeout(statusTimer);
      statusTimer = setTimeout(() => saveStatus = 'idle', 3000);
    } catch (err) {
      console.error("Failed to save note:", err);
      saveStatus = 'error';
    }
  }

  // Автосохранение (debounce 2 секунды)
  $effect(() => {
    const currentContent = markdownContent; // trigger dependency
    
    if (ignoreNextChange) {
       ignoreNextChange = false;
       return;
    }
    
    if (activeNote) {
      if (saveTimer) clearTimeout(saveTimer);
      saveTimer = setTimeout(() => {
        saveNote();
      }, 2000);
    }
  });
</script>

<div class="flex flex-col h-screen w-full bg-white text-gray-800 font-sans overflow-hidden">
  <div class="flex-1 flex overflow-hidden">
    <Sidebar onSelect={loadNote} onDelete={(id) => { if (activeNote?.id === id) activeNote = null; }} />

    <main class="flex-1 flex flex-col h-full overflow-hidden min-w-0">
      <header class="bg-white border-b border-gray-200 px-4 py-2 flex items-center justify-between shrink-0 h-14">
      <div class="font-semibold text-gray-800 truncate">
        {activeNote ? activeNote.name : 'Выберите заметку'}
      </div>
      <div class="flex gap-3 items-center">
        {#if saveStatus === 'saving'}
          <span class="text-sm text-blue-500 font-medium flex items-center gap-1">
             <svg class="animate-spin h-4 w-4" viewBox="0 0 24 24" fill="none"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>
             Сохранение...
          </span>
        {:else if saveStatus === 'saved'}
          <span class="text-sm text-green-600 font-medium flex items-center gap-1">
            <svg class="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"></path></svg>
            Сохранено
          </span>
        {:else if saveStatus === 'error'}
          <span class="text-sm text-red-500 font-medium">Ошибка сохранения</span>
        {/if}
        
        <button 
          onclick={saveNote}
          disabled={!activeNote || saveStatus === 'saving'}
          class="px-4 py-1.5 bg-blue-600 text-white font-medium text-sm rounded-md hover:bg-blue-700 disabled:opacity-50 transition-colors shadow-sm cursor-pointer">
          Сохранить
        </button>
      </div>
    </header>

    <div class="flex-1 overflow-hidden">
      {#if activeNote}
        <Editor noteId={activeNote.id} bind:content={markdownContent} />
      {:else}
        <div class="h-full flex items-center justify-center text-gray-400 bg-gray-50">
          Выберите файл в меню слева
        </div>
      {/if}
    </div>
    </main>
  </div>

  <!-- Status Line -->
  <footer class="bg-gray-100 border-t border-gray-200 px-3 py-1 flex items-center shrink-0 h-6">
    <span class="text-[11px] text-gray-500 font-mono truncate" title="Текущая база заметок">
      {basePath ? `📂 ${basePath}` : 'Загрузка информации о базе...'}
    </span>
  </footer>
</div>
