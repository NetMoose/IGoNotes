package service

import (
	"errors"
	"os"
)

// MutateActiveFilesystem runs a controlled active-worktree mutation. The caller
// must hold BaseOperationCoordinator; callbacks and repositories must not call
// upward into service layers. Fetch and push remain outside this transaction.
func (s *NoteService) MutateActiveFilesystem(expectedPath string, mutate func(canonicalPath string) error) error {
	if mutate == nil {
		return os.ErrInvalid
	}

	s.baseMu.Lock()
	defer s.baseMu.Unlock()
	if s.baseErr != nil {
		return s.baseErr
	}
	if s.basePath == "" {
		return os.ErrNotExist
	}

	matches, canonicalPath, err := s.baseIdentityLocked(expectedPath)
	if err != nil {
		return err
	}
	if !matches {
		return ErrRuntimePathChanged
	}

	// Deliberately bypass the ordinary conflict gate so resolution can repair the
	// worktree; reindex even after callback failure before readers resume.
	mutationErr := mutate(canonicalPath)
	indexErr := s.replaceIndexLocked()
	if indexErr != nil && s.baseErr == nil {
		indexErr = s.failClosedLocked(indexErr, nil)
	}
	return errors.Join(mutationErr, indexErr)
}
