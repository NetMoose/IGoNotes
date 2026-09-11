<script module>
    let nextModalId = 0;
</script>

<script>
    import DialogShell from './DialogShell.svelte';

    let {
        show = false,
        title = "",
        onConfirm,
        onCancel,
        confirmText = "OK",
        cancelText = "Отмена",
        input = false,
        inputValue = $bindable(""),
        error = "",
        description = "",
        busy = false,
        confirmDisabled = false,
        danger = false
    } = $props();

    const idPrefix = `modal-${++nextModalId}`;
    const errorId = `${idPrefix}-error`;
    const inputId = `${idPrefix}-input`;

    function handleInputKeydown(event) {
        if (
            event.key === 'Enter'
            && !event.repeat
            && !event.isComposing
            && !busy
            && !confirmDisabled
        ) {
            event.preventDefault();
            event.stopPropagation();
            onConfirm();
        }
    }
</script>

<DialogShell {show} {title} {description} {error} {busy} maxWidth="max-w-sm" {onCancel}>
    {#snippet children({ errorId })}
        {#if input}
            <label for={inputId} class="sr-only">{title}</label>
            <input
                id={inputId}
                type="text"
                bind:value={inputValue}
                data-dialog-initial-focus
                class="mb-2 w-full rounded border border-gray-300 px-3 py-2 focus:border-blue-500 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
                aria-invalid={error ? 'true' : undefined}
                aria-describedby={error ? errorId : undefined}
                disabled={busy}
                onkeydown={handleInputKeydown}
            />
        {/if}
    {/snippet}

    {#snippet actions()}
        <button type="button" onclick={onCancel} disabled={busy} class="cursor-pointer rounded px-4 py-2 text-sm text-gray-600 transition-colors hover:bg-gray-100 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-50">{cancelText}</button>
        <button
            type="button"
            onclick={onConfirm}
            disabled={busy || confirmDisabled}
            data-dialog-initial-focus={!input || undefined}
            aria-busy={busy}
            class="cursor-pointer rounded px-4 py-2 text-sm text-white transition-colors {danger ? 'bg-red-600 hover:bg-red-700' : 'bg-blue-600 hover:bg-blue-700'} focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-50"
        >{confirmText}</button>
    {/snippet}
</DialogShell>
