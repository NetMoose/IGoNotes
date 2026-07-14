<script>
    let { show = false, title = "", onConfirm, onCancel, confirmText = "OK", cancelText = "Отмена", input = false, inputValue = $bindable(""), error = "" } = $props();

    function focusInput(node) {
        // Небольшая задержка помогает в некоторых случаях при рендере модалок
        setTimeout(() => node.focus(), 10);
    }
</script>

{#if show}
<div class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
    <div class="bg-white rounded-lg shadow-lg w-96 p-5">
        <h3 class="text-lg font-medium text-gray-900 mb-4">{title}</h3>
        
        {#if input}
            <input 
                use:focusInput
                type="text" 
                bind:value={inputValue} 
                class="w-full border border-gray-300 rounded px-3 py-2 mb-2 focus:outline-none focus:border-blue-500" 
                onkeydown={(e) => e.key === 'Enter' && onConfirm()}
            />
        {/if}

        {#if error}
            <p class="text-sm text-red-600 mb-4">{error}</p>
        {/if}

        <div class="flex justify-end gap-2">
            <button onclick={onCancel} class="px-4 py-2 text-sm text-gray-600 hover:bg-gray-100 rounded cursor-pointer transition-colors">{cancelText}</button>
            <button onclick={onConfirm} class="px-4 py-2 text-sm bg-blue-600 text-white rounded hover:bg-blue-700 cursor-pointer transition-colors">{confirmText}</button>
        </div>
    </div>
</div>
{/if}
