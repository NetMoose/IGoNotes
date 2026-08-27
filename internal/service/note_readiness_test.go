package service

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

func TestNoteServiceInitialSyncFailureHidesStaleIndexUntilRecovery(t *testing.T) {
	db, err := repository.InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()
	repo := repository.NewNoteRepository(db)
	if err := repo.ReplaceAll([]model.NoteNode{{ID: "old.md", Name: "old", Path: "old.md", Type: "file"}}); err != nil {
		t.Fatalf("seed ReplaceAll() error = %v", err)
	}

	base := t.TempDir()
	writeTestNote(t, base, "new.md", "new")
	notes := newTestNoteService(t, repo, base)
	wantErr := errors.New("initial scan failed")
	notes.scan = func(*os.Root) ([]model.NoteNode, error) { return nil, wantErr }

	if err := notes.SyncFS(); !errors.Is(err, wantErr) {
		t.Fatalf("SyncFS() error = %v, want %v", err, wantErr)
	}
	if tree, err := notes.GetTree(); !errors.Is(err, wantErr) || tree != nil {
		t.Fatalf("GetTree() = %#v, %v; want nil tree and %v", tree, err, wantErr)
	}

	notes.scan = scanNotes
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("recovery SyncFS() error = %v", err)
	}
	tree, err := notes.GetTree()
	if err != nil {
		t.Fatalf("GetTree() after recovery error = %v", err)
	}
	if len(tree) != 1 || tree[0].ID != "new.md" {
		t.Fatalf("GetTree() after recovery = %#v, want only new.md", tree)
	}
}

func TestNoteServiceFailedInitialSyncRetryKeepsReadinessError(t *testing.T) {
	firstErr := errors.New("first scan failed")
	retryErr := errors.New("retry scan failed")
	repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md", Name: "old", Path: "old.md", Type: "file"}}}
	notes := newTestNoteService(t, repo, t.TempDir())
	notes.scan = func(*os.Root) ([]model.NoteNode, error) { return nil, firstErr }

	if err := notes.SyncFS(); !errors.Is(err, firstErr) {
		t.Fatalf("first SyncFS() error = %v, want %v", err, firstErr)
	}
	notes.scan = func(*os.Root) ([]model.NoteNode, error) { return nil, retryErr }
	if err := notes.SyncFS(); !errors.Is(err, retryErr) {
		t.Fatalf("retry SyncFS() error = %v, want %v", err, retryErr)
	}
	if tree, err := notes.GetTree(); !errors.Is(err, firstErr) || tree != nil {
		t.Fatalf("GetTree() = %#v, %v; want nil tree and retained %v", tree, err, firstErr)
	}
}

func TestNoteServiceLaterSyncFailureKeepsServingConsistentIndex(t *testing.T) {
	base := t.TempDir()
	writeTestNote(t, base, "current.md", "current")
	notes := newTestNoteService(t, &fakeNoteRepository{}, base)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("initial SyncFS() error = %v", err)
	}

	wantErr := errors.New("later scan failed")
	notes.scan = func(*os.Root) ([]model.NoteNode, error) { return nil, wantErr }
	if err := notes.SyncFS(); !errors.Is(err, wantErr) {
		t.Fatalf("later SyncFS() error = %v, want %v", err, wantErr)
	}
	tree, err := notes.GetTree()
	if err != nil {
		t.Fatalf("GetTree() after later failure error = %v", err)
	}
	if len(tree) != 1 || tree[0].ID != "current.md" {
		t.Fatalf("GetTree() after later failure = %#v, want current.md", tree)
	}
}

func TestNoteServiceSuccessfulSwitchRecoversInitialReadiness(t *testing.T) {
	initialErr := errors.New("initial scan failed")
	notes := newTestNoteService(t, &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md"}}}, t.TempDir())
	notes.scan = func(*os.Root) ([]model.NoteNode, error) { return nil, initialErr }
	if err := notes.SyncFS(); !errors.Is(err, initialErr) {
		t.Fatalf("SyncFS() error = %v, want %v", err, initialErr)
	}
	if err := notes.SwitchBase(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed SwitchBase() error = %v, want os.ErrNotExist", err)
	}
	if _, err := notes.GetTree(); !errors.Is(err, initialErr) {
		t.Fatalf("GetTree() after failed switch error = %v, want %v", err, initialErr)
	}

	target := t.TempDir()
	writeTestNote(t, target, "switched.md", "switched")
	notes.scan = scanNotes
	if err := notes.SwitchBase(target); err != nil {
		t.Fatalf("successful SwitchBase() error = %v", err)
	}
	tree, err := notes.GetTree()
	if err != nil || len(tree) != 1 || tree[0].ID != "switched.md" {
		t.Fatalf("GetTree() after successful switch = %#v, %v; want switched.md", tree, err)
	}
}

func TestNoteServiceSuccessfulSettingsSwitchRecoversInitialReadiness(t *testing.T) {
	initialErr := errors.New("initial scan failed")
	notes := newTestNoteService(t, &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md"}}}, t.TempDir())
	notes.scan = func(*os.Root) ([]model.NoteNode, error) { return nil, initialErr }
	if err := notes.SyncFS(); !errors.Is(err, initialErr) {
		t.Fatalf("SyncFS() error = %v, want %v", err, initialErr)
	}

	target := t.TempDir()
	writeTestNote(t, target, "settings.md", "settings")
	notes.scan = scanNotes
	saveErr := errors.New("save failed")
	if operationErr, rollbackErr := notes.switchBaseTransaction(target, &fakeConfigStore{saveErr: saveErr}, &model.Config{}); !errors.Is(operationErr, saveErr) || rollbackErr != nil {
		t.Fatalf("failed switchBaseTransaction() errors = %v, %v; want %v, nil", operationErr, rollbackErr, saveErr)
	}
	if _, err := notes.GetTree(); !errors.Is(err, initialErr) {
		t.Fatalf("GetTree() after failed settings switch error = %v, want %v", err, initialErr)
	}
	if operationErr, rollbackErr := notes.switchBaseTransaction(target, &fakeConfigStore{}, &model.Config{}); operationErr != nil || rollbackErr != nil {
		t.Fatalf("switchBaseTransaction() errors = %v, %v", operationErr, rollbackErr)
	}
	tree, err := notes.GetTree()
	if err != nil || len(tree) != 1 || tree[0].ID != "settings.md" {
		t.Fatalf("GetTree() after settings switch = %#v, %v; want settings.md", tree, err)
	}
}

func TestNoteServiceRecoverySyncsSerialize(t *testing.T) {
	initialErr := errors.New("initial scan failed")
	notes := newTestNoteService(t, &fakeNoteRepository{}, t.TempDir())
	notes.scan = func(*os.Root) ([]model.NoteNode, error) { return nil, initialErr }
	if err := notes.SyncFS(); !errors.Is(err, initialErr) {
		t.Fatalf("initial SyncFS() error = %v, want %v", err, initialErr)
	}

	entered := make(chan struct{}, 2)
	releases := make(chan struct{})
	notes.scan = func(*os.Root) ([]model.NoteNode, error) {
		entered <- struct{}{}
		<-releases
		return nil, nil
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- notes.SyncFS() }()
	<-entered

	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- notes.SyncFS()
	}()
	<-secondStarted
	select {
	case <-entered:
		t.Fatal("second recovery scan entered before the first completed")
	default:
	}

	releases <- struct{}{}
	if err := <-firstDone; err != nil {
		t.Fatalf("first recovery SyncFS() error = %v", err)
	}
	<-entered
	releases <- struct{}{}
	if err := <-secondDone; err != nil {
		t.Fatalf("second recovery SyncFS() error = %v", err)
	}
	if _, err := notes.GetTree(); err != nil {
		t.Fatalf("GetTree() after serialized recovery error = %v", err)
	}
}

func TestNoteServiceInitialSyncFailureReleasesAllTreeWaiters(t *testing.T) {
	wantErr := errors.New("scan failed")
	scanStarted := make(chan struct{})
	releaseScan := make(chan struct{})
	notes := newTestNoteService(t, &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md"}}}, t.TempDir())
	notes.scan = func(*os.Root) ([]model.NoteNode, error) {
		close(scanStarted)
		<-releaseScan
		return nil, wantErr
	}

	syncDone := make(chan error, 1)
	go func() { syncDone <- notes.SyncFS() }()
	<-scanStarted

	const waiterCount = 8
	results := make(chan error, waiterCount)
	var waitersStarted sync.WaitGroup
	waitersStarted.Add(waiterCount)
	for range waiterCount {
		go func() {
			waitersStarted.Done()
			_, err := notes.GetTree()
			results <- err
		}()
	}
	waitersStarted.Wait()
	close(releaseScan)
	if err := <-syncDone; !errors.Is(err, wantErr) {
		t.Fatalf("SyncFS() error = %v, want %v", err, wantErr)
	}
	for range waiterCount {
		if err := <-results; !errors.Is(err, wantErr) {
			t.Errorf("GetTree() waiter error = %v, want %v", err, wantErr)
		}
	}
}
