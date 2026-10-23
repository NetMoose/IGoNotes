<script>
  import { onMount, onDestroy, untrack } from 'svelte';
  import { createConnectionStore, DEFAULT_ENDPOINT, ConnectionError, validateEndpoint, validateToken } from '../connection/settings.js';
  import { createTransport, TransportError } from '../connection/transport.js';

  let { store: suppliedStore, transportFactory = createTransport } = $props();
  const store = untrack(() => suppliedStore) ?? createConnectionStore();
  let endpoint = $state(DEFAULT_ENDPOINT);
  let token = $state('');
  let loading = $state(true);
  let busy = $state(false);
  let status = $state('');
  let error = $state('');
  let revision = 0;
  let alive = true;
  let checkController;

  function friendly(cause) {
    // Select static messages, never use a native/server/injected error's text.
    if (cause instanceof ConnectionError) return new ConnectionError(cause.code).message;
    if (cause instanceof TransportError) return new TransportError(cause.code).message;
    return 'Connection settings could not be completed. Keep your inputs and try again.';
  }
  function changed() {
    revision += 1;
    status = 'Inputs changed. Test the current connection again.';
    error = '';
  }
  function snapshot() { return { endpoint: validateEndpoint(endpoint), token: validateToken(token) }; }
  async function run(action) {
    if (busy || loading) return;
    const started = revision;
    busy = true; error = ''; status = '';
    try {
      await action(started);
    } catch (cause) {
      if (alive && started === revision) error = friendly(cause);
    } finally {
      if (alive) {
        busy = false;
        if (started !== revision) { error = ''; status = 'Inputs changed. Test the current connection again.'; }
      }
    }
  }
  function save() {
    return run(async started => {
      await store.save(snapshot());
      if (alive && started === revision) status = 'Connection saved locally. Not tested.';
    });
  }
  function test() {
    return run(async started => {
      const api = transportFactory(snapshot());
      checkController = new AbortController();
      await api.info({ signal: checkController.signal });
      if (alive && started === revision) status = 'Connected: protocol v1 is supported.';
    });
  }
  function clear() {
    return run(async started => {
      await store.clear();
      if (alive && started === revision) {
        endpoint = DEFAULT_ENDPOINT; token = '';
        status = 'Local connection removed. Server token is not revoked.';
      }
    });
  }
  onMount(() => {
    void (async () => {
      try {
        const saved = await store.load();
        if (alive) { endpoint = saved.endpoint; token = saved.token; }
      } catch (cause) {
        if (alive) error = friendly(cause);
      } finally { if (alive) loading = false; }
    })();
  });
  onDestroy(() => { alive = false; token = ''; checkController?.abort(); });
</script>

<section aria-label="Connection settings" aria-busy={loading || busy}>
  <h2>Connection settings</h2>
  <p>Connect only to IGoNotes on this computer. Paste a connection token manually; it stays in trusted extension storage.</p>
  {#if loading}<p>Loading connection settings…</p>{/if}
  <fieldset disabled={loading}>
    <legend class="sr-only">Local IGoNotes connection</legend>
    <label for="connection-endpoint">Endpoint</label>
    <input id="connection-endpoint" type="text" bind:value={endpoint} oninput={changed} spellcheck="false" autocomplete="off" />
    <label for="connection-token">Connection token</label>
    <input id="connection-token" type="password" bind:value={token} oninput={changed} autocomplete="off" spellcheck="false" />
    <div class="editor-actions">
      <button onclick={save} disabled={busy}>Save connection</button>
      <button onclick={test} disabled={busy}>Test connection</button>
      <button onclick={clear} disabled={busy}>Clear connection</button>
    </div>
  </fieldset>
  <p class="muted">Save and Clear are local only. Test sends the current inputs to the chosen local endpoint, even before saving. Clear keeps drafts and does not revoke the token in IGoNotes.</p>
  {#if status}<p role="status">{status}</p>{/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</section>
