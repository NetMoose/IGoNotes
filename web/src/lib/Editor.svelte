<script>
  import { onMount, onDestroy } from 'svelte';
  import { EditorState, EditorSelection } from '@codemirror/state';
  import { EditorView, keymap } from '@codemirror/view';
  import { defaultKeymap } from '@codemirror/commands';
  import { markdown } from '@codemirror/lang-markdown';
  import { languages } from '@codemirror/language-data';
  import { syntaxTree } from '@codemirror/language';
  import { marked } from 'marked';
  import { markedHighlight } from 'marked-highlight';
  import hljs from 'highlight.js';

  marked.use(markedHighlight({
    langPrefix: 'hljs language-',
    highlight(code, lang) {
      const language = hljs.getLanguage(lang) ? lang : 'plaintext';
      return hljs.highlight(code, { language }).value;
    }
  }));

  let { content = $bindable() } = $props();
  
  let editorContainer;
  let editorView;
  let mode = $state('edit');
  let fileInput = $state();
  let showHeadingMenu = $state(false);
  let showCodeMenu = $state(false);

  const codeLanguages = [
    { label: 'Без подсветки', val: '' },
    { label: 'Bash', val: 'bash' },
    { label: 'JSON', val: 'json' },
    { label: 'JavaScript', val: 'javascript' },
    { label: 'Go', val: 'go' },
    { label: 'Python', val: 'python' },
    { label: 'HTML', val: 'html' },
    { label: 'CSS', val: 'css' },
    { label: 'SQL', val: 'sql' },
  ];

  function insertCodeBlock(lang) {
    const prefix = '```' + lang + '\n';
    const suffix = '\n```';
    applyFormat(prefix, suffix);
    showCodeMenu = false;
  }

  function insertHeading(level) {
    const prefix = '#'.repeat(level) + ' ';
    applyFormat(prefix, '');
    showHeadingMenu = false;
  }

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

  function applyFormat(prefix, suffix = '') {
    if (!editorView) return;
    
    const tr = editorView.state.update(editorView.state.changeByRange(range => {
      const selected = editorView.state.sliceDoc(range.from, range.to);
      const text = prefix + selected + suffix;
      
      let newFrom = range.from + prefix.length;
      let newTo = newFrom + selected.length;
      
      if (suffix === '' && selected === '') {
        newFrom = newTo = range.from + prefix.length;
      }
      
      return {
        changes: {from: range.from, to: range.to, insert: text},
        range: EditorSelection.range(newFrom, newTo)
      };
    }));
    
    editorView.dispatch(tr);
    editorView.focus();
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
        markdown({ codeLanguages: languages }),
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
    
    // Выделяем Frontmatter (YAML метаданные)
    let frontmatterHtml = "";
    let markdownContent = md;
    const frontmatterRegex = /^---\r?\n([\s\S]*?)\r?\n---\r?\n/;
    const match = md.match(frontmatterRegex);
    
    if (match) {
      const frontmatterText = match[1].trim();
      markdownContent = md.replace(frontmatterRegex, "");
      
      frontmatterHtml = `
        <details class="bg-gray-50 border border-gray-200 rounded-md mb-4 group">
          <summary class="cursor-pointer px-3 py-2 font-medium text-sm text-gray-600 hover:text-gray-800 hover:bg-gray-100 transition-colors list-none flex items-center justify-between">
            <span>Свойства (Frontmatter)</span>
            <svg class="w-4 h-4 text-gray-400 group-open:rotate-180 transition-transform" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path></svg>
          </summary>
          <div class="p-3 border-t border-gray-200 overflow-x-auto text-xs text-gray-700 bg-white rounded-b-md">
            <pre class="m-0 font-mono text-[11px] leading-relaxed text-gray-600">${frontmatterText}</pre>
          </div>
        </details>
      `;
    }
    
    // Преобразуем Obsidian-стиль ссылок на изображения ![[...]] в стандартный Markdown ![...](/api/raw?path=...)
    // Регулярка учитывает возможные опечатки (отсутствие закрывающих ]])
    let processedMd = markdownContent.replace(/!\[\[([^\]\n]+)(?:\]\])?/g, (match, p1) => {
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
    
    return frontmatterHtml + html;
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

<svelte:window onclick={() => { showHeadingMenu = false; showCodeMenu = false; }} />

<div class="flex flex-col h-full bg-white relative w-full">
  <div class="flex border-b border-gray-200 bg-gray-50 p-1.5 gap-2 justify-between items-center h-12 shrink-0">
    <!-- Левая часть: форматирование Markdown -->
    <div class="flex items-center gap-0.5">
      {#if mode === 'edit'}
        <button class="p-1.5 text-gray-500 hover:text-blue-600 hover:bg-blue-50 rounded-md transition-colors" title="Жирный" onclick={() => applyFormat('**', '**')}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 4h8a4 4 0 0 1 4 4 4 4 0 0 1-4 4H6z"></path><path d="M6 12h9a4 4 0 0 1 4 4 4 4 0 0 1-4 4H6z"></path></svg>
        </button>
        <button class="p-1.5 text-gray-500 hover:text-blue-600 hover:bg-blue-50 rounded-md transition-colors" title="Курсив" onclick={() => applyFormat('*', '*')}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="19" y1="4" x2="10" y2="4"></line><line x1="14" y1="20" x2="5" y2="20"></line><line x1="15" y1="4" x2="9" y2="20"></line></svg>
        </button>
        <button class="p-1.5 text-gray-500 hover:text-blue-600 hover:bg-blue-50 rounded-md transition-colors" title="Зачеркнутый" onclick={() => applyFormat('~~', '~~')}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="5" y1="12" x2="19" y2="12"></line><path d="M16 6C16 6 14.5 4 12 4C9.5 4 8 6 8 6"></path><path d="M8 18C8 18 9.5 20 12 20C14.5 20 16 18 16 18"></path></svg>
        </button>
        <div class="w-px h-4 bg-gray-300 mx-1"></div>
        <div class="relative">
          <button 
            class="p-1.5 rounded-md transition-colors {showHeadingMenu ? 'bg-blue-50 text-blue-600' : 'text-gray-500 hover:text-blue-600 hover:bg-blue-50'}" 
            title="Заголовок" 
            onclick={(e) => { e.stopPropagation(); showHeadingMenu = !showHeadingMenu; }}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12h16M4 18V6M20 18V6"></path></svg>
          </button>
          
          {#if showHeadingMenu}
            <div class="absolute top-full left-0 mt-1 bg-white border border-gray-200 shadow-lg rounded-md py-1 z-10 w-32 flex flex-col">
              {#each [1, 2, 3, 4, 5] as level}
                <button 
                  class="text-left px-3 py-1.5 text-sm hover:bg-blue-50 text-gray-700 transition-colors"
                  onclick={() => insertHeading(level)}>
                  H{level} Заголовок
                </button>
              {/each}
            </div>
          {/if}
        </div>
        <button class="p-1.5 text-gray-500 hover:text-blue-600 hover:bg-blue-50 rounded-md transition-colors" title="Цитата" onclick={() => applyFormat('> ', '')}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 21c3 0 7-1 7-8V5c0-1.25-.756-2.017-2-2H4c-1.25 0-2 .75-2 1.972V11c0 1.25.75 2 2 2 1 0 1 0 1 1v1c0 1-1 2-2 2s-1 .008-1 1.031V20c0 1 0 1 1 1z"></path><path d="M15 21c3 0 7-1 7-8V5c0-1.25-.757-2.017-2-2h-4c-1.25 0-2 .75-2 1.972V11c0 1.25.75 2 2 2h.75c0 2.25.25 4-2.75 4v3c0 1 0 1 1 1z"></path></svg>
        </button>
        <div class="relative">
          <button 
            class="p-1.5 rounded-md transition-colors {showCodeMenu ? 'bg-blue-50 text-blue-600' : 'text-gray-500 hover:text-blue-600 hover:bg-blue-50'}" 
            title="Блок кода" 
            onclick={(e) => { e.stopPropagation(); showCodeMenu = !showCodeMenu; }}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="16 18 22 12 16 6"></polyline><polyline points="8 6 2 12 8 18"></polyline></svg>
          </button>
          
          {#if showCodeMenu}
            <div class="absolute top-full left-0 mt-1 bg-white border border-gray-200 shadow-lg rounded-md py-1 z-10 w-40 flex flex-col">
              {#each codeLanguages as lang}
                <button 
                  class="text-left px-3 py-1.5 text-sm hover:bg-blue-50 text-gray-700 transition-colors"
                  onclick={() => insertCodeBlock(lang.val)}>
                  {lang.label}
                </button>
              {/each}
            </div>
          {/if}
        </div>
        <button class="p-1.5 text-gray-500 hover:text-blue-600 hover:bg-blue-50 rounded-md transition-colors" title="Инлайн код" onclick={() => applyFormat('`', '`')}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 17 10 11 4 5"></polyline><line x1="12" y1="19" x2="20" y2="19"></line></svg>
        </button>
        <div class="w-px h-4 bg-gray-300 mx-1"></div>
        <button class="p-1.5 text-gray-500 hover:text-blue-600 hover:bg-blue-50 rounded-md transition-colors" title="Список" onclick={() => applyFormat('- ', '')}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="8" y1="6" x2="21" y2="6"></line><line x1="8" y1="12" x2="21" y2="12"></line><line x1="8" y1="18" x2="21" y2="18"></line><line x1="3" y1="6" x2="3.01" y2="6"></line><line x1="3" y1="12" x2="3.01" y2="12"></line><line x1="3" y1="18" x2="3.01" y2="18"></line></svg>
        </button>
        <button class="p-1.5 text-gray-500 hover:text-blue-600 hover:bg-blue-50 rounded-md transition-colors" title="Чекбокс" onclick={() => applyFormat('- [ ] ', '')}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 11 12 14 22 4"></polyline><path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"></path></svg>
        </button>
        <div class="w-px h-4 bg-gray-300 mx-1"></div>
        <button class="p-1.5 text-gray-500 hover:text-blue-600 hover:bg-blue-50 rounded-md transition-colors" title="Ссылка" onclick={() => applyFormat('[', '](url)')}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"></path><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"></path></svg>
        </button>
        <input type="file" accept="image/*" class="hidden" bind:this={fileInput} onchange={handleFileInput} />
        <button class="p-1.5 text-gray-500 hover:text-blue-600 hover:bg-blue-50 rounded-md transition-colors" title="Вставить картинку" onclick={handleToolbarImage}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect><circle cx="8.5" cy="8.5" r="1.5"></circle><polyline points="21 15 16 10 5 21"></polyline>
          </svg>
        </button>
      {/if}
    </div>

    <!-- Правая часть: переключатели режима -->
    <div class="flex gap-1 bg-gray-200/50 p-0.5 rounded-lg border border-gray-200 ml-auto">
      <button 
        class="p-1.5 rounded-md transition-colors {mode === 'edit' ? 'bg-white text-blue-700 shadow-sm cursor-default' : 'text-gray-500 hover:text-gray-800 cursor-pointer'}"
        title="Режим редактора"
        onclick={() => mode = 'edit'}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"></path><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"></path></svg>
      </button>
      <button 
        class="p-1.5 rounded-md transition-colors {mode === 'preview' ? 'bg-white text-blue-700 shadow-sm cursor-default' : 'text-gray-500 hover:text-gray-800 cursor-pointer'}"
        title="Предварительный просмотр"
        onclick={() => mode = 'preview'}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path><circle cx="12" cy="12" r="3"></circle></svg>
      </button>
    </div>
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
