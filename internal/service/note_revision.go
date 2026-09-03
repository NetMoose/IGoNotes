package service

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path"

	"IGoNotes/internal/model"
)

var ErrNoteChanged = errors.New("note changed")

func noteRevision(content []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(content))
}

func (s *NoteService) GetNote(id string) (model.NoteContentResponse, error) {
	cleanID, err := cleanRelativeNotePath(id, false)
	if err != nil {
		return model.NoteContentResponse{}, err
	}
	if s.beforeReadLock != nil {
		s.beforeReadLock()
	}
	s.baseMu.RLock()
	defer s.baseMu.RUnlock()

	if s.baseErr != nil {
		return model.NoteContentResponse{}, s.baseErr
	}
	if s.basePath == "" {
		return model.NoteContentResponse{}, os.ErrNotExist
	}

	data, err := s.baseRoot.ReadFile(rootPath(cleanID))
	if err != nil {
		return model.NoteContentResponse{}, normalizeRootError(err)
	}
	return model.NoteContentResponse{
		ID:       id,
		Content:  string(data),
		Revision: noteRevision(data),
	}, nil
}

func (s *NoteService) SaveNote(request model.SaveNoteRequest) (model.SaveNoteResponse, error) {
	cleanID, err := cleanRelativeNotePath(request.ID, false)
	if err != nil {
		return model.SaveNoteResponse{}, err
	}
	s.baseMu.Lock()
	defer s.baseMu.Unlock()

	if s.baseErr != nil {
		return model.SaveNoteResponse{}, s.baseErr
	}
	if s.basePath == "" {
		return model.SaveNoteResponse{}, os.ErrNotExist
	}
	if err := s.checkMutationLocked(); err != nil {
		return model.SaveNoteResponse{}, err
	}
	if request.ExpectedRevision != nil {
		current, err := s.baseRoot.ReadFile(rootPath(cleanID))
		if err != nil {
			return model.SaveNoteResponse{}, normalizeRootError(err)
		}
		if noteRevision(current) != *request.ExpectedRevision {
			return model.SaveNoteResponse{}, ErrNoteChanged
		}
	}

	parent := path.Dir(cleanID)
	if err := ensureRootDir(s.baseRoot, parent); err != nil {
		return model.SaveNoteResponse{}, err
	}
	content := []byte(request.Content)
	if err := s.baseRoot.WriteFile(rootPath(cleanID), content, 0644); err != nil {
		return model.SaveNoteResponse{}, normalizeRootError(err)
	}

	return model.SaveNoteResponse{Status: "saved", Revision: noteRevision(content)}, nil
}
