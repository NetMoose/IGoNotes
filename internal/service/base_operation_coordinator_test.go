package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBaseOperationCoordinatorSerializesOperations(t *testing.T) {
	coordinator := NewBaseOperationCoordinator()
	coordinator.Lock()

	attempting := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		close(attempting)
		coordinator.Lock()
		close(acquired)
		coordinator.Unlock()
	}()
	<-attempting

	select {
	case <-acquired:
		coordinator.Unlock()
		t.Fatal("second Lock() acquired while the first operation held the coordinator")
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
