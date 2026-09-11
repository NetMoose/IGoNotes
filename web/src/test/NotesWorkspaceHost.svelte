<script>
  import NotesWorkspace from '../lib/NotesWorkspace.svelte'

  let content = $state('# Parent')
  let notesWorkspace = $state()

  function refreshTree() {
    notesWorkspace?.refreshTree?.()
  }
</script>

<NotesWorkspace
  bind:this={notesWorkspace}
  activeNote={{ id: 'note.md', name: 'note.md' }}
  bind:content
  saveStatus="idle"
  basePath="/notes/work"
  gitBase={{ name: 'work', git_url: 'https://example.test/notes.git', git_branch: 'main' }}
  gitStatus={{ base: 'work', state: 'ready', ahead: 0 }}
  gitSyncBusy={false}
  gitSyncError=""
  onGitSync={() => {}}
  error=""
  onSelectNote={() => {}}
  onRenameNote={() => {}}
  onDeleteNote={() => {}}
  onSave={() => {}}
  onOpenSettings={() => {}}
/>

<button type="button" onclick={refreshTree}>Refresh note tree</button>
<output aria-label="Parent markdown">{content}</output>
