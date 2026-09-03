package service

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

var ErrGitConflictPending = errors.New("git conflict pending")

type conflictPathEntry struct {
	identity fs.FileInfo
}

type conflictPathSet map[string]conflictPathEntry

// BaseOperationCoordinator serializes base lifecycle and Git operations.
// Lock ordering is coordinator -> SettingsService.mu -> NoteService.baseMu ->
// repository/SQLite. Repository and filesystem callbacks must not call Lock or
// call upward into SettingsService, NoteService, or repository layers; lock-free
// SetConflict and CheckMutation handoffs remain allowed. Conflict snapshots are
// immutable after publication and never acquire operations.
type BaseOperationCoordinator struct {
	operations sync.Mutex
	conflicts  atomic.Pointer[conflictPathSet]
}

func NewBaseOperationCoordinator() *BaseOperationCoordinator {
	coordinator := &BaseOperationCoordinator{}
	conflicts := make(conflictPathSet)
	coordinator.conflicts.Store(&conflicts)
	return coordinator
}

func (c *BaseOperationCoordinator) Lock() {
	c.operations.Lock()
}

func (c *BaseOperationCoordinator) Unlock() {
	c.operations.Unlock()
}

// SetConflict publishes a copied conflict snapshot without taking operations.
func (c *BaseOperationCoordinator) SetConflict(canonicalBasePath string, pending bool) {
	if canonicalBasePath == "" {
		return
	}
	basePath := filepath.Clean(canonicalBasePath)
	var identity fs.FileInfo
	if pending {
		identity, _ = os.Stat(basePath)
	}
	for {
		current := c.conflicts.Load()
		next := make(conflictPathSet)
		if current != nil {
			next = make(conflictPathSet, len(*current)+1)
			for path, entry := range *current {
				next[path] = entry
			}
		}
		if pending {
			next[basePath] = conflictPathEntry{identity: identity}
		} else {
			delete(next, basePath)
		}
		if c.conflicts.CompareAndSwap(current, &next) {
			return
		}
	}
}

func (c *BaseOperationCoordinator) clearConflictForIdentity(canonicalBasePath string, identity fs.FileInfo) {
	if canonicalBasePath == "" {
		return
	}
	basePath := filepath.Clean(canonicalBasePath)
	for {
		current := c.conflicts.Load()
		next := make(conflictPathSet)
		if current != nil {
			next = make(conflictPathSet, len(*current))
			for path, entry := range *current {
				if path != basePath && !sameFileIdentity(identity, entry.identity) {
					next[path] = entry
				}
			}
		}
		if c.conflicts.CompareAndSwap(current, &next) {
			return
		}
	}
}

// CheckMutation reads the immutable conflict snapshot without taking operations.
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

func (c *BaseOperationCoordinator) checkMutationForIdentity(canonicalBasePath string, identity fs.FileInfo) error {
	if canonicalBasePath == "" && identity == nil {
		return nil
	}
	conflicts := c.conflicts.Load()
	if conflicts == nil {
		return nil
	}
	if _, pending := (*conflicts)[filepath.Clean(canonicalBasePath)]; pending {
		return ErrGitConflictPending
	}
	if identity != nil {
		for _, entry := range *conflicts {
			if sameFileIdentity(identity, entry.identity) {
				return ErrGitConflictPending
			}
		}
	}
	return nil
}

func sameFileIdentity(left, right fs.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right)
}
