<script>
  import { onMount, onDestroy, untrack, tick } from 'svelte';
  import { createDraftStore } from '../storage/drafts.js';
  import { downloadMarkdown, sourceLink } from './markdown-download.js';
  import ConnectionSettings from './ConnectionSettings.svelte';
  import ImagePreparation from './ImagePreparation.svelte';
  import Delivery from './Delivery.svelte';

  let { store: suppliedStore, initialDraftId, imageAcquirer, onImageState, resultWarnings = [], deliveryOptions = {} } = $props();
  // A component owns only the handle it creates, never a caller's handle.
  const injectedStore = untrack(() => suppliedStore);
  let store = $state.raw(injectedStore ?? createDraftStore());
  let drafts = $state([]);
  let current = $state(null);
  let loading = $state(true);
  let busy = $state(false);
  let preparingImages = $state(false);
  let delivering = $state(false), imageState = $state.raw(null), deliveryResult = $state.raw(null), resultHeading = $state();
  const frozen = $derived(delivering || ['sending', 'uncertain', 'pending'].includes(current?.status));
  let saving = $state(false);
  let version = $state(0);
  let persistedVersion = $state(0);
  let writingVersion = $state(0);
  let error = $state('');
  let connectionOpen = $state(false);
  let failedAction = $state('');
  let alive = true;
  let pendingSave;
  let retryLoadId;
  const dirty = $derived(version !== persistedVersion);
  const status = $derived(
    failedAction === 'save' ? 'Changes not saved' :
    saving ? (version !== writingVersion ? 'Unsaved changes · saving earlier edits…' : 'Saving…') :
    dirty ? 'Unsaved changes' : 'Saved locally'
  );

  function report(cause, action) {
    if (!alive) return;
    error = cause?.message || 'Local storage failed. Keep your work and retry.';
    failedAction = action;
  }
  function clearError() { error = ''; failedAction = ''; }
  function edit(field, value) {
    if (frozen) return;
    current = { ...current, [field]: value };
    version += 1;
  }
  function sorted(rows) {
    return rows.toSorted((a, b) => b.updatedAt.localeCompare(a.updatedAt) || a.id.localeCompare(b.id));
  }

  // One serial drain, including edits typed while a transaction is pending.
  // saveDraft resolves on IDB transaction completion, never request success.
  function flush() {
    if (pendingSave) return pendingSave;
    if (!current || !dirty) return Promise.resolve(true);
    clearError();
    saving = true;
    pendingSave = (async () => {
      try {
        while (version !== persistedVersion) {
          const snapshot = $state.snapshot(current);
          const snapshotVersion = version;
          if (alive) writingVersion = snapshotVersion;
          const saved = await store.saveDraft(snapshot, { requireExisting: true, expectedUpdatedAt: snapshot.updatedAt });
          // Even on teardown, drain already-requested edits before closing our handle.
          persistedVersion = snapshotVersion;
          if (alive) {
            current = { ...current, createdAt: saved.createdAt, updatedAt: saved.updatedAt };
            drafts = sorted(drafts.map(row => row.id === saved.id ? saved : row));
          }
        }
        return true;
      } catch (cause) {
        report(cause, 'save');
        return false;
      } finally {
        pendingSave = undefined;
        if (alive) saving = false;
      }
    })();
    return pendingSave;
  }

  async function load(id) {
    retryLoadId = id;
    loading = true;
    clearError();
    try {
      const rows = sorted(await store.listDrafts());
      if (!alive) return;
      drafts = rows;
      const selected = id === undefined ? rows[0]?.id : id;
      const value = selected === undefined ? null : await store.getDraft(selected);
      if (!alive) return;
      if (selected !== undefined && !value) throw new Error('Requested draft not found. Select an existing draft from the list or refresh.');
      current = value;
      version = persistedVersion = 0;
    } catch (cause) {
      report(cause, 'load');
    } finally {
      if (alive) loading = false;
    }
  }

  async function navigate(id) {
    if (busy || loading || preparingImages || delivering) return;
    busy = true;
    try {
      if (!await flush() || !alive) return;
      await load(id);
    } finally {
      if (alive) busy = false;
    }
  }

  async function remove() {
    if (!current || busy || loading || preparingImages || frozen) return;
    busy = true;
    clearError();
    try {
      // Deletion is explicit, including unsaved edits; never race a prior write.
      if (pendingSave) await pendingSave;
      if (!alive) return;
      clearError();
      await store.deleteDraft(current.id);
      if (!alive) return;
      current = null;
      version = persistedVersion = 0;
      await load(undefined);
    } catch (cause) {
      report(cause, 'delete');
    } finally {
      if (alive) busy = false;
    }
  }

  function retry() {
    if (!injectedStore && failedAction !== 'download' && !pendingSave) {
      // Failed opens and version-changed handles are terminal in the storage API.
      store.close();
      store = createDraftStore();
    }
    if (failedAction === 'save') return flush();
    if (failedAction === 'delete') return remove();
    if (failedAction === 'download') return download();
    return navigate(retryLoadId);
  }
  function download() {
    try {
      downloadMarkdown($state.snapshot(current), current.filename);
      if (failedAction === 'download') clearError();
    }
    catch (cause) { report(cause, 'download'); }
  }
  function beforeUnload(event) {
    if (dirty || saving) { event.preventDefault(); event.returnValue = ''; }
  }
  function imageChanged(value) { imageState = value; onImageState?.(value); }
  function deliveryDraft(saved) {
    current = saved; version = persistedVersion = 0;
    drafts = sorted(drafts.map(row => row.id === saved.id ? saved : row));
  }
  async function delivered(result) {
    const id = current.id;
    deliveryResult = result;
    current = null; delivering = false; retryLoadId = undefined; version = persistedVersion = 0;
    const url = new URL(window.location.href);
    if (url.searchParams.get('draft') === id) { url.searchParams.delete('draft'); window.history.replaceState(null, '', url); }
    // Show success before the list read: even a failed refresh cannot lose it.
    await tick(); resultHeading?.focus();
    try { drafts = sorted(await store.listDrafts()); } catch (cause) { report(cause, 'load'); }
  }
  onMount(() => {
    const requested = initialDraftId ?? new URLSearchParams(window.location.search).get('draft') ?? undefined;
    void load(requested);
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  });
  onDestroy(() => {
    alive = false;
    if (!injectedStore) {
      if (pendingSave) void pendingSave.finally(() => store.close());
      else store.close();
    }
  });
</script>

<main class="editor">
  <header>
    <p class="brand">IGoNotes Web Clipper</p>
    <h1>Local draft editor</h1>
    <p>Drafts stay in this browser profile. Save edits locally or download the current Markdown.</p>
    <button onclick={() => connectionOpen = !connectionOpen} aria-expanded={connectionOpen}>
      {connectionOpen ? 'Close connection settings' : 'Connection settings'}
    </button>
  </header>
  {#if connectionOpen}<ConnectionSettings />{/if}
  <div class="draft-layout">
    <nav aria-label="Local drafts" aria-busy={loading}>
      <h2>Drafts <span class="muted">({drafts.length})</span></h2>
      <button onclick={() => navigate(current?.id ?? retryLoadId)} disabled={busy || loading || preparingImages || delivering}>Refresh drafts</button>
      <ul class="draft-list">
        {#each drafts as row (row.id)}
          <li><button aria-current={current?.id === row.id ? 'true' : undefined} disabled={busy || loading || preparingImages || delivering}
            onclick={() => navigate(row.id)}>{row.title || row.filename}</button></li>
        {/each}
      </ul>
    </nav>
    <section aria-label="Draft editor" aria-busy={loading || busy}>
      {#if loading}<p>Loading drafts…</p>{/if}
      {#if error}
        <div class="error" role="alert"><p>{error}</p>
          <button onclick={retry} disabled={busy || loading || saving}>Retry</button>
        </div>
      {/if}
      {#if deliveryResult}
        <aside aria-label="Import result" aria-live="polite">
          <h2 tabindex="-1" bind:this={resultHeading}>Import confirmed</h2>
          <p>{deliveryResult.base} / {deliveryResult.path}</p>
          <p>Receipt: {deliveryResult.import_id} · {deliveryResult.revision}</p>
          {#if deliveryResult.warnings.length}
            <p>This material is not fully offline / uses external images.</p>
            <ul>{#each deliveryResult.warnings as reason}<li>{reason}</li>{/each}</ul>
          {/if}
        </aside>
      {/if}
      {#if current}
        <div class="editor-actions">
          <p role="status" aria-live="polite">{status}</p>
          <button class="primary" onclick={flush} disabled={!dirty || saving || busy || loading || preparingImages || frozen}>Save</button>
          <button onclick={download}>Download Markdown</button>
        </div>
        <fieldset disabled={busy || loading || preparingImages || frozen}>
          <legend class="sr-only">Draft content</legend>
          <label for="draft-title">Title</label>
          <input id="draft-title" value={current.title} oninput={event => edit('title', event.currentTarget.value)} />
          <label for="draft-filename">Filename</label>
          <input id="draft-filename" value={current.filename} oninput={event => edit('filename', event.currentTarget.value)} />
          <dl class="metadata">
            <dt>Source URL</dt><dd>
              {#if sourceLink(current.sourceUrl)}
                <a href={sourceLink(current.sourceUrl)} target="_blank" rel="noopener noreferrer">{current.sourceUrl}</a>
              {:else}{current.sourceUrl}{/if}
            </dd>
            <dt>Captured at</dt><dd>{current.capturedAt}</dd>
          </dl>
          <label for="draft-markdown">Markdown</label>
          <textarea id="draft-markdown" rows="20" spellcheck="false" value={current.markdown}
            oninput={event => edit('markdown', event.currentTarget.value)}></textarea>
        </fieldset>
        <p class="muted">Manual save. Download includes current title/source/time as frontmatter and the Markdown body; image files are not included.</p>
        {#key current.id}
          <ImagePreparation {store} draftId={current.id} markdown={current.markdown} sourceUrl={current.sourceUrl}
            status={current.status} disabled={busy || loading || saving || delivering} flushSave={flush} acquirer={imageAcquirer}
            onBusy={value => preparingImages = value} onState={imageChanged} {resultWarnings} />
          {#key store}
            <Delivery {store} draft={current} {imageState} flushSave={flush} disabled={busy || loading || saving || preparingImages}
              onBusy={value => delivering = value} onDraft={deliveryDraft} onSuccess={delivered} options={deliveryOptions} />
          {/key}
        {/key}
        <button class="danger" onclick={remove} disabled={busy || loading || preparingImages || frozen}>Delete draft</button>
        <p class="muted">Delete permanently removes this draft, its unsaved edits and its stored images from this profile.</p>
      {:else if !loading && !error && drafts.length === 0}
        <p>No local drafts yet. Capture an Article or Selection from the popup.</p>
      {/if}
    </section>
  </div>
</main>
