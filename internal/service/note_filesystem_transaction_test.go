package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"IGoNotes/internal/model"
)

func TestNoteServiceMutateActiveFilesystemRebuildsIndexBeforeReturn(t *testing.T) {
	physicalPath := t.TempDir()
	writeTestNote(t, physicalPath, "old.md", "old")
	aliasPath := filepath.Join(t.TempDir(), "active")
	createSymlinkOrSkip(t, physicalPath, aliasPath)
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, aliasPath)
	if err := service.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}

	var callbackPath string
	err := mutateActiveFilesystemWithCoordinator(service, aliasPath, func(canonicalPath string) error {
		callbackPath = canonicalPath
		if err := os.Remove(filepath.Join(canonicalPath, "old.md")); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(canonicalPath, "incoming.md"), []byte("incoming"), 0o600)
	})
	if err != nil {
		t.Fatalf("MutateActiveFilesystem() error = %v", err)
	}
	if callbackPath != physicalPath {
		t.Errorf("callback path = %q, want canonical path %q", callbackPath, physicalPath)
	}
	if _, err := os.Stat(filepath.Join(physicalPath, "old.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old note Stat() error = %v, want os.ErrNotExist", err)
	}
	assertFileContent(t, filepath.Join(physicalPath, "incoming.md"), []byte("incoming"))
	assertRepositoryIDs(t, repo, "incoming.md")
	if repo.prepareCalls != 2 || repo.commitCalls != 2 {
		t.Errorf("index transactions = prepare %d commit %d, want initial sync plus mandatory reindex", repo.prepareCalls, repo.commitCalls)
	}
}

func TestNoteServiceMutateActiveFilesystemReindexesAfterCallbackError(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "old.md", "old")
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, basePath)
	if err := service.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	wantErr := errors.New("merge conflict")

	err := mutateActiveFilesystemWithCoordinator(service, basePath, func(canonicalPath string) error {
		if err := os.WriteFile(filepath.Join(canonicalPath, "conflicted.md"), []byte("conflicted"), 0o600); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("MutateActiveFilesystem() error = %v, want callback error %v", err, wantErr)
	}
	assertFileContent(t, filepath.Join(basePath, "conflicted.md"), []byte("conflicted"))
	assertRepositoryIDs(t, repo, "conflicted.md", "old.md")
	if repo.prepareCalls != 2 || repo.commitCalls != 2 {
		t.Errorf("index transactions = prepare %d commit %d, want initial sync plus mandatory reindex", repo.prepareCalls, repo.commitCalls)
	}
	if content, err := service.GetNoteContent("conflicted.md"); err != nil || content != "conflicted" {
		t.Errorf("GetNoteContent() after recoverable callback error = %q, %v; want conflicted, nil", content, err)
	}
	if _, err := service.GetTree(); err != nil {
		t.Errorf("GetTree() after recoverable callback error = %v, want nil", err)
	}
}

func TestNoteServiceMutateActiveFilesystemFailsClosedWhenIndexPreparationFails(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "old.md", "old")
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, basePath)
	if err := service.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	mutationErr := errors.New("mutation failed")
	indexErr := errors.New("index failed")
	repo.prepareErr = indexErr

	err := mutateActiveFilesystemWithCoordinator(service, basePath, func(canonicalPath string) error {
		if err := os.Remove(filepath.Join(canonicalPath, "old.md")); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(canonicalPath, "incoming.md"), []byte("incoming"), 0o600); err != nil {
			return err
		}
		return mutationErr
	})
	if !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, mutationErr) || !errors.Is(err, indexErr) {
		t.Fatalf("MutateActiveFilesystem() error = %v, want fail-closed mutation %v and index %v", err, mutationErr, indexErr)
	}
	if repo.prepareCalls != 2 {
		t.Errorf("index preparations = %d, want initial sync plus mandatory reindex", repo.prepareCalls)
	}
	assertFilesystemTransactionFailedClosed(t, service, repo, indexErr)
}

func TestNoteServiceMutateActiveFilesystemFailsClosedWhenScanFails(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "old.md", "old")
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, basePath)
	if err := service.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	scanErr := errors.New("scan failed")
	service.scan = func(*os.Root) ([]model.NoteNode, error) { return nil, scanErr }

	err := mutateActiveFilesystemWithCoordinator(service, basePath, replaceOldWithIncoming)
	if !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, scanErr) {
		t.Fatalf("MutateActiveFilesystem() error = %v, want fail-closed scan error %v", err, scanErr)
	}
	if repo.prepareCalls != 1 || repo.commitCalls != 1 {
		t.Errorf("index transactions = prepare %d commit %d, want initial sync only", repo.prepareCalls, repo.commitCalls)
	}
	assertFilesystemTransactionFailedClosed(t, service, repo, scanErr)
}

func TestNoteServiceMutateActiveFilesystemDoesNotDoubleWrapExistingFailClosedError(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "old.md", "old")
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, basePath)
	if err := service.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	commitErr := errors.New("commit failed")
	repo.commitErr = commitErr

	err := mutateActiveFilesystemWithCoordinator(service, basePath, replaceOldWithIncoming)
	if !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, commitErr) {
		t.Fatalf("MutateActiveFilesystem() error = %v, want existing fail-closed commit error %v", err, commitErr)
	}
	if got := strings.Count(service.baseErr.Error(), ErrRollbackFailed.Error()); got != 1 {
		t.Errorf("base error contains ErrRollbackFailed %d times, want one: %v", got, service.baseErr)
	}
	assertFilesystemTransactionFailedClosed(t, service, repo, commitErr)
}

func TestNoteServiceMutateActiveFilesystemRejectsInactiveOrChangedIdentity(t *testing.T) {
	t.Run("inactive path", func(t *testing.T) {
		activePath := t.TempDir()
		writeTestNote(t, activePath, "old.md", "old")
		inactivePath := t.TempDir()
		writeTestNote(t, inactivePath, "inactive.md", "inactive")
		repo := &fakeNoteRepository{}
		service := newTestNoteService(t, repo, activePath)
		if err := service.SyncFS(); err != nil {
			t.Fatalf("SyncFS() error = %v", err)
		}
		called := false

		err := mutateActiveFilesystemWithCoordinator(service, inactivePath, func(string) error {
			called = true
			return nil
		})
		if !errors.Is(err, ErrRuntimePathChanged) {
			t.Fatalf("MutateActiveFilesystem() error = %v, want ErrRuntimePathChanged", err)
		}
		if called {
			t.Error("callback was called for inactive path")
		}
		assertRepositoryIDs(t, repo, "old.md")
		if repo.prepareCalls != 1 || repo.commitCalls != 1 {
			t.Errorf("index transactions = prepare %d commit %d, want initial sync only", repo.prepareCalls, repo.commitCalls)
		}
	})

	t.Run("changed identity", func(t *testing.T) {
		parent := t.TempDir()
		activePath := filepath.Join(parent, "active")
		if err := os.Mkdir(activePath, 0o755); err != nil {
			t.Fatalf("Mkdir(active) error = %v", err)
		}
		writeTestNote(t, activePath, "old.md", "old")
		repo := &fakeNoteRepository{}
		service := newTestNoteService(t, repo, activePath)
		if err := service.SyncFS(); err != nil {
			t.Fatalf("SyncFS() error = %v", err)
		}
		displacedPath := filepath.Join(parent, "displaced")
		if err := os.Rename(activePath, displacedPath); err != nil {
			t.Fatalf("Rename(active, displaced) error = %v", err)
		}
		if err := os.Mkdir(activePath, 0o755); err != nil {
			t.Fatalf("Mkdir(replacement) error = %v", err)
		}
		writeTestNote(t, activePath, "replacement.md", "replacement")
		called := false

		err := mutateActiveFilesystemWithCoordinator(service, activePath, func(string) error {
			called = true
			return nil
		})
		if !errors.Is(err, ErrRuntimePathChanged) {
			t.Fatalf("MutateActiveFilesystem() error = %v, want ErrRuntimePathChanged", err)
		}
		if called {
			t.Error("callback was called after active path identity changed")
		}
		assertRepositoryIDs(t, repo, "old.md")
		if repo.prepareCalls != 1 || repo.commitCalls != 1 {
			t.Errorf("index transactions = prepare %d commit %d, want initial sync only", repo.prepareCalls, repo.commitCalls)
		}
		assertFileContent(t, filepath.Join(displacedPath, "old.md"), []byte("old"))
		assertFileContent(t, filepath.Join(activePath, "replacement.md"), []byte("replacement"))
	})
}

func TestNoteServiceMutateActiveFilesystemBypassesPendingConflict(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "old.md", "old")
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, basePath)
	if err := service.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	service.coordinator.SetConflict(basePath, true)

	err := mutateActiveFilesystemWithCoordinator(service, basePath, func(canonicalPath string) error {
		return os.WriteFile(filepath.Join(canonicalPath, "repaired.md"), []byte("repaired"), 0o600)
	})
	if err != nil {
		t.Fatalf("MutateActiveFilesystem() error = %v, want conflict bypass", err)
	}
	assertRepositoryIDs(t, repo, "old.md", "repaired.md")
}

func TestNoteServiceMutateActiveFilesystemBlocksReadersThroughReindex(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "old.md", "old")
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, basePath)
	if err := service.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}

	callbackMutation := make(chan error, 1)
	callbackRelease := make(chan struct{})
	reindexStarted := make(chan struct{})
	reindexRelease := make(chan struct{})
	var releaseCallbackOnce sync.Once
	var releaseReindexOnce sync.Once
	releaseCallback := func() { releaseCallbackOnce.Do(func() { close(callbackRelease) }) }
	releaseReindex := func() { releaseReindexOnce.Do(func() { close(reindexRelease) }) }
	repo.replaceStarted = reindexStarted
	repo.replaceRelease = reindexRelease

	transactionDone := make(chan error, 1)
	transactionStarted := true
	transactionJoined := false
	readerStarted := false
	readerJoined := false
	contentDone := make(chan noteReadResult, 1)
	t.Cleanup(func() {
		releaseCallback()
		releaseReindex()
		if transactionStarted && !transactionJoined {
			<-transactionDone
		}
		if readerStarted && !readerJoined {
			<-contentDone
		}
	})
	go func() {
		transactionDone <- mutateActiveFilesystemWithCoordinator(service, basePath, func(canonicalPath string) error {
			if err := os.Remove(filepath.Join(canonicalPath, "old.md")); err != nil {
				callbackMutation <- err
				return err
			}
			if err := os.WriteFile(filepath.Join(canonicalPath, "incoming.md"), []byte("incoming"), 0o600); err != nil {
				callbackMutation <- err
				return err
			}
			callbackMutation <- nil
			<-callbackRelease
			return nil
		})
	}()
	if err := <-callbackMutation; err != nil {
		t.Fatalf("callback mutation error = %v", err)
	}

	readAttempted := make(chan struct{})
	var readAttemptedOnce sync.Once
	service.beforeReadLock = func() { readAttemptedOnce.Do(func() { close(readAttempted) }) }
	readerStarted = true
	go func() {
		content, err := service.GetNoteContent("incoming.md")
		contentDone <- noteReadResult{content: content, err: err}
	}()
	<-readAttempted
	assertFilesystemTransactionReaderBlocked(t, contentDone, &readerJoined, "callback")

	releaseCallback()
	<-reindexStarted
	assertFilesystemTransactionReaderBlocked(t, contentDone, &readerJoined, "reindex")

	releaseReindex()
	err := <-transactionDone
	transactionJoined = true
	if err != nil {
		t.Fatalf("MutateActiveFilesystem() error = %v", err)
	}
	contentResult := <-contentDone
	readerJoined = true
	if contentResult.err != nil || contentResult.content != "incoming" {
		t.Errorf("GetNoteContent() after transaction = %q, %v; want incoming, nil", contentResult.content, contentResult.err)
	}
}

func TestNoteServiceMutateActiveFilesystemRejectsNilCallback(t *testing.T) {
	basePath := t.TempDir()
	writeTestNote(t, basePath, "old.md", "old")
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, basePath)
	if err := service.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}

	err := mutateActiveFilesystemWithCoordinator(service, basePath, nil)
	if !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("MutateActiveFilesystem() error = %v, want os.ErrInvalid", err)
	}
	assertFileContent(t, filepath.Join(basePath, "old.md"), []byte("old"))
	assertRepositoryIDs(t, repo, "old.md")
	if repo.prepareCalls != 1 || repo.commitCalls != 1 {
		t.Errorf("index transactions = prepare %d commit %d, want initial sync only", repo.prepareCalls, repo.commitCalls)
	}
}

func mutateActiveFilesystemWithCoordinator(service *NoteService, expectedPath string, mutate func(string) error) error {
	service.coordinator.Lock()
	defer service.coordinator.Unlock()
	return service.MutateActiveFilesystem(expectedPath, mutate)
}

func assertFilesystemTransactionReaderBlocked(t *testing.T, contentDone <-chan noteReadResult, readerJoined *bool, phase string) {
	t.Helper()
	select {
	case result := <-contentDone:
		*readerJoined = true
		t.Fatalf("GetNoteContent() completed during %s: %#v", phase, result)
	default:
	}
}

func replaceOldWithIncoming(canonicalPath string) error {
	if err := os.Remove(filepath.Join(canonicalPath, "old.md")); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(canonicalPath, "incoming.md"), []byte("incoming"), 0o600)
}

func assertFilesystemTransactionFailedClosed(t *testing.T, service *NoteService, repo *fakeNoteRepository, cause error) {
	t.Helper()
	if tree, err := service.GetTree(); tree != nil || !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, cause) {
		t.Errorf("GetTree() after reindex failure = %#v, %v; want nil and fail-closed cause", tree, err)
	}
	if content, err := service.GetNoteContent("incoming.md"); content != "" || !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, cause) {
		t.Errorf("GetNoteContent() after reindex failure = %q, %v; want empty and fail-closed cause", content, err)
	}
	if err := service.SaveNoteContent("incoming.md", "changed"); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, cause) {
		t.Errorf("SaveNoteContent() after reindex failure = %v; want fail-closed cause", err)
	}
	assertFileContent(t, filepath.Join(service.basePath, "incoming.md"), []byte("incoming"))
	assertRepositoryIDs(t, repo, "old.md")
}
