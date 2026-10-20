# Settings About Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Execute inline in the existing `feat/settings-about` branch; commits and publication require an explicit user request.

**Goal:** Add application name/version to settings and move the settings button into the left action row.

**Architecture:** A small Go package owns build metadata, exposed as additive fields of `/api/info`. A focused Svelte component loads and presents this information in the third settings tab. Sidebar forwards its settings callback through the existing workspace upload/save transition.

**Tech Stack:** Go 1.26, Svelte 5, Tailwind CSS 4, Vitest, Testing Library, GitHub Actions.

**Specification:** `docs/superpowers/specs/2026-10-20-settings-about-design.md` (approved).

**Status:** Completed and verified locally, 2026-10-20.

---

## File map

- Create `internal/appinfo/info.go`: application name and linker-injected version.
- Modify `internal/handlers/note_handler.go` and its existing test: extend the info response without changing `base_path`.
- Modify `.github/workflows/release.yml`: inject the tag into both archive build functions.
- Create `web/src/lib/settings/AboutSection.svelte` and `.test.js`: load, validate and display app metadata, with loading/error/retry states.
- Modify `web/src/lib/settings/SettingsWorkspace.svelte` and its test: add About tab and keyboard navigation.
- Modify `web/src/lib/NotesWorkspace.svelte`, `Sidebar.svelte` and existing workspace tests: move settings action while preserving its callback and busy state.
- Modify `docs/api.md`, `docs/developer.md`, `docs/user.md` and their exact `site/docs/` mirrors: describe the additive info fields, version injection and settings navigation.

## Task 1: Build metadata and API

- [x] Add `TestNoteHandlerGetInfoIncludesApplication` to the existing handler test. Assert HTTP 200, JSON content type, unchanged base path, `app_name == "IGoNotes"`, and `app_version == "dev"`. Allow an explicitly supplied `IGONOTES_TEST_VERSION` to set the expected version for the linker verification command.

```go
wantVersion := os.Getenv("IGONOTES_TEST_VERSION")
if wantVersion == "" {
    wantVersion = "dev"
}
```

- [x] Run `go test ./internal/handlers -run '^TestNoteHandlerGetInfoIncludesApplication$' -count=1`; observe failure because the new fields are absent.
- [x] Create the metadata package:

```go
package appinfo

const Name = "IGoNotes"

// Version is set to the release tag using -ldflags -X.
var Version = "dev"
```

- [x] Import `IGoNotes/internal/appinfo` in the handler and add the fields:

```go
info := map[string]string{
    "base_path": h.NoteService.GetBasePath(),
    "app_name": appinfo.Name,
    "app_version": appinfo.Version,
}
```

- [x] In both `go build` lines of the release workflow use `-ldflags="-s -w -X IGoNotes/internal/appinfo.Version=${VERSION}"`.
- [x] Run the handler test normally, then run `IGONOTES_TEST_VERSION=v0.0.1-alpha.99 go test -ldflags='-X IGoNotes/internal/appinfo.Version=v0.0.1-alpha.99' ./internal/handlers -run '^TestNoteHandlerGetInfoIncludesApplication$' -count=1`. Both must pass.

## Task 2: About tab and left settings action

- [x] Install frontend dependencies with `npm --prefix web ci` after confirming Node major 24.
- [x] Extend the existing settings integration test to select the About tab, see its linked panel, use End to focus it, and preserve Home/arrow navigation. Mock `getInfo` at the existing API boundary and return `{ app_name: 'IGoNotes', app_version: 'v0.0.1-alpha.99' }`.
- [x] Extend the existing empty workspace test to find the settings action within the `complementary` region, last among the creation/rename/delete actions, and assert the header does not contain it. Existing upload/save/error tests remain the transition contract.
- [x] Run `npm --prefix web test -- src/lib/settings/AboutSection.test.js src/lib/settings/SettingsWorkspace.test.js src/lib/NotesWorkspace.test.js` and observe failures for the missing tab and wrong button location.
- [x] Add the About component with local loading, info and error state. Load through `getInfo` on mount; ignore completion after unmount. Reject missing/empty/non-string metadata, display a retry button on failure, and render `dev` as «Сборка разработки». Keep the component independent of base management.

```js
if (typeof result?.app_name !== 'string' || !result.app_name.trim()
    || typeof result?.app_version !== 'string' || !result.app_version.trim()) {
  throw new Error('Некорректные сведения о программе')
}
```

- [x] Add meaningful component tests for a pending response, release version, dev display, network failure followed by successful retry, and malformed metadata that must not display a development version. Run the new tests before implementing the corresponding states, then confirm they pass.
- [x] Add `about` to the settings navigation list. Add the third tab and panel using the existing ARIA/id/tabindex conventions. Mount `AboutSection` only when its tab is active, so ordinary settings do not cause another request.
- [x] Forward `onOpenSettings` and `settingsDisabled` from the workspace to Sidebar:

```svelte
onOpenSettings={(...args) => runAfterUploads(onOpenSettings, ...args)}
settingsDisabled={saveStatus === 'saving'}
```

- [x] Move the existing gear SVG into a final Sidebar row button, reuse neighboring button styling, keep accessible name/title, and remove the old header button. Sidebar calls the forwarded callback without owning save logic.
- [x] Run `npm --prefix web test -- src/lib/settings/AboutSection.test.js src/lib/settings/SettingsWorkspace.test.js src/lib/NotesWorkspace.test.js src/App.test.js src/lib/app-transitions.test.js`; all must pass.

## Task 3: Documentation and acceptance

- [x] Update `/api/info` description/example with `app_name` and `app_version`, including `dev` semantics. Document release injection and the new settings location in developer/user guides.
- [x] Run `npm --prefix web run verify`, `go test ./... -count=1`, `go vet ./...` and `git diff --check`; all must pass. Existing nonfatal build warnings can be reported separately.
- [x] Cross-build the application for linux/windows/darwin × amd64/arm64 using `CGO_ENABLED=0`, `-trimpath` and the same linker flag as releases, into a verified temporary directory outside the workspace.
- [x] Review the final diff against the approved specification. Mark completed steps in this plan and report modified behavior and verification evidence. Leave commits/merge/push to an explicit user request.

## Verification evidence

- Node `v24.20.0`, npm `12.0.2`; lockfile installation completed.
- Go info contract test: RED for missing fields, GREEN for default `dev` and injected `v0.0.1-alpha.99`.
- Frontend RED: 11 expected failures for absent About tab and settings button location. GREEN: 151 targeted tests, including application save/upload transitions.
- `npm --prefix web run verify`: 508 tests in 31 files passed; production build passed.
- `go test ./... -count=1`, `go vet ./...`, `git diff --check`: passed.
- Six stripped, CGO-disabled cross-builds with injected `v0.0.1-alpha.99` passed; executable formats matched their targets. Linux amd64 `--help` smoke check passed.
- Independent read-only code review found no material correctness issues.
- Existing nonfatal warnings: Modal tests report double unmount; Vite reports a large main chunk. Dependency installation reports the two previously known moderate advisories.
