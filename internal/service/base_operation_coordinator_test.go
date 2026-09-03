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
	captured, err := os.Stat(physical)
	if err != nil {
		t.Fatalf("Stat(physical) error = %v", err)
	}
	coordinator := NewBaseOperationCoordinator()
	coordinator.SetConflict(alias, true)
	coordinator.clearConflictForIdentity(physical, captured)

	if err := coordinator.CheckMutation(alias); err != nil {
		t.Fatalf("CheckMutation(alias) after physical clear error = %v, want nil", err)
	}
}

func TestBaseOperationCoordinatorPublicClearDoesNotResolveReboundPath(t *testing.T) {
	root := t.TempDir()
	removedPath := filepath.Join(root, "removed")
	displacedPath := filepath.Join(root, "removed-original")
	retainedPath := filepath.Join(root, "retained")
	if err := os.Mkdir(removedPath, 0o755); err != nil {
		t.Fatalf("Mkdir(removed) error = %v", err)
	}
	if err := os.Mkdir(retainedPath, 0o755); err != nil {
		t.Fatalf("Mkdir(retained) error = %v", err)
	}
	coordinator := NewBaseOperationCoordinator()
	coordinator.SetConflict(removedPath, true)
	coordinator.SetConflict(retainedPath, true)
	if err := os.Rename(removedPath, displacedPath); err != nil {
		t.Fatalf("Rename(removed) error = %v", err)
	}
	if err := os.Symlink(retainedPath, removedPath); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	coordinator.SetConflict(removedPath, false)

	if err := coordinator.CheckMutation(removedPath); err != nil {
		t.Errorf("removed exact conflict = %v, want nil", err)
	}
	if err := coordinator.CheckMutation(retainedPath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("retained conflict = %v, want ErrGitConflictPending", err)
	}
}

func TestBaseOperationCoordinatorCapturedClearUsesSuppliedIdentity(t *testing.T) {
	root := t.TempDir()
	removedPath := filepath.Join(root, "removed")
	displacedPath := filepath.Join(root, "removed-original")
	retainedPath := filepath.Join(root, "retained")
	if err := os.Mkdir(removedPath, 0o755); err != nil {
		t.Fatalf("Mkdir(removed) error = %v", err)
	}
	if err := os.Mkdir(retainedPath, 0o755); err != nil {
		t.Fatalf("Mkdir(retained) error = %v", err)
	}
	captured, err := os.Stat(removedPath)
	if err != nil {
		t.Fatalf("Stat(removed) error = %v", err)
	}
	coordinator := NewBaseOperationCoordinator()
	coordinator.SetConflict(removedPath, true)
	coordinator.SetConflict(retainedPath, true)
	if err := os.Rename(removedPath, displacedPath); err != nil {
		t.Fatalf("Rename(removed) error = %v", err)
	}
	if err := os.Symlink(retainedPath, removedPath); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	coordinator.clearConflictForIdentity(removedPath, captured)

	if err := coordinator.CheckMutation(removedPath); err != nil {
		t.Errorf("removed exact conflict = %v, want nil", err)
	}
	if err := coordinator.CheckMutation(retainedPath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("retained conflict = %v, want ErrGitConflictPending", err)
	}
}

func TestBaseOperationCoordinatorCapturedClearPreservesReboundCachedIdentity(t *testing.T) {
	root := t.TempDir()
	originalPath := filepath.Join(root, "base")
	retainedAlias := filepath.Join(root, "retained")
	if err := os.Mkdir(originalPath, 0o755); err != nil {
		t.Fatalf("Mkdir(original) error = %v", err)
	}
	originalInfo, err := os.Stat(originalPath)
	if err != nil {
		t.Fatalf("Stat(original) error = %v", err)
	}
	coordinator := NewBaseOperationCoordinator()
	coordinator.SetConflict(originalPath, true)
	if err := os.Rename(originalPath, retainedAlias); err != nil {
		t.Fatalf("Rename(original) error = %v", err)
	}
	if err := os.Mkdir(originalPath, 0o755); err != nil {
		t.Fatalf("Mkdir(replacement) error = %v", err)
	}
	replacementInfo, err := os.Stat(originalPath)
	if err != nil {
		t.Fatalf("Stat(replacement) error = %v", err)
	}

	coordinator.clearConflictForIdentity(originalPath, replacementInfo)

	if err := coordinator.CheckMutation(originalPath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("CheckMutation(original path) error = %v, want retained ErrGitConflictPending", err)
	}
	if err := coordinator.checkMutationForIdentity(retainedAlias, originalInfo); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("checkMutationForIdentity(retained O) error = %v, want ErrGitConflictPending", err)
	}
}

func TestBaseOperationCoordinatorCapturedClearExactIdentityRules(t *testing.T) {
	t.Run("captured nil preserves cached identity", func(t *testing.T) {
		path := t.TempDir()
		coordinator := NewBaseOperationCoordinator()
		coordinator.SetConflict(path, true)

		coordinator.clearConflictForIdentity(path, nil)

		if err := coordinator.CheckMutation(path); !errors.Is(err, ErrGitConflictPending) {
			t.Fatalf("CheckMutation() error = %v, want retained ErrGitConflictPending", err)
		}
	})

	t.Run("matching identity clears", func(t *testing.T) {
		path := t.TempDir()
		identity, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		coordinator := NewBaseOperationCoordinator()
		coordinator.SetConflict(path, true)

		coordinator.clearConflictForIdentity(path, identity)

		if err := coordinator.CheckMutation(path); err != nil {
			t.Fatalf("CheckMutation() error = %v, want nil", err)
		}
	})

	t.Run("nil identities clear exact fallback", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing")
		coordinator := NewBaseOperationCoordinator()
		coordinator.SetConflict(path, true)

		coordinator.clearConflictForIdentity(path, nil)

		if err := coordinator.CheckMutation(path); err != nil {
			t.Fatalf("CheckMutation() error = %v, want nil", err)
		}
	})

	t.Run("captured identity preserves cached nil fallback", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing")
		identity, err := os.Stat(t.TempDir())
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		coordinator := NewBaseOperationCoordinator()
		coordinator.SetConflict(path, true)

		coordinator.clearConflictForIdentity(path, identity)

		if err := coordinator.CheckMutation(path); !errors.Is(err, ErrGitConflictPending) {
			t.Fatalf("CheckMutation() error = %v, want retained ErrGitConflictPending", err)
		}
	})
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
