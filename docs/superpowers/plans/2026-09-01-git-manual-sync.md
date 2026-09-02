# Git Manual Synchronization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add crash-recoverable initial Git connection and manual synchronization for configured note bases, with exact-OID safety, a persistent operation journal, a globally sequential deduplicating manager queue, conflict detection/mutation blocking, and startup recovery.

**Architecture:** The landed Plan 1 `internal/git.Runner`/`Client`, service validation/probe/config ownership, model status DTOs, and status repository remain the only Git process/config/status primitives. The landed Plan 2 `BaseOperationCoordinator` and `NoteService.MutateActiveFilesystem` remain the only way to serialize Git work with base changes and active-base note mutations; Plan 3 adds connect/sync/recovery methods to an `internal/git.Service`, a journal repository, and `internal/service.GitManager` orchestration. The manager owns the coordinator and the worktree transaction adapter, and the coordinator remains locked through each operation. Add/commit/switch/merge for the active base run through `MutateActiveFilesystem`, which always rebuilds SQLite before releasing `NoteService.baseMu`; the same local commands for an inactive base run directly while the coordinator prevents switching into that base. The adapter detects Plan 3 `ConflictError` before either mutation callback returns and publishes the canonical path through lock-free `SetConflict`, so the active conflict gate closes before `baseMu` unlocks and the inactive gate closes before direct return. Durable terminal conflict publication later idempotently republishes the gate. Fetch and push never hold `NoteService.baseMu`.

**Tech Stack:** Go 1.26, system Git 2.28+, `context`, `net/http`, `database/sql`, `modernc.org/sqlite`, existing `internal/git` and `internal/service` contracts from Plans 1-2, `testing`, `httptest`, temporary bare repositories, and the Go race detector.

---

## Scope and Dependencies

This is Plan 3 of the approved Git synchronization design. Execute it only after Plans 1 and 2 have landed in the implementation worktree.

Plan 1 owns and has already established:

- `internal/git/runner.go`: exact `Runner.Run(context.Context, Command) (Result, error)` contract and sole `exec.CommandContext` boundary, with local/network scopes, bounded output, noninteractive environment, redaction, and `SafeError` classification.
- `internal/git/porcelain.go`: `Client`/`Porcelain`, exact repository-root inspection, remote branch discovery, Git version/identity checks, and side-effect-free local/remote inspection.
- `internal/service/git_config.go`: `GitConfigValidator`, URL/branch/interval/template validation, and `RenderGitCommitMessage`.
- `internal/service/git_probe_service.go`: `GitProbeService.Probe(...)(model.GitProbeResponse, error)` over the exact Plan 1 `SettingsSnapshot` and porcelain contracts.
- `internal/model/config.go` and `internal/model/api.go`: `model.Base`, `GitConfigRequest/Response`, `GitProbeResponse`, `GitStatus`, public states, changed paths, ahead/behind values, and API-safe errors.
- `internal/repository/git_status_repo.go`: concrete `GitStatusRepository` persistence for `model.GitStatus` keyed by canonical repository path.
- `internal/repository/db.go`: ordered, transactional metadata-schema migrations used by status persistence.
- `internal/service/settings_service.go`: existing `ConfigureGit`, `DisableGit`, Git status compensation, and `NewSettingsServiceWithGit` configuration ownership.
- `internal/handlers/git_handler.go` and `internal/handlers/git_routes.go`: existing probe/config/status handlers and separately registered guarded Git routes.

Plan 2 owns and has already established:

- `internal/service/base_operation_coordinator.go`: one process-wide `BaseOperationCoordinator` with `Lock`, `Unlock`, lock-free `CheckMutation`, and copy-on-write `SetConflict`.
- `internal/service/note_filesystem_transaction.go`: `NoteService.MutateActiveFilesystem(expectedPath, func(canonicalPath string) error)`, which validates the pinned active path and always reindexes after callback invocation, including callback errors.
- `internal/service/note_service.go`: revision-safe note writes and coordinator-backed conflict mutation checks.
- `internal/service/settings_service.go`: coordinator-aware base configuration ownership. The corrected integrated Plan 2 constructors are `NewNoteService(repo, basePath, coordinator)`, `NewSettingsService(store, notes, coordinator, activeBaseName, logger)`, and `NewSettingsServiceWithGit(store, notes, coordinator, activeBaseName, logger, validator, statuses)`; `NewSettingsService` remains the nil-Git-dependency wrapper.
- `cmd/api/main.go`: exactly one shared `BaseOperationCoordinator` passed to both `NoteService` and `SettingsService`.

Plan 3 must consume those concrete landed types directly. Do not add alternative runner, porcelain, validator, status repository, coordinator, worktree, mutation-policy, or revision interfaces in new files. Plan 3 adds one necessary immutable `internal/git.ConfiguredBase` value because Plan 1 deliberately exposes only config DTOs and `SettingsSnapshot.GetConfig`; it is built by copying a landed `model.Base`, canonicalizing its path, and hashing all Git fields. In particular, do not create `GitCoordinator`, `GitWorktree`, or `GitMutationPolicy`: manager code locks the shared `BaseOperationCoordinator`, calls a snapshot function bound to `SettingsService.GitSnapshot` while preserving the coordinator-before-settings lock order, owns the transaction adapter that invokes `NoteService.MutateActiveFilesystem` only when that snapshot identifies the active base, and publishes conflict gates through `SetConflict` before the transaction callback returns and again during terminal conflict publication.

This plan includes:

- Git operation journal migration and repository.
- Idempotent initialize/connect for all four local-repository/remote-branch cases.
- Local backup refs before branch switching or history merging can strand a snapshot.
- The one allowed empty initial commit exception.
- Manual fetch/add/conditional commit/merge/reindex/exact-OID push.
- One and only one retry after a non-fast-forward push rejection.
- Remote branch deletion and rewritten-history safeguards.
- A single global sequential queue with per-repository deduplication.
- Configure-to-initialize queueing, status, and manual-sync REST routes.
- Local recovery before the HTTP server starts accepting requests.
- Merge-conflict detection, persistence, and note mutation blocking.

This plan excludes:

- Autosync timers, scheduling, jitter, and interval execution.
- Circuit-breaker counters, `paused`, and resume behavior.
- Conflict stage/content endpoints, resolution, complete, abort, or automatic side selection.
- Force-push, force-with-lease, rebase, reset, prune, remote branch deletion, or cleanup of Git data created before an error.
- Frontend work.

## File Structure

- Modify `internal/model/api.go`: add the accepted-operation response embedded by configure and returned by manual sync.
- Modify `internal/model/git_test.go`: JSON coverage for accepted operation responses.
- Modify `internal/git/errors.go`: add Plan 3 safe error codes and non-fast-forward classification.
- Modify `internal/git/runner_test.go`: preserve Plan 1 classification precedence while testing push rejection.
- Create `internal/git/operation.go`: internal configured snapshot, operation kind/state/stage/checkpoint/result types using Plan 1 `SafeError` rather than duplicating public status.
- Create `internal/git/service.go`: Plan 3 operation entry points over the landed `Runner` and a callback adapted to Plan 2 `MutateActiveFilesystem`.
- Create `internal/git/service_test.go`: exact landed `Command` forwarding and secret propagation tests.
- Create `internal/git/connect.go`: idempotent initial-connect state machine and backup refs.
- Create `internal/git/connect_test.go`: command/state unit tests for connect.
- Create `internal/git/connect_integration_test.go`: real-repository tests for the four connect cases.
- Create `internal/git/sync.go`: ordinary exact-OID sync and single non-fast-forward retry.
- Create `internal/git/sync_test.go`: command sequencing, exact-OID, safeguard, and retry unit tests.
- Create `internal/git/sync_integration_test.go`: real bare/working repository sync tests.
- Create `internal/git/recovery.go`: local-only unfinished-operation inspection.
- Create `internal/git/recovery_test.go`: restart/conflict/lock recovery tests.
- Modify `internal/repository/db.go`: append the next Plan 1 metadata migration for the operation journal.
- Create `internal/repository/git_operation_repo.go`: journal lifecycle and active-operation lookup.
- Create `internal/repository/git_operation_repo_test.go`: migration, transition, dedupe, and restart tests.
- Create `internal/service/git_manager.go`: single worker queue, dedupe, status/journal updates, Git service coordination, and recovery.
- Create `internal/service/git_manager_test.go`: manager state-machine and concurrency tests.
- Modify `internal/service/settings_service.go`: persist Git configuration through the existing owner and publish immutable snapshots.
- Modify `internal/service/settings_service_test.go`: canonical persistence and snapshot freshness tests.
- Modify `internal/handlers/git_handler.go`: extend the landed probe/config/status handler with configure-to-initialize and manual sync.
- Modify `internal/handlers/git_handler_test.go`: update landed configure expectations and add `202`, dedupe, safe error, and sync tests.
- Modify `internal/handlers/git_routes.go`: add manual sync to the landed guarded Git route registration.
- Modify `internal/handlers/git_routes_test.go`: extend the landed route/method/setup/local-origin matrix.
- Modify `internal/handlers/errors.go`: map Plan 1 Git errors without leaking stderr or URLs.
- Modify `internal/handlers/errors_test.go`: Git status/code/message mappings.
- Modify `cmd/api/main.go`: construct the Git service/manager and recover locally before serving.
- Create `cmd/api/git_runtime_test.go`: startup ordering and shutdown tests.

## Safe Git Command Rules

All commands go through the landed Plan 1 runner as argument slices. The table is normative.

| Purpose | Required argument sequence |
|---|---|
| Initialize repository | `git init --initial-branch <branch>` |
| Verify repository root | `git rev-parse --show-toplevel` |
| Read commit | `git rev-parse --verify <revision>^{commit}` |
| Read current branch | `git symbolic-ref --quiet --short HEAD` |
| Read origin | `git remote get-url --all origin` |
| Add origin | `git remote add origin <url>` |
| Confirmed origin rewrite | `git remote set-url origin <url>` |
| Inspect all remote refs | `git ls-remote --symref origin` |
| Require selected branch | `git ls-remote --exit-code --heads origin refs/heads/<branch>` |
| Fetch selected branch | `git fetch --no-tags --show-forced-updates origin +refs/heads/<branch>:refs/igonotes/fetch/<operation-id>` |
| Validate remote ancestry | `git merge-base --is-ancestor <trusted-oid> <fetched-oid>` |
| Record trusted remote ref | `git update-ref refs/igonotes/remotes/<branch> <new-oid> <old-oid>` |
| Stage complete tree | `git add --all -- .` |
| Detect staged changes | `git diff --cached --quiet --exit-code` |
| List staged paths | `git diff --cached --name-only -z` |
| Changed paths between commits | `git diff --name-only -z <pre-oid> <final-oid> --` |
| Changed paths from unborn state | `git diff-tree --root --no-commit-id --name-only -r -z <final-oid> --` |
| Local sync commit | `git commit -m <rendered-template>` |
| Initial snapshot commit | `git commit -m "IGoNotes: initial snapshot"` |
| Empty initial commit | `git commit --allow-empty -m "IGoNotes: initial snapshot"` |
| Create backup ref | `git update-ref --create-reflog -m "IGoNotes initial-connect backup" <backup-ref> <snapshot-oid> <zero-oid>` |
| Switch existing branch | `git switch <branch>` |
| Create branch at exact OID | `git switch -c <branch> <oid>` |
| Populate empty unborn branch | `git checkout <fetched-oid> -- .`, then `git update-ref refs/heads/<branch> <fetched-oid> <zero-oid>` |
| Merge local snapshot | `git merge --no-edit -m "IGoNotes: merge local snapshot" <snapshot-oid>` |
| Merge remote | `git merge --no-edit -m "IGoNotes: merge origin/<branch>" <fetched-oid>` |
| Initial unrelated merge | Add `--allow-unrelated-histories` only for the confirmed initial-connect case |
| Inspect unmerged paths | `git ls-files -u -z` |
| Compute ahead/behind | `git rev-list --left-right --count HEAD...<captured-remote-oid>` |
| Capture push commit | `git rev-parse --verify HEAD^{commit}` |
| Push exact commit | `git push --no-verify --porcelain origin <captured-oid>:refs/heads/<branch>` |

Never invoke `push --force`, `--force-with-lease`, `reset`, `rebase`, `clean`, `remote prune`, `push --delete`, an empty-source push refspec, or `merge --abort`. A leading `+` is allowed only on the private fetch destination `refs/igonotes/fetch/<operation-id>`; it is forbidden on push.

Every push includes `--no-verify`, intentionally disabling pre-push hooks because push runs after the active-base `NoteService.baseMu` worktree transaction has unlocked. Commit and merge hooks remain enabled and governed by their local mutation phase inside `MutateActiveFilesystem`; for inactive bases, the same commands remain inside the coordinator-locked local mutation phase. Do not add `--no-verify` to commit or merge.

The private fetch ref intentionally prevents a forced remote update from overwriting the last trusted managed ref before ancestry validation. Task 4 generates operation IDs as exactly 32 lowercase hex characters from 128 bits of `crypto/rand`; Git service code validates that grammar again before interpolating an ID into a ref. On successful remote ancestry validation, update `refs/igonotes/remotes/<branch>` with `update-ref` and an expected old OID. Plan 3 never deletes private fetch, trusted, or backup refs; initial-connect and recovery are observationally idempotent and preserve Git data.

## Parallel Dispatch Waves

### Wave 0: Contract and Journal Freeze

Task 1 is serial. It adds Plan 3 operation types/DTOs and extends the landed migration system. Merge it before parallel Git service work.

### Wave 1: Independent Git Service Work

After Task 1, Tasks 2 and 3 may run in parallel.

| Worker | Exclusive files |
|---|---|
| Connect service | `internal/git/connect.go`, `internal/git/connect_test.go`, `internal/git/connect_integration_test.go` |
| Sync/recovery service | `internal/git/sync.go`, `internal/git/sync_test.go`, `internal/git/sync_integration_test.go`, `internal/git/recovery.go`, `internal/git/recovery_test.go` |

Both workers may read but must not modify `internal/git/runner.go`, `internal/git/porcelain.go`, `internal/service/git_config.go`, `internal/model/api.go`, `internal/git/operation.go`, or `internal/git/service.go`. Task 1 freezes those contracts.

### Wave 2: Manager Integration

Task 4 is serial after Tasks 2 and 3 merge. The manager is the only layer allowed to combine mutating Git service calls, coordinator ownership, journal updates, public status, and mutation blocking.

### Wave 3: Settings and HTTP

After Task 4 freezes `GitManager.QueueInitialize` and `QueueSync`, Tasks 5 and 6 may run in parallel. Task 6 uses consumer-local interfaces matching those frozen methods and Task 5's frozen `ConfigureGitForInitialize`/`GitSnapshot` signatures, so handler tests use fakes without introducing replacement core contracts. Merge Task 5 before compiling Task 6 against production services.

| Worker | Exclusive files |
|---|---|
| Settings integration | `internal/service/settings_service.go`, `internal/service/settings_service_test.go` |
| HTTP integration | `internal/handlers/git_handler.go`, `internal/handlers/git_handler_test.go`, `internal/handlers/git_routes.go`, `internal/handlers/git_routes_test.go`, `internal/handlers/errors.go`, `internal/handlers/errors_test.go` |

### Wave 4: Startup and Cross-Layer Verification

Tasks 7 and 8 are serial. Startup wiring follows settings and handlers; cross-layer verification follows startup.

### Task 1: Add Plan 3 Operation Contracts and the Journal

**Files:**
- Modify: `internal/model/api.go`
- Modify: `internal/model/git_test.go`
- Modify: `internal/git/errors.go`
- Modify: `internal/git/runner_test.go`
- Create: `internal/git/operation.go`
- Create: `internal/git/service.go`
- Create: `internal/git/service_test.go`
- Modify: `internal/repository/db.go`
- Create: `internal/repository/git_operation_repo.go`
- Create: `internal/repository/git_operation_repo_test.go`

- [ ] **Step 1: Write RED journal migration and lifecycle tests**

Add tests with these exact names:

```go
func TestGitOperationRepositoryCreateCheckpointFinish(t *testing.T)
func TestGitOperationRepositoryPreservesUTCNanosecondCreationTime(t *testing.T)
func TestGitOperationRepositoryRejectsSecondActivePath(t *testing.T)
func TestGitOperationRepositoryReturnsActiveOperation(t *testing.T)
func TestGitOperationRepositoryReturnsLatestOperationByPath(t *testing.T)
func TestGitOperationRepositoryListsUnfinishedOperations(t *testing.T)
func TestGitOperationRepositoryPersistsConflictPaths(t *testing.T)
func TestGitOperationRepositoryRejectsTerminalTransition(t *testing.T)
func TestGitOperationResponseJSON(t *testing.T)
func TestGitConfigResponseIncludesAcceptedOperation(t *testing.T)
func TestClassifyFailureNonFastForward(t *testing.T)
func TestServiceRunForwardsLandedCommand(t *testing.T)
```

The lifecycle test must create a queued operation with a nonzero nanosecond UTC `CreatedAt`, checkpoint it as running, finish it as conflict, reopen the database, and compare every persisted field. The dedicated timestamp test proves `CreateQueued`, `ActiveByPath`, `LatestByPath`, and `ListUnfinished` preserve the exact UTC instant without truncating to seconds or replacing it on retry. Extend the landed model contract with:

```go
type GitOperationResponse struct {
	OperationID  string `json:"operation_id"`
	Status       string `json:"status"`
	Deduplicated bool   `json:"deduplicated"`
}
```

Add this field to the landed `GitConfigResponse`; retain its existing `Base` and `Status` fields:

```go
Operation *GitOperationResponse `json:"operation,omitempty"`
```

- [ ] **Step 2: Run the RED repository tests**

Run:

```bash
go test ./internal/model ./internal/git ./internal/repository -run 'Test(GitOperationResponse|GitConfigResponseIncludesAcceptedOperation|ClassifyFailureNonFastForward|ServiceRunForwardsLandedCommand|GitOperationRepository)' -v
```

Expected: FAIL to compile because the accepted-operation DTO, `NewGitOperationRepository`, and operation journal types do not exist.

- [ ] **Step 3: Add internal operation types without duplicating Plan 1 public status**

Create `internal/git/operation.go`; use the landed `SafeError` from `internal/git/errors.go`. Keep `model.GitStatus` as the sole public status representation and convert `Stage` to string only when publishing it.

```go
type OperationKind string
type OperationState string
type Stage string

const (
	OperationInitialize OperationKind = "initialize"
	OperationSync       OperationKind = "sync"

	OperationQueued    OperationState = "queued"
	OperationRunning   OperationState = "running"
	OperationSucceeded OperationState = "succeeded"
	OperationFailed    OperationState = "failed"
	OperationConflict  OperationState = "conflict"

	StageQueued       Stage = "queued"
	StageProbing      Stage = "probing"
	StageFetching     Stage = "fetching"
	StageSnapshotting Stage = "snapshotting"
	StageBackingUp    Stage = "backing_up"
	StageSwitching    Stage = "switching"
	StageMerging      Stage = "merging"
	StageReindexing   Stage = "reindexing"
	StagePushing      Stage = "pushing"
	StageCompleted    Stage = "completed"
)

type Operation struct {
	ID                string
	BaseName          string
	RepoPath          string
	ConfigFingerprint string
	RemoteFingerprint string
	Kind              OperationKind
	State             OperationState
	Stage             Stage
	Branch            string
	BackupRef         string
	LocalOID          string
	CandidateOID      string
	RemoteOID         string
	PushOID           string
	ChangedPaths      []string
	ConflictPaths     []string
	Error             *SafeError
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Checkpoint struct {
	Stage         Stage
	BackupRef     string
	LocalOID      string
	CandidateOID  string
	RemoteOID     string
	PushOID       string
	ChangedPaths  []string
	ConflictPaths []string
}
```

Also add the immutable value required by the approved manager architecture:

```go
type ConfiguredBase struct {
	Name              string
	Path              string
	URL               string
	Branch            string
	AutoSync          bool
	IntervalMinutes   int
	CommitTemplate    string
	Fingerprint       string
	RemoteFingerprint string
}
```

`ConfiguredBase` is not an API/config model. `SettingsService.GitSnapshot` constructs it from a copied landed `model.Base` and canonical path. `Fingerprint` is SHA-256 over all length-prefixed Git fields; `RemoteFingerprint` is a separate SHA-256 over canonical path, URL, and branch so template/autosync changes retain rewritten-history protection while an explicit URL/branch/path change re-baselines internal trust. Never concatenate ambiguous raw strings or include credentials in logs/status. Do not add a second public Git status model or duplicate Plan 1 error codes.

Append only the missing Plan 3 codes to the landed `ErrorCode` block in `internal/git/errors.go`: `origin_mismatch`, `branch_deleted`, `remote_history_rewritten`, `push_rejected`, `git_conflict`, `needs_reconnect`, `git_confirmation_required`, `operation_interrupted`, and `backup_mismatch`. Add table tests to landed `runner_test.go` proving non-fast-forward diagnostics classify as `push_rejected` while authentication/transport classifications retain precedence. Continue using public `SafeError` fields for safe constructed errors; do not expose its diagnostic/cause fields or duplicate handler error models.

- [ ] **Step 4: Freeze the Plan 3 service entry points over landed dependencies**

`internal/git/service.go` adds only Plan 3 operation orchestration over the exact landed `Runner` interface. Its worktree argument is a callback supplied by `GitManager`, so `internal/git` does not import `internal/service` and create a package cycle.

```go
type WorktreeTransaction func(
	context.Context,
	func(canonicalPath string) error,
) error

type Progress func(context.Context, Checkpoint) error

type InitializeRequest struct {
	Snapshot      ConfiguredBase
	Confirmations model.GitConfirmations
}

type SyncRequest struct {
	Snapshot ConfiguredBase
}

type InitializeOptions struct {
	Operation      Operation
	Snapshot       ConfiguredBase
	Probe          model.GitProbeResponse
	Confirmations  model.GitConfirmations
	LastRemoteOID  string
}

type SyncOptions struct {
	Operation     Operation
	Snapshot      ConfiguredBase
	LastRemoteOID string
}

type OperationResult struct {
	HeadOID       string
	RemoteOID     string
	PushOID       string
	BackupRef     string
	ChangedPaths []string
	ConflictPaths []string
	Ahead         int
	Behind        int
}

type RecoveryResult struct {
	HeadOID       string
	MergeHeadOID  string
	RemoteOID     string
	ConflictPaths []string
	Blocking      bool
}

type RecoveryOptions struct {
	Snapshot  ConfiguredBase
	Operation *Operation
}

type Service struct {
	runner    Runner
	porcelain Porcelain
	now       func() time.Time
}

func NewService(runner Runner, porcelain Porcelain) *Service
func (s *Service) Initialize(context.Context, InitializeOptions, WorktreeTransaction, Progress) (OperationResult, error)
func (s *Service) Sync(context.Context, SyncOptions, WorktreeTransaction, Progress) (OperationResult, error)
func (s *Service) RecoverLocal(context.Context, RecoveryOptions) (RecoveryResult, error)
```

`InitializeRequest` and `SyncRequest` are immutable queue inputs. `GitManager` derives the operation ID and last trusted remote OID and re-runs landed `GitProbeService.Probe` under coordinator ownership before constructing service-only `InitializeOptions`/`SyncOptions`; HTTP and settings code must not synthesize those derived values. `Service` uses Plan 1 `Runner` directly, and `InitializeOptions.Probe` uses the landed `model.GitProbeResponse`. Freeze these contracts before Wave 1 dispatch.

Task 1 implements the types, `Service` fields, `NewService`, and a shared private runner helper that takes explicit `OperationScope` and `ReadOnly`, constructs the exact landed `Command` with cloned argv, and places the URL in `Secrets`. Thin `runLocal`/`runNetwork` callers must still pass the read-only bit; inspection commands set it true, while init/fetch/add/commit/ref/switch/checkout/merge/push set it false. Reuse landed `Porcelain` methods for every inspection they already cover; use raw `Runner` only for Plan 3 commands or extra exact-OID/local-state checks absent from Plan 1. Do not add panic/error stubs: Task 2 adds `Initialize`, and Task 3 adds `Sync`/`RecoverLocal`, so their RED tests fail at compile time for the intended reason.

- [ ] **Step 5: Append the journal migration to Plan 1's migration list**

Use the next schema version already established in `internal/repository/db.go`. The migration body is:

```sql
CREATE TABLE git_operations (
    operation_id TEXT PRIMARY KEY,
    base_name TEXT NOT NULL,
    repo_path TEXT NOT NULL,
    config_fingerprint TEXT NOT NULL,
    remote_fingerprint TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('initialize', 'sync')),
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'conflict')),
    stage TEXT NOT NULL,
    branch TEXT NOT NULL,
    backup_ref TEXT NOT NULL DEFAULT '',
    local_oid TEXT NOT NULL DEFAULT '',
    candidate_oid TEXT NOT NULL DEFAULT '',
    remote_oid TEXT NOT NULL DEFAULT '',
    push_oid TEXT NOT NULL DEFAULT '',
    changed_paths_json TEXT NOT NULL DEFAULT '[]',
    conflict_paths_json TEXT NOT NULL DEFAULT '[]',
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    error_field TEXT NOT NULL DEFAULT '',
    error_exit_code INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX git_operations_one_active_path
ON git_operations(repo_path)
WHERE state IN ('queued', 'running');

CREATE INDEX git_operations_unfinished
ON git_operations(state, updated_at)
WHERE state IN ('queued', 'running');
```

Do not create a second migration framework or a second database file.

- [ ] **Step 6: Implement journal transitions**

Expose:

```go
type GitOperationRepository struct {
	db *sql.DB
}

func NewGitOperationRepository(db *sql.DB) *GitOperationRepository
func (r *GitOperationRepository) CreateQueued(context.Context, git.Operation) error
func (r *GitOperationRepository) Checkpoint(context.Context, string, git.Checkpoint) error
func (r *GitOperationRepository) Finish(context.Context, git.Operation) error
func (r *GitOperationRepository) ActiveByPath(context.Context, string) (git.Operation, bool, error)
func (r *GitOperationRepository) LatestByPath(context.Context, string) (git.Operation, bool, error)
func (r *GitOperationRepository) ListUnfinished(context.Context) ([]git.Operation, error)
```

Define repository-local `ErrGitOperationTransition` for a zero-row checkpoint/finish; `ActiveByPath`/`LatestByPath` follow landed `GitStatusRepository.Get` style and return `found=false` for no row. Latest ordering is `updated_at DESC, rowid DESC`. `Checkpoint` and `Finish` must include `WHERE state IN ('queued','running')` and check `RowsAffected`. Encode changed/conflict paths separately as JSON, sort/deduplicate both before persistence, and persist only public `SafeError` fields (`Code`, fixed `Message`, `Field`, numeric `ExitCode`), never private diagnostic/cause, command output, or a remote URL.

- [ ] **Step 7: Run journal tests GREEN**

Run:

```bash
gofmt -w internal/model/api.go internal/model/git_test.go internal/git/errors.go internal/git/runner_test.go internal/git/operation.go internal/git/service.go internal/git/service_test.go internal/repository/db.go internal/repository/git_operation_repo.go internal/repository/git_operation_repo_test.go
go test ./internal/model ./internal/git ./internal/repository -count=1
```

Expected: PASS; reopening the database preserves checkpoints and the partial unique index rejects a second active operation for the same canonical path.

- [ ] **Step 8: Commit the journal and service contracts**

```bash
git add internal/model/api.go internal/model/git_test.go internal/git/errors.go internal/git/runner_test.go internal/git/operation.go internal/git/service.go internal/git/service_test.go internal/repository/db.go internal/repository/git_operation_repo.go internal/repository/git_operation_repo_test.go
git commit -m "feat: persist git operation journal"
```

### Task 2: Implement Idempotent Initial Connect for All Four Cases

**Files:**
- Create: `internal/git/connect.go`
- Create: `internal/git/connect_test.go`
- Create: `internal/git/connect_integration_test.go`

- [ ] **Step 1: Write RED integration tests for the four top-level cases**

```go
func TestInitializeNoRepositoryEmptyRemote(t *testing.T)
func TestInitializeNoRepositoryExistingRemoteBranch(t *testing.T)
func TestInitializeExistingRepositoryEmptyRemote(t *testing.T)
func TestInitializeExistingRepositoryExistingRemoteBranch(t *testing.T)
```

Required assertions:

| Case | Assertions |
|---|---|
| No repo, empty remote | initializes selected branch, commits all nonignored files, permits one empty initial commit only when needed, pushes exact HEAD |
| No repo, existing branch | empty local tree starts from fetched OID; nonempty tree commits local snapshot, creates backup, and merges unrelated histories only with confirmed probe consequence |
| Existing repo, empty remote | commits current changes, preserves detached/current snapshot with backup when switching, creates selected branch only because remote is empty, pushes exact selected HEAD |
| Existing repo, existing branch | commits current changes, backs up snapshot, switches without force, merges snapshot and fetched exact OID, preserves every local and remote commit |

- [ ] **Step 2: Write RED idempotency and safety tests**

```go
func TestInitializeRetryDoesNotCreateSecondEmptyCommit(t *testing.T)
func TestInitializeRetryReusesJournaledBackupRef(t *testing.T)
func TestInitializeBackupRefUsesPersistedCreationTimestamp(t *testing.T)
func TestInitializeBackupRefCollisionUsesDeterministicSuffix(t *testing.T)
func TestInitializePersistsBackupRefBeforeUpdateRef(t *testing.T)
func TestInitializeFailureKeepsRepositoryRemoteCommitAndBackup(t *testing.T)
func TestInitializeRejectsOriginMismatchWithoutConfirmation(t *testing.T)
func TestInitializeRewritesOriginOnlyWithConfirmation(t *testing.T)
func TestInitializePreservesRemoteTrustAcrossTemplateChange(t *testing.T)
func TestInitializeRebaselinesManagedRefAfterConfirmedRemoteChange(t *testing.T)
func TestInitializeRejectsMissingSelectedBranchOnNonemptyRemote(t *testing.T)
func TestInitializeRejectsParentRepositoryRoot(t *testing.T)
func TestInitializeRejectsUnfinishedGitOperation(t *testing.T)
func TestInitializePushTimeoutIsRecognizedOnRetry(t *testing.T)
func TestInitializeRechecksRemoteBeforePush(t *testing.T)
func TestInitializePushUsesNoVerify(t *testing.T)
func TestInitializeRetryRecoversBackupWhenCheckpointLags(t *testing.T)
func TestInitializeRetryRecoversTrustedRefWhenCheckpointLags(t *testing.T)
func TestInitializeReindexesBeforeWorktreeUnlockAndPush(t *testing.T)
```

For the empty-commit retry, record `git rev-list --count HEAD`, rerun the same operation ID, and require the count to remain unchanged.

- [ ] **Step 3: Run connect tests RED**

Run:

```bash
go test ./internal/git -run '^TestInitialize' -count=1 -v
```

Expected: FAIL because `Service.Initialize` has no connect implementation.

- [ ] **Step 4: Implement actual-state inspection before every mutation**

The connect state machine must inspect, in order:

```text
canonical base path
repository presence and exact toplevel
MERGE_HEAD / rebase / cherry-pick / revert markers
HEAD OID and symbolic branch
selected local branch existence
origin URL list
remote heads and selected branch OID
journaled backup ref and checkpoint OIDs
```

An existing parent repository is `repository_root_mismatch`. An existing unfinished operation is reported and left untouched. A retry continues from actual refs/commits even when the journal checkpoint lags a completed Git command.

Before each corresponding mutation, require the queued `model.GitConfirmations` bit for actual current consequences: `CreateRepository` before `git init`, `ReplaceOrigin` before `set-url`, `CreateBranch` before creating/pushing a branch on a truly empty remote, and `MergeHistories` before `--allow-unrelated-histories`. The fresh probe is advisory evidence, but the post-fetch ancestry check is authoritative for unrelated history. Missing confirmation returns `CodeConfirmationRequired` before that mutation; never infer consent from persisted config.

- [ ] **Step 5: Implement idempotent origin handling**

```go
func (s *Service) ensureOrigin(ctx context.Context, path, expectedURL string, replace bool) error {
	urls, exists, err := s.originURLs(ctx, path)
	if err != nil {
		return err
	}
	if !exists {
		return s.runLocal(ctx, path, "remote", "add", "origin", expectedURL)
	}
	if len(urls) == 1 && urls[0] == expectedURL {
		return nil
	}
	if !replace {
		return &SafeError{Code: CodeOriginMismatch, Message: "Configured origin does not match"}
	}
	return s.runLocal(ctx, path, "remote", "set-url", "origin", expectedURL)
}

type ConflictError struct {
	Paths []string
}
```

`ConflictError.Error` returns a fixed safe message and `Unwrap` returns a `SafeError{Code: CodeGitConflict}`. Its constructor sorts/deduplicates cloned paths. This is the only typed carrier from Git service to manager; it never contains file contents or command diagnostics.

Implement `runLocal`/`runNetwork` as private helpers that call landed `Runner.Run` with `Command{Dir, Args, Scope, Secrets}`; do not add a second runner interface. `originURLs` classifies missing-origin exit status without inspecting localized strings. Every command carrying the remote URL includes it in `Secrets`. Never place `expectedURL` in status, journal, or API errors.

- [ ] **Step 6: Implement snapshot and backup rules**

Run `add --all -- .`, then classify `diff --cached --quiet --exit-code`: exit `1` means commit, exit `0` means no staged changes, all other results are errors.

The empty commit is allowed only when:

```go
allowEmpty := remoteEmpty && !state.HasHEAD && stagedCount == 0
```

It is forbidden in ordinary sync and forbidden when any local HEAD already exists.

Create a deterministic backup before switching away from or merging an existing local snapshot. The base name comes from the durable operation creation instant, not the operation ID or the wall clock at backup time:

```go
const backupTimestampLayout = "20060102T150405.000000000Z"

func backupRefBase(createdAt time.Time) string {
	return "refs/igonotes/backups/" + createdAt.UTC().Format(backupTimestampLayout)
}

func backupRefCandidate(base string, collision int) string {
	if collision == 0 {
		return base
	}
	return base + "-" + strconv.Itoa(collision)
}
```

`GitManager` captures one `createdAt := m.now().UTC()` when it creates the operation, assigns that exact value to both `CreatedAt` and initial `UpdatedAt`, and durably inserts it as UTC RFC3339Nano without precision loss. Backup code uses only `Operation.CreatedAt`; it never derives a backup ref from `Operation.ID` and never calls `now` to rename a retry.

If `Operation.BackupRef` is already journaled, it is authoritative: reuse it when it resolves to the snapshot OID, retry its zero-OID creation when absent, and return `backup_mismatch` if a checkpoint known to follow successful creation resolves elsewhere. If no backup ref is journaled, inspect `backupRefBase(Operation.CreatedAt)` and then `-1`, `-2`, and so on in ascending order until the first absent candidate. Call `Progress` to durably checkpoint that exact candidate in `Operation.BackupRef` and require the callback to succeed before invoking `git update-ref --create-reflog ... <candidate> <snapshot-oid> <zero-oid>`. If an external writer wins the zero-OID race, resolve the candidate: matching `snapshot-oid` is idempotent success; a different OID advances to the next numeric suffix, checkpoints that exact replacement before attempting it, and never overwrites the colliding ref. After successful creation, checkpoint again before switching or merging. Thus retries reuse the persisted exact ref, while equal creation timestamps and real ref collisions have deterministic suffixes. A no-repo/empty-remote initialization needs no backup because no existing snapshot can be stranded.

- [ ] **Step 7: Implement the four-case decision table**

| Repository | Remote selected branch | Exact behavior |
|---|---|---|
| Absent | Empty remote | `init --initial-branch`; ensure origin; snapshot or permitted empty commit; exact-OID push |
| Absent | Exists | initialize; snapshot if committable files exist; fetch exact branch; when no local HEAD, populate the empty unborn branch with `checkout <oid> -- .` and create its ref with expected zero; otherwise backup the snapshot and merge fetched OID with confirmed unrelated histories; exact-OID push |
| Exists | Empty remote | commit current changes; backup current snapshot when selected branch differs/detached; switch existing selected branch or create it at snapshot OID; merge snapshot only when not already reachable; exact-OID push |
| Exists | Exists | commit current changes; backup current snapshot; fetch; switch existing selected branch or create it at fetched OID; merge snapshot when not reachable; merge fetched OID; exact-OID push |

“Empty remote” means the same thing as landed Plan 1 `RemoteInspection.Empty`: `ls-remote --symref origin` returned no refs at all. A tag-only remote is nonempty. If the selected branch is absent while any other remote ref exists, return `CodeBranchDeleted` before local mutation; never silently classify that repository as empty or create the selected branch. Use `merge-base --is-ancestor A B` before each merge. Exit `0` skips the merge, exit `1` permits it, and any other exit is an error. Never use `switch -C`, `checkout -B`, reset, or forced checkout. The unborn-branch checkout is allowed only after probe confirms there is no local HEAD and no committable local file; ignored files remain untouched, and a checkout collision fails without cleanup.

For an existing remote branch, checkpoint `CandidateOID` then establish `refs/igonotes/remotes/<branch>` before local merge. When `LastRemoteOID` is nonempty for the same remote fingerprint, require the managed ref to match it (or recover the narrow checkpoint/private-ref crash gap), validate ancestry to the new fetched OID, and CAS old to new. When `LastRemoteOID` is empty because this is first configuration or an explicitly changed path/URL/branch, read any existing managed-ref OID and CAS-rebaseline that internal metadata ref to the freshly probed/fetched candidate; do not use the stale managed OID for ancestry or merge decisions. Template/autosync-only changes preserve the remote fingerprint and therefore cannot bypass rewritten-history checks. The fresh probe plus required replace/create/merge confirmations authorize a true remote re-baseline, and no user commit/ref is deleted. All init/add/commit/branch-switch/checkout/merge operations run inside the supplied callback. `GitManager` owns the adapter: it wraps the service mutation callback, detects `ConflictError` with `errors.As`, and calls `SetConflict` with the callback's canonical path before returning to active-base `NoteService.MutateActiveFilesystem` or with the current canonical snapshot path before returning from an inactive-base mutation. Plan 2 then reindexes the active base after callback invocation, including callback errors, while the gate is already closed; the coordinator excludes switching into an inactive base. Capture the exact push OID inside the callback, then push after the callback returns while `BaseOperationCoordinator` remains locked.

Immediately before initial push, inspect remote refs again. For an originally empty remote, require it still has no refs before the confirmed branch creation. For an existing selected branch, require it still exists at the fetched OID. Any change returns a safe current-state error without push; initialize does not consume ordinary sync's one-retry budget, and a new explicit request re-probes.

- [ ] **Step 8: Persist durable checkpoints**

Call the Task 1 checkpoint callback with the next stage before each irreversible local mutation/network push, then immediately after remote configuration, fetch, local snapshot, backup, branch switch, merge, captured push OID, and successful push to persist resulting refs/OIDs. After a confirmed successful initial push, CAS-update `refs/igonotes/remotes/<branch>` from the fetched OID (or zero for a confirmed empty remote) to `PushOID` and publish `model.GitStatus.RemoteOID=PushOID`; an unknown push result leaves the prior trusted value until a later fetch proves acceptance. Define the complete Plan 3 `Stage` constants in `internal/git/operation.go` during Task 1 and publish their string values through landed `model.GitStatus.Stage`. If an after-checkpoint fails, stop; a retry inspects actual Git state rather than repeating blindly.

- [ ] **Step 9: Run connect tests GREEN**

Run:

```bash
gofmt -w internal/git/connect.go internal/git/connect_test.go internal/git/connect_integration_test.go
go test ./internal/git -run '^TestInitialize' -count=1 -v
```

Expected: PASS for all four cases, retries, origin confirmation, preserved Git data, and the single empty-commit exception.

- [ ] **Step 10: Commit initial connect**

```bash
git add internal/git/connect.go internal/git/connect_test.go internal/git/connect_integration_test.go
git commit -m "feat: add idempotent git connect"
```

### Task 3: Implement Exact-OID Sync, Safeguards, and Local Recovery

**Files:**
- Create: `internal/git/sync.go`
- Create: `internal/git/sync_test.go`
- Create: `internal/git/sync_integration_test.go`
- Create: `internal/git/recovery.go`
- Create: `internal/git/recovery_test.go`

- [ ] **Step 1: Write RED ordinary-sync tests**

```go
func TestSyncNoChangesCreatesNoCommit(t *testing.T)
func TestSyncCommitsAllNonignoredChanges(t *testing.T)
func TestSyncPropagatesAssetsOtherFilesAndDeletes(t *testing.T)
func TestSyncFastForwardsRemoteOnlyChange(t *testing.T)
func TestSyncCreatesMergeCommitForDivergence(t *testing.T)
func TestSyncReindexesBeforeWorktreeUnlock(t *testing.T)
func TestSyncPushesCapturedExactOID(t *testing.T)
func TestSyncReportsAheadBehindFromCapturedRemoteOID(t *testing.T)
func TestSyncLeavesEditsCreatedDuringPushForNextCycle(t *testing.T)
func TestSyncPushUsesNoVerify(t *testing.T)
```

- [ ] **Step 2: Write RED retry and remote-safety tests**

```go
func TestSyncRetriesOneNonFastForwardPush(t *testing.T)
func TestSyncStopsAfterSecondNonFastForwardPush(t *testing.T)
func TestSyncDetectsDeletedRemoteBranchBeforeWorktreeMutation(t *testing.T)
func TestSyncDetectsRemoteHistoryRewriteBeforeMerge(t *testing.T)
func TestSyncDoesNotAdvanceTrustedOIDOnRewrite(t *testing.T)
func TestSyncRejectsManagedRefAndStatusMismatch(t *testing.T)
func TestSyncRecognizesPreviouslyAcceptedTimedOutPush(t *testing.T)
func TestSyncRechecksRemoteOIDImmediatelyBeforePush(t *testing.T)
func TestSyncConflictReindexesAndSkipsPush(t *testing.T)
```

- [ ] **Step 3: Write RED recovery tests**

```go
func TestRecoverLocalDetectsMergeConflict(t *testing.T)
func TestRecoverLocalPreservesPartiallyResolvedIndex(t *testing.T)
func TestRecoverLocalDetectsRepositoryLockWithoutDeletingIt(t *testing.T)
func TestRecoverLocalRejectsOtherUnfinishedOperation(t *testing.T)
func TestRecoverLocalReconcilesTrustedRefOnlyOnThreeWayMatch(t *testing.T)
func TestRecoverLocalRecognizesLocallyRecordedSuccessfulPush(t *testing.T)
func TestRecoverLocalRunsNoNetworkCommand(t *testing.T)
func TestRecoverLocalReportsCleanRepository(t *testing.T)
```

- [ ] **Step 4: Run sync/recovery tests RED**

Run:

```bash
go test ./internal/git -run '^Test(Sync|RecoverLocal)' -count=1 -v
```

Expected: FAIL because sync and recovery methods do not exist.

- [ ] **Step 5: Implement selected-branch fetch and exact remote OID capture**

Before fetch, require `refs/heads/<branch>` with `ls-remote --exit-code --heads`. Exit `2` maps to `branch_deleted` before any worktree mutation.

Fetch into the operation-private ref:

```go
candidateRef := "refs/igonotes/fetch/" + operation.ID
refspec := "+refs/heads/" + snapshot.Branch + ":" + candidateRef
```

Resolve `candidateRef^{commit}` immediately. Every later merge receives that full captured OID, never `FETCH_HEAD`, `origin/<branch>`, or mutable `HEAD`.

- [ ] **Step 6: Implement rewritten-history protection**

Before network access, require the managed trusted ref to equal `LastRemoteOID` (both absent is valid only before initial trust). A mismatch is `needs_reconnect`; do not guess from remote state. If Plan 1 status has a last trusted remote OID, run after fetch:

```text
git merge-base --is-ancestor <trusted-oid> <candidate-oid>
```

Exit `1` is `remote_history_rewritten`; do not call `MutateActiveFilesystem` and do not update the trusted managed ref/status OID. Exit `0` permits sync. Once validated, checkpoint it as `CandidateOID` in the operation journal before CAS-updating `refs/igonotes/remotes/<branch>` with the previous trusted OID as `update-ref`'s expected old value; only after CAS checkpoint/publish it as trusted `RemoteOID`. Startup recovery accepts a candidate across this crash gap only when `CandidateOID`, managed ref, and operation-private fetch ref are identical. Never expose an un-CASed candidate as public `model.GitStatus.RemoteOID`.

For the non-fast-forward retry, use the first candidate OID as the trusted predecessor. A concurrent forced rewrite therefore stops the retry instead of merging it.

- [ ] **Step 7: Implement one worktree mutation pass using Plan 2**

Inside the callback passed to landed Plan 2 `NoteService.MutateActiveFilesystem`:

```text
capture pre-operation HEAD/tree
git add --all -- .
list staged paths with NUL delimiters
commit only when staged paths exist
merge captured remote OID without unrelated-history allowance
inspect MERGE_HEAD and unmerged index on merge failure
capture final HEAD/tree
capture exact push OID before unlock
compute sorted changed paths from pre-operation HEAD to final HEAD
```

Use the validated Plan 1 template renderer only for the local manual-sync commit. Initial and merge commits use stable system messages. Commit/merge hooks and signing remain enabled inside the coordinator-locked local mutation phase; for the active base that phase is also inside `MutateActiveFilesystem` and `NoteService.baseMu`. Pre-push hooks are intentionally disabled with `--no-verify` because push occurs only after that active-base lock is released.

If unmerged entries exist, return Plan 3 `ConflictError` with sorted paths from the callback and skip push. The Git service only reports the typed error; the manager-owned transaction adapter must detect it with `errors.As` and close the canonical-path coordinator gate before the service callback returns. For the active base, this precedes `MutateActiveFilesystem` reindexing and `baseMu` release even when Plan 2 joins the callback error with an index error. For an inactive base, it precedes direct mutation return; a later base switch performs the existing Plan 2 `SetBase`/index refresh. Do not resolve, stage, abort, or clean the conflict; reindex scans the working tree only and does not alter Git's unmerged index or partially staged resolutions.

- [ ] **Step 8: Implement exact-OID push and one retry**

```go
refspec := pushOID + ":refs/heads/" + snapshot.Branch
err := s.runNetwork(ctx, snapshot.Path, "push", "--no-verify", "--porcelain", "origin", refspec)
```

Immediately before each push, re-run selected-branch `ls-remote --exit-code`: absence returns `branch_deleted`; an OID different from that pass's captured candidate consumes the one concurrent-update retry without pushing stale state. This narrows the deletion/rewrite race while non-force push still enforces fast-forward server-side. Compute ahead/behind against the captured candidate with strict whitespace-safe two-integer parsing after each worktree pass; never compare against mutable remote-tracking names. A successful exact-OID push proves terminal `ahead=0, behind=0`; a rejected/unknown push retains the pre-push counts in the result/status. The complete algorithm is:

```text
fetch and validate candidate 1
worktree pass 1
recheck remote selected OID 1
push captured OID 1
if success: finish
if recheck changed or push is non-fast-forward: continue
if another error: finish with error
fetch and validate candidate 2 against candidate 1
worktree pass 2 (normally merge-only; add/commit remains conditional)
recheck remote selected OID 2
push captured OID 2
if second recheck changed or push is non-fast-forward: return push_rejected
return second push result
```

There is no third fetch, merge, or push. A timeout/transport error is not treated as non-fast-forward and is not retried immediately. After a confirmed push success, CAS-update the managed trusted ref from that pass's candidate OID to `PushOID` and return/publish `RemoteOID=PushOID`. Timeout/transport/cancellation retains the pre-push candidate as the last trusted remote OID; the next explicit operation fetches and proves whether the remote accepted `PushOID`.

- [ ] **Step 9: Handle unknown push outcome idempotently**

Persist the captured push OID before network push. On timeout, keep all local commits. The next manual operation fetches first; if the remote accepted the prior OID, ancestry/merge checks become no-ops and no duplicate local commit is created.

- [ ] **Step 10: Implement local-only recovery**

`RecoverLocal` validates root, origin, selected branch, `MERGE_HEAD`, unmerged entries, rebase/cherry-pick/revert markers, and `.git/index.lock`. It also validates candidate recovery by `CandidateOID`/managed/private-ref equality. For the post-push crash gap, it may return `RemoteOID=PushOID` only when the operation checkpoint contains `PushOID` and both managed ref and local `HEAD` equal it; code CAS-updates the managed ref only after the push runner returned success. It performs no network command and deletes nothing.

Return conflict when `MERGE_HEAD` and unmerged entries exist. Preserve partially staged resolutions. Return a blocking safe error for another unfinished local operation or repository lock. Return clean otherwise.

- [ ] **Step 11: Run sync/recovery tests GREEN**

Run:

```bash
gofmt -w internal/git/sync.go internal/git/sync_test.go internal/git/sync_integration_test.go internal/git/recovery.go internal/git/recovery_test.go
go test ./internal/git -run '^Test(Sync|RecoverLocal)' -count=1 -v
```

Expected: PASS; logs prove captured OIDs, one retry, branch deletion/rewrite rejection before mutation, and zero network commands during recovery.

- [ ] **Step 12: Commit sync and recovery**

```bash
git add internal/git/sync.go internal/git/sync_test.go internal/git/sync_integration_test.go internal/git/recovery.go internal/git/recovery_test.go
git commit -m "feat: add exact oid git sync"
```

### Task 4: Add the Sequential Deduplicating GitManager

**Files:**
- Create: `internal/service/git_manager.go`
- Create: `internal/service/git_manager_test.go`

- [ ] **Step 1: Write RED queue, dedupe, and state tests**

```go
func TestGitManagerRunsRepositoriesSequentially(t *testing.T)
func TestGitManagerDeduplicatesByCanonicalPath(t *testing.T)
func TestGitManagerDeduplicatesInitializeAgainstSync(t *testing.T)
func TestGitManagerRejectsDifferentFingerprintWhilePathQueued(t *testing.T)
func TestGitManagerQueueInitializeLocksCoordinatorBeforeFinalSnapshot(t *testing.T)
func TestGitManagerQueueSyncLocksCoordinatorBeforeFinalSnapshot(t *testing.T)
func TestGitManagerQueueHoldsCoordinatorThroughDurableFIFOAdmission(t *testing.T)
func TestGitManagerQueueDoesNotHoldManagerMutexWhileWaitingForCoordinator(t *testing.T)
func TestGitManagerQueueRaceWithReconfigureAndBaseSwitch(t *testing.T)
func TestGitManagerPersistsQueuedBeforeReturning(t *testing.T)
func TestGitManagerGeneratesRefSafeRandomOperationID(t *testing.T)
func TestGitManagerPersistsUTCNanosecondOperationCreationTime(t *testing.T)
func TestGitManagerPublishesOperationIDAndStage(t *testing.T)
func TestGitManagerPersistsSuccessAndChangedPaths(t *testing.T)
func TestGitManagerPersistsSafeFailure(t *testing.T)
func TestGitManagerConflictGateClosesBeforeActiveTransactionReturns(t *testing.T)
func TestGitManagerConflictGateClosesBeforeInactiveTransactionReturns(t *testing.T)
func TestGitManagerConflictHandoffBlocksMutationsBeforeTerminalPublication(t *testing.T)
func TestGitManagerConflictPersistenceFailureLeavesGateClosed(t *testing.T)
func TestGitManagerPersistsConflictAndIdempotentlyRepublishesGate(t *testing.T)
func TestGitManagerRejectsSyncWhileConflictOrNeedsReconnect(t *testing.T)
func TestGitManagerInitializeReprobesAndRequiresCurrentConfirmations(t *testing.T)
func TestGitManagerInitializeStopsWhenProbeConsequencesChanged(t *testing.T)
func TestGitManagerShutdownCancelsNetworkAndStartsNoNextJob(t *testing.T)
```

The two queue-specific snapshot tests cover `QueueInitialize` and `QueueSync` separately. Hold the shared coordinator, start the queue call, and prove the snapshot callback has not run; while that call is blocked, `GitManager.mu.TryLock` must succeed, proving the queue caller did not retain `GitManager.mu` while waiting for the coordinator. Then release the coordinator, block the snapshot callback, and start a competing coordinator acquisition; it must remain blocked until the queue call has inserted the journal row, published status, appended/signaled the FIFO, and returned. Inspect all three durable/in-memory results before allowing the competitor to proceed.

The reconfigure/base-switch test repeatedly races each queue method against coordinator-locked snapshot replacement and active-base changes. Every accepted operation must contain one complete before-or-after immutable snapshot, its journal/status/FIFO values must agree, and a request stale at the serialized final lookup must return `needs_reconnect` without a row, status transition, or FIFO entry. Run this test in the Task 8 race suite; do not weaken it with sleeps.

The two transaction-return tests use an error wrapped around `*git.ConflictError` so only `errors.As`, not direct type assertion, succeeds. In the active case, a channel-controlled fake note repository blocks reindexing after the wrapped callback returns; wait for its `reindexStarted` channel, assert the gate is closed and the transaction has not returned, then release reindexing. This observes the gate before `MutateActiveFilesystem` can release `baseMu`. In the inactive case, the pre-terminal barrier proves the direct transaction returned with the gate already closed. Both assert that the gate key is the current canonical path supplied by the active callback or current snapshot, never the queued job's stale path; use channels, not sleeps.

- [ ] **Step 2: Write RED recovery tests**

```go
func TestGitManagerRecoverMarksInterruptedConflict(t *testing.T)
func TestGitManagerRecoverMarksOtherInterruptedOperationFailed(t *testing.T)
func TestGitManagerRecoverRestoresMutationBlock(t *testing.T)
func TestGitManagerRecoverLeavesCleanBaseReady(t *testing.T)
func TestGitManagerRecoverLeavesUninitializedBaseNeedsReconnect(t *testing.T)
func TestGitManagerRecoverReconcilesCheckpointedTrustedRef(t *testing.T)
func TestGitManagerRecoverReconcilesTerminalJournalStatusGap(t *testing.T)
func TestGitManagerRecoverDoesNotQueueNetworkWork(t *testing.T)
```

- [ ] **Step 3: Run manager tests RED**

Run:

```bash
go test ./internal/service -run '^TestGitManager' -count=1 -v
```

Expected: FAIL because `GitManager` does not exist.

- [ ] **Step 4: Implement one global worker and unbounded in-memory queue**

Use one worker goroutine, a mutex/condition variable, a FIFO slice, and `inFlight map[string]git.Operation`. Do not use one goroutine per base and do not use a fixed-size channel that can block an HTTP handler when many bases are queued.

Define `ErrGitManagerClosed` in `git_manager.go` for queue calls after shutdown; handlers map it in Task 6. A worker whose fresh probe requires an unconfirmed mutation finishes asynchronously with a fixed `SafeError{Code: git.CodeConfirmationRequired}` before any mutating command.

The manager owns a lifetime context canceled by `Close`. Queue methods use the caller context only for durable insertion/status publication; accepted jobs run from the manager lifetime context, never the HTTP request context. Every path that needs both locks acquires `BaseOperationCoordinator` before `GitManager.mu`; no caller may hold `GitManager.mu` while acquiring or waiting for the coordinator.

The manager owns these new fields directly: `sync.Mutex`, `sync.Cond`, FIFO `[]gitJob`, `map[string]git.Operation`, closed/started flags, worker completion channel, `*git.Service`, concrete `*repository.GitStatusRepository`/`*repository.GitOperationRepository`, `*GitProbeService`, a `func(string) (git.ConfiguredBase, bool, error)` snapshot dependency whose boolean means active, `*NoteService`, the shared `*BaseOperationCoordinator`, and a private `now func() time.Time` defaulted to `time.Now`. Production startup binds the snapshot function to `SettingsService.GitSnapshot`; manager tests use local repository fixtures, a closure, and may replace the private clock with a fixed nanosecond UTC instant. For deterministic same-package tests only, add an unexported `beforeTerminalPublication func()` barrier defaulted to a no-op; invoke it after the worktree transaction has returned and before any terminal journal/status write, never from inside the mutation callback. Do not create parallel runner, status, coordinator, worktree, mutation-policy, settings interface files, or constructor parameters for the clock or test barrier.

Freeze the concrete constructor with those dependencies in that order; because it returns no error, panic with dependency context on any nil argument, matching landed `NewNoteService` fail-fast wiring:

```go
func NewGitManager(
	gitService *git.Service,
	statuses *repository.GitStatusRepository,
	operations *repository.GitOperationRepository,
	prober *GitProbeService,
	snapshot func(string) (git.ConfiguredBase, bool, error),
	notes *NoteService,
	coordinator *BaseOperationCoordinator,
) *GitManager
```

Freeze these public methods before Wave 3:

```go
func (m *GitManager) QueueInitialize(context.Context, git.InitializeRequest) (git.Operation, bool, error)
func (m *GitManager) QueueSync(context.Context, git.SyncRequest) (git.Operation, bool, error)
func (m *GitManager) RecoverLocal(context.Context, []git.ConfiguredBase) error
func (m *GitManager) Start() error
func (m *GitManager) Close() error
```

The boolean reports deduplication. Public status reads continue through landed `GitStatusService`; the manager only updates the same concrete Plan 1 status repository.

- [ ] **Step 5: Implement durable queueing and dedupe**

Use one shared queue helper for `QueueInitialize` and `QueueSync`. Acquire `BaseOperationCoordinator` before the final current snapshot lookup and retain it through durable journal insertion, public status publication, and FIFO admission. Only after coordinator ownership may the helper acquire `GitManager.mu`; never read or retain `GitManager.mu` first:

```text
acquire BaseOperationCoordinator without holding GitManager.mu
resolve request.Snapshot.Name through the snapshot dependency
require exact name/path/full-fingerprint equality with the final current snapshot
run lock-free coordinator.CheckMutation for that canonical path
acquire GitManager.mu while still holding the coordinator
reject closed manager
return in-memory active operation when present
query journal ActiveByPath to cover process-local map gaps
capture one high-resolution UTC creation instant
construct the operation from the validated current snapshot
insert queued journal row
persist public initializing/syncing status with operation ID
append FIFO job
signal worker
release GitManager.mu
release BaseOperationCoordinator
return operation and deduplicated flag
```

The lookup under coordinator ownership is the final current-state authority. Reject a forged or stale request snapshot with `CodeNeedsReconnect` before touching `GitManager.mu`, the journal, status, or FIFO. The validated current `ConfiguredBase` is the only snapshot copied into the job; do not cache the active boolean for execution, because the worker re-reads both under its own coordinator ownership. This serializes admission against Plan 2 configure, disable, rename, path change, active-base switch, and conflict publication.

The partial unique index is the final per-path dedupe authority. Compare base name and full request fingerprint with the active journal row: the same canonical path, base name, and fingerprint returns `202` with the same operation ID even across initialize/sync; a changed name or fingerprint returns landed `ErrGitRepositoryInUse`/`409 git_repository_in_use` and never mutates the active row. Persist both fingerprints with the operation so dedupe and remote-trust decisions remain safe across restart. All dedupe returns occur while the coordinator still protects the final snapshot/admission ordering.

Before insertion, call lock-free `coordinator.CheckMutation(current.Path)` for both kinds while the coordinator is owned. `QueueSync` also rejects landed public state `needs_reconnect`; `QueueInitialize` is the explicit operation allowed to leave that state. Queueing initialize publishes `model.GitStateInitializing`; queueing sync publishes `model.GitStateSyncing`.

Generate the operation ID as exactly 32 lowercase hex characters, then capture `createdAt := m.now().UTC()` once and assign that exact instant to both `Operation.CreatedAt` and its initial `UpdatedAt`. Store timestamps with nanosecond-preserving UTC serialization so backup naming remains stable across restart. Create the journal row before publishing status and before appending memory. If status publication fails, finish the row as failed with a safe persistence error and return without appending. A process crash between the two writes leaves a queued row that synchronous startup recovery marks interrupted; it never silently starts network work. Every return path releases `GitManager.mu` before releasing the coordinator.

- [ ] **Step 6: Execute jobs under the exact Plan 2 coordinator and filesystem transaction**

The worker executes this lock order:

```go
m.coordinator.Lock()
defer m.coordinator.Unlock()

current, active, err := m.snapshot(job.Operation.BaseName)
if err != nil {
	return &git.SafeError{Code: git.CodeNeedsReconnect, Message: "Git configuration changed; reconnect is required"}
}
if current.Name != job.Snapshot.Name ||
	current.Path != job.Snapshot.Path ||
	current.Fingerprint != job.Snapshot.Fingerprint {
	return &git.SafeError{Code: git.CodeNeedsReconnect, Message: "Git configuration changed; reconnect is required"}
}

transaction := func(_ context.Context, mutate func(string) error) error {
	guardedMutate := func(canonicalPath string) error {
		err := mutate(canonicalPath)
		var conflictErr *git.ConflictError
		if errors.As(err, &conflictErr) {
			m.coordinator.SetConflict(canonicalPath, true)
		}
		return err
	}

	if active {
		return m.notes.MutateActiveFilesystem(current.Path, guardedMutate)
	}
	return guardedMutate(current.Path)
}
```

`current.Path` is canonical because it comes from the final coordinator-owned snapshot. For the active base, `guardedMutate` receives and uses the canonical path revalidated by `MutateActiveFilesystem`; for an inactive base, pass the current snapshot path to the same guard. `SetConflict` is the lock-free copy-on-write publication established by Plan 2, so calling it while the manager already owns the coordinator and, for an active base, `baseMu` does not acquire an earlier lock. After `mutate` returns, the guard's conflict-handoff segment performs only `errors.As` and `SetConflict`: it must not route handoff through `Progress` or call `SettingsService`, `NoteService`, either repository, or terminal publication from inside the mutation callback.

Dequeue under `GitManager.mu`, release it, and only then acquire the coordinator. After the service/transaction returns, invoke the private test barrier and perform terminal journal/status publication. Remove the terminal entry from `inFlight` by briefly acquiring `GitManager.mu` while the coordinator is still owned, then release `GitManager.mu` before the coordinator. This uses the same `coordinator -> GitManager.mu` order as queue admission and prevents a newly admitted request from observing a terminal operation as active. Never hold `GitManager.mu` while taking the coordinator or running Git.

The Git service's mutation function may call the Plan 1 runner and its existing `Progress` callback only; it must not call `SettingsService`, `NoteService`, or `noteRepository` from inside `MutateActiveFilesystem`. The manager's progress callback may write only operation/status metadata repositories, preserving Plan 2 order `coordinator -> NoteService.baseMu -> metadata SQLite`; it must not call back into settings, notes, or the coordinator. The sole conflict-handoff exception is the manager adapter's direct lock-free `SetConflict` publication after the service mutation returns and before the adapter returns. For initialize, re-run landed `GitProbeService.Probe` from the current snapshot after freshness succeeds, re-check every required mutation against `InitializeRequest.Confirmations`, then construct service `InitializeOptions`. Derive `LastRemoteOID` from public status plus `LatestByPath` only when the remote fingerprint matches: use the sole nonempty value or their equal value; two different nonempty values are `needs_reconnect`. A genuinely new path/URL/branch fingerprint passes an empty value so initialize re-baselines internal trust. A base rename alone preserves trust because canonical path/URL/branch are unchanged. Sync always requires the public trusted OID and managed ref to agree. Then invoke `Service.Initialize` or `Service.Sync` with `transaction`. The coordinator remains locked through probe/fetch, local mutation, and push; `NoteService.baseMu` is held only by active-base `MutateActiveFilesystem`.

Each service checkpoint updates journal and public `model.GitStatus`. The settings snapshot read occurs after coordinator lock and before worktree mutation, preserving Plan 2 order `coordinator -> SettingsService.mu -> NoteService.baseMu -> repository/SQLite`.

- [ ] **Step 7: Persist terminal states and idempotently republish conflict state**

On success, persist `ready`, OIDs, ahead/behind, changed paths, and success time. On freshness mismatch, finish only the stale journal row with `CodeNeedsReconnect`; do not recreate or overwrite public status because landed `SettingsService` has already reconciled the renamed/moved/disabled/reconfigured base to `needs_reconnect` or deleted its status. On another ordinary error, persist `error` and the safe API error while leaving mutations enabled.

On an error for which `errors.As` finds `*git.ConflictError`, first call `m.coordinator.SetConflict(current.Path, true)` again while the coordinator is locked, then finish the operation as conflict with separate sorted conflict paths and durably publish `model.GitStateConflict`. This terminal call is an intentional idempotent republication of the gate already closed by the transaction adapter; durable journal/status publication remains mandatory but is not the synchronization point for note safety. Attempt and report the terminal writes according to the repository error policy, but never clear or roll back the gate if either write fails. Startup recovery uses the durable conflict row/status that did succeed to republish `true`; this plan never clears a real conflict because conflict resolution is out of scope. Never persist stderr or a remote URL.

This plan does not increment failure counters, enter `paused`, or enqueue a timed retry.

- [ ] **Step 8: Implement synchronous startup recovery**

Expose `RecoverLocal(ctx, configuredSnapshots)` and call the Git service's local recovery for every fully configured Git base. Load queued/running rows and `LatestByPath`; pass the latest operation to `RecoveryOptions` even when terminal so status-write crash gaps remain recoverable. Reconcile a checkpointed candidate into public `RemoteOID` only when `CandidateOID`, managed ref, and operation-private fetch ref are identical; also accept the service's stricter post-push `PushOID == managed ref == HEAD` result. Mark an interrupted row `conflict` when repository state is conflicted; otherwise mark it failed with the Plan 3 `operation_interrupted` safe error added to `internal/git/errors.go` in Task 1. Restore mutation blocks for conflict/ambiguous local operation. A configured base with no exact-root repository and no unfinished row remains `needs_reconnect`; recovery never initializes it. Do not enqueue initialize/sync and do not access the network.

- [ ] **Step 9: Run manager tests GREEN**

Run:

```bash
gofmt -w internal/service/git_manager.go internal/service/git_manager_test.go
go test ./internal/service -run '^TestGitManager' -count=1 -v
```

Expected: PASS; maximum mutating-service concurrency is one, duplicate paths share one operation ID, and recovery performs no network work.

- [ ] **Step 10: Commit manager integration**

```bash
git add internal/service/git_manager.go internal/service/git_manager_test.go
git commit -m "feat: orchestrate git operations"
```

### Task 5: Publish Immutable Git Snapshots from the Landed Settings Owner

**Files:**
- Modify: `internal/service/settings_service.go`
- Modify: `internal/service/settings_service_test.go`

- [ ] **Step 1: Write RED configuration snapshot tests**

```go
func TestSettingsServiceConfigureGitForInitializeReturnsSavedSnapshot(t *testing.T)
func TestSettingsServiceConfigureGitForInitializeDoesNotPersistConfirmations(t *testing.T)
func TestSettingsServiceConfigureGitForInitializeRejectsConflictBeforeSave(t *testing.T)
func TestSettingsServiceConfigureGitPreservesTrustedOIDForSameRemote(t *testing.T)
func TestSettingsServiceConfigureGitClearsTrustedOIDForChangedRemote(t *testing.T)
func TestSettingsServiceDisableGitRejectsConflict(t *testing.T)
func TestSettingsServiceGitSnapshotIsDetachedAndReportsActive(t *testing.T)
func TestSettingsServiceGitSnapshotRejectsUnknownOrIncompleteBase(t *testing.T)
```

Keep all landed Plan 1 `ConfigureGit` validation, status compensation, response, and one-time-confirmation behavior. Assert the new initialize variant returns a `ConfiguredBase` built from the exact config publication performed by that call, not from a second unlocked lookup. Assert `GitSnapshot` returns a detached canonical value and computes active state under the same settings read lock. Queue ordering belongs to Task 6 because `SettingsService` must not depend on `GitManager`.

- [ ] **Step 2: Run settings tests RED**

Run:

```bash
go test ./internal/service -run 'TestSettingsService(ConfigureGitForInitialize|GitSnapshot)' -count=1 -v
```

Expected: FAIL because the Plan 3 snapshot methods do not exist.

- [ ] **Step 3: Keep the exact Plan 2 SettingsService dependency graph**

Do not add a manager setter or package-global queue. Preserve both integrated Plan 1-2 constructors and their exact dependency direction:

```go
NewNoteService(repo, basePath, coordinator)
NewSettingsService(store, notes, coordinator, activeBaseName, logger)
NewSettingsServiceWithGit(store, notes, coordinator, activeBaseName, logger, validator, statuses)
```

`SettingsService` remains the sole config owner. `GitManager` reads fresh snapshots from it, while `GitHandler` performs the post-persistence queue call. This direction avoids `SettingsService -> GitManager -> SettingsService` construction and runtime cycles.

- [ ] **Step 4: Refactor the landed configure transaction to return its publication snapshot**

Extract the landed Plan 1 configure body into a private helper called only while the existing Plan 2 mutation wrapper holds `BaseOperationCoordinator` then `SettingsService.mu`. It retains this order:

```text
locate base
validate request with Plan 1 validators
check canonical path uniqueness
copy config
apply Git fields
upsert needs_reconnect with existing compensation
persist config through applyConfigLocked
publish in-memory config
build immutable snapshot and fingerprint
return existing GitConfigResponse plus immutable snapshot
```

When upserting `needs_reconnect`, preserve the previous status `RemoteOID` only when canonical path, URL, and branch are unchanged; clear it for a changed remote fingerprint. This lets template/autosync-only changes retain rewrite protection without carrying trust across a path/remote/branch change. The existing `ConfigureGit` calls the helper and discards the snapshot, preserving its exact Plan 1 signature and other non-conflict behavior. Both configure entry points first call lock-free `coordinator.CheckMutation` for the resolved canonical path while the coordinator/mu wrapper is held; `ConfigureGitForInitialize` then returns both values. Add the same check to `DisableGit`: disabling Git must not hide an unresolved merge while leaving a blocked working tree. Confirmations remain request-only and are never persisted. Neither method queues work.

Freeze the settings methods consumed by the handler:

```go
func (s *SettingsService) ConfigureGitForInitialize(
	context.Context,
	string,
	model.GitConfigRequest,
) (model.GitConfigResponse, git.ConfiguredBase, error)

func (s *SettingsService) GitSnapshot(string) (git.ConfiguredBase, bool, error)
```

`GitSnapshot` takes `SettingsService.mu.RLock` but does not acquire the coordinator: the manager already holds the non-reentrant coordinator when it calls this function. The boolean is true only when the named base is current in that same locked config snapshot. Both methods reject an unknown or incompletely configured base and share one private `configuredBaseLocked` builder.

- [ ] **Step 5: Run settings tests GREEN**

Run:

```bash
gofmt -w internal/service/settings_service.go internal/service/settings_service_test.go
go test ./internal/service -run 'TestSettingsService(ConfigureGit|ConfigureGitForInitialize|GitSnapshot|.*Coordinator)' -count=1 -v
```

Expected: PASS; all landed configure/compensation tests still pass, and Plan 3 receives the exact canonical publication snapshot without a manager dependency or coordinator re-entry.

- [ ] **Step 6: Commit settings integration**

```bash
git add internal/service/settings_service.go internal/service/settings_service_test.go
git commit -m "feat: expose git config snapshots"
```

### Task 6: Add Configure, Status, and Manual Sync APIs

**Files:**
- Modify: `internal/handlers/git_handler.go`
- Modify: `internal/handlers/git_handler_test.go`
- Modify: `internal/handlers/git_routes.go`
- Modify: `internal/handlers/git_routes_test.go`
- Modify: `internal/handlers/errors.go`
- Modify: `internal/handlers/errors_test.go`

- [ ] **Step 1: Write RED handler tests**

```go
func TestGitHandlerConfigureReturnsAccepted(t *testing.T)
func TestGitHandlerConfigureDuplicateReturnsSameOperation(t *testing.T)
func TestGitHandlerConfigurePersistsBeforeQueueInitialize(t *testing.T)
func TestGitHandlerConfigureKeepsSavedConfigWhenQueueFails(t *testing.T)
func TestGitHandlerManualSyncReturnsAccepted(t *testing.T)
func TestGitHandlerManualSyncDuplicateReturnsSameOperation(t *testing.T)
func TestGitHandlerRejectsMissingBase(t *testing.T)
func TestGitHandlerDoesNotLeakStderrOrURL(t *testing.T)
func TestGitHandlerLandedProbeDisableStatusRemainUnchanged(t *testing.T)
```

Successful manual-sync response:

```json
{
  "operation_id": "0123456789abcdef0123456789abcdef",
  "status": "queued",
  "deduplicated": false
}
```

- [ ] **Step 2: Write RED route tests**

Register and test:

| Method | Route | Success |
|---|---|---:|
| `POST` | `/api/git/probe` | `200` |
| `PUT` | `/api/git/config?base=<name>` | `202` |
| `DELETE` | `/api/git/config?base=<name>` | `200` |
| `GET` | `/api/git/status` | `200` |
| `GET` | `/api/git/status?base=<name>` | `200` |
| `POST` | `/api/git/sync?base=<name>` | `202` |

All routes use existing local-origin and setup guards. Wrong methods return `405` with exact `Allow` values.

- [ ] **Step 3: Run handler tests RED**

Run:

```bash
go test ./internal/handlers -run 'TestGit(Handler|Routes)' -count=1 -v
```

Expected: FAIL because the landed handler has no operation queue and the landed route table has no manual-sync route.

- [ ] **Step 4: Implement the handler against SettingsService and GitManager**

```go
type GitOperationConfigurer interface {
	GitConfigurer
	ConfigureGitForInitialize(context.Context, string, model.GitConfigRequest) (model.GitConfigResponse, git.ConfiguredBase, error)
	GitSnapshot(string) (git.ConfiguredBase, bool, error)
}

type GitOperations interface {
	QueueInitialize(context.Context, git.InitializeRequest) (git.Operation, bool, error)
	QueueSync(context.Context, git.SyncRequest) (git.Operation, bool, error)
}

type GitHandler struct {
	// Retain landed prober/configurer/statuses fields.
	operationConfig GitOperationConfigurer
	operations      GitOperations
}

func NewGitHandlerWithOperations(
	prober GitProber,
	configurer GitOperationConfigurer,
	statuses GitStatusReader,
	operations GitOperations,
) *GitHandler
```

Keep landed `NewGitHandler(prober, configurer, statuses)` source-compatible for its existing Plan 1 tests and read-only/disable uses; production switches to `NewGitHandlerWithOperations`. Do not redefine `GitProber`, `GitConfigurer`, or `GitStatusReader`. Use existing `decodeSingleJSON`, `writeJSON`, and `writeServiceError`. In operation mode, `PUT config` calls `ConfigureGitForInitialize` first, then passes its exact returned snapshot and the original request's complete `model.GitConfirmations` to `QueueInitialize`. If queue insertion fails, return its safe error without rolling back valid config. After queue/dedupe, read the one-base status through landed `GitStatusReader` so `GitConfigResponse.Status` exactly reflects initializing/syncing state, and attach `GitOperationResponse`. `POST sync` gets `GitSnapshot` and calls `QueueSync`. `GET status` remains delegated to landed `GitStatusReader`, which reads the repository updated by the manager. Both queue endpoints return `202` for a new or deduplicated operation. Handler tests record exact `save`, then `queue_initialize` events.

The configure payload includes the saved base and nested operation response; manual sync returns the operation response directly:

```json
{
  "base": {
    "name": "work",
    "path": "/canonical/notes",
    "git_url": "file:///remote.git",
    "git_branch": "main",
    "auto_sync": false
  },
  "status": {
    "base": "work",
    "repository_path": "/canonical/notes",
    "state": "initializing",
    "operation_id": "0123456789abcdef0123456789abcdef",
    "stage": "queued",
    "ahead": 0,
    "behind": 0,
    "consecutive_failures": 0,
    "changed_paths": []
  },
  "operation": {
    "operation_id": "0123456789abcdef0123456789abcdef",
    "status": "queued",
    "deduplicated": false
  }
}
```

- [ ] **Step 5: Extend the landed Git route registration**

Add to existing `RegisterGitRoutes`; keep `NewRouter` source-compatible and keep existing probe/config/status registrations:

```go
mux.Handle("/api/git/sync", RequireLocalOrigin(methods(map[string]http.Handler{
	http.MethodPost: RequireSetup(state, http.HandlerFunc(handler.Sync)),
})))
```

Do not register on `http.DefaultServeMux` or move Git endpoints into `routes.go`.

- [ ] **Step 6: Map safe Git errors**

Extend the landed `serviceErrorMappings`/`SafeError` code switch; retain every Plan 1 mapping:

| Error code | HTTP status |
|---|---:|
| `branch_deleted` | `409` |
| `remote_history_rewritten` | `409` |
| `origin_mismatch` | `409` |
| `repository_root_mismatch` | `409` |
| `repository_locked` | `409` |
| `git_conflict_pending` | `409` |
| `push_rejected` | `409` |
| `needs_reconnect` | `409` |
| `git_repository_in_use` | `409` |
| manager shutting down | `503` |

Task 4 defines service sentinel `ErrGitManagerClosed`; reuse landed `ErrGitRepositoryInUse` and Plan 2 `ErrGitConflictPending`. Use only fixed safe messages or `SafeError.Message`. Never serialize runner stderr, `Diagnostic()`, command output, filesystem internals, or remote URL values.

- [ ] **Step 7: Do not register out-of-scope routes**

Confirm there is no `/api/git/resume`, `/api/git/conflicts`, `/resolve`, `/complete`, or `/abort` registration.

- [ ] **Step 8: Run handler tests GREEN**

Run:

```bash
gofmt -w internal/handlers/git_handler.go internal/handlers/git_handler_test.go internal/handlers/git_routes.go internal/handlers/git_routes_test.go internal/handlers/errors.go internal/handlers/errors_test.go
go test ./internal/handlers -run 'TestGit(Handler|Routes)|TestWriteServiceError' -count=1 -v
```

Expected: PASS; queue endpoints return `202`, duplicate requests reuse operation IDs, and guards/error redaction hold.

- [ ] **Step 9: Commit HTTP integration**

```bash
git add internal/handlers/git_handler.go internal/handlers/git_handler_test.go internal/handlers/git_routes.go internal/handlers/git_routes_test.go internal/handlers/errors.go internal/handlers/errors_test.go
git commit -m "feat: expose manual git sync api"
```

### Task 7: Recover Locally Before Serving HTTP

**Files:**
- Modify: `cmd/api/main.go`
- Create: `cmd/api/git_runtime_test.go`

- [ ] **Step 1: Write RED startup-order tests**

```go
func TestGitRecoveryCompletesBeforeInitialIndexAndServe(t *testing.T)
func TestGitManagerStartsAfterLocalRecovery(t *testing.T)
func TestGitManagerClosesDuringShutdown(t *testing.T)
func TestUnconfiguredBasesDoNotInvokeGit(t *testing.T)
func TestRecoveryUsesNoNetworkCommand(t *testing.T)
```

The event order must be:

```text
construct git runtime
recover local repositories
start manager worker
start initial NoteService SyncFS
construct router
serve
```

- [ ] **Step 2: Run startup tests RED**

Run:

```bash
go test ./cmd/api -run 'TestGitRecovery|TestGitManagerStarts|TestUnconfiguredBases|TestRecoveryUsesNoNetwork' -count=1 -v
```

Expected: FAIL because `runServer` does not construct or recover Git services.

- [ ] **Step 3: Construct landed dependencies in current startup flow**

Reconcile both landed plans in this exact startup order:

```text
repository.InitDB
one BaseOperationCoordinator
NewNoteService(noteRepository, basePath, coordinator)
Plan 1 CommandRunner and Client
Plan 1 GitStatusRepository and GitConfigValidator
NewSettingsServiceWithGit(store, noteService, coordinator, activeBaseName, logger, validator, statusRepository)
Plan 1 GitProbeService and GitStatusService
Task 1 GitOperationRepository
Plan 3 git.Service using the same Runner and Client-as-Porcelain
GitManager(service, statusRepository, operationRepository, probeService, settingsService.GitSnapshot, noteService, coordinator)
NewGitHandlerWithOperations(probeService, settingsService, statusService, manager)
```

`GitManager` creates its `WorktreeTransaction` closure per job by calling the existing `noteService.MutateActiveFilesystem`; do not construct another worktree adapter object. Keep one runner, porcelain client, metadata database, status repository, NoteService, SettingsService, and coordinator. Constructing the runner is allowed for unconfigured bases, but do not execute Git or require the executable until a fully configured base is recovered or a Git endpoint is invoked.

- [ ] **Step 4: Recover synchronously before serving**

Call manager local recovery with all fully configured snapshots before launching the current asynchronous `noteService.SyncFS` goroutine. Recovery errors caused by a single repository become persisted safe status; metadata database failure aborts startup.

After recovery, start the one manager worker. Defer manager close before database close. Graceful shutdown cancels the current network command and does not enqueue or create a final commit.

- [ ] **Step 5: Keep the landed router and register the extended Git routes**

```go
router := handlers.NewRouter(noteHandler, settingsHandler, settingsService, spaHandler)
handlers.RegisterGitRoutes(router, gitHandler, settingsService)
```

Keep the existing `NewRouter` signature and `registerSystemRoutes` call unchanged; do not register duplicate Plan 1 routes.

- [ ] **Step 6: Verify scheduler/circuit-breaker absence**

Run:

```bash
! git grep -n -E 'NewTicker|time\.After|paused|resume|failure.*5|auto.*queue' -- internal/git/connect.go internal/git/sync.go internal/git/recovery.go internal/service/git_manager.go cmd/api/main.go
```

Expected: no Plan 3 production-code matches.

- [ ] **Step 7: Run startup tests GREEN**

Run:

```bash
gofmt -w cmd/api/main.go cmd/api/git_runtime_test.go
go test ./cmd/api -run 'TestGitRecovery|TestGitManagerStarts|TestGitManagerCloses|TestUnconfiguredBases|TestRecoveryUsesNoNetwork' -count=1 -v
```

Expected: PASS; local recovery precedes initial index/serve and shutdown closes the worker.

- [ ] **Step 8: Commit startup wiring**

```bash
git add cmd/api/main.go cmd/api/git_runtime_test.go
git commit -m "feat: recover git state before serving"
```

### Task 8: Add Conflict Blocking and Cross-Layer Concurrency Regression Coverage

**Files:**
- Modify: `internal/service/git_manager_test.go`
- Modify: `internal/handlers/git_handler_test.go`
- Modify: `cmd/api/git_runtime_test.go`

- [ ] **Step 1: Add conflict mutation regression tests through landed Plan 2 policy**

Create a real merge conflict through `GitManager`, then assert save, create, rename, delete, and asset upload return `git_conflict_pending`. Reads and tree/status access remain available. Do not add conflict resolution or abort calls.

Make the handoff race deterministic with the Task 4 `beforeTerminalPublication` barrier. Pause the worker after the active-base transaction has returned, and therefore after `NoteService.baseMu` has unlocked, but before either terminal journal or status publication. Assert the journal/status are still nonterminal, then run ordinary save, create, rename, and delete operations and require each to return `ErrGitConflictPending` without a filesystem or repository side effect. This proves no ordinary mutation can pass through the callback-to-terminal handoff; do not use sleeps or probabilistic repetition.

Run the same transaction-adapter scenario with the snapshot marked inactive and assert `coordinator.CheckMutation(currentCanonicalPath)` is blocked at the pre-terminal barrier, while a different path remains allowed. For the persistence-failure case, perform the active-base mutation assertions first, close the test metadata database while the worker is held at the barrier, release it so terminal journal/status writes fail, wait for the worker to finish, and assert `coordinator.CheckMutation(currentCanonicalPath)` still returns `ErrGitConflictPending`. The manager must never compensate for durable publication failure by reopening the gate.

- [ ] **Step 2: Add worktree/network lock-order regression tests**

```go
func TestGitSyncSerializesAgainstSaveCreateRenameDelete(t *testing.T)
func TestGitPushRunsAfterWorktreeUnlock(t *testing.T)
func TestGitQueueAdmissionSerializesWithReconfigureAndBaseSwitch(t *testing.T)
func TestGitIncomingMergeReindexesBeforeReadersResume(t *testing.T)
func TestGitInactiveBaseOperationBlocksSwitchNotActiveEditor(t *testing.T)
func TestGitManagerConflictHandoffBlocksMutationsBeforeTerminalPublication(t *testing.T)
func TestGitManagerConflictPersistenceFailureLeavesGateClosed(t *testing.T)
func TestGitForbiddenCommandsAbsentFromIntegratedFlows(t *testing.T)
```

Block fetch and separately push in the fake runner and prove a note save can finish while each network command remains blocked. Block a local add/merge command inside `MutateActiveFilesystem` and prove save/create/rename/delete/asset upload wait until reindex/unlock. This demonstrates that the coordinator spans the operation while `NoteService.baseMu` spans only local worktree mutation/reindex. At conflict handoff, require the gate to remain closed after that unlock and while terminal publication is deliberately blocked or failing. Race queue admission against real coordinator-backed reconfiguration and active-base switching; assert the final snapshot lookup, journal row, status publication, and FIFO job are one serialized before-or-after state for both initialize and sync. The forbidden-command test runs connect, sync, retry, conflict, and recovery scenarios through a recording landed `Runner`, fails on any forbidden argv token sequence, and requires every recorded push to include `--no-verify` before `origin`.

- [ ] **Step 3: Add HTTP dedupe concurrency regression test**

Send concurrent manual-sync requests for one base and require `202` plus one shared operation ID. Send requests for two bases and require distinct operation IDs but maximum mutating-service concurrency of one.

- [ ] **Step 4: Run the focused regression tests**

Run:

```bash
go test ./internal/service ./internal/handlers ./cmd/api -run 'TestGit(ManagerConflict|SyncSerializes|PushRuns|QueueAdmission|IncomingMerge|InactiveBase|ForbiddenCommands|Conflict|ManualSync)' -count=1 -v
```

Expected: PASS. A failure identifies a regression in the already TDD-implemented Task 4 manager, Task 6 handler, or landed Plan 2 transaction/policy; return to that owning task, add the failing assertion to its unit test, and fix it there before continuing.

- [ ] **Step 5: Format the added tests and rerun them GREEN**

Run:

```bash
gofmt -w internal/service/git_manager_test.go internal/handlers/git_handler_test.go cmd/api/git_runtime_test.go
go test ./internal/service ./internal/handlers ./cmd/api -run 'TestGit(ManagerConflict|SyncSerializes|PushRuns|QueueAdmission|IncomingMerge|InactiveBase|ForbiddenCommands|Conflict|ManualSync)' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Run race coverage**

Run:

```bash
go test -race ./internal/git ./internal/repository ./internal/service ./internal/handlers ./cmd/api -count=1
```

Expected: PASS with no race reports.

- [ ] **Step 7: Commit cross-layer coverage**

```bash
git add internal/service/git_manager_test.go internal/handlers/git_handler_test.go cmd/api/git_runtime_test.go
git commit -m "test: cover git sync orchestration"
```

## Final Verification

- [ ] **Run all backend tests**

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Run the full race suite**

```bash
go test -race ./... -count=1
```

Expected: PASS with no race reports.

- [ ] **Run static analysis and build**

```bash
go vet ./...
go build ./cmd/...
```

Expected: no diagnostics and exit status `0`.

- [ ] **Verify forbidden Git operations are absent from production code**

```bash
! git grep -n -E '"--force"|"--force-with-lease"|"reset"|"rebase"|"clean"|"prune"|"--delete"|"update-ref", "-d"|"branch", "-[dD]"|"merge", "--abort"' -- internal/git/connect.go internal/git/sync.go internal/git/recovery.go internal/service/git_manager.go
```

Expected: no production-code matches. Test setup may use history-rewrite commands only inside `_test.go` fixtures.

- [ ] **Verify exact-OID and retry tests repeatedly**

```bash
go test ./internal/git -run 'TestSync(PushesCapturedExactOID|RetriesOneNonFastForwardPush|StopsAfterSecondNonFastForwardPush)' -count=10 -v
```

Expected: PASS in all ten runs.

- [ ] **Verify all four connect cases repeatedly**

```bash
go test ./internal/git -run 'TestInitialize(NoRepositoryEmptyRemote|NoRepositoryExistingRemoteBranch|ExistingRepositoryEmptyRemote|ExistingRepositoryExistingRemoteBranch)$' -count=5 -v
```

Expected: PASS in all five runs.

- [ ] **Verify startup recovery ordering repeatedly**

```bash
go test ./cmd/api -run 'TestGitRecoveryCompletesBeforeInitialIndexAndServe|TestRecoveryUsesNoNetworkCommand' -count=10 -v
```

Expected: PASS in all ten runs.

- [ ] **Verify the conflict handoff gate repeatedly**

```bash
go test ./internal/service -run 'TestGitManager(ConflictGate|ConflictHandoff|ConflictPersistence)' -count=10 -v
```

Expected: PASS in all ten runs; the channel-controlled pre-terminal handoff has no sleeps, all ordinary mutations remain blocked, and terminal persistence failure never reopens the gate.

- [ ] **Inspect the intended commit sequence**

```bash
git log --oneline -8
```

Expected Plan 3 commits, newest first:

```text
test: cover git sync orchestration
feat: recover git state before serving
feat: expose manual git sync api
feat: expose git config snapshots
feat: orchestrate git operations
feat: add exact oid git sync
feat: add idempotent git connect
feat: persist git operation journal
```

## Completion Criteria

- [ ] The implementation extends, rather than duplicates, Plan 1 runner/config/status and Plan 2 `BaseOperationCoordinator`/`MutateActiveFilesystem` contracts.
- [ ] All four initial-connect cases preserve local and remote commits and are retry-safe.
- [ ] Backup refs use the operation's durable high-resolution UTC creation timestamp, persist their exact collision-safe name before creation, and protect snapshots before branch switching or history merging.
- [ ] The only empty commit is the initial empty-local/empty-remote branch bootstrap.
- [ ] Ordinary sync commits only staged changes and never creates an empty commit.
- [ ] Fetch, merge, and push use captured full OIDs.
- [ ] Push never uses a force option or mutable `HEAD` refspec, and every push uses `--no-verify` because it runs outside the active-base `NoteService` lock.
- [ ] A non-fast-forward push triggers exactly one fetch/merge/push retry.
- [ ] Deleted or rewritten remote history stops before unsafe worktree mutation.
- [ ] The manager-owned transaction adapter detects wrapped `ConflictError` with `errors.As` and closes the current canonical-path gate before the active callback returns and unlocks `NoteService.baseMu`, or before an inactive callback returns.
- [ ] Durable terminal conflict journal/status publication idempotently republishes the gate; publication failure leaves it closed, and save/create/rename/delete cannot pass through the handoff window.
- [ ] The manager executes one global Git operation at a time and deduplicates by canonical path.
- [ ] Configuration persists before initialize queueing; configure and manual sync return `202`.
- [ ] Startup recovery is local-only and completes before HTTP serving.
- [ ] No scheduler, circuit breaker, pause/resume behavior, or conflict-resolution API is introduced.
