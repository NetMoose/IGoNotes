package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBaseOperationCoordinatorZeroValueSupportsConflictPolicy(t *testing.T) {
	var coordinator BaseOperationCoordinator
	basePath := filepath.Join(t.TempDir(), "notes")

	if err := coordinator.CheckMutation(""); err != nil {
		t.Fatalf("CheckMutation(empty) error = %v, want nil", err)
	}
	if err := coordinator.CheckMutation(basePath); err != nil {
		t.Fatalf("CheckMutation() before conflict error = %v, want nil", err)
	}
	coordinator.SetConflict("", true)
	coordinator.SetConflict(basePath, true)
	if err := coordinator.CheckMutation(basePath); !errors.Is(err, ErrGitConflictPending) {
		t.Fatalf("CheckMutation() error = %v, want ErrGitConflictPending", err)
	}
	if err := coordinator.checkMutationForIdentity(basePath, nil); !errors.Is(err, ErrGitConflictPending) {
		t.Fatalf("checkMutationForIdentity() error = %v, want ErrGitConflictPending", err)
	}
	coordinator.SetConflict(basePath, false)
	if err := coordinator.CheckMutation(basePath); err != nil {
		t.Fatalf("CheckMutation() after clearing error = %v, want nil", err)
	}
}

func TestBaseOperationCoordinatorMatchesCachedPhysicalIdentity(t *testing.T) {
	physical := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	pinnedInfo, err := os.Stat(alias)
	if err != nil {
		t.Fatalf("Stat(alias) error = %v", err)
	}
	coordinator := NewBaseOperationCoordinator()
	coordinator.SetConflict(physical, true)
	differentSpelling := filepath.Join(t.TempDir(), "different-spelling")

	if err := coordinator.CheckMutation(differentSpelling); err != nil {
		t.Fatalf("CheckMutation() exact-only error = %v, want nil", err)
	}
	if err := coordinator.checkMutationForIdentity(differentSpelling, pinnedInfo); !errors.Is(err, ErrGitConflictPending) {
		t.Fatalf("checkMutationForIdentity() error = %v, want ErrGitConflictPending", err)
	}
}

func TestBaseOperationCoordinatorClearsPhysicalConflictAliases(t *testing.T) {
	physical := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	coordinator := NewBaseOperationCoordinator()
	coordinator.SetConflict(alias, true)
	coordinator.SetConflict(physical, false)

	if err := coordinator.CheckMutation(alias); err != nil {
		t.Fatalf("CheckMutation(alias) after physical clear error = %v, want nil", err)
	}
}

func TestBaseOperationCoordinatorSerializesOperations(t *testing.T) {
	coordinator := NewBaseOperationCoordinator()
	coordinator.Lock()

	acquired := make(chan struct{})
	go func() {
		coordinator.Lock()
		close(acquired)
		coordinator.Unlock()
	}()

	select {
	case <-acquired:
		t.Fatal("second operation was observed while the first held the coordinator")
	case <-time.After(50 * time.Millisecond):
	}

	coordinator.Unlock()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second Lock() did not acquire after the first operation released the coordinator")
	}
}

func TestBaseOperationCoordinatorConflictPolicyIsLockFree(t *testing.T) {
	coordinator := NewBaseOperationCoordinator()
	basePath := filepath.Join(t.TempDir(), "notes")
	lookupPath := filepath.Join(basePath, "nested", "..")
	coordinator.Lock()

	checked := make(chan error, 1)
	go func() {
		coordinator.SetConflict(basePath, true)
		checked <- coordinator.CheckMutation(lookupPath)
	}()

	select {
	case err := <-checked:
		if !errors.Is(err, ErrGitConflictPending) {
			coordinator.Unlock()
			t.Fatalf("CheckMutation() error = %v, want ErrGitConflictPending", err)
		}
	case <-time.After(time.Second):
		coordinator.Unlock()
		t.Fatal("conflict policy waited for the operation lock")
	}
	coordinator.Unlock()

	coordinator.SetConflict(basePath, false)
	if err := coordinator.CheckMutation(basePath); err != nil {
		t.Fatalf("CheckMutation() after clearing error = %v, want nil", err)
	}
}

func TestBaseOperationCoordinatorConcurrentConflictUpdatesPreservePaths(t *testing.T) {
	coordinator := NewBaseOperationCoordinator()
	root := t.TempDir()
	const pathCount = 64
	paths := make([]string, pathCount)
	start := make(chan struct{})
	var writers sync.WaitGroup
	for i := range pathCount {
		paths[i] = filepath.Join(root, fmt.Sprintf("base-%d", i))
		if err := os.Mkdir(paths[i], 0o755); err != nil {
			t.Fatalf("Mkdir(%q) error = %v", paths[i], err)
		}
		writers.Add(1)
		go func(path string) {
			defer writers.Done()
			<-start
			coordinator.SetConflict(path, true)
		}(paths[i])
	}

	close(start)
	writers.Wait()
	for _, path := range paths {
		if err := coordinator.CheckMutation(path); !errors.Is(err, ErrGitConflictPending) {
			t.Errorf("CheckMutation(%q) error = %v, want ErrGitConflictPending", path, err)
		}
	}
}
