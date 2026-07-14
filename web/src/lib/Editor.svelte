<script>
  import { onMount, onDestroy } from 'svelte';
  import { EditorState } from '@codemirror/state';
  import { EditorView, keymap } from '@codemirror/view';
  import { defaultKeymap } from '@codemirror/commands';
  import { markdown } from '@codemirror/lang-markdown';
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
    let html = marked(md);
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
      
      if (index !== -1) {
        let currentIndex = 0;
        // Регулярка ищет элементы списка задач, учитывая отступы
        const regex = /^([ \t]*[-*+]\s+)\[([ xX])\]/gm;
        
        content = content.replace(regex, (match, p1, p2) => {
          if (currentIndex === index) {
            const newVal = (p2 === ' ' ? 'x' : ' ');
            currentIndex++;
            return `${p1}[${newVal}]`;
          }
          currentIndex++;
          return match;
        });
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
