# IGoNotes Web Clipper — local draft editor

An independently built JavaScript ESM / Svelte 5 / MV3 package
with native IndexedDB storage and a local draft list/editor. The popup opens a
separate editor tab (also the extension's options page).

> **Available:** list/reopen existing local drafts; edit title, filename and plain
> Markdown; read source URL and capture time; save, retry, explicitly delete and
> download the current Markdown; manually save/clear connection settings and test
> the read-only protocol connection. **Planned:** page capture (7), image
> acquisition and the extension send UI (8).
> A fresh profile truthfully shows an empty list. There is no capture or new-draft
> button yet; future capture task 7.3 will create drafts and open the editor.

## Quickstart: build, Load unpacked and connect

The first delivery is a local source build loaded unpacked, not a Chrome Web Store
listing or an already published release ZIP. Use desktop Chrome/Chromium. The
manifest declares a minimum of 120; Linux Chromium 154.0.8037.97 is the browser
actually checked here. Latest-Chrome/platform compatibility acceptance is still
planned in task 9.2.

1. From your local repository root, check **Node.js 24** before running npm:

   ```sh
   node --version
   npm --prefix extensions/webclipper ci
   npm --prefix extensions/webclipper run verify
   ```

   `verify` runs tests and builds `dist`; no separate global tooling is required.
2. Open `chrome://extensions`, enable **Developer mode**, choose **Load unpacked**,
   and select `extensions/webclipper/dist` (not the repository or `src`). Keep the
   whole directory, including `LICENSES/` and `THIRD_PARTY_NOTICES.txt`.
3. Run IGoNotes **on the same computer**, finish its first-run base setup and keep
   it running. Default: `http://127.0.0.1:8080`. For a custom port, for example:

   ```sh
   igonotes --port 9000
   ```

4. In the IGoNotes web app, open **Настройки → Веб-клиппер**, enter
   **Название подключения**, and click **Создать подключение** once. Manually copy
   **Секрет подключения**: it is shown only once. If lost, revoke that connection
   and create a new one. Never paste the token into a URL.
5. Open the extension action/popup, click **Open draft editor**, then
   **Connection settings** in the separate editor tab. Enter **Endpoint** (for the
   example, `http://127.0.0.1:9000`) and paste the secret into **Connection token**.
   Only HTTP `127.0.0.1` or `localhost`, ports **1–65535**, and the origin/root path
   are accepted; a trailing `/` is allowed. No non-root path, credentials, query,
   fragment, HTTPS or remote host. IPv6 is not declared supported and is rejected.
6. Click **Save connection**. `Connection saved locally. Not tested.` means only
   local storage was updated: no network request was made. Click **Test connection**
   explicitly to send `GET /api/clipper/v1/info` with the current inputs (even if
   unsaved). Success is **Connected: protocol v1 is supported.** This validates
   discovery, not page delivery. The server must have completed setup and expose
   compatible protocol v1; a protocol mismatch requires a compatible app/extension
   update. If unavailable, check that IGoNotes is running and the port is correct;
   an authorization error requires a valid, unrevoked token.
7. Close/reopen the editor or reload it, open **Connection settings**, and check
   that the saved endpoint/token persist. **Clear connection** removes only local
   credentials; it neither revokes the server connection nor deletes drafts.
   To revoke access, use **Отозвать** in the app's **Настройки → Веб-клиппер**.

The popup only opens the editor; it has no capture buttons. A fresh installation
has an empty draft list and no new-draft button. Existing drafts can be edited,
manually saved and downloaded with **Download Markdown**. Capture is planned for
task 7; image acquisition and the send UI are planned for task 8. The HTTP backend,
including image import/status and recovery, is already implemented; see the
canonical [server API documentation](../../docs/api.md#web-clipper-v1--discovery-и-http-import-доступны).

### Local storage, updates and removal

Drafts/images/attempts live in this extension's **IndexedDB** in this browser
profile. Endpoint/token live separately in trusted-only **chrome.storage.local**,
not browser sync. Neither store is a backup or synchronized with IGoNotes.
**Download Markdown** exports only the current Markdown text, not all drafts or
an offline archive with all images. Uninstalling the extension, deleting the
browser profile or clearing its storage can lose drafts, images and credentials;
browser eviction is also possible. **Clear connection** keeps drafts and images.

After rebuilding, use the extension's **Reload** button at `chrome://extensions`
and reopen the editor to load the new code. Keep the same `dist` location and
browser profile for the existing extension storage. A changed unpacked location/
extension ID or a different profile may give a separate, empty store and require
entering the endpoint/token again; it does not migrate the old drafts. Download
needed Markdown before removing an installation/profile.

This package is independent of `web/` and Go embed.

### Clean-install guide acceptance (task 6.5)

Checked on Linux with Node.js **24.20.0** and headed, sandboxed Chromium
**154.0.8037.97** via Xvfb, after a clean `npm ci` and `run verify` (116 tests
and a fresh build). A temporary harness loaded that unpacked `dist` into a fresh
browser profile and launched a Go binary built from the current checkout using
`--no-browser`, a temporary HOME/config and custom port **36977**. First-run setup
was completed through the public setup API with a temporary base; connection
creation and the one-time secret were exercised through the actual app UI,
**Настройки → Веб-клиппер → Создать подключение**.

The actual **Open draft editor → Connection settings → Save connection → Test
connection** flow passed: Save made no discovery request; Test displayed
`Connected: protocol v1 is supported.` and its real GET response contained
`app_name: IGoNotes`, `protocol_version: 1`, `app_version: dev`. Saved credentials
survived reload and close/reopen. Clear removed local credentials without network
requests, kept an explicitly seeded IndexedDB draft across reload, and left the
same server token authorized and its connection unrevoked. The UI does not display
the app name/version; those were checked in the discovery response. The separate
packaging smoke below also passed trusted-only storage isolation with a real
content script. No secret was logged. Owned processes were stopped, the backend
port was confirmed closed and temporary HOME/config/profile data were removed.
This is guide/settings acceptance, not the capture/image/send acceptance planned
for tasks 7–9.

## Build layout and host permissions

`vite.config.js` builds local ESM worker and UI entries with relative asset
paths. `vite.content.config.js` runs separately, emitting one classic IIFE
`content-script.js` with no shared imports, ready for future isolated-world
`scripting.executeScript({ files })` use. There are no persistent content scripts,
remote scripts, inline executable scripts or eval permissions. The declared
minimum is Chrome/Chromium 120; the actual smoke version below is newer.

Required hosts are exactly HTTP `127.0.0.1` and `localhost`; match patterns cover
all ports. IPv6 endpoints are **not supported/advertised** by this scaffold:
IPv6 match-pattern compatibility with the minimum supported browser has not
been established. Endpoint settings do not accept `[::1]` without that
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
popup/editor DOM, popup-to-editor navigation, manual connection save/reopen/clear,
native trusted-only Chrome storage (an isolated content script cannot read it),
no unsolicited connection requests, native IndexedDB v1 stores and the
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
`@testing-library/dom` 10.4.2 and `jsdom` 30.1.3. `editor.test.js` and
`connection-ui.test.js` select jsdom via file directives; packaging, storage,
transport and filename tests retain Node.

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
  `noopener noreferrer`. Draft editing itself makes no network or Chrome storage calls;
  explicitly opening connection settings mounts the separate trusted settings UI.

## Connection settings and read-only transport (task 6.4)

Open **Connection settings** in the editor, enter the local endpoint and manually
paste the connection token into the password field. The default endpoint is
`http://127.0.0.1:8080`; `http://localhost:9000` is an example custom port.
Only HTTP loopback origins whose canonical host is `127.0.0.1` or `localhost` are
accepted. Ports must be 1–65535. Credentials (even empty `@`), non-root paths,
queries, fragments, malformed addresses, remote hosts and IPv6 are rejected before
any credentialed request. A trailing root slash is normalized away.

- **Save connection** writes the inputs locally; it does not test or contact IGoNotes.
- **Test connection** explicitly sends a GET to `/api/clipper/v1/info` using the
  current inputs, including unsaved endpoint/token changes. Success confirms protocol
  v1 and a valid discovery DTO, not delivery. Changed inputs invalidate a pending
  check's result. Closing settings cancels the check and drops the component's token.
- **Clear connection** removes only the local credential settings object, keeping
  IndexedDB drafts, images and attempts. It does not revoke the token on the server.
- Opening/closing settings preserves unsaved draft edits. Opening, saving and clearing
  settings do not trigger network requests. No image permission is requested.

`src/connection/settings.js` exports `DEFAULT_ENDPOINT`, `validateEndpoint`,
`validateToken`, `ConnectionError` and `createConnectionStore({ chrome })`.
The store exposes `load()`, `save({ endpoint, token })`, `clear()` and `protect()`.
`load()` returns credentials only to trusted extension callers. Every get/set/remove
first awaits `chrome.storage.local.setAccessLevel({ accessLevel: 'TRUSTED_CONTEXTS' })`;
missing/failed access control fails closed. The worker restricts access on each
evaluation, install and browser startup as well. No sync/localStorage fallback exists.
Credentials stay in trusted extension memory and this local storage object, never
in drafts/attempts, URLs, content-script messages or logs. Storage is profile-local,
not encrypted credential backup; it is lost when extension/profile data is removed.

`src/connection/transport.js` exports `createTransport({ endpoint, token }, { fetch })`
and `TransportError`. A factory snapshots the validated endpoint/token. Its only
methods are `info({ signal }?)`, `bases({ signal }?)` and
`folders(base, { signal }?)`, restricted to the corresponding `/api/clipper/v1/`
GET routes with encoded query components. There is no arbitrary URL request API,
ordinary note/config/admin access, POST, retry or delivery state machine. Future
task 8 must add typed import operations, native FormData (browser-generated
Content-Type/boundary) and original-endpoint uncertain-attempt policy.

All requests use Bearer authorization, `credentials: 'omit'`, `redirect: 'error'`
and `cache: 'no-store'`. A 10-second deadline includes response JSON parsing;
caller cancellation is a separate error. Redirects have no fallback. Protocol version
must be exactly numeric `1`; app name/version, all eight byte/count/pixel limits and
supported image MIME types are checked against the current Go DTO shape. Friendly
static errors distinguish unavailable, authorization failure, setup required,
protocol mismatch, invalid response, timeout and cancellation. Native network error
text and server error bodies are never displayed or retained as error causes.

Chrome storage access-level and Svelte lifecycle APIs were checked with current
Context7 documentation. This uses native AbortController and signal event listeners
(supported by Chrome 120), not newer `AbortSignal.any()`/`timeout()` helpers.

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
  settings live separately in trusted `chrome.storage.local`, never
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
can download the current Markdown text; credential and image export are not available.
