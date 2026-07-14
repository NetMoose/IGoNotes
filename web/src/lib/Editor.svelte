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
  <div class="flex border-b border-gray-200 bg-gray-50 p-2 gap-2">
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
