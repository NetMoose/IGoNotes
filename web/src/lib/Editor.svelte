<script>
  import { onMount, onDestroy } from 'svelte';
  import { EditorState } from '@codemirror/state';
  import { EditorView, keymap } from '@codemirror/view';
  import { defaultKeymap } from '@codemirror/commands';
  import { markdown } from '@codemirror/lang-markdown';
  import { syntaxTree } from '@codemirror/language';
  import { marked } from 'marked';

  let { content = $bindable() } = $props();
  
  let editorContainer;
  let editorView;
  let mode = $state('edit');
  let fileInput = $state();

  async function uploadImage(file, pos) {
    if (!file.type.startsWith('image/')) return;
    
    const placeholder = `![[Загрузка...]]`;
    editorView.dispatch({
      changes: { from: pos, insert: placeholder }
    });

    const formData = new FormData();
    formData.append('file', file);

    try {
      const res = await fetch('/api/assets', {
        method: 'POST',
        body: formData
      });
      if (!res.ok) throw new Error('Upload failed');
      const data = await res.json();
      
      const docStr = editorView.state.doc.toString();
      const searchIndex = docStr.indexOf(placeholder);
      if (searchIndex !== -1) {
        editorView.dispatch({
          changes: {
            from: searchIndex,
            to: searchIndex + placeholder.length,
            insert: `![[${data.path}]]`
          }
        });
      }
    } catch (err) {
      console.error(err);
      const docStr = editorView.state.doc.toString();
      const searchIndex = docStr.indexOf(placeholder);
      if (searchIndex !== -1) {
        editorView.dispatch({
          changes: {
            from: searchIndex,
            to: searchIndex + placeholder.length,
            insert: `![[Ошибка загрузки]]`
          }
        });
      }
    }
  }

  function handleToolbarImage() {
    fileInput.click();
  }

  function handleFileInput(e) {
    const file = e.target.files?.[0];
    if (file && editorView) {
      const pos = editorView.state.selection.main.head;
      uploadImage(file, pos);
    }
    e.target.value = '';
  }

  onMount(() => {
    const state = EditorState.create({
      doc: content,
      extensions: [
        keymap.of(defaultKeymap),
        markdown(),
        EditorView.updateListener.of((update) => {
          if (update.docChanged) {
            content = update.state.doc.toString();
          }
        }),
        EditorView.theme({
          "&": { height: "100%", fontSize: "15px" },
          ".cm-scroller": { overflow: "auto" }
        }),
        EditorView.domEventHandlers({
          paste(e, view) {
            if (e.clipboardData && e.clipboardData.files && e.clipboardData.files.length > 0) {
              const file = e.clipboardData.files[0];
              if (file.type.startsWith('image/')) {
                e.preventDefault();
                uploadImage(file, view.state.selection.main.head);
                return true;
              }
            }
            return false;
          },
          drop(e, view) {
            if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length > 0) {
              const file = e.dataTransfer.files[0];
              if (file.type.startsWith('image/')) {
                e.preventDefault();
                const pos = view.posAtCoords({x: e.clientX, y: e.clientY});
                if (pos !== null) {
                  uploadImage(file, pos);
                }
                return true;
              }
            }
            return false;
          }
        })
      ]
    });

    editorView = new EditorView({
      state,
      parent: editorContainer
    });
  });

  $effect(() => {
    if (editorView && content !== editorView.state.doc.toString()) {
       editorView.dispatch({
         changes: {from: 0, to: editorView.state.doc.length, insert: content}
       });
    }
  });

  onDestroy(() => {
    if (editorView) editorView.destroy();
  });

  function renderMarkdown(md) {
    if (!md) return "";
    
    // Преобразуем Obsidian-стиль ссылок на изображения ![[...]] в стандартный Markdown ![...](/api/raw?path=...)
    // Регулярка учитывает возможные опечатки (отсутствие закрывающих ]])
    let processedMd = md.replace(/!\[\[([^\]\n]+)(?:\]\])?/g, (match, p1) => {
      let filename = p1.trim();
      let parts = filename.split('|');
      let urlPath = encodeURIComponent(parts[0].trim());
      return `![${parts[0].trim()}](/api/raw?path=${urlPath})`;
    });

    let html = marked(processedMd);
    
    // Перехватываем стандартные относительные Markdown-картинки ![alt](image.png)
    html = html.replace(/<img([^>]*)src="([^"]+)"([^>]*)>/g, (match, before, src, after) => {
      if (!src.startsWith('http') && !src.startsWith('data:') && !src.startsWith('/api/raw')) {
        return `<img${before}src="/api/raw?path=${encodeURIComponent(src)}"${after}>`;
      }
      return match;
    });

    // Убираем атрибут disabled у чекбоксов, чтобы они стали кликабельными
    html = html.replace(/<input([^>]*)disabled([^>]*)>/g, '<input$1$2>');
    // Все ссылки в превью открываем в новой вкладке
    html = html.replace(/<a /g, '<a target="_blank" rel="noopener noreferrer" ');
    return html;
  }

  function handlePreviewClick(e) {
    if (e.target.tagName === 'INPUT' && e.target.type === 'checkbox') {
      const checkboxes = Array.from(e.currentTarget.querySelectorAll('input[type="checkbox"]'));
      const index = checkboxes.indexOf(e.target);
      
      if (index !== -1 && editorView) {
        let currentIdx = 0;
        let found = false;
        
        // Используем AST от CodeMirror для надежного поиска чекбокса
        syntaxTree(editorView.state).iterate({
          enter(node) {
            if (node.name === "TaskMarker") {
              if (currentIdx === index) {
                const from = node.from;
                const to = node.to;
                const text = editorView.state.doc.sliceString(from, to);
                // Инвертируем состояние (в тексте маркера)
                const newVal = text.toLowerCase().includes('x') ? '[ ]' : '[x]';
                
                // Диспатчим изменения в редактор, что автоматически обновит 'content'
                editorView.dispatch({
                  changes: {from, to, insert: newVal}
                });
                found = true;
                return false; // Останавливаем итерацию
              }
              currentIdx++;
            }
          }
        });
        
        // Fallback-вариант на случай рассинхронизации парсеров
        if (!found) {
          console.warn("Чекбокс не найден в AST CodeMirror. Попытка текстового поиска...");
          let lineIdx = 0;
          let inCodeBlock = false;
          const lines = content.split('\n');
          
          for (let i = 0; i < lines.length; i++) {
            const line = lines[i];
            if (line.trim().startsWith('```')) {
              inCodeBlock = !inCodeBlock;
              continue;
            }
            if (!inCodeBlock) {
              const match = line.match(/^([ \t]*[-*+]\s+)\[([ xX])\]/);
              if (match) {
                if (lineIdx === index) {
                  const newVal = match[2] === ' ' ? 'x' : ' ';
                  lines[i] = line.replace(/^([ \t]*[-*+]\s+)\[([ xX])\]/, `$1[${newVal}]`);
                  content = lines.join('\n');
                  break;
                }
                lineIdx++;
              }
            }
          }
        }
      }
    }
  }
</script>

<div class="flex flex-col h-full bg-white relative w-full">
  <div class="flex border-b border-gray-200 bg-gray-50 p-2 gap-2 justify-between items-center">
    <div class="flex gap-2">
      <button 
        class="px-4 py-1.5 text-sm font-medium rounded-md transition-colors {mode === 'edit' ? 'bg-blue-100 text-blue-700 shadow-sm cursor-default' : 'text-gray-600 hover:bg-gray-200 cursor-pointer'}"
        onclick={() => mode = 'edit'}>
        Редактор
      </button>
      <button 
        class="px-4 py-1.5 text-sm font-medium rounded-md transition-colors {mode === 'preview' ? 'bg-blue-100 text-blue-700 shadow-sm cursor-default' : 'text-gray-600 hover:bg-gray-200 cursor-pointer'}"
        onclick={() => mode = 'preview'}>
        Превью
      </button>
    </div>
    
    {#if mode === 'edit'}
      <div class="flex items-center">
        <input 
          type="file" 
          accept="image/*" 
          class="hidden" 
          bind:this={fileInput} 
          onchange={handleFileInput} 
        />
        <button 
          class="p-1.5 text-gray-500 hover:text-blue-600 hover:bg-blue-50 rounded-md transition-colors"
          title="Вставить картинку"
          onclick={handleToolbarImage}>
          <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect>
            <circle cx="8.5" cy="8.5" r="1.5"></circle>
            <polyline points="21 15 16 10 5 21"></polyline>
          </svg>
        </button>
      </div>
    {/if}
  </div>

  <div class="flex-1 overflow-hidden relative">
    <div 
      bind:this={editorContainer} 
      class="h-full w-full absolute inset-0 {mode === 'edit' ? 'block' : 'hidden'}"
    ></div>
    
    {#if mode === 'preview'}
      <div class="absolute inset-0 overflow-y-auto p-6 bg-white" onclick={handlePreviewClick} role="presentation">
        <article class="prose max-w-4xl mx-auto">
          {@html renderMarkdown(content)}
        </article>
      </div>
    {/if}
  </div>
</div>
