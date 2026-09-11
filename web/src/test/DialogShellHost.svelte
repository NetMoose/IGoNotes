<script>
    import DialogShell from '../lib/DialogShell.svelte';

    let {
        show = false,
        title = 'Диалог',
        description = '',
        error = '',
        busy = false,
        maxWidth = 'max-w-lg',
        includeContenteditable = false,
        includeTabindexMinusOne = false,
        onCancel = () => {}
    } = $props();
</script>

<button type="button">Открыть</button>

<DialogShell {show} {title} {description} {error} {busy} {maxWidth} {onCancel}>
    {#snippet children({ descriptionId, errorId })}
        <input
            type="text"
            aria-label="Значение"
            data-dialog-initial-focus
            aria-describedby={error ? errorId : undefined}
        />
    {/snippet}

    {#snippet actions()}
        <button type="button" onclick={onCancel} disabled={busy}>Отмена</button>
        <button type="button" disabled={busy}>Подтвердить</button>
        {#if includeTabindexMinusOne}
            <button type="button" tabindex="-1">Пропустить</button>
        {/if}
        {#if includeContenteditable}
            <div contenteditable="true" role="textbox" aria-label="Редактор">Текст</div>
        {/if}
    {/snippet}
</DialogShell>
