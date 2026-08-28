<script>
  import Editor from '../lib/Editor.svelte'

  let editor = $state()
  let noteId = $state('note.md')
  let content = $state('')
  let capturedContent = $state('')
  let transitioned = $state(false)

  async function transitionNote() {
    await editor?.flushPendingUploads?.()
    capturedContent = content
    noteId = 'next.md'
    content = '# Next note'
    transitioned = true
  }
</script>

<Editor bind:this={editor} {noteId} bind:content />
<button type="button" onclick={transitionNote}>Transition note</button>
<output aria-label="Markdown content">{content}</output>
<output aria-label="Flushed markdown">{capturedContent}</output>
<output aria-label="Transition status">{transitioned ? 'next.md' : noteId}</output>
