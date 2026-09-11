<script>
    import DialogShell from './DialogShell.svelte';

    let {
        stale,
        busy = false,
        onLoadDisk = () => {},
        onOverwrite = () => {},
        onManualMerge = () => {}
    } = $props();

    let mode = $state('compare');
    let mergeContent = $state('');
    let localBusy = $state(false);
    let error = $state('');
    let isBusy = $derived(busy || localBusy);
    let previousStale;
    let previousNoteId;
    let previousDiskRevision;

    $effect(() => {
        const nextStale = stale;
        const nextNoteId = nextStale?.noteId;
        const nextDiskRevision = nextStale?.diskRevision;
        if (
            nextStale === previousStale
            && nextNoteId === previousNoteId
            && nextDiskRevision === previousDiskRevision
        ) return;

        previousStale = nextStale;
        previousNoteId = nextNoteId;
        previousDiskRevision = nextDiskRevision;
        mode = 'compare';
        mergeContent = nextStale?.mine ?? '';
        error = '';
    });

    function showError(reason) {
        error = reason instanceof Error && reason.message
            ? reason.message
            : 'Не удалось выполнить действие.';
    }

    async function run(action, value) {
        if (isBusy) return;

        localBusy = true;
        error = '';
        try {
            await action(value);
        } catch (reason) {
            showError(reason);
        } finally {
            localBusy = false;
        }
    }

    function showCompare() {
        error = '';
        mode = 'compare';
    }

    function showConfirmation() {
        error = '';
        mode = 'confirm';
    }

    function showManualMerge() {
        error = '';
        mode = 'manual';
    }
</script>

<DialogShell
    show={Boolean(stale)}
    title="Конфликт изменений"
    error={error}
    busy={isBusy}
    maxWidth="max-w-6xl"
    onCancel={() => {}}
>
    {#snippet children()}
        {#if mode === 'compare'}
            <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
                <label class="flex flex-col gap-2 text-sm font-medium text-gray-700">
                    Моя версия
                    <textarea
                        readonly
                        value={stale.mine}
                        class="min-h-56 w-full resize-y rounded border border-gray-300 bg-gray-50 p-3 font-mono text-sm font-normal text-gray-900 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
                    ></textarea>
                </label>
                <label class="flex flex-col gap-2 text-sm font-medium text-gray-700">
                    Версия на диске
                    <textarea
                        readonly
                        value={stale.diskContent}
                        class="min-h-56 w-full resize-y rounded border border-gray-300 bg-gray-50 p-3 font-mono text-sm font-normal text-gray-900 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
                    ></textarea>
                </label>
            </div>
        {:else if mode === 'confirm'}
            <p class="text-sm text-gray-700">Перезапись заменит актуальный файл на диске.</p>
        {:else}
            <label class="flex flex-col gap-2 text-sm font-medium text-gray-700">
                Итоговый текст
                <textarea
                    bind:value={mergeContent}
                    disabled={isBusy}
                    class="min-h-56 w-full resize-y rounded border border-gray-300 p-3 font-mono text-sm font-normal text-gray-900 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:bg-gray-100"
                ></textarea>
            </label>
        {/if}
    {/snippet}

    {#snippet actions()}
        {#if mode === 'compare'}
            <button
                type="button"
                onclick={() => run(onLoadDisk)}
                disabled={isBusy}
                data-dialog-initial-focus
                class="cursor-pointer rounded px-4 py-2 text-sm text-gray-700 transition-colors hover:bg-gray-100 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
            >{stale.diskMissing ? 'Закрыть заметку' : 'Загрузить версию с диска'}</button>
            <button
                type="button"
                onclick={showManualMerge}
                disabled={isBusy || stale.diskMissing}
                class="cursor-pointer rounded px-4 py-2 text-sm text-blue-700 transition-colors hover:bg-blue-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
            >Объединить вручную</button>
            <button
                type="button"
                onclick={showConfirmation}
                disabled={isBusy || stale.diskMissing}
                class="cursor-pointer rounded bg-blue-600 px-4 py-2 text-sm text-white transition-colors hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
            >Оставить мою версию</button>
        {:else if mode === 'confirm'}
            <button
                type="button"
                onclick={showCompare}
                disabled={isBusy}
                class="cursor-pointer rounded px-4 py-2 text-sm text-gray-700 transition-colors hover:bg-gray-100 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
            >Назад</button>
            <button
                type="button"
                onclick={() => run(onOverwrite, { content: stale.mine, revision: stale.diskRevision })}
                disabled={isBusy}
                data-dialog-initial-focus
                class="cursor-pointer rounded bg-red-600 px-4 py-2 text-sm text-white transition-colors hover:bg-red-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
            >Подтвердить перезапись</button>
        {:else}
            <button
                type="button"
                onclick={showCompare}
                disabled={isBusy}
                class="cursor-pointer rounded px-4 py-2 text-sm text-gray-700 transition-colors hover:bg-gray-100 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
            >Назад</button>
            <button
                type="button"
                onclick={() => run(onManualMerge, { content: mergeContent, revision: stale.diskRevision })}
                disabled={isBusy}
                data-dialog-initial-focus
                class="cursor-pointer rounded bg-blue-600 px-4 py-2 text-sm text-white transition-colors hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
            >Сохранить объединение</button>
        {/if}
    {/snippet}
</DialogShell>
