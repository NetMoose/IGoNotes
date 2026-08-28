<script>
  import { onMount, onDestroy } from 'svelte';
  import Modal from './Modal.svelte';
  import { getNotes, syncNotes, createNote, renameNote, deleteNote } from './api.js';

  let { onSelect, onDelete } = $props();

  let nodes = $state([]);
  let activeId = $state(null);
  let activeName = $state(""); // имя выбранного узла для переименования
  let activeFolderId = $state(null); // корень по умолчанию (null вместо "")
  let isRefreshing = $state(false);
  let operationError = $state("");
  let mutationBusy = $state(false);
  let mounted = false;
  let loadGeneration = 0;
  let refreshTimer = null;
  
  // Храним ID открытых папок для восстановления после обновления дерева
  let openFolders = new Set();

  // Modal states
  let showCreateModal = $state(false);
  let createType = $state("");
  let createName = $state("");
  let createError = $state("");

  let showDeleteModal = $state(false);
  let deleteError = $state("");

  let showRenameModal = $state(false);
  let renameValue = $state("");
  let renameError = $state("");
  
  function restoreOpenState(nodeList) {
    for (let node of nodeList) {
      if (node.type === 'dir') {
        if (openFolders.has(node.id)) {
          node.open = true;
        }
        if (node.children && node.children.length > 0) {
          restoreOpenState(node.children);
        }
      }
    }
  }

  function clearRefreshTimer() {
    if (refreshTimer !== null) {
      clearTimeout(refreshTimer);
      refreshTimer = null;
    }
  }

  function startRefresh() {
    const generation = ++loadGeneration;
    clearRefreshTimer();
    operationError = "";
    isRefreshing = true;
    return generation;
  }

  function isCurrentLoad(generation) {
    return mounted && generation === loadGeneration;
  }

  function finishRefresh(generation) {
    if (!isCurrentLoad(generation)) return;
    refreshTimer = setTimeout(() => {
      refreshTimer = null;
      if (isCurrentLoad(generation)) isRefreshing = false;
    }, 300);
  }

  async function loadTree() {
    if (!mounted) return;
    const generation = startRefresh();
    try {
      const newNodes = await getNotes();
      if (!isCurrentLoad(generation)) return;
      if (!Array.isArray(newNodes)) {
        throw new Error("Приложение вернуло некорректное дерево заметок");
      }

      // Убедимся, что текущая активная папка (куда добавляем файлы) тоже будет открыта
      if (activeFolderId) {
        openFolders.add(activeFolderId);
      }

      restoreOpenState(newNodes);
      nodes = newNodes;
      finishRefresh(generation);
    } catch (err) {
      if (!isCurrentLoad(generation)) return;
      operationError = err instanceof Error ? err.message : "Ошибка при загрузке дерева заметок";
      isRefreshing = false;
    }
  }

  async function syncTree() {
    if (!mounted) return;
    const generation = startRefresh();
    try {
      await syncNotes();
      if (!isCurrentLoad(generation)) return;
      loadTree();
    } catch (err) {
      if (!isCurrentLoad(generation)) return;
      operationError = err instanceof Error ? err.message : "Ошибка при синхронизации дерева";
      isRefreshing = false;
    }
  }

  onMount(() => {
    mounted = true;
    loadTree();
  });

  onDestroy(() => {
    mounted = false;
    loadGeneration++;
    clearRefreshTimer();
  });

  function handleSelect(node, e) {
    e.stopPropagation();
    activeId = node.id;
    activeName = node.name;
    if (node.type === 'dir') {
      node.open = !node.open;
      if (node.open) {
        openFolders.add(node.id);
      } else {
        openFolders.delete(node.id);
      }
      activeFolderId = node.id;
    } else {
      activeFolderId = node.parent_id || null;
      if (onSelect) onSelect(node);
    }
  }

  function openCreateModal(type) {
    createType = type;
    createName = "";
    createError = "";
    showCreateModal = true;
  }

  function openRenameModal() {
    if (!activeId) return;
    renameValue = activeName;
    renameError = "";
    showRenameModal = true;
  }

  function openDeleteModal() {
    if (!activeId) return;
    deleteError = "";
    showDeleteModal = true;
  }

  async function confirmCreate() {
    if (mutationBusy || !createName.trim()) return;
    mutationBusy = true;
    operationError = "";
    createError = "";
    
    try {
      await createNote({
        parent_id: activeFolderId || "",
        name: createName.trim(),
        type: createType
      });

      if (!mounted) return;
      showCreateModal = false;
      loadTree();
    } catch (err) {
      if (!mounted) return;
      const message = err instanceof Error ? err.message : "Произошла ошибка при создании";
      operationError = message;
      if (err?.status === 409) {
        createError = createType === 'dir' 
          ? `Папка "${createName.trim()}" уже существует.` 
          : `Файл "${createName.trim()}" уже существует.`;
      } else {
        createError = message;
      }
    } finally {
      if (mounted) mutationBusy = false;
    }
  }

  async function confirmRename() {
    if (mutationBusy) return;
    operationError = "";
    renameError = "";
    if (!renameValue.trim() || renameValue === activeName) {
      showRenameModal = false;
      return;
    }
    mutationBusy = true;
    
    try {
      await renameNote(activeId, renameValue.trim());
      if (!mounted) return;
      showRenameModal = false;

      // Сбросим текущее выделение, так как ID поменялся
      // Можно также было бы попытаться обновить activeId, но сброс проще и надежнее
      if (onDelete) onDelete(activeId); // это очистит редактор в App.svelte
      activeId = null;
      activeName = "";

      loadTree();
    } catch (err) {
      if (!mounted) return;
      const message = err instanceof Error ? err.message : "Произошла ошибка при переименовании";
      operationError = message;
      renameError = message;
    } finally {
      if (mounted) mutationBusy = false;
    }
  }

  async function confirmDelete() {
    if (mutationBusy || !activeId) return;
    mutationBusy = true;
    operationError = "";
    deleteError = "";

    try {
      await deleteNote(activeId);
      if (!mounted) return;
      if (onDelete) onDelete(activeId);
      // Если удаляем папку, её тоже надо удалить из списка открытых
      openFolders.delete(activeId);
      activeId = null;
      activeFolderId = null; // Сбрасываем активную папку на корень
      activeName = "";
      showDeleteModal = false;
      loadTree();
    } catch (err) {
      if (!mounted) return;
      const message = err instanceof Error ? err.message : "Произошла ошибка при удалении";
      operationError = message;
      deleteError = message;
    } finally {
      if (mounted) mutationBusy = false;
    }
  }

  function handleContainerClick(e) {
    if (e.target === e.currentTarget) {
      activeId = null;
      activeFolderId = null;
      activeName = "";
    }
  }
</script>

<aside class="w-72 bg-gray-50 border-r border-gray-200 flex flex-col h-full shrink-0">
  <div class="p-3 border-b border-gray-200 bg-gray-100 flex flex-col gap-2">
    <div class="flex justify-between items-center mb-1">
      <h2 class="font-semibold text-gray-700 text-sm uppercase tracking-wider">База заметок</h2>
      <button onclick={syncTree} class="text-gray-400 hover:text-blue-500 transition-colors p-1 cursor-pointer" title="Обновить дерево (Синхронизировать с диском)">
        <svg class="w-4 h-4 {isRefreshing ? 'animate-spin' : ''}" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"></path></svg>
      </button>
    </div>
    {#if operationError}
      <p role="alert" class="text-xs text-red-600">{operationError}</p>
    {/if}
    <div class="flex gap-1.5 justify-between">
      <button onclick={() => openCreateModal('file')} class="flex-1 flex justify-center items-center bg-white border border-gray-300 rounded p-1.5 text-gray-600 hover:text-blue-600 hover:bg-blue-50 transition-colors shadow-sm cursor-pointer" title="Создать новую заметку">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path><polyline points="14 2 14 8 20 8"></polyline><line x1="12" y1="18" x2="12" y2="12"></line><line x1="9" y1="15" x2="15" y2="15"></line></svg>
      </button>
      <button onclick={() => openCreateModal('dir')} class="flex-1 flex justify-center items-center bg-white border border-gray-300 rounded p-1.5 text-gray-600 hover:text-blue-600 hover:bg-blue-50 transition-colors shadow-sm cursor-pointer" title="Создать новую папку">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"></path><line x1="12" y1="11" x2="12" y2="17"></line><line x1="9" y1="14" x2="15" y2="14"></line></svg>
      </button>
      <button onclick={openRenameModal} disabled={!activeId} class="flex-1 flex justify-center items-center bg-white border border-gray-300 rounded p-1.5 text-gray-600 hover:text-blue-600 hover:bg-blue-50 disabled:opacity-50 disabled:hover:text-gray-600 disabled:hover:bg-white transition-colors shadow-sm cursor-pointer" title="Переименовать выбранное">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path></svg>
      </button>
      <button onclick={openDeleteModal} disabled={!activeId} class="flex-1 flex justify-center items-center bg-white border border-gray-300 rounded p-1.5 text-red-500 hover:text-red-700 hover:bg-red-50 disabled:opacity-50 disabled:hover:text-red-500 disabled:hover:bg-white transition-colors shadow-sm cursor-pointer" title="Удалить выбранное">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path><line x1="10" y1="11" x2="10" y2="17"></line><line x1="14" y1="11" x2="14" y2="17"></line></svg>
      </button>
    </div>
    {#if activeFolderId || activeId}
      <button onclick={() => { activeId = null; activeFolderId = null; activeName = ""; }} class="text-[11px] text-gray-500 hover:text-gray-800 text-left cursor-pointer transition-colors mt-0.5 flex items-center gap-1">
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>
        Сбросить выделение (в корень)
      </button>
    {/if}
  </div>
  
  <div class="flex-1 overflow-y-auto p-2 text-sm text-gray-700" onclick={handleContainerClick} role="presentation" aria-busy={isRefreshing}>
    {#snippet treeNode(node)}
      <li class="py-0.5">
        <button type="button" class="w-full flex items-center gap-1 cursor-pointer hover:bg-gray-200 p-1.5 rounded transition-colors text-left {activeId === node.id ? 'bg-blue-100 text-blue-800' : ''}" 
             onclick={(e) => handleSelect(node, e)}>
          {#if node.type === 'dir'}
            <svg class="w-4 h-4 text-blue-500 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              {#if node.open}
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path>
              {:else}
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"></path>
              {/if}
            </svg>
            <svg class="w-4 h-4 text-blue-400 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"></path></svg>
          {:else}
            <svg class="w-4 h-4 text-gray-400 ml-1 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"></path></svg>
          {/if}
          <span class="select-none truncate" title={node.name}>{node.name}</span>
        </button>
        
        {#if node.type === 'dir' && node.open && node.children}
          <ul class="ml-4 border-l border-gray-200 pl-1 mt-0.5">
            {#each node.children as child}
              {@render treeNode(child)}
            {/each}
          </ul>
        {/if}
      </li>
    {/snippet}

    <ul class="min-h-full" onclick={handleContainerClick} role="presentation">
      {#each nodes as node}
        {@render treeNode(node)}
      {/each}
    </ul>
  </div>
</aside>

<Modal 
  show={showCreateModal} 
  title={`Создать ${createType === 'dir' ? 'папку' : 'файл'} ${activeFolderId ? 'в текущей папке' : 'в корне'}`}
  input={true}
  bind:inputValue={createName}
  error={createError}
  busy={mutationBusy}
  confirmDisabled={!createName.trim()}
  confirmText="Создать"
  onConfirm={confirmCreate}
  onCancel={() => showCreateModal = false}
/>

<Modal 
  show={showRenameModal} 
  title="Переименовать"
  input={true}
  bind:inputValue={renameValue}
  error={renameError}
  busy={mutationBusy}
  confirmDisabled={!renameValue.trim() || renameValue === activeName}
  confirmText="Сохранить"
  onConfirm={confirmRename}
  onCancel={() => showRenameModal = false}
/>

<Modal 
  show={showDeleteModal} 
  title="Удалить выбранный элемент?"
  confirmText="Удалить"
  error={deleteError}
  busy={mutationBusy}
  danger={true}
  onConfirm={confirmDelete}
  onCancel={() => showDeleteModal = false}
/>
