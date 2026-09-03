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
// Lock ordering is coordinator -> SettingsService.mu -> NoteService.baseMu ->
// repository/SQLite. Conflict snapshots are immutable after publication so
// mutation checks remain lock-free and never acquire operations.
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
	for {
		current := c.conflicts.Load()
		next := make(conflictPathSet, len(*current)+1)
		for path := range *current {
			next[path] = struct{}{}
		}
		if pending {
			next[basePath] = struct{}{}
		} else {
			delete(next, basePath)
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
	if _, pending := (*conflicts)[filepath.Clean(canonicalBasePath)]; pending {
		return ErrGitConflictPending
	}
	return nil
}
