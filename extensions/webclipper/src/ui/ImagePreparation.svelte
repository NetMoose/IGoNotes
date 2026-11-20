<script>
  import { onDestroy, untrack, tick } from 'svelte';
  import { createImageAcquirer, inspectImageBlob, normalizeImageSource } from '../images/acquire.js';
  import { scanImageReferences } from '../images/references.js';
  import { prepareDraftImages } from '../images/prepare.js';

  let { store, draftId, markdown, sourceUrl, status = 'draft', disabled = false,
    flushSave, acquirer: suppliedAcquirer, onBusy, onState, resultWarnings = [] } = $props();
  const acquirer = untrack(() => suppliedAcquirer ?? createImageAcquirer());
  let assets = $state.raw([]), verified = $state.raw(new Set());
  let selected = $state.raw([]), confirmed = $state.raw([]);
  let errors = $state.raw([]), running = $state(false), reading = $state(true), attempted = $state(false);
  let cachedMarkdown = $state(null), cachedSource = $state(null), cachedStatus = $state(null);
  let notice = $state(''), summary;
  let alive = true, generation = 0, cacheRead = 0, controller, previousInput;
  const frozen = $derived([status, cachedStatus].some(value => ['sending', 'uncertain', 'pending'].includes(value)));
  const urls = $derived([...new Set(scanImageReferences(markdown, sourceUrl).map(ref => ref.url))]);
  function key(url) { try { return normalizeImageSource(url); } catch { return url; } }
  function eligible(url) {
    try {
      normalizeImageSource(url);
      return /^https?:\/\//i.test(url) && !/[\\\p{Cc}\p{White_Space}]/u.test(url) &&
        !/\p{Cc}/u.test(decodeURIComponent(url)) && new TextEncoder().encode(url).length <= 8192;
    } catch { return false; }
  }
  const rows = $derived(urls.map(url => {
    const row = assets.find(row => key(row.normalizedUrl) === key(url));
    const descriptor = row?.descriptor;
    let policyError;
    try { normalizeImageSource(url); } catch (cause) { policyError = { code: cause.code, message: cause.message }; }
    const local = !policyError && descriptor?.status === 'ready' && verified.has(row.id);
    const error = policyError ?? descriptor?.error ??
      (!reading && descriptor?.status === 'ready' && !local ? { code: 'invalid_cache', message: 'Cached image bytes or descriptor are invalid. Retry preparation.' } : null);
    return { url, local, eligible: eligible(url), mediaType: descriptor?.mediaType, size: descriptor?.size,
      error,
      status: confirmed.some(ack => ack.url === url) ? 'External confirmed' : local ? 'Stored locally' : error || descriptor?.status === 'error' ? 'Failed' : 'Pending' };
  }));
  const preparationErrors = $derived([...errors, ...rows.filter(row => row.error).map(row => ({ url: row.url, ...row.error }))]);
  const ready = $derived(!running && !reading && !frozen && !errors.length && cachedMarkdown === markdown && cachedSource === sourceUrl &&
    rows.every(row => row.local || confirmed.some(ack => ack.url === row.url)));
  // Detached, deeply immutable handoff. 8.5 still flushes, readBundles and calls
  // buildImportSnapshot afresh before freezing/persisting an actual attempt.
  $effect(() => {
    const value = { draftId, markdown, ready, confirmedFallbacks: confirmed.map(ack => ({ ...ack })),
      warnings: confirmed.map(ack => ({ ...ack })), preparationErrors: preparationErrors.map(error => ({ ...error })) };
    for (const list of [value.confirmedFallbacks, value.warnings, value.preparationErrors]) {
      list.forEach(Object.freeze); Object.freeze(list);
    }
    untrack(() => { if (alive) onState?.(Object.freeze(value)); });
  });

  async function readCache(token) {
    const read = ++cacheRead;
    try {
      const bundle = await store.readBundle(draftId);
      if (!bundle.draft) throw Error('The draft was deleted. Preparation stopped.');
      const valid = new Set(), inspected = new Map();
      for (const row of bundle.assets) {
        if (row.descriptor.status !== 'ready') continue;
        const owner = bundle.assets.find(owner => owner.id === (row.descriptor.canonicalAssetId ?? row.id));
        if (!owner?.blob || owner.descriptor.status !== 'ready' || (owner.descriptor.canonicalAssetId ?? owner.id) !== owner.id) continue;
        try {
          let actual = inspected.get(owner.id);
          if (!actual) { actual = await inspectImageBlob(owner.blob); inspected.set(owner.id, actual); }
          if (owner.size === owner.blob.size && [row, owner].every(candidate =>
            ['size', 'sha256', 'mediaType', 'extension'].every(field => candidate.descriptor[field] === actual[field]))) valid.add(row.id);
        } catch { /* The unverified row is reported as invalid_cache below. */ }
      }
      if (!alive || token !== generation || read !== cacheRead) return;
      assets = bundle.assets; verified = valid;
      cachedMarkdown = bundle.draft.markdown; cachedSource = bundle.draft.sourceUrl; cachedStatus = bundle.draft.status;
    } catch (cause) {
      if (alive && token === generation && read === cacheRead) errors = [...errors, { code: cause.code ?? 'storage', message: cause.message }];
    } finally { if (alive && token === generation && read === cacheRead) reading = false; }
  }
  // Local reads only. Lifecycle changes also need an authoritative reread: a
  // definitive rejection can unlock the same body after a cached uncertain read.
  // Keep cachedStatus until that read succeeds so a stale parent's editable
  // status cannot override a freeze observed in another editor's persisted row.
  $effect(() => {
    const body = markdown, source = sourceUrl, id = draftId, lifecycle = status;
    untrack(() => {
      if (previousInput?.body === body && previousInput.source === source && previousInput.id === id && previousInput.lifecycle === lifecycle) return;
      previousInput = { body, source, id, lifecycle };
      controller?.abort();
      controller = undefined;
      if (running) { running = false; onBusy?.(false); }
      confirmed = []; selected = []; errors = []; notice = '';
      reading = true;
      // Invalidate earlier reads/preparation, including a late frozen snapshot
      // after rejection or a late editable snapshot after another handle freezes.
      void readCache(++generation);
    });
  });
  async function focusSummary() { await tick(); if (alive) summary?.focus(); }
  function prepare() {
    if (running || disabled || frozen) return;
    // Critical: CURRENT form URLs, on the click stack BEFORE flush/storage or
    // contains. An unsafe row must not prevent asking for other exact hosts.
    const permission = acquirer.requestAccessFromUserGesture(urls.filter(eligible));
    const token = ++generation;
    controller = new AbortController();
    const signal = controller.signal;
    running = true; reading = true; attempted = true;
    confirmed = []; selected = []; errors = []; notice = '';
    onBusy?.(true);
    void (async () => {
      try {
        const saved = await flushSave();
        await permission;
        if (!alive || token !== generation || signal.aborted) return;
        if (!saved) { errors = [{ code: 'save', message: 'Draft changes were not saved. Keep your edits and retry Save before preparing images.' }]; return; }
        const result = await prepareDraftImages(store, draftId, { acquirer, signal,
          progress: () => { if (alive && token === generation && !signal.aborted) void readCache(token); } });
        if (!alive || token !== generation || signal.aborted) return;
        errors = [result.persistenceError, result.errorStatusPersistenceError].filter(Boolean).map(error => ({ ...error }));
        await readCache(token);
        if (!alive || token !== generation || signal.aborted) return;
        notice = 'Image preparation finished. Review each image below.';
      } catch (cause) {
        if (alive && token === generation && !signal.aborted) errors = [{ code: cause.code ?? 'preparation', message: cause.message }];
      } finally {
        if (alive && token === generation) {
          running = false; reading = false; onBusy?.(false); void focusSummary();
        }
      }
    })();
  }
  function cancel() {
    controller?.abort(); ++generation;
    running = false; reading = true; onBusy?.(false);
    notice = 'Image preparation cancelled. Already stored images are retained.';
    void readCache(generation); void focusSummary();
  }
  function choose(url, checked) {
    selected = checked ? [...selected, url] : selected.filter(value => value !== url);
    confirmed = confirmed.filter(ack => ack.url !== url);
  }
  function reason(row) { return row.error?.code ? `User chose external reference (${row.error.code}).` : 'User chose external reference instead of local storage.'; }
  function confirmExternal() {
    if (running || disabled || frozen) return;
    const acknowledged = new Map(confirmed.map(ack => [ack.url, ack]));
    for (const row of rows.filter(row => row.eligible && selected.includes(row.url))) {
      acknowledged.set(row.url, { url: row.url, reason: reason(row) });
    }
    confirmed = rows.filter(row => row.eligible && acknowledged.has(row.url)).map(row => ({ ...acknowledged.get(row.url) }));
    selected = []; void focusSummary();
  }
  onDestroy(() => { alive = false; ++generation; controller?.abort(); });
</script>

<section aria-label="Image preparation" aria-busy={running}>
  <h2>Images</h2>
  <p>Prepare images explicitly before going offline or closing the source page. No image previews are loaded.</p>
  <button onclick={prepare} disabled={running || disabled || frozen}>{attempted ? 'Retry images / grant access' : 'Prepare images'}</button>
  {#if running}<button onclick={cancel}>Cancel image preparation</button>{/if}
  <div tabindex="-1" bind:this={summary} aria-live="polite" class="image-summary">
    <p>{rows.filter(row => row.local).length} / {rows.length} stored locally. {ready ? 'Images ready for import validation.' : 'Images not ready for import validation.'}</p>
    {#if notice}<p>{notice}</p>{/if}
  </div>
  {#if frozen}<p>Resolve the live delivery attempt before preparing images or changing external references.</p>{/if}
  {#each errors as error}<p class="error" role="alert">{error.code}: {error.message}</p>{/each}
  <ul class="image-list">
    {#each rows as row (row.url)}
      <li>
        <code>{row.url}</code>
        <p>{row.status}</p>
        <p>MIME: {row.mediaType ?? 'unknown'} · Bytes: {row.size ?? 'unknown'}</p>
        {#if row.error}<p class="error">{row.error.code}{row.error.status ? ` (${row.error.status})` : ''}: {row.error.message}</p>{/if}
        {#if row.eligible}
          <label class="checkbox-label"><input type="checkbox" aria-label={`Keep external reference: ${row.url}`}
            checked={selected.includes(row.url) || confirmed.some(ack => ack.url === row.url)} disabled={running || disabled || frozen}
            onchange={event => choose(row.url, event.currentTarget.checked)} /> Keep external reference</label>
        {:else if !row.local}<p>Edit or remove this source if it cannot be stored; external fallback is not allowed.</p>{/if}
      </li>
    {/each}
  </ul>
  {#if selected.length}
    <h3>Confirm selected external references</h3>
    <ul>{#each rows.filter(row => selected.includes(row.url)) as row (row.url)}<li>{row.url} — {reason(row)}</li>{/each}</ul>
  {/if}
  <button onclick={confirmExternal} disabled={!selected.length || running || disabled || frozen}>Confirm external references</button>
  {#if confirmed.length || resultWarnings.length}
    <aside aria-label="External image warnings">
      <p>This material is not fully offline / uses external images.</p>
      <ul>{#each confirmed as warning}<li>{warning.url} — {warning.reason}</li>{/each}</ul>
      <!-- HTTP success/receipt warnings are ordered reason strings, including
           duplicates. They are separate from preparation acknowledgements. -->
      <ul>{#each resultWarnings as reason}<li>{reason}</li>{/each}</ul>
    </aside>
  {/if}
  <p class="muted">External choices start unchecked and require separate confirmation. Reconfirm after editing Markdown, retrying preparation or reopening the editor. Use Send to IGoNotes below for manual delivery.</p>
</section>

<style>
  .image-list code { overflow-wrap: anywhere; white-space: pre-wrap; }
  .image-list li { margin-block: 1rem; }
  .image-summary:focus { outline: 2px solid currentColor; }
</style>
