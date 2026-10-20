# Git Synchronization — Final Acceptance

**Status:** accepted on 2026-10-20. All eight implementation plans are complete.

**Specification:** [Git synchronization design](../specs/2026-09-01-git-synchronization-design.md).
**Plan index:** [implementation roadmap](../plans/2026-09-01-git-synchronization-roadmap.md).

## Accepted Revision And Scope

- Branch at acceptance: `develop`.
- Accepted commit: `30662c07bd22e660f55b23becf4c4d4acae5d24f`.
- Integration: [PR #8](https://github.com/NetMoose/IGoNotes/pull/8), merged on 2026-10-20.
- Plan 8 Task 10 was rerun serially after all native-CI corrections landed.
- The source revision and tracked working tree remained unchanged throughout acceptance. No implementation correction was necessary, so Task 10's conditional correction commit was not created.
- This report records acceptance of that revision; the subsequent documentation commit does not change the accepted implementation or imply another full acceptance run.

Release tagging and publication remain a separate maintainer action, as required by Plan 8.

## Local Environment

Linux/amd64, Go `1.26.5`, native Git `2.43.0`, Node.js `v24.20.0`, npm `12.0.2`. Node major 24 was checked before installing frontend dependencies.

## Serial Verification Results

Commands below ran in the listed order. Every command exited successfully.

### Frontend And Documentation

```bash
npm --prefix web ci
npm --prefix web run verify
npm --prefix web audit --audit-level=high
go test ./internal/docs -count=1 -v
```

- Vitest: **499 tests passed in 30 files**; Vite production build passed.
- Audit: **no high/critical findings**. Two moderate entries remain for `postcss-selector-parser` and its parent `@tailwindcss/typography`; the latter pins the affected parser version. No forced dependency downgrade was applied.
- Documentation tests passed, including exact site mirrors, API contracts, architecture, and stale-claim checks.
- Existing non-failing frontend output included two Svelte `lifecycle_double_unmount` warnings in `Modal.test.js` and Vite's large-chunk warning.

### Backend, Native Git, And Ten Repetitions

```bash
go test ./... -count=1
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./internal/git ./internal/service ./internal/handlers ./cmd/api -count=1 -v
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./internal/git -run 'Test(CommandRunnerCancellationTerminates|WindowsProcessTree|NativeGit|TwoDeviceEndToEnd)' -count=10 -v
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./internal/git -run '^TestCommandRunner(TerminatesProcessTree|ProcessTreeLifecycle|ProcessTreeCancellationLifetime|CancelsDescendantWhileDrainingPipes|CleansDescendantsAfterPipeWaitDelay)$' -count=10 -v
```

- Full backend and mandatory native Git packages passed.
- Native path/ignore/assets/deletion checks and both two-device scenarios passed ten repetitions: **30.113s** for the plan's repeated selection.
- The plan's historical regular expression does not select all current process-tree test names. The additional command explicitly covered them ten times: **100.422s**.
- No native Git tests were skipped in these Linux selections. Two service tests for physical case aliases skipped because the local filesystem is case-sensitive: `TestNoteServiceConflictPolicyMatchesPhysicalCaseAlias` and `TestValidateUniqueGitRepositoryPathsRejectsPhysicalCaseAlias`. Windows-specific process tests are covered by native CI rather than Linux execution.

### Race Detector And Repeated Manager Stress

```bash
go test -race ./... -count=1
go test -race ./internal/service -run '^TestGitManagerRace' -count=20 -v -timeout=45m
```

- Full race suite passed without `DATA RACE`.
- Manager stress: **100 successful scenario runs** (five tests repeated twenty times), **1921.299s**, without test failure, deadlock, or stale-overwrite assertion.
- The explicit 45-minute suite timeout replaces Go's default ten-minute limit for this long repeated run; it does not change the tests' own synchronization or operation-completion bounds.

### Finite Fuzz Campaigns

These commands ran separately, one after the other:

```bash
go test ./internal/git -run '^$' -fuzz=FuzzRedactGitDiagnostic -fuzztime=15s
go test ./internal/git -run '^$' -fuzz=FuzzConflictParsers -fuzztime=15s
```

- Redaction: **1,264,146 executions**, PASS, **15.398s** including setup/teardown.
- Conflict parsers: **3,231,205 executions**, PASS, **16.047s** including setup/teardown.
- No crash corpus or source-tree changes were produced.

### Static Checks And Production Builds

```bash
go vet ./...
make clean
make all
test -x builds/igonotes
```

All passed. `make all` installed dependencies from the lockfile, rebuilt embedded frontend assets, and built the production binary with `-trimpath`.

Six separate `CGO_ENABLED=0` cross-builds used `go build -trimpath -ldflags="-s -w"` for `./cmd/api`: Linux, Windows, and macOS, each for amd64 and arm64. All passed; exactly six binaries were verified in an external temporary directory.

## Acceptance Matrix

| Design requirement | Successful evidence |
|---|---|
| Non-Git bases need no Git installation | `TestGitUnconfiguredRuntimeStartsAndStopsWithoutGit` and ordinary backend suite |
| Per-base enable/disable and file preservation | Settings configure/disable/forget tests and scheduler reconciliation tests |
| Two-device propagation | `TestTwoDeviceEndToEndConvergesCreateEditRenameDeleteAndAssets`, repeated ten times |
| Existing local/remote data and backup refs | Initialize backup, unrelated-history, exact-command, and retry tests |
| Conflict, timeout, restart, and rejected-push safety | Conflict lifecycle/recovery, exact-OID push, retry/rejection, and two-device conflict tests |
| Stale saves cannot overwrite incoming content | Revision/HTTP `note_changed` tests, frontend stale-note tests, and repeated manager race scenarios |
| Worktree, SQLite, and frontend consistency | Initialize/sync reindex-before-unlock tests, two-device tree comparisons, and frontend incoming-change tests |
| Fifth failure pauses; pause survives restart | Breaker/resume, autosync resilience, persisted recovery, and frontend pause lifecycle tests |
| Partial conflict resolutions survive restart and UI handles conflicts | Backend conflict restart, two-device conflict, repeated manager conflict race, and frontend resolver/workspace tests |
| No secrets in public/persistent sinks | Redaction tests/fuzz, `TestGitSecretsNeverReachPublicOrPersistentSinks`, and `TestGitSecretAcceptanceRealHTTPResponses` |
| Tests and production builds pass | Full backend/race/frontend runs, clean build, six cross-builds, and native CI |

## Workflow And Cross-Platform Evidence

[Merge-commit CI run 39380230370](https://github.com/NetMoose/IGoNotes/actions/runs/39380230370) completed successfully for the accepted SHA. All six jobs passed:

- `frontend`;
- `race-vet-build`;
- `Native Git (ubuntu-latest)`;
- `Native Git (macos-latest)`;
- `Native Git (windows-latest / git)`;
- `Native Git (windows-latest / application)`.

The hardening branch's [push run](https://github.com/NetMoose/IGoNotes/actions/runs/39374628948) and [PR run](https://github.com/NetMoose/IGoNotes/actions/runs/39374635010) also completed successfully before merge. The PR's Windows Git job required one rerun after a single local Git command reached its standard 15-second timeout; that rerun passed. No claim of zero transient CI failures is implied.

Workflow review confirmed:

- Native Git is mandatory in CI and release verification. Windows shards jointly select every Go package.
- Release remains triggered by `v*` tags, with prerelease classification derived from the tag's hyphen.
- Exactly one `contents: write` permission exists, in the release `publish` job; build verification is read-only.
- Release publication depends on verified artifacts and checksum verification.
- Git integration fixtures use temporary local remotes rather than an external Git host or stored credentials.

The prescribed stale-placeholder scan and `git diff --check` passed. Final HEAD equalled the accepted SHA, both staged and unstaged diffs were empty, and `git status --short` was empty.

## Closure

Plans 1–8 and Plan 8 Task 10 are accepted. The original implementation recipes remain historical instructions; their procedural checkboxes do not represent an outstanding feature backlog. Completion status is recorded in each plan and in the roadmap, with this report as the acceptance record.
