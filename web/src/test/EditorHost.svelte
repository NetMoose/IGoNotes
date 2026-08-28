<script>
  import Editor from '../lib/Editor.svelte'

  let content = $state('')
</script>

<Editor noteId="note.md" bind:content />
<output aria-label="Markdown content">{content}</output>
