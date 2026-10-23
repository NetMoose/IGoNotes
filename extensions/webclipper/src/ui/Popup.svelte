<script>
  import { onDestroy } from 'svelte';
  import { captureMessage, captureResponse, captureFailureMessage, manualOpenDraftEditor } from '../capture/messages.js';
  import { CaptureError } from '../capture/errors.js';
  import { downloadMarkdown } from './markdown-download.js';

  let { capture = captureMessage, openEditor = manualOpenDraftEditor, download = downloadMarkdown } = $props();
  let loading = $state(false);
  let saved = $state(null);
  let unsaved = $state(null);
  let error = $state('');
  let disposed = false;
  onDestroy(() => { disposed = true; });

  async function extract(mode) {
    if (loading) return;
    loading = true;
    saved = null;
    unsaved = null;
    error = '';
    try {
      const result = captureResponse(await capture(mode));
      if (disposed) return;
      if (result.ok) {
        saved = result;
        if (!result.editorOpened) error = 'Saved, but the editor could not open. Open the saved draft below; capturing again creates another draft.';
      } else {
        unsaved = result.unsaved ?? null;
        error = captureFailureMessage(result.code);
      }
    } catch (failure) {
      if (!disposed) error = new CaptureError(failure instanceof CaptureError ? failure.code : 'capture_failed').message;
    } finally { if (!disposed) loading = false; }
  }
  async function openSaved() {
    if (loading || !saved) return;
    loading = true;
    try {
      await openEditor(saved.draftId);
      if (!disposed) { saved = { ...saved, editorOpened: true }; error = ''; }
    } catch { if (!disposed) error = 'The editor could not open. Your draft is still saved locally. Try opening it again.'; }
    finally { if (!disposed) loading = false; }
  }
  function downloadCurrent() {
    try { download(unsaved.markdown, 'clip.md'); }
    catch { error = 'Download failed. Keep this popup open and try downloading again.'; }
  }
</script>

<main class="popup" aria-busy={loading}>
  <h1>IGoNotes Web Clipper</h1>
  <p>Capture the current page as a local Markdown draft.</p>
  <button disabled={loading || !!unsaved} onclick={() => extract('article')}>Article</button>
  <button disabled={loading || !!unsaved} onclick={() => extract('selection')}>Selection</button>
  <div role="status">
    {#if loading}Capturing…{:else if saved}<p>Saved locally{saved.editorOpened ? ' · editor opened' : ''}. You can close this popup.</p>{/if}
  </div>
  {#if error}<p role="alert">{error}</p>{/if}
  {#if saved && !saved.editorOpened}<button disabled={loading} onclick={openSaved}>Open saved draft</button>{/if}
  {#if unsaved}
    <p>Not saved: closing this popup loses the current material. Download it before closing or capturing again.</p>
    <p>{unsaved.title}</p>
    <button onclick={downloadCurrent}>Download current Markdown</button>
    <p>Download contains the Markdown body only, without metadata or image files.</p>
  {/if}
  <a href="./editor.html" target="_blank" rel="noopener">Open draft editor</a>
</main>
