package service

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

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
		t.Fatalf("initial SyncFS() error = %v", err)
	}
	assertRepositoryIDs(t, repo, "active.md")

	completed := true
	config := model.Config{
		Bases: []model.Base{
			{Name: "active", Path: activePath},
			{Name: "target", Path: targetPath},
		},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config}
	settings, err := NewSettingsService(store, notes, coordinator, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	if notes.coordinator != coordinator || settings.coordinator != coordinator {
		t.Fatal("NoteService and SettingsService do not share the coordinator")
	}

	mutationStarted := make(chan struct{})
	mutationRelease := make(chan struct{})
	mutationDone := make(chan error, 1)
	switchStarted := make(chan struct{})
	switchDone := make(chan error, 1)
	var releaseMutationOnce sync.Once
	releaseMutation := func() { releaseMutationOnce.Do(func() { close(mutationRelease) }) }
	settingsLockHeld := false
	releaseSettingsLock := func() {
		if settingsLockHeld {
			settingsLockHeld = false
			settings.mu.Unlock()
		}
	}
	mutationLaunched := false
	switchLaunched := false
	mutationJoined := false
	switchJoined := false
	t.Cleanup(func() {
		releaseMutation()
		releaseSettingsLock()
		if mutationLaunched && !mutationJoined {
			select {
			case <-mutationDone:
			case <-time.After(5 * time.Second):
				t.Errorf("filesystem mutation goroutine did not stop during cleanup")
			}
		}
		if switchLaunched && !switchJoined {
			select {
			case <-switchDone:
			case <-time.After(5 * time.Second):
				t.Errorf("base switch goroutine did not stop during cleanup")
			}
		}
	})

	mutationLaunched = true
	go func() {
		coordinator.Lock()
		mutationErr := notes.MutateActiveFilesystem(activePath, func(canonicalPath string) error {
			close(mutationStarted)
			<-mutationRelease
			return os.WriteFile(filepath.Join(canonicalPath, "incoming.md"), []byte("incoming"), 0o600)
		})
		coordinator.Unlock()
		mutationDone <- mutationErr
	}()
	select {
	case <-mutationStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("filesystem mutation callback did not start")
	}

	switchLaunched = true
	go func() {
		close(switchStarted)
		_, err := settings.SwitchBase("target")
		switchDone <- err
	}()
	select {
	case <-switchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("base switch goroutine did not start")
	}
	for range 10 {
		runtime.Gosched()
	}
	if !settings.mu.TryLock() {
		t.Fatal("settings lock was taken while switch should be waiting at coordinator")
	}
	settingsLockHeld = true
	if notes.baseMu.TryRLock() {
		notes.baseMu.RUnlock()
		t.Fatal("base read lock was available while filesystem mutation callback was blocked")
	}
	select {
	case err := <-switchDone:
		switchJoined = true
		t.Fatalf("SwitchBase() completed while filesystem mutation held the coordinator: %v", err)
	default:
	}

	releaseMutation()
	select {
	case err := <-mutationDone:
		mutationJoined = true
		if err != nil {
			t.Fatalf("MutateActiveFilesystem() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("filesystem mutation did not finish after release")
	}

	deadline := time.Now().Add(5 * time.Second)
	for coordinator.operations.TryLock() {
		coordinator.operations.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("SwitchBase() did not acquire coordinator before settings lock")
		}
		runtime.Gosched()
	}

	releaseSettingsLock()
	select {
	case err := <-switchDone:
		switchJoined = true
		if err != nil {
			t.Fatalf("SwitchBase() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SwitchBase() did not finish after settings lock release")
	}

	if got := notes.GetBasePath(); got != targetPath {
		t.Errorf("GetBasePath() = %q, want %q", got, targetPath)
	}
	if got := settings.GetConfig().CurrentBase; got != "target" {
		t.Errorf("current base = %q, want target", got)
	}
	assertRepositoryIDs(t, repo, "target.md")
	assertFileContent(t, filepath.Join(activePath, "incoming.md"), []byte("incoming"))
}
