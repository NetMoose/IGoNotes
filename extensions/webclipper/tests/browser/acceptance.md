> Обновление 2026-11-20: задача 9.2 закрыта. Пользователь подтвердил версии ОС/браузеров
> и условие loopback prompt: при отсутствии — NOT PRESENT, подключение проверено отдельно.
> Ручная установка через системный chooser и пользовательские
> проверки Chromium/Chrome/Vivaldi подтверждены пользователем в
> [manual-results-2026-11-20.md](manual-results-2026-11-20.md).
> Основной текст ниже сохраняет исторические результаты и ограничения своих
> запусков; актуальная сверка — [reconciliation.md](reconciliation.md).

# Headed acceptance — task 9.2, 2026-10-26

Human desktop follow-up: [manual protocol](manual-acceptance.md) and
[per-browser report template](manual-report-template.md). They contain no new
execution results; keep completed manual reports separate from this historical run.

## Result and evidence boundaries

**14 checks passed in each browser**, using the actual product `dist/`, real
toolbar capture and native optional-host permission dialogs. Baseline:
`feat/chromium-web-clipper` / `6e5bba4c605db26450b9d4d3e1cca470d1ad1e20`, plus
the uncommitted Git-status read fix below. This was UI-driven headed execution
under Xvfb, not a human-operated desktop session.

**Task 9.2 is not unconditionally signed off.** The user **Load unpacked file
chooser** was not verified, and neither product installation presented a native
runtime **loopback** prompt. No native loopback deny/allow PASS is claimed. All
56 spec scenarios have explicit test mappings below (including the five private-storage scenarios added in the final reconciliation); this inventory
does not remove the remaining installation/permission evidence limits.

| Environment | Observed value |
| --- | --- |
| OS | Linux Mint 22.3 (Zena), Ubuntu noble base, x86_64 |
| Kernel | `7.0.0-38-generic` |
| Official Google Chrome stable | **156.0.8078.47**, extracted official Google Debian package |
| Official executable | `/tmp/opencode/chrome-research-extracted/opt/google/chrome/chrome` |
| Chromium | **156.0.8078.47 for Linux Mint**, `/usr/bin/chromium` (rechecked; no longer 154) |
| Node | `v24.20.0` |
| Display/input | Isolated Xvfb `1280x900x24`; Python ctypes/libX11/libXtst; ImageMagick `import` |
| Sandbox | **ON**, no `--no-sandbox`, permission policies, permission API grants, auto-accept, or disabled web security |
| Extension | Fresh, unmodified product `dist/`, MV3 version `0.0.0`; no research/test entry point |
| Manifest SHA-256 | `ce24dca843f6328186063f4e761f1d8f768eb2a4763983b09bb190789b369d39` |
| Tested Go binary SHA-256 | `84a21d908d4a11353a4731034621b4bac282ac3b2ed161c4c324cce510d63f73` |

The previously researched script/binary were absent: `/tmp/opencode` was empty
at session start. The official Chrome package was fetched from Google's
`linux/direct/google-chrome-stable_current_amd64.deb` distribution and extracted
only under `/tmp/opencode`. A minimal research extension is not product evidence.

### Installation mechanism and native prompts

Both browsers installed the **actual product directory** through browser-level
CDP `Extensions.loadUnpacked`, with `--enable-unsafe-extension-debugging`.
Official Chrome 156 removed the old `--load-extension` switch. This is an honest
**test installation mechanism**, not the user file-chooser flow.

An additional official-Chrome attempt clicked Developer mode and Load unpacked
in `chrome://extensions`. No chooser became visible on the owned Xvfb display;
subsequent native keys reached the browser address bar. A separate
`dbus-run-session` attempt also failed to install, reporting portal FUSE mount
permission failures for `/run/user/1000/doc`. **Chooser installation remains
NOT VERIFIED.** CDP installation started the product without an observed
startup/CSP failure and supported actual toolbar capture.

**Loopback:** Test connection, with a real token/backend at a custom port,
returned “Connected: protocol v1 is supported.” for both `127.0.0.1` and
`localhost`. **No runtime local-network/loopback modal appeared** in either
browser. The product declares these required manifest hosts. No test-only
removal/regrant was passed off as native consent. Loopback connectivity PASS;
native loopback **DENY/ALLOW NOT EXERCISED**.

**Optional CDN:** Initially `chrome.permissions.contains` was false. Product
**Prepare images** produced a real native modal naming **IGoNotes Web Clipper**
and **cdn.example.test**, with Russian buttons “Запретить” (Deny, initially
focused) / “Разрешить” (Allow). **XTest Escape** denied it: visible permission
error, retained draft, **zero image requests**. **Retry images / grant access**
opened it again; **XTest Tab, Enter** allowed it. Permission became present and
a verified **35-byte GIF Blob** was committed to native IndexedDB. The later
batch introduced **images.example.test** and required another real native
deny/allow cycle. That modal listed **both cdn.example.test and
images.example.test**, not a one-host-only prompt. These are **native UI PASS**,
not pregranted smoke results.

On relaunch, the test invokes CDP `Extensions.loadUnpacked` again. Draft/storage
persistence is verified across the actual browser exit/relaunch plus this test
reload; persistence of optional permission grants across a normal user-installed
extension restart is not asserted. The later batch had no images-origin grant,
and denial also left the CDN row without usable access, as visible in evidence.

Product buttons use trusted CDP input; toolbar activation, popup capture buttons
and native modal answers use real XTest input. All pages/images and bare Git
remotes are local. `.test` image names resolve to loopback only in the owned
browser. No external article/CDN, user profile or user Git repository is used.

## Per-browser observations

Case names match `native-acceptance.js`. Every PASS below ran in **both** browser
families at version 156.0.8078.47.

| Check | Chrome | Chromium | Observed assertion |
| --- | --- | --- | --- |
| product-unpacked-CDP-install | PASS | PASS | Product dist loads by the stated CDP mechanism; **not chooser PASS** |
| create-connection-through-product-settings | PASS | PASS | Embedded app → Веб-клиппер → create; once-only secret transferred in memory, then hidden |
| loopback-custom-port | PASS | PASS | Both loopback spellings connect; runtime prompt **ABSENT** |
| actual-toolbar-article | PASS | PASS | Real Extensions menu/action → Article opens durable editor; source body unchanged |
| actual-toolbar-selection | PASS | PASS | Real action → Selection captures selected heading only |
| native-CDN-deny-retains-draft | PASS | PASS | Native Deny; exact host permission error; zero image fetches; draft retained |
| native-CDN-allow-stores-Blob | PASS | PASS | Native Allow; permission present; one fetch; verified local Blob |
| browser-restart-offline-cache-no-refetch | PASS | PASS | Actual Browser.close/relaunch, same profile, source closed; draft/assets identical; offline reload preserves bytes; no refetch |
| interrupted-upload-browser-Go-restart | PASS | PASS | Proxy pauses **request before forwarding body**; no publication; posting snapshot retained across browser and SIGKILL/Go restart; manual exact replay rejects stale destination |
| explicit-reconfirm-and-import-after-restart | PASS | PASS | Explicit destination reconfirmation and Send create exactly one nested note |
| native-second-origin-deny-allow-and-explicit-warning-fallback | PASS | PASS | Native batch prompt includes CDN and newly introduced images host; redirect refused/404 visible; checkbox alone leaves Send disabled; separate confirmation enables fallback; receipt has two literal warning strings |
| real-Git-conflict-gates-product-Send-no-file-changes | PASS | PASS | Real divergent commits + production Git worker merge conflict; POST **409 git_conflict_pending**; worktree bytes/status unchanged; draft retained |
| Git-resolve-through-product-UI-explicit-exact-attempt-retry | PASS | PASS | Explicit base switch; actual resolver radio/apply/complete; manual **Check / retry exact attempt** confirms import |
| ordinary-Git-sync-includes-import | PASS | PASS | Normal sync publishes note to local bare remote; independent fetch shows matching note |

The upload interruption is distinct from holding a **successful response**.
Historical-success replay, interrupted image preparation, worker termination/wake,
revoked-host offline cache, and embedded SPA nested-image rendering also passed
the separate task-9.1 Chromium suite. Those are automated integration results,
not additional native Chrome permission results.

## Pinned local evidence

Final redacted reports/screenshots: `/tmp/opencode/native-chrome/` and
`/tmp/opencode/native-chromium/`. These session-local artifacts are not product
files. Each final `report.json` records 14 PASS cases and cleanup
`processGroupsGone`, `profilesRemoved`, `listenersClosed` = true, errors = `[]`.
Reports omit credentials/configuration, request bodies and note content.

| Screenshot | Official Chrome SHA-256 | Chromium SHA-256 |
| --- | --- | --- |
| cdn-native-deny-dialog.png | `5a926db1af73afecd40d150a2f2dd89bb7875a73e598de965959b5f98d12cc3c` | `df8f71ed6a10de0d0a9bfe72b03985a4e93ad4c99400d5e1718712686e312394` |
| cdn-native-allow-dialog.png | `eb074f04d6788d147d74375cc1675da35f180214a56b7906d9d64d58d56e1fa0` | `51188ecfe876f737cdaf13e2393e97fcd705db98fe4a8e905f1d3639c5ff3744` |
| second-origin-native-deny-dialog.png | `f5864ddcd19c4a30f78357baee9b458a6bf86791e80b724652918c290f8e8c86` | `2d48ec427310677751a79575f875105c3a4fe5bb820f23e32d23abfaa0c21e3c` |
| second-origin-native-allow-dialog.png | `fc8f66b07224cde90aa61ddd80680c4fded1f553406a08379522b3a84993fcfd` | `43e04d023be0266a72e9533b7d228edddb7991bb7db68d642e9ddadb353cca5f` |
| git-conflict-retained-draft.png | `6f7272da45fa891d203e4f33ce8d70c892159078091fea02fdb60b6ea42ffd4b` | `b906ab3e364a5d79bcb88c926d05cc2e9ae6c32abc0f2ee072816df44519f591` |

Native screenshots were inspected for product name and exact requested host.
Password inputs were masked; the application's once-only secret was hidden
before screenshots. Profiles, private Go HOME/SQLite/staging, browser/backend/
Xvfb process groups and listeners were identity-tracked and cleaned. Evidence
directories intentionally remain under `/tmp/opencode`.

## Discovered regression and reliability limit

Before the fix, immediate Git status polling during initialization returned
`500 {"code":"internal_error","message":"Internal server error"}`. Investigation
found Git status Get/List lacked the bounded lock wait already used for Clipper
credential/journal reads. New regression
`TestGitStatusReadsWaitForWriterAndRespectCancellation` failed **before the fix**
in all four get/list × release/cancel cases with **SQLITE_BUSY (5)**. The minimal
fix reuses the existing retry policy, rereads committed conflict state and honors
cancellation. Repository tests and race tests now pass.

The final native runs use the rebuilt binary and remove the diagnostic 1.2-second
initialization wait. The driver still waits 1.2 seconds after merge completion
and ordinary sync; these runs do not establish immediate post-queue polling
reliability. Two earlier Chromium runs after this fix also hit a
**Git-state observation deadline** (one concurrent with heavy Go tests, one
isolated); their terminal state was not captured. The final run with explicit
terminal-state diagnostics passed. Those earlier failures are **not relabeled
PASS**; repeated scheduling reliability under 50ms polling remains unproven.
No speculative scheduler/write-path fix was made.

## Reproduction and verification actually run

From the repository root, Node 24, existing `/tmp/opencode`:

```sh
npm --prefix extensions/webclipper run verify
go build -trimpath -o /tmp/opencode/igonotes-browser ./cmd/api
CHROMIUM_PATH=/tmp/opencode/chrome-research-extracted/opt/google/chrome/chrome \
  CLIPPER_SMOKE_GO_BINARY=/tmp/opencode/igonotes-browser \
  CLIPPER_NATIVE_OUTPUT=/tmp/opencode/native-chrome \
  node extensions/webclipper/tests/browser/native-acceptance.js
CHROMIUM_PATH=/usr/bin/chromium \
  CLIPPER_SMOKE_GO_BINARY=/tmp/opencode/igonotes-browser \
  CLIPPER_NATIVE_OUTPUT=/tmp/opencode/native-chromium \
  node extensions/webclipper/tests/browser/native-acceptance.js
CLIPPER_BROWSER_TMP=/tmp/opencode npm --prefix extensions/webclipper run test:browser
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./internal/model ./internal/repository ./internal/service ./internal/handlers ./cmd/api -run 'Clip|Origin' -count=1
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./internal/repository ./internal/service ./internal/handlers ./cmd/api -count=1
go test -race ./internal/repository -count=1
```

Results: extension **33 files / 810 tests PASS**; task-9.1 Chromium **24 functional
checks + 5 fault cases PASS**; listed Go packages and repository race tests PASS.
The full task-9.1 suite preceded the Git-status fix; rebuilt native runs and all
listed Go packages verified the fix afterward. Task 9.4's full project-wide merge
command set is not claimed.

The opt-in native driver requires Linux/Xvfb, Python, libX11/libXtst,
ImageMagick, current dist and an already-built Go binary. It is not added to CI
or `test:browser`. Toolbar coordinates are pinned to the recorded default window
geometry/builds: inspect screenshots before reuse with different geometry or
locale. Blind keys alone do not establish a native-consent PASS.

## Complete scenario → executed test mapping

**TEST PASS** identifies executed automated coverage, not human manual coverage
of every permutation. Prefixes: **E** = `extensions/webclipper/tests/`, **S** =
`internal/service/`, **H** = `internal/handlers/`, **R** = `internal/repository/`.
**N** = per-browser headed check above. Every one of the **56 scenarios** in the
four delta specs has a test mapping. Chooser limitations are explicit.

### web-clipping/spec.md — 17 scenarios

| Exact scenario | Evidence / result |
| --- | --- |
| Capture article | TEST PASS E:capture.test.js; browser normal domUnchanged; N actual-toolbar-article PASS both |
| Capture selection | TEST PASS E:capture.test.js; browser normal selectionUnchanged; N actual-toolbar-selection PASS both |
| Unsupported or empty input | TEST PASS E:capture.test.js (PDF/empty/no article), E:capture-tab.test.js, E:popup.test.js |
| Preserve common formatting | TEST PASS E:convert.test.js; browser normal markdownConversion/adjacentCodeRenderedSafely |
| Edit before import | TEST PASS E:editor.test.js, E:delivery.test.js; browser normal durable edits/reload/download |
| Download images before server availability | TEST PASS E:images-prepare.test.js; browser restart offlineCapturedDraft; N image acquisition performs no Go request |
| Denied permission or failed image | TEST PASS E:images-acquire.test.js, E:image-preparation-ui.test.js; N native denial/fallback PASS both |
| Relative images and duplicate references | TEST PASS E:convert.test.js, E:images-prepare.test.js, E:image-rewrite.test.js |
| Decode limits or unsupported animation | TEST PASS E:images-acquire.test.js (frame/pixel/APNG/animated WebP), E:image-preparation-ui.test.js |
| Restart browser | TEST PASS browser restart originalIDManifestBlobsPreserved; N browser-restart-offline-cache-no-refetch PASS both |
| Interrupted operation | TEST PASS E:delivery-restart.test.js; browser restart partialPreparationDurable/worker wake; N interrupted upload PASS both |
| Storage full | TEST PASS E:drafts.test.js (quota/rollback), E:editor.test.js (unsaved Markdown download), E:popup.test.js |
| Server offline | TEST PASS E:delivery.test.js, E:delivery-restart.test.js, E:delivery-ui.test.js (no online-event delivery) |
| Missing destination | TEST PASS E:delivery.test.js, E:delivery-restart.test.js; browser restart oldDestinationRequiresExplicitReconfirm; N stale rejection |
| Confirmed import | TEST PASS E:delivery.test.js; browser delivery receipt/cleanup; N explicit reconfirm/import |
| First installation | TEST PASS E:package.test.js (build/CSP/package); N product CDP install PASS both; **user directory chooser NOT VERIFIED** |
| Scoped page access | TEST PASS E:package.test.js, E:capture-tab.test.js; browser normal trustedStorageOnly; N optional permission absent before click |

### clipper-connection/spec.md — 13 scenarios

| Exact scenario | Evidence / result |
| --- | --- |
| Create connection | TEST PASS S:clipper_connection_service_test.go, H:clipper_connection_handler_test.go, R:clipper_connection_repo_test.go; N real settings UI |
| Revoke connection | TEST PASS S:clipper_connection_service_test.go, H:clipper_guard_test.go; E:delivery.test.js auth failure retains draft |
| Restart application | TEST PASS S:clipper_connection_service_test.go, R:clipper_connection_repo_test.go; N Go restart reuses token |
| Authorized extension request | TEST PASS H:clipper_guard_test.go, H:clipper_discovery_test.go; browser delivery native headers + separately labeled OPTIONS contract |
| Unauthorized website or remote host | TEST PASS H:clipper_guard_test.go; browser delivery hostilePreflightRejected (actual 403 preflight/no POST) |
| Existing API remains guarded | TEST PASS H:clipper_guard_test.go:TestClipperBearerDoesNotBypassOrdinaryOriginGuard |
| Select inactive base | TEST PASS S:clipper_folders_test.go, H:clipper_discovery_test.go; browser delivery activeBaseUnchangedByImport |
| Detect changed base binding | TEST PASS S:clipper_destination_service_test.go, S:clip_import_service_test.go, E:delivery.test.js |
| Exclude invisible note destinations | TEST PASS S:clipper_folders_test.go, H:clipper_discovery_test.go |
| Setup or protocol mismatch | TEST PASS H:clipper_discovery_test.go; E:connection.test.js, E:connection-ui.test.js distinct setup/protocol errors |
| Non-default port | TEST PASS E:connection.test.js; browser delivery custom port; N loopback-custom-port PASS both (**native prompt absent**) |
| Invalid endpoint | TEST PASS E:connection.test.js (remote/credentials/query/fragment blocked before fetch), E:connection-ui.test.js |
| Untrusted page message | TEST PASS E:capture-messages.test.js, E:capture-coordinator.test.js, E:connection-worker.test.js; browser normal trustedStorageOnly |

### clip-import/spec.md — 21 scenarios

| Exact scenario | Evidence / result |
| --- | --- |
| Import to inactive base | TEST PASS S:clip_import_service_test.go:TestClipImportActiveInactiveReceiptReplay; browser delivery activeBaseUnchangedByImport |
| Missing or changed destination | TEST PASS S:clip_import_service_test.go:TestClipImportPreflightGates, S:clipper_destination_service_test.go; N stale handle rejection |
| Manually supplied excluded destination | TEST PASS S:clip_import_service_test.go:TestClipImportExcludedNoteParentsRejectedBeforeClaim |
| Visible nested destination | TEST PASS S:clip_import_service_test.go:TestClipImportVisibleNestedParentsAppearInTree |
| Filename collision | TEST PASS S:clip_import_filesystem_test.go, S:clip_import_publication_test.go (concurrent collision/no replacement) |
| Invalid path | TEST PASS S:clip_import_manifest_test.go, S:clip_import_filesystem_test.go (symlink/confinement), S:clip_portable_paths_fixture_test.go |
| Nested destination | TEST PASS E:image-rewrite.test.js, S:clip_import_publication_test.go; browser delivery backendPreviewImageBytes (embedded SPA/raw image hash) |
| Invalid or oversized attachment | TEST PASS S:clip_import_assets_test.go, H:clip_import_assets_test.go |
| Explicit external fallback | TEST PASS E:image-preparation-ui.test.js, S:clip_import_warnings_test.go, H:clip_import_publication_test.go; N native second-origin fallback |
| Corruption beyond image configuration | TEST PASS S:clip_import_assets_test.go:TestClipGIFCorruptLaterFrame, TestClipImagePNGCRCAndWebPFrameTruncation, TestClipImageRejectsHeaderOnlyAndTrailingIDAT |
| Decode budget exceeded | TEST PASS S:clip_import_assets_test.go:TestClipGIFPreflightPixelBudgetsWithoutAllocation, TestClipImagePreflightBudgetsAndAnimations, TestClipImagePreflightHeaderPixelBoundaries |
| Unsupported animation | TEST PASS S:clip_import_assets_test.go:TestClipImagePreflightBudgetsAndAnimations; E:images-acquire.test.js |
| Lost success response | TEST PASS H:clip_import_handler_test.go:TestClipImportHTTPReceiptLostResponseConcurrentRotationRestart; browser restart historicalExactPOSTReplay |
| Concurrent duplicates | TEST PASS H:clip_import_handler_test.go:TestClipImportHTTPConcurrentPendingAfterClaimResolvesExactReceipt; S:clip_import_service_test.go:TestClipImportConcurrentID |
| Payload changed under same identifier | TEST PASS R:clip_import_repo_test.go:TestClipImportClaimReplayAndMismatch; S:clip_import_service_test.go:TestClipImportCompetingClaimDigest |
| Failure before publication | TEST PASS S:clip_import_service_test.go:TestClipImportFailureBoundaries; S:clip_import_publication_test.go |
| Restart during publication | TEST PASS S:clip_import_recovery_test.go (subprocess crash/ownership boundaries); S:clip_import_git_integration_test.go:TestClipStartupRecoveryNativeGit |
| Abandon ambiguous import | TEST PASS S:clip_import_abandon_test.go, H:clip_import_management_test.go; E:delivery.test.js abandoned handling |
| Pending conflict | TEST PASS S:clip_import_service_test.go:TestClipImportPreflightGates, S:clip_import_git_integration_test.go; N real conflict + UI resolve/exact retry PASS both |
| Active base refresh | TEST PASS S:clip_import_service_test.go:TestClipImportActiveReceiptOrderingAndExactBytes, TestClipImportVisibleNestedParentsAppearInTree |
| Ordinary Git synchronization | TEST PASS S:clip_import_git_integration_test.go:TestClipStartupRecoveryNativeGit (active and target ordinary-sync); N ordinary-Git-sync-includes-import PASS both |

## Remaining acceptance work

1. Verify the documented user **Load unpacked directory chooser** flow with a
   functioning desktop chooser. CDP installation is accurately recorded, not
   substituted for that UX.
2. Product loopback native deny/allow cannot be recorded on these builds because
   no runtime dialog appeared. Exercise an actual presented product prompt on a
   supported build/environment before claiming that native result.
3. Investigate the two Git-state wait timeouts if repeatable aggressive polling
   is required for sign-off; final successful runs do not erase them.

No `tasks.md` changes, staging or commits were made.


### private-storage-boundaries/spec.md — 5 scenarios

Дополнено при финальной сверке 2026-11-20. Автотесты исполняются в полном Go suite;
это не дополнительные ручные результаты старого headed-прогона.

| Exact scenario | Evidence / result |
| --- | --- |
| Overlapping base root | S:private_storage_test.go:TestPrivateStorageSettingsRejectWithoutWrites; H:private_storage_test.go:TestPrivateStorageSettingsHTTP |
| Default base remains supported | S:private_storage_test.go:TestPrivateStoragePaths (data/bases/default allowed) |
| Alias and not-yet-created private path | S:private_storage_test.go:TestPrivateStorageMissingAncestorsAndRetarget; S:private_storage_alias_test.go:TestPrivateStorageProspectiveNativeAliases |
| Configuration change during upload | S:private_storage_test.go:TestPrivateStorageStagingConcurrentRebind |
| Existing incompatible configuration | S:private_storage_test.go:TestPrivateStorageStartupAllBasesAndNoWrites; cmd/api/private_storage_test.go:TestPrivateStorageStartupRejectsBeforeDBOpen |
