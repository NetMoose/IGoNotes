# IGoNotes Web Clipper — local draft editor

Tasks 6.1–6.3: an independently built JavaScript ESM / Svelte 5 / MV3 package
with native IndexedDB storage and a local draft list/editor. The popup opens a
separate editor tab (also the extension's options page).

> **Available:** list/reopen existing local drafts; edit title, filename and plain
> Markdown; read source URL and capture time; save, retry, explicitly delete and
> download the current Markdown. **Planned:** endpoint/token settings (6.4),
> page capture (7), image acquisition and backend delivery (8).
> A fresh profile truthfully shows an empty list. There is no capture or new-draft
> button yet; future capture task 7.3 will create drafts and open the editor.

## Build and load

From the repository root, with Node.js 24:

```sh
node --version
npm --prefix extensions/webclipper ci
npm --prefix extensions/webclipper run verify
```

Open `chrome://extensions`, enable Developer mode, choose **Load unpacked**, and
select `extensions/webclipper/dist`. Open the extension action to see the popup
and follow **Open draft editor**. Reload the extension after rebuilding.
This package is independent of `web/` and Go embed.

`vite.config.js` builds local ESM worker and UI entries with relative asset
paths. `vite.content.config.js` runs separately, emitting one classic IIFE
`content-script.js` with no shared imports, ready for future isolated-world
`scripting.executeScript({ files })` use. There are no persistent content scripts,
remote scripts, inline executable scripts or eval permissions. The declared
minimum is Chrome/Chromium 120; the actual smoke version below is newer.

Required hosts are exactly HTTP `127.0.0.1` and `localhost`; match patterns cover
all ports. IPv6 endpoints are **not supported/advertised** by this scaffold:
IPv6 match-pattern compatibility with the minimum supported browser has not
been established. Future endpoint settings must not accept `[::1]` without that
compatibility check and a corresponding manifest permission. Optional HTTP(S)
hosts are reserved for later per-origin image consent; none are requested now.

## Packaging smoke

Requires system Chromium, Xvfb and `xvfb-run` on Linux; it downloads no browser.
After building:

```sh
TMPDIR=/tmp/opencode xvfb-run -a npm --prefix extensions/webclipper run smoke
```

The headed smoke loads the real unpacked extension in a fresh temporary profile
under `/tmp/opencode` and removes it on exit. It verifies worker registration,
popup/editor DOM, popup-to-editor navigation, native IndexedDB v1 stores and the
empty editor after reload, real isolated content-file injection
on a local HTTP fixture, unchanged fixture DOM, and absence of manifest/runtime,
CSP, module, console and CDP errors. It is a packaging/storage startup check, not the
capture/backend acceptance suite planned for task 9.1. Browser DNS is restricted
to loopback and background networking is disabled.

The Linux supervisor owns Chromium and a separate Playwright check worker in
private process groups. Its overall deadline defaults to 30 seconds
(`CLIPPER_SMOKE_TIMEOUT_MS`, positive integer milliseconds) and covers browser
launch, evaluation, navigation and graceful close. Cleanup runs independently:
it closes the fixture server, asks Chromium directly to exit over CDP, then
force-terminates only its owned groups and removes the profile. Each process/server
cleanup step has a 2-second bound (the browser also gets up to 500 ms for graceful
CDP shutdown), followed by up to 2 seconds for profile removal. A failed/hung
Playwright close cannot skip these steps. Success is printed only after cleanup.

The failure-path regression checks use a 4-second smoke deadline and verify
nonzero exit, removed profile, refused fixture connection and absent owned
process groups for hung evaluation, failed/hung close and stalled launch:

```sh
TMPDIR=/tmp/opencode xvfb-run -a node extensions/webclipper/scripts/check-smoke-failures.js
```

Fault injection is test-only (`CLIPPER_SMOKE_FAULT`: `hang-evaluate`, `close-fail`,
`close-hang`, `launch-hang`). Packaging tests also bound each synchronous child
build to 10 seconds with SIGKILL. A regression test injects a 1 ms
`CLIPPER_BUILD_TIMEOUT_MS` to verify the child timeout itself causes failure;
Vitest's test timeout is not relied on to interrupt blocking calls.

Override `CHROMIUM_PATH` or `CLIPPER_SMOKE_TMP` if necessary. Sandbox is enabled
by default. `CLIPPER_SMOKE_NO_SANDBOX=1` is an explicit test-environment escape
hatch only, never an extension permission or application requirement. The smoke
passed on Linux with Chromium **154.0.8037.97**, headed via Xvfb, sandbox enabled.

## Dependencies and notices

All direct dependencies are exactly pinned: Svelte 5.56.4, Svelte Vite plugin
7.2.0, Vite 8.3.5 and Vitest 4.1.12. The extension uses the same tooling majors
as `web/`, with independently selected patched Vite/Vitest versions. The plugin's
Vite 8 and Svelte 5 peer requirements are satisfied. Playwright Core 1.64.1 is
development-only. `fake-indexeddb` 6.2.6 (Apache-2.0) is an exactly pinned
development-only IndexedDB test implementation; it is not browser runtime code.
UI tests use exactly pinned `@testing-library/svelte` 5.4.2,
`@testing-library/dom` 10.4.2 and `jsdom` 30.1.3. Only `editor.test.js` selects
jsdom via its file directive; packaging, storage and filename tests retain Node.

- `@mozilla/readability` 0.6.0 — Apache-2.0; `new Readability(clone).parse()`
  mutates its input and is **not a sanitizer**.
- `turndown` 7.2.4 and `turndown-plugin-gfm` 1.0.2 — MIT;
  `new TurndownService().use(gfm).turndown(input)`.
- Svelte and the bundled UI runtime dependencies — MIT.

APIs were checked against official Readability/Turndown documentation via
Context7 and the installed package documentation. Vite's Context7 request timed
out; its official build-options documentation was used instead.
Upstream package license texts were inspected. These extraction dependencies
are reserved for later capture tasks, not exposed as working capture features.

Each build generates `dist/THIRD_PARTY_NOTICES.txt`, copies direct runtime
dependency LICENSE/NOTICE files verbatim to `dist/LICENSES/`, includes the full
Apache-2.0 license, and uses Vite's license inventory for actually bundled
transitive code. Ship the whole `dist` directory, including these notices.

The full `npm --prefix extensions/webclipper audit`, including development
dependencies, reports zero vulnerabilities with this lockfile.

## Local editor behavior (task 6.3)

- `editor.html?draft=<id>` selects only an existing draft ID. A missing ID shows
  an error and requires explicit selection; it never silently opens a different
  draft. No content, token or source URL is read from query parameters.
- Without `draft`, startup selects the most recently updated draft, breaking
  equal-time ties by ID. Selection stays in memory; it does not rewrite the URL.
  A fresh browser reload applies that same URL/default rule.
- **Save is manual.** The indicator distinguishes unsaved edits, saving,
  transaction-complete local save and a failed save. Edits made during a save
  remain unsaved until a subsequent serialized transaction completes. Only
  metadata is updated; stored images and immutable attempt snapshots are retained.
- Selecting a draft or **Refresh drafts** first drains pending edits. A failed
  save blocks that transition and keeps the current text editable, retryable and
  downloadable. Refresh preserves the selected ID. Browser reload/close warns
  while work is unsaved or saving; forced close can still lose unsaved work.
  In-flight saves drain before a component-owned handle is closed on teardown;
  no late completion updates an unmounted UI. Injected handles remain caller-owned.
- **Delete draft** is an explicit permanent action on the selected draft, its
  unsaved edits and its owned stored images/attempts. It waits for any pending
  save, does not need a successful new save, and leaves other drafts intact.
  Storage/list/read/delete errors are shown with **Retry**. Storage retries reopen
  a component-owned handle so a cached open failure or terminal connection can
  recover without discarding in-memory edits; injected handles stay caller-owned.
- **Download Markdown** uses the current textarea text, even if saving failed.
  It is raw Markdown only: title/source/time remain separate metadata, no YAML
  frontmatter is generated (planned for 7.4), and images are not packaged/exported.
  The download uses a local Blob URL, a sanitized basename ending in `.md` (or
  `draft.md` fallback) and revokes the object URL after use.
- Markdown is a plain textarea with no HTML preview or external image loading.
  Titles, filenames, paths and metadata are escaped text. The source link is
  enabled only for absolute HTTP(S) URLs without credentials and opens with
  `noopener noreferrer`. This UI makes no network or Chrome storage/sync calls.

UI tests seed real fake-IndexedDB fixtures; delayed/failing adapters exercise
transaction timing and quota-like failures. Production never seeds example data.

## Local draft storage API (task 6.2)

`src/storage/drafts.js` exports `createDraftStore`, `DraftStorageError`,
`MAX_DRAFTS` (50) and `MAX_BLOB_BYTES` (250 × 1024²). It uses native IndexedDB,
without Chrome APIs, network requests, Node buffers or a runtime dependency.
Worker and editor callers can each keep one instance; independent handles/tabs
share the same database. There is no JavaScript-global lock.

```js
import { createDraftStore } from './storage/drafts.js';
const store = createDraftStore(); // lazy open, database igonotes-webclipper v1
await store.saveDraft({
  id: 'draft-1', title: 'Example', filename: 'example.md', markdown: '# Example',
  sourceUrl: 'https://example.org/article', capturedAt: new Date().toISOString(),
  base: null, parent: '', destinationRevision: null, status: 'draft',
}, { assets: [{
  id: 'image-1', normalizedUrl: 'https://example.org/image.png',
  descriptor: { mime: 'image/png', status: 'ready' }, blob: imageBlob,
}] }); // resolves only after transaction complete, not request success
const drafts = await store.listDrafts();
const metadata = await store.getDraft('draft-1'); // undefined if absent
const assets = await store.getAssets('draft-1');
const attempts = await store.getAttempts('draft-1');
await store.saveDraft({ ...metadata, title: 'Edited' }); // keeps all assets/attempts
await store.deleteDraft('draft-1'); // explicit, atomic deletion of owned rows
store.close(); // terminal for this instance; create another instance to reopen
```

The example's `imageBlob` is a caller-prepared native Blob. Downloading, hashing,
image validation and manifest building belong to later tasks. Prepare asynchronous
work **before** saving: transaction callbacks issue only IndexedDB requests and
synchronous computation, avoiding auto-commit across network/crypto awaits.

### Version 1 schema and ownership

- `drafts`, key `id`: required `id`, `title`, `filename`, `markdown`, `sourceUrl`,
  `capturedAt`, `base` (string/null), `parent` (string, root is `''`),
  `destinationRevision` (string/null), `status`; storage sets ISO `createdAt` and
  `updatedAt`, preserving the original creation time on update. Incoming timestamp
  fields may be round-tripped but cannot override those stored values.
- `assets`, compound key `[draftId, id]`, nonunique `draftId` ownership index:
  `draftId` is assigned from the saved draft, never from caller asset metadata.
  Required `id`, `normalizedUrl`, JSON-object `descriptor`, `blob` (Blob/null).
  `size` is always derived from `blob.size` (zero for null), ignoring caller size.
  Null supports metadata-only image acquisition records for future callers.
- `attempts`, compound key `[draftId, id]`, nonunique `draftId` index: append-only
  `{ draftId, id, createdAt, manifest, deliverySnapshot }`. Caller supplies an
  object-shaped JSON `manifest` and
  `deliverySnapshot: { endpoint, assetIds, digest }`. `endpoint` is an HTTP(S)
  origin without credentials, path, query or fragment; it is normalized with a
  trailing slash. `assetIds` is a unique array of draft-local asset IDs; `digest`
  is a nonempty string. This is persistence shape validation, not protocol/digest
  verification or endpoint authorization. A duplicate attempt ID aborts the
  entire save instead of replacing the snapshot.

Asset `normalizedUrl` has its own source contract, separate from page URLs and
attempt endpoints. HTTP(S) asset sources are normalized as before, without URL
credentials. It also accepts `data:image/png`, `data:image/jpeg`, `data:image/gif`
and `data:image/webp` sources with a nonempty payload:

- `data:<approved MIME>;base64,<payload>` uses the standard base64 alphabet,
  conventional padding or an unpadded final group; whitespace is rejected.
- `data:<approved MIME>,<payload>` uses URL-safe ASCII and/or complete `%HH`
  byte escapes (binary octets need not form UTF-8).

The scheme/MIME/base64 marker are case-insensitive; extra MIME parameters,
fragments, malformed escapes/base64, SVG, text and script schemes are rejected.
For data sources the **exact input string** is persisted, including case and
percent-escape spelling, so future acquisition/lookup/rewriting can use the same
key. There is no synthetic HTTP alias or data URL canonicalization. The source
string is bounded at **32 × 1024² characters**, enough for a protocol-sized
10 MiB image even with every byte percent-encoded. Oversized sources fail with
`invalid_data` without truncation or changes to saved work. For supported data
sources every character is ASCII; this is a metadata bound, not an image quota.
Blob usage is still calculated solely from actual `Blob.size`, regardless of the
source length, encoding or caller-supplied size. Duplicate encoded metadata adds
browser storage overhead outside the 250 MiB Blob cap. Storage checks source
representation only: task 8.1 must decode and validate actual image bytes, MIME,
signature and image limits before treating an asset as acquired. No decoder or
network logic is implemented here. Page `sourceUrl` remains HTTP(S)-only, and
attempt endpoints retain their separate origin-only validation.

JavaScript record properties use **camelCase**. The future transport layer must
explicitly map fields such as `sourceUrl`, `capturedAt` and `destinationRevision`
to the Go wire contract's `source_url`, `captured_at`, `destination_revision`.
The JSON manifest is caller-prepared wire data; storage never transforms its keys.
JSON values must be finite, well-formed Unicode, plain JSON, at most 64 levels
deep. Unknown top-level record fields are rejected. Auth/settings-like keys
(token, secret, password, authorization, credentials, cookies, headers, endpoint)
are rejected recursively in descriptors/manifests. Plain article/Markdown strings
remain content, not a credential scanner. Callers must never place secrets in
content fields. No auth credential belongs in these records; trusted local token
settings are reserved for task 6.4, separately in `chrome.storage.local`, never
`chrome.storage.sync`.

`saveDraft(draft, { assets = [], deleteAssetIds = [], attempt })` is a full
metadata replacement plus explicit asset upserts/deletions and optional attempt
append, all atomic. Omitted assets are retained. Duplicate/conflicting asset
updates are invalid. Attempts own **metadata snapshots**, referencing assets in
the same draft; they do not duplicate Blob bytes. Future task 8.5 callers must
build the immutable manifest/delivery snapshot, verify references, and preserve
the referenced Blob IDs/bytes (no overwrite/deletion while replay is needed).
This module implements no delivery state machine, reference freezing, network
replay, automatic cleanup after success, or concurrency revision protocol.
Concurrent metadata edits across tabs are last-committed-write-wins; this editor
serializes its own writes only. Future task 8 will coordinate editing restrictions
for pending/uncertain delivery. Returned values are structured clones, not live records.

The v1 upgrade creates missing stores/indexes without deleting existing stores
or records. Opening/reopening never runs garbage collection or `deleteDatabase`.
Future schema versions must add explicit nondestructive upgrade steps. A blocked
upgrade reports an error; a late-opened handle is closed. A `versionchange`
closes the connection and requires reopening the editor. Calling `close()` stops
new operations; already-started native transactions may finish normally.

### Quota, failures and manual deletion

All saves/deletions use one readwrite transaction spanning **all three stores**.
Global draft count and Blob usage checks occur in that same transaction as the
writes, serializing even two independent tabs/instances. Updating an existing
draft is allowed at 50 drafts. Blob usage counts all asset rows, including
orphans, using actual Blob sizes and exact replacement/deletion deltas. The
250 MiB cap is Blob payload only, not a promise about metadata/index overhead
or available browser storage. Tests inject smaller caps through
`createDraftStore({ indexedDB, name, limits: { maxDrafts, maxBlobBytes } })`.

Errors have a stable `.code` and actionable `.message`: `invalid_data`,
`draft_limit`, `blob_limit`, `storage_quota` (native `QuotaExceededError`),
`storage_conflict`, `storage_cancelled`, `storage_unavailable`, `storage_blocked`,
`storage_version_changed`, `storage_closed`, or `storage_error` (other failures,
with the native cause retained). Failed transactions roll back metadata, Blob
updates, removals and attempts together. Browser quota can be smaller than the
application cap; a quota failure preserves previous saved work. Callers must keep
unsaved edits and report errors rather than displaying “saved”.

There is **no automatic eviction/GC** on open, save or quota failure.
`deleteDraft(id)` deletes only that draft and asset/attempt rows selected by its
ownership index, including orphan rows if its metadata is already missing.
Identical asset/attempt IDs in another draft cannot cause cross-draft deletion.
`deleteAssetIds` also permits deliberate image removal within a save. No API
silently deletes unrelated work to make space.

Storage is local to this extension origin in this browser profile. It does not
sync or back up to IGoNotes or the cloud. Uninstalling the extension, deleting
the profile or clearing extension/browser storage can lose all drafts and Blobs;
browser eviction is also possible. This is not an archive guarantee. The editor
can download the current Markdown text; settings and image export are not available.
