# Preview Scroll Reset Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Open each newly selected note at the top of preview while preserving preview mode.

**Architecture:** Pass the active note ID from `App.svelte` into the persistent `Editor` component. Key only the preview scroll subtree by that ID so Svelte recreates the scroll container on note changes, while content changes within the same note keep the existing container and scroll position.

**Tech Stack:** Svelte 5, Vite 8, Tailwind CSS 4

---

## File Structure

- Modify `web/src/App.svelte`: pass the active note ID to `Editor`.
- Modify `web/src/lib/Editor.svelte`: accept `noteId` and key the preview scroll container by it.
- Create `docs/superpowers/specs/2026-08-26-preview-scroll-reset-design.md`: preserve the approved behavior design.
- Create `docs/superpowers/plans/2026-08-26-preview-scroll-reset.md`: preserve implementation and verification steps.

### Task 1: Reset Preview Scroll On Note Changes

**Files:**
- Modify: `web/src/App.svelte:115`
- Modify: `web/src/lib/Editor.svelte:22,580-586`

- [ ] **Step 1: Create an isolated branch**

Run:

```bash
git switch -c fix/preview-scroll-reset
```

Expected: the current branch becomes `fix/preview-scroll-reset`; the untracked design and plan files remain available.

- [ ] **Step 2: Run the structural regression check and verify it fails**

Run from the repository root:

```bash
if ! grep -q 'noteId={activeNote.id}' web/src/App.svelte || ! grep -q '{#key noteId}' web/src/lib/Editor.svelte; then
  printf '%s\n' 'FAIL: preview is not keyed by the active note ID'
  exit 1
fi
```

Expected: exit code `1` with `FAIL: preview is not keyed by the active note ID`.

- [ ] **Step 3: Pass the active note ID to Editor**

In `web/src/App.svelte`, replace the Editor invocation with:

```svelte
<Editor noteId={activeNote.id} bind:content={markdownContent} />
```

- [ ] **Step 4: Accept noteId in Editor**

In `web/src/lib/Editor.svelte`, replace the props declaration with:

```javascript
let { noteId, content = $bindable() } = $props();
```

- [ ] **Step 5: Key only the preview scroll container**

Replace the preview block in `web/src/lib/Editor.svelte` with:

```svelte
{#if mode === 'preview'}
  {#key noteId}
    <div class="absolute inset-0 overflow-y-auto p-6 bg-white" onclick={handlePreviewClick} role="presentation">
      <article class="prose max-w-4xl mx-auto">
        {@html renderMarkdown(content)}
      </article>
    </div>
  {/key}
{/if}
```

Do not key the entire `Editor` or key by `content`.

- [ ] **Step 6: Re-run the structural regression check**

Run:

```bash
grep -q 'noteId={activeNote.id}' web/src/App.svelte
grep -q 'let { noteId, content = \$bindable() } = \$props();' web/src/lib/Editor.svelte
grep -q '{#key noteId}' web/src/lib/Editor.svelte
if grep -q '{#key content}' web/src/lib/Editor.svelte; then
  printf '%s\n' 'FAIL: content changes would reset preview scroll'
  exit 1
fi
```

Expected: exit code `0` with no output.

- [ ] **Step 7: Build the frontend**

Run:

```bash
npm run build
```

Working directory: `web`

Expected: Vite completes successfully with no Svelte compilation errors.

- [ ] **Step 8: Run repository verification**

Run from the repository root:

```bash
go test ./...
git diff --check
```

Expected: Go tests pass and the diff has no whitespace errors.

- [ ] **Step 9: Verify behavior manually**

Run the application and perform this scenario:

1. Open a long note and switch to preview.
2. Scroll to the middle.
3. Select another long note in the tree.
4. Confirm preview mode remains active and the new note starts at the top.
5. Scroll down and toggle a checkbox in the current preview.
6. Confirm the checkbox update does not reset the current note's scroll position.

### Task 2: Commit The Fix

**Files:**
- Modify: `web/src/App.svelte`
- Modify: `web/src/lib/Editor.svelte`
- Create: `docs/superpowers/specs/2026-08-26-preview-scroll-reset-design.md`
- Create: `docs/superpowers/plans/2026-08-26-preview-scroll-reset.md`

- [ ] **Step 1: Review intended changes**

Run:

```bash
git status --short
git diff
git log --oneline -10
```

Expected: only the two Svelte files and the preview-scroll design/plan documents are changed.

- [ ] **Step 2: Commit the verified fix**

Run:

```bash
git add web/src/App.svelte web/src/lib/Editor.svelte docs/superpowers/specs/2026-08-26-preview-scroll-reset-design.md docs/superpowers/plans/2026-08-26-preview-scroll-reset.md
git commit -m "fix: reset preview scroll on note change"
```

Expected: one commit and a clean feature branch.
