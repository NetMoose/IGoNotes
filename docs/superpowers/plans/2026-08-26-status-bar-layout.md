# Status Bar Layout Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent the global status bar from overlapping the notes tree while keeping it below both the Sidebar and editor.

**Architecture:** Keep the existing column layout in `App.svelte`, where the workspace consumes the remaining height above the fixed-height footer. Make the Sidebar inherit that workspace height instead of independently consuming the full viewport height.

**Tech Stack:** Svelte 5, Tailwind CSS 4, Vite 8

---

## File Structure

- Modify `web/src/lib/Sidebar.svelte`: constrain the root Sidebar element to its parent workspace height.
- Verify `web/src/App.svelte`: no change is required because it already reserves a separate flex row for the footer.

### Task 1: Constrain Sidebar To The Workspace

**Files:**
- Modify: `web/src/lib/Sidebar.svelte:202`

- [ ] **Step 1: Run the layout regression check and verify it fails**

Run from the repository root:

```bash
if ! grep -q 'flex flex-col h-full shrink-0' web/src/lib/Sidebar.svelte; then
  printf '%s\n' 'FAIL: Sidebar does not inherit the workspace height'
  exit 1
fi
```

Expected: exit code `1` with `FAIL: Sidebar does not inherit the workspace height` because the element currently contains `h-screen`.

- [ ] **Step 2: Apply the minimal layout fix**

In `web/src/lib/Sidebar.svelte`, replace the root element with:

```svelte
<aside class="w-72 bg-gray-50 border-r border-gray-200 flex flex-col h-full shrink-0">
```

Do not modify the footer or the surrounding layout in `web/src/App.svelte`.

- [ ] **Step 3: Re-run the layout regression check**

Run:

```bash
if ! grep -q 'flex flex-col h-full shrink-0' web/src/lib/Sidebar.svelte; then
  printf '%s\n' 'FAIL: Sidebar does not inherit the workspace height'
  exit 1
fi
if grep -q 'flex flex-col h-screen shrink-0' web/src/lib/Sidebar.svelte; then
  printf '%s\n' 'FAIL: Sidebar still uses viewport height'
  exit 1
fi
```

Expected: exit code `0` with no output.

- [ ] **Step 4: Build the frontend**

Run:

```bash
npm run build
```

Working directory: `web`

Expected: Vite reports a successful production build with no Svelte or Tailwind errors.

- [ ] **Step 5: Verify the layout behavior in the application**

Open the application at desktop and narrow viewport sizes and confirm:

- The status bar spans the full window width at the bottom.
- The Sidebar and editor or preview end immediately above the status bar.
- A long notes tree scrolls inside the Sidebar and remains visible above the status bar.
- The editor or preview height and scrolling behavior remain unchanged.
