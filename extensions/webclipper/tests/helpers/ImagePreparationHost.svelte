<script>
  import ImagePreparation from '../../src/ui/ImagePreparation.svelte';
  let { store, draft, acquirer, resultWarnings = [], flushSave = async () => true } = $props();
  let state = $state(null);
</script>
<ImagePreparation {store} draftId={draft.id} markdown={draft.markdown} sourceUrl={draft.sourceUrl}
  status={draft.status} {acquirer} {flushSave} {resultWarnings} onState={value => state = value} />
<!-- Readiness contract only: actual delivery belongs to 8.5. -->
<button disabled={!state?.ready}>Future send predicate</button>
