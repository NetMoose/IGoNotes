# IGoNotes Web Clipper — durable Markdown capture and local draft editor

An independently built JavaScript ESM / Svelte 5 / MV3 package
with native IndexedDB storage and a local draft list/editor. The popup opens a
separate editor tab (also the extension's options page).

> **Available:** list/reopen existing local drafts; edit title, filename and plain
> Markdown; read source URL and capture time; save, retry, explicitly delete and
> download the current Markdown; manually save/clear connection settings and test
> the read-only protocol connection; capture Article/Selection as converted Markdown,
> commit a local draft, and open it in a separate editor tab; suggest a portable
> filename and export safe YAML frontmatter. Tasks 1–7 are implemented/documented.
> **Module-only (8.1–8.2):** explicit image acquisition, durable preparation and Markdown reference scanning APIs/tests, not wired to capture/editor.
> **Planned:** image preparation/permission UI and
> the extension send UI (8). Captured image URLs are pending metadata only; no image
> bytes are downloaded. A fresh profile has no drafts until the first capture.

## Quickstart: build, Load unpacked and capture locally

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
3. Open the source **HTTP(S) main page**. For **Selection**, select a rich range
   in that page **before opening the popup**; a range containing a real `img`
   element can be captured, including an image-only selection.
4. Open the extension popup and explicitly click **Article** or **Selection**.
   **Article** uses best-effort Readability extraction on a clone, not the whole
   page DOM. **Selection** uses only the selected main-document range. Capture
   converts to Markdown, commits a new local draft, then opens its editor tab.
5. Wait for **Saved locally**. This confirms a completed IndexedDB transaction;
   closing the popup now loses no saved draft. If the editor tab could not open,
   use **Open saved draft** to open the **same ID**, or **Open draft editor** to
   find it in the list. Repeating the entire capture intentionally creates a new
   draft; it is not a retry of opening the existing one.
6. In the editor, review **Title**, **Filename**, **Source URL**, **Captured at**
   (UTC), and **Markdown**. Click **Save** to persist edits; **Unsaved changes**
   identifies pending edits. Selecting another draft or **Refresh drafts** first
   saves pending edits and blocks navigation if saving fails. **Download Markdown**
   exports the full current note (safe metadata frontmatter plus body), including
   unsaved edits, without image files. Browser close/reload can still lose unsaved
   edits if its warning is bypassed.

Local capture/edit/download needs neither IGoNotes running nor a token or
**Test connection**. Capture makes no backend or image requests and reads no
connection credentials. Image URLs are pending metadata (`blob: null`), not
acquired images; image acquisition and delivery remain task 8. The editor is a
plain textarea and does not load those images. Exported Markdown may still
reference external HTTP(S) images/links: opening links or rendering the export
elsewhere can access the network as a separate user action. This is not yet a
fully self-contained offline image archive.

### Capture failures and supported sources

- **Article** may fail to identify useful content even on an HTML page. The fixed
  message is `Could not extract an article. You can select content and choose Selection.`
  Select content yourself, reopen the popup and explicitly choose **Selection**.
  There is no automatic mode switch, whole-page DOM fallback or text fallback.
  Readability is best effort, not a guarantee of detecting every empty article.
- For an empty/collapsed/whitespace-only or foreign-frame selection, the message
  is `Select non-empty content in the main page, then try Selection again.`
  A real image in the cloned range is allowed even without text; unsupported
  content can still leave nothing after conversion. That reports
  `No supported content remains after conversion. Select content and try again.`
  Conversion failure asks for a smaller fragment, without saving a partial draft.
- Only HTTP(S) HTML/XHTML main documents without URL credentials are supported.
  No PDF, iframe (including cross-origin iframe), screenshot, canvas or shadow-DOM
  extraction, and no paywall bypass is provided. `chrome://`, `file://` and
  extension URLs report `Only HTTP(S) pages without URL credentials can be captured.`
  An accessible PDF/non-HTML document reports `PDF and non-HTML documents are not supported.`
  If the browser blocks injection (including its PDF viewer), the fixed message
  can instead be `Could not capture the active page. Check page access and try again.`
  These errors do not switch modes or expose native error details.
- Navigation during capture reports
  `The active page changed. Open the popup on the source page and try again.`
  Return to the intended source and explicitly capture again. Literal HTML/script
  examples in code remain code, not executable scripts; actual script elements
  are discarded during safe conversion.
- A quota/transaction failure or the **50-draft limit** preserves old work with no
  automatic eviction. **Not saved** means the current material is only in the
  popup: **keep it open until Download current Markdown has downloaded the file**.
  If download fails, keep it open and try downloading again. Capture buttons are
  disabled while this unsaved material is present. The download includes current
  metadata as exactly three safely quoted YAML keys (`title`, `source`,
  `clipped_at`) plus the Markdown body, without image files. Manage old drafts
  manually through **Open draft editor**; that does not save the failed capture.
  Markdown exceeding **5 MiB UTF-8** likewise remains downloadable, without
  truncation, and asks for a smaller selection. After securing the download,
  reopen the popup on the source to make a new capture.

### Optional: configure and test the IGoNotes connection

This is separate from local capture; the extension send UI is still planned.

1. Run IGoNotes **on the same computer**, finish its first-run base setup and keep
   it running. Default: `http://127.0.0.1:8080`. For a custom port, for example:

   ```sh
   igonotes --port 9000
   ```

2. In the IGoNotes web app, open **Настройки → Веб-клиппер**, enter
   **Название подключения**, and click **Создать подключение** once. Manually copy
   **Секрет подключения**: it is shown only once. If lost, revoke that connection
   and create a new one. Never paste the token into a URL.
3. Open the extension action/popup, click **Open draft editor**, then
   **Connection settings** in the separate editor tab. Enter **Endpoint** (for the
   example, `http://127.0.0.1:9000`) and paste the secret into **Connection token**.
   Only HTTP `127.0.0.1` or `localhost`, ports **1–65535**, and the origin/root path
   are accepted; a trailing `/` is allowed. No non-root path, credentials, query,
   fragment, HTTPS or remote host. IPv6 is not declared supported and is rejected.
4. Click **Save connection**. `Connection saved locally. Not tested.` means only
   local storage was updated: no network request was made. Click **Test connection**
   explicitly to send `GET /api/clipper/v1/info` with the current inputs (even if
   unsaved). Success is **Connected: protocol v1 is supported.** This validates
   discovery, not page delivery. The server must have completed setup and expose
   compatible protocol v1; a protocol mismatch requires a compatible app/extension
   update. If unavailable, check that IGoNotes is running and the port is correct;
   an authorization error requires a valid, unrevoked token.
5. Close/reopen the editor or reload it, open **Connection settings**, and check
   that the saved endpoint/token persist. **Clear connection** removes only local
   credentials; it neither revokes the server connection nor deletes drafts.
   To revoke access, use **Отозвать** in the app's **Настройки → Веб-клиппер**.

The popup offers **Article** and **Selection** capture and the editor link. Capture
converts the source to Markdown and saves it locally before opening the editor.
**Saved locally** confirms the IndexedDB transaction completed; closing the popup
then keeps the draft. No IGoNotes connection is needed for capture. Drafts can be
edited, manually saved and downloaded with **Download Markdown**. Image
acquisition and the send UI are planned for task 8. The HTTP backend,
including image import/status and recovery, is already implemented; see the
canonical [server API documentation](../../docs/api.md#web-clipper-v1--discovery-и-http-import-доступны).

### Local storage, updates and removal

Drafts/images/attempts live in this extension's **IndexedDB** in this browser
profile. Endpoint/token live separately in trusted-only **chrome.storage.local**,
not browser sync. Neither store is a backup or synchronized with IGoNotes.
**Download Markdown** exports current metadata frontmatter and Markdown body, not all drafts or
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
This historical task-6.5 run is guide/settings acceptance. Capture/formatting smoke
coverage from tasks 7.1–7.4 is described below; image/send and full cross-component
acceptance remain planned for tasks 8–9.

## Build layout and host permissions

`vite.config.js` builds local ESM worker and UI entries with relative asset
paths. `vite.content.config.js` runs separately, emitting one classic IIFE
`content-script.js` with no shared imports, used by isolated-world
`scripting.executeScript({ files })` capture. There are no persistent content scripts,
remote scripts, inline executable scripts or eval permissions. The declared
minimum is Chrome/Chromium 120; the actual smoke version below is newer.

Required hosts are exactly HTTP `127.0.0.1` and `localhost`; match patterns cover
all ports. IPv6 endpoints are **not supported/advertised** by this scaffold:
IPv6 match-pattern compatibility with the minimum supported browser has not
been established. Endpoint settings do not accept `[::1]` without that
compatibility check and a corresponding manifest permission. Optional HTTP(S)
hosts allow the standalone acquisition API to request individual image hosts.
The current capture/editor UI does not call it or request image permissions.

## Image acquisition API (task 8.1 — standalone module)

`src/images/acquire.js` exports `createImageAcquirer(options?)`. It has no imports,
connection/settings/token access, storage writes, message listener, content-script
proxy, background activity, automatic retry or preview creation. Instantiating it
does not request permission or download anything. Capture/worker/editor are not
wired to this module yet; captured images remain pending metadata in the current UI.

```js
const images = createImageAcquirer({ limits: info.limits }); // optional protocol limits
// Future 8.4 trusted-editor Prepare/Retry button handler:
button.onclick = async () => {
  // Use the already available source list. This invokes permissions.request()
  // synchronously, BEFORE the first await, contains(), storage or network call.
  const access = await images.requestAccessFromUserGesture(urls);
  // Display denial/platform failure. Never retry by broadening permissions.
  if (!access.ok) return showAccessError(access.error);
  await flushEditorEdits();
  const prepared = await prepareDraftImages(store, draftId, {
    acquirer: images, signal: controller.signal, progress: showCommittedProgress,
  });
  // Inspect prepared.ok, per-image errors and persistenceError before showing
  // success. This is local preparation, not a delivery snapshot or a send action.
};
```

These are illustrative future caller functions, not an existing UI. Only call
`requestAccessFromUserGesture(urls)` directly on the user-handler stack: do not
await `permissions.contains`, a draft read, or another operation beforehand.
It synchronously validates the sources and deduplicates exact host patterns, then
calls native `chrome.permissions.request({ origins })`; it returns a Promise of
`{ ok, origins, error? }` (invalid sources can fail before origins are available).
Data sources require no permission. Invalid mixed input requests no hosts.
Requested patterns have a specific HTTP/HTTPS scheme and exact hostname, never
`<all_urls>`, wildcard hosts or subdomains. Chrome host patterns cover **all ports**
of that scheme/host; they cannot enforce an individual origin's port. Public IPv6
image literals pass source validation, but unsupported platform match-pattern
requests/queries fail visibly with `permission`; no policy expansion follows.
This does not change the separate IPv4/localhost-only backend endpoint contract.

`acquire(urls, { signal }?)` checks `permissions.contains` for each unique HTTP(S)
source and **never prompts**. Each batch runs at most three workers (including
permission wait, fetch, body read and hash). Concurrent batches are caller-owned;
future UI should serialize Prepare/Retry. Results preserve input order and every
input entry, including duplicate/failed aliases:

- Success: `{ ok: true, url, normalizedUrl, blob, sha256, mediaType, extension, size }`.
  `blob` contains exact original bytes, no re-encoding. `sha256` is bare lowercase
  WebCrypto SHA-256 hex; `extension` is `png | jpg | gif | webp`, `mediaType` is the
  corresponding verified signature MIME, and `size` is actual byte length.
- Failure: `{ ok: false, url, normalizedUrl?, error: { code, message, status? } }`.
  Fixed codes: `permission`, `http` (numeric HTTP `status`), `redirect`, `network`,
  `timeout`, `cancel`, `unsafe`, `unsupported`, `mimeMismatch`, `tooLarge`.
  Native exception details/response bodies are not exposed. Invalid input may lack
  `normalizedUrl`. There are no silently dropped failures or external fallbacks.

HTTP(S) sources normalize via `URL` and discard only their fragments; every original
`url` alias remains in the result for later mapping. Data source keys retain their
**exact original spelling**, including case/percent escapes. Within one batch,
normalized-URL duplicates share acquisition; the 8.2 adapter below also deduplicates bytes across batches and cached images.

### Source, fetch and byte policy

- HTTP(S) without URL credentials only; unsafe schemes, `blob:`, localhost and
  `.localhost` (also trailing-dot forms), private/loopback/unspecified/link-local
  IPv4/IPv6 literals, IPv6 ULA and private IPv4-mapped/compatible literals are
  rejected before permission/fetch. IPv4 checks use the canonical URL hostname,
  catching short, integer, hexadecimal and octal aliases. Shared-address IPv4,
  multicast and reserved high IPv4 literals/IPv6 multicast are also rejected.
  **This is literal-address screening, not DNS-rebinding defense.** Named hosts
  are not resolved/classified by the module; the test fixture deliberately maps
  an ordinary public name to loopback through a pinned test resolver.
- Requests have no caller-supplied headers, Authorization or Cookie. They use
  `credentials: 'omit'`, `redirect: 'error'`, `cache: 'no-store'` and
  `referrerPolicy: 'no-referrer'`. There is no no-cors/credentialed fallback.
  Cookie-protected images can fail; explicit manual replacement/fallback belongs
  to 8.4. Every redirect is blocked **before following**, including same-origin
  and CDN redirects. Replace with a direct URL and explicitly grant its host.
  Native Fetch reports blocked redirects and ordinary network failures as the
  same opaque rejection: `network` explicitly mentions both. `redirect` is used
  only when a response exposes a redirect; no follow-up probe attempts to classify it.
- Default per-image cap: **10 MiB**. Content-Length is an early check, not trusted
  accounting: streamed bytes are capped even for missing/lying lengths and a
  single oversized chunk. No whole-response `arrayBuffer()` precedes the cap.
  Received bytes are immediately copied into 64 KiB slabs (at most 160 parts
  at the default cap), not retained as one entry per network chunk; empty chunks
  retain nothing. Each asynchronous wait removes its own abort subscription when
  settled, and periodic task yields let deadlines/caller cancellation run even
  during immediately resolved tiny/empty reads. Fragmentation adds no data cap.
  Oversize, timeout and caller abort cancel the reader and abort fetch, including
  a hanging body. A default **10-second** per-image deadline covers permission
  query, network/body and asynchronous hash; it is configurable as `timeoutMs`.
- Only PNG/JPEG/GIF/WebP data URI shapes approved by storage/conversion are
  accepted: base64 (padded or valid unpadded final group), or ASCII with `%HH`
  octets. No extra MIME parameters, whitespace, malformed escapes, fragments or
  SVG. Source length is at most **32 MiB ASCII characters**, enough for a complete
  10 MiB percent-encoded image. Decoded size is computed **before** byte-buffer
  allocation/`atob`, then capped by the same image byte limit; no fetch is made.

### Client validation boundary and future integration

The module checks actual signatures and structural preflight for static PNG,
JPEG, WebP and GIF (including animated GIF). Absent/octet-stream HTTP MIME can be
inferred from bytes; another declared MIME must match or gives `mimeMismatch`.
APNG and animated WebP give `unsupported`. Preflight enforces **40,000,000 pixels**
per image/canvas/frame, **100 GIF frames**, and **200,000,000 GIF frame pixels** in
total, walking **all** GIF frames/subblocks before any preview/pixel allocation.
It checks bounded container framing and dimensions, not PNG CRC/inflate, JPEG
entropy, GIF LZW or WebP full decode. It uses no ImageDecoder/createImageBitmap
dependency and allocates no decoded pixel buffers; successful acquisition is
**not full server validation or durable ready status**. The server remains
authoritative for strict container integrity and full decoding of all GIF frames.
The protocol's `asset_bytes`, `image_pixels`, `gif_frames`, `gif_total_pixels`
limits may tighten these defaults via `limits`; invalid nonpositive/noninteger
limits throw at factory creation. Aggregate asset/count/upload quotas belong to
8.3/8.5, not this individual-image module; the 8.2 adapter enforces the local storage cap.

**8.2** persists exact Blobs/hash/descriptors/errors and alias mappings
transactionally through the standalone API below. **8.3** owns destination rewriting and immutable manifests, reusing the 8.2 parser. **8.4**
must supply explicit Prepare/Retry and per-host consent actions, keep failures
visible, and require explicit external-fallback confirmation. Neither capture nor
worker should start downloads automatically. **8.5** owns manual delivery.

Verification: `tests/images-acquire.test.js` covers gesture timing, grant/denial,
platform failure, URL/data policy, all four format signatures/MIME mismatches,
pixel/frame budgets, bounded streams, cancellation, concurrency/aliases, and
credential-free real Fetch with blocked redirect/404 fixtures. On Linux:

```sh
TMPDIR=/tmp/opencode xvfb-run -a npm --prefix extensions/webclipper run smoke:images
```

`tests/images-acquire-bounds.test.js` also runs isolated `--expose-gc` probes while
the body is still open: heap growth from 1,000 to 100,000 one-byte reads is checked
with a generous runtime-noise allowance, for fresh and reused source arrays.
It checks byte/hash preservation across every stride of the small PNG fixture
and 64 KiB boundaries of a 100,000-byte image, removable abort listeners, and
deadline/abort of microtask-only empty/tiny streams whose source `cancel()` hangs.
The parent bounds each child independently so a starved timer cannot hang tests.
For a reproducible retained-memory report (byte values, no exact-V8-size assertion):

```sh
node --expose-gc extensions/webclipper/tests/helpers/images-acquire-probe.js heap-fresh
node --expose-gc extensions/webclipper/tests/helpers/images-acquire-probe.js heap-reused
```

The headed image smoke uses a temporary test-only copy of the built extension
plus the standalone acquisition ESM and a bundled preparation test module (all
relative/bare imports resolved with the production Vite configuration). It does
not add image entry points or UI to production `dist`.
Sandboxed Chromium **154.0.8037.97** passed native missing/pregranted/revoked host
checks, a real trusted click calling native request on an already granted host,
actual `images.example.test` network requests via pinned loopback DNS, GIF bytes/hash,
blocked redirect (target never requested), 404, and omission of a real preseeded
image-host cookie and Authorization. Its trusted setup uses `chrome.developerPrivate`
from `chrome://extensions` only inside the temporary test profile. **Real browser
permission prompt grant/denial was not manually tested**; denial/platform failure
are unit-tested. The existing supervisor bounds launch/evaluation/cleanup, stops
owned processes, closes the server and removes the temporary profile. Packaging
smoke remains separate and still verifies capture makes zero image/backend requests.

## Durable image preparation API (task 8.2 — standalone adapter)

`src/images/prepare.js` exports
`prepareDraftImages(store, draftId, { acquirer, signal?, progress? })`.
The caller must **flush unsaved editor edits first** and serialize its Prepare/Retry
actions. Preparation reads the **current persisted Markdown body**, not capture's
DOM image list. It never changes/serializes that body, title, filename, destination,
draft status, timestamps or attempt history. There is no connection/token access,
permission prompt, automatic capture/worker/UI download, delivery or fallback.
The current textarea editor still only saves text; task **8.4** must supply the
explicit user-gesture permission and preparation UI.

The resolved result contains:

- `ok`: all active images are committed ready and no persistence failure occurred.
- `markdown`: the exact persisted body used for this checkpoint.
- `refs`: ordered scanner results for each actual Markdown image occurrence.
- `assets`: **only active** source mappings, including explicit per-image errors.
- `blobs`: unique canonical owner rows needed by those ready active mappings. An
  owner may have an inactive original URL if another active URL uses its bytes.
- `persistenceError?: { code, message }`: preparation could not be saved. A small
  error-status checkpoint is attempted separately; if it also fails,
  `errorStatusPersistenceError` reports that failure. Neither failure claims ready.

`progress({ status: 'pending' | 'ready' | 'error', total, ready })` runs **only
after transaction complete**, never on acquisition or request success. It reports
a committed checkpoint, not a promise that the editor will never be changed again.
Source failures retain the acquirer's fixed `code/message/status?` (permission,
HTTP, timeout, cancellation, etc.); unsafe edited sources are explicit errors
without permission queries/fetch. Descriptor/byte mismatch gives `invalid_bytes`.
Storage failure can persist a per-image `persistence` status while reporting the
separate storage code. Fix sources/storage or grant access and call Prepare again;
there is no automatic retry or silent external fallback.

### Asset mapping, ownership and lifetime

Existing captured pending IDs are reused when their normalized source still
matches. Each source row has `normalizedUrl` and a JSON descriptor:

```js
{
  status: 'pending' /* or 'ready' / 'error' */,
  active: true,
  aliases: ['https://cdn.example/a.png#figure', 'HTTPS://CDN.example:443/a.png#figure'],
  activeAliases: [/* decoded/resolved destinations present in this body */],
  // Ready only:
  sha256: 'bare-lowercase-hex', mediaType: 'image/png', extension: 'png', size: 123,
  canonicalAssetId: 'draft-local-owner-id',
  // Error only:
  // error: { code, message, status? }
}
```

HTTP(S) URL keys use acquisition normalization (including fragment removal);
**all original decoded and resolved aliases**, including fragment/case/port
variants, are retained for future rewriting. Data URLs retain exact spelling.
Different URLs with the same SHA-256 **and verified MIME/extension** share one
physical Blob per draft, including across batches and cached rows. The owner has
the real Blob and its real nonzero physical row `size`. An alias row has
`blob: null`, physical row `size: 0`, descriptor `canonicalAssetId` pointing to
the owner, and descriptor `size` describing the canonical bytes. It is not a fake
zero-byte ready owner. Quota accounting counts the physical Blob once.

Cache reuse verifies actual Blob size, hash, signature/structural preflight and
descriptor MIME/extension. Valid committed bytes work offline after source page
closure, server unavailability or host-permission revocation, with **no acquirer
call**. Full server integrity validation remains authoritative. Acquisition runs
in batches of at most three; results are committed/released before the next
batch, without retaining all downloaded byte buffers or decoding pixels. There
is no arbitrary URL-count cap: many references/URLs may share one unique asset.
The local **250 MiB** Blob cap is enforced transactionally; the server's unique
asset/count/50 MiB upload preflight belongs to 8.3/8.5. No data is silently evicted.

Removed sources are absent from returned `refs/assets`, and unused owners are
absent from returned `blobs`. Old unreferenced rows/Blobs may remain as evidence;
preparation performs no automatic deletion. **Never build a snapshot by sending
all `getAssets()` rows or by trusting an old `active` flag after text edits.**
Task 8.3 must re-evaluate the current body and use its active mapping/canonical-byte
closure for the immutable snapshot; preparation results alone are not that snapshot.

`store.updateDraftAssets(id, { expectedMarkdown, expectedAssets, assets })` is the
minimal assets-only CAS. It reads the latest draft and global asset usage inside
one all-store readwrite transaction, retains all latest metadata/attempts, checks
the expected body and asset state, then commits upserts atomically. All parsing,
fetch and crypto happens **outside** that transaction. Concurrent body changes
cause preparation to reparse/re-evaluate and skip results for removed URLs;
concurrent title edits are retained. Missing drafts (`draft_deleted`), changed
asset state (`assets_changed`, including manual removal) and live draft statuses
`sending`, `uncertain`, `pending` (`draft_frozen`) stop preparation without
resurrecting or overwriting their bytes. Historical append-only attempts do not
freeze the draft forever: task 8.5 owns transitions out of live statuses and must
coordinate immutable attempt ownership, all editor mutations and explicit deletion.
This guard applies to preparation, not a new general delivery state machine.

### Pure Markdown image scanner

`src/images/references.js` exports synchronous
`scanImageReferences(markdown, sourceUrl)`. The exactly pinned MIT packages
`mdast-util-from-markdown` **2.1.0**, `mdast-util-gfm` **3.1.0** and
`micromark-extension-gfm` **3.0.0** parse CommonMark/GFM without DOM/network.
`decode-named-character-reference` **1.3.0** is also pinned so build/test resolution
can explicitly alias its pure default/worker entry rather than the DOM-based
browser optimization, without changing Svelte's browser conditions. No dependency
is imported from `web/node_modules`.

Results include `url` (resolved against sourceUrl), `destination` (Markdown escape/
entity-decoded spelling), `kind` (`inline`, `full`, `collapsed`, `shortcut`), alt,
title and original `position` (UTF-16 offsets, line/column, including CRLF). Reference
images also include identifier and definition URL/title/position. First matching
definitions and CommonMark whitespace/case folding apply. Undefined references
are plaintext, not fetch candidates. Ordinary links, escaped markers, inline code,
indented code and fences are excluded; GFM tables and images nested in links work.
Unicode/backtick/escaped-parenthesis destinations retain meaningful original ranges.

**Raw HTML image editing is unsupported by acquisition.** The textarea still
accepts/saves it as text, but raw HTML nodes are not interpreted or fetched by
this scanner. Capture's converter emits Markdown images, not raw image HTML.
There is no new HTML-editing restriction or whole-document serialization.
For 8.3, shared link/image definitions **must not be rewritten in place**: expand
the image occurrence inline or copy to an image-only definition, preserving
ordinary links. The scanner supplies definition/occurrence ranges but does no rewriting.

Verification: `image-references.test.js`, `images-prepare.test.js` and assets-only
storage tests use real fake-IndexedDB transactions/close/reopen, real acquirer
bytes, delayed fetches, independent handles, quota failures, live-status freezes,
deleted drafts/assets, >100 URL aliases, and progress-after-complete checks.
`images-bundle.test.js` imports a standalone browser ESM bundle without a DOM.
The headed `smoke:images` additionally checks **native IndexedDB** physical Blob
deduplication and full extension-page close/reopen after permission revocation,
with exact cached bytes and zero acquisition calls; it needs no source page or
IGoNotes process. Full actual image UI/delivery acceptance remains tasks 8.4/8.5/9.1.

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
empty editor after reload, real popup Article/Selection capture to persisted drafts
on a local HTTP article fixture, closing each popup after confirmation and reloading
the selected editor with the exact saved body/source, unchanged fixture DOM,
selection boundaries and scroll, article/selection Markdown conversion with zero
new conversion-induced requests after the live-page resource baseline, and absence
of manifest/runtime, CSP, module, console and CDP errors. Capture makes zero image/backend
requests and zero worker connection-storage reads. It also checks proposed filenames
and actual Blob downloads of unsaved title/body edits with an independent manual filename.
It covers packaging/storage startup and tasks 7.1–7.4,
not the full capture/backend acceptance suite planned for task 9.1. Browser DNS is restricted
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

Failure-path regression checks use a **minimal test-only worker**, not the full
functional suite: it connects to the owned real Chromium session, obtains the
extension service worker and verifies a native evaluation. An IPC `fault-ready`
barrier lets the supervisor arm a **1-second fault deadline before authorizing
injection**. The failed/hung close marker is inside the injected `browser.close()`
implementation, proving the call actually ran. Stalled launch instead arms the
phase after creating the owned browser process and fixture, before waiting for CDP.

An independent **10-second overall deadline** bounds all setup/launch/CDP/phase
work, even if the barrier is never reached. This replaces the old requirement to
finish an ever-growing full functional suite within four seconds just to reach
its close fault. The normal packaging/image smokes still execute their **full**
functional checks with the unchanged default **30-second overall deadline**.
Cleanup retains the independent bounds described above (at most 4.5 seconds
across its parallel process/server steps and subsequent profile removal). The
checker requires completion within overall budget + 6 seconds, checks the phase
and cleanup bound separately, and has an 18-second outer failsafe which must
never fire. It verifies exact injected-error/deadline classification, nonzero
exit, removed profile, refused fixture connection and absent owned process groups.

The checker defaults to **three repetitions of all four modes**. A fifth case per
repetition deliberately uses a 1-second overall / 10-second fault budget to prove
the independent overall watchdog still wins. An optional argument selects 1–5
repetitions (use 3–5 for repeated browser acceptance):

```sh
TMPDIR=/tmp/opencode xvfb-run -a node extensions/webclipper/scripts/check-smoke-failures.js
```

Verified with **three repetitions** on sandboxed Chromium **154.0.8037.97** via
Xvfb, including the independent overall-watchdog case. The phase setup and total
elapsed times, browser version, sandbox flag and cleanup evidence are emitted as
JSON for every run. Neither a missed injection nor an overall timeout masquerading
as a close failure is accepted. This checks harness cleanup; actual functional
coverage remains in the separate full packaging/image smokes.

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
development-only. Marked 18.0.6, marked-highlight 2.2.4 and highlight.js 11.11.1
(MIT/BSD-3-Clause), independently pinned here to match the app's parser/highlight
configuration, are test-only for adversarial rendering assertions; no renderer is
bundled in the extension. `fake-indexeddb` 6.2.6 (Apache-2.0) is an exactly pinned
development-only IndexedDB test implementation; it is not browser runtime code.
`yaml` 2.9.1 (ISC) is a pinned development-only independent YAML 1.2 parser for
frontmatter assertions. It is not bundled or included in runtime license notices.
UI tests use exactly pinned `@testing-library/svelte` 5.4.2,
`@testing-library/dom` 10.4.2 and `jsdom` 30.1.3. `editor.test.js` and
`connection-ui.test.js` select jsdom via file directives; packaging, storage,
transport and filename tests retain Node.

- `@mozilla/readability` 0.6.0 — Apache-2.0; `new Readability(clone).parse()`
  mutates its input and is **not a sanitizer**.
- `turndown` 7.2.4 and `turndown-plugin-gfm` 1.0.2 — MIT;
  `new TurndownService().use(gfm).turndown(input)`.
- Svelte and the bundled UI runtime dependencies — MIT.
- `mdast-util-from-markdown` 2.1.0, `mdast-util-gfm` 3.1.0 and
  `micromark-extension-gfm` 3.0.0, `decode-named-character-reference` 1.3.0 — MIT;
  standalone 8.2 scanner/preparation modules.
  Direct LICENSE texts are included even before UI integration bundles this code.

APIs were checked against official Readability/Turndown documentation via
Context7 and the installed package documentation. Vite's Context7 request timed
out; its official build-options documentation was used instead.
Upstream package license texts were inspected. Readability 0.6.0 and Turndown/GFM
run in the isolated content bundle. Current Turndown rule/escape/plugin APIs were
checked with Context7 and the installed GFM source; Marked's Context7 request timed
out, so its official usage documentation was checked for the test-only parser.
The mdast parsing/position API was checked with Context7; the mdast GFM lookup
timed out, so the official upstream GFM README confirmed `gfm()` /
`gfmFromMarkdown()` integration. Parser package license texts were inspected.

Each build generates `dist/THIRD_PARTY_NOTICES.txt`, copies direct runtime
dependency LICENSE/NOTICE files verbatim to `dist/LICENSES/`, includes the full
Apache-2.0 license, and uses Vite's license inventory for actually bundled
transitive code. Ship the whole `dist` directory, including these notices.

The full `npm --prefix extensions/webclipper audit`, including development
dependencies, reports zero vulnerabilities with this lockfile.

## Extraction API (task 7.1)

On a normal HTTP(S) page, open the extension action and click **Article** or
**Selection**. Opening the action grants temporary `activeTab` access; extraction
runs only after the explicit mode click, in main frame `[0]`, `world: 'ISOLATED'`.
No mandatory all-sites permission, background capture or tab watcher is added.
The popup sends only the chosen mode to the worker; page HTML never enters the
popup or draft store. The worker's durable capture pipeline is described below.
No IGoNotes connection is needed.

- Article uses `new Readability(document.cloneNode(true)).parse()`. An empty or
  failed result asks the user to select content and choose Selection explicitly;
  there is no automatic mode/full-DOM fallback.
- Selection clones only the main document's noncollapsed Range, preserving rich
  fragments and image-only selections. Empty/whitespace/foreign-frame selections
  fail clearly. Neither mode changes live DOM, selection, scroll or styles.
- HTTP(S) sources with URL credentials, browser/extension pages, file URLs, PDF
  and other non-HTML documents are unsupported. No iframe/shadow DOM capture or
  paywall bypass is provided. Restricted-page/permission/navigation errors show
  fixed friendly messages, without native stacks, page HTML or private URLs.

`src/capture/extract.js` exports synchronous `extractPage(document, mode)`;
`mode` is exactly `'article' | 'selection'`. Its JSON-serializable result is
`{ mode, title, html, text, baseURI, sourceUrl, capturedAt }` with UTC ISO time.
**HTML and metadata are untrusted. Readability is not a sanitizer.** The separate
converter sanitizes/normalizes before conversion; no raw HTML is rendered or stored here.
Cloned images retain raw lazy/srcset attrs and a `data-igonotes-current-src`
runtime hint for conversion. The converter validates all hints/URLs. Recognized
syntax-language classes are preserved through Readability for safe normalization.

`src/capture/browser.js` retains `captureActiveTab(mode, { chrome }?)` as a raw
extraction helper and exports `captureActiveMarkdownTab(mode, { chrome }?)` for
the worker. Each snapshots the active tab ID/URL at action
time, injects the classic bundle then calls its isolated extractor with mode/URL
arguments; frame/document IDs and the final tab URL detect navigation. The
stateless IIFE installs no listeners or page-world bridge. It accesses no settings,
credentials, storage or network. `CaptureError.code` is a stable fixed-message
error contract. The popup uses the worker message API, not the raw extraction helper.

Verified with unit/fixture tests and headed, sandboxed Chromium 154.0.8037.97 via
Xvfb. The smoke drives the real popup capture code on a localhost fixture; declared
localhost host permission supplies its automation grant, rather than simulating
a production `activeTab` user grant. Full Chrome/platform acceptance remains 9.2.

## Safe Markdown conversion API (task 7.2)

`src/capture/convert.js` exports synchronous `convertCapture(capture, { document })`.
It accepts the raw `extractPage` result and uses only the supplied document's inert
HTML implementation. It returns JSON data
`{ mode, title, markdown, images, sourceUrl, capturedAt }`, without raw HTML.
Source URL identity and capture timestamp pass through unchanged after validation;
title remains literal metadata; the shared note formatter below handles export.

- Untrusted HTML is parsed only in an inert template and rebuilt with a controlled
  HTML/attribute allowlist in its inactive owner document. Nothing is attached to
  the page; image/frame/CSS loading and custom-element constructors are not activated.
  Scripts, styles, forms/controls, active media and foreign namespaces are dropped;
  ordinary unknown wrappers preserve safe children. A direct list checkbox may
  become an inert GFM task marker; control values are never copied.
- Links resolve against captured `baseURI`, accepting only HTTP(S) without URL
  credentials. Unsupported links unwrap to text and unsupported images to alt text.
  Images prefer real currentSrc hints over lazy/srcset alternatives and recognized
  placeholders. Without currentSrc, lazy candidates precede responsive candidates
  (largest descriptor first) and src. Approved raster data URL shapes are retained,
  but bytes are not decoded or claimed acquired. `images` is a unique source URL
  list for future acquisition, not a download result. No image requests occur.
- Turndown/GFM preserve Unicode, headings, lists, quotes, ordinary tables and code.
  Headerless tables get an empty header row; nested/complex tables containing block
  code or literal code pipes flatten to Markdown blocks, retaining data and literal
  code rather than falling back to raw HTML. Prose and attribute escaping prevent
  HTML/Markdown breakout. Code stays literal, with delimiters longer than any
  contained backtick run and normalized syntax-language hints. Turndown's isolated
  `preformattedCode` option preserves inline-code whitespace before custom rules
  run; code-span padding preserves original edge spaces, including all-space code.
  Multiline inline code uses a fence rather than normalizing newlines to spaces;
  surrounding inline formatting/links unwrap when they cannot enclose that block.
  Adjacent inline-code payloads coalesce in the inert rebuilt DOM, including across
  transparent/empty and whitespace-only formatting wrappers, before one delimiter is selected for the combined
  literal text. This prevents neighboring backtick fences from joining and exposing
  code as HTML or Markdown links, without adding visible separators or comments.
  Nonblank emphasis/strong wrappers retain their formatting. Neighboring separate
  code runs use different fence lengths (at most one extra backtick) so Marked's
  emphasis mask cannot mistake a closing fence plus the next opening fence for a
  span. Controlled inert payload attributes survive Turndown clones and prevent
  code-edge whitespace from being duplicated outside formatting. At direct prose/
  formatted-code boundaries, only the adjacent prose character may be spelled as
  a CommonMark numeric entity to satisfy delimiter flanking without changing text
  or inserting separators. These internal attributes never come from source HTML.
- Empty conversion throws `CaptureError('conversion_empty')`; conversion/resource
  failures use fixed `conversion_failed` messages. There is no extraction-text or
  whole-page fallback and no additional content-size truncation. The worker enforces
  the final protocol Markdown byte limit of 5 MiB UTF-8 without truncating work.

The standalone isolated IIFE additionally exposes
`IGoNotesContentScript.captureMarkdown(mode, expectedUrl)`, with the same
`{ ok: true, value } | { ok: false, code }` envelope and page-URL check as `capture`.
This fixed method combines extraction and conversion for the worker. The isolated
bundle adds no listeners, storage, backend, token or arbitrary-fetch functionality.

## Durable capture message API (task 7.3)

`src/capture/coordinator.js` registers `runtime.onMessage` synchronously on every
worker evaluation. Chrome 120 compatibility uses literal `return true` with
`sendResponse`, not a Promise-returning listener. Only the exact own `popup.html`
URL, matching runtime ID and extension origin are accepted. Subframes, source-page
content scripts (even with the same runtime ID), other extension pages and popup
URLs with query/fragment are rejected. Native extension-tab sender metadata is
accepted only for frame 0 and the exact own popup tab URL.

Schemas are defined and validated in `src/capture/messages.js`:

```js
// Exactly these keys, no URLs, scripts, settings, token or proxy options:
{ type: 'capture', mode: 'article' /* or 'selection' */ }
// Durable success, with no captured text/HTML in the acknowledgement:
{ ok: true, draftId: 'random-uuid', editorOpened: true /* or false */ }
// Fixed allowlisted code, never native exception text:
{ ok: false, code: 'article_empty' }
// Only after a valid capture that cannot be stored (or exceeds 5 MiB):
{ ok: false, code: 'storage_quota', unsaved: { title, markdown, sourceUrl, capturedAt } }
```

Unknown commands/extra keys are rejected before storage, injection or tab operations.
The capture result is strict plain JSON with exactly `mode`, `title`, `markdown`,
`images` (URL strings), `sourceUrl`, `capturedAt`; malformed results fail before
persistence, with no raw-HTML/text fallback. Both worker and popup validate responses.
There is no external/page message listener, `externally_connectable` or fetch proxy.

Order: await trusted-storage protection (without loading credentials) → query the
active HTTP(S) source → isolated frame-0 `captureMarkdown(mode, expectedUrl)` with
document-ID/final-URL checks → real IndexedDB transaction **complete** → open
`editor.html?draft=<encoded-id>` → acknowledge. The editor URL contains only the
ID. The stored draft has `filename: proposeFilename(title)`, `base: null`, `parent: ''`,
`destinationRevision: null`, `status: 'draft'`, plus title/body/source/time.
Title/source/time remain separate from the Markdown body in storage.
Unique image URLs are stored atomically as assets with generated ID,
`descriptor: { status: 'pending' }`, and `blob: null`, never acquired image bytes.

The worker owns the operation and closes only its own store handle after the
transaction settles. Closing/unmounting the popup cannot cancel a committed save;
a closed response channel does not delete or re-create it. Worker restart opens
the same database. Busy popup buttons prevent double-click captures and late
callbacks do not update destroyed UI. If tab opening fails after commit, success
still reports `editorOpened: false`: **Open saved draft** uses the same ID without
re-capturing. **Open draft editor** also offers the existing draft list.

Quota/transaction failures and the 50-draft limit never evict old work or open an
editor for the unsaved capture. The trusted popup receives only the safe current
Markdown/metadata, says **Not saved**, and offers **Download current Markdown** plus
manual space management through the draft list. Closing it loses unsaved material.
Download exports metadata frontmatter and body, without image bytes. A 5 MiB limit failure
similarly keeps the current material downloadable and asks for a smaller selection.
No capture success is claimed until durable persistence succeeds.

Verification: converter fixtures and test-only Marked rendering cover hostile
HTML/namespaces, entity/control-character schemes, attribute breakout, lazy images,
headerless/nested tables and code-fence/adjacent-code attacks. The test renderer
matches the app's Marked + marked-highlight + highlight.js configuration. Headed sandboxed Chromium
**154.0.8037.97** via Xvfb passed both modes with zero new conversion requests,
unchanged source DOM/selection/scroll, and no CSP/runtime/CDP errors. Both modes'
hostile adjacent-code output is also actually rendered in a separate browser page,
checking literal text, absence of injected elements/events and zero new requests.

## Note formatting API (task 7.4)

`src/markdown/note.js` is pure shared runtime code with no YAML dependency:

```js
proposeFilename('Лес: наука 🌲.MD'); // 'Лес_ наука 🌲.md'
formatNote({
  title: 'Лес: наука 🌲.MD',
  sourceUrl: 'https://example.org/article?q=1&lang=ru#section',
  capturedAt: '2026-10-23T12:00:00Z',
  markdown: '# Body\r\nNo final newline',
});
// ---
// title: "Лес: наука 🌲.MD"
// source: "https://example.org/article?q=1&lang=ru#section"
// clipped_at: "2026-10-23T12:00:00Z"
// ---
//
// followed by the EXACT body bytes represented above
```

`formatNote({ title, sourceUrl, capturedAt, markdown })` returns exactly
`---\ntitle: <quoted>\nsource: <quoted>\nclipped_at: <quoted>\n---\n\n` plus
the untouched body. It never parses/removes a body's own `---`, changes CRLF or
adds a final newline. Scalars use JSON double quoting compatible with YAML 1.2,
with explicit Unicode NEL/LS/PS, C1 and YAML-disallowed character escapes. Title
is preserved exactly, including literal HTML-looking data; HTML escaping belongs
to the backend preview. Source remains the original credential-free HTTP(S) URL,
including query/fragment spelling; capture time remains a quoted string with
UTC RFC3339 `Z` and up to nine fractional digits. Invalid Unicode, URL or timestamp
throws an explicit `Invalid note …` error; capture validates metadata before saving.
`validateNoteMetadata({ title, sourceUrl, capturedAt })` shares that validation with
the strict worker/popup schemas without assembling a body or reading credentials.

`proposeFilename(title)` runs only when initially creating a captured draft. It
normalizes the filename to NFC (never title), replaces separators, Windows-invalid
characters and Unicode controls/format/line separators, trims dot/space edges,
avoids Windows device aliases including `CONIN$`/`CONOUT$` and superscript COM/LPT
variants, and appends one lowercase `.md` suffix (`.MD` is normalized). Empty titles
fall back to `clip.md`. The component is at most 255 UTF-8 bytes including suffix,
truncated only between code points.
Device-name protection runs after final truncation and trailing dot/space trimming;
if `_` is needed, one byte is reserved and the protected component is re-bounded.
An empty final stem falls back to `clip`. It is not a collision guarantee: backend
`409 note_conflict` still requires the user to choose another filename.

Editing title, saving or reopening never regenerates the user's filename. Manual
invalid filenames can remain local; future delivery preflight (8.5) must reject
them, not silently sanitize the wire value. Download alone sanitizes the current
filename with the same proposer, leaving the stored/input filename unchanged.
`downloadMarkdown(note, filename)` uses the shared formatter for both editor and
popup quota fallback, exporting current unsaved metadata/body rather than old DB
values. No frontmatter is persisted into the draft body. Future 8.3 upload
snapshots should use this same formatter with `source_url`/`captured_at` also on
the wire; `ClipperManifest` has no new title field.

Shared fixtures in `testdata/clipper/note-format.json` assert exact filename/full
Markdown in JS, parse exactly `title`, `source`, `clipped_at` as equal YAML 1.2
string scalars using dev-only `yaml`, and pass Go manifest validation/strict wire
decode with byte-exact content. They cover quotes, newlines, Unicode, URL queries,
HTML-looking title data, a body's own frontmatter and a 255-byte emoji filename.

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
- **Download Markdown** uses current metadata and textarea text, even if saving failed.
  It prepends safe title/source/time YAML frontmatter; images are not packaged/exported.
  The download uses a local Blob URL, a sanitized basename ending in `.md` (or
  `clip.md` fallback) and revokes the object URL after use.
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
   descriptor: { status: 'pending' }, blob: null,
}] }); // resolves only after transaction complete, not request success
const drafts = await store.listDrafts();
const metadata = await store.getDraft('draft-1'); // undefined if absent
const assets = await store.getAssets('draft-1');
const attempts = await store.getAttempts('draft-1');
await store.saveDraft({ ...metadata, title: 'Edited' }); // keeps all assets/attempts
await store.deleteDraft('draft-1'); // explicit, atomic deletion of owned rows
store.close(); // terminal for this instance; create another instance to reopen
```

The example stores a pending source. The standalone 8.2 adapter above owns
verified bytes/hash/status preparation; manifest building (8.3) remains pending. Prepare asynchronous
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
fragments, malformed escapes/base64, SVG, text and script schemes are rejected
for pending/ready rows. Explicit `status: 'error'` descriptors with an error code
may retain an unsupported edited source as bounded well-formed text for diagnosis;
this never authorizes fetching/rendering that source.
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
representation only: the standalone task-8.1 acquisition module checks decoded
bytes, MIME, signatures and structural budgets before acquisition success; it is
used by the standalone persistence adapter, but not wired into UI. No decoder or
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
The assets-only preparation CAS and live-status guard are described above.
This module implements no delivery state machine, general editor/attempt freezing,
network replay or automatic cleanup after success.
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
Assets-only CAS also reports `draft_deleted`, `draft_frozen`, `markdown_changed`
and `assets_changed`; these do not replace or append metadata/attempts.

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
