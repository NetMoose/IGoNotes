<script>
    let { show = false, title = "", onConfirm, onCancel, confirmText = "OK", cancelText = "Отмена", input = false, inputValue = $bindable("") } = $props();
</script>

{#if show}
<div class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
    <div class="bg-white rounded-lg shadow-lg w-96 p-5">
        <h3 class="text-lg font-medium text-gray-900 mb-4">{title}</h3>
        
        {#if input}
            <input 
                type="text" 
                bind:value={inputValue} 
                class="w-full border border-gray-300 rounded px-3 py-2 mb-4 focus:outline-none focus:border-blue-500" 
                onkeydown={(e) => e.key === 'Enter' && onConfirm()}
                autofocus
            />
        {/if}

        <div class="flex justify-end gap-2">
            <button onclick={onCancel} class="px-4 py-2 text-sm text-gray-600 hover:bg-gray-100 rounded">{cancelText}</button>
            <button onclick={onConfirm} class="px-4 py-2 text-sm bg-blue-600 text-white rounded hover:bg-blue-700">{confirmText}</button>
        </div>
    </div>
</div>
{/if}
