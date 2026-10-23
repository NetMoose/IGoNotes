# IGoNotes Web Clipper scaffold

Task 6.1 only: an independently built JavaScript ESM / Svelte 5 / MV3 package.
The popup opens a separate editor scaffold tab (also the extension's options
page). Capture, persistent drafts, editing, endpoint/token configuration, images
and delivery are not implemented. No data is saved or sent by this scaffold.

## Build and load

From the repository root, with Node.js 24:

```sh
node --version
npm --prefix extensions/webclipper ci
npm --prefix extensions/webclipper run verify
```

Open `chrome://extensions`, enable Developer mode, choose **Load unpacked**, and
select `extensions/webclipper/dist`. Open the extension action to see the popup
and follow **Open editor scaffold**. Reload the extension after rebuilding.
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
popup/editor DOM, popup-to-editor navigation, real isolated content-file injection
on a local HTTP fixture, unchanged fixture DOM, and absence of manifest/runtime,
CSP, module, console and CDP errors. It is a scaffold packaging check, not the
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
development-only.

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
