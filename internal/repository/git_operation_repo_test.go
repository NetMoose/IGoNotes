package repository

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	gitcmd "IGoNotes/internal/git"
)

func TestGitOperationRepositoryCreateCheckpointFinish(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	repo := NewGitOperationRepository(db)
	ctx := context.Background()
	createdAt := time.Date(2026, time.September, 3, 10, 11, 12, 123456789, time.UTC)
	operation := completeGitOperation("operation-1", "/notes/work", createdAt)
	operation.State = gitcmd.OperationQueued
	operation.Stage = gitcmd.StageQueued
	operation.UpdatedAt = createdAt
	operation.BackupRef = ""
	operation.CandidateOID = ""
	operation.PushOID = ""
	operation.ChangedPaths = []string{"z.md", "a.md", "z.md"}
	operation.ConflictPaths = nil
	operation.Error = nil
	if err := repo.CreateQueued(ctx, operation); err != nil {
		t.Fatalf("CreateQueued() error = %v", err)
	}

	checkpoint := gitcmd.Checkpoint{
		Stage:         gitcmd.StageMerging,
		BackupRef:     "refs/igonotes/backups/20260903T101112.123456789Z",
		LocalOID:      "1111111111111111111111111111111111111111",
		CandidateOID:  "2222222222222222222222222222222222222222",
		RemoteOID:     "3333333333333333333333333333333333333333",
		PushOID:       "4444444444444444444444444444444444444444",
		ChangedPaths:  []string{"nested/b.md", "a.md", "nested/b.md"},
		ConflictPaths: []string{"conflict/z.md", "conflict/a.md", "conflict/z.md"},
	}
	if err := repo.Checkpoint(ctx, operation.ID, checkpoint); err != nil {
		t.Fatalf("Checkpoint() error = %v", err)
	}
	running, found, err := repo.ActiveByPath(ctx, operation.RepoPath)
	if err != nil || !found {
		t.Fatalf("ActiveByPath() = %#v, %v, %v", running, found, err)
	}
	if running.State != gitcmd.OperationRunning || running.Stage != checkpoint.Stage {
		t.Fatalf("checkpointed state/stage = %q/%q, want running/%q", running.State, running.Stage, checkpoint.Stage)
	}
	if !reflect.DeepEqual(running.ChangedPaths, []string{"a.md", "nested/b.md"}) ||
		!reflect.DeepEqual(running.ConflictPaths, []string{"conflict/a.md", "conflict/z.md"}) {
		t.Fatalf("checkpoint paths = %#v / %#v", running.ChangedPaths, running.ConflictPaths)
	}

	finishedAt := time.Date(2026, time.September, 3, 10, 12, 13, 987654321, time.UTC)
	want := operation
	want.State = gitcmd.OperationConflict
	want.Stage = gitcmd.StageReindexing
	want.BackupRef = checkpoint.BackupRef
	want.LocalOID = checkpoint.LocalOID
	want.CandidateOID = checkpoint.CandidateOID
	want.RemoteOID = checkpoint.RemoteOID
	want.PushOID = checkpoint.PushOID
	want.ChangedPaths = []string{"b.md", "a.md", "b.md"}
	want.ConflictPaths = []string{"conflict/z.md", "conflict/a.md", "conflict/z.md"}
	want.Error = &gitcmd.SafeError{Code: gitcmd.CodeGitConflict, Message: "Git merge has conflicts", Field: "git_branch", ExitCode: 1}
	want.UpdatedAt = finishedAt
	if err := repo.Finish(ctx, want); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	db, err = InitDB(dbPath)
	if err != nil {
		t.Fatalf("reopen InitDB() error = %v", err)
	}
	defer db.Close()
	repo = NewGitOperationRepository(db)
	got, found, err := repo.LatestByPath(ctx, operation.RepoPath)
	if err != nil || !found {
		t.Fatalf("LatestByPath() = %#v, %v, %v", got, found, err)
	}
	want.ChangedPaths = []string{"a.md", "b.md"}
	want.ConflictPaths = []string{"conflict/a.md", "conflict/z.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened operation mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestGitOperationRepositoryFinishPreservesAdmissionAndCheckpointData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	ctx := context.Background()
	repo := NewGitOperationRepository(db)
	createdAt := time.Date(2026, time.September, 3, 8, 9, 10, 123456789, time.UTC)
	admitted := completeGitOperation("preserve-finish", "/notes/admitted", createdAt)
	admitted.BaseName = "admitted-base"
	admitted.ConfigFingerprint = "admitted-config"
	admitted.RemoteFingerprint = "admitted-remote"
	admitted.Kind = gitcmd.OperationInitialize
	admitted.Branch = "admitted-branch"
	if err := repo.CreateQueued(ctx, admitted); err != nil {
		t.Fatalf("CreateQueued() error = %v", err)
	}

	checkpoint := gitcmd.Checkpoint{
		Stage:         gitcmd.StagePushing,
		BackupRef:     "refs/igonotes/backups/checkpoint",
		LocalOID:      "1111111111111111111111111111111111111111",
		CandidateOID:  "2222222222222222222222222222222222222222",
		RemoteOID:     "3333333333333333333333333333333333333333",
		PushOID:       "4444444444444444444444444444444444444444",
		ChangedPaths:  []string{"checkpoint/z.md", "checkpoint/a.md", "checkpoint/z.md"},
		ConflictPaths: []string{"conflict/z.md", "conflict/a.md", "conflict/z.md"},
	}
	if err := repo.Checkpoint(ctx, admitted.ID, checkpoint); err != nil {
		t.Fatalf("Checkpoint() error = %v", err)
	}

	finishedAt := time.Date(2026, time.September, 3, 8, 10, 11, 987654321, time.UTC)
	stale := admitted
	stale.BaseName = "moved-base"
	stale.RepoPath = "/notes/moved"
	stale.ConfigFingerprint = "moved-config"
	stale.RemoteFingerprint = "moved-remote"
	stale.Kind = gitcmd.OperationSync
	stale.Branch = "moved-branch"
	stale.CreatedAt = createdAt.Add(24 * time.Hour)
	stale.State = gitcmd.OperationConflict
	stale.Stage = gitcmd.StageCompleted
	stale.BackupRef = ""
	stale.LocalOID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	stale.CandidateOID = ""
	stale.RemoteOID = "cccccccccccccccccccccccccccccccccccccccc"
	stale.PushOID = ""
	stale.ChangedPaths = nil
	stale.ConflictPaths = nil
	stale.Error = &gitcmd.SafeError{
		Code: gitcmd.CodeGitConflict, Message: "Git merge has conflicts", Field: "git_branch", ExitCode: 1,
	}
	stale.UpdatedAt = finishedAt
	if err := repo.Finish(ctx, stale); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	db, err = InitDB(dbPath)
	if err != nil {
		t.Fatalf("reopen InitDB() error = %v", err)
	}
	defer db.Close()
	repo = NewGitOperationRepository(db)
	got, found, err := repo.LatestByPath(ctx, admitted.RepoPath)
	if err != nil || !found {
		t.Fatalf("LatestByPath() = %#v, %v, %v", got, found, err)
	}
	want := admitted
	want.State = stale.State
	want.Stage = stale.Stage
	want.BackupRef = checkpoint.BackupRef
	want.LocalOID = checkpoint.LocalOID
	want.CandidateOID = checkpoint.CandidateOID
	want.RemoteOID = checkpoint.RemoteOID
	want.PushOID = checkpoint.PushOID
	want.ChangedPaths = []string{"checkpoint/a.md", "checkpoint/z.md"}
	want.ConflictPaths = []string{"conflict/a.md", "conflict/z.md"}
	want.Error = stale.Error
	want.UpdatedAt = finishedAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("finished operation mismatch:\n got: %#v\nwant: %#v", got, want)
	}
	if err := repo.Finish(ctx, stale); !errors.Is(err, ErrGitOperationTransition) {
		t.Fatalf("repeated Finish() error = %v, want ErrGitOperationTransition", err)
	}
}

func TestGitOperationRepositoryFinishExplicitEmptyPathsAndFillsMissingCheckpointData(t *testing.T) {
	repo, db := openTestGitOperationRepository(t)
	defer db.Close()
	ctx := context.Background()
	operation := completeGitOperation("finish-empty-paths", "/notes/finish-empty", time.Now().UTC())
	operation.BackupRef = ""
	operation.LocalOID = ""
	operation.CandidateOID = ""
	operation.RemoteOID = ""
	operation.PushOID = ""
	if err := repo.CreateQueued(ctx, operation); err != nil {
		t.Fatalf("CreateQueued() error = %v", err)
	}
	if err := repo.Checkpoint(ctx, operation.ID, gitcmd.Checkpoint{
		Stage:         gitcmd.StageMerging,
		ChangedPaths:  []string{"changed.md"},
		ConflictPaths: []string{"conflict.md"},
	}); err != nil {
		t.Fatalf("Checkpoint() error = %v", err)
	}

	operation.State = gitcmd.OperationSucceeded
	operation.Stage = gitcmd.StageCompleted
	operation.BackupRef = "refs/igonotes/backups/terminal"
	operation.LocalOID = "1111111111111111111111111111111111111111"
	operation.CandidateOID = "2222222222222222222222222222222222222222"
	operation.RemoteOID = "3333333333333333333333333333333333333333"
	operation.PushOID = "4444444444444444444444444444444444444444"
	operation.ChangedPaths = []string{}
	operation.ConflictPaths = []string{}
	operation.Error = nil
	operation.UpdatedAt = operation.CreatedAt.Add(time.Second)
	if err := repo.Finish(ctx, operation); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	got, found, err := repo.LatestByPath(ctx, operation.RepoPath)
	if err != nil || !found {
		t.Fatalf("LatestByPath() = %#v, %v, %v", got, found, err)
	}
	if got.BackupRef != operation.BackupRef || got.LocalOID != operation.LocalOID ||
		got.CandidateOID != operation.CandidateOID || got.RemoteOID != operation.RemoteOID || got.PushOID != operation.PushOID {
		t.Errorf("terminal checkpoint fills = %q/%q/%q/%q/%q", got.BackupRef, got.LocalOID, got.CandidateOID, got.RemoteOID, got.PushOID)
	}
	if got.ChangedPaths == nil || len(got.ChangedPaths) != 0 {
		t.Errorf("ChangedPaths = %#v, want non-nil empty", got.ChangedPaths)
	}
	if got.ConflictPaths == nil || len(got.ConflictPaths) != 0 {
		t.Errorf("ConflictPaths = %#v, want non-nil empty", got.ConflictPaths)
	}
}

func TestGitOperationRepositoryPreservesUTCNanosecondCreationTime(t *testing.T) {
	repo, db := openTestGitOperationRepository(t)
	defer db.Close()
	ctx := context.Background()
	createdAt := time.Date(2026, time.September, 3, 22, 23, 24, 100200300, time.FixedZone("source", -7*60*60))
	wantInstant := createdAt.UTC()
	operation := completeGitOperation("timestamp", "/notes/timestamp", createdAt)
	operation.State = gitcmd.OperationQueued
	operation.Stage = gitcmd.StageQueued
	operation.UpdatedAt = createdAt
	if err := repo.CreateQueued(ctx, operation); err != nil {
		t.Fatalf("CreateQueued() error = %v", err)
	}

	active, found, err := repo.ActiveByPath(ctx, operation.RepoPath)
	if err != nil || !found {
		t.Fatalf("ActiveByPath() = %#v, %v, %v", active, found, err)
	}
	latest, found, err := repo.LatestByPath(ctx, operation.RepoPath)
	if err != nil || !found {
		t.Fatalf("LatestByPath() = %#v, %v, %v", latest, found, err)
	}
	unfinished, err := repo.ListUnfinished(ctx)
	if err != nil || len(unfinished) != 1 {
		t.Fatalf("ListUnfinished() = %#v, %v", unfinished, err)
	}
	for name, got := range map[string]time.Time{
		"ActiveByPath CreatedAt":   active.CreatedAt,
		"ActiveByPath UpdatedAt":   active.UpdatedAt,
		"LatestByPath CreatedAt":   latest.CreatedAt,
		"LatestByPath UpdatedAt":   latest.UpdatedAt,
		"ListUnfinished CreatedAt": unfinished[0].CreatedAt,
		"ListUnfinished UpdatedAt": unfinished[0].UpdatedAt,
	} {
		if !got.Equal(wantInstant) || got.Location() != time.UTC || got.Nanosecond() != wantInstant.Nanosecond() {
			t.Errorf("%s = %s (%s), want exact UTC %s", name, got, got.Location(), wantInstant)
		}
	}
}

func TestGitOperationRepositoryRejectsSecondActivePath(t *testing.T) {
	repo, db := openTestGitOperationRepository(t)
	defer db.Close()
	ctx := context.Background()
	createdAt := time.Now().UTC()
	first := completeGitOperation("first", "/notes/shared", createdAt)
	second := completeGitOperation("second", first.RepoPath, createdAt.Add(time.Nanosecond))
	if err := repo.CreateQueued(ctx, first); err != nil {
		t.Fatalf("first CreateQueued() error = %v", err)
	}
	if err := repo.CreateQueued(ctx, second); err == nil {
		t.Fatal("second CreateQueued() error = nil, want unique active-path error")
	} else if errors.Is(err, ErrGitOperationTransition) {
		t.Fatalf("second CreateQueued() error = %v, must preserve SQL uniqueness error", err)
	}
}

func TestGitOperationRepositoryReturnsActiveOperation(t *testing.T) {
	repo, db := openTestGitOperationRepository(t)
	defer db.Close()
	ctx := context.Background()
	operation := completeGitOperation("active", "/notes/active", time.Now().UTC())
	if err := repo.CreateQueued(ctx, operation); err != nil {
		t.Fatal(err)
	}
	got, found, err := repo.ActiveByPath(ctx, operation.RepoPath)
	if err != nil || !found || got.ID != operation.ID || got.State != gitcmd.OperationQueued {
		t.Fatalf("ActiveByPath() = %#v, %v, %v", got, found, err)
	}
	operation.State = gitcmd.OperationSucceeded
	operation.Stage = gitcmd.StageCompleted
	operation.UpdatedAt = operation.CreatedAt.Add(time.Second)
	if err := repo.Finish(ctx, operation); err != nil {
		t.Fatal(err)
	}
	if got, found, err := repo.ActiveByPath(ctx, operation.RepoPath); err != nil || found || !reflect.DeepEqual(got, gitcmd.Operation{}) {
		t.Fatalf("ActiveByPath() after finish = %#v, %v, %v", got, found, err)
	}
	if got, found, err := repo.ActiveByPath(ctx, "/notes/missing"); err != nil || found || !reflect.DeepEqual(got, gitcmd.Operation{}) {
		t.Fatalf("ActiveByPath() missing = %#v, %v, %v", got, found, err)
	}
}

func TestGitOperationRepositoryReturnsLatestOperationByPath(t *testing.T) {
	repo, db := openTestGitOperationRepository(t)
	defer db.Close()
	ctx := context.Background()
	baseTime := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)
	for _, operation := range []gitcmd.Operation{
		completeGitOperation("newer-row-first", "/notes/latest", baseTime.Add(900*time.Millisecond)),
		completeGitOperation("older", "/notes/latest", baseTime.Add(100*time.Millisecond)),
		completeGitOperation("newest", "/notes/latest", baseTime.Add(time.Second)),
	} {
		if err := repo.CreateQueued(ctx, operation); err != nil {
			t.Fatal(err)
		}
		operation.State = gitcmd.OperationSucceeded
		operation.Stage = gitcmd.StageCompleted
		if err := repo.Finish(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}
	got, found, err := repo.LatestByPath(ctx, "/notes/latest")
	if err != nil || !found || got.ID != "newest" {
		t.Fatalf("LatestByPath() = %#v, %v, %v; want newest", got, found, err)
	}
	if got, found, err := repo.LatestByPath(ctx, "/notes/missing"); err != nil || found || !reflect.DeepEqual(got, gitcmd.Operation{}) {
		t.Fatalf("LatestByPath() missing = %#v, %v, %v", got, found, err)
	}
}

func TestGitOperationRepositoryListsUnfinishedOperations(t *testing.T) {
	repo, db := openTestGitOperationRepository(t)
	defer db.Close()
	ctx := context.Background()
	baseTime := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)
	operations := []gitcmd.Operation{
		completeGitOperation("second", "/notes/b", baseTime.Add(2*time.Second)),
		completeGitOperation("finished", "/notes/c", baseTime.Add(3*time.Second)),
		completeGitOperation("first", "/notes/a", baseTime.Add(time.Second)),
	}
	for _, operation := range operations {
		if err := repo.CreateQueued(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}
	operations[1].State = gitcmd.OperationFailed
	operations[1].Stage = gitcmd.StageProbing
	if err := repo.Finish(ctx, operations[1]); err != nil {
		t.Fatal(err)
	}
	if err := repo.Checkpoint(ctx, operations[0].ID, gitcmd.Checkpoint{Stage: gitcmd.StageFetching}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListUnfinished(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "first" || got[1].ID != "second" || got[1].State != gitcmd.OperationRunning {
		t.Fatalf("ListUnfinished() = %#v, want first then second", got)
	}
}

func TestGitOperationRepositoryPersistsConflictPaths(t *testing.T) {
	repo, db := openTestGitOperationRepository(t)
	defer db.Close()
	ctx := context.Background()
	operation := completeGitOperation("conflicts", "/notes/conflicts", time.Now().UTC())
	operation.ConflictPaths = []string{"z.md", "a.md", "z.md"}
	if err := repo.CreateQueued(ctx, operation); err != nil {
		t.Fatal(err)
	}
	operation.State = gitcmd.OperationConflict
	operation.ConflictPaths = []string{"nested/z.md", "a.md", "nested/z.md"}
	operation.ChangedPaths = []string{"changed.md", "a.md", "changed.md"}
	if err := repo.Finish(ctx, operation); err != nil {
		t.Fatal(err)
	}
	got, found, err := repo.LatestByPath(ctx, operation.RepoPath)
	if err != nil || !found {
		t.Fatalf("LatestByPath() = %#v, %v, %v", got, found, err)
	}
	if !reflect.DeepEqual(got.ChangedPaths, []string{"a.md", "changed.md"}) {
		t.Errorf("ChangedPaths = %#v", got.ChangedPaths)
	}
	if !reflect.DeepEqual(got.ConflictPaths, []string{"a.md", "nested/z.md"}) {
		t.Errorf("ConflictPaths = %#v", got.ConflictPaths)
	}
}

func TestGitOperationRepositoryRejectsTerminalTransition(t *testing.T) {
	repo, db := openTestGitOperationRepository(t)
	defer db.Close()
	ctx := context.Background()
	operation := completeGitOperation("terminal", "/notes/terminal", time.Now().UTC())
	if err := repo.CreateQueued(ctx, operation); err != nil {
		t.Fatal(err)
	}
	operation.State = gitcmd.OperationSucceeded
	operation.Stage = gitcmd.StageCompleted
	if err := repo.Finish(ctx, operation); err != nil {
		t.Fatal(err)
	}
	if err := repo.Checkpoint(ctx, operation.ID, gitcmd.Checkpoint{Stage: gitcmd.StageFetching}); !errors.Is(err, ErrGitOperationTransition) {
		t.Fatalf("Checkpoint() error = %v, want ErrGitOperationTransition", err)
	}
	operation.State = gitcmd.OperationFailed
	if err := repo.Finish(ctx, operation); !errors.Is(err, ErrGitOperationTransition) {
		t.Fatalf("second Finish() error = %v, want ErrGitOperationTransition", err)
	}
	nonterminal := completeGitOperation("nonterminal", "/notes/nonterminal", time.Now().UTC())
	if err := repo.CreateQueued(ctx, nonterminal); err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, nonterminal); !errors.Is(err, ErrGitOperationTransition) {
		t.Fatalf("nonterminal Finish() error = %v, want ErrGitOperationTransition", err)
	}
	if err := repo.Checkpoint(ctx, "missing", gitcmd.Checkpoint{Stage: gitcmd.StageFetching}); !errors.Is(err, ErrGitOperationTransition) {
		t.Fatalf("missing Checkpoint() error = %v, want ErrGitOperationTransition", err)
	}
}

func completeGitOperation(id, path string, instant time.Time) gitcmd.Operation {
	return gitcmd.Operation{
		ID:                id,
		BaseName:          "work-notes",
		RepoPath:          path,
		ConfigFingerprint: "config-fingerprint",
		RemoteFingerprint: "remote-fingerprint",
		Kind:              gitcmd.OperationSync,
		State:             gitcmd.OperationQueued,
		Stage:             gitcmd.StageQueued,
		Branch:            "main",
		BackupRef:         "refs/igonotes/backups/test",
		LocalOID:          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CandidateOID:      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		RemoteOID:         "cccccccccccccccccccccccccccccccccccccccc",
		PushOID:           "dddddddddddddddddddddddddddddddddddddddd",
		ChangedPaths:      []string{},
		ConflictPaths:     []string{},
		Error:             &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Git command failed", Field: "git_url", ExitCode: 23},
		CreatedAt:         instant,
		UpdatedAt:         instant,
	}
}

func openTestGitOperationRepository(t *testing.T) (*GitOperationRepository, interface{ Close() error }) {
	t.Helper()
	db, err := InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	return NewGitOperationRepository(db), db
}
