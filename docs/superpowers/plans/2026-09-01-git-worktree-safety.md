# Git Worktree Safety Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the coordination, active-worktree transaction, conflict mutation policy, and note revision safeguards required before Git can safely change a notes base.

**Architecture:** A process-wide `BaseOperationCoordinator` serializes base lifecycle operations and future Git operations. Settings mutations acquire locks in the fixed order `coordinator -> SettingsService.mu -> NoteService.baseMu -> repository/SQLite`, while ordinary note mutations consult an immutable atomic conflict snapshot without acquiring another lock. `NoteService` exposes one controlled active-filesystem mutation transaction for future Git worktree changes, and revision-aware note reads and saves prevent stale browser buffers from overwriting incoming changes.

**Tech Stack:** Go 1.26, `sync.Mutex`, `sync/atomic`, SHA-256 from the Go standard library, `net/http`, and the existing SQLite repository layer.

---

## Dependency and Scope

Plan 1 foundation must already be landed before executing this plan. Preserve every Git configuration model, validation function, error type, and test introduced by Plan 1.

Plan 2 extends, rather than replaces, Plan 1's settings construction seam. Its frozen output for Plan 3 is `NewSettingsService(store, notes, coordinator, activeBaseName, logger)` as the nil-Git wrapper and `NewSettingsServiceWithGit(store, notes, coordinator, activeBaseName, logger, validator, statuses)` for Git-aware and production composition.

Verify the starting point:

```bash
go test ./...
```

Expected: exit status `0`; every package reports `ok` or `[no test files]`, with no `FAIL` line.

This plan implements:

- one shared `BaseOperationCoordinator` in the production composition root;
- serialization of setup/add/switch/update/forget/replace and Git configure/disable settings mutations;
- the lock order `coordinator -> SettingsService.mu -> NoteService.baseMu -> repository/SQLite`;
- a lock-free conflict-state policy for ordinary active-base mutations;
- a controlled `NoteService.MutateActiveFilesystem` transaction for later Git worktree changes;
- SHA-256 revisions on `GET /api/note`;
- optional `expected_revision` on `POST /api/save`;
- backward-compatible unconditional saves when `expected_revision` is omitted;
- `409 note_changed` without modifying the note;
- `409 git_conflict_pending` for ordinary note mutations during a conflict;
- service, concurrency, and HTTP handler tests.

This plan does not implement:

- Git probe, connect, initialize, fetch, commit, merge, push, or conflict-resolution commands;
- `GitService`, `GitManager`, scheduling, operation status, or circuit breaker behavior;
- `/api/git/*` routes;
- frontend revision handling or conflict UI;
- any change to the existing `/api/sync` filesystem-rescan endpoint.

## Locking Contract and Hazards

The required order is:

```text
BaseOperationCoordinator
    -> SettingsService.mu
        -> NoteService.baseMu
            -> noteRepository / SQLite
```

The implementation must obey these rules:

- A published settings mutation acquires the coordinator before `SettingsService.mu`.
- Settings code may call `NoteService` only after acquiring those two locks in that order.
- `NoteService` may call its repository while holding `baseMu`.
- Repository methods, conflict-policy checks, and filesystem callbacks never call back into settings, `NoteService`, or the coordinator.
- `BaseOperationCoordinator.CheckMutation` uses only an atomic immutable snapshot. Taking a coordinator or manager mutex from inside a note mutation would invert the future Git lock order and can deadlock.
- `MutateActiveFilesystem` does not acquire the coordinator itself. Future Git callers must hold the coordinator before calling it.
- `MutateActiveFilesystem` deliberately bypasses ordinary conflict blocking so later conflict resolution and merge abort can repair a conflicted worktree.
- Both settings constructors require the coordinator. Their shared constructor path may call `NoteService` without locking the coordinator only during startup, before the service is published and before any Git manager or HTTP handler exists. Document this constructor-only exception rather than adding a redundant startup lock.
- `SettingsService.GetConfig` and `SetupCompleted` remain coordinator-free immutable reads.
- A future Git operation must not hold `NoteService.baseMu` during fetch or push. Only local worktree/index changes and SQLite reindexing belong inside `MutateActiveFilesystem`.
- The callback path is validated against the pinned active root before invocation. The coordinator prevents application-level path switches, but cannot prevent an external process from replacing the directory after validation; later Git code must still inspect repository identity before destructive worktree commands.

## File Map

Create:

- `internal/service/base_operation_coordinator.go` - global operation mutex and copy-on-write conflict snapshot.
- `internal/service/base_operation_coordinator_test.go` - serialization and lock-free policy tests.
- `internal/service/note_filesystem_transaction.go` - controlled active worktree mutation and mandatory reindex.
- `internal/service/note_filesystem_transaction_test.go` - identity, blocking, callback-error, and reindex tests.
- `internal/service/note_revision.go` - SHA-256 calculation and revision-aware note read/save methods.
- `internal/service/note_revision_test.go` - revision, compatibility, and stale-save tests.
- `internal/service/base_operation_integration_test.go` - complete coordinator/settings/note/repository lock-order test.

Modify:

- `internal/service/settings_service.go:30-45, 248-419` - inject the coordinator into both constructors, preserve Git dependencies, and wrap every settings mutation.
- `internal/service/settings_service_test.go` - update constructor calls and test coordinator ordering.
- `internal/service/base_transaction_test.go` - update constructor calls without changing transaction assertions.
- `internal/service/transaction_review_test.go` - update constructor calls without changing rollback assertions.
- `internal/service/startup_service_test.go` - update constructors and production-style fixtures.
- `internal/service/note_service.go:18-79, 449-473, 597-866` - inject the coordinator, gate mutations, and delegate note read/save operations.
- `internal/service/note_service_test.go` - update the central test constructor and add conflict mutation coverage.
- `internal/model/api.go:9-12` - add revision request and response JSON types.
- `internal/handlers/note_handler.go:67-94, 123-158, 188-327` - use revision-aware methods and map conflict errors.
- `internal/handlers/note_handler_test.go` - update constructors and add HTTP revision/conflict tests.
- `internal/handlers/errors.go:21-36` - add `note_changed` and `git_conflict_pending` mappings.
- `internal/handlers/errors_test.go` - update constructors and verify the new mappings.
- `internal/handlers/settings_handler_test.go` - update constructors.
- `cmd/api/main.go:84-95` - create and share exactly one coordinator while retaining Git-aware production wiring.
- `cmd/api/git_wiring_test.go` - update Plan 1 production constructor/source assertions for the coordinator argument.

## Parallel Dispatch Waves

The coordinator and settings lock refactor is serial because Tasks 1-3 change shared constructors and locking assumptions:

```text
Task 1 -> Task 2 -> Task 3
```

After Task 3 is committed, dispatch Wave A from that exact commit:

| Worker | Task | Exclusive ownership |
|---|---|---|
| Worker A | Task 4 | `internal/service/note_filesystem_transaction.go`, `internal/service/note_filesystem_transaction_test.go` |
| Worker B | Task 5 | `internal/model/api.go`, `internal/service/note_service.go`, `internal/service/note_revision.go`, `internal/service/note_revision_test.go` |

Create isolated worktrees:

```bash
git worktree add ../IGoNotes-plan2-active-fs -b plan2-active-fs
git worktree add ../IGoNotes-plan2-note-revisions -b plan2-note-revisions
```

The ownership sets do not overlap. Each worker runs and commits its own task. Integrate both branches after their focused tests pass:

```bash
git merge --no-ff plan2-active-fs -m "merge: add active filesystem transaction"
git merge --no-ff plan2-note-revisions -m "merge: add note revision service"
git worktree remove ../IGoNotes-plan2-active-fs
git worktree remove ../IGoNotes-plan2-note-revisions
git branch -d plan2-active-fs
git branch -d plan2-note-revisions
```

Run Tasks 6 and 7 serially after Wave A. Task 6 owns handler files; Task 7 owns only the new integration test.

### Task 1: Add the BaseOperationCoordinator

**Files:**

- Create: `internal/service/base_operation_coordinator.go`
- Create: `internal/service/base_operation_coordinator_test.go`

- [ ] **Step 1: Write failing serialization and lock-free conflict tests**

Create `internal/service/base_operation_coordinator_test.go`:

```go
package service

import (
	"errors"
	"testing"
	"time"
)

func TestBaseOperationCoordinatorSerializesOperations(t *testing.T) {
	coordinator := NewBaseOperationCoordinator()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan struct{})

	go func() {
		coordinator.Lock()
		defer coordinator.Unlock()
		close(firstStarted)
		<-releaseFirst
		close(firstDone)
	}()

	<-firstStarted
	secondAcquired := make(chan struct{})
	go func() {
		coordinator.Lock()
		defer coordinator.Unlock()
		close(secondAcquired)
	}()

	select {
	case <-secondAcquired:
		t.Fatal("second operation acquired the coordinator while the first was active")
	default:
	}

	close(releaseFirst)
	<-firstDone
	select {
	case <-secondAcquired:
	case <-time.After(time.Second):
		t.Fatal("second operation did not acquire the released coordinator")
	}
}

func TestBaseOperationCoordinatorConflictPolicyIsLockFree(t *testing.T) {
	coordinator := NewBaseOperationCoordinator()
	basePath := t.TempDir()

	coordinator.Lock()
	defer coordinator.Unlock()
	coordinator.SetConflict(basePath, true)

	checkDone := make(chan error, 1)
	go func() {
		checkDone <- coordinator.CheckMutation(basePath)
	}()

	select {
	case err := <-checkDone:
		if !errors.Is(err, ErrGitConflictPending) {
			t.Fatalf("CheckMutation() error = %v, want ErrGitConflictPending", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CheckMutation() waited for the operation mutex")
	}

	coordinator.SetConflict(basePath, false)
	if err := coordinator.CheckMutation(basePath); err != nil {
		t.Fatalf("CheckMutation() after clear error = %v", err)
	}
}
```

- [ ] **Step 2: Run the focused test and confirm the missing API failure**

```bash
go test ./internal/service -run '^TestBaseOperationCoordinator' -count=1
```

Expected: build failure containing `undefined: NewBaseOperationCoordinator` and `undefined: ErrGitConflictPending`.

- [ ] **Step 3: Implement the coordinator and atomic conflict snapshot**

Create `internal/service/base_operation_coordinator.go`:

```go
package service

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
)

var ErrGitConflictPending = errors.New("git conflict pending")

type conflictPathSet map[string]struct{}

// BaseOperationCoordinator serializes base lifecycle and Git operations.
// Lock order: coordinator -> SettingsService.mu -> NoteService.baseMu
// -> repository/SQLite.
type BaseOperationCoordinator struct {
	operations sync.Mutex
	conflicts  atomic.Pointer[conflictPathSet]
}

func NewBaseOperationCoordinator() *BaseOperationCoordinator {
	coordinator := &BaseOperationCoordinator{}
	empty := make(conflictPathSet)
	coordinator.conflicts.Store(&empty)
	return coordinator
}

func (c *BaseOperationCoordinator) Lock() {
	c.operations.Lock()
}

func (c *BaseOperationCoordinator) Unlock() {
	c.operations.Unlock()
}

// SetConflict publishes a copy-on-write snapshot. canonicalBasePath must be
// the canonical configured base path. This method never takes operations.
func (c *BaseOperationCoordinator) SetConflict(canonicalBasePath string, pending bool) {
	if canonicalBasePath == "" {
		return
	}
	canonicalBasePath = filepath.Clean(canonicalBasePath)

	for {
		current := c.conflicts.Load()
		next := make(conflictPathSet)
		if current != nil {
			next = make(conflictPathSet, len(*current)+1)
			for path := range *current {
				next[path] = struct{}{}
			}
		}
		if pending {
			next[canonicalBasePath] = struct{}{}
		} else {
			delete(next, canonicalBasePath)
		}
		if c.conflicts.CompareAndSwap(current, &next) {
			return
		}
	}
}

// CheckMutation is safe while NoteService.baseMu is held: it only loads an
// immutable atomic snapshot and cannot invert the operation lock order.
func (c *BaseOperationCoordinator) CheckMutation(canonicalBasePath string) error {
	if canonicalBasePath == "" {
		return nil
	}
	conflicts := c.conflicts.Load()
	if conflicts == nil {
		return nil
	}
	if _, pending := (*conflicts)[filepath.Clean(canonicalBasePath)]; pending {
		return ErrGitConflictPending
	}
	return nil
}
```

- [ ] **Step 4: Format and run the coordinator tests**

```bash
gofmt -w internal/service/base_operation_coordinator.go internal/service/base_operation_coordinator_test.go
go test ./internal/service -run '^TestBaseOperationCoordinator' -count=1
```

Expected: exit status `0` and `ok IGoNotes/internal/service`.

- [ ] **Step 5: Run the lock-free snapshot test under the race detector**

```bash
go test -race ./internal/service -run '^TestBaseOperationCoordinator' -count=20
```

Expected: exit status `0` with no `DATA RACE` report.

- [ ] **Step 6: Commit**

```bash
git add internal/service/base_operation_coordinator.go internal/service/base_operation_coordinator_test.go
git commit -m "feat: add base operation coordinator"
```

### Task 2: Put Settings Mutations Behind the Coordinator

**Files:**

- Modify: `internal/service/settings_service.go:30-45, 248-419`
- Modify: `internal/service/settings_service_test.go`
- Modify: `internal/service/base_transaction_test.go`
- Modify: `internal/service/transaction_review_test.go`
- Modify: `internal/service/startup_service_test.go`
- Modify: `internal/handlers/settings_handler_test.go`
- Modify: `internal/handlers/errors_test.go`
- Modify: `cmd/api/main.go:84-95`
- Modify: `cmd/api/git_wiring_test.go`

- [ ] **Step 1: Write a failing test for coordinator-before-settings ordering**

Add `context` and `runtime` to the imports and add this test to `internal/service/settings_service_test.go`:

```go
func TestSettingsServiceMutationsWaitAtCoordinatorBeforeSettingsLock(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	replacementPath := t.TempDir()

	tests := []struct {
		name string
		call func(*SettingsService) error
	}{
		{name: "complete setup", call: func(settings *SettingsService) error {
			_, err := settings.CompleteSetup(model.BaseMutationRequest{})
			return err
		}},
		{name: "add", call: func(settings *SettingsService) error {
			_, err := settings.AddBase(model.BaseMutationRequest{})
			return err
		}},
		{name: "switch", call: func(settings *SettingsService) error {
			_, err := settings.SwitchBase("other")
			return err
		}},
		{name: "update", call: func(settings *SettingsService) error {
			_, err := settings.UpdateBase("other", model.BaseUpdateRequest{Name: "renamed", Path: replacementPath})
			return err
		}},
		{name: "forget", call: func(settings *SettingsService) error {
			_, err := settings.ForgetBase("other")
			return err
		}},
		{name: "replace", call: func(settings *SettingsService) error {
			_, err := settings.ReplaceConfig(model.Config{
				Bases: []model.Base{{Name: "active", Path: activePath}, {Name: "other", Path: otherPath}},
				CurrentBase: "active",
			})
			return err
		}},
		{name: "configure git", call: func(settings *SettingsService) error {
			_, err := settings.ConfigureGit(context.Background(), "other", model.GitConfigRequest{})
			return err
		}},
		{name: "disable git", call: func(settings *SettingsService) error {
			_, err := settings.DisableGit(context.Background(), "other")
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := NewBaseOperationCoordinator()
			settings, _, _, _ := newConfiguredSettingsServiceWithCoordinator(t, coordinator, activePath, otherPath)

			coordinator.Lock()
			done := make(chan error, 1)
			go func() { done <- test.call(settings) }()
			for range 10 {
				runtime.Gosched()
			}

			select {
			case err := <-done:
				coordinator.Unlock()
				t.Fatalf("settings mutation returned before the coordinator was released: %v", err)
			default:
			}
			if !settings.mu.TryLock() {
				coordinator.Unlock()
				<-done
				t.Fatal("settings mutation took SettingsService.mu before the coordinator")
			}
			settings.mu.Unlock()
			coordinator.Unlock()
			<-done
		})
	}
}

func newConfiguredSettingsServiceWithCoordinator(
	t *testing.T,
	coordinator *BaseOperationCoordinator,
	activePath string,
	otherPath string,
) (*SettingsService, *fakeConfigStore, *fakeBaseRuntime, model.Config) {
	t.Helper()
	completed := true
	config := model.Config{
		Bases: []model.Base{{Name: "active", Path: activePath}, {Name: "other", Path: otherPath}},
		CurrentBase: "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config}
	runtime := &fakeBaseRuntime{path: activePath}
	settings, err := NewSettingsService(store, runtime, coordinator, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	return settings, store, runtime, cloneConfig(config)
}
```

- [ ] **Step 2: Run the focused test and confirm the constructor mismatch**

```bash
go test ./internal/service -run '^TestSettingsServiceMutationsWaitAtCoordinatorBeforeSettingsLock$' -count=1
```

Expected: build failure because neither Plan 1 settings constructor accepts a coordinator yet.

- [ ] **Step 3: Inject the coordinator and centralize mutation locking**

Change `internal/service/settings_service.go`:

```go
type SettingsService struct {
	// Lock order: coordinator -> SettingsService.mu -> NoteService.baseMu
	// -> repository/SQLite. Dependencies never call an earlier layer.
	coordinator  *BaseOperationCoordinator
	mu           sync.RWMutex
	store        ConfigStore
	notes        BaseRuntime
	logger       *log.Logger
	config       model.Config
	degraded     error
	gitValidator GitConfigValidator
	gitStatuses  GitStatusStore
}

func (s *SettingsService) lockMutation() {
	s.coordinator.Lock()
	s.mu.Lock()
}

func (s *SettingsService) unlockMutation() {
	s.mu.Unlock()
	s.coordinator.Unlock()
}

func (s *SettingsService) publishConfigLocked(next model.Config) {
	previous := s.config
	s.config = cloneConfig(next)

	retainedPaths := make(map[string]struct{}, len(next.Bases))
	for _, base := range next.Bases {
		retainedPaths[filepath.Clean(base.Path)] = struct{}{}
	}
	for _, base := range previous.Bases {
		if _, retained := retainedPaths[filepath.Clean(base.Path)]; !retained {
			s.coordinator.SetConflict(base.Path, false)
		}
	}
}
```

Keep both Plan 1 constructors. Change the plain constructor into the nil-Git-dependency wrapper below:

```go
func NewSettingsService(
	store ConfigStore,
	notes BaseRuntime,
	coordinator *BaseOperationCoordinator,
	activeBaseName string,
	logger *log.Logger,
) (*SettingsService, error) {
	return NewSettingsServiceWithGit(
		store,
		notes,
		coordinator,
		activeBaseName,
		logger,
		nil,
		nil,
	)
}
```

Add the coordinator to the existing Git-aware constructor without removing or reordering its Plan 1 Git dependencies:

```go
func NewSettingsServiceWithGit(
	store ConfigStore,
	notes BaseRuntime,
	coordinator *BaseOperationCoordinator,
	activeBaseName string,
	logger *log.Logger,
	gitValidator GitConfigValidator,
	gitStatuses GitStatusStore,
) (*SettingsService, error)
```

Immediately after the existing `notes == nil` validation in `NewSettingsServiceWithGit`, insert:

```go
if coordinator == nil {
	return nil, fmt.Errorf("base operation coordinator: %w", ErrInvalidConfig)
}
```

In the existing `NewSettingsServiceWithGit` successful return literal, insert the coordinator field and preserve both Plan 1 Git fields:

```go
return &SettingsService{
	coordinator:  coordinator,
	store:        store,
	notes:        notes,
	logger:       logger,
	config:       cloneConfig(config),
	gitValidator: gitValidator,
	gitStatuses:  gitStatuses,
}, nil
```

Do not alter the shared constructor path's existing config load, setup migration, runtime identity validation, CLI base selection, logger defaulting, or Git dependency behavior. Construction still never validates Git configuration or runs Git.

Replace `s.mu.Lock()` / `defer s.mu.Unlock()` at the start of each method below with:

```go
s.lockMutation()
defer s.unlockMutation()
```

Apply it to these exact methods:

```text
CompleteSetup
AddBase
UpdateBase
ForgetBase
SwitchBase
ReplaceConfig
ConfigureGit
DisableGit
```

The wrappers must begin before degraded-state checks, base lookup, request validation, Git validation, status compensation, or config cloning. This makes the table test prove that all eight methods wait at the coordinator without first taking `SettingsService.mu` or mutating Git status/config state.

In both successful branches of `applyConfigLocked`, replace:

```go
s.config = cloneConfig(next)
```

with:

```go
s.publishConfigLocked(next)
```

Call it only after config persistence and any runtime/index commit have succeeded. This preserves a conflict for a same-path rename, clears it after a path update, forget, or replacement removes the final reference to that path, and leaves it untouched after any failed persistence or rollback. Do not change rollback behavior or repository transaction ordering.

- [ ] **Step 4: Update both settings constructor call shapes without dropping Git dependencies**

The integrated Plan 1-2 signatures are:

```go
NewSettingsService(store, notes, coordinator, activeBaseName, logger)
NewSettingsServiceWithGit(store, notes, coordinator, activeBaseName, logger, gitValidator, gitStatuses)
```

In tests that do not need a shared coordinator, use:

```go
coordinator := NewBaseOperationCoordinator()
settings, err := NewSettingsService(store, notes, coordinator, "", nil)
```

Tests that exercise Plan 1 Git validation, configuration, status compensation, rename/path reconciliation, disable, or aggregation must remain wired through `NewSettingsServiceWithGit`; add the coordinator argument but preserve their existing validator and status-store fakes:

```go
coordinator := NewBaseOperationCoordinator()
settings, err := NewSettingsServiceWithGit(
	store,
	notes,
	coordinator,
	"",
	nil,
	gitValidator,
	gitStatuses,
)
```

Update every current call in:

```text
internal/service/settings_service_test.go
internal/service/base_transaction_test.go
internal/service/transaction_review_test.go
internal/service/startup_service_test.go
internal/handlers/settings_handler_test.go
internal/handlers/errors_test.go
cmd/api/main.go
cmd/api/git_wiring_test.go
```

Keep all existing assertions and fixture behavior unchanged. Do not convert an existing Git-aware test fixture to the nil-Git wrapper.

After the edits, run this source sweep and update every Go call it reports to one of the two exact signatures above:

```bash
git grep -n -E 'NewSettingsService(WithGit)?\(' -- '*.go'
```

Expected: each plain call passes a coordinator before `activeBaseName`; each Git-aware call passes the coordinator in that same position and retains its existing validator and status store after `logger`.

In `cmd/api/main.go`, create the coordinator before constructing settings while `NewNoteService` still has its current two-argument signature. Preserve Plan 1's command runner, client, status repository, validator, probe/status services, handler, and route wiring; only add the coordinator argument to the Git-aware settings constructor:

```go
coordinator := service.NewBaseOperationCoordinator()
noteRepo := repository.NewNoteRepository(db)
noteService := service.NewNoteService(noteRepo, basePath)

gitRunner := gitcmd.NewCommandRunner()
gitClient := gitcmd.NewClient(gitRunner)
gitStatusRepo := repository.NewGitStatusRepository(db)
gitValidator := service.NewGitConfigValidator(gitClient)

settingsService, err := service.NewSettingsServiceWithGit(
	configService,
	noteService,
	coordinator,
	options.base,
	log.Default(),
	gitValidator,
	gitStatusRepo,
)
```

The `NewSettingsServiceWithGit` call above is the production contract consumed by Plan 3. Do not replace it with `NewSettingsService` or pass nil Git dependencies in `cmd/api/main.go`.

- [ ] **Step 5: Add nil-coordinator validation coverage for both constructors**

Add to `internal/service/settings_service_test.go`:

```go
func TestSettingsServiceConstructorsRejectNilCoordinator(t *testing.T) {
	tests := []struct {
		name string
		call func(ConfigStore, BaseRuntime) (*SettingsService, error)
	}{
		{name: "plain wrapper", call: func(store ConfigStore, notes BaseRuntime) (*SettingsService, error) {
			return NewSettingsService(store, notes, nil, "", nil)
		}},
		{name: "git aware", call: func(store ConfigStore, notes BaseRuntime) (*SettingsService, error) {
			return NewSettingsServiceWithGit(store, notes, nil, "", nil, nil, nil)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			completed := false
			store := &fakeConfigStore{config: &model.Config{SetupCompleted: &completed}}
			settings, err := test.call(store, &fakeBaseRuntime{})
			if settings != nil {
				t.Errorf("constructor service = %#v, want nil", settings)
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("constructor error = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), "base operation coordinator") {
				t.Errorf("constructor error = %q, want coordinator context", err)
			}
		})
	}
}
```

- [ ] **Step 6: Test conflict snapshot reconciliation on successful publication**

Add to `internal/service/settings_service_test.go`:

```go
func TestSettingsServiceClearsOnlyRemovedConflictPaths(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	replacementPath := t.TempDir()
	coordinator := NewBaseOperationCoordinator()
	settings, _, _, _ := newConfiguredSettingsServiceWithCoordinator(t, coordinator, activePath, otherPath)
	coordinator.SetConflict(activePath, true)
	coordinator.SetConflict(otherPath, true)

	if _, err := settings.UpdateBase("other", model.BaseUpdateRequest{Name: "renamed", Path: replacementPath}); err != nil {
		t.Fatalf("UpdateBase() error = %v", err)
	}
	if err := coordinator.CheckMutation(otherPath); err != nil {
		t.Errorf("removed path conflict = %v, want cleared", err)
	}
	if !errors.Is(coordinator.CheckMutation(activePath), ErrGitConflictPending) {
		t.Error("retained active path conflict was cleared")
	}

	coordinator.SetConflict(replacementPath, true)
	if _, err := settings.UpdateBase("renamed", model.BaseUpdateRequest{Name: "same-path-rename", Path: replacementPath}); err != nil {
		t.Fatalf("same-path UpdateBase() error = %v", err)
	}
	if !errors.Is(coordinator.CheckMutation(replacementPath), ErrGitConflictPending) {
		t.Error("same-path rename cleared its conflict")
	}

	if _, err := settings.ForgetBase("same-path-rename"); err != nil {
		t.Fatalf("ForgetBase() error = %v", err)
	}
	if err := coordinator.CheckMutation(replacementPath); err != nil {
		t.Errorf("forgotten path conflict = %v, want cleared", err)
	}
}
```

Add this focused failure test:

```go
func TestSettingsServiceFailedPublicationRetainsConflictSnapshot(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	coordinator := NewBaseOperationCoordinator()
	settings, store, _, _ := newConfiguredSettingsServiceWithCoordinator(t, coordinator, activePath, otherPath)
	coordinator.SetConflict(otherPath, true)
	store.saveErr = errors.New("disk full")

	if _, err := settings.ForgetBase("other"); !errors.Is(err, store.saveErr) {
		t.Fatalf("ForgetBase() error = %v, want %v", err, store.saveErr)
	}
	if !errors.Is(coordinator.CheckMutation(otherPath), ErrGitConflictPending) {
		t.Error("failed publication cleared the conflict snapshot")
	}
}
```

Add explicit replacement coverage:

```go
func TestSettingsServiceReplaceConfigClearsRemovedConflictPath(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	coordinator := NewBaseOperationCoordinator()
	settings, _, _, _ := newConfiguredSettingsServiceWithCoordinator(t, coordinator, activePath, otherPath)
	coordinator.SetConflict(otherPath, true)

	if _, err := settings.ReplaceConfig(model.Config{
		Bases: []model.Base{{Name: "active", Path: activePath}},
		CurrentBase: "active",
	}); err != nil {
		t.Fatalf("ReplaceConfig() error = %v", err)
	}
	if err := coordinator.CheckMutation(otherPath); err != nil {
		t.Errorf("replaced path conflict = %v, want cleared", err)
	}
}
```

- [ ] **Step 7: Format and run settings and transaction tests**

```bash
gofmt -w cmd/api/main.go cmd/api/git_wiring_test.go internal/service/settings_service.go internal/service/settings_service_test.go internal/service/base_transaction_test.go internal/service/transaction_review_test.go internal/service/startup_service_test.go internal/handlers/settings_handler_test.go internal/handlers/errors_test.go
go test ./...
```

Expected: exit status `0`; every package reports `ok` or `[no test files]`.

- [ ] **Step 8: Run the affected tests with the race detector**

```bash
go test -race ./internal/service -run 'Test(SettingsService|NewSettingsService|NoteServiceBasePersistence)' -count=1
```

Expected: exit status `0` with no deadlock, timeout, or `DATA RACE`.

- [ ] **Step 9: Commit**

```bash
git add cmd/api/main.go cmd/api/git_wiring_test.go internal/service/settings_service.go internal/service/settings_service_test.go internal/service/base_transaction_test.go internal/service/transaction_review_test.go internal/service/startup_service_test.go internal/handlers/settings_handler_test.go internal/handlers/errors_test.go
git commit -m "refactor: coordinate settings base mutations"
```

### Task 3: Share the Coordinator with NoteService and Gate Mutations

**Files:**

- Modify: `internal/service/note_service.go:48-79, 597-866`
- Modify: `internal/service/note_service_test.go`
- Modify: `internal/service/base_transaction_test.go`
- Modify: `internal/service/transaction_review_test.go`
- Modify: `internal/service/startup_service_test.go`
- Modify: `internal/handlers/note_handler_test.go`
- Modify: `internal/handlers/settings_handler_test.go`
- Modify: `internal/handlers/errors_test.go`
- Modify: `cmd/api/main.go:84-95`
- Modify: `cmd/api/git_wiring_test.go`

- [ ] **Step 1: Write a failing table test for conflict-blocked mutations**

Add to `internal/service/note_service_test.go`:

```go
func TestNoteServiceConflictPolicyBlocksOrdinaryMutations(t *testing.T) {
	tests := []struct {
		name string
		call func(*NoteService) error
	}{
		{name: "save", call: func(notes *NoteService) error {
			return notes.SaveNoteContent("note.md", "changed")
		}},
		{name: "create", call: func(notes *NoteService) error {
			_, err := notes.CreateNode("", "created", "file")
			return err
		}},
		{name: "delete", call: func(notes *NoteService) error {
			return notes.DeleteNode("note.md")
		}},
		{name: "rename", call: func(notes *NoteService) error {
			return notes.RenameNode("note.md", "renamed")
		}},
		{name: "asset", call: func(notes *NoteService) error {
			_, err := notes.SaveAsset(strings.NewReader("image"), "image.png")
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			basePath := t.TempDir()
			notePath := filepath.Join(basePath, "note.md")
			if err := os.WriteFile(notePath, []byte("original"), 0o600); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			coordinator := NewBaseOperationCoordinator()
			notes := newTestNoteServiceWithCoordinator(
				t,
				&fakeNoteRepository{nodes: []model.NoteNode{{ID: "note.md"}}},
				basePath,
				coordinator,
			)
			coordinator.SetConflict(basePath, true)

			if err := test.call(notes); !errors.Is(err, ErrGitConflictPending) {
				t.Fatalf("mutation error = %v, want ErrGitConflictPending", err)
			}
			assertFileContent(t, notePath, []byte("original"))
			if _, err := os.Lstat(filepath.Join(basePath, "created.md")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("blocked mutation created a note, Lstat error = %v", err)
			}
			if _, err := os.Lstat(filepath.Join(basePath, "renamed.md")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("blocked mutation renamed a note, Lstat error = %v", err)
			}
			if _, err := os.Lstat(filepath.Join(basePath, "assets")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("blocked mutation created assets, Lstat error = %v", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run the focused test and confirm the constructor failure**

```bash
go test ./internal/service -run '^TestNoteServiceConflictPolicyBlocksOrdinaryMutations$' -count=1
```

Expected: build failure because `NewNoteService` does not yet accept a coordinator.

- [ ] **Step 3: Inject the coordinator into NoteService**

Change `internal/service/note_service.go`:

```go
type NoteService struct {
	repo        noteRepository
	coordinator *BaseOperationCoordinator
	basePath    string
	baseRoot    *os.Root
	baseErr     error
	closeErr    error

	// Lock order: coordinator -> SettingsService.mu -> baseMu
	// -> repository/SQLite.
	baseMu sync.RWMutex

	initialSyncDone  chan struct{}
	initialSyncErr   error
	once             sync.Once
	scan             noteScanner
	openRoot         func(string) (*os.Root, error)
	beforeReadLock   func()
	beforeCreateLock func()
	beforeRename     func()
}

func NewNoteService(
	repo noteRepository,
	basePath string,
	coordinator *BaseOperationCoordinator,
) *NoteService {
	if coordinator == nil {
		panic("service.NewNoteService: nil BaseOperationCoordinator")
	}
	service := &NoteService{
		repo:            repo,
		coordinator:     coordinator,
		basePath:        basePath,
		initialSyncDone: make(chan struct{}),
		scan:            scanNotes,
		openRoot:        os.OpenRoot,
	}
	if basePath != "" {
		service.baseRoot, service.baseErr = os.OpenRoot(basePath)
	}
	return service
}

func (s *NoteService) checkMutationLocked() error {
	return s.coordinator.CheckMutation(s.basePath)
}
```

- [ ] **Step 4: Gate every ordinary filesystem mutation before its first side effect**

After checking `baseErr` and `basePath`, add the corresponding conflict check to these exact methods:

```text
SaveNoteContent
CreateNode
DeleteNode
RenameNode
SaveAsset
```

Use the correct return form:

```go
if err := s.checkMutationLocked(); err != nil {
	return err
}
```

```go
if err := s.checkMutationLocked(); err != nil {
	return nil, err
}
```

```go
if err := s.checkMutationLocked(); err != nil {
	return "", err
}
```

Do not gate `GetNoteContent`, `GetTree`, `GetAbsoluteFilePath`, `OpenRawFile`, `SyncFS`, `SwitchBase`, or `Close`.

- [ ] **Step 5: Update the shared NoteService test constructor**

Replace the helper at the end of `internal/service/note_service_test.go` with:

```go
func newTestNoteServiceWithCoordinator(
	t *testing.T,
	repo noteRepository,
	base string,
	coordinator *BaseOperationCoordinator,
) *NoteService {
	t.Helper()
	service := NewNoteService(repo, base, coordinator)
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("NoteService.Close() error = %v", err)
		}
	})
	return service
}

func newTestNoteService(t *testing.T, repo noteRepository, base string) *NoteService {
	t.Helper()
	return newTestNoteServiceWithCoordinator(t, repo, base, NewBaseOperationCoordinator())
}
```

- [ ] **Step 6: Update every direct `NewNoteService` call**

The exact signature is:

```go
NewNoteService(repo, basePath, coordinator)
```

Update direct calls in:

```text
internal/service/settings_service_test.go
internal/service/startup_service_test.go
internal/service/transaction_review_test.go
internal/handlers/settings_handler_test.go
internal/handlers/errors_test.go
internal/handlers/note_handler_test.go
cmd/api/main.go
cmd/api/git_wiring_test.go
```

Fixtures that construct both services must pass the same coordinator to `NewNoteService` and whichever settings constructor they already use. Preserve `NewSettingsServiceWithGit` plus its validator/status fakes in every Plan 1 Git-aware fixture; use `NewSettingsService` only for fixtures that intentionally have nil Git dependencies.

- [ ] **Step 7: Wire one shared production coordinator**

Change `cmd/api/main.go`:

```go
coordinator := service.NewBaseOperationCoordinator()
noteRepo := repository.NewNoteRepository(db)
noteService := service.NewNoteService(noteRepo, basePath, coordinator)

gitRunner := gitcmd.NewCommandRunner()
gitClient := gitcmd.NewClient(gitRunner)
gitStatusRepo := repository.NewGitStatusRepository(db)
gitValidator := service.NewGitConfigValidator(gitClient)

settingsService, err := service.NewSettingsServiceWithGit(
	configService,
	noteService,
	coordinator,
	options.base,
	log.Default(),
	gitValidator,
	gitStatusRepo,
)
```

There must be exactly one `NewBaseOperationCoordinator` call in `runServer`. The same coordinator is passed to `NewNoteService` and `NewSettingsServiceWithGit`; Plan 1's `gitValidator` and `gitStatusRepo` are passed unchanged. These are the exact constructor contracts Plan 3 consumes:

```go
NewSettingsService(store, notes, coordinator, activeBaseName, logger)
NewSettingsServiceWithGit(store, notes, coordinator, activeBaseName, logger, validator, statuses)
```

- [ ] **Step 8: Format and run all tests**

```bash
gofmt -w cmd/api/main.go cmd/api/git_wiring_test.go internal/service/note_service.go internal/service/note_service_test.go internal/service/base_transaction_test.go internal/service/transaction_review_test.go internal/service/startup_service_test.go internal/service/settings_service_test.go internal/handlers/note_handler_test.go internal/handlers/settings_handler_test.go internal/handlers/errors_test.go
go test ./...
```

Expected: exit status `0`; no old constructor call remains.

- [ ] **Step 9: Run conflict policy tests under the race detector**

```bash
go test -race ./internal/service -run 'Test(NoteServiceConflictPolicy|BaseOperationCoordinatorConflictPolicy)' -count=20
```

Expected: exit status `0` with no `DATA RACE`.

- [ ] **Step 10: Commit**

```bash
git add cmd/api/main.go cmd/api/git_wiring_test.go internal/service/note_service.go internal/service/note_service_test.go internal/service/base_transaction_test.go internal/service/transaction_review_test.go internal/service/startup_service_test.go internal/service/settings_service_test.go internal/handlers/note_handler_test.go internal/handlers/settings_handler_test.go internal/handlers/errors_test.go
git commit -m "feat: gate note mutations during conflicts"
```

### Task 4: Add the Controlled Active Filesystem Transaction

**Files:**

- Create: `internal/service/note_filesystem_transaction.go`
- Create: `internal/service/note_filesystem_transaction_test.go`

- [ ] **Step 1: Write failing transaction tests**

Create `internal/service/note_filesystem_transaction_test.go`:

```go
package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNoteServiceMutateActiveFilesystemReindexesBeforeUnlock(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "old.md", "old")
	coordinator := NewBaseOperationCoordinator()
	repo := &fakeNoteRepository{}
	notes := newTestNoteServiceWithCoordinator(t, repo, basePath, coordinator)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}

	coordinator.Lock()
	err := notes.MutateActiveFilesystem(basePath, func(canonicalPath string) error {
		if canonicalPath != basePath {
			t.Errorf("callback path = %q, want %q", canonicalPath, basePath)
		}
		if err := os.Remove(filepath.Join(canonicalPath, "old.md")); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(canonicalPath, "incoming.md"), []byte("incoming"), 0o600)
	})
	coordinator.Unlock()
	if err != nil {
		t.Fatalf("MutateActiveFilesystem() error = %v", err)
	}
	assertRepositoryIDs(t, repo, "incoming.md")
}

func TestNoteServiceMutateActiveFilesystemReindexesAfterCallbackError(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "note.md", "before")
	coordinator := NewBaseOperationCoordinator()
	repo := &fakeNoteRepository{}
	notes := newTestNoteServiceWithCoordinator(t, repo, basePath, coordinator)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}

	callbackErr := errors.New("merge left conflicts")
	coordinator.Lock()
	err := notes.MutateActiveFilesystem(basePath, func(canonicalPath string) error {
		if err := os.WriteFile(filepath.Join(canonicalPath, "conflicted.md"), []byte("conflict markers"), 0o600); err != nil {
			return err
		}
		return callbackErr
	})
	coordinator.Unlock()
	if !errors.Is(err, callbackErr) {
		t.Fatalf("MutateActiveFilesystem() error = %v, want callback error", err)
	}
	assertRepositoryIDs(t, repo, "conflicted.md", "note.md")
}

func TestNoteServiceMutateActiveFilesystemRejectsInactivePath(t *testing.T) {
	activePath := t.TempDir()
	inactivePath := t.TempDir()
	writeTestNote(t, activePath, "active.md", "active")
	coordinator := NewBaseOperationCoordinator()
	repo := &fakeNoteRepository{}
	notes := newTestNoteServiceWithCoordinator(t, repo, activePath, coordinator)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}

	called := false
	coordinator.Lock()
	err := notes.MutateActiveFilesystem(inactivePath, func(string) error {
		called = true
		return nil
	})
	coordinator.Unlock()
	if !errors.Is(err, ErrRuntimePathChanged) {
		t.Fatalf("MutateActiveFilesystem() error = %v, want ErrRuntimePathChanged", err)
	}
	if called {
		t.Fatal("callback ran for an inactive path")
	}
	assertRepositoryIDs(t, repo, "active.md")
}

func TestNoteServiceMutateActiveFilesystemBypassesConflictPolicy(t *testing.T) {
	basePath := t.TempDir()
	coordinator := NewBaseOperationCoordinator()
	notes := newTestNoteServiceWithCoordinator(t, &fakeNoteRepository{}, basePath, coordinator)
	coordinator.SetConflict(basePath, true)

	coordinator.Lock()
	err := notes.MutateActiveFilesystem(basePath, func(canonicalPath string) error {
		return os.WriteFile(filepath.Join(canonicalPath, "resolved.md"), []byte("resolved"), 0o600)
	})
	coordinator.Unlock()
	if err != nil {
		t.Fatalf("MutateActiveFilesystem() error = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests and confirm the missing method failure**

```bash
go test ./internal/service -run '^TestNoteServiceMutateActiveFilesystem' -count=1
```

Expected: build failure containing `notes.MutateActiveFilesystem undefined`.

- [ ] **Step 3: Implement the active filesystem transaction**

Create `internal/service/note_filesystem_transaction.go`:

```go
package service

import (
	"errors"
	"os"
)

// MutateActiveFilesystem changes the active worktree under baseMu and rebuilds
// SQLite before readers resume. The caller must already hold the coordinator.
// The callback must not call SettingsService, NoteService, or noteRepository.
func (s *NoteService) MutateActiveFilesystem(
	expectedPath string,
	mutate func(canonicalPath string) error,
) error {
	if mutate == nil {
		return os.ErrInvalid
	}

	s.baseMu.Lock()
	defer s.baseMu.Unlock()
	if s.baseErr != nil {
		return s.baseErr
	}
	if s.basePath == "" {
		return os.ErrNotExist
	}

	matches, canonicalPath, err := s.baseIdentityLocked(expectedPath)
	if err != nil {
		return err
	}
	if !matches {
		return ErrRuntimePathChanged
	}

	mutationErr := mutate(canonicalPath)
	indexErr := s.replaceIndexLocked()
	return errors.Join(mutationErr, indexErr)
}
```

Always reindex after callback invocation, including when the callback reports a merge conflict. Do not call `checkMutationLocked` from this method.

- [ ] **Step 4: Add a blocked-reader test**

Append to `internal/service/note_filesystem_transaction_test.go`:

```go
func TestNoteServiceMutateActiveFilesystemBlocksReadersThroughReindex(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "note.md", "before")
	coordinator := NewBaseOperationCoordinator()
	notes := newTestNoteServiceWithCoordinator(t, &fakeNoteRepository{}, basePath, coordinator)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}

	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		coordinator.Lock()
		defer coordinator.Unlock()
		mutationDone <- notes.MutateActiveFilesystem(basePath, func(path string) error {
			close(callbackStarted)
			<-releaseCallback
			return os.WriteFile(filepath.Join(path, "note.md"), []byte("after"), 0o600)
		})
	}()
	<-callbackStarted

	readDone := make(chan noteReadResult, 1)
	go func() {
		content, err := notes.GetNoteContent("note.md")
		readDone <- noteReadResult{content: content, err: err}
	}()
	select {
	case result := <-readDone:
		t.Fatalf("read completed during active filesystem mutation: %#v", result)
	default:
	}

	close(releaseCallback)
	if err := <-mutationDone; err != nil {
		t.Fatalf("MutateActiveFilesystem() error = %v", err)
	}
	result := <-readDone
	if result.err != nil || result.content != "after" {
		t.Fatalf("read after mutation = %q, %v; want after, nil", result.content, result.err)
	}
}
```

- [ ] **Step 5: Format and run focused tests**

```bash
gofmt -w internal/service/note_filesystem_transaction.go internal/service/note_filesystem_transaction_test.go
go test ./internal/service -run '^TestNoteServiceMutateActiveFilesystem' -count=1
```

Expected: exit status `0` and `ok IGoNotes/internal/service`.

- [ ] **Step 6: Run the transaction tests under the race detector**

```bash
go test -race ./internal/service -run '^TestNoteServiceMutateActiveFilesystem' -count=10
```

Expected: exit status `0` with no race report or timeout.

- [ ] **Step 7: Commit on `plan2-active-fs`**

```bash
git add internal/service/note_filesystem_transaction.go internal/service/note_filesystem_transaction_test.go
git commit -m "feat: add active filesystem transaction"
```

### Task 5: Add SHA-256 Note Revisions

**Files:**

- Modify: `internal/model/api.go:9-12`
- Modify: `internal/service/note_service.go:449-473, 597-622`
- Create: `internal/service/note_revision.go`
- Create: `internal/service/note_revision_test.go`

- [ ] **Step 1: Write failing revision and stale-save tests**

Create `internal/service/note_revision_test.go`:

```go
package service

import (
	"errors"
	"testing"

	"IGoNotes/internal/model"
)

const revisionOfIdea = "sha256:25ec1fffebeb4b54346d7df2c71511065bf99f571ac1c615e3fc1d4ce17ca5f9"

func TestNoteServiceGetNoteReturnsSHA256RevisionAndRequestID(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "idea.md", "# Idea\n")
	notes := newTestNoteServiceWithCoordinator(t, &fakeNoteRepository{}, basePath, NewBaseOperationCoordinator())

	got, err := notes.GetNote("folder/../idea.md")
	if err != nil {
		t.Fatalf("GetNote() error = %v", err)
	}
	want := model.NoteContentResponse{
		ID: "folder/../idea.md",
		Content: "# Idea\n",
		Revision: revisionOfIdea,
	}
	if got != want {
		t.Errorf("GetNote() = %#v, want %#v", got, want)
	}
}

func TestNoteServiceSaveNoteAcceptsMatchingRevision(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "idea.md", "# Idea\n")
	notes := newTestNoteServiceWithCoordinator(t, &fakeNoteRepository{}, basePath, NewBaseOperationCoordinator())
	current, err := notes.GetNote("idea.md")
	if err != nil {
		t.Fatalf("GetNote() error = %v", err)
	}

	response, err := notes.SaveNote(model.SaveNoteRequest{
		ID: "idea.md",
		Content: "# Changed\n",
		ExpectedRevision: &current.Revision,
	})
	if err != nil {
		t.Fatalf("SaveNote() error = %v", err)
	}
	if response.Status != "saved" || response.Revision == "" || response.Revision == current.Revision {
		t.Errorf("SaveNote() response = %#v", response)
	}
	assertFileContent(t, filepath.Join(basePath, "idea.md"), []byte("# Changed\n"))
}

func TestNoteServiceSaveNoteRejectsStaleRevisionWithoutWriting(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "idea.md", "incoming")
	notes := newTestNoteServiceWithCoordinator(t, &fakeNoteRepository{}, basePath, NewBaseOperationCoordinator())
	stale := "sha256:stale"

	response, err := notes.SaveNote(model.SaveNoteRequest{
		ID: "idea.md",
		Content: "browser buffer",
		ExpectedRevision: &stale,
	})
	if !errors.Is(err, ErrNoteChanged) {
		t.Fatalf("SaveNote() error = %v, want ErrNoteChanged", err)
	}
	if response != (model.SaveNoteResponse{}) {
		t.Errorf("SaveNote() response = %#v, want zero value", response)
	}
	assertFileContent(t, filepath.Join(basePath, "idea.md"), []byte("incoming"))
}

func TestNoteServiceSaveNoteWithoutExpectedRevisionRemainsUnconditional(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "idea.md", "incoming")
	notes := newTestNoteServiceWithCoordinator(t, &fakeNoteRepository{}, basePath, NewBaseOperationCoordinator())

	response, err := notes.SaveNote(model.SaveNoteRequest{ID: "idea.md", Content: "legacy client"})
	if err != nil {
		t.Fatalf("SaveNote() error = %v", err)
	}
	if response.Status != "saved" || response.Revision == "" {
		t.Errorf("SaveNote() response = %#v", response)
	}
	assertFileContent(t, filepath.Join(basePath, "idea.md"), []byte("legacy client"))
}
```

Add `path/filepath` to this test's imports.

- [ ] **Step 2: Run focused tests and confirm missing revision types and methods**

```bash
go test ./internal/service -run 'TestNoteService(GetNoteReturnsSHA256Revision|SaveNote)' -count=1
```

Expected: build failure mentioning undefined `model.NoteContentResponse`, `model.SaveNoteResponse`, `GetNote`, `SaveNote`, or `ErrNoteChanged`.

- [ ] **Step 3: Add request and response JSON types**

Change `internal/model/api.go`:

```go
type SaveNoteRequest struct {
	ID               string  `json:"id"`
	Content          string  `json:"content"`
	ExpectedRevision *string `json:"expected_revision,omitempty"`
}

type NoteContentResponse struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Revision string `json:"revision"`
}

type SaveNoteResponse struct {
	Status   string `json:"status"`
	Revision string `json:"revision"`
}
```

The pointer is required: `nil` means omitted and preserves current unconditional behavior; an explicitly supplied empty string participates in comparison and is stale.

- [ ] **Step 4: Implement revision calculation and guarded read/save**

Create `internal/service/note_revision.go`:

```go
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path"

	"IGoNotes/internal/model"
)

var ErrNoteChanged = errors.New("note changed")

func noteRevision(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *NoteService) GetNote(id string) (model.NoteContentResponse, error) {
	cleanID, err := cleanRelativeNotePath(id, false)
	if err != nil {
		return model.NoteContentResponse{}, err
	}
	if s.beforeReadLock != nil {
		s.beforeReadLock()
	}
	s.baseMu.RLock()
	defer s.baseMu.RUnlock()
	if s.baseErr != nil {
		return model.NoteContentResponse{}, s.baseErr
	}
	if s.basePath == "" {
		return model.NoteContentResponse{}, os.ErrNotExist
	}

	content, err := s.baseRoot.ReadFile(rootPath(cleanID))
	if err != nil {
		return model.NoteContentResponse{}, normalizeRootError(err)
	}
	return model.NoteContentResponse{
		ID: id,
		Content: string(content),
		Revision: noteRevision(content),
	}, nil
}

func (s *NoteService) SaveNote(request model.SaveNoteRequest) (model.SaveNoteResponse, error) {
	cleanID, err := cleanRelativeNotePath(request.ID, false)
	if err != nil {
		return model.SaveNoteResponse{}, err
	}
	s.baseMu.Lock()
	defer s.baseMu.Unlock()
	if s.baseErr != nil {
		return model.SaveNoteResponse{}, s.baseErr
	}
	if s.basePath == "" {
		return model.SaveNoteResponse{}, os.ErrNotExist
	}
	if err := s.checkMutationLocked(); err != nil {
		return model.SaveNoteResponse{}, err
	}

	if request.ExpectedRevision != nil {
		current, err := s.baseRoot.ReadFile(rootPath(cleanID))
		if err != nil {
			return model.SaveNoteResponse{}, normalizeRootError(err)
		}
		if noteRevision(current) != *request.ExpectedRevision {
			return model.SaveNoteResponse{}, ErrNoteChanged
		}
	}

	if err := ensureRootDir(s.baseRoot, path.Dir(cleanID)); err != nil {
		return model.SaveNoteResponse{}, err
	}
	content := []byte(request.Content)
	if err := s.baseRoot.WriteFile(rootPath(cleanID), content, 0o644); err != nil {
		return model.SaveNoteResponse{}, normalizeRootError(err)
	}
	return model.SaveNoteResponse{Status: "saved", Revision: noteRevision(content)}, nil
}
```

- [ ] **Step 5: Delegate existing service methods without changing their signatures**

Replace `GetNoteContent` in `internal/service/note_service.go` with:

```go
func (s *NoteService) GetNoteContent(id string) (string, error) {
	note, err := s.GetNote(id)
	if err != nil {
		return "", err
	}
	return note.Content, nil
}
```

Replace `SaveNoteContent` with:

```go
func (s *NoteService) SaveNoteContent(id string, content string) error {
	_, err := s.SaveNote(model.SaveNoteRequest{ID: id, Content: content})
	return err
}
```

Remove the superseded duplicate lock/read/write bodies. These wrappers preserve current internal call sites while ensuring all note writes use one conflict-aware implementation.

- [ ] **Step 6: Add explicit empty-revision and conflict-priority tests**

Append to `internal/service/note_revision_test.go`:

```go
func TestNoteServiceSaveNoteTreatsExplicitEmptyRevisionAsStale(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "idea.md", "current")
	notes := newTestNoteServiceWithCoordinator(t, &fakeNoteRepository{}, basePath, NewBaseOperationCoordinator())
	empty := ""
	_, err := notes.SaveNote(model.SaveNoteRequest{ID: "idea.md", Content: "replacement", ExpectedRevision: &empty})
	if !errors.Is(err, ErrNoteChanged) {
		t.Fatalf("SaveNote() error = %v, want ErrNoteChanged", err)
	}
	assertFileContent(t, filepath.Join(basePath, "idea.md"), []byte("current"))
}

func TestNoteServiceSaveNoteChecksConflictBeforeRevision(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "idea.md", "current")
	coordinator := NewBaseOperationCoordinator()
	notes := newTestNoteServiceWithCoordinator(t, &fakeNoteRepository{}, basePath, coordinator)
	coordinator.SetConflict(basePath, true)
	stale := "sha256:stale"
	_, err := notes.SaveNote(model.SaveNoteRequest{ID: "idea.md", Content: "replacement", ExpectedRevision: &stale})
	if !errors.Is(err, ErrGitConflictPending) {
		t.Fatalf("SaveNote() error = %v, want ErrGitConflictPending", err)
	}
	assertFileContent(t, filepath.Join(basePath, "idea.md"), []byte("current"))
}
```

- [ ] **Step 7: Verify the exact test revision independently**

```bash
printf '# Idea\n' | sha256sum
```

Expected:

```text
25ec1fffebeb4b54346d7df2c71511065bf99f571ac1c615e3fc1d4ce17ca5f9  -
```

- [ ] **Step 8: Format and run focused service tests**

```bash
gofmt -w internal/model/api.go internal/service/note_service.go internal/service/note_revision.go internal/service/note_revision_test.go
go test ./internal/service -run 'TestNoteService(GetNote|SaveNote|ConflictPolicy)' -count=1
```

Expected: exit status `0` and `ok IGoNotes/internal/service`.

- [ ] **Step 9: Run the complete service package under the race detector**

```bash
go test -race ./internal/service -count=1
```

Expected: exit status `0` with no `DATA RACE`.

- [ ] **Step 10: Commit on `plan2-note-revisions`**

```bash
git add internal/model/api.go internal/service/note_service.go internal/service/note_revision.go internal/service/note_revision_test.go
git commit -m "feat: add note revision checks"
```

### Task 6: Expose Revisions and Conflict Errors Through HTTP

**Files:**

- Modify: `internal/handlers/note_handler.go:67-94, 123-158, 188-327`
- Modify: `internal/handlers/note_handler_test.go`
- Modify: `internal/handlers/errors.go:21-36`
- Modify: `internal/handlers/errors_test.go`

- [ ] **Step 1: Write failing GET and compatibility tests**

Add to `internal/handlers/note_handler_test.go`:

```go
func TestNoteHandlerGetNoteReturnsRevision(t *testing.T) {
	basePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(basePath, "idea.md"), []byte("# Idea\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	coordinator := service.NewBaseOperationCoordinator()
	notes := service.NewNoteService(handlerNoteRepository{}, basePath, coordinator)
	t.Cleanup(func() { _ = notes.Close() })

	recorder := httptest.NewRecorder()
	NewNoteHandler(notes).GetNote(recorder, httptest.NewRequest(http.MethodGet, "/api/note?id=idea.md", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	var response model.NoteContentResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response.ID != "idea.md" || response.Content != "# Idea\n" {
		t.Errorf("response = %#v", response)
	}
	if response.Revision != "sha256:25ec1fffebeb4b54346d7df2c71511065bf99f571ac1c615e3fc1d4ce17ca5f9" {
		t.Errorf("revision = %q", response.Revision)
	}
}

func TestNoteHandlerSaveWithoutExpectedRevisionRemainsCompatible(t *testing.T) {
	basePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(basePath, "idea.md"), []byte("old"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	coordinator := service.NewBaseOperationCoordinator()
	notes := service.NewNoteService(handlerNoteRepository{}, basePath, coordinator)
	t.Cleanup(func() { _ = notes.Close() })

	recorder := httptest.NewRecorder()
	NewNoteHandler(notes).SaveNote(recorder, httptest.NewRequest(
		http.MethodPost,
		"/api/save",
		strings.NewReader(`{"id":"idea.md","content":"legacy"}`),
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	var response model.SaveNoteResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response.Status != "saved" || response.Revision == "" {
		t.Errorf("response = %#v", response)
	}
	content, err := os.ReadFile(filepath.Join(basePath, "idea.md"))
	if err != nil || string(content) != "legacy" {
		t.Errorf("saved content = %q, %v", content, err)
	}
}
```

- [ ] **Step 2: Write a failing stale-save handler test**

Add:

```go
func TestNoteHandlerSaveReturnsNoteChangedWithoutWriting(t *testing.T) {
	basePath := t.TempDir()
	notePath := filepath.Join(basePath, "idea.md")
	if err := os.WriteFile(notePath, []byte("incoming"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	coordinator := service.NewBaseOperationCoordinator()
	notes := service.NewNoteService(handlerNoteRepository{}, basePath, coordinator)
	t.Cleanup(func() { _ = notes.Close() })

	recorder := httptest.NewRecorder()
	NewNoteHandler(notes).SaveNote(recorder, httptest.NewRequest(
		http.MethodPost,
		"/api/save",
		strings.NewReader(`{"id":"idea.md","content":"browser buffer","expected_revision":"sha256:stale"}`),
	))
	assertAPIErrorResponse(t, recorder, http.StatusConflict, model.APIError{
		Code: "note_changed",
		Message: "note changed",
	})
	content, err := os.ReadFile(notePath)
	if err != nil || string(content) != "incoming" {
		t.Errorf("content after stale save = %q, %v", content, err)
	}
}
```

- [ ] **Step 3: Run focused handler tests and confirm response failures**

```bash
go test ./internal/handlers -run 'TestNoteHandler(GetNoteReturnsRevision|SaveWithoutExpectedRevision|SaveReturnsNoteChanged)' -count=1
```

Expected: test failure because handlers do not yet return revisions or map stale saves to `note_changed`.

- [ ] **Step 4: Add the two service error mappings**

Add to `serviceErrorMappings` in `internal/handlers/errors.go` before generic base conflicts:

```go
{service.ErrGitConflictPending, http.StatusConflict, "git_conflict_pending", service.ErrGitConflictPending.Error()},
{service.ErrNoteChanged, http.StatusConflict, "note_changed", service.ErrNoteChanged.Error()},
```

Add these cases to the existing table in `internal/handlers/errors_test.go`:

```go
{name: "git conflict pending", err: fmt.Errorf("mutation rejected: %w", service.ErrGitConflictPending), status: http.StatusConflict, want: model.APIError{Code: "git_conflict_pending", Message: "git conflict pending"}},
{name: "note changed", err: fmt.Errorf("save rejected: %w", service.ErrNoteChanged), status: http.StatusConflict, want: model.APIError{Code: "note_changed", Message: "note changed"}},
```

- [ ] **Step 5: Make GET and save use revision-aware methods**

Replace the service call and response in `NoteHandler.GetNote`:

```go
note, err := h.NoteService.GetNote(id)
if err != nil {
	if errors.Is(err, os.ErrNotExist) {
		WriteAPIError(w, http.StatusNotFound, "note_not_found", "Note not found", "id")
		return
	}
	if errors.Is(err, service.ErrInvalidNotePath) {
		WriteAPIError(w, http.StatusBadRequest, "invalid_path", "Invalid path", "id")
		return
	}
	WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
	return
}
writeJSON(w, http.StatusOK, note)
```

Replace the service call and successful response in `NoteHandler.SaveNote`:

```go
response, err := h.NoteService.SaveNote(req)
if err != nil {
	if errors.Is(err, service.ErrNoteChanged) || errors.Is(err, service.ErrGitConflictPending) {
		writeServiceError(w, err)
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		WriteAPIError(w, http.StatusNotFound, "note_not_found", "Note not found", "id")
		return
	}
	if errors.Is(err, service.ErrInvalidNotePath) {
		WriteAPIError(w, http.StatusBadRequest, "invalid_path", "Invalid path", "id")
		return
	}
	WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
	return
}
writeJSON(w, http.StatusOK, response)
```

- [ ] **Step 6: Map conflict errors from all remaining mutation handlers**

Before the generic internal-error branch in `CreateNote`, `DeleteNote`, `RenameNote`, and `UploadAsset`, add:

```go
if errors.Is(err, service.ErrGitConflictPending) {
	writeServiceError(w, err)
	return
}
```

Do not alter existing `note_conflict`, not-found, invalid-path, multipart-size, or method handling.

- [ ] **Step 7: Add an HTTP conflict-policy test**

Add to `internal/handlers/note_handler_test.go`:

```go
func TestNoteHandlerSaveReturnsGitConflictPending(t *testing.T) {
	basePath := t.TempDir()
	notePath := filepath.Join(basePath, "idea.md")
	if err := os.WriteFile(notePath, []byte("original"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	coordinator := service.NewBaseOperationCoordinator()
	notes := service.NewNoteService(handlerNoteRepository{}, basePath, coordinator)
	t.Cleanup(func() { _ = notes.Close() })
	coordinator.SetConflict(basePath, true)

	recorder := httptest.NewRecorder()
	NewNoteHandler(notes).SaveNote(recorder, httptest.NewRequest(
		http.MethodPost,
		"/api/save",
		strings.NewReader(`{"id":"idea.md","content":"changed"}`),
	))
	assertAPIErrorResponse(t, recorder, http.StatusConflict, model.APIError{
		Code: "git_conflict_pending",
		Message: "git conflict pending",
	})
	content, err := os.ReadFile(notePath)
	if err != nil || string(content) != "original" {
		t.Errorf("content after blocked save = %q, %v", content, err)
	}
}
```

- [ ] **Step 8: Format and run all handler tests**

```bash
gofmt -w internal/handlers/note_handler.go internal/handlers/note_handler_test.go internal/handlers/errors.go internal/handlers/errors_test.go
go test ./internal/handlers -count=1
```

Expected: exit status `0` and `ok IGoNotes/internal/handlers`.

- [ ] **Step 9: Verify existing guards and structured errors still pass**

```bash
go test ./internal/handlers -run 'Test(Router|NoteHandler|WriteServiceError)' -count=1
```

Expected: exit status `0`; existing local-origin, setup, method, and error tests pass.

- [ ] **Step 10: Commit**

```bash
git add internal/handlers/note_handler.go internal/handlers/note_handler_test.go internal/handlers/errors.go internal/handlers/errors_test.go
git commit -m "feat: expose note revisions over API"
```

### Task 7: Prove the Full Lock Order and Run Final Verification

**Files:**

- Create: `internal/service/base_operation_integration_test.go`

- [ ] **Step 1: Write the complete lock-order integration test**

Create `internal/service/base_operation_integration_test.go`:

```go
package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"IGoNotes/internal/model"
)

func TestBaseOperationLockOrderCoordinatesFilesystemMutationAndSwitch(t *testing.T) {
	activePath := t.TempDir()
	targetPath := t.TempDir()
	writeTestNote(t, activePath, "active.md", "active")
	writeTestNote(t, targetPath, "target.md", "target")

	coordinator := NewBaseOperationCoordinator()
	repo := &fakeNoteRepository{}
	notes := newTestNoteServiceWithCoordinator(t, repo, activePath, coordinator)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	completed := true
	config := model.Config{
		Bases: []model.Base{{Name: "active", Path: activePath}, {Name: "target", Path: targetPath}},
		CurrentBase: "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config}
	settings, err := NewSettingsService(store, notes, coordinator, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	mutationStarted := make(chan struct{})
	releaseMutation := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		coordinator.Lock()
		defer coordinator.Unlock()
		mutationDone <- notes.MutateActiveFilesystem(activePath, func(canonicalPath string) error {
			close(mutationStarted)
			<-releaseMutation
			return os.WriteFile(filepath.Join(canonicalPath, "incoming.md"), []byte("incoming"), 0o600)
		})
	}()
	<-mutationStarted

	switchDone := make(chan error, 1)
	go func() {
		_, err := settings.SwitchBase("target")
		switchDone <- err
	}()
	for range 20 {
		runtime.Gosched()
	}

	if !settings.mu.TryLock() {
		close(releaseMutation)
		<-mutationDone
		<-switchDone
		t.Fatal("SwitchBase took SettingsService.mu while waiting for the coordinator")
	}
	settings.mu.Unlock()
	if notes.baseMu.TryRLock() {
		notes.baseMu.RUnlock()
		close(releaseMutation)
		<-mutationDone
		<-switchDone
		t.Fatal("active filesystem transaction did not hold NoteService.baseMu")
	}

	close(releaseMutation)
	if err := <-mutationDone; err != nil {
		t.Fatalf("MutateActiveFilesystem() error = %v", err)
	}
	if err := <-switchDone; err != nil {
		t.Fatalf("SwitchBase() error = %v", err)
	}
	if got := notes.GetBasePath(); got != targetPath {
		t.Errorf("active path = %q, want %q", got, targetPath)
	}
	assertRepositoryIDs(t, repo, "target.md")
	assertFileContent(t, filepath.Join(activePath, "incoming.md"), []byte("incoming"))
}
```

- [ ] **Step 2: Run the focused integration test repeatedly**

```bash
go test ./internal/service -run '^TestBaseOperationLockOrderCoordinatesFilesystemMutationAndSwitch$' -count=20
```

Expected: exit status `0` across all runs, with no deadlock or timeout.

- [ ] **Step 3: Run the integration test with the race detector**

```bash
go test -race ./internal/service -run '^TestBaseOperationLockOrderCoordinatesFilesystemMutationAndSwitch$' -count=10
```

Expected: exit status `0` with no `DATA RACE`.

- [ ] **Step 4: Commit the integration test**

```bash
git add internal/service/base_operation_integration_test.go
git commit -m "test: verify base operation lock order"
```

- [ ] **Step 5: Run all Go tests**

```bash
go test ./...
```

Expected: exit status `0`; all packages report `ok` or `[no test files]`.

- [ ] **Step 6: Run all Go tests under the race detector**

```bash
go test -race ./...
```

Expected: exit status `0` with no race, deadlock, timeout, or `FAIL` output.

- [ ] **Step 7: Run static analysis**

```bash
go vet ./...
```

Expected: exit status `0` with no diagnostics.

- [ ] **Step 8: Build the Go application**

```bash
go build ./cmd/...
```

Expected: exit status `0` with no compiler output.

- [ ] **Step 9: Verify the production coordinator is shared**

```bash
git grep -n -E 'NewBaseOperationCoordinator|NewNoteService|NewSettingsService' -- cmd/api/main.go
```

Expected: one `NewBaseOperationCoordinator` call in `runServer`; the same variable is passed to `NewNoteService` and production's `NewSettingsServiceWithGit`, followed by the existing Plan 1 validator and status repository dependencies.

- [ ] **Step 10: Verify Plan 2 did not add Git execution or frontend changes**

```bash
git status --short
git diff --name-only develop...HEAD
```

Expected: implementation changes are limited to the Go files named by this plan; no path under `web/` is listed.

```bash
git diff develop...HEAD -- internal/service internal/handlers cmd/api
```

Expected: manual review finds no Plan 2 addition that launches Git directly or adds a Git HTTP route. Git foundation code inherited from Plan 1 is not modified by this plan.
