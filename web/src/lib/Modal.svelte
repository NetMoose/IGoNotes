<script>
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

    function focusOnOpen(node, enabled = true) {
        // requestAnimationFrame гарантирует, что элемент уже отрендерен браузером
        let frame = null;

        function scheduleFocus(shouldFocus) {
            if (frame !== null) cancelAnimationFrame(frame);
            frame = shouldFocus
                ? requestAnimationFrame(() => node.focus())
                : null;
        }

        scheduleFocus(enabled);
        return {
            update: scheduleFocus,
            destroy() {
                if (frame !== null) cancelAnimationFrame(frame);
            }
        };
    }

    function handleOverlayKeydown(event) {
        if (event.key === 'Escape') {
            event.preventDefault();
            onCancel();
        }
    }

    function handleInputKeydown(event) {
        if (event.key === 'Enter' && !busy && !confirmDisabled) {
            event.preventDefault();
            onConfirm();
        }
    }
</script>

{#if show}
<div class="fixed inset-0 bg-black/50 flex items-center justify-center z-50" role="presentation" onkeydown={handleOverlayKeydown}>
    <div
        class="bg-white rounded-lg shadow-lg w-96 p-5"
        role="dialog"
        aria-modal="true"
        aria-labelledby="modal-title"
        aria-describedby={description ? 'modal-description' : undefined}
    >
        <h3 id="modal-title" class="text-lg font-medium text-gray-900 mb-4">{title}</h3>

        {#if description}
            <p id="modal-description" class="text-sm text-gray-600 mb-4">{description}</p>
        {/if}
        
        {#if input}
            <input 
                use:focusOnOpen
                type="text" 
                bind:value={inputValue} 
                class="w-full border border-gray-300 rounded px-3 py-2 mb-2 focus:outline-none focus:border-blue-500" 
                aria-invalid={error ? 'true' : undefined}
                aria-describedby={error ? 'modal-error' : undefined}
                disabled={busy}
                onkeydown={handleInputKeydown}
            />
        {/if}

        {#if error}
            <p id="modal-error" role="alert" class="text-sm text-red-600 mb-4">{error}</p>
        {/if}

        <div class="flex justify-end gap-2">
            <button type="button" onclick={onCancel} disabled={busy} class="px-4 py-2 text-sm text-gray-600 hover:bg-gray-100 disabled:opacity-50 rounded cursor-pointer transition-colors">{cancelText}</button>
            <button
                use:focusOnOpen={!input}
                type="button"
                onclick={onConfirm}
                disabled={busy || confirmDisabled}
                aria-busy={busy}
                class="px-4 py-2 text-sm {danger ? 'bg-red-600 hover:bg-red-700' : 'bg-blue-600 hover:bg-blue-700'} text-white rounded disabled:opacity-50 cursor-pointer transition-colors"
            >{confirmText}</button>
        </div>
    </div>
</div>
{/if}
