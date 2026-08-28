<script>
  let { content = $bindable('') } = $props()
</script>

<label>
  Markdown
  <textarea bind:value={content}></textarea>
</label>
