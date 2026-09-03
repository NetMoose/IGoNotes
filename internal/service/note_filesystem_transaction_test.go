package service

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
}

func TestNoteServiceMutateActiveFilesystemJoinsMutationAndIndexErrors(t *testing.T) {
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

	err := mutateActiveFilesystemWithCoordinator(service, basePath, func(string) error {
		return mutationErr
	})
	if !errors.Is(err, mutationErr) || !errors.Is(err, indexErr) {
		t.Fatalf("MutateActiveFilesystem() error = %v, want mutation %v and index %v", err, mutationErr, indexErr)
	}
	if repo.prepareCalls != 2 {
		t.Errorf("index preparations = %d, want initial sync plus mandatory reindex", repo.prepareCalls)
	}
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
	t.Cleanup(releaseCallback)
	t.Cleanup(releaseReindex)
	repo.replaceStarted = reindexStarted
	repo.replaceRelease = reindexRelease

	transactionDone := make(chan error, 1)
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
	contentDone := make(chan noteReadResult, 1)
	go func() {
		content, err := service.GetNoteContent("incoming.md")
		contentDone <- noteReadResult{content: content, err: err}
	}()
	<-readAttempted
	treeStarted := make(chan struct{})
	treeDone := make(chan struct {
		nodes []model.NoteNode
		err   error
	}, 1)
	go func() {
		close(treeStarted)
		nodes, err := service.GetTree()
		treeDone <- struct {
			nodes []model.NoteNode
			err   error
		}{nodes: nodes, err: err}
	}()
	<-treeStarted
	for range 10 {
		runtime.Gosched()
	}
	assertFilesystemTransactionReadersBlocked(t, contentDone, treeDone, "callback")

	releaseCallback()
	<-reindexStarted
	assertFilesystemTransactionReadersBlocked(t, contentDone, treeDone, "reindex")

	releaseReindex()
	if err := <-transactionDone; err != nil {
		t.Fatalf("MutateActiveFilesystem() error = %v", err)
	}
	contentResult := <-contentDone
	if contentResult.err != nil || contentResult.content != "incoming" {
		t.Errorf("GetNoteContent() after transaction = %q, %v; want incoming, nil", contentResult.content, contentResult.err)
	}
	treeResult := <-treeDone
	if treeResult.err != nil {
		t.Fatalf("GetTree() after transaction error = %v", treeResult.err)
	}
	if len(treeResult.nodes) != 1 || treeResult.nodes[0].ID != "incoming.md" {
		t.Errorf("GetTree() after transaction = %#v, want incoming.md", treeResult.nodes)
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

func assertFilesystemTransactionReadersBlocked(
	t *testing.T,
	contentDone <-chan noteReadResult,
	treeDone <-chan struct {
		nodes []model.NoteNode
		err   error
	},
	phase string,
) {
	t.Helper()
	select {
	case result := <-contentDone:
		t.Fatalf("GetNoteContent() completed during %s: %#v", phase, result)
	default:
	}
	select {
	case result := <-treeDone:
		t.Fatalf("GetTree() completed during %s: %#v", phase, result)
	default:
	}
}
