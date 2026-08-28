<script module>
    let nextModalId = 0;
    const inertElements = new Map();

    function retainInert(element) {
        const existing = inertElements.get(element);
        if (existing) {
            existing.count++;
            return;
        }

        const state = {
            count: 1,
            hadAttribute: element.hasAttribute('inert'),
            attributeValue: element.getAttribute('inert'),
            hasProperty: 'inert' in element,
            propertyValue: element.inert
        };
        inertElements.set(element, state);
        element.setAttribute('inert', '');
        if (state.hasProperty) element.inert = true;
    }

    function releaseInert(element) {
        const state = inertElements.get(element);
        if (!state) return;
        state.count--;
        if (state.count > 0) return;

        inertElements.delete(element);
        if (state.hasProperty) element.inert = state.propertyValue;
        if (state.hadAttribute) {
            element.setAttribute('inert', state.attributeValue ?? '');
        } else {
            element.removeAttribute('inert');
        }
    }
</script>

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

    const idPrefix = `modal-${++nextModalId}`;
    const titleId = `${idPrefix}-title`;
    const descriptionId = `${idPrefix}-description`;
    const errorId = `${idPrefix}-error`;
    const inputId = `${idPrefix}-input`;
    let dialogDescriptionIds = $derived([
        description ? descriptionId : '',
        error ? errorId : ''
    ].filter(Boolean).join(' '));
    let inputElement = $state();
    let cancelButton = $state();
    let confirmButton = $state();

    function backgroundElements(overlay) {
        const elements = new Set();
        const parent = overlay.parentElement;
        if (parent) {
            for (const sibling of parent.children) {
                if (sibling !== overlay) elements.add(sibling);
            }
        }

        let bodyBranch = overlay;
        while (bodyBranch.parentElement && bodyBranch.parentElement !== document.body) {
            bodyBranch = bodyBranch.parentElement;
        }
        if (bodyBranch.parentElement === document.body) {
            for (const sibling of document.body.children) {
                if (sibling !== bodyBranch) elements.add(sibling);
            }
        }
        return [...elements];
    }

    function focusableElements(panel) {
        const selector = [
            'a[href]',
            'button:not([disabled])',
            'input:not([disabled])',
            'select:not([disabled])',
            'textarea:not([disabled])',
            '[tabindex]:not([tabindex="-1"])'
        ].join(',');
        return [...panel.querySelectorAll(selector)].filter((element) => {
            const style = getComputedStyle(element);
            return !element.hidden
                && element.getAttribute('aria-hidden') !== 'true'
                && style.display !== 'none'
                && style.visibility !== 'hidden';
        });
    }

    function manageDialog(panel) {
        const previousFocus = document.activeElement;
        const overlay = panel.parentElement;
        const backgrounds = overlay ? backgroundElements(overlay) : [];
        for (const element of backgrounds) retainInert(element);

        const frame = requestAnimationFrame(() => {
            const initialFocus = inputElement && !inputElement.disabled
                ? inputElement
                : confirmButton && !confirmButton.disabled
                    ? confirmButton
                    : cancelButton && !cancelButton.disabled
                        ? cancelButton
                        : panel;
            initialFocus.focus();
        });

        return {
            destroy() {
                cancelAnimationFrame(frame);
                for (const element of backgrounds) releaseInert(element);
                if (previousFocus instanceof HTMLElement && previousFocus.isConnected) {
                    previousFocus.focus();
                }
            }
        };
    }

    function handleDialogKeydown(event) {
        if (event.key === 'Escape') {
            event.preventDefault();
            event.stopPropagation();
            if (!busy) onCancel();
            return;
        }
        if (event.key !== 'Tab') return;

        const focusable = focusableElements(event.currentTarget);
        if (focusable.length === 0) {
            event.preventDefault();
            event.currentTarget.focus();
            return;
        }

        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        const active = document.activeElement;
        if (event.shiftKey && (active === first || !event.currentTarget.contains(active))) {
            event.preventDefault();
            last.focus();
        } else if (!event.shiftKey && (active === last || !event.currentTarget.contains(active))) {
            event.preventDefault();
            first.focus();
        }
    }

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

{#if show}
<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="presentation">
    <div
        use:manageDialog
        class="w-full max-w-sm rounded-lg bg-white p-5 shadow-lg"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={dialogDescriptionIds || undefined}
        tabindex="-1"
        onkeydown={handleDialogKeydown}
    >
        <h3 id={titleId} class="text-lg font-medium text-gray-900 mb-4">{title}</h3>

        {#if description}
            <p id={descriptionId} class="text-sm text-gray-600 mb-4">{description}</p>
        {/if}
        
        {#if input}
            <label for={inputId} class="sr-only">{title}</label>
            <input 
                bind:this={inputElement}
                id={inputId}
                type="text" 
                bind:value={inputValue} 
                class="mb-2 w-full rounded border border-gray-300 px-3 py-2 focus:border-blue-500 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600"
                aria-invalid={error ? 'true' : undefined}
                aria-describedby={error ? errorId : undefined}
                disabled={busy}
                onkeydown={handleInputKeydown}
            />
        {/if}

        {#if error}
            <p id={errorId} role="alert" class="text-sm text-red-600 mb-4">{error}</p>
        {/if}

        <div class="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <button bind:this={cancelButton} type="button" onclick={onCancel} disabled={busy} class="cursor-pointer rounded px-4 py-2 text-sm text-gray-600 transition-colors hover:bg-gray-100 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-50">{cancelText}</button>
            <button
                bind:this={confirmButton}
                type="button"
                onclick={onConfirm}
                disabled={busy || confirmDisabled}
                aria-busy={busy}
                class="cursor-pointer rounded px-4 py-2 text-sm text-white transition-colors {danger ? 'bg-red-600 hover:bg-red-700' : 'bg-blue-600 hover:bg-blue-700'} focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-50"
            >{confirmText}</button>
        </div>
    </div>
</div>
{/if}
