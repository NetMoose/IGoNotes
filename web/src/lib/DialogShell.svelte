<script module>
    let nextDialogId = 0;
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
        title = '',
        description = '',
        error = '',
        busy = false,
        maxWidth = 'max-w-lg',
        onCancel = () => {},
        children,
        actions
    } = $props();

    const idPrefix = `dialog-${++nextDialogId}`;
    const titleId = `${idPrefix}-title`;
    const descriptionId = `${idPrefix}-description`;
    const errorId = `${idPrefix}-error`;
    let dialogDescriptionIds = $derived([
        description ? descriptionId : '',
        error ? errorId : ''
    ].filter(Boolean).join(' '));
    let dialogElement = $state();

    $effect(() => {
        if (!busy || !dialogElement) return;

        const frame = requestAnimationFrame(() => {
            if (!dialogElement?.isConnected) return;
            const focused = document.activeElement;
            if (!dialogElement.contains(focused) || focused.matches(':disabled')) {
                dialogElement.focus();
            }
        });

        return () => cancelAnimationFrame(frame);
    });

    function backgroundElements(overlay) {
        const elements = new Set();
        let branch = overlay;
        while (branch.parentElement) {
            const parent = branch.parentElement;
            for (const sibling of parent.children) {
                if (sibling !== branch) elements.add(sibling);
            }
            if (parent === document.body) break;
            branch = parent;
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
            const initialFocus = panel.querySelector('[data-dialog-initial-focus]:not(:disabled)')
                ?? focusableElements(panel)[0]
                ?? panel;
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
</script>

{#if show}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="presentation">
        <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role: dialog is intentionally a section landmark -->
        <section
            bind:this={dialogElement}
            use:manageDialog
            class="w-full {maxWidth} rounded-lg bg-white p-5 shadow-lg"
            role="dialog"
            aria-modal="true"
            aria-labelledby={titleId}
            aria-describedby={dialogDescriptionIds || undefined}
            aria-busy={busy}
            tabindex="-1"
            onkeydown={handleDialogKeydown}
        >
            <h3 id={titleId} class="mb-4 text-lg font-medium text-gray-900">{title}</h3>

            {#if description}
                <p id={descriptionId} class="mb-4 text-sm text-gray-600">{description}</p>
            {/if}

            {@render children?.({ descriptionId, errorId })}

            {#if error}
                <p id={errorId} role="alert" class="mb-4 text-sm text-red-600">{error}</p>
            {/if}

            {#if actions}
                <footer class="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
                    {@render actions()}
                </footer>
            {/if}
        </section>
    </div>
{/if}
