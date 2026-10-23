<script>
  import { untrack, onDestroy } from 'svelte';
  import { createDelivery } from '../delivery/stateMachine.js';
  import { isFrozen } from '../storage/drafts.js';

  let { store, draft, imageState, flushSave, disabled = false, onBusy, onDraft, onSuccess, options = {} } = $props();
  const delivery = untrack(() => createDelivery({ ...options, store, flush: flushSave, onFrozen: value => { if (alive) onDraft?.(value); } }));
  // '/' cannot be a protocol parent (root is ''), so no legal folder collides.
  let bases = $state(null), base = $state(''), folder = $state('/'), listing = $state(null);
  let confirmed = $state(''), running = $state(false), notice = $state(''), error = $state('');
  let alive = true;
  const frozen = $derived(isFrozen(draft));
  const imagesReady = $derived(imageState?.draftId === draft.id && imageState.markdown === draft.markdown && imageState.ready);
  const descriptions = {
    clipper_request_changed: 'Import identity conflict. Keep the immutable draft; inspect the original attempt in IGoNotes.',
    clipper_import_abandoned: 'Server recovery was abandoned. Files may already exist. The local draft is preserved; inspect the destination before a new import.',
    note_conflict: 'The filename already exists. Edit it and explicitly reconfirm the destination for a new attempt.',
    clipper_destination_changed: 'The destination changed. Load and explicitly confirm it again.',
    prepare_again: 'Frozen image verification failed before POST. Prepare images and confirm again.',
    unauthorized: 'Reconnect using a valid token at the original endpoint, then check the same attempt.',
    setup_required: 'Complete IGoNotes setup at the original endpoint, then check the same attempt.',
    clipper_recovery_required: 'Server recovery is required. Inspect Web Clipper settings in IGoNotes.',
    git_conflict_pending: 'Resolve the Git conflict in IGoNotes, then check or replay this exact attempt.',
  };
  async function run(action) {
    if (running || disabled) return;
    running = true; onBusy?.(true); error = ''; notice = '';
    try { await action(); }
    catch (cause) {
      if (alive) {
        if (cause?.code === 'confirm_destination') confirmed = '';
        error = cause?.message || 'Delivery failed. Keep the draft and check the saved attempt.';
      }
    }
    finally { if (alive) { running = false; onBusy?.(false); } }
  }
  async function load() {
    confirmed = ''; base = ''; folder = '/'; listing = null; bases = null;
    bases = (await delivery.loadDestinations()).bases;
  }
  async function chooseBase(value) {
    base = value; confirmed = ''; folder = '/'; listing = null;
    if (base) listing = await delivery.loadFolders(base);
  }
  async function confirm() {
    const saved = await delivery.confirmDestination(draft.id, { base, parent: folder, destinationRevision: listing.destination_revision });
    if (alive) { onDraft?.(saved); confirmed = `Confirmed destination: ${saved.base} / ${saved.parent || '(root)'}`; }
  }
  async function deliver(kind) {
    // Capture image acknowledgements before onBusy propagates a frozen UI state.
    const acks = imageState?.confirmedFallbacks?.map(ack => ({ ...ack })) ?? [];
    let outcome;
    try { outcome = kind === 'send' ? await delivery.send(draft.id, { confirmedFallbacks: acks }) : await delivery[kind](draft.id); }
    finally {
      // A storage failure after POST must not make this editor appear editable.
      // Do not overwrite unsaved form values on a failed preflight/flush.
      try {
        const live = await store.getDraft(draft.id);
        if (alive && isFrozen(live)) onDraft?.(live);
      } catch { /* Existing UI freeze remains; explicit reload can recover. */ }
    }
    if (!alive) return;
    if (outcome.state === 'succeeded') { await onSuccess?.(outcome.result); return; }
    const live = await store.getDraft(draft.id);
    if (!alive) return;
    if (live) onDraft?.(live);
    if (outcome.state === 'rejected') confirmed = '';
    notice = `Delivery ${outcome.state}. ${descriptions[outcome.code] ?? (outcome.state === 'rejected' ? 'Draft preserved. Review it and confirm a new destination before sending again.' : 'Keep this immutable draft. Manually check the attempt; retry checks first and only replays the exact saved ID if it is unknown.')}`;
  }
  onDestroy(() => { alive = false; });
</script>

<section aria-label="Manual delivery" aria-busy={running}>
  <h2>Send to IGoNotes</h2>
  <p>All network actions are manual. A new import requires an explicitly confirmed base and existing folder.</p>
  {#if !frozen}
    <button onclick={() => run(load)} disabled={running || disabled}>Load destinations</button>
    {#if bases}
      <label for="delivery-base">Destination base</label>
      <select id="delivery-base" value={base} disabled={running || disabled} onchange={event => run(() => chooseBase(event.currentTarget.value))}>
        <option value="">Choose a base</option>
        {#each bases as name}<option value={name}>{name}</option>{/each}
      </select>
    {/if}
    {#if listing}
      <label for="delivery-folder">Destination folder</label>
      <select id="delivery-folder" bind:value={folder} disabled={running || disabled} onchange={() => confirmed = ''}>
        <option value="/">Choose a folder</option>
        {#each listing.folders as path}<option value={path}>{path || '(root)'}</option>{/each}
      </select>
      <button onclick={() => run(confirm)} disabled={running || disabled || folder === '/'}>Confirm destination</button>
    {/if}
    {#if confirmed}<p>{confirmed}</p>{/if}
    <button class="primary" onclick={() => run(() => deliver('send'))} disabled={running || disabled || !confirmed || !imagesReady}>Send</button>
  {:else}
    <p>Immutable attempt: {draft.activeAttemptId ?? 'unresolved'}. Destination: {draft.base} / {draft.parent || '(root)'}. Keep its draft and images until a matching receipt is confirmed.</p>
    <button onclick={() => run(() => deliver('check'))} disabled={running || disabled}>Check attempt</button>
    <button onclick={() => run(() => deliver('retry'))} disabled={running || disabled}>Check / retry exact attempt</button>
    <p>Connection settings remain available for reconnecting at the original endpoint.</p>
  {/if}
  <div aria-live="polite">{#if notice}<p>{notice}</p>{/if}</div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</section>
