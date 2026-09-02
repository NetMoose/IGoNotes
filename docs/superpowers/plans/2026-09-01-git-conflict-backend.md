# Git Synchronization Plan 4: Conflict Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add safe backend conflict inspection, one-path resolution, merge completion, abort, restart recovery, and guarded REST routes on top of Plan 3 manual synchronization.

**Architecture:** Extend the landed `internal/git.Service`, Plan 3 operation journal, sequential `service.GitManager`, Plan 2 coordinator/worktree transaction, and mutation policy rather than creating parallel primitives. Pure NUL-delimited parsers feed read-only conflict inspection; resolution, complete, abort, reindex, exact-OID push, and recovery remain serialized, with Git's index and `MERGE_HEAD` as repository truth and `git.Operation` checkpoints as crash-recovery intent.

**Tech Stack:** Go 1.26, system Git 2.28+, `net/http`, SQLite through `modernc.org/sqlite`, `os.Root`, table-driven tests, temporary bare/working repositories, and the Go race detector.

---

## Dependency and Scope

Execute this plan only after Plans 1-3 have landed and their tests pass.

Plan 1 owns:

- `internal/git/runner.go`: the sole `exec.CommandContext` boundary, local/network timeouts, bounded output, noninteractive environment, redaction, and classified command errors.
- `internal/git/porcelain.go` and `internal/git/errors.go`: repository inspection and safe errors.
- `internal/service/git_config.go` and `internal/service/git_probe_service.go`: Git configuration validation and probe ownership.
- `internal/repository/git_status_repo.go`: public status persistence by canonical repository path.
- `internal/model/config.go` and `internal/model/api.go`: configured base and Git API models.

Plan 2 owns:

- `internal/service/base_operation_coordinator.go`: serialization and lock-free conflict mutation policy.
- `internal/service/note_filesystem_transaction.go`: active-base `NoteService.MutateActiveFilesystem` transaction and mandatory reindex callback.
- `internal/service/note_service.go`: revision-safe note writes and worktree/reindex integration.

Plan 3 owns:

- `internal/git/operation.go`, `internal/git/service.go`, `internal/git/connect.go`, `internal/git/sync.go`, and `internal/git/recovery.go`: operation contracts, `Service`, connect, exact-OID manual sync, and local-only recovery.
- `internal/repository/git_operation_repo.go`: persisted `git.Operation` journal and active-operation lookup.
- `internal/service/git_manager.go`: one global FIFO worker, per-path deduplication, coordinator ownership, status publication, and mutation policy.
- `internal/handlers/git_handler.go` and `internal/handlers/git_routes.go`: configure, status, manual-sync APIs, and guarded route registration.
- `cmd/api/git_runtime_test.go`: startup recovery/worker/shutdown ordering.

The required starting state is a Plan 3 `OperationConflict` row containing base name, canonical path, configuration/remote fingerprints, branch, local OID, candidate/trusted remote OIDs, sorted conflict paths, and operation ID; public status is `conflict`; `MERGE_HEAD` and unmerged index entries remain on disk; mutation policy is enabled for that canonical path. Plan 3 may also recover a partially staged index without changing it.

This plan includes:

- Safe parsing of porcelain v2, index stages, rename records, attributes, and checkout-index temporary-file mappings.
- DTOs for text, binary, add/add, modify/delete, and rename/delete conflicts.
- Read-only conflict listing.
- One logical conflict per `local`, `remote`, `manual`, `delete`, or `keep_both` request.
- Complete only after all unmerged entries are gone, then merge commit, reindex, and exact-OID push.
- Abort through `git merge --abort`, restore/reindex, and public `paused` state.
- Local-only startup recovery for partial resolutions, complete/abort checkpoints, locks, foreign operations, and ambiguous states.
- Guarded conflict REST routes and backend tests.

This plan excludes:

- Frontend files, conflict workspace, polling, or editor behavior.
- Autosync scheduling, timers, jitter, five-failure counters, circuit breaker, and resume route.
- Force-push, force-with-lease, reset, rebase, clean, prune, submodule, LFS, or automatic side selection.

## Reused Plan 1-3 Types

Use the landed names directly:

```go
type OperationKind string
type OperationState string

const (
	OperationInitialize OperationKind = "initialize"
	OperationSync       OperationKind = "sync"

	OperationQueued    OperationState = "queued"
	OperationRunning   OperationState = "running"
	OperationSucceeded OperationState = "succeeded"
	OperationFailed    OperationState = "failed"
	OperationConflict  OperationState = "conflict"
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

Plan 4 extends this contract instead of introducing a second journal:

```go
const (
	OperationConflictComplete OperationKind = "conflict_complete"
	OperationConflictAbort    OperationKind = "conflict_abort"

	StageConflictResolving  Stage = "conflict_resolving"
	StageConflictCompleting Stage = "conflict_completing"
	StageConflictCommitted  Stage = "conflict_committed"
	StageConflictReindexed  Stage = "conflict_reindexed"
	StageConflictPushing    Stage = "conflict_pushing"
	StageConflictAborting   Stage = "conflict_aborting"
)
```

`resolve` is synchronous and does not create a new operation row. Git's index records the partial result. `complete` and `abort` use new queued/running operations so their irreversible checkpoints are durable.

Add two methods to the landed journal repository:

```go
func (r *GitOperationRepository) ByID(context.Context, string) (git.Operation, bool, error)
func (r *GitOperationRepository) LatestConflictByPath(context.Context, string) (git.Operation, bool, error)
```

`LatestConflictByPath` selects the newest `state='conflict'` row for the exact canonical path. Both methods follow landed `ActiveByPath` convention and return `found=false, err=nil` for no row. The latest lookup is used only while public status is currently `conflict` and its operation ID references a complete/abort row rather than the original conflict; historical conflict rows are never surfaced from ready/error/paused states.

## Safe Git Command Contract

Reuse landed `Service.porcelain.InspectLocal` for canonical root, absolute Git dir, current branch, and foreign-operation markers. Conflict-specific commands absent from `Porcelain` go through the landed Plan 1 runner as argument slices.

| Purpose | Exact arguments | Scope/input |
|---|---|---|
| Status | `--no-optional-locks status --porcelain=v2 -z --untracked-files=no --renames` | local, no stdin |
| Conflict stages | `ls-files --unmerged --stage --full-name -z` | local, no stdin |
| All tracked stages | `ls-files --stage --full-name -z` | local, no stdin; exact tracked-destination collision map |
| Local OID | `rev-parse --verify HEAD^{commit}` | local, no stdin |
| Merge OID | `rev-parse --verify MERGE_HEAD^{commit}` | local, no stdin |
| Trusted remote OID | `rev-parse --verify refs/igonotes/remotes/<validated-branch>^{commit}` | local, no stdin |
| Merge bases | `merge-base --all HEAD MERGE_HEAD` | local, no stdin |
| Local rename evidence | `diff-tree -r --no-commit-id --name-status -z --find-renames <base-oid> <local-oid>` | local, no stdin |
| Remote rename evidence | `diff-tree -r --no-commit-id --name-status -z --find-renames <base-oid> <remote-oid>` | local, no stdin |
| Stage checkout | `--git-dir=<validated-absolute-git-dir> --work-tree=<fresh-temp-root> --literal-pathspecs checkout-index --temp --stage=<1|2|3> -z --stdin` | local/read-only repository access, command dir=temp root, one final-NUL path |
| Attribute lookup | `check-attr -z --stdin diff` | local, final-NUL paths |
| Hash regular destination | `hash-object --path=<validated-result-path> --stdin` | local, stream complete file bytes through configured clean filters; path is one option value, never a pathspec |
| Hash symlink destination | `hash-object --stdin` | local, stream exact link-target bytes |
| Stage exact paths | `--literal-pathspecs add -A --pathspec-from-file=- --pathspec-file-nul` | local, final-NUL paths |
| Complete merge | `commit --no-edit` | local, no stdin |
| Capture exact merge | `rev-parse --verify HEAD^{commit}` | local, no stdin |
| Verify parents | `rev-list --parents -n 1 <validated-push-oid>` | local, no stdin |
| Capture completed paths | `diff-tree -r --no-commit-id --name-only -z <local-oid> <push-oid>` | local, no stdin |
| Capture abort paths | `diff --name-only -z <local-oid> --` | local, no stdin; before abort |
| Exact push | `push --no-verify --porcelain origin <push-oid>:refs/heads/<validated-branch>` | network, no stdin; only after the worktree transaction returns |
| Advance trusted remote | `update-ref refs/igonotes/remotes/<validated-branch> <push-oid> <trusted-remote-oid>` | local, no stdin; only after confirmed push success |
| Abort | `merge --abort` | local, no stdin |

Safety rules:

- Never parse human-readable status, diff, checkout, merge, or push messages.
- Every path-bearing output uses NUL framing and preserves spaces, tabs, newlines, leading dashes, and pathspec punctuation verbatim.
- `--` is not treated as literal-path protection. Request paths never become command-line pathspec arguments.
- Exact paths are supplied through NUL-delimited stdin; staging also uses global `--literal-pathspecs`.
- OID arguments come only from validated Git output and support 40- or 64-character object formats.
- Stage bytes are materialized with `checkout-index --temp`; this avoids expanding the bounded runner output limit for large binary assets.
- Plan 4 additively extends landed `git.Command` with `Stdin io.Reader`; the runner assigns it directly to `exec.Cmd.Stdin`. Callers keep readers open through `Run`, and fakes consume stdin during the call. No byte copy, log, diagnostic, retry, or persistence of stdin is allowed.
- Stage checkout first creates one mode-0700 `os.MkdirTemp("", "igonotes-conflict-")` root and opens it with `os.OpenRoot`. The returned checkout-index filename must be local to that root; close the root and remove only that exact process-created directory in defer. The configured notes worktree receives no preview temporary file, and restart never scans for or deletes stale temporary directories.
- Worktree output finishes before `git add` clears unmerged entries. A crash remains retryable.
- No code deletes `.git/index.lock`, `MERGE_HEAD`, or another Git state file.
- Push uses exact arguments `push --no-verify --porcelain origin <PushOID>:refs/heads/<validated-branch>` with the journaled `PushOID`, never mutable `HEAD`, `FETCH_HEAD`, or branch source. It runs only after the worktree transaction has returned and released the note/worktree lock, while the Plan 2 coordinator remains held.
- Startup recovery performs no network command. A crash after reindex or during push retains exact `PushOID`, publishes a safe interrupted/push-unknown status with mutations enabled, and lets the next explicit Plan 3 sync fetch and recognize an already accepted commit.

References compatible with minimum Git 2.28:

- <https://git-scm.com/docs/git-status/2.24.0>
- <https://git-scm.com/docs/git-ls-files/2.27.0>
- <https://git-scm.com/docs/git-checkout-index/2.0.5>
- <https://git-scm.com/docs/git-update-index/2.25.1>
- <https://git-scm.com/docs/git-merge/2.27.0>
- <https://git-scm.com/docs/git-push/2.25.0>

## Public API DTOs

Append these types to the landed `internal/model/api.go`:

```go
type GitConflictKind string
type GitConflictContentKind string
type GitConflictAction string

const (
	GitConflictContent      GitConflictKind = "content"
	GitConflictAddAdd       GitConflictKind = "add_add"
	GitConflictModifyDelete GitConflictKind = "modify_delete"
	GitConflictRenameDelete GitConflictKind = "rename_delete"

	GitConflictText   GitConflictContentKind = "text"
	GitConflictBinary GitConflictContentKind = "binary"

	GitConflictUseLocal  GitConflictAction = "local"
	GitConflictUseRemote GitConflictAction = "remote"
	GitConflictManual    GitConflictAction = "manual"
	GitConflictDelete    GitConflictAction = "delete"
	GitConflictKeepBoth  GitConflictAction = "keep_both"
)

type GitConflictStage struct {
	Path             string  `json:"path"`
	OID              string  `json:"oid"`
	Mode             string  `json:"mode"`
	Size             int64   `json:"size"`
	Content          *string `json:"content,omitempty"`
	PreviewTruncated bool    `json:"preview_truncated"`
}

type GitConflict struct {
	ID           string                 `json:"id"`
	Kind         GitConflictKind        `json:"kind"`
	ContentKind  GitConflictContentKind `json:"content_kind"`
	Path         string                 `json:"path"`
	OriginalPath string                 `json:"original_path,omitempty"`
	Base         *GitConflictStage      `json:"base,omitempty"`
	Local        *GitConflictStage      `json:"local,omitempty"`
	Remote       *GitConflictStage      `json:"remote,omitempty"`
	Actions      []GitConflictAction    `json:"actions"`
}

type GitConflictListResponse struct {
	Base         string        `json:"base"`
	OperationID  string        `json:"operation_id"`
	HeadOID      string        `json:"head_oid"`
	MergeHeadOID string        `json:"merge_head_oid"`
	Conflicts    []GitConflict `json:"conflicts"`
	CanComplete  bool          `json:"can_complete"`
}

type GitConflictResolveRequest struct {
	Base        string            `json:"base"`
	OperationID string            `json:"operation_id"`
	ConflictID  string            `json:"conflict_id"`
	Path        string            `json:"path"`
	Action      GitConflictAction `json:"action"`
	ResultPath  string            `json:"result_path,omitempty"`
	Content     *string           `json:"content,omitempty"`
	LocalPath   string            `json:"local_path,omitempty"`
	RemotePath  string            `json:"remote_path,omitempty"`
	LocalOID    string            `json:"local_oid,omitempty"`
	RemoteOID   string            `json:"remote_oid,omitempty"`
}

type GitConflictResolveResponse struct {
	ResolvedPath string                  `json:"resolved_path"`
	Remaining    GitConflictListResponse `json:"remaining"`
}
```

`resolved_path` is the logical conflict's original `GitConflict.Path`; keep-both output names remain the request's `local_path`/`remote_path`. Every list uses non-nil `conflicts` and `actions` arrays.

Conflict ID is `sha256:<lowercase hex>` over these UTF-8 byte fields separated by NUL:

```text
kind, path, original-path, base-oid, local-oid, remote-oid
```

## Classification and Actions

| Porcelain XY | Stages | Rename evidence | Kind |
|---|---|---|---|
| `UU` | 1, 2, 3 | None | `content` |
| `AA` | 2, 3 | None | `add_add` |
| `UD` | 1, 2 | None | `modify_delete`, local retained |
| `DU` | 1, 3 | None | `modify_delete`, remote retained |
| `UD` | 1, 2 | Local `R<score> old new`, remote `D old` | `rename_delete`, local renamed |
| `DU` | 1, 3 | Remote `R<score> old new`, local `D old` | `rename_delete`, remote renamed |

Any other unmerged XY/stage combination, conflicting rename evidence, rename correlation with multiple merge bases, copy conflict, directory/file conflict, or gitlink mode `160000` returns `ErrConflictStateAmbiguous`. No side is selected and mutation policy remains enabled.

Content classification order:

1. `check-attr` reports `diff` as `unset`: binary.
2. Any present stage preview contains NUL: binary.
3. Any present stage preview is invalid UTF-8: binary.
4. Otherwise: text.

`GitConflictStage.size` is the complete materialized worktree-byte size after Git checkout filters. Text previews include complete content through 1 MiB per stage. Larger valid text remains `text` with omitted content and `preview_truncated: true`. Resolution still streams the complete selected stage from checkout-index's temporary file.

| Conflict | Text actions | Binary actions |
|---|---|---|
| Content | `local`, `remote`, `manual` | `local`, `remote`, `keep_both` |
| Add/add | `local`, `remote`, `manual`, `keep_both` | `local`, `remote`, `keep_both` |
| Modify/delete | Existing side, `manual`, `delete` | Existing side, `delete` |
| Rename/delete | Existing side, `manual`, `delete` | Existing side, `delete` |

Every resolve requires the listed `operation_id`, conflict ID, and logical path. `local` and `remote` require `result_path` and the matching current OID. `manual` requires `result_path` and non-nil `content`. `keep_both` requires distinct local/remote paths and both current OIDs. `delete` requires no output path. Reject nonzero action-specific fields that do not belong to the selected action rather than silently ignoring them.

## File Map

| Action | Exact path | Responsibility |
|---|---|---|
| Modify | `internal/model/api.go` | Public conflict DTOs |
| Create | `internal/model/git_conflict_test.go` | JSON contract tests |
| Modify | `internal/git/operation.go` | Complete/abort kinds and checkpoint stages |
| Modify | `internal/git/errors.go` | Fixed safe conflict error codes/sentinels |
| Modify | `internal/git/runner.go` | Add streaming stdin to the sole command boundary |
| Modify | `internal/git/runner_test.go` | Stdin forwarding/non-exposure regression tests |
| Modify | `internal/git/service_test.go` | Service-helper stdin forwarding test |
| Create | `internal/git/conflict_errors_test.go` | Safe code/message/field tests |
| Create | `internal/git/conflict_parser.go` | Pure NUL parsers |
| Create | `internal/git/conflict_parser_test.go` | Parser and unusual-path tests |
| Create | `internal/git/conflict.go` | Read-only conflict inspection/classification |
| Create | `internal/git/conflict_test.go` | Fake-runner read-service tests |
| Create | `internal/git/conflict_resolution.go` | One-path resolution planning/application |
| Create | `internal/git/conflict_resolution_test.go` | Validation, collisions, modes, literal staging |
| Create | `internal/git/conflict_integration_test.go` | Real conflict fixtures/lifecycles |
| Modify | `internal/git/recovery.go` | Conflict complete/abort local checkpoint recovery |
| Modify | `internal/git/recovery_test.go` | Partial and ambiguous restart tests |
| Modify | `internal/git/service.go` | Conflict service entry points using landed runner/worktree callback |
| Modify | `internal/repository/db.go` | Migration extending operation kind CHECK constraint |
| Modify | `internal/repository/git_operation_repo.go` | By-ID/latest-conflict queries |
| Modify | `internal/repository/git_operation_repo_test.go` | Migration/query/transition tests |
| Modify | `internal/service/git_manager.go` | Serialized list/resolve/complete/abort/recovery |
| Modify | `internal/service/git_manager_test.go` | Queue, status, journal, policy, concurrency tests |
| Create | `internal/handlers/git_conflict_handler.go` | Focused conflict handlers over the manager seam |
| Create | `internal/handlers/git_conflict_handler_test.go` | Request/response/redaction tests |
| Modify | `internal/handlers/errors.go` | Conflict HTTP mappings |
| Modify | `internal/handlers/errors_test.go` | Mapping tests |
| Modify | `internal/handlers/git_routes.go` | Four guarded conflict routes |
| Modify | `internal/handlers/git_routes_test.go` | Methods/setup/origin guards |
| Modify | `cmd/api/main.go` | Reuse Plan 3 local recovery before serving |
| Modify | `cmd/api/git_runtime_test.go` | Conflict recovery ordering/no-network tests |
| Modify | `docs/api.md` | Conflict API documentation |

Do not modify `internal/service/note_service.go`, `internal/service/base_operation_coordinator.go`, or `internal/service/note_filesystem_transaction.go`; Plan 4 consumes those landed Plan 2 extension points unchanged.

## Parallel Dispatch Waves

| Wave | Tasks | Rule |
|---|---|---|
| Wave 0 | Task 1 | Serial contract and migration freeze |
| Wave 1A | Tasks 2-3 | Parser, then read-only service inspection |
| Wave 1B | Task 4 | API models/handler methods against the frozen narrow conflict interface |
| Wave 1C | Task 5 | Test-only integration fixtures |
| Wave 2 | Tasks 6-10 | Strictly serial resolution/complete/abort/manager recovery transitions |
| Wave 3 | Tasks 11-13 | Routes, startup, lifecycle, docs, verification |

Wave 1 workers use isolated worktrees and exclusive files. Merge all three lanes before Task 6. Tasks 6-10 may not run concurrently because they change service and manager transition ordering.

### Task 1: Freeze Conflict Contracts and Journal Schema

**Files:**
- Modify: `internal/git/operation.go`
- Modify: `internal/git/errors.go`
- Modify: `internal/git/runner.go`
- Modify: `internal/git/runner_test.go`
- Modify: `internal/git/service.go`
- Modify: `internal/git/service_test.go`
- Create: `internal/git/conflict_errors_test.go`
- Modify: `internal/repository/db.go`
- Modify: `internal/repository/git_operation_repo.go`
- Modify: `internal/repository/git_operation_repo_test.go`

- [ ] **Step 1: Run the Plan 3 baseline**

Run:

```bash
go test ./internal/git ./internal/repository ./internal/service ./internal/handlers ./cmd/api
```

Expected: PASS before Plan 4 changes.

- [ ] **Step 2: Write RED migration and query tests**

Add exact tests:

```go
func TestGitOperationMigrationAcceptsConflictCompleteAndAbort(t *testing.T)
func TestGitOperationRepositoryByID(t *testing.T)
func TestGitOperationRepositoryLatestConflictByPath(t *testing.T)
func TestGitOperationRepositoryKeepsOneActivePathAcrossNewKinds(t *testing.T)
func TestGitConflictSafeErrors(t *testing.T)
func TestCommandRunnerForwardsStdinWithoutExposingIt(t *testing.T)
func TestServiceRunForwardsStdin(t *testing.T)
```

The migration test opens a database at the Plan 3 schema, inserts a historical sync/conflict row with distinct configuration/remote fingerprints, candidate/trusted/push OIDs, changed/conflict paths, and all public safe-error fields, upgrades, and verifies every value unchanged before creating queued complete and abort rows. The unique active-path index must still reject two queued/running rows for one canonical path. The error test asserts every code/message below and verifies a field-bearing validation error exposes only its safe field.

- [ ] **Step 3: Run contract/repository tests RED**

Run:

```bash
go test ./internal/git ./internal/repository -run 'Test(CommandRunnerForwardsStdin|ServiceRunForwardsStdin|GitConflictSafeErrors|GitOperation(Migration|Repository))' -count=1 -v
```

Expected: FAIL because streaming stdin, new operation kinds, safe errors, and query methods do not exist.

- [ ] **Step 4: Add operation kinds and stages**

Append the constants from “Reused Plan 1-3 Types” to `internal/git/operation.go`. Reuse the landed `model.GitStatePaused` solely for user abort; do not add a second public state, failure counters, or resume behavior.

Append these fixed safe codes and sentinels to `internal/git/errors.go`:

```go
const (
	CodeConflictNotFound    ErrorCode = "git_conflict_not_found"
	CodeConflictStale       ErrorCode = "git_conflict_stale"
	CodeConflictUnresolved  ErrorCode = "git_conflict_unresolved"
	CodeConflictUnsupported ErrorCode = "git_conflict_unsupported"
	CodeMergeNotInProgress  ErrorCode = "git_merge_not_in_progress"
	CodeRecoveryRequired    ErrorCode = "git_recovery_required"
	CodePaused              ErrorCode = "git_paused"
)

var (
	ErrConflictNotFound = &SafeError{
		Code: CodeConflictNotFound, Message: "Git conflict was not found",
	}
	ErrConflictStale = &SafeError{
		Code: CodeConflictStale, Message: "Git conflict changed; refresh and try again",
	}
	ErrConflictUnresolved = &SafeError{
		Code: CodeConflictUnresolved, Message: "Git conflict still has unresolved paths",
	}
	ErrConflictUnsupported = &SafeError{
		Code: CodeConflictUnsupported, Message: "Git conflict type is unsupported",
	}
	ErrMergeNotInProgress = &SafeError{
		Code: CodeMergeNotInProgress, Message: "Git merge is not in progress",
	}
	ErrRecoveryRequired = &SafeError{
		Code: CodeRecoveryRequired, Message: "Git repository requires recovery",
	}
	ErrGitPaused = &SafeError{
		Code: CodePaused, Message: "Git synchronization is paused",
	}
	ErrConflictStateAmbiguous = ErrRecoveryRequired
)

func newConflictFieldError(message, field string) *SafeError {
	return &SafeError{
		Code: CodeConflictUnsupported, Message: message, Field: field,
	}
}
```

Never mutate a package sentinel. Path/action validation that needs a field creates a fresh error through `newConflictFieldError`; identity changes return `ErrConflictStale`.

Add the one backward-compatible field to the landed command contract and assign it before process start:

```go
type Command struct {
	Dir      string
	Args     []string
	Scope    OperationScope
	ReadOnly bool
	Secrets  []string
	Stdin    io.Reader
}

cmd.Stdin = command.Stdin
```

The regression command reads stdin and returns its SHA-256, while the input contains a unique secret marker. Assert the digest is correct and the marker appears in neither `Result` nor returned errors; the command spy records only `Dir`, `Args`, and scope metadata, never reader contents.

Extend Plan 3's one private `Service.run` helper with an `io.Reader` parameter and have existing `runLocal`/`runNetwork` wrappers pass nil. Add only this stdin wrapper for Plan 4 commands:

```go
func (s *Service) runLocalInput(
	ctx context.Context,
	dir string,
	readOnly bool,
	stdin io.Reader,
	args ...string,
) (Result, error) {
	return s.run(ctx, dir, LocalOperation, readOnly, nil, stdin, args...)
}
```

`Service.run` still clones args/secrets and constructs the sole landed `Command`; it now assigns `Stdin: stdin`. Do not create a second runner interface or bypass `Service.run`.

- [ ] **Step 5: Rebuild the SQLite operation table in the next migration**

Use the existing ordered migration framework. In one transaction: rename `git_operations` to `git_operations_plan3`; drop `git_operations_one_active_path` and `git_operations_unfinished`; create `git_operations` with the exact Plan 3 columns/defaults plus this kind check; copy all columns in their declared order; drop `git_operations_plan3`; recreate both Plan 3 indexes:

```sql
ALTER TABLE git_operations RENAME TO git_operations_plan3;
DROP INDEX git_operations_one_active_path;
DROP INDEX git_operations_unfinished;

CREATE TABLE git_operations (
    operation_id TEXT PRIMARY KEY,
    base_name TEXT NOT NULL,
    repo_path TEXT NOT NULL,
    config_fingerprint TEXT NOT NULL,
    remote_fingerprint TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('initialize', 'sync', 'conflict_complete', 'conflict_abort')),
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

INSERT INTO git_operations (
    operation_id, base_name, repo_path, config_fingerprint, remote_fingerprint, kind, state, stage, branch,
    backup_ref, local_oid, candidate_oid, remote_oid, push_oid, changed_paths_json,
    conflict_paths_json, error_code, error_message, error_field, error_exit_code,
    created_at, updated_at
)
SELECT
    operation_id, base_name, repo_path, config_fingerprint, remote_fingerprint, kind, state, stage, branch,
    backup_ref, local_oid, candidate_oid, remote_oid, push_oid, changed_paths_json,
    conflict_paths_json, error_code, error_message, error_field, error_exit_code,
    created_at, updated_at
FROM git_operations_plan3;

DROP TABLE git_operations_plan3;

CREATE UNIQUE INDEX git_operations_one_active_path
ON git_operations(repo_path)
WHERE state IN ('queued', 'running');

CREATE INDEX git_operations_unfinished
ON git_operations(state, updated_at)
WHERE state IN ('queued', 'running');
```

Preserve operation IDs, configuration/remote fingerprints, timestamps, candidate/trusted/push OIDs, changed/conflict-path JSON, all public safe-error fields, and terminal states exactly.

- [ ] **Step 6: Implement journal queries**

Add the two declared methods. `ByID` requires exact ID. `LatestConflictByPath` uses:

```sql
WHERE repo_path = ? AND state = 'conflict'
ORDER BY updated_at DESC, operation_id DESC
LIMIT 1
```

Reuse the repository's existing row scanner and `found` boolean convention.

- [ ] **Step 7: Run repository tests GREEN**

Run:

```bash
gofmt -w internal/git/operation.go internal/git/errors.go internal/git/runner.go internal/git/runner_test.go internal/git/service.go internal/git/service_test.go internal/git/conflict_errors_test.go internal/repository/db.go internal/repository/git_operation_repo.go internal/repository/git_operation_repo_test.go
go test ./internal/git ./internal/repository -run 'Test(CommandRunnerForwardsStdin|ServiceRunForwardsStdin|GitConflictSafeErrors|GitOperation(Migration|Repository)|MetadataMigration)' -count=1 -v
```

Expected: PASS; historical rows survive and new active kinds obey one-active-path deduplication.

- [ ] **Step 8: Commit contract freeze**

```bash
git add internal/git/operation.go internal/git/errors.go internal/git/runner.go internal/git/runner_test.go internal/git/service.go internal/git/service_test.go internal/git/conflict_errors_test.go internal/repository/db.go internal/repository/git_operation_repo.go internal/repository/git_operation_repo_test.go
git commit -m "feat: extend git conflict operation journal"
```

### Task 2: Parse NUL-Delimited Conflict Records

**Files:**
- Create: `internal/git/conflict_parser.go`
- Create: `internal/git/conflict_parser_test.go`

- [ ] **Step 1: Write RED parser tests**

Define internal records:

```go
type porcelainEntry struct {
	RecordType   byte
	XY           string
	Path         string
	OriginalPath string
}

type indexStage struct {
	Path  string
	Mode  string
	OID   string
	Stage int
}

type nameStatus struct {
	Status  byte
	Score   int
	OldPath string
	Path    string
}

type attributeRecord struct {
	Path  string
	Name  string
	Value string
}

type checkoutTemp struct {
	TempPath string
	Path     string
}
```

Test porcelain type `1`, type `2`, and `u`; index stages 0/1/2/3; `M`, `D`, `R100`, `C100`; attribute triples; and checkout-index `temp<TAB>path<NUL>`. Include mixed ordinary/unmerged status plus spaces, tab, newline, leading `-`, and `:(glob)*.md`. Reject missing final NUL, missing metadata tab, bad mode, stage outside 0-3, bad OID, duplicate stage, truncated rename/copy, invalid score, and unknown record type. Add `func FuzzConflictParsers(*testing.F)` with one valid and one truncated seed for every parser.

- [ ] **Step 2: Run parser tests RED**

Run:

```bash
go test ./internal/git -run 'TestParse(Porcelain|IndexStages|NameStatus|Attributes|CheckoutTemp)' -count=1 -v
```

Expected: FAIL because parser functions do not exist.

- [ ] **Step 3: Implement shared NUL framing**

```go
var ErrOutputMalformed = errors.New("malformed Git output")

func splitNULTerminated(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != 0 {
		return nil, fmt.Errorf("%w: output is not NUL terminated", ErrOutputMalformed)
	}
	return bytes.Split(data[:len(data)-1], []byte{0}), nil
}
```

Parser errors remain internal diagnostics. `Service` converts them to fixed `ErrRecoveryRequired`/`ErrConflictUnsupported` values before manager or HTTP boundaries; malformed output text is never persisted or serialized.

- [ ] **Step 4: Implement all five parsers**

Expose:

```go
func parsePorcelainV2Z([]byte) ([]porcelainEntry, error)
func parseIndexStagesZ([]byte) ([]indexStage, error)
func parseNameStatusZ([]byte) ([]nameStatus, error)
func parseCheckAttrZ([]byte) ([]attributeRecord, error)
func parseCheckoutTempZ([]byte) ([]checkoutTemp, error)
```

Porcelain type `1` uses `bytes.SplitN(record, []byte{' '}, 9)`, type `u` uses 11, and type `2` consumes the next NUL token as original path; ordinary records remain available for validation but conflict grouping ignores them. Index parsing splits only at the first tab and validates six octal mode digits, stage 0-3, and 40/64 hex OIDs. The unmerged caller separately requires stages 1-3, while the all-index caller uses stage 0 to reject tracked destinations. Rename status consumes old/new tokens. Attributes consume triples. Checkout temp splits only at its first tab and validates the returned temp path with `filepath.IsLocal`; the checkout helper additionally requires exactly one mapping whose reported source path byte-matches the requested path.

- [ ] **Step 5: Run parser tests GREEN**

Run:

```bash
gofmt -w internal/git/conflict_parser.go internal/git/conflict_parser_test.go
go test ./internal/git -run 'TestParse(Porcelain|IndexStages|NameStatus|Attributes|CheckoutTemp)' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Run parser fuzz seeds**

Run:

```bash
go test ./internal/git -run '^$' -fuzz=FuzzConflictParsers -fuzztime=5s
```

Expected: PASS without a panic.

- [ ] **Step 7: Commit parser lane**

```bash
git add internal/git/conflict_parser.go internal/git/conflict_parser_test.go
git commit -m "feat: parse git conflict records safely"
```

### Task 3: Build Read-Only Conflict Inspection

**Files:**
- Create: `internal/git/conflict.go`
- Create: `internal/git/conflict_test.go`
- Modify: `internal/git/service.go`

- [ ] **Step 1: Write RED command-sequence tests**

The landed porcelain fake must receive `InspectLocal` once; its canonical root, absolute Git dir, current branch, and pending-operation result are authoritative. The landed runner fake must observe the remaining exact read commands in “Safe Git Command Contract”, no network class, and no repository/index mutation. Add a truncated-stdout case that stops before parsing and returns a fixed recovery-required error.

- [ ] **Step 2: Run command tests RED**

Run:

```bash
go test ./internal/git -run TestConflictReaderCommands -count=1 -v
```

Expected: FAIL because conflict inspection is absent.

- [ ] **Step 3: Add conflict service types**

```go
type ConflictKind string
type ContentKind string

const (
	ConflictContent      ConflictKind = "content"
	ConflictAddAdd       ConflictKind = "add_add"
	ConflictModifyDelete ConflictKind = "modify_delete"
	ConflictRenameDelete ConflictKind = "rename_delete"

	ContentText   ContentKind = "text"
	ContentBinary ContentKind = "binary"
)

type ConflictStage struct {
	Path             string
	OID              string
	Mode             string
	Size             int64
	Content          *string
	PreviewTruncated bool
}

type Conflict struct {
	ID           string
	Kind         ConflictKind
	ContentKind  ContentKind
	Path         string
	OriginalPath string
	Base         *ConflictStage
	Local        *ConflictStage
	Remote       *ConflictStage
	Actions      []string
}

type ConflictSnapshot struct {
	HeadOID       string
	MergeHeadOID  string
	MergeBaseOIDs []string
	Conflicts     []Conflict
	CanComplete   bool
}

func (s *Service) Conflicts(context.Context, ConfiguredBase, Operation) (ConflictSnapshot, error)
func (s *Service) checkoutConflictStage(context.Context, string, string, string, int) (string, error)
```

The checkout helper string arguments are validated absolute Git dir, fresh temporary worktree root, and literal index path, in that order.

- [ ] **Step 4: Group porcelain and stage records**

Before parsing any `Result.Stdout`, reject `StdoutTruncated`; never interpret a truncated NUL or OID stream. Require `InspectLocal` to report the exact configured canonical root/branch, merge as the sole pending operation, and no repository lock/foreign marker. Require one porcelain `u` record for each unmerged stage path, no duplicate stage, current `HEAD == operation.LocalOID`, and `operation.CandidateOID == operation.RemoteOID == managed trusted ref == MERGE_HEAD`. Zero merge bases is accepted only when every entry is add/add with stages 2/3, as produced by a confirmed unrelated-history merge; otherwise it is ambiguous. Multiple bases are acceptable only when rename correlation is unnecessary.

- [ ] **Step 5: Write RED classification tests**

Cover every row in “Classification and Actions”, unrelated-history add/add without a merge base, both rename/delete orientations, conflicting rename evidence, multiple-base rename ambiguity, invalid-UTF-8 paths, and gitlink rejection.

- [ ] **Step 6: Implement classification and rename correlation**

Require valid UTF-8 for every path before constructing JSON-visible identities; unsupported byte paths return `ErrConflictUnsupported` without lossy conversion. Rename/delete requires exactly one side `R<score> old new`, opposite side `D old`, unmerged path `new`, and matching `UD`/`DU` stages. Set `Path=new` and `OriginalPath=old`.

- [ ] **Step 7: Write RED preview tests**

Cover `diff=unset`, NUL, invalid UTF-8, empty text, normal UTF-8, and valid text larger than 1 MiB. Assert each checkout temp exists only under the injected system temp parent during inspection and no `.merge_*` entry appears in the configured notes worktree before or after the call.

- [ ] **Step 8: Implement stage checkout and previews**

Use the absolute Git dir returned by landed `InspectLocal`, create one private system temp worktree root for the inspection call, and defer closing/removing that exact root. For each stage/path, run checkout-index against that Git dir/temp worktree, parse its exact mapping, open the returned local filename through the temp `os.Root`, read at most 1 MiB plus one byte, and stat full size. Query attributes once for all paths. Never write a temp file into the configured base and never return binary bytes.

- [ ] **Step 9: Implement stable ID/order and service verification**

Sort by path/original path and hash the documented identity. Return a non-nil empty conflict slice with completion eligibility when all entries are stage 0 but `MERGE_HEAD` remains.

- [ ] **Step 10: Run read-service tests GREEN**

Run:

```bash
gofmt -w internal/git/conflict.go internal/git/conflict_test.go internal/git/service.go
go test ./internal/git -run 'TestConflict(Reader|Classifies|Preview|Identity|Service)' -count=1 -v
```

Expected: PASS.

- [ ] **Step 11: Commit read-only lane**

```bash
git add internal/git/conflict.go internal/git/conflict_test.go internal/git/service.go
git commit -m "feat: inspect git conflicts"
```

### Task 4: Add API DTOs and Handler Methods

**Files:**
- Modify: `internal/model/api.go`
- Create: `internal/model/git_conflict_test.go`
- Create: `internal/handlers/git_conflict_handler.go`
- Create: `internal/handlers/git_conflict_handler_test.go`

- [ ] **Step 1: Write RED DTO JSON tests**

Marshal text and binary/add-add examples and assert `operation_id`, `content_kind`, optional content, truncation, actions, original path, and nil stages exactly.

- [ ] **Step 2: Run DTO tests RED**

Run:

```bash
go test ./internal/model -run TestGitConflictJSONContract -count=1 -v
```

Expected: FAIL because conflict API types are undefined.

- [ ] **Step 3: Add the public DTO declarations**

Append the declarations from “Public API DTOs” to `internal/model/api.go`.

- [ ] **Step 4: Add a narrow conflict handler seam**

Keep Plan 3's concrete manager for existing methods and add only this field for parallel conflict handler tests:

```go
type gitConflictManager interface {
	ListConflicts(context.Context, string) (model.GitConflictListResponse, error)
	ResolveConflict(context.Context, model.GitConflictResolveRequest) (model.GitConflictResolveResponse, error)
	QueueConflictComplete(context.Context, string) (git.Operation, bool, error)
	QueueConflictAbort(context.Context, string) (git.Operation, bool, error)
}

type GitConflictHandler struct {
	manager gitConflictManager
}

func NewGitConflictHandler(manager gitConflictManager) *GitConflictHandler {
	return &GitConflictHandler{manager: manager}
}
```

The concrete landed `*service.GitManager` satisfies this interface after Task 11. Keep Plan 3's `GitHandler` and `GitOperations` interface unchanged so the Wave 1 API lane has exclusive files and no existing handler contract grows.

- [ ] **Step 5: Write RED handler tests**

Cover missing base, malformed/multiple JSON, missing operation/conflict/path/action identity fields, unknown action, manual without content, keep-both without paths, leading-dash forwarding, `200` list/resolve, `202` complete/abort, and private error redaction.

- [ ] **Step 6: Implement handler methods**

Add:

```go
func (h *GitConflictHandler) List(http.ResponseWriter, *http.Request)
func (h *GitConflictHandler) Resolve(http.ResponseWriter, *http.Request)
func (h *GitConflictHandler) Complete(http.ResponseWriter, *http.Request)
func (h *GitConflictHandler) Abort(http.ResponseWriter, *http.Request)
```

Use existing `decodeSingleJSON`, `writeJSON`, and `writeServiceError`. Validate required JSON presence/action enum in the handler; repository identity/path/collision belongs to engine/manager. Complete/abort convert the returned operation plus deduplication boolean to the same `202` operation JSON shape used by Plan 3 manual sync.

- [ ] **Step 7: Run DTO/handler tests GREEN**

Run:

```bash
gofmt -w internal/model/api.go internal/model/git_conflict_test.go internal/handlers/git_conflict_handler.go internal/handlers/git_conflict_handler_test.go
go test ./internal/model ./internal/handlers -run 'TestGitConflictJSONContract|TestGitConflictHandler' -count=1 -v
```

Expected: PASS with the handler test seam.

- [ ] **Step 8: Commit API lane**

```bash
git add internal/model/api.go internal/model/git_conflict_test.go internal/handlers/git_conflict_handler.go internal/handlers/git_conflict_handler_test.go
git commit -m "feat: add git conflict api contracts"
```

### Task 5: Add Real Conflict Fixtures

**Files:**
- Create: `internal/git/conflict_integration_test.go`

- [ ] **Step 1: Add bare/two-device fixture**

```go
type conflictFixture struct {
	Remote string
	Local  string
	Other  string
	Branch string
}

func newConflictFixture(t *testing.T) conflictFixture
func runConflictGit(t *testing.T, dir string, args ...string) []byte
```

Add helpers to commit both devices, push other, fetch private ref, and merge captured OID. Use argument slices and local test identity; every fixture push uses `git push --no-verify`, including `git push --no-verify origin HEAD:refs/heads/<branch>` for the other device.

- [ ] **Step 2: Add fixture smoke RED/GREEN test**

Run:

```bash
go test ./internal/git -run TestConflictFixtureCreatesUnmergedIndex -count=1 -v
```

Expected: PASS with non-empty parsed unmerged stages.

- [ ] **Step 3: Add all conflict forms**

Create text/binary `UU`, text/binary `AA`, both modify/delete orientations, both rename/delete orientations, and multiple simultaneous paths. Assert exact stages and rename records.

- [ ] **Step 4: Add literal filenames**

Use spaces, brackets, and leading `-` on every supported system. Use tab/newline only after a test-local create/remove capability probe (required on Unix, skipped with the probe reason on filesystems that reject control characters). On non-Windows also use `:(glob)*.md` beside `victim.md`.

- [ ] **Step 5: Run fixture matrix**

Run:

```bash
go test ./internal/git -run 'TestConflictFixture' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Commit fixture lane**

```bash
git add internal/git/conflict_integration_test.go
git commit -m "test: add git conflict fixtures"
```

### Task 6: Validate and Plan One Resolution

**Files:**
- Create: `internal/git/conflict_resolution.go`
- Create: `internal/git/conflict_resolution_test.go`

- [ ] **Step 1: Write RED path and stale-identity tests**

Reject empty, NUL, absolute, `.`, escaping `..`, and `.git` components. Accept literal unusual names. Reject changed operation ID/conflict ID/path/local OID/remote OID, missing selected side, unsupported action, and missing action fields.

- [ ] **Step 2: Run validation tests RED**

Run:

```bash
go test ./internal/git -run 'TestCleanConflictPath|TestValidateResolution' -count=1 -v
```

Expected: FAIL because validation functions are absent.

- [ ] **Step 3: Implement validation**

```go
func cleanConflictPath(value, field string) (string, error)
func validateResolution(Conflict, model.GitConflictResolveRequest) error
```

Require `path.Clean(value) == value`, then use `filepath.IsLocal(filepath.FromSlash(value))`; reject NUL, empty/dot components, and any component equal-folding to `.git`. Preserve valid separators/bytes rather than rewriting the request. Return fresh `newConflictFieldError` values for request fields and `ErrConflictStale` for identity mismatch.

- [ ] **Step 4: Write RED resolution plan table**

```go
type resolutionWrite struct {
	Path  string
	Stage int
	OID   string
	Mode  string
	Data  []byte
}

type resolutionPlan struct {
	RemovePaths []string
	Writes      []resolutionWrite
	StagePaths  []string
}
```

Cover every valid action and source/output set.

- [ ] **Step 5: Implement deterministic planning**

`local`/`remote` select stage 2/3; manual selects local mode, then remote, then `100644`; delete removes primary/original; keep-both writes both stages to distinct explicit paths. Source paths not reused are removed. Reject any requested output/source set that overlaps another logical conflict's path or original path, so one request cannot clear another unmerged entry. Stage paths are deduplicated and bytewise sorted.

- [ ] **Step 6: Run plan tests GREEN**

Run:

```bash
gofmt -w internal/git/conflict_resolution.go internal/git/conflict_resolution_test.go
go test ./internal/git -run 'TestCleanConflictPath|TestValidateResolution|TestBuildResolutionPlan' -count=1 -v
```

Expected: PASS.

- [ ] **Step 7: Commit resolution planning**

```bash
git add internal/git/conflict_resolution.go internal/git/conflict_resolution_test.go
git commit -m "feat: validate git conflict resolutions"
```

### Task 7: Apply Local, Remote, Manual, and Delete

**Files:**
- Modify: `internal/git/conflict_resolution.go`
- Modify: `internal/git/conflict_resolution_test.go`
- Modify: `internal/git/service.go`
- Modify: `internal/git/conflict_integration_test.go`

- [ ] **Step 1: Write RED checkout/write/mode tests**

Test checkout-index uses `LocalOperation`, `ReadOnly:true`, validated `--git-dir`, the fresh temp `--work-tree`, temp command directory/stdin, temp mapping/path validation, modes `100644`, `100755`, `120000`, existing symlink replacement without following, parent creation, and `160000` rejection.

- [ ] **Step 2: Implement stage materialization**

```go
func writeConflictEntry(*os.Root, string, string, io.Reader) error
```

Reuse Task 3's stage checkout with one fresh private temp root for the resolution call. Open stage sources through the temp `os.Root` and destinations through a separate configured-base `os.Root`; copy full bytes, apply regular/symlink mode, and remove only the exact temp root in defer. New output paths use exclusive creation and never truncate an entry that appeared after destination verification; retry-safe matching outputs skip the content rewrite, with executable-bit correction allowed for regular files. Only conflict-owned source paths may be deliberately replaced or removed.

- [ ] **Step 3: Write RED literal staging test**

Require `LocalOperation`, `ReadOnly:false`, exact arguments, and final-NUL stdin:

```go
[]string{
	"--literal-pathspecs",
	"add",
	"-A",
	"--pathspec-from-file=-",
	"--pathspec-file-nul",
}
```

- [ ] **Step 4: Implement `Service.ResolveConflict`**

```go
func (s *Service) ResolveConflict(
	ctx context.Context,
	snapshot ConfiguredBase,
	operation Operation,
	request model.GitConflictResolveRequest,
	transaction WorktreeTransaction,
) (ConflictSnapshot, error)
```

Sequence: read identity; validate; plan; enter landed worktree transaction; re-read HEAD/MERGE_HEAD/stages; open `os.Root` from transaction canonical path; verify destinations; materialize/write/remove; stage exact NUL paths once; re-read snapshot; leave mutation policy unchanged. Worktree transaction performs its landed reindex behavior before unlock.

- [ ] **Step 5: Run unit resolution tests**

Run:

```bash
gofmt -w internal/git/conflict_resolution.go internal/git/conflict_resolution_test.go internal/git/service.go
go test ./internal/git -run 'TestCheckoutConflictStage|TestWriteConflictEntry|TestResolveConflictUsesLiteralPaths' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Run real local/remote/manual/delete tests**

Run:

```bash
go test ./internal/git -run 'TestResolve(Local|Remote|Manual|Delete)Integration' -count=1 -v
```

Expected: PASS and the resolved logical path has no unmerged entry.

- [ ] **Step 7: Commit core resolution**

```bash
git add internal/git/conflict_resolution.go internal/git/conflict_resolution_test.go internal/git/service.go internal/git/conflict_integration_test.go
git commit -m "feat: resolve git conflicts one path at a time"
```

### Task 8: Apply Keep-Both and Rename Paths Without Overwrite

**Files:**
- Modify: `internal/git/conflict_resolution.go`
- Modify: `internal/git/conflict_resolution_test.go`
- Modify: `internal/git/conflict_integration_test.go`

- [ ] **Step 1: Write RED collision/idempotency tests**

Cover equal output paths, unrelated tracked collision even with matching bytes, untracked collision, owned source reuse, untracked retry with matching selected blob, retry with differing bytes, truncated all-index output, and pathspec-looking output.

- [ ] **Step 2: Implement destination verification**

```go
func verifyResolutionDestination(
	context.Context,
	*os.Root,
	Runner,
	string,
	resolutionWrite,
	map[string]struct{},
) error
```

The string is the canonical repository path. Load and NUL-parse the complete tracked stage-0 map once before destination checks. Missing destination and owned source are safe. Any destination tracked at stage 0 and not owned by this logical conflict collides regardless of bytes. For another untracked existing destination, use root-relative `Lstat`: directories and special files collide; a requested `120000` mode requires an existing symlink and hashes the `Readlink` target bytes without following it; regular modes reject symlinks and stream complete file bytes through `hash-object --path=<validated-result-path> --stdin`, so configured clean filters produce the index identity. Compare with the selected OID, or with the OID first computed from manual `Data` through the same path-aware command. Matching untracked content is retry-safe; any type/content mismatch returns a field conflict without overwrite. Executable-bit correction for matching regular content is allowed only after collision verification inside the transaction.

- [ ] **Step 3: Run collision tests GREEN**

Run:

```bash
go test ./internal/git -run TestResolutionDestination -count=1 -v
```

Expected: PASS.

- [ ] **Step 4: Add add/add keep-both integration test**

Resolve binary add/add to `assets/images/photo-local.png` and `assets/images/photo-remote.png`; assert exact bytes, stage-0 outputs, unused source removal, and no unmerged entry.

- [ ] **Step 5: Add rename/delete integration tests**

For both orientations test retained renamed path, alternate final path, manual text final path, and confirmed deletion of original plus renamed paths.

- [ ] **Step 6: Prove pathspec punctuation is literal**

Resolve `:(glob)*.md` on non-Windows and assert `victim.md` unchanged in worktree and index.

- [ ] **Step 7: Run advanced integration tests**

Run:

```bash
go test ./internal/git -run 'TestResolve(AddAddKeepBoth|RenameDelete|PathspecLiteral)Integration' -count=1 -v
```

Expected: PASS.

- [ ] **Step 8: Commit advanced resolution**

```bash
git add internal/git/conflict_resolution.go internal/git/conflict_resolution_test.go internal/git/conflict_integration_test.go
git commit -m "feat: resolve complex git conflict paths"
```

### Task 9: Complete, Reindex, and Push Exact OID

**Files:**
- Modify: `internal/git/conflict_resolution.go`
- Modify: `internal/git/conflict_resolution_test.go`
- Modify: `internal/git/service.go`
- Modify: `internal/git/conflict_integration_test.go`

- [ ] **Step 1: Write RED completion precondition tests**

Reject missing MERGE_HEAD, remaining unmerged stages, changed operation LocalOID/CandidateOID/RemoteOID or managed trusted ref, detached HEAD, branch mismatch, and foreign Git operation state.

- [ ] **Step 2: Implement preconditions**

```go
func (s *Service) verifyConflictCompletion(context.Context, ConfiguredBase, Operation) error
```

Use structured Git state only and return `ErrConflictUnresolved`, `ErrMergeNotInProgress`, or `ErrRecoveryRequired`.

- [ ] **Step 3: Write RED checkpoint and exact-push tests**

Require stage checkpoints `conflict_completing`, `conflict_committed`, `conflict_reindexed`, `conflict_pushing`, then landed `completed`; assert inspection commands are local/read-only, commit/update-ref are local/mutating, and push is network/mutating. Require exact push arguments `push --no-verify --porcelain origin <PushOID>:refs/heads/<branch>` and reject missing `--no-verify`, mutable HEAD, or branch sources. Block transaction return and prove no push starts; then release it and prove push starts only after the note/worktree lock is released while coordinator exclusion remains held. `completed` is persisted only after push succeeds and the managed trusted-remote ref CASes from operation RemoteOID to PushOID.

- [ ] **Step 4: Implement service completion callbacks**

```go
func (s *Service) CompleteConflict(
	context.Context,
	ConfiguredBase,
	Operation,
	WorktreeTransaction,
	Progress,
) (string, error)
```

Checkpoint completing, then enter the landed worktree transaction. Inside its callback: recheck; run `commit --no-edit`; capture `HEAD^{commit}`; verify exactly two parents equal LocalOID then RemoteOID; capture bytewise sorted/deduplicated NUL changed paths from LocalOID to PushOID; checkpoint committed with PushOID and ChangedPaths. Return from the callback so the landed transaction performs its mandatory reindex before unlock; only after the transaction returns successfully checkpoint reindexed. Outside the worktree lock but still under the coordinator: checkpoint pushing; verify the branch still equals PushOID; run `push --no-verify --porcelain origin <PushOID>:refs/heads/<validated-branch>`. After confirmed push success, CAS `refs/igonotes/remotes/<branch>` from operation RemoteOID to PushOID, then checkpoint landed `StageCompleted` with `CandidateOID=RemoteOID=PushOID` before returning success. A push/CAS uncertainty retains the old trusted OID and journaled PushOID for explicit-sync reconciliation.

- [ ] **Step 5: Run unit completion tests**

Run:

```bash
go test ./internal/git -run 'TestConflictCompletion|TestCompleteConflictPushesExactOID' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Run completion integration tests**

Assert merge parents, exact remote OID, exact NUL-derived changed paths, CAS-updated managed trusted ref/public RemoteOID, rebuilt note index, late worktree edit excluded from pushed commit, and coherent local history after rejection/timeout. Install a failing pre-push hook and prove completion still pushes the exact OID through `--no-verify` only after worktree unlock. Inject crashes after push return and after trusted-ref CAS; recovery performs no network, trusts only the strict local managed-ref/HEAD proof, and otherwise reports push unknown with PushOID retained.

Run:

```bash
go test ./internal/git -run TestCompleteConflictIntegration -count=1 -v
```

Expected: PASS.

- [ ] **Step 7: Commit completion service**

```bash
git add internal/git/conflict_resolution.go internal/git/conflict_resolution_test.go internal/git/service.go internal/git/conflict_integration_test.go
git commit -m "feat: complete resolved git merges"
```

### Task 10: Abort and Extend Local Recovery

**Files:**
- Modify: `internal/git/recovery.go`
- Modify: `internal/git/recovery_test.go`
- Modify: `internal/git/service.go`
- Modify: `internal/git/conflict_integration_test.go`

- [ ] **Step 1: Write RED abort tests**

Require matching HEAD/MERGE_HEAD, pre-abort NUL changed paths, `merge --abort` under worktree transaction, restored local OID, absent MERGE_HEAD, rebuilt index, published refresh paths, and no network command.

- [ ] **Step 2: Implement abort**

```go
func (s *Service) AbortConflict(
	context.Context,
	ConfiguredBase,
	Operation,
	WorktreeTransaction,
	Progress,
) error
```

Checkpoint aborting; verify operation identity; run `merge --abort`; verify MERGE_HEAD absent and HEAD equals LocalOID; return through landed transaction so reindex completes before unlock.

- [ ] **Step 3: Write RED local recovery table**

Cover unresolved merge, partially resolved merge, all paths stage 0 with MERGE_HEAD, committed merge before reindex, reindexed before push, push in progress/unknown, abort completed before journal finish, repository lock, rebase/cherry-pick/revert, unrelated HEAD, and clean repository. Distinguish journal intent explicitly: an original conflict row with MERGE_HEAD gone is ambiguous even when HEAD equals LocalOID; only an unfinished abort operation may classify that state as recovered abort, and only committed/reindexed/pushing complete checkpoints may trust PushOID.

- [ ] **Step 4: Extend Plan 3 local recovery result**

Add decisions:

```go
type ConflictRecoveryState string

const (
	RecoveryConflict       ConflictRecoveryState = "conflict"
	RecoveryCanComplete    ConflictRecoveryState = "can_complete"
	RecoveryNeedsReindex   ConflictRecoveryState = "needs_reindex"
	RecoveryPushPending    ConflictRecoveryState = "push_pending"
	RecoveryPushUnknown    ConflictRecoveryState = "push_unknown"
	RecoveryPushed         ConflictRecoveryState = "pushed"
	RecoveryAborted        ConflictRecoveryState = "aborted"
	RecoveryAmbiguous      ConflictRecoveryState = "ambiguous"
	RecoveryLocked         ConflictRecoveryState = "locked"
)

// Append to the landed RecoveryResult; retain HeadOID, MergeHeadOID,
// RemoteOID, ConflictPaths, and Blocking unchanged.
type RecoveryResult struct {
	HeadOID       string
	MergeHeadOID  string
	RemoteOID     string
	PushOID       string
	ConflictPaths []string
	Blocking      bool
	ConflictState ConflictRecoveryState
}

func (s *Service) RecoverLocal(context.Context, RecoveryOptions) (RecoveryResult, error)
```

Extend the landed method rather than adding a parallel recovery entry point. Recovery remains local-only, preserves partial index entries, deletes nothing, and does not queue work. It uses `RecoveryOptions.Operation` kind plus journal stage to distinguish unresolved, committed, reindexed, pushing, pushed, and aborting checkpoints; repository shape alone never invents completion or abort intent. A complete operation is `RecoveryPushed` only when its PushOID, local HEAD, and managed trusted ref are identical, matching Plan 3's strict post-push crash rule. Other committed/reindexed/pushing checkpoints retain PushOID for the next explicit manual sync.

- [ ] **Step 5: Run abort/recovery tests GREEN**

Run:

```bash
gofmt -w internal/git/recovery.go internal/git/recovery_test.go internal/git/service.go
go test ./internal/git -run 'TestAbortConflict|TestRecoverLocal.*Conflict' -count=1 -v
```

Expected: PASS with zero network commands during recovery.

- [ ] **Step 6: Run restart integration tests**

Cover partial resolution, all staged, crash after merge commit, crash after reindex, crash during push, completed abort before journal finish, unrelated external commit, and untouched index lock.

Run:

```bash
go test ./internal/git -run TestConflictRestartIntegration -count=1 -v
```

Expected: PASS.

- [ ] **Step 7: Commit abort/recovery service**

```bash
git add internal/git/recovery.go internal/git/recovery_test.go internal/git/service.go internal/git/conflict_integration_test.go
git commit -m "feat: abort and recover git conflicts"
```

### Task 11: Integrate Serial Manager Transitions

**Files:**
- Modify: `internal/service/git_manager.go`
- Modify: `internal/service/git_manager_test.go`

- [ ] **Step 1: Write RED manager tests**

Add exact tests for list, synchronous resolve, sequential resolves, resolve during worker job, complete/abort durable queueing, duplicate complete/abort, complete-versus-abort exclusion, unresolved complete, stale configuration fingerprint, status/journal checkpoints, mutation policy, manual sync rejected while abort-paused, path switch/forget serialization, and startup decisions.

- [ ] **Step 2: Run manager tests RED**

Run:

```bash
go test -race ./internal/service -run '^TestGitManager.*Conflict' -count=1 -v
```

Expected: FAIL because conflict manager methods are absent.

- [ ] **Step 3: Implement list and synchronous resolve**

Expose:

```go
func (m *GitManager) ListConflicts(context.Context, string) (model.GitConflictListResponse, error)
func (m *GitManager) ResolveConflict(context.Context, model.GitConflictResolveRequest) (model.GitConflictResolveResponse, error)
```

Resolve the configured immutable snapshot through the landed settings dependency. Submit list/resolve as synchronous jobs to the same manager FIFO and wait on a buffered one-result channel so they cannot overtake or overlap worker jobs; these jobs create no operation row. They retain the caller context, skip execution if it is already canceled, and never block the worker when the caller stops waiting. Under coordinator ownership, first require current public status `model.GitStateConflict`. List loads its referenced original conflict operation, falling back to latest conflict only when the status operation ID is an active complete/abort row; resolve requires `request.OperationID` to equal that current original conflict operation and loads it exactly. Both require operation configuration/remote fingerprints to equal the current snapshot fingerprints. List maps service `ConflictSnapshot`/`Conflict`/`ConflictStage` values to the public DTOs. Resolve builds the same active/inactive `git.WorktreeTransaction` closure used by Plan 3, calls the Git service, updates public conflict paths/stage, and keeps the conflict mutation block set until complete or abort succeeds.

- [ ] **Step 4: Implement durable complete/abort queueing**

Expose:

```go
func (m *GitManager) QueueConflictComplete(context.Context, string) (git.Operation, bool, error)
func (m *GitManager) QueueConflictAbort(context.Context, string) (git.Operation, bool, error)
```

Use Plan 3 FIFO/partial unique index/deduplication and immutable configured snapshot freshness checks. Require current public conflict state and inspect current Git state under coordinator ownership before insertion; never act on a historical conflict row from ready/error/paused state. Complete requires `CanComplete` and stores an empty `ConflictPaths`; abort requires the same HEAD/MERGE_HEAD identity, stores the currently unresolved paths, and captures sorted/deduplicated NUL changed paths versus LocalOID before insertion so restored/resolved/output paths can be refreshed after abort. Each new operation copies base/path/configuration/remote fingerprints/branch/LocalOID/CandidateOID/RemoteOID from the conflict row. A duplicate of the same kind returns the active operation; the opposite kind returns landed `service.ErrGitRepositoryInUse` without modifying either row. Worker checkpoint callback writes `git.Checkpoint` before each irreversible action.

- [ ] **Step 5: Implement terminal status and mutation policy**

After complete reindex succeeds, publish `model.GitStateSyncing`/`StageConflictPushing` and clear the conflict mutation block before network push; the tree/index are coherent and later note edits cannot change the exact PushOID. Complete success finishes operation succeeded and publishes ready with checkpointed changed paths/trusted PushOID. Push failure publishes the existing safe error with the conflict block still cleared. Abort success finishes operation succeeded, publishes `model.GitStatePaused` with the abort operation's captured changed paths, clears the conflict block, and queues no sync. Extend Plan 3 initialize/sync admission to return `git.ErrGitPaused` while that public state remains paused; this plan deliberately adds no resume route. Unresolved/ambiguous/locked state keeps the conflict block set.

- [ ] **Step 6: Extend synchronous startup recovery**

Reuse Plan 3 `RecoverLocal`: load unfinished complete/abort operations and, for a public conflict without one, the exact referenced/latest original conflict row; call `Service.RecoverLocal` with the landed `git.RecoveryOptions`; preserve conflict rows/partial stages; perform required local reindex under coordinator; finish `RecoveryPushed` as succeeded/ready and recovered abort as succeeded/paused; publish interrupted/push-unknown status with PushOID and the conflict block cleared for coherent committed trees; and publish recovery-required/locked with the conflict block set for unsafe ambiguity. Do not access network or enqueue jobs.

- [ ] **Step 7: Run manager tests GREEN**

Run:

```bash
gofmt -w internal/service/git_manager.go internal/service/git_manager_test.go
go test -race ./internal/service -run '^TestGitManager.*Conflict' -count=1 -v
```

Expected: PASS; manager concurrency remains globally one and policy transitions match repository safety.

- [ ] **Step 8: Commit manager transitions**

```bash
git add internal/service/git_manager.go internal/service/git_manager_test.go
git commit -m "feat: orchestrate git conflict transitions"
```

### Task 12: Register Routes, Errors, and Startup Recovery

**Files:**
- Modify: `internal/handlers/git_conflict_handler.go`
- Modify: `internal/handlers/git_conflict_handler_test.go`
- Modify: `internal/handlers/errors.go`
- Modify: `internal/handlers/errors_test.go`
- Modify: `internal/handlers/git_routes.go`
- Modify: `internal/handlers/git_routes_test.go`
- Modify: `cmd/api/main.go`
- Modify: `cmd/api/git_runtime_test.go`

- [ ] **Step 1: Write RED error mapping tests**

Require:

| Safe Git/service error | HTTP | Code |
|---|---:|---|
| Conflict not found | 404 | `git_conflict_not_found` |
| Stale identity | 409 | `git_conflict_stale` |
| Unresolved complete | 409 | `git_conflict_unresolved` |
| Ambiguous/recovery required | 409 | `git_recovery_required` |
| Unsupported conflict | 422 | `git_conflict_unsupported` |
| Merge not in progress | 409 | `git_merge_not_in_progress` |
| Abort-paused sync | 409 | `git_paused` |
| Plan 2 conflict pending | 409 | `git_conflict_pending` |

Public messages contain no path, OID, stderr, command output, or URL.

- [ ] **Step 2: Implement mappings and run GREEN**

Run:

```bash
go test ./internal/handlers -run TestWriteServiceErrorGitConflict -count=1 -v
```

Expected: PASS.

- [ ] **Step 3: Write RED route method/guard tests**

Register:

```text
GET  /api/git/conflicts
PUT  /api/git/conflicts/resolve
POST /api/git/conflicts/complete
POST /api/git/conflicts/abort
```

Wrong methods return `405` with `GET`, `PUT`, `POST`, `POST`; every route uses existing setup and local-origin guards.

- [ ] **Step 4: Extend the landed Git route registration**

Keep `NewRouter` and existing `RegisterGitRoutes` source-compatible. Add the focused registration beside it in `internal/handlers/git_routes.go`:

```go
func RegisterGitConflictRoutes(
	mux *http.ServeMux,
	handler *GitConflictHandler,
	state SetupState,
)
```

Construct and register the production handler in `cmd/api/main.go` after the existing `RegisterGitRoutes` call:

```go
gitConflictHandler := handlers.NewGitConflictHandler(gitManager)
handlers.RegisterGitConflictRoutes(router, gitConflictHandler, settingsService)
```

The new function registers exact paths with the same setup and local-origin guards as landed Git routes:

```go
mux.Handle("/api/git/conflicts", RequireLocalOrigin(methods(map[string]http.Handler{
	http.MethodGet: RequireSetup(state, http.HandlerFunc(handler.List)),
})))
mux.Handle("/api/git/conflicts/resolve", RequireLocalOrigin(methods(map[string]http.Handler{
	http.MethodPut: RequireSetup(state, http.HandlerFunc(handler.Resolve)),
})))
mux.Handle("/api/git/conflicts/complete", RequireLocalOrigin(methods(map[string]http.Handler{
	http.MethodPost: RequireSetup(state, http.HandlerFunc(handler.Complete)),
})))
mux.Handle("/api/git/conflicts/abort", RequireLocalOrigin(methods(map[string]http.Handler{
	http.MethodPost: RequireSetup(state, http.HandlerFunc(handler.Abort)),
})))
```

- [ ] **Step 5: Run handler/router tests GREEN**

Run:

```bash
gofmt -w internal/handlers/git_conflict_handler.go internal/handlers/git_conflict_handler_test.go internal/handlers/errors.go internal/handlers/errors_test.go internal/handlers/git_routes.go internal/handlers/git_routes_test.go
go test ./internal/handlers -run 'TestGitConflictHandler|TestGitRoutes.*Conflict|TestWriteServiceErrorGitConflict' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Extend Plan 3 startup recovery test**

Assert conflict recovery finishes before initial note index and serve, partial stages survive, required local reindex completes, ambiguous state restores mutation policy, and no network command runs.

- [ ] **Step 7: Reuse existing startup call**

Do not add a second recovery pass. Extend the Plan 3 manager's existing local recovery implementation invoked from `cmd/api/main.go` before manager worker start and initial `SyncFS`.

- [ ] **Step 8: Run startup tests GREEN**

Run:

```bash
gofmt -w cmd/api/main.go cmd/api/git_runtime_test.go
go test ./cmd/api -run 'TestGitRecovery|TestConflictRecovery' -count=1 -v
```

Expected: PASS with zero startup network commands.

- [ ] **Step 9: Commit routes/startup**

```bash
git add internal/handlers/git_conflict_handler.go internal/handlers/git_conflict_handler_test.go internal/handlers/errors.go internal/handlers/errors_test.go internal/handlers/git_routes.go internal/handlers/git_routes_test.go cmd/api/main.go cmd/api/git_runtime_test.go
git commit -m "feat: expose git conflict routes"
```

### Task 13: Complete Lifecycles, Documentation, and Verification

**Files:**
- Modify: `internal/git/conflict_integration_test.go`
- Modify: `internal/service/git_manager_test.go`
- Modify: `internal/handlers/git_conflict_handler_test.go`
- Modify: `internal/handlers/git_routes_test.go`
- Modify: `docs/api.md`

- [ ] **Step 1: Add complete REST resolution lifecycle**

Create two conflicts, list, resolve one, reconstruct manager, verify only one remains, resolve the second, verify `can_complete`, queue complete, wait for operation, and assert ready status, exact changed paths, exact remote/trusted merge OID, rebuilt note index, and open mutations.

- [ ] **Step 2: Add complete REST abort lifecycle**

List, queue abort, wait, assert paused with restored changed paths, restored local snapshot, unchanged remote, open mutations, rejected manual sync until a future resume, and no queued sync.

- [ ] **Step 3: Add negative REST lifecycle**

Cover absolute/traversal/`.git` path, stale ID/OID, manual binary, keep-both collision, unresolved complete, abort without merge, ambiguous restart, repository lock, and error redaction.

- [ ] **Step 4: Run lifecycle tests**

Run:

```bash
go test ./internal/git ./internal/service ./internal/handlers -run 'TestGitConflictREST|TestConflictRestart|TestCompleteConflictIntegration' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Document exact API schemas**

Update `docs/api.md` for:

```text
GET /api/git/conflicts?base=<name>
PUT /api/git/conflicts/resolve
POST /api/git/conflicts/complete?base=<name>
POST /api/git/conflicts/abort?base=<name>
```

Include complete JSON examples for text, binary add/add, modify/delete, rename/delete, manual, keep-both, accepted operation, stale conflict, unresolved complete, abort-paused status/blocked sync, and recovery required. State local is stage 2 and remote is stage 3.

- [ ] **Step 6: Commit lifecycles/docs**

```bash
git add internal/git/conflict_integration_test.go internal/service/git_manager_test.go internal/handlers/git_conflict_handler_test.go internal/handlers/git_routes_test.go docs/api.md
git commit -m "test: cover git conflict lifecycle"
```

- [ ] **Step 7: Format changed Go files**

Run:

```bash
gofmt -w internal/model/api.go internal/model/git_conflict_test.go internal/git/operation.go internal/git/errors.go internal/git/runner.go internal/git/runner_test.go internal/git/service.go internal/git/service_test.go internal/git/conflict_errors_test.go internal/git/conflict_parser.go internal/git/conflict_parser_test.go internal/git/conflict.go internal/git/conflict_test.go internal/git/conflict_resolution.go internal/git/conflict_resolution_test.go internal/git/conflict_integration_test.go internal/git/recovery.go internal/git/recovery_test.go internal/repository/db.go internal/repository/git_operation_repo.go internal/repository/git_operation_repo_test.go internal/service/git_manager.go internal/service/git_manager_test.go internal/handlers/git_conflict_handler.go internal/handlers/git_conflict_handler_test.go internal/handlers/errors.go internal/handlers/errors_test.go internal/handlers/git_routes.go internal/handlers/git_routes_test.go cmd/api/main.go cmd/api/git_runtime_test.go
```

Expected: exit 0.

- [ ] **Step 8: Run focused tests repeatedly**

Run:

```bash
go test ./internal/git ./internal/service ./internal/handlers -run 'Conflict|Recovery' -count=10
```

Expected: PASS ten times.

- [ ] **Step 9: Run full backend verification**

Run:

```bash
go test ./...
go test -race ./...
go vet ./...
```

Expected: every command exits 0; race run reports no data race.

- [ ] **Step 10: Verify Windows compilation**

Run:

```bash
mkdir -p /tmp/igonotes-plan4-windows-tests
GOOS=windows GOARCH=amd64 go test -c -o /tmp/igonotes-plan4-windows-tests/model.test.exe ./internal/model
GOOS=windows GOARCH=amd64 go test -c -o /tmp/igonotes-plan4-windows-tests/git.test.exe ./internal/git
GOOS=windows GOARCH=amd64 go test -c -o /tmp/igonotes-plan4-windows-tests/repository.test.exe ./internal/repository
GOOS=windows GOARCH=amd64 go test -c -o /tmp/igonotes-plan4-windows-tests/service.test.exe ./internal/service
GOOS=windows GOARCH=amd64 go test -c -o /tmp/igonotes-plan4-windows-tests/handlers.test.exe ./internal/handlers
GOOS=windows GOARCH=amd64 go test -c -o /tmp/igonotes-plan4-windows-tests/api.test.exe ./cmd/api
```

Expected: all six package test binaries compile to the explicit output paths; none is executed on the host.

- [ ] **Step 11: Audit literal paths and forbidden commands**

Run:

```bash
git grep -n -E 'checkout-index|check-attr|pathspec-from-file|git add|git rm' -- 'internal/git/conflict*.go'
if git grep -n -E 'reset --hard|clean -f|force-with-lease|push --force|remote prune|Remove.*index.lock|Remove.*MERGE_HEAD' -- internal/git internal/service; then
  exit 1
fi
if git grep -n -E 'exec\.Command|exec\.CommandContext' -- 'internal/git/conflict*.go' internal/git/service.go internal/service/git_manager.go; then
  exit 1
fi
```

Expected: path commands use NUL/literal contracts; forbidden-command and direct-process searches have no Plan 4 production match.

- [ ] **Step 12: Inspect final diff**

Run:

```bash
git status --short
git diff --check
git diff --stat
```

Expected: `git diff --check` exits 0 and only planned backend/tests/`docs/api.md` files changed.

- [ ] **Step 13: Commit verification fixes when the diff contains fixes**

```bash
git add internal/model/api.go internal/git internal/repository internal/service/git_manager.go internal/service/git_manager_test.go internal/handlers cmd/api docs/api.md
git commit -m "fix: harden git conflict recovery"
```

Skip this commit when verification changed no file.

## Completion Criteria

- Porcelain, stages, rename evidence, attributes, and checkout mappings are NUL-safe for literal unusual paths.
- Listing exposes base stage 1, local stage 2, and remote stage 3 without repository mutation.
- Every resolve request affects one logical conflict and stages only exact source/output paths.
- Text, binary, add/add, modify/delete, rename/delete, and keep-both have real repository coverage.
- Complete refuses unresolved entries, creates one merge commit, reindexes before note unlock, and pushes the journaled exact OID.
- Abort uses Git's merge abort, reindexes restored content, transitions to paused, and does not schedule sync.
- Startup recovery is local-only, preserves partial stages, and deterministically handles complete/abort checkpoints, locks, foreign operations, and ambiguity.
- Ambiguous or locked repositories remain mutation-blocked without deleting Git state.
- All conflict routes use setup/local-origin guards and structured redacted errors.
- No frontend or autosync-breaker implementation is introduced.
