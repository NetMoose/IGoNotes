<script module>
  let flush = () => undefined

  export function setEditorFlush(implementation = () => undefined) {
    flush = implementation
  }
</script>

<script>
  let { content = $bindable('') } = $props()

  export function flushPendingUploads() {
    return flush()
  }
</script>

<label>
  Markdown
  <textarea bind:value={content}></textarea>
</label>
