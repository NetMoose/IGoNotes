<script>
  import NotesWorkspace from '../lib/NotesWorkspace.svelte'

  let content = $state('# Parent')
</script>

<NotesWorkspace
  activeNote={{ id: 'note.md', name: 'note.md' }}
  bind:content
  saveStatus="idle"
  basePath="/notes/work"
  error=""
  onSelectNote={() => {}}
  onRenameNote={() => {}}
  onDeleteNote={() => {}}
  onSave={() => {}}
  onOpenSettings={() => {}}
/>

<output aria-label="Parent markdown">{content}</output>
