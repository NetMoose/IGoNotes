# Git Synchronization Plan 7: Autosync Resilience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add deterministic all-base automatic Git synchronization, an atomic persisted five-failure circuit breaker, explicit resume behavior, a persistent paused alert, and cancellation-safe startup/shutdown lifecycle integration.

**Architecture:** Extend the landed Plan 3 `GitManager` and Plan 1 `GitStatusRepository`; do not create another operation queue, status model, database, or Git execution layer. A fake-clock-driven scheduler computes due work for every configured autosync base and submits it to the manager's existing single FIFO worker, while one SQLite upsert atomically preserves, resets, or increments the persisted failure count and changes the fifth operational failure to `paused`. Plan 4 conflict/abort states remain authoritative, and settings publication wakes scheduler reconciliation without calling back into the manager while settings locks are held.

**Tech Stack:** Go 1.26, system Git 2.28+ through the landed Plan 1 runner, `context`, `sync`, `time`, `database/sql`, `modernc.org/sqlite`, `net/http`, Svelte 5, JavaScript, Tailwind CSS 4, Vitest, Testing Library, and the Go race detector.

---

## Dependencies And Starting Point

Execute backend contract/scheduler Tasks 1-3 only after Plans 1-4 have landed in one implementation worktree and these commands pass:

```bash
go test ./internal/git ./internal/repository ./internal/service ./internal/handlers ./cmd/api -count=1
```

Expected: exit status `0`; every listed package reports `ok` or `[no test files]`.

The required landed ownership is:

- Plan 1: `internal/model/api.go` owns `model.GitStatus`; `internal/repository/git_status_repo.go` owns persistence by canonical repository path; `internal/repository/db.go` owns ordered migrations.
- Plan 2: one `BaseOperationCoordinator` serializes base lifecycle and Git operations; `NoteService.MutateActiveFilesystem` remains the only active-worktree mutation transaction.
- Plan 3: one `GitManager` owns the global FIFO worker, per-path deduplication, operation journal/status publication, startup recovery, and `QueueSync`; `SettingsService.GitSnapshot` is the fresh per-name source.
- Plan 4: conflict complete/abort extend the same manager and journal; conflict abort publishes `paused`, clears the mutation block, and waits for a future explicit resume.

At planning time the implementation worktree is still at the pre-Git codebase: `internal/git`, `GitManager`, Git routes, and Git frontend components do not exist yet; `go.mod` declares Go 1.26; `web/package.json` declares Svelte 5.56, Vite 8.1, Tailwind CSS 4.3, and Vitest 4.1; `App.svelte` owns the editor/save state machine and `NotesWorkspace.svelte` owns the editor shell. The untracked Plans 1-6 are therefore dependency contracts, not landed code. Do not execute this plan directly against the planning checkout.

Plans 5 and 6 are hard prerequisites for frontend Task 8. Task 8 starts from Plan 5's strict `getGitStatus(base = '')` wrapper, sole App-owned `gitPoller`, `gitStatuses`, `activeGitStatus`, `gitBusyBase`, `gitActionErrors`, and zero-argument `refreshGitStatuses()` seam after Plan 6 has made `applyGitStatuses` asynchronous for revision-safe changed-path handling. Task 8 adds only the strict `resumeGit` wrapper, the alert, and resume orchestration; it must not add `getGitStatuses`, another status collection, timer, generation counter, polling lifecycle, or fallback implementation. Per the roadmap, only Tasks 1-3 may overlap Plan 6. Do not begin central `GitManager`, settings, HTTP, production lifecycle, frontend, or final integration Tasks 4-9 until Plan 6 has landed and its full frontend suite/build pass.

This plan includes:

- all configured bases with `auto_sync:true`, active and inactive;
- exactly `5`, `15`, `30`, and `60` minute intervals already validated by Plan 1;
- deterministic startup staggering in config order, beginning five seconds after manager start;
- scheduling relative to persisted `last_attempt` and manager status notifications;
- one global sequential worker, with scheduled/manual requests sharing Plan 3 deduplication;
- atomic persisted failure-count transitions and a five-failure breaker;
- explicit outcome classification for success, operational failure, validation/config drift, conflict, user abort, and manager shutdown;
- `POST /api/git/resume?base=<name>`;
- persistent paused UI with retry/resume and settings actions;
- enable/interval/template/config, rename, path move, disable, and forget reconciliation;
- immediate manager cancellation when application shutdown starts;
- safe application logging when the breaker opens.

This plan excludes:

- cron syntax, arbitrary intervals, random jitter, exponential backoff, desktop notifications, and web push;
- parallel Git execution across bases;
- a second scheduler process, SQLite file, migration framework, status table, operation journal, or manager;
- automatic resume after restart, config reload, network recovery, or browser open;
- automatic conflict-side selection, force push, rebase, reset, or final shutdown commit;
- changing Plan 4 conflict-resolution semantics.

## Behavioral Contract

### Deterministic Scheduling

The scheduler uses canonical repository path as identity and current config order as its stable tie-breaker.

1. On manager start, read fully configured bases in config order.
2. Ignore `auto_sync:false` and block `paused`, `conflict`, `needs_reconnect`, `initializing`, and `syncing` states.
3. For an eligible base with `last_attempt`, use `last_attempt + interval`.
4. If that time is already due at startup, clamp it to `manager_start + (eligible_index + 1) * 5 seconds`.
5. For an eligible base without `last_attempt`, use the same startup slot.
6. After startup, a newly enabled base with no future due time receives `now + (newly_eligible_index + 1) * 5 seconds`.
7. Due entries are submitted by due time, then current config order. The existing Plan 3 FIFO worker executes them one at a time.
8. A successful new queue submission or deduplication advances the in-memory due time by the configured interval, preventing a tight loop before the manager publishes `last_attempt`.
9. A manual sync, resume, initialize, or conflict-complete status change wakes reconciliation; its persisted `last_attempt` becomes the next interval anchor.
10. `error` with fewer than five consecutive failures remains eligible. `paused`, `conflict`, and `needs_reconnect` have no timer.

The first startup slot is intentionally nonzero. This preserves Plan 3 startup order (`RecoverLocal`, manager start, initial note indexing, router, serve) without allowing an overdue autosync to race the initial note index immediately at `Start`.

### Outcome Classification

| Outcome | Persisted counter | Public state | Automatic retry |
|---|---:|---|---|
| Successful sync or conflict complete | reset to `0` | `ready` | next configured interval |
| Successful initialize after config change | reset to `0` | `ready` | next configured interval when enabled |
| Operational sync failure 1-4 | increment atomically | `error` | next configured interval |
| Operational sync failure 5 | increment atomically to `5` | `paused` | none |
| Later operational failure after an externally inconsistent count above 5 | increment atomically | `paused` | none |
| Merge conflict | preserve | `conflict` | none |
| Successful user conflict abort | preserve | `paused` | none |
| Failed user conflict abort | preserve | Plan 4 conflict/recovery state | none |
| Validation/config freshness failure | preserve | `needs_reconnect` or existing reconciled state | none |
| Deleted branch or rewritten remote history safety stop | preserve | `paused` with its fixed safe error | none |
| Queue validation rejected before insertion | unchanged | unchanged | none |
| Manager-lifetime cancellation during shutdown | preserve | `error` with fixed `operation_interrupted`, unless local recovery detects conflict | none |
| Explicit resume | reset atomically to `0` before queueing | `syncing` after durable queue insertion | one immediate sync |
| Successful Git config change | reset to `0` before initialize queueing | `initializing` after durable queue insertion | initialize once |

Only retryable failed `sync` and `conflict_complete` operations consume failure budget. Initialize failures do not consume autosync attempts because a repository that has not connected successfully is not yet an autosync cycle. Queue validation failures happen before an operation is inserted and therefore never call terminal outcome classification. A `git.ConflictError`, successful or failed Plan 4 abort, `CodeConfirmationRequired`, `CodeNeedsReconnect`, `CodePaused`, `CodeNotPaused`, `CodeBranchDeleted`, `CodeRemoteHistoryRewritten`, or cancellation from `GitManager.Close` never increments the counter. Branch deletion and rewritten history use the approved immediate safety pause rather than five automatic retries.

### Resume Semantics

- Resume accepts only current `paused` status, including five-failure breaker pause and Plan 4 user-abort pause.
- A repeated resume that races the first accepted request returns the exact active sync operation with `deduplicated:true`; it does not reset or enqueue again.
- It resolves a fresh `SettingsService.GitSnapshot`, holds the shared coordinator, atomically resets the counter, durably inserts one normal Plan 3 sync operation, and publishes `syncing`.
- `POST /api/git/sync` continues returning `409 git_paused`; resume is the only bypass.
- If journal insertion or queued-status publication fails, restore the exact previous paused status before returning the safe error.
- A resume failure starts a new sequence at `1`; five new consecutive operational failures pause again.
- Resume never changes config, performs conflict abort, or bypasses `needs_reconnect`/`conflict`.

### Settings Reconciliation

| Settings action | Persisted status | Scheduler action |
|---|---|---|
| Enable autosync or change interval/template/URL/branch | reset failures; preserve trusted OID only under Plan 3 same-remote rule; queue initialize | wake and reconcile; wait while initializing |
| Disable autosync but retain Git config through Git settings | reset breaker and queue one initialize operation; retain no recurring schedule | remove timer immediately; observe only the explicit initialize status |
| Same-path base rename | preserve row/counter/OID, update `Base` | retain due time; rebind queued work by canonical path and remote fingerprint |
| Configured path change | delete old row, create new `needs_reconnect` with count `0` | remove old timer; do not schedule new path |
| Disable Git integration | delete status; leave `.git` untouched | remove timer |
| Forget base | delete status; leave files and `.git` untouched | remove timer |
| Replace config | apply the same rules per canonical path | reconcile all entries in new config order |

Settings code only sends a nonblocking coalesced notification after successful in-memory publication. The scheduler later reads immutable snapshots. It never calls manager/scheduler code while holding coordinator, `SettingsService.mu`, `NoteService.baseMu`, or SQLite locks.

## File Map

Create:

- `internal/repository/git_status_transition.go`: atomic failure-count transition contracts and SQL implementation.
- `internal/repository/git_status_transition_test.go`: fresh, concurrent, fifth-failure, preserve, reset, and rollback tests.
- `internal/service/git_resilience_contracts.go`: outcome and scheduler clock/source contracts frozen before parallel work.
- `internal/service/git_resilience_contracts_test.go`: classification table tests.
- `internal/service/git_scheduler.go`: deterministic timer planner/event loop feeding Plan 3 queue.
- `internal/service/git_scheduler_test.go`: fake-clock all-base, staggering, interval, reconciliation, and cancellation tests.
- `web/src/lib/git/GitPausedAlert.svelte`: persistent accessible pause alert.
- `web/src/lib/git/GitPausedAlert.test.js`: rendering, failure, busy, and actions tests.

Modify according to the wave prerequisites below; Tasks 1-3 need Plans 1-4, while central/backend integration Tasks 4-7 and frontend Tasks 8-9 also wait for the Plan 6 barrier:

- `internal/git/errors.go`: add fixed `git_not_paused` code/sentinel.
- `internal/git/operation.go`: unchanged; reuse sync/initialize/conflict-complete/abort kinds and `ConflictError`.
- `internal/repository/git_status_repo.go`: reuse row encode/scan helpers from the atomic transition.
- `internal/service/git_manager.go`: scheduler ownership, atomic outcome publication, `Resume`, notifications, idempotent close.
- `internal/service/git_manager_test.go`: outcome, resume, sequential scheduler, and close races.
- `internal/service/settings_service.go`: ordered snapshots, coalesced config events, and exact status reconciliation.
- `internal/service/settings_service_test.go`: rename/path/disable/forget/config reset/event tests.
- `internal/handlers/git_handler.go`: resume method and narrow operation interface.
- `internal/handlers/git_handler_test.go`: `202`, validation, compensation, and redaction tests.
- `internal/handlers/git_routes.go`: guarded resume route.
- `internal/handlers/git_routes_test.go`: method/setup/origin matrix.
- `internal/handlers/errors.go`: `git_not_paused` mapping.
- `internal/handlers/errors_test.go`: fixed safe mapping/no-leak tests.
- `cmd/api/main.go`: autosync-aware manager constructor and immediate shutdown cancellation.
- `cmd/api/git_runtime_test.go`: scheduler startup/recovery/shutdown ordering.
- `web/src/lib/api.js`: reuse the landed singular strict status wrapper and add strict resume validation.
- `web/src/lib/api.test.js`: exact encoded paths/methods.
- `web/src/App.svelte`: extend the landed Plan 5/6 poller owner and shared Git action lock with flush-before-resume.
- `web/src/App.test.js`: persisted pause after load, shared sync/resume exclusion, resume refresh, flush failure, and unmount/base-change guards.
- `web/src/lib/NotesWorkspace.svelte`: render the persistent alert above the editor.
- `web/src/lib/NotesWorkspace.test.js`: prop/action/accessibility tests.
- `docs/api.md`: autosync status, breaker, and resume contract.

Do not add a migration. Plan 1 already created `git_status.consecutive_failures`, `last_attempt_unix_ms`, `last_success_unix_ms`, state `paused`, and the canonical-path primary key.

## Parallel Dispatch Waves

Every worker starts from the same repository commit for its wave, uses an isolated worktree, owns only the listed files, and returns a focused commit. The coordinator reviews and merges each commit; workers never reset, rebase, or edit another lane's files.

### Wave 0: Contract Freeze, Allowed To Overlap Plan 6

- Task 1 is serial after Plans 1-4 and may run while Plan 6 component/API work is in progress because it owns backend contract files only.
- Merge it and run `go test ./internal/git ./internal/repository ./internal/service -run 'TestGitOutcome|TestGitResilience' -count=1`.
- This freezes failure actions, outcome classification, scheduler clock/timer/source/queue signatures, constants, and the not-paused error before implementation lanes begin.

### Wave 1: Independent Atomic State And Scheduler, Allowed To Overlap Plan 6

Run Tasks 2 and 3 in parallel after Wave 0:

| Worker | Task | Exclusive files |
|---|---|---|
| Atomic status | Task 2 | `internal/repository/git_status_transition.go`, `internal/repository/git_status_transition_test.go`, `internal/repository/git_status_repo.go` |
| Fake-clock scheduler | Task 3 | `internal/service/git_scheduler.go`, `internal/service/git_scheduler_test.go` |

The scheduler lane uses `gitScheduleStatusReader` and `gitScheduleQueue`; it must not import the concrete manager or edit repository files. The repository lane must not edit service files. Merge both, then run `go test ./internal/repository ./internal/service -count=1` and `go test -race ./internal/repository ./internal/service -run 'TestGit(StatusRepositoryApply|Scheduler)' -count=1`.

### Wave 2: Plan 6 Barrier, Then Serial Manager And Settings Integration

- Do not start this wave until Plan 6 is merged and its complete Node 24 frontend suite/build pass. Tasks 4-9 may not overlap Plan 6, even where their immediate file lists are disjoint.
- Task 4 is serial after that barrier and integrates outcome transitions, resume, scheduler ownership, worker notifications, and manager close.
- Task 5 is serial after Task 4 and integrates settings snapshots/events/reconciliation.
- Do not parallelize these tasks: both establish operation/config publication ordering and the manager lifecycle contract.

### Wave 3: Guarded HTTP Integration

- Task 6 is serial after Tasks 4-5 freeze `GitManager.Resume` and `SettingsService.GitSnapshots`.
- Although its files are disjoint from Plan 6, it starts only after the Wave 2 Plan 6 barrier so the execution schedule has exactly the roadmap-approved overlap.

### Wave 4: Serial Production Lifecycle

- Task 7 is serial after Tasks 4-6.
- It is the only task that changes production construction/start/close order.

### Wave 5: Serial Frontend Integration

- Task 8 is serial after Tasks 6-7 and after both Plans 5 and 6 have landed.
- Re-read the integrated Plan 6 `App.svelte`, `App.test.js`, `NotesWorkspace.svelte`, and API client immediately before editing. Reuse its one poller and Plan 5's App-owned Git action lock; no frontend lane from this plan runs in parallel with Plan 6.
- Task 9 is serial final documentation and cross-layer verification after Task 8.

### Task 1: Freeze Failure, Outcome, And Scheduler Contracts

**Files:**
- Create: `internal/repository/git_status_transition.go`
- Create: `internal/service/git_resilience_contracts.go`
- Create: `internal/service/git_resilience_contracts_test.go`
- Modify: `internal/git/errors.go`

- [ ] **Step 1: Write the RED outcome table tests**

Create `internal/service/git_resilience_contracts_test.go` with table cases named:

```go
func TestGitOutcomeSuccessfulSyncResetsFailures(t *testing.T)
func TestGitOutcomeOperationalSyncFailureIncrements(t *testing.T)
func TestGitOutcomeInitializeFailurePreservesFailures(t *testing.T)
func TestGitOutcomeConflictPreservesFailures(t *testing.T)
func TestGitOutcomeAbortPreservesFailuresAndPauses(t *testing.T)
func TestGitOutcomeFailedAbortPreservesFailuresAndConflictState(t *testing.T)
func TestGitOutcomeValidationPreservesFailures(t *testing.T)
func TestGitOutcomeRemoteRewritePausesWithoutIncrement(t *testing.T)
func TestGitOutcomeShutdownCancellationPreservesFailures(t *testing.T)
func TestGitOutcomeConflictCompleteSuccessResetsFailures(t *testing.T)
```

Use this representative complete table body:

```go
func TestGitOutcomeClassification(t *testing.T) {
	tests := []struct {
		name     string
		kind     git.OperationKind
		err      error
		shutdown bool
		want     gitOutcome
	}{
		{name: "sync success", kind: git.OperationSync, want: gitOutcome{Failures: repository.ResetGitFailures}},
		{name: "sync transport failure", kind: git.OperationSync, err: &git.SafeError{Code: git.CodeRemoteUnreachable, Message: "Git remote is unreachable"}, want: gitOutcome{Failures: repository.IncrementGitFailures}},
		{name: "initialize failure", kind: git.OperationInitialize, err: &git.SafeError{Code: git.CodeAuthentication, Message: "Git authentication failed"}, want: gitOutcome{Failures: repository.PreserveGitFailures}},
		{name: "conflict", kind: git.OperationSync, err: &git.ConflictError{Paths: []string{"note.md"}}, want: gitOutcome{Failures: repository.PreserveGitFailures, State: model.GitStateConflict}},
		{name: "abort", kind: git.OperationConflictAbort, want: gitOutcome{Failures: repository.PreserveGitFailures, State: model.GitStatePaused}},
		{name: "failed abort", kind: git.OperationConflictAbort, err: &git.SafeError{Code: git.CodeRepositoryLocked, Message: "Git repository is locked"}, want: gitOutcome{Failures: repository.PreserveGitFailures}},
		{name: "stale config", kind: git.OperationSync, err: &git.SafeError{Code: git.CodeNeedsReconnect, Message: "Git configuration changed; reconnect is required"}, want: gitOutcome{Failures: repository.PreserveGitFailures, State: model.GitStateNeedsReconnect}},
		{name: "remote rewrite", kind: git.OperationSync, err: &git.SafeError{Code: git.CodeRemoteHistoryRewritten, Message: "Git remote history was rewritten"}, want: gitOutcome{Failures: repository.PreserveGitFailures, State: model.GitStatePaused}},
		{name: "shutdown", kind: git.OperationSync, err: context.Canceled, shutdown: true, want: gitOutcome{Failures: repository.PreserveGitFailures}},
		{name: "complete success", kind: git.OperationConflictComplete, want: gitOutcome{Failures: repository.ResetGitFailures}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyGitOutcome(tt.kind, tt.err, tt.shutdown); got != tt.want {
				t.Fatalf("classifyGitOutcome() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the RED contract tests**

Run:

```bash
go test ./internal/service -run '^TestGitOutcome' -count=1 -v
```

Expected: FAIL to compile with undefined `gitOutcome`, `classifyGitOutcome`, and failure-action constants.

- [ ] **Step 3: Define atomic failure actions without implementing a second repository**

Create `internal/repository/git_status_transition.go` with these declarations and imports; Task 2 adds the method body in the same file:

```go
package repository

import "IGoNotes/internal/model"

type GitFailureAction uint8

const (
	PreserveGitFailures GitFailureAction = iota
	ResetGitFailures
	IncrementGitFailures
)

type GitStatusTransition struct {
	Status   model.GitStatus
	Failures GitFailureAction
}
```

No schema or database is created here.

- [ ] **Step 4: Add the not-paused safe error**

Append to the landed constants/sentinels in `internal/git/errors.go`:

```go
const CodeNotPaused ErrorCode = "git_not_paused"

var ErrGitNotPaused = &SafeError{
	Code:    CodeNotPaused,
	Message: "Git synchronization is not paused",
}
```

Keep the Plan 4 `CodePaused`/`ErrGitPaused` values unchanged. Never mutate either sentinel. Reuse the landed `ConflictError` type directly; do not add another conflict carrier or constructor.

- [ ] **Step 5: Freeze outcome and scheduler interfaces**

Create `internal/service/git_resilience_contracts.go`:

```go
package service

import (
	"context"
	"errors"
	"log"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

const (
	gitStartupStagger       = 5 * time.Second
	gitSchedulerRetryDelay  = 30 * time.Second
	gitFailurePauseThreshold = 5
)

type gitOutcome struct {
	Failures repository.GitFailureAction
	State    model.GitState
}

func classifyGitOutcome(kind gitcmd.OperationKind, err error, shuttingDown bool) gitOutcome {
	if kind == gitcmd.OperationConflictAbort {
		if err == nil {
			return gitOutcome{Failures: repository.PreserveGitFailures, State: model.GitStatePaused}
		}
		return gitOutcome{Failures: repository.PreserveGitFailures}
	}
	var conflict *gitcmd.ConflictError
	if errors.As(err, &conflict) {
		return gitOutcome{Failures: repository.PreserveGitFailures, State: model.GitStateConflict}
	}
	if shuttingDown && errors.Is(err, context.Canceled) {
		return gitOutcome{Failures: repository.PreserveGitFailures}
	}
	var safe *gitcmd.SafeError
	if errors.As(err, &safe) {
		switch safe.Code {
		case gitcmd.CodeConfirmationRequired, gitcmd.CodePaused, gitcmd.CodeNotPaused:
			return gitOutcome{Failures: repository.PreserveGitFailures}
		case gitcmd.CodeBranchDeleted, gitcmd.CodeRemoteHistoryRewritten:
			return gitOutcome{Failures: repository.PreserveGitFailures, State: model.GitStatePaused}
		case gitcmd.CodeNeedsReconnect:
			return gitOutcome{Failures: repository.PreserveGitFailures, State: model.GitStateNeedsReconnect}
		}
	}
	if err == nil {
		switch kind {
		case gitcmd.OperationInitialize, gitcmd.OperationSync, gitcmd.OperationConflictComplete:
			return gitOutcome{Failures: repository.ResetGitFailures}
		default:
			return gitOutcome{Failures: repository.PreserveGitFailures}
		}
	}
	if kind == gitcmd.OperationSync || kind == gitcmd.OperationConflictComplete {
		return gitOutcome{Failures: repository.IncrementGitFailures}
	}
	return gitOutcome{Failures: repository.PreserveGitFailures}
}

type gitSchedulerTimer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(time.Duration) bool
}

type gitSchedulerClock interface {
	Now() time.Time
	NewTimer(time.Duration) gitSchedulerTimer
}

type gitScheduleStatusReader interface {
	Get(context.Context, string) (model.GitStatus, bool, error)
}

type gitScheduleSource func() ([]gitcmd.ConfiguredBase, error)

type gitScheduleQueue func(context.Context, gitcmd.SyncRequest) (gitcmd.Operation, bool, error)

func normalizeGitSchedulerLogger(logger *log.Logger) *log.Logger {
	if logger == nil {
		return log.Default()
	}
	return logger
}
```

- [ ] **Step 6: Run GREEN contract tests**

Run:

```bash
gofmt -w internal/git/errors.go internal/repository/git_status_transition.go internal/service/git_resilience_contracts.go internal/service/git_resilience_contracts_test.go
go test ./internal/git ./internal/repository ./internal/service -run 'TestGitOutcome|TestGitResilience' -count=1 -v
```

Expected: PASS; no Git executable or database is required.

- [ ] **Step 7: Commit the frozen contracts**

```bash
git add internal/git/errors.go internal/repository/git_status_transition.go internal/service/git_resilience_contracts.go internal/service/git_resilience_contracts_test.go
git commit -m "feat: define git autosync resilience contracts"
```

### Task 2: Apply Failure Counts Atomically In The Plan 1 Status Repository

**Files:**
- Modify: `internal/repository/git_status_transition.go`
- Modify: `internal/repository/git_status_repo.go`
- Create: `internal/repository/git_status_transition_test.go`

- [ ] **Step 1: Write RED atomic transition tests**

Add these exact tests:

```go
func TestGitStatusRepositoryApplyTransitionPreservesFailures(t *testing.T)
func TestGitStatusRepositoryApplyTransitionResetsFailures(t *testing.T)
func TestGitStatusRepositoryApplyTransitionIncrementsFailures(t *testing.T)
func TestGitStatusRepositoryApplyTransitionFifthFailurePauses(t *testing.T)
func TestGitStatusRepositoryApplyTransitionKeepsPauseAboveThreshold(t *testing.T)
func TestGitStatusRepositoryApplyTransitionConcurrentIncrementsAreAtomic(t *testing.T)
func TestGitStatusRepositoryApplyTransitionReturnsStoredStatus(t *testing.T)
func TestGitStatusRepositoryApplyTransitionRejectsUnknownAction(t *testing.T)
func TestGitStatusRepositoryApplyTransitionClosedDatabaseDoesNotChangeMemory(t *testing.T)
```

The fifth-failure test starts from a persisted count of `4`, submits an `error` status with `IncrementGitFailures`, and requires returned/reloaded state `paused`, count `5`, unchanged safe error, attempt time, OIDs, and changed paths. The concurrent test sets its fixture DB to one open connection, launches five goroutines against one canonical path, waits for all, and requires exactly `5`, not any value from `1` through `4` and not more than `5`. Production correctness does not rely on this connection limit: the single SQL statement is atomic per successful call, and the Plan 3 worker serializes operation outcomes.

- [ ] **Step 2: Run the RED repository tests**

Run:

```bash
go test ./internal/repository -run '^TestGitStatusRepositoryApplyTransition' -count=1 -v
```

Expected: FAIL to compile because `ApplyTransition` does not exist.

- [ ] **Step 3: Reuse one row encoder/scanner from the landed repository**

Refactor `internal/repository/git_status_repo.go` so `Upsert`, `Get`, `List`, and `ApplyTransition` share:

```go
type gitStatusScanner interface {
	Scan(...any) error
}

type gitStatusRecord struct {
	RepositoryPath      string
	BaseName            string
	State               string
	OperationID         string
	Stage               string
	Ahead               int
	Behind              int
	ConsecutiveFailures int
	LastAttemptUnixMS   any
	LastSuccessUnixMS   any
	ChangedPathsJSON    string
	RemoteOID           string
	ErrorCode           string
	ErrorMessage        string
	ErrorField          string
	UpdatedAtUnixMS     int64
}

func scanGitStatus(row gitStatusScanner) (model.GitStatus, error)
func encodeGitStatus(model.GitStatus, time.Time) (gitStatusRecord, error)
```

`encodeGitStatus` clones, sorts, and JSON-encodes `ChangedPaths`, stores nullable Unix milliseconds, and stores only `APIError.Code`, `Message`, and `Field`. `scanGitStatus` reconstructs non-nil changed paths and UTC timestamps. Keep landed `Upsert` behavior unchanged so Plan 1 round-trip tests still accept an explicitly supplied counter.

- [ ] **Step 4: Implement one-statement atomic transition**

Replace Task 1's single import with this block, then append the method in `internal/repository/git_status_transition.go`:

```go
import (
	"context"
	"fmt"

	"IGoNotes/internal/model"
)

func (r *GitStatusRepository) ApplyTransition(
	ctx context.Context,
	transition GitStatusTransition,
) (model.GitStatus, error) {
	if transition.Failures > IncrementGitFailures {
		return model.GitStatus{}, fmt.Errorf("invalid Git failure action %d", transition.Failures)
	}
	record, err := encodeGitStatus(transition.Status, r.now())
	if err != nil {
		return model.GitStatus{}, err
	}
	initialFailures := record.ConsecutiveFailures
	switch transition.Failures {
	case ResetGitFailures:
		initialFailures = 0
	case IncrementGitFailures:
		initialFailures = 1
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO git_status (
			repository_path, base_name, state, operation_id, stage,
			ahead, behind, consecutive_failures, last_attempt_unix_ms,
			last_success_unix_ms, changed_paths_json, remote_oid,
			error_code, error_message, error_field, updated_at_unix_ms
		) VALUES (?, ?,
			CASE WHEN ? = ? AND ? >= 5 THEN 'paused' ELSE ? END,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		)
		ON CONFLICT(repository_path) DO UPDATE SET
			base_name = excluded.base_name,
			state = CASE
				WHEN ? = ? AND git_status.consecutive_failures + 1 >= 5 THEN 'paused'
				ELSE excluded.state
			END,
			operation_id = excluded.operation_id,
			stage = excluded.stage,
			ahead = excluded.ahead,
			behind = excluded.behind,
			consecutive_failures = CASE ?
				WHEN ? THEN 0
				WHEN ? THEN git_status.consecutive_failures + 1
				ELSE git_status.consecutive_failures
			END,
			last_attempt_unix_ms = excluded.last_attempt_unix_ms,
			last_success_unix_ms = excluded.last_success_unix_ms,
			changed_paths_json = excluded.changed_paths_json,
			remote_oid = excluded.remote_oid,
			error_code = excluded.error_code,
			error_message = excluded.error_message,
			error_field = excluded.error_field,
			updated_at_unix_ms = excluded.updated_at_unix_ms
		RETURNING repository_path, base_name, state, operation_id, stage,
			ahead, behind, consecutive_failures, last_attempt_unix_ms,
			last_success_unix_ms, changed_paths_json, remote_oid,
			error_code, error_message, error_field, updated_at_unix_ms
	`,
		record.RepositoryPath, record.BaseName,
		transition.Failures, IncrementGitFailures, initialFailures, record.State,
		record.OperationID, record.Stage, record.Ahead, record.Behind,
		initialFailures, record.LastAttemptUnixMS, record.LastSuccessUnixMS,
		record.ChangedPathsJSON, record.RemoteOID, record.ErrorCode,
		record.ErrorMessage, record.ErrorField, record.UpdatedAtUnixMS,
		transition.Failures, IncrementGitFailures,
		transition.Failures, ResetGitFailures, IncrementGitFailures,
	)
	stored, err := scanGitStatus(row)
	if err != nil {
		return model.GitStatus{}, fmt.Errorf("apply Git status transition: %w", err)
	}
	return stored, nil
}
```

The implementation must pass the full status round-trip test, not merely state/count assertions. Use SQLite `RETURNING`; do not implement `Get` followed by `Upsert`, a Go mutex, or a second transaction because those permit cross-connection lost updates.

- [ ] **Step 5: Run GREEN atomic tests repeatedly**

Run:

```bash
gofmt -w internal/repository/git_status_repo.go internal/repository/git_status_transition.go internal/repository/git_status_transition_test.go
go test ./internal/repository -run '^TestGitStatusRepositoryApplyTransition' -count=20 -v
```

Expected: PASS in all twenty runs; every concurrent run finishes at count `5` and state `paused`.

- [ ] **Step 6: Run all repository migrations/status tests**

```bash
go test ./internal/repository -count=1
```

Expected: PASS; Plan 1 migration versions are unchanged and no new table appears.

- [ ] **Step 7: Commit atomic status transitions**

```bash
git add internal/repository/git_status_repo.go internal/repository/git_status_transition.go internal/repository/git_status_transition_test.go
git commit -m "feat: persist git failure transitions atomically"
```

### Task 3: Build The Deterministic Fake-Clock Scheduler

**Files:**
- Create: `internal/service/git_scheduler.go`
- Create: `internal/service/git_scheduler_test.go`

- [ ] **Step 1: Write RED fake-clock scheduler tests**

Add exact tests:

```go
func TestGitSchedulerStaggersOverdueBasesInConfigOrder(t *testing.T)
func TestGitSchedulerSupportsExactlyFiveFifteenThirtySixtyMinutes(t *testing.T)
func TestGitSchedulerUsesPersistedLastAttemptAsIntervalAnchor(t *testing.T)
func TestGitSchedulerQueuesDueBasesInStableOrder(t *testing.T)
func TestGitSchedulerDoesNotQueueDisabledPausedConflictOrReconnect(t *testing.T)
func TestGitSchedulerBlocksInconsistentStatusAtFailureThreshold(t *testing.T)
func TestGitSchedulerWaitsWhileInitializingOrSyncing(t *testing.T)
func TestGitSchedulerReconcilesEnableIntervalRenamePathDisableAndForget(t *testing.T)
func TestGitSchedulerStatusWakeUsesManualAttempt(t *testing.T)
func TestGitSchedulerDeduplicatedQueueDoesNotSpin(t *testing.T)
func TestGitSchedulerRetriesMetadataFailureAfterThirtySeconds(t *testing.T)
func TestGitSchedulerMetadataFailureKeepsPreviousSchedule(t *testing.T)
func TestGitSchedulerRejectsNilDependencies(t *testing.T)
func TestGitSchedulerCancellationStopsTimerAndQueuesNothing(t *testing.T)
```

The fake clock owns all timers and advances only when the test calls `Advance`; tests must not call `time.Sleep` or use real deadlines to drive scheduler behavior.

- [ ] **Step 2: Run the RED scheduler tests**

Run:

```bash
go test ./internal/service -run '^TestGitScheduler' -count=1 -v
```

Expected: FAIL to compile because `newGitScheduler` and `gitScheduler.run` do not exist.

- [ ] **Step 3: Implement real clock adapters and scheduler state**

Create `internal/service/git_scheduler.go` with:

```go
type realGitSchedulerClock struct{}

func (realGitSchedulerClock) Now() time.Time { return time.Now() }
func (realGitSchedulerClock) NewTimer(delay time.Duration) gitSchedulerTimer {
	return &realGitSchedulerTimer{timer: time.NewTimer(delay)}
}

type realGitSchedulerTimer struct{ timer *time.Timer }

func (t *realGitSchedulerTimer) C() <-chan time.Time        { return t.timer.C }
func (t *realGitSchedulerTimer) Stop() bool                 { return t.timer.Stop() }
func (t *realGitSchedulerTimer) Reset(d time.Duration) bool { return t.timer.Reset(d) }

type gitScheduleEntry struct {
	Snapshot    gitcmd.ConfiguredBase
	Due         time.Time
	LastAttempt *time.Time
	Order       int
	Blocked     bool
}

type gitScheduler struct {
	clock         gitSchedulerClock
	statuses      gitScheduleStatusReader
	snapshots     gitScheduleSource
	configChanges <-chan struct{}
	queue         gitScheduleQueue
	logger        *log.Logger
	statusChanged chan struct{}
	entries       map[string]gitScheduleEntry
	startedAt     time.Time
}

func newGitScheduler(
	clock gitSchedulerClock,
	statuses gitScheduleStatusReader,
	snapshots gitScheduleSource,
	configChanges <-chan struct{},
	queue gitScheduleQueue,
	logger *log.Logger,
) *gitScheduler {
	if clock == nil || statuses == nil || snapshots == nil || configChanges == nil || queue == nil {
		panic("service.newGitScheduler: nil dependency")
	}
	return &gitScheduler{
		clock: clock, statuses: statuses, snapshots: snapshots,
		configChanges: configChanges, queue: queue,
		logger: normalizeGitSchedulerLogger(logger),
		statusChanged: make(chan struct{}, 1),
		entries: make(map[string]gitScheduleEntry),
	}
}

func (s *gitScheduler) notifyStatusChanged() {
	select {
	case s.statusChanged <- struct{}{}:
	default:
	}
}
```

- [ ] **Step 4: Implement reconciliation without running Git**

`reconcile(ctx, startup bool)` reads snapshots once, gets the current status by canonical path, and updates entries. Use this complete state rule:

```go
func gitScheduleBlocked(status model.GitStatus) bool {
	if status.ConsecutiveFailures >= gitFailurePauseThreshold {
		return true
	}
	switch status.State {
	case model.GitStatePaused, model.GitStateConflict,
		model.GitStateNeedsReconnect, model.GitStateInitializing,
		model.GitStateSyncing, model.GitStateUnconfigured:
		return true
	default:
		return false
	}
}

func gitScheduleDue(
	now time.Time,
	startupSlot time.Time,
	status model.GitStatus,
	interval time.Duration,
) time.Time {
	if status.LastAttempt == nil {
		return startupSlot
	}
	due := status.LastAttempt.Add(interval)
	if due.Before(startupSlot) {
		return startupSlot
	}
	if due.Before(now) {
		return now
	}
	return due
}
```

Rules enforced in `reconcile`:

- Reject an interval outside `5`, `15`, `30`, `60` by logging base name plus fixed text and omit its entry; Plan 1 normally prevents this, so scheduler must not repair config.
- A missing status is treated as `needs_reconnect` and blocked.
- A row with `consecutive_failures >= 5` is blocked even if an external/legacy writer left its state as `error`; the scheduler never spends a sixth automatic attempt to repair inconsistent metadata.
- Existing same-path/same-interval entries retain `Due` when both `LastAttempt` values are nil or `time.Time.Equal` reports equality; update snapshot/name/order in place.
- A changed `LastAttempt`, path, interval, or newly eligible base recomputes due.
- Remove every path absent/disabled in the latest snapshot.
- Sort due work by `Due`, then `Order`, then canonical path.
- Build the entire next entry map in local variables and assign `s.entries = next` only after every snapshot/status read succeeds. A metadata error keeps the previous complete schedule and retries after `gitSchedulerRetryDelay`; it never leaves a partially reconciled map.
- Compute startup/newly-eligible slot indexes only among autosync-enabled, currently unblocked entries in current config order, so disabled or paused rows do not create unexplained gaps in the five-second sequence.

- [ ] **Step 5: Implement the cancellable timer loop**

Use one timer and no ticker:

```go
func (s *gitScheduler) run(ctx context.Context) {
	s.startedAt = s.clock.Now()
	if err := s.reconcile(ctx, true); err != nil {
		s.logger.Printf("Git autosync scheduler metadata error: %v", err)
	}
	timer := s.clock.NewTimer(gitSchedulerRetryDelay)
	defer timer.Stop()

	for {
		delay := s.nextDelay(s.clock.Now())
		if !timer.Stop() {
			select {
			case <-timer.C():
			default:
			}
		}
		timer.Reset(delay)

		select {
		case <-ctx.Done():
			return
		case <-s.configChanges:
			if err := s.reconcile(ctx, false); err != nil {
				s.logger.Printf("Git autosync scheduler metadata error: %v", err)
			}
		case <-s.statusChanged:
			if err := s.reconcile(ctx, false); err != nil {
				s.logger.Printf("Git autosync scheduler metadata error: %v", err)
			}
		case <-timer.C():
			s.queueDue(ctx, s.clock.Now())
			if err := s.reconcile(ctx, false); err != nil {
				s.logger.Printf("Git autosync scheduler metadata error: %v", err)
			}
		}
	}
}
```

`nextDelay` returns the earliest unblocked due delay, `gitSchedulerRetryDelay` when no entry is currently runnable, and `0` for an overdue entry. `queueDue` calls the frozen queue function in sorted order. After every nil error, including `deduplicated:true`, set that entry's in-memory `Due` to `now + interval` before processing the next event. On `ErrGitManagerClosed` or canceled context, return immediately. Other safe admission failures log only safe `Error()` text and move due by `gitSchedulerRetryDelay`; they never mutate the failure count.

- [ ] **Step 6: Implement a deterministic fake clock in the test file**

Use these concrete test types, with `Advance` collecting due timers under the clock mutex and sending at most one event per armed timer after releasing it:

```go
type fakeGitSchedulerClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeGitSchedulerTimer]struct{}
}

type fakeGitSchedulerTimer struct {
	clock    *fakeGitSchedulerClock
	channel  chan time.Time
	deadline time.Time
	armed    bool
}

func newFakeGitSchedulerClock(now time.Time) *fakeGitSchedulerClock {
	return &fakeGitSchedulerClock{
		now: now, timers: make(map[*fakeGitSchedulerTimer]struct{}),
	}
}

func (c *fakeGitSchedulerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeGitSchedulerClock) NewTimer(delay time.Duration) gitSchedulerTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeGitSchedulerTimer{
		clock: c, channel: make(chan time.Time, 1),
		deadline: c.now.Add(delay), armed: true,
	}
	c.timers[timer] = struct{}{}
	return timer
}

func (t *fakeGitSchedulerTimer) C() <-chan time.Time { return t.channel }

func (t *fakeGitSchedulerTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasArmed := t.armed
	t.armed = false
	return wasArmed
}

func (t *fakeGitSchedulerTimer) Reset(delay time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasArmed := t.armed
	t.deadline = t.clock.now.Add(delay)
	t.armed = true
	return wasArmed
}

func (c *fakeGitSchedulerClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	now := c.now
	due := make([]*fakeGitSchedulerTimer, 0)
	for timer := range c.timers {
		if timer.armed && !timer.deadline.After(now) {
			timer.armed = false
			due = append(due, timer)
		}
	}
	c.mu.Unlock()
	for _, timer := range due {
		select {
		case timer.channel <- now:
		default:
		}
	}
}
```

Before every fake `Reset`, the production loop drains a fired channel as shown in Step 5. The fake remains race-safe when scheduler and test goroutines run concurrently.

- [ ] **Step 7: Run GREEN scheduler tests**

```bash
gofmt -w internal/service/git_scheduler.go internal/service/git_scheduler_test.go
go test ./internal/service -run '^TestGitScheduler' -count=20 -v
go test -race ./internal/service -run '^TestGitScheduler' -count=10
```

Expected: all runs PASS with no timer leak, timeout, flaky order, or race report.

- [ ] **Step 8: Commit the scheduler lane**

```bash
git add internal/service/git_scheduler.go internal/service/git_scheduler_test.go
git commit -m "feat: schedule deterministic git autosync"
```

### Task 4: Integrate Breaker, Resume, Scheduler, And Close Into GitManager

**Files:**
- Modify: `internal/service/git_manager.go`
- Modify: `internal/service/git_manager_test.go`

- [ ] **Step 1: Write RED manager outcome tests**

Add exact tests:

```go
func TestGitManagerOperationalFailuresPauseExactlyOnFifth(t *testing.T)
func TestGitManagerSuccessResetsPersistedFailures(t *testing.T)
func TestGitManagerConflictDoesNotConsumeFailure(t *testing.T)
func TestGitManagerConflictAbortPreservesFailureAndPauses(t *testing.T)
func TestGitManagerInitializeFailureDoesNotConsumeFailure(t *testing.T)
func TestGitManagerShutdownCancellationDoesNotConsumeFailure(t *testing.T)
func TestGitManagerManualAndScheduledSyncShareFailureSequence(t *testing.T)
func TestGitManagerLogsBreakerOnceWithoutDiagnosticOrURL(t *testing.T)
func TestGitManagerNotifiesSchedulerAfterAttemptAndTerminalStatus(t *testing.T)
func TestGitManagerConfigChangeSupersedesOnlyQueuedStaleOperation(t *testing.T)
func TestGitManagerSamePathRenameRebindsQueuedOperation(t *testing.T)
func TestGitManagerMoveDisableForgetDoNotRepublishStaleStatus(t *testing.T)
```

Use the real temporary Plan 1 repository, run failures one at a time through the Plan 3 worker, reopen SQLite after the fifth, and assert persisted state/counter/error. The log test places a secret URL only in private wrapped diagnostics and requires it absent from captured logs.

- [ ] **Step 2: Write RED resume tests**

```go
func TestGitManagerResumeResetsBreakerAndQueuesOneSync(t *testing.T)
func TestGitManagerResumeAcceptsConflictAbortPause(t *testing.T)
func TestGitManagerResumeRejectsReadyConflictAndReconnect(t *testing.T)
func TestGitManagerResumeRestoresPausedStatusWhenJournalInsertFails(t *testing.T)
func TestGitManagerResumeDeduplicatesConcurrentRequests(t *testing.T)
func TestGitManagerResumeUsesFreshNamePathAndFingerprint(t *testing.T)
func TestGitManagerResumeNextFailureStartsAtOne(t *testing.T)
```

- [ ] **Step 3: Write RED scheduler ownership/close tests**

```go
func TestGitManagerAutosyncUsesExistingSequentialWorker(t *testing.T)
func TestGitManagerStartStartsWorkerBeforeScheduler(t *testing.T)
func TestGitManagerCloseCancelsSchedulerAndRunningNetworkCommand(t *testing.T)
func TestGitManagerCloseStartsNoQueuedOrScheduledJobAfterCancel(t *testing.T)
func TestGitManagerCloseIsIdempotentAndConcurrentSafe(t *testing.T)
```

- [ ] **Step 4: Run RED manager tests**

```bash
go test ./internal/service -run '^TestGitManager(Operational|Success|Conflict|Initialize|Shutdown|Manual|Logs|Notifies|Config|SamePath|Move|Resume|Autosync|Start|Close)' -count=1 -v
```

Expected: FAIL because manager has no atomic transition helper, scheduler owner, or `Resume`.

- [ ] **Step 5: Add an autosync-aware source-compatible constructor**

Keep the exact Plan 3 `NewGitManager` signature and behavior for existing tests. Add:

```go
func NewGitManagerWithAutosync(
	gitService *gitcmd.Service,
	statuses *repository.GitStatusRepository,
	operations *repository.GitOperationRepository,
	prober *GitProbeService,
	snapshot func(string) (gitcmd.ConfiguredBase, bool, error),
	notes *NoteService,
	coordinator *BaseOperationCoordinator,
	snapshots gitScheduleSource,
	configChanges <-chan struct{},
	logger *log.Logger,
) *GitManager {
	return newGitManagerWithAutosync(
		gitService, statuses, operations, prober, snapshot, notes,
		coordinator, snapshots, configChanges, realGitSchedulerClock{}, logger,
	)
}
```

The private constructor first builds the normal Plan 3 manager, stores `snapshots` as the all-base reconciliation source, then creates `m.scheduler = newGitScheduler(clock, statuses, snapshots, configChanges, m.QueueSync, logger)`. The old constructor leaves both `scheduler` and the all-base source nil and retains exact Plan 3 behavior. Do not create a second worker or scheduled operation kind.

- [ ] **Step 6: Centralize all manager status writes**

Add:

```go
func (m *GitManager) applyStatus(
	ctx context.Context,
	status model.GitStatus,
	failures repository.GitFailureAction,
) (model.GitStatus, error) {
	stored, err := m.statuses.ApplyTransition(ctx, repository.GitStatusTransition{
		Status: status, Failures: failures,
	})
	if err != nil {
		return model.GitStatus{}, err
	}
	if m.scheduler != nil {
		m.scheduler.notifyStatusChanged()
	}
	return stored, nil
}
```

Refactor queued/progress/running publications to `PreserveGitFailures`. Set `LastAttempt` exactly once when the worker changes an operation from queued to running. Terminal publication calls `classifyGitOutcome` once and passes its action to `applyStatus`. Use the returned stored state/count as public truth.

When `IncrementGitFailures` returns `paused` and the proposed terminal state was `error`, log exactly one safe record:

```go
m.logger.Printf(
	"Git autosync paused for base %q after %d consecutive failures (%s)",
	stored.Base,
	stored.ConsecutiveFailures,
	stored.Error.Code,
)
```

Never log `Diagnostic()`, command argv, remote URL, or wrapped error text.

- [ ] **Step 7: Preserve Plan 4 terminal semantics**

Apply these rules in the existing terminal switch:

- `ConflictError`: operation/status conflict, mutation block true, preserve count.
- conflict complete success: ready, mutation block false, reset count.
- conflict complete operational push failure: error or fifth-failure paused, mutation block false, increment count.
- conflict abort success: paused, mutation block false, preserve count.
- conflict abort failure: preserve count and Plan 4's conflict/recovery state and mutation block; never report a successful pause.
- initialize success: ready and reset count; initialize failure preserves count.
- branch-deleted/remote-history-rewritten sync failure: immediate paused safety state, preserve count, and do not schedule.
- shutdown cancellation: finish the journal as failed with the landed fixed `operation_interrupted` safe error, publish `error` while preserving count (or `conflict` if immediate local inspection proves one), and never start the next FIFO entry.
- freshness mismatch: finish stale journal only and do not overwrite SettingsService's reconciled status.

Extend Plan 3 queue admission for the config-publication race without adding another queue or journal API:

```text
resolve the request against the current exact-name snapshot
under GitManager.mu inspect the active in-memory job for the canonical path
same path/name/fingerprint: return the existing operation as deduplicated
same path and remote fingerprint, old name absent, current job still queued:
    replace only the in-memory job snapshot/name with the current same-path rename
    preserve the journal row and operation ID; return it as deduplicated
different full fingerprint and current job still queued:
    finish the stale journal operation with fixed CodeNeedsReconnect
    remove that exact job from FIFO/inFlight
    insert and publish the current initialize/sync operation normally
running job: never replace it; return git_repository_in_use
```

This is safe because running Git work holds `BaseOperationCoordinator`, so a successful config publication cannot overtake a running operation. A queued operation has not invoked Git and may be rebound for a pure same-path rename or retired for a real Git config change. At dequeue, first resolve by exact current name; if that name disappeared, read the all-base source once and accept only one snapshot whose canonical path and `RemoteFingerprint` equal the queued values. That unique alias is the same-path rename case; zero or multiple matches are a freshness failure. Read exact/all-base snapshots before taking `GitManager.mu` or after releasing it; never call `SettingsService` while the manager mutex is held. Publish progress/terminal status with the current base name while retaining the historical journal row's original admission identity. Path move, Git disable, and forget have no same-path configured snapshot, so the stale operation is finished without recreating the deleted/reconciled status. Tests must hold the worker before dequeue so each race is deterministic.

- [ ] **Step 8: Implement explicit resume using the existing queue insertion path**

Expose:

```go
func (m *GitManager) Resume(
	ctx context.Context,
	baseName string,
) (gitcmd.Operation, bool, error)
```

Implementation order:

```text
resolve immutable snapshot by exact name
acquire BaseOperationCoordinator
re-resolve and compare name/path/fingerprint
acquire GitManager.mu
reject closed manager
load exact persisted status
when status is syncing and the journal has an active sync for the same path/name/fingerprint, return it with deduplicated true
require state paused
save previous paused status value
apply ready status with ResetGitFailures and clear operation/stage/error
call the existing private durable queue insertion with OperationSync
if insertion or syncing publication fails, restore previous status with Upsert
append one FIFO job, signal worker, notify scheduler
release manager mutex, then coordinator
return operation and deduplication flag
```

Refactor Plan 3 `QueueSync` and resume to share one `queueValidatedLocked` helper. Resume is the only caller allowed to pass `allowPaused:true`; all ordinary manual/scheduled sync calls retain Plan 4 `ErrGitPaused` admission. If insertion or syncing publication fails after the reset, restore with `context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)` so HTTP cancellation cannot strand a false ready/count-zero state. Return `errors.Join(queueErr, restoreErr)` when restoration itself fails; log only fixed persistence context, never status error details or remote data.

- [ ] **Step 9: Make manager start/close own both goroutines**

Add scheduler completion and idempotent-close fields. `Start` starts the Plan 3 worker first, then scheduler. `Close` uses `sync.Once` to set closed, cancel the lifetime context, broadcast the worker condition, and wait for both worker/scheduler completion channels. Concurrent callers wait on one `closeDone` channel and receive the same stored error. Cancellation occurs before waiting, so a blocked `git fetch`/`push` receives context cancellation immediately. The worker checks lifetime cancellation before every dequeue and starts no next job.

- [ ] **Step 10: Run GREEN manager tests**

```bash
gofmt -w internal/service/git_manager.go internal/service/git_manager_test.go
go test ./internal/service -run '^TestGitManager' -count=1 -v
go test -race ./internal/service -run '^TestGitManager' -count=5
```

Expected: PASS; fifth failure pauses exactly, resume queues once, scheduler and manual work have maximum Git-service concurrency `1`, and close reports no race.

- [ ] **Step 11: Commit manager integration**

```bash
git add internal/service/git_manager.go internal/service/git_manager_test.go
git commit -m "feat: add git autosync circuit breaker"
```

### Task 5: Publish Schedule Events And Reconcile Settings Lifecycles

**Files:**
- Modify: `internal/service/settings_service.go`
- Modify: `internal/service/settings_service_test.go`

- [ ] **Step 1: Write RED ordered-snapshot/event tests**

```go
func TestSettingsServiceGitSnapshotsReturnsConfiguredBasesInConfigOrder(t *testing.T)
func TestSettingsServiceGitSnapshotsReturnsDetachedValues(t *testing.T)
func TestSettingsServiceGitConfigChangesCoalescesWithoutBlocking(t *testing.T)
func TestSettingsServiceGitConfigChangesSignalsOnlySuccessfulPublication(t *testing.T)
func TestSettingsServiceGitConfigChangesSignalsConfigureDisableRenameMoveForgetReplace(t *testing.T)
```

- [ ] **Step 2: Write RED failure reconciliation tests**

```go
func TestSettingsServiceConfigureGitResetsCircuitBreaker(t *testing.T)
func TestSettingsServiceDisableAutosyncResetsCircuitBreaker(t *testing.T)
func TestSettingsServiceSamePathRenamePreservesFailureCount(t *testing.T)
func TestSettingsServicePathChangeCreatesReconnectWithZeroFailures(t *testing.T)
func TestSettingsServiceDisableGitDeletesPausedStatus(t *testing.T)
func TestSettingsServiceForgetDeletesPausedStatus(t *testing.T)
func TestSettingsServiceReplaceConfigReconcilesFailureCountsByCanonicalPath(t *testing.T)
func TestSettingsServiceFailedConfigSaveRestoresCounterAndEmitsNoScheduleEvent(t *testing.T)
```

- [ ] **Step 3: Run RED settings tests**

```bash
go test ./internal/service -run '^TestSettingsService(GitSnapshots|GitConfigChanges|ConfigureGitResets|DisableAutosync|SamePathRename|PathChangeCreates|DisableGitDeletes|ForgetDeletes|ReplaceConfigReconciles|FailedConfigSaveRestores)' -count=1 -v
```

Expected: FAIL because ordered snapshots/event accessors do not exist and configure currently preserves a paused counter.

- [ ] **Step 4: Add one nonblocking coalesced settings event**

Initialize this field in both landed SettingsService constructors:

```go
gitConfigChanged chan struct{}
```

Use:

```go
func (s *SettingsService) notifyGitConfigChangedLocked() {
	select {
	case s.gitConfigChanged <- struct{}{}:
	default:
	}
}

func (s *SettingsService) GitConfigChanges() <-chan struct{} {
	return s.gitConfigChanged
}
```

Create the channel with capacity `1`. Call the notifier only after `applyConfigLocked` has successfully published `s.config`; never call it on validation, status mutation, config persistence, or rollback failure. Sending remains safe while locks are held because it never waits and the scheduler only reads later.

- [ ] **Step 5: Publish all immutable snapshots in config order**

Add:

```go
func (s *SettingsService) GitSnapshots() ([]gitcmd.ConfiguredBase, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshots := make([]gitcmd.ConfiguredBase, 0, len(s.config.Bases))
	for _, base := range s.config.Bases {
		if !base.GitConfigured() {
			continue
		}
		snapshot, err := configuredBase(base)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}
```

Extract the landed Plan 3 canonical configured-base/fingerprint body into `configuredBase(model.Base) (gitcmd.ConfiguredBase, error)`. Make both `GitSnapshot` and `GitSnapshots` call that one function. Do not recalculate fingerprints differently or return references to config slices.

- [ ] **Step 6: Apply the exact reconciliation matrix**

Extend existing Plan 1 compensation snapshots, not a new transaction mechanism:

- Configure any Git field, including `auto_sync:false`: write `needs_reconnect`, set `ConsecutiveFailures:0`, retain trusted remote OID only under Plan 3's same path/URL/branch fingerprint rule, persist config, emit one event; handler queues one initialize operation afterward. When autosync is false, the scheduler removes recurring work and only observes that explicit initialize operation.
- Same canonical path rename: update only status `Base`; preserve count, times, OIDs, error, and changed paths.
- Path change: delete old status and create new `needs_reconnect` with count `0`; preserve Git config fields, including saved autosync preference.
- Disable Git: delete status and emit; never inspect or remove `.git`.
- Forget: delete status and emit; never remove files.
- Failed config publication: restore every previous status including exact count/state, emit nothing.

- [ ] **Step 7: Run GREEN settings tests**

```bash
gofmt -w internal/service/settings_service.go internal/service/settings_service_test.go
go test ./internal/service -run '^TestSettingsService' -count=1
go test -race ./internal/service -run '^TestSettingsService(GitSnapshots|GitConfigChanges|.*Git|.*Base)' -count=3
```

Expected: PASS; no send blocks under settings/coordinator locks and all compensation tests retain exact status.

- [ ] **Step 8: Commit settings reconciliation**

```bash
git add internal/service/settings_service.go internal/service/settings_service_test.go
git commit -m "feat: reconcile git autosync settings"
```

### Task 6: Expose The Guarded Resume API

**Files:**
- Modify: `internal/handlers/git_handler.go`
- Modify: `internal/handlers/git_handler_test.go`
- Modify: `internal/handlers/git_routes.go`
- Modify: `internal/handlers/git_routes_test.go`
- Modify: `internal/handlers/errors.go`
- Modify: `internal/handlers/errors_test.go`

- [ ] **Step 1: Write RED handler tests**

```go
func TestGitHandlerResumeReturnsAcceptedOperation(t *testing.T)
func TestGitHandlerResumeDuplicateReturnsSameOperation(t *testing.T)
func TestGitHandlerResumeRequiresBase(t *testing.T)
func TestGitHandlerResumeMapsNotPaused(t *testing.T)
func TestGitHandlerResumeDoesNotLeakDiagnosticOrURL(t *testing.T)
```

Expected response:

```json
{
  "operation_id": "0123456789abcdef0123456789abcdef",
  "status": "queued",
  "deduplicated": false
}
```

- [ ] **Step 2: Write RED method/guard tests**

Require `POST /api/git/resume?base=<name>`, `Allow: POST` for wrong methods, `428 setup_required` before setup, and `403 forbidden_origin` for a cross-origin POST before manager invocation.

- [ ] **Step 3: Run RED handler tests**

```bash
go test ./internal/handlers -run 'TestGit(HandlerResume|Routes.*Resume)|TestWriteServiceError.*NotPaused' -count=1 -v
```

Expected: FAIL because resume is not in the operation interface or route table.

- [ ] **Step 4: Extend the existing narrow operation interface and handler**

Add to the landed `GitOperations` interface:

```go
Resume(context.Context, string) (gitcmd.Operation, bool, error)
```

Add:

```go
func (h *GitHandler) Resume(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimSpace(r.URL.Query().Get("base"))
	if base == "" {
		writeMissingField(w, "base")
		return
	}
	operation, deduplicated, err := h.operations.Resume(r.Context(), base)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, gitOperationResponse(operation, deduplicated))
}
```

Extract this exact shared conversion and call it from manual sync, configure, and resume:

```go
func gitOperationResponse(operation gitcmd.Operation, deduplicated bool) model.GitOperationResponse {
	return model.GitOperationResponse{
		OperationID: operation.ID,
		Status: string(operation.State),
		Deduplicated: deduplicated,
	}
}
```

The resume body remains the exact JSON above.

- [ ] **Step 5: Register the guarded route**

Append beside Plan 3 sync registration:

```go
mux.Handle("/api/git/resume", RequireLocalOrigin(methods(map[string]http.Handler{
	http.MethodPost: RequireSetup(state, http.HandlerFunc(handler.Resume)),
})))
```

- [ ] **Step 6: Map not-paused safely**

Map `git.CodeNotPaused` to HTTP `409` and code `git_not_paused`. Use only fixed `SafeError.Message`; never serialize private diagnostic/cause.

- [ ] **Step 7: Run GREEN handler tests**

```bash
gofmt -w internal/handlers/git_handler.go internal/handlers/git_handler_test.go internal/handlers/git_routes.go internal/handlers/git_routes_test.go internal/handlers/errors.go internal/handlers/errors_test.go
go test ./internal/handlers -run 'TestGit|TestWriteServiceError' -count=1
```

Expected: PASS; resume returns `202` for new/deduplicated operations and all existing Git routes retain guards/methods.

- [ ] **Step 8: Commit resume HTTP support**

```bash
git add internal/handlers/git_handler.go internal/handlers/git_handler_test.go internal/handlers/git_routes.go internal/handlers/git_routes_test.go internal/handlers/errors.go internal/handlers/errors_test.go
git commit -m "feat: expose git autosync resume api"
```

### Task 7: Wire Startup Scheduling And Immediate Shutdown Cancellation

**Files:**
- Modify: `cmd/api/main.go`
- Modify: `cmd/api/git_runtime_test.go`

- [ ] **Step 1: Write RED lifecycle tests**

```go
func TestGitAutosyncStartsAfterLocalRecovery(t *testing.T)
func TestGitAutosyncStartupStaggerBeginsAfterManagerStart(t *testing.T)
func TestGitAutosyncIncludesInactiveConfiguredBases(t *testing.T)
func TestGitShutdownCancelsManagerWhenContextEndsBeforeHTTPDrain(t *testing.T)
func TestGitShutdownWaitsForManagerBeforeNoteServiceAndDatabaseClose(t *testing.T)
func TestGitShutdownQueuesNoFinalSyncOrCommit(t *testing.T)
func TestGitUnconfiguredStartupStillRunsNoGitCommand(t *testing.T)
```

Assert event order:

```text
construct shared Git runtime
recover every configured repository locally
start manager worker
start scheduler
start initial NoteService SyncFS
construct/register router
serve
context cancellation
cancel manager lifetime immediately
drain HTTP server
wait manager
close NoteService
close metadata database
```

- [ ] **Step 2: Run RED runtime tests**

```bash
go test ./cmd/api -run 'TestGit(Autosync|Shutdown|Unconfigured)' -count=1 -v
```

Expected: FAIL because production uses the old manager constructor and only deferred close.

- [ ] **Step 3: Use the autosync-aware manager constructor**

Replace only the Plan 3 manager construction in `cmd/api/main.go`:

```go
gitManager := service.NewGitManagerWithAutosync(
	gitService,
	gitStatusRepo,
	gitOperationRepo,
	gitProbeService,
	settingsService.GitSnapshot,
	noteService,
	coordinator,
	settingsService.GitSnapshots,
	settingsService.GitConfigChanges(),
	log.Default(),
)
```

Retain one runner, client, manager, status repository, operation repository, settings service, note service, coordinator, and metadata DB.

- [ ] **Step 4: Preserve recovery-before-scheduler order**

Keep:

```go
if err := gitManager.RecoverLocal(ctx, configuredSnapshots); err != nil {
	return fmt.Errorf("восстановить состояние Git: %w", err)
}
if err := gitManager.Start(); err != nil {
	return fmt.Errorf("запустить менеджер Git: %w", err)
}
```

`Start` now starts worker then scheduler. It remains before the existing asynchronous initial `noteService.SyncFS` and before router serving.

- [ ] **Step 5: Cancel Git when shutdown starts, not after HTTP shutdown finishes**

Immediately after successful manager start, install:

```go
stopGitClose := context.AfterFunc(ctx, func() {
	if err := gitManager.Close(); err != nil {
		log.Printf("Ошибка остановки Git manager: %v", err)
	}
})
defer stopGitClose()
defer func() {
	if err := gitManager.Close(); err != nil {
		returnErr = errors.Join(returnErr, fmt.Errorf("остановить Git manager: %w", err))
	}
}()
```

Register the manager defer after DB/NoteService defers so LIFO closure waits for manager before either dependency closes. `Close` is concurrent-safe/idempotent from Task 4. The callback error log must contain only manager lifecycle errors, never Git diagnostics.

- [ ] **Step 6: Run GREEN lifecycle tests repeatedly**

```bash
gofmt -w cmd/api/main.go cmd/api/git_runtime_test.go
go test ./cmd/api -run 'TestGit(Autosync|Shutdown|Unconfigured|Recovery)' -count=10 -v
go test -race ./cmd/api -run 'TestGit(Autosync|Shutdown)' -count=5
```

Expected: PASS; a blocked network fake observes cancellation before HTTP shutdown is released, no next job starts, and dependency close order is stable.

- [ ] **Step 7: Commit lifecycle wiring**

```bash
git add cmd/api/main.go cmd/api/git_runtime_test.go
git commit -m "feat: wire git autosync lifecycle"
```

### Task 8: Add Persistent Paused Alert And Resume Actions

**Hard prerequisites:** Plans 5 and 6 are merged in this worktree, Plan 6's full frontend suite/build passed, and the integrated frontend contains exactly one App-owned `gitPoller`, one `gitStatuses` collection, asynchronous `applyGitStatuses`, `gitBusyBase`, `gitActionErrors`, and the zero-argument `refreshGitStatuses()` seam. If any seam is absent, stop and finish Plans 5/6; do not recreate it in this task.

**Files:**
- Modify: `web/src/lib/api.js`
- Modify: `web/src/lib/api.test.js`
- Create: `web/src/lib/git/GitPausedAlert.svelte`
- Create: `web/src/lib/git/GitPausedAlert.test.js`
- Modify: `web/src/App.svelte`
- Modify: `web/src/App.test.js`
- Modify: `web/src/lib/NotesWorkspace.svelte`
- Modify: `web/src/lib/NotesWorkspace.test.js`

- [ ] **Frontend prerequisite: enforce Node 24 and install the exact lockfile before any Task 8 test**

Run:

```bash
node --version
node -e "if (Number(process.versions.node.split('.')[0]) !== 24) { console.error('Task 8 requires Node.js 24'); process.exit(1) }"
npm --prefix web ci
```

Expected: `node --version` prints `v24.x.x`, the gate exits `0`, and `npm ci` exits `0` without modifying `web/package.json` or `web/package-lock.json`. Stop immediately on any failure; Node 20/22 and `npm install` are not substitutes.

- [ ] **Step 1: Write RED API wrapper tests**

Import `getGitStatus` and `resumeGit`, then assert:

```js
it('gets one encoded Git status and resumes with no request body', async () => {
  const pausedStatus = {
    base: 'team/work',
    repository_path: '/notes/team/work',
    state: 'paused',
    operation_id: '11111111111111111111111111111111',
    stage: 'completed',
    ahead: 1,
    behind: 0,
    consecutive_failures: 5,
    last_attempt: '2026-09-01T12:15:00Z',
    last_success: '2026-09-01T11:00:00Z',
    changed_paths: [],
    remote_oid: '0123456789abcdef0123456789abcdef01234567',
    error: { code: 'remote_unreachable', message: 'Git remote is unreachable' },
  }
  fetchMock
    .mockResolvedValueOnce(jsonResponse({ statuses: [pausedStatus] }))
    .mockResolvedValueOnce(jsonResponse({
      operation_id: '0123456789abcdef0123456789abcdef',
      status: 'queued',
      deduplicated: false,
    }, 202))

  await expect(getGitStatus('team/work')).resolves.toEqual({
    statuses: [pausedStatus],
  })
  await expect(resumeGit('team/work')).resolves.toMatchObject({ status: 'queued' })

  expect(requestAt(fetchMock, 0)).toMatchObject({
    path: '/api/git/status?base=team%2Fwork', options: { method: 'GET' },
  })
  expect(requestAt(fetchMock, 1)).toMatchObject({
    path: '/api/git/resume?base=team%2Fwork', options: { method: 'POST' },
  })
  expect(requestAt(fetchMock, 1).options.body).toBeUndefined()
})

it('rejects a malformed successful resume payload', async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({
    operation_id: '', status: 'queued', deduplicated: false,
  }, 202))
  await expect(resumeGit('work')).rejects.toMatchObject({
    name: 'ApiError', status: 202, code: 'invalid_response',
  })
})
```

Every Git status fixture added by this task must satisfy the landed Plan 5 `validStatus` contract: nonempty `base`, valid `state`, nonnegative integer `ahead`, `behind`, and `consecutive_failures`, plus string-only `changed_paths`. Paused fixtures also carry the persisted operation/stage/timestamps/OID/safe-error fields shown above; do not use `{base, state}` partials that the production wrapper rejects.

- [ ] **Step 2: Run the API wrapper tests RED**

```bash
node -e "if (Number(process.versions.node.split('.')[0]) !== 24) process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/api.test.js
```

Expected: FAIL because `resumeGit` is undefined; the landed strict `getGitStatus` test remains GREEN.

- [ ] **Step 3: Implement the strict resume wrapper on the landed API seam**

Retain Plan 5's `getGitStatus`, private `requestChecked`, and private `validOperation` byte-for-byte. Append only:

```js
export function resumeGit(base) {
  return requestChecked(`/api/git/resume?base=${encodeURIComponent(base)}`, {
    method: 'POST',
  }, validOperation)
}
```

Do not call raw `request`, export validators, weaken successful-response checks, add a plural status wrapper, or duplicate any Plan 5 helper.

- [ ] **Step 4: Write RED paused-alert component tests**

Cover visible fixed heading/reason, five failures, machine-readable `<time>`, missing attempt fallback, busy disable, resume callback, settings callback, inline action failure, and absence when status is not paused. Use Russian accessible names `Повторить и возобновить` and `Открыть настройки Git`.

- [ ] **Step 5: Run the paused-alert tests RED**

```bash
node -e "if (Number(process.versions.node.split('.')[0]) !== 24) process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/git/GitPausedAlert.test.js
```

Expected: FAIL because `GitPausedAlert.svelte` does not exist.

- [ ] **Step 6: Create the complete persistent alert component**

Create `web/src/lib/git/GitPausedAlert.svelte`:

```svelte
<script>
  let {
    status = null,
    busy = false,
    error = '',
    onResume,
    onOpenSettings,
  } = $props()

  let paused = $derived(status?.state === 'paused')
  let failures = $derived(Number.isInteger(status?.consecutive_failures)
    ? status.consecutive_failures
    : 0)
  let attempt = $derived(typeof status?.last_attempt === 'string'
    ? status.last_attempt
    : '')
  let attemptLabel = $derived(attempt && !Number.isNaN(Date.parse(attempt))
    ? new Date(attempt).toLocaleString('ru-RU')
    : 'Время последней попытки неизвестно')
  let reason = $derived(typeof status?.error?.message === 'string' && status.error.message
    ? status.error.message
    : 'Автоматическая синхронизация остановлена до явного возобновления.')
</script>

{#if paused}
  <section
    role="alert"
    aria-labelledby="git-paused-title"
    class="border-b border-amber-300 bg-amber-50 px-4 py-3 text-amber-950"
  >
    <div class="mx-auto flex max-w-5xl flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div class="min-w-0">
        <h2 id="git-paused-title" class="text-sm font-bold">Git-синхронизация приостановлена</h2>
        <p class="mt-1 text-sm">{reason}</p>
        <p class="mt-1 text-xs text-amber-800">
          Последовательных ошибок: {failures}.
          {#if attempt}
            Последняя попытка: <time datetime={attempt}>{attemptLabel}</time>.
          {:else}
            {attemptLabel}.
          {/if}
        </p>
        {#if error}
          <p role="status" class="mt-2 text-sm font-medium text-red-700">{error}</p>
        {/if}
      </div>
      <div class="flex shrink-0 flex-wrap gap-2">
        <button
          type="button"
          onclick={onResume}
          disabled={busy}
          aria-busy={busy ? 'true' : 'false'}
          class="rounded-md bg-amber-700 px-3 py-2 text-sm font-semibold text-white hover:bg-amber-800 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-amber-800 disabled:cursor-wait disabled:opacity-60"
        >
          Повторить и возобновить
        </button>
        <button
          type="button"
          onclick={onOpenSettings}
          disabled={busy}
          class="rounded-md border border-amber-400 bg-white px-3 py-2 text-sm font-semibold text-amber-900 hover:bg-amber-100 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-amber-800 disabled:opacity-60"
        >
          Открыть настройки Git
        </button>
      </div>
    </div>
  </section>
{/if}
```

This is persistent page state, not a toast, modal, or auto-dismissed banner.

- [ ] **Step 7: Write RED NotesWorkspace composition tests**

Add tests that render a complete strict paused status and assert the alert is between the editor header and editor body, resume delegates exactly once without first invoking `runAfterUploads`, settings waits for `flushPendingUploads`, and non-paused status renders no alert. Prove the combined `transitioning || gitSyncBusy` state disables both alert actions without unmounting it, and that the same `gitSyncBusy` value still disables the landed manual-sync control. Do not introduce a `gitResumeBusy` prop.

- [ ] **Step 8: Run the NotesWorkspace tests RED**

```bash
node -e "if (Number(process.versions.node.split('.')[0]) !== 24) process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/NotesWorkspace.test.js
```

Expected: FAIL because `NotesWorkspace` does not accept Git pause props or render `GitPausedAlert`.

- [ ] **Step 9: Pass alert state through NotesWorkspace**

Import the component and add only the new `onResumeGit` prop; reuse the landed `gitStatus`, `gitSyncBusy`, and `gitSyncError` props. Derive one local `gitControlsBusy = transitioning || gitSyncBusy`, pass it to both the landed manual-sync control and `GitPausedAlert`, and pass `gitSyncError` as the alert's inline error. Render the alert between the editor header and editor body. `Открыть настройки Git` calls the existing upload-safe `runAfterUploads(onOpenSettings)` path. Resume calls the parent directly because App performs the stronger `flushWorkspace` sequence. This keeps `NotesWorkspace` independent of all-base polling and gives sync/resume/settings controls the same propagated busy state without a second lock or error store.

- [ ] **Step 10: Write RED App polling/resume tests**

Add mocked `resumeGit` beside the landed `getGitStatus` mock. Add one strict fixture helper and use it for every new status; never pass partial status objects through Plan 5's strict API mock contract:

```js
function gitStatus(overrides = {}) {
  return {
    base: 'personal',
    state: 'ready',
    ahead: 0,
    behind: 0,
    consecutive_failures: 0,
    changed_paths: [],
    ...overrides,
  }
}

const pausedGitStatus = gitStatus({
  state: 'paused',
  repository_path: '/notes/personal',
  operation_id: '11111111111111111111111111111111',
  stage: 'completed',
  consecutive_failures: 5,
  last_attempt: '2026-09-01T12:15:00Z',
  last_success: '2026-09-01T11:00:00Z',
  remote_oid: '0123456789abcdef0123456789abcdef01234567',
  error: { code: 'remote_unreachable', message: 'Git remote is unreachable' },
})
```

Add exact tests:

```js
it('shows a persisted paused status immediately after application load')
it('keeps the paused alert across polling failures')
it('flushes workspace before resume and refreshes status afterward')
it('keeps the alert and dirty buffer when resume flush fails')
it('keeps the alert and shows an inline error when resume API fails')
it('shares one global action lock between manual sync and resume')
it('ignores resume settlement after active base change or unmount')
```

Drive status changes through the landed Plan 5/6 `gitPoller` behavior already exposed by the existing mocks. The successful resume test requires `flushWorkspace`, then `resumeGit(baseName)`, then the zero-argument `refreshGitStatuses()` in that order. The lock test holds each API promise in turn and proves a pending settings/manual sync disables and rejects resume admission, then a pending resume disables all manual-sync controls across settings/footer; neither overlap may invoke the other API. Use deferred promises for active-base/unmount settlement guards. Do not add cadence, timer, generation, or cleanup tests already owned by Plans 5/6.

- [ ] **Step 11: Run the App polling/resume tests RED**

```bash
node -e "if (Number(process.versions.node.split('.')[0]) !== 24) process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/App.test.js
```

Expected: FAIL because the landed App has no paused alert, resume API orchestration, or shared sync/resume admission yet; its existing polling and conflict tests remain GREEN.

- [ ] **Step 12: Extend the landed Plan 5/6 poller owner and shared Git action lock**

Import `resumeGit` beside Plan 5's existing Git API imports. Retain the one `gitPoller`, `gitStatuses`, `activeGitStatus`, Plan 6 asynchronous `applyGitStatuses`, poller construction/start/stop, changed-path receipts, and cleanup. Make the landed refresh seam explicitly asynchronous and keep it zero-argument:

```js
async function refreshGitStatuses() {
  ensureGitPolling()
  return gitPoller?.refresh() ?? null
}

async function resumeGitSync() {
  const baseName = activeBase(config)?.name
  if (!mounted || !baseName || gitBusyBase !== '') return
  gitBusyBase = baseName
  gitActionErrors = { ...gitActionErrors, [baseName]: '' }
  let flushComplete = false

  try {
    try {
      await flushWorkspace()
      flushComplete = true
    } catch (error) {
      if (mounted && saveStatus !== 'error') showSaveError(error)
      throw error
    }

    if (!mounted || activeBase(config)?.name !== baseName) return
    await resumeGit(baseName)
    if (!mounted || activeBase(config)?.name !== baseName) return
    await refreshGitStatuses()
  } catch (error) {
    if (!mounted) return
    const fallback = flushComplete
      ? 'Не удалось возобновить Git-синхронизацию'
      : 'Не удалось сохранить рабочую область перед возобновлением Git-синхронизации'
    gitActionErrors = {
      ...gitActionErrors,
      [baseName]: errorMessage(error, fallback),
    }
  } finally {
    if (mounted && gitBusyBase === baseName) gitBusyBase = ''
  }
}
```

`runGitSync` and `resumeGitSync` must both gate on, set, and conditionally clear the same App-owned `gitBusyBase`; both report through the existing per-base `gitActionErrors`. Keep `runGitSync`'s mandatory flush and zero-argument refresh unchanged. Pass the existing `gitStatus={activeGitStatus}`, `gitSyncBusy={gitBusyBase !== ''}`, and `gitSyncError={gitActionErrors[config?.current_base] || ''}` props plus `onResumeGit={resumeGitSync}` to `NotesWorkspace`. Pass the same `gitBusyBase` to `SettingsWorkspace`, preserving global single-flight behavior across footer, settings cards, and resume. Do not add `gitResumeBusy`, `gitResumeError`, a direct `getGitStatus` call, a timer, a generation counter, another `applyGitStatuses`, or any poller lifecycle code.

- [ ] **Step 13: Run GREEN frontend tests**

```bash
node -e "if (Number(process.versions.node.split('.')[0]) !== 24) process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/api.test.js src/lib/git/GitPausedAlert.test.js src/lib/NotesWorkspace.test.js src/App.test.js
```

Expected: PASS under Node 24 from the exact lockfile; the landed poller suite remains GREEN, no pending Git action survives unmount, and the alert remains until backend status leaves `paused`.

- [ ] **Step 14: Build the production frontend**

```bash
node -e "if (Number(process.versions.node.split('.')[0]) !== 24) process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web run build
```

Expected: Vite exits `0`; no Svelte accessibility warning is emitted for the alert/actions.

- [ ] **Step 15: Commit frontend pause recovery**

```bash
git add web/src/lib/api.js web/src/lib/api.test.js web/src/lib/git/GitPausedAlert.svelte web/src/lib/git/GitPausedAlert.test.js web/src/App.svelte web/src/App.test.js web/src/lib/NotesWorkspace.svelte web/src/lib/NotesWorkspace.test.js
git commit -m "feat: show persistent git pause recovery"
```

### Task 9: Document And Verify The Complete Resilience Lifecycle

**Files:**
- Modify: `docs/api.md`
- Modify: `internal/service/git_manager_test.go`
- Modify: `internal/handlers/git_handler_test.go`
- Modify: `cmd/api/git_runtime_test.go`
- Modify: `web/src/App.test.js`

- [ ] **Step 1: Add `TestGitAutosyncResilienceLifecycle`**

Use two temporary configured bases with intervals `5` and `15`: recover, start, advance fake time through startup slots, fail base A five times while base B succeeds between A attempts, reopen metadata, assert only A is paused, issue resume, succeed A, and require count `0`/ready. Rename B at the same path, disable A autosync, move B, forget A, and assert timers/status rows follow the reconciliation matrix without overlapping Git service calls.

- [ ] **Step 2: Add `TestGitAutosyncShutdownDuringScheduledPush`**

Block a scheduled push, cancel application context, require runner context cancellation, preserve the pre-shutdown failure count, start no queued base, reopen metadata/recover locally without network, and assert no hidden commit or push was added.

- [ ] **Step 3: Add complete resume HTTP/UI flow coverage**

Backend test `TestGitResumeRESTLifecycle`: `GET status` returns persisted paused status after repository reopen; ordinary sync returns `409 git_paused`; resume returns `202`; duplicate resume shares operation ID; successful operation returns ready/count `0`.

Frontend test `shows the complete persisted pause and resume lifecycle`: initial poll shows the alert, resume flushes the note, operation status changes to syncing, later polling removes the alert only when ready, and settings action opens the existing settings screen. Build every snapshot with Task 8's strict `gitStatus(...)` fixture and include all required `ahead`, `behind`, `consecutive_failures`, and `changed_paths` fields; the paused snapshot also includes repository path, operation/stage, timestamps, remote OID, and safe error. Do not bypass Plan 5's strict wrapper with partial `{base, state}` objects.

- [ ] **Step 4: Run focused lifecycle tests**

```bash
go test ./internal/repository ./internal/service ./internal/handlers ./cmd/api -run 'TestGit(Autosync|Scheduler|Manager|Resume|Shutdown|StatusRepositoryApply)' -count=1 -v
node --version
node -e "if (Number(process.versions.node.split('.')[0]) !== 24) { console.error('Task 9 requires Node.js 24'); process.exit(1) }" &&
npm --prefix web ci &&
npm --prefix web test -- --run src/lib/git/GitPausedAlert.test.js src/App.test.js
```

Expected: backend tests PASS with maximum Git operation concurrency `1` and no real-time sleeps in scheduler tests; Node reports `v24.x.x`; the hard gate, lockfile install, and focused frontend tests exit `0`. Stop before any frontend test if the Node gate or `npm ci` fails.

- [ ] **Step 5: Document status and resume contracts**

Update `docs/api.md` with:

```text
GET  /api/git/status?base=<name>
POST /api/git/sync?base=<name>
POST /api/git/resume?base=<name>
```

Include complete JSON for first-four `error`, fifth-failure `paused`, conflict-abort `paused`, `202` resume, `409 git_not_paused`, and `409 git_paused`. State that `consecutive_failures`, `last_attempt`, pause, and safe error survive restart; validation/conflict/abort/shutdown do not increment; resume/config change resets to zero; disable/forget never remove `.git` or user files.

Use these representative payloads verbatim apart from documented base/path/OID/time values. First-four operational failure:

```json
{
  "statuses": [{
    "base": "work",
    "repository_path": "/home/user/notes/work",
    "state": "error",
    "operation_id": "11111111111111111111111111111111",
    "stage": "completed",
    "ahead": 1,
    "behind": 0,
    "consecutive_failures": 4,
    "last_attempt": "2026-09-01T12:00:00Z",
    "last_success": "2026-09-01T11:00:00Z",
    "changed_paths": [],
    "remote_oid": "0123456789abcdef0123456789abcdef01234567",
    "error": {"code": "remote_unreachable", "message": "Git remote is unreachable"}
  }]
}
```

Fifth operational failure differs in the persisted breaker fields and current operation only:

```json
{
  "statuses": [{
    "base": "work",
    "repository_path": "/home/user/notes/work",
    "state": "paused",
    "operation_id": "22222222222222222222222222222222",
    "stage": "completed",
    "ahead": 1,
    "behind": 0,
    "consecutive_failures": 5,
    "last_attempt": "2026-09-01T12:15:00Z",
    "last_success": "2026-09-01T11:00:00Z",
    "changed_paths": [],
    "remote_oid": "0123456789abcdef0123456789abcdef01234567",
    "error": {"code": "remote_unreachable", "message": "Git remote is unreachable"}
  }]
}
```

Successful Plan 4 abort preserves the existing count while explaining the explicit pause:

```json
{
  "statuses": [{
    "base": "work",
    "repository_path": "/home/user/notes/work",
    "state": "paused",
    "operation_id": "33333333333333333333333333333333",
    "stage": "completed",
    "ahead": 1,
    "behind": 1,
    "consecutive_failures": 2,
    "last_attempt": "2026-09-01T12:20:00Z",
    "last_success": "2026-09-01T11:00:00Z",
    "changed_paths": ["notes/idea.md"],
    "remote_oid": "0123456789abcdef0123456789abcdef01234567",
    "error": {"code": "git_paused", "message": "Git synchronization is paused"}
  }]
}
```

Resume acceptance and the two `409` responses are:

```json
{"operation_id":"44444444444444444444444444444444","status":"queued","deduplicated":false}
```

```json
{"code":"git_not_paused","message":"Git synchronization is not paused"}
```

```json
{"code":"git_paused","message":"Git synchronization is paused"}
```

- [ ] **Step 6: Commit lifecycle coverage and docs**

```bash
git add docs/api.md internal/service/git_manager_test.go internal/handlers/git_handler_test.go cmd/api/git_runtime_test.go web/src/App.test.js
git commit -m "test: cover git autosync resilience lifecycle"
```

- [ ] **Step 7: Run all backend tests**

```bash
go test ./... -count=1
```

Expected: PASS; real-Git tests may SKIP only when Git is absent or older than 2.28.

- [ ] **Step 8: Run the full race suite**

```bash
go test -race ./... -count=1
```

Expected: PASS with no `DATA RACE`, deadlock, or timeout.

- [ ] **Step 9: Run static analysis and cross-platform compilation**

```bash
go vet ./...
rm -rf /tmp/igonotes-plan7-cross && mkdir -p /tmp/igonotes-plan7-cross/windows /tmp/igonotes-plan7-cross/darwin
for package in model git repository service; do CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c "./internal/$package" -o "/tmp/igonotes-plan7-cross/windows/$package.test.exe"; done
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c ./internal/handlers -o /tmp/igonotes-plan7-cross/windows/handlers.test.exe
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c ./cmd/api -o /tmp/igonotes-plan7-cross/windows/api.test.exe
for package in model git repository service; do CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go test -c "./internal/$package" -o "/tmp/igonotes-plan7-cross/darwin/$package.test"; done
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go test -c ./internal/handlers -o /tmp/igonotes-plan7-cross/darwin/handlers.test
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go test -c ./cmd/api -o /tmp/igonotes-plan7-cross/darwin/api.test
```

Expected: all commands exit `0` without diagnostics; twelve foreign test binaries exist under `/tmp/igonotes-plan7-cross` and are not executed on the Linux host.

- [ ] **Step 10: Run all frontend tests and production build**

```bash
node -e "if (Number(process.versions.node.split('.')[0]) !== 24) process.exit(1)" &&
npm --prefix web ci &&
npm --prefix web test -- --run &&
npm --prefix web run build
```

Expected: the Node 24 gate, exact lockfile install, Vitest, and Vite exit `0`.

- [ ] **Step 11: Audit architecture and forbidden behavior**

```bash
! git grep -n -E 'time\.NewTicker|math/rand|cron|force-with-lease|push --force|reset --hard|rebase|final.*commit' -- internal/service/git_scheduler.go internal/service/git_manager.go cmd/api/main.go
! git grep -n -E 'CREATE TABLE|schema_migrations|sql\.Open' -- internal/repository/git_status_transition.go internal/service/git_scheduler.go
! git grep -n -E 'exec\.Command|exec\.CommandContext' -- internal/service/git_scheduler.go internal/service/git_manager.go
```

Expected: no matches. The scheduler uses one resettable timer, Plan 1 remains the sole process runner/migration owner, and shutdown creates no final Git operation.

- [ ] **Step 12: Inspect the final diff and commit sequence**

```bash
git diff --check
git status --short
git log --oneline -9
```

Expected: `git diff --check` exits `0`; only files in this plan changed. Expected newest commits:

```text
test: cover git autosync resilience lifecycle
feat: show persistent git pause recovery
feat: wire git autosync lifecycle
feat: expose git autosync resume api
feat: reconcile git autosync settings
feat: add git autosync circuit breaker
feat: schedule deterministic git autosync
feat: persist git failure transitions atomically
feat: define git autosync resilience contracts
```

## Completion Criteria

- [ ] Every configured `auto_sync:true` base is considered, including inactive bases.
- [ ] Intervals are exactly `5`, `15`, `30`, `60` minutes and are anchored to persisted last attempt.
- [ ] Overdue startup work is deterministically staggered by five seconds in config order.
- [ ] All scheduled/manual/initialize/conflict operations still execute through one Plan 3 FIFO worker.
- [ ] Duplicate manual/scheduled requests share one operation ID by canonical path.
- [ ] Failure count updates are one SQLite statement and survive process restart.
- [ ] Success resets; operational sync failures increment; fifth failure persists `paused`.
- [ ] Validation/config drift, conflict, user abort, and shutdown cancellation do not increment.
- [ ] Paused bases receive no scheduled jobs until explicit resume or successful Git config change.
- [ ] Resume resets, durably queues one sync, compensates queue persistence failure, and returns `202`.
- [ ] Plan 4 conflict abort remains paused and becomes resumable without changing conflict semantics.
- [ ] Rename/path/disable/forget/replace actions reconcile both persisted status and in-memory timers safely.
- [ ] Shutdown cancellation reaches network Git before HTTP drain completes and no next/final job starts.
- [ ] The UI alert survives restart/poll failures and provides resume/settings actions without discarding dirty editor state.
- [ ] Plans 5 and 6 land before frontend integration; Task 8 reuses their sole `gitPoller`, asynchronous status application, and zero-argument refresh seam without fallback polling code.
- [ ] Manual sync and resume share one App-owned `gitBusyBase`/`gitActionErrors` boundary, and the combined busy state reaches every footer/settings/alert Git action.
- [ ] Every frontend run is gated on Node.js 24 and a successful lockfile-strict `npm --prefix web ci`; strict Git status/operation response validation remains enabled.
- [ ] No new Git runner, manager, status model/table, operation journal, migration framework, or database exists.
- [ ] Backend tests, race suite, frontend tests, production build, vet, Windows compile, and macOS compile pass.
