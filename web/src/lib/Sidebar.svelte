<script>
  import { onMount } from 'svelte';
  import Modal from './Modal.svelte';

  let { onSelect, onDelete } = $props();

  let nodes = $state([]);
  let activeId = $state(null);
  let activeName = $state(""); // имя выбранного узла для переименования
  let activeFolderId = $state(null); // корень по умолчанию (null вместо "")
  let isRefreshing = $state(false);
  
  // Храним ID открытых папок для восстановления после обновления дерева
  let openFolders = new Set();

  // Modal states
  let showCreateModal = $state(false);
  let createType = $state("");
  let createName = $state("");
  let createError = $state("");

  let showDeleteModal = $state(false);

  let showRenameModal = $state(false);
  let renameValue = $state("");
  
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

  async function loadTree() {
    isRefreshing = true;
    try {
      const res = await fetch('/api/notes');
      if (res.ok) {
        const newNodes = await res.json();
        
        // Убедимся, что текущая активная папка (куда добавляем файлы) тоже будет открыта
        if (activeFolderId) {
          openFolders.add(activeFolderId);
        }
        
        restoreOpenState(newNodes);
        nodes = newNodes;
      }
    } catch (err) {
      console.error("Ошибка при загрузке дерева заметок:", err);
    } finally {
      // Искусственная задержка для визуального эффекта спиннера, если ответ пришел мгновенно
      setTimeout(() => isRefreshing = false, 300);
    }
  }

  onMount(() => {
    loadTree();
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
    showRenameModal = true;
  }

  async function confirmCreate() {
    if (!createName.trim()) return;
    createError = "";
    
    try {
      const res = await fetch('/api/notes', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          parent_id: activeFolderId || "",
          name: createName.trim(),
          type: createType
        })
      });

      if (res.ok) {
        showCreateModal = false;
        loadTree();
      } else if (res.status === 409) {
        createError = createType === 'dir' 
          ? `Папка "${createName.trim()}" уже существует.` 
          : `Файл "${createName.trim()}" уже существует.`;
      }
    } catch (err) {
      console.error(err);
      createError = "Произошла ошибка при создании";
    }
  }

  async function confirmRename() {
    if (!renameValue.trim() || renameValue === activeName) {
      showRenameModal = false;
      return;
    }
    
    try {
      const res = await fetch('/api/rename', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          id: activeId,
          new_name: renameValue.trim()
        })
      });
      if (res.ok) {
        showRenameModal = false;
        
        // Сбросим текущее выделение, так как ID поменялся
        // Можно также было бы попытаться обновить activeId, но сброс проще и надежнее
        if (onDelete) onDelete(activeId); // это очистит редактор в App.svelte
        activeId = null;
        activeName = "";
        
        loadTree();
      }
    } catch (err) {
      console.error(err);
    }
  }

  async function confirmDelete() {
    if (!activeId) return;

    try {
      const res = await fetch(`/api/note?id=${encodeURIComponent(activeId)}`, {
        method: 'DELETE'
      });
      if (res.ok) {
        if (onDelete) onDelete(activeId);
        // Если удаляем папку, её тоже надо удалить из списка открытых
        openFolders.delete(activeId);
        activeId = null;
        activeFolderId = null; // Сбрасываем активную папку на корень
        activeName = "";
        showDeleteModal = false;
        loadTree();
      }
    } catch (err) {
      console.error(err);
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

<aside class="w-72 bg-gray-50 border-r border-gray-200 flex flex-col h-screen shrink-0">
  <div class="p-3 border-b border-gray-200 bg-gray-100 flex flex-col gap-2">
    <div class="flex justify-between items-center">
      <h2 class="font-semibold text-gray-700 text-sm uppercase tracking-wider">База заметок</h2>
      <button onclick={loadTree} class="text-gray-400 hover:text-blue-500 transition-colors p-1 cursor-pointer" title="Обновить дерево">
        <svg class="w-4 h-4 {isRefreshing ? 'animate-spin' : ''}" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"></path></svg>
      </button>
    </div>
    <div class="flex gap-1">
      <button onclick={() => openCreateModal('file')} class="flex-1 text-xs bg-white border border-gray-300 rounded py-1.5 hover:bg-gray-50 transition-colors shadow-sm cursor-pointer">📄 Файл</button>
      <button onclick={() => openCreateModal('dir')} class="flex-1 text-xs bg-white border border-gray-300 rounded py-1.5 hover:bg-gray-50 transition-colors shadow-sm cursor-pointer">📁 Папка</button>
    </div>
    <div class="flex gap-1">
      <button onclick={openRenameModal} disabled={!activeId} class="flex-1 text-xs bg-white border border-gray-300 rounded py-1.5 hover:bg-gray-50 disabled:opacity-50 disabled:hover:bg-white transition-colors shadow-sm cursor-pointer">✏️ Переим.</button>
      <button onclick={() => showDeleteModal = true} disabled={!activeId} class="flex-1 text-xs bg-white border border-red-200 rounded py-1.5 text-red-600 hover:bg-red-50 disabled:opacity-50 disabled:hover:bg-white transition-colors shadow-sm cursor-pointer">🗑️ Удал.</button>
    </div>
    {#if activeFolderId || activeId}
      <button onclick={() => { activeId = null; activeFolderId = null; activeName = ""; }} class="text-[11px] text-gray-500 hover:text-gray-800 text-left cursor-pointer transition-colors mt-1">
        Сбросить выделение (в корень) ✕
      </button>
    {/if}
  </div>
  
  <div class="flex-1 overflow-y-auto p-2 text-sm text-gray-700" onclick={handleContainerClick} role="presentation">
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
  confirmText="Создать"
  onConfirm={confirmCreate}
  onCancel={() => showCreateModal = false}
/>

<Modal 
  show={showRenameModal} 
  title="Переименовать"
  input={true}
  bind:inputValue={renameValue}
  confirmText="Сохранить"
  onConfirm={confirmRename}
  onCancel={() => showRenameModal = false}
/>

<Modal 
  show={showDeleteModal} 
  title="Удалить выбранный элемент?"
  confirmText="Удалить"
  onConfirm={confirmDelete}
  onCancel={() => showDeleteModal = false}
/>
