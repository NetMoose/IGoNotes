<script>
  import { onDestroy } from 'svelte';
  import { captureActiveTab } from '../capture/browser.js';
  import { CaptureError } from '../capture/errors.js';

  let { capture = captureActiveTab } = $props();
  let loading = $state(false);
  let preview = $state(null);
  let error = $state('');
  let disposed = false;
  onDestroy(() => { disposed = true; });

  async function extract(mode) {
    if (loading) return;
    loading = true;
    preview = null;
    error = '';
    try {
      const result = await capture(mode);
      if (!disposed) preview = { title: result.title, count: result.text.length };
    } catch (failure) {
      if (!disposed) error = new CaptureError(failure instanceof CaptureError ? failure.code : 'capture_failed').message;
    } finally { if (!disposed) loading = false; }
  }
</script>

<main class="popup" aria-busy={loading}>
  <h1>IGoNotes Web Clipper</h1>
  <p>Extract a preview from the current page.</p>
  <button disabled={loading} onclick={() => extract('article')}>Article</button>
  <button disabled={loading} onclick={() => extract('selection')}>Selection</button>
  <p>Not saved: preview only. Draft creation arrives in task 7.3; closing this popup loses the result.</p>
  <div role="status">
    {#if loading}Extracting…{:else if preview}<p>{preview.title}</p><p>{preview.count} text characters extracted.</p>{/if}
  </div>
  {#if error}<p role="alert">{error}</p>{/if}
  <a href="./editor.html" target="_blank" rel="noopener">Open draft editor</a>
</main>
