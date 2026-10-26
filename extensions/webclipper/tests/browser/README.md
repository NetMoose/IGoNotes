# Local MV3 / Go browser acceptance — task 9.1

## Reproduce automated checks

From the repository root, install build/runtime prerequisites **before** testing:
Node.js **24**, Go **1.26+** (including a cached toolchain/modules), Linux system
Chromium, and Xvfb when no `DISPLAY` is available. No browser is downloaded.

```sh
node --version
go version
npm --prefix web ci
npm --prefix web run build
npm --prefix extensions/webclipper ci
npm --prefix extensions/webclipper audit --audit-level=low
npm --prefix extensions/webclipper run test:browser
```

`pretest:browser` runs the extension's complete `verify` (tests + fresh `dist`)
before the browser runner. The runner checks the embedded web app exists and
builds `./cmd/api` from this checkout into its private temporary directory with
`GOPROXY=off` / `GOSUMDB=off`. Missing prerequisites fail with an actionable phase;
they are never skipped or installed at runtime. Rebuild `web/dist` after app changes;
the later task-9.3 CI job must make the fresh web build a dependency.

The runner starts Xvfb using `-displayfd` only on Linux with no `DISPLAY`, and owns
that process group. It reuses `scripts/smoke.js` and all four existing workers;
`delivery-fixture.js` was moved here, not forked. Smoke owns Chromium, worker and
Go groups, the fixture listener, and a unique profile. Each Go instance has its
own HOME/config/SQLite/private staging **outside** both note bases. A stable
loopback HTTP proxy survives actual Go and browser restarts. All fixture pages,
image responses, source capture, application JS and API requests are local HTTP.
Public-shaped `images.example.test`, `cdn.example.test`, and
`hostile.example.test` resolve only to loopback through Chromium resolver rules;
other DNS names are rejected, browser background networking/proxies are disabled,
and an observational request allowlist fails unexpected test traffic. No remote
article/CDN, package fetch, browser download, cookie disabling, web-security
disabling, or response interception is used. In particular, Playwright routing
is deliberately avoided: even a continue-only route can suppress native preflight.

Controls (all optional):

| Variable | Meaning |
| --- | --- |
| `CHROMIUM_PATH` | Browser executable (default `/usr/bin/chromium`) |
| `CLIPPER_BROWSER_TMP` | Existing parent for suite temp files (fallback `CLIPPER_SMOKE_TMP`, then OS temp) |
| `CLIPPER_BROWSER_REPORT` | JSON output filename; parent must exist |
| `CLIPPER_BROWSER_MODE_TIMEOUT_MS` | Per-mode overall smoke deadline, fallback `CLIPPER_SMOKE_TIMEOUT_MS`, default 120000, max 180000 |
| `CLIPPER_BROWSER_TIMEOUT_MS` | Whole browser/build/fault suite budget, default 600000, max 900000 |
| `CLIPPER_SMOKE_CHECK_TIMEOUT_MS` | Standalone checker's external per-supervisor failsafe, default/max 18000; smaller diagnostic budgets preserve failure |
| `CLIPPER_BROWSER_CHECKER_TIMEOUT_MS` | Suite's external checker failsafe, default/max 90000; lowered only by the outer-watchdog regression |

Go build has a 120s cap; every mode has an independent watchdog and 10s outer
allowance, reduced to the remaining suite budget. The five fault checks have a
90s cap; the standalone checker still uses its original 18s external failsafe.
Command waits reserve eight seconds of the remaining suite budget for independent
fallback cleanup; final cleanup/disposal is separately bounded. Standalone
smoke still supports `CLIPPER_SMOKE_TMP` and `CLIPPER_SMOKE_TIMEOUT_MS` (default
30s). On installations that require it, `CLIPPER_SMOKE_NO_SANDBOX=1` is an explicit
diagnostic override; it is not the standard run or an extension requirement.

### Ownership survives stopped/killed watchdogs

`ownership.js` is shared by the suite, standalone checker and smoke supervisor.
Each top-level invocation creates a private `0700` registry with `0600` metadata
and an append-only JSONL journal. A random run nonce and descendant scope travel
through inherited environment variables. Profile/inode, private process-group
PID/start identity (including checker, supervisor, browser, worker, backend and
Xvfb), and fixture listener ownership are synchronously registered before waits
for process completion, CDP or readiness. No token, request body or source text is
written to the journal. Ancestors retain it until their descendants are cleaned.

External waits use async owned spawning rather than `spawnSync`: the parent
knows the child's group before waiting. The standalone checker cleans the
timed-out supervisor's scoped journal itself. If that checker is also SIGSTOP'ed
or killed before forwarding any stdout, the suite reads the same journal and
cleans its descendants directly. `SMOKE_RESOURCES` remains diagnostic output;
cleanup never depends on its delivery or treats arbitrary stdout PIDs as authority.

Cleanup first stops owned command allocators, rereads the journal, asks owned
Chromium to close over local CDP (700ms), then kills only recorded owned groups
and waits up to two seconds for them. Recycled identities need a surviving member
with the matching private run nonce **and descendant scope**; there is no
browser-name-wide kill or authority over a sibling scope's recycled PID. Directory
removal is inode/device checked, independently bounded to two seconds per parallel
phase (profiles before suite parent), and refuses substituted profiles **and their
parent removal**. TCP probes verify recorded listeners are closed. Failed steps
do not suppress the other cleanup phases, and cleanup errors cannot turn the
original external timeout into success. The registry is disposed separately.
Only aggregate cleanup booleans/error categories enter the final JSON allowlist;
private registry paths, nonce and process IDs stay out of it.

Ordinary `smoke.js` cleanup uses this same identity-checked API: there is no local
path-based `rm(profile)` before it. Each smoke invocation derives its own child
scope even when a caller supplies the global registry scope, so cleanup cannot
kill the parent controller. Local process/server shutdown completes before the
guarded directory phase; surviving owned groups block directory removal. An
inode mismatch preserves the replacement and containing suite directories,
logs `SMOKE_OWNERSHIP_CLEANUP` with errors, emits no successful `SMOKE_CLEANUP`
marker and exits nonzero. Registry disposal also checks its original identity
and refuses removal through an unresolved nested profile.

## Automated matrix and evidence

`results.json` (gitignored) contains OS/kernel/architecture, browser version,
per-case PASS/FAIL, overall result/deadline, native header observations, and all
five phase-barrier fault results. It projects an allowlist: no source DOM/text,
request bodies, Authorization values, token, profile/HOME/config paths, or raw
worker errors. Any missing evidence, failed required case, launch failure or
timeout exits nonzero. The standalone modes are useful diagnostics, not retries
that turn a failing suite into PASS.

| Mode | Required evidence |
| --- | --- |
| normal | Real unpacked MV3 worker/popup, Article **and** Selection, unchanged DOM/range/scroll, durable draft/reload/download, safe conversion, isolated capture/trusted-only storage, zero capture network requests |
| images | Native optional permission missing/pregranted/revoked; cookie exists but image acquisition omits cookies and Bearer; redirect/404; native IndexedDB cached Blob reuse, nested snapshot rewrites |
| delivery | Real extension GET/POST to current Go build/custom port; header observations; explicit extension OPTIONS contract; hostile web preflight 403 with zero POST/publication; inactive nested import; lost receipt → manual known-status GET; note GET and actual embedded SPA preview → `/api/raw` 200 and matching image hash/natural width |
| restart | Interrupted HTTP image preparation beside committed Blob; actual browser restart and manual missing-only fetch; captured source closed + permissions revoked + browser network offline → reopen unchanged draft/bytes without refetch; held completed receipt, browser/Go restarts, transient unknown GET → exact frozen same-ID POST returns historical 200 receipt including `String[]` warnings, unchanged inode/content, no duplicates; wrong-endpoint blocking; real worker termination/wake; stale destination requires explicit reconfirmation |
| faults | `hang-evaluate`, `close-fail`, `close-hang`, `launch-hang`, and independent overall watchdog; reached-phase barrier, bounded failure, fixture/profile/group cleanup |

The native HTTP proxy records only method, Origin, Sec-Fetch-Site/Mode/Dest,
boolean Bearer presence and response status. GET may omit Origin; POST may carry
the extension Origin; native privileged extension requests may bypass preflight.
The suite asserts observed allowed forms and successful Go responses, rather
than fabricating browser metadata. `authorizedOPTIONS` is a **separate Node HTTP
contract check**, without Bearer, using the running extension's valid Origin.
`nativePreflightObserved` reports whether Chromium itself actually issued OPTIONS.
The hostile browser page intentionally knows the disposable fixture token, yet
must produce an actual OPTIONS 403 with no import POST. Nil/remote/forged-origin
server permutations are covered by the Go guard tests, not claimed as browser UI.

Standalone diagnostics (build `web/dist` and extension `verify` first):

```sh
# Use an existing private temporary directory for the binary.
go build -trimpath -o /tmp/opencode/igonotes-browser ./cmd/api
xvfb-run -a npm --prefix extensions/webclipper run smoke
xvfb-run -a npm --prefix extensions/webclipper run smoke:images
CLIPPER_SMOKE_DELIVERY=1 CLIPPER_SMOKE_TIMEOUT_MS=60000 \
  CLIPPER_SMOKE_GO_BINARY=/tmp/opencode/igonotes-browser \
  xvfb-run -a npm --prefix extensions/webclipper run smoke
CLIPPER_SMOKE_RESTART=1 CLIPPER_SMOKE_TIMEOUT_MS=120000 \
  CLIPPER_SMOKE_GO_BINARY=/tmp/opencode/igonotes-browser \
  xvfb-run -a npm --prefix extensions/webclipper run smoke
xvfb-run -a node extensions/webclipper/scripts/check-smoke-failures.js 1
```

Repeat all five original phase-barrier cases and the two outer-watchdog regressions:

```sh
xvfb-run -a node extensions/webclipper/scripts/check-smoke-failures.js 3
xvfb-run -a node extensions/webclipper/tests/browser/check-outer-failsafes.js 3
xvfb-run -a node extensions/webclipper/tests/browser/check-profile-replacement.js 3
```

The latter launches actual Chromium, waits for its private DevTools/profile and
fixture listener, then SIGSTOPs the supervisor. It tests the standalone checker's
external timeout, and additionally SIGSTOPs the checker to test the **actual suite**
fallback with no forwarded resource stdout. Each child must fail with its original
timeout while all owned groups disappear, the generated profile is removed and
the listener refuses TCP connections. The test's own emergency cleanup also runs
on RED. `outer-results.json` is a separate gitignored allowlisted result file.
The controlled `CLIPPER_BROWSER_FAILSAFE_PROBE=stopped-checker` branch runs only
the real checker for this negative regression, marks `failsafeProbe`, and always
exits FAIL; it cannot produce a functional-suite PASS or replace the normal 24+5
checks. Default sandbox, DNS rules and functional budgets are unchanged.

`check-profile-replacement.js` exercises actual local `smoke.js` cleanup after
a native Chromium/session-ready barrier. It renames the allocated profile and
creates a foreign replacement with a sentinel at the original path. Three
repetitions cover both standalone and inherited-global-scope invocation: sentinel
bytes/inode and ancestors must survive, cleanup must report identity failure,
owned groups and TCP listener must be gone, and the parent controller must remain
alive. Only the test removes its own replacement/moved directories after assertions,
using captured identities. Redacted evidence is saved as `profile-results.json`.

The Go HTTP acceptance regression also deterministically blocks the creator
**after durable Claim** using a wrapped real repository and test-only pinned
staging/setup providers. Duplicate HTTP POST must return the well-formed pending
202, then creator 201, GET succeeded and exact POST 200 with the same receipt,
inode/content and single file. No production test hook is introduced.

## Manual matrix — NOT RUN here; reserved for task 9.2

Automation uses the trusted `chrome://extensions` developerPrivate API to
pregrant/remove exact optional image hosts in disposable profiles. Popup-in-tab
capture is clicked without stealing source focus; localhost manifest access
supplies the automation grant. This does **not** exercise real toolbar popup
activation, activeTab consent, native loopback/local-network prompts or permission
dialog grant/denial. These cases need a person in headed **official Chrome and
Chromium** with fresh profiles. No official Chrome download/run or task-9.2 PASS
is part of this change. The future `acceptance.md` should be created only from
those actual runs, with OS/browser version and separate observed results.

Optional local manual fixtures:

```sh
node extensions/webclipper/tests/browser/serve-fixtures.js
```

Open `http://127.0.0.1:19491/`. Start the chosen browser with a private profile
and the same resolver rules as `scripts/smoke.js` (map images/CDN/hostile `.test`
names to 127.0.0.1, reject other DNS; disable proxy). Use **Load unpacked** from
fresh `dist`, a separately started current-checkout Go binary at a custom port,
two disposable bases including `research/articles`, and a disposable connection
created through the actual settings UI. Do not pregrant hosts for manual prompts.
The manual server supports `/pixel.gif`, `/missing.gif` (404) and `/redirect`
(cross-host 302), and never starts Go/browser or generates/logs credentials.

| Manual case (each browser) | Required observation / record |
| --- | --- |
| Load unpacked / toolbar Article / Selection | No CSP/startup errors; real toolbar popup; selection/DOM preserved; editor opens; source can close |
| Localhost / 127.0.0.1 / custom port | Pair using actual settings UI; observe native local-network/loopback prompt if this version presents it; record absent prompt honestly; allow/deny behavior and retry |
| Optional source/CDN permissions | Fresh profiles: deny, confirm visible failure and retained draft; retry and grant exact host; redirect still refused; direct CDN host asks separately |
| Offline cached draft | Prepare, close source, revoke host, stop Go/disconnect network, restart browser; ready bytes/Markdown retained; no automatic Send/fetch |
| Browser/server restart | Interrupt image stream/upload and restart each process; record retained draft/attempt, explicit check/replay, no duplicate publication; reconfirm new destination handle |
| Git conflict gate | Local bare remote fixture, create real target-base conflict; Send held, files unchanged; resolve through app and explicitly retry |
| Warning fallback | 404/denial external URL requires checkbox **and** separate confirmation; receipt retains ordered literal warning strings; never label fully offline |

For future `acceptance.md`, record date, OS and exact browser/build, fixture/mode,
manual action, observed result, and evidence reference; failures/NOT RUN remain
explicit. Automated Chromium evidence cannot substitute for official Chrome or
native-prompt manual PASS.
