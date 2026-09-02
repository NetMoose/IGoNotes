package service

import (
	"context"
	"errors"

	"IGoNotes/internal/model"
)

var errGitStatusServiceNotInitialized = errors.New("Git status service is not initialized")

// GitStatusSettingsSnapshot keeps one config generation stable for the callback.
// The callback must not call back into SettingsService.
type GitStatusSettingsSnapshot interface {
	ReadConfigSnapshot(func(model.Config) error) error
}

type GitStatusService struct {
	settings GitStatusSettingsSnapshot
	statuses GitStatusReader
}

func NewGitStatusService(settings GitStatusSettingsSnapshot, statuses GitStatusReader) *GitStatusService {
	return &GitStatusService{settings: settings, statuses: statuses}
}

func (s *GitStatusService) Status(ctx context.Context, name string) (model.GitStatusResponse, error) {
	if s == nil || s.settings == nil {
		return model.GitStatusResponse{}, errGitStatusServiceNotInitialized
	}
	var response model.GitStatusResponse
	err := s.settings.ReadConfigSnapshot(func(config model.Config) error {
		if name != "" {
			index := baseIndex(config.Bases, name)
			if index < 0 {
				return ErrBaseNotFound
			}
			status, err := s.statusForBase(ctx, config.Bases[index], nil)
			if err != nil {
				return err
			}
			response.Statuses = []model.GitStatus{status}
			return nil
		}

		byPath := make(map[string]model.GitStatus)
		if s.statuses != nil {
			statuses, err := s.statuses.List(ctx)
			if err != nil {
				return err
			}
			for _, status := range statuses {
				byPath[status.RepositoryPath] = cloneGitStatus(status)
			}
		}
		result := make([]model.GitStatus, 0, len(config.Bases))
		for _, base := range config.Bases {
			status, err := s.statusForBase(ctx, base, byPath)
			if err != nil {
				return err
			}
			result = append(result, status)
		}
		response.Statuses = result
		return nil
	})
	if err != nil {
		return model.GitStatusResponse{}, err
	}
	return response, nil
}

func (s *GitStatusService) statusForBase(ctx context.Context, base model.Base, byPath map[string]model.GitStatus) (model.GitStatus, error) {
	path, err := canonicalGitStatusPath(base.Path)
	if err != nil {
		return model.GitStatus{}, err
	}
	if !base.GitConfigured() {
		return unconfiguredGitStatus(base.Name, path), nil
	}

	var status model.GitStatus
	found := false
	if byPath != nil {
		status, found = byPath[path]
	} else if s.statuses != nil {
		status, found, err = s.statuses.Get(ctx, path)
		if err != nil {
			return model.GitStatus{}, err
		}
	}
	if !found {
		return needsReconnectGitStatus(base.Name, path), nil
	}
	status = cloneGitStatus(status)
	status.Base = base.Name
	status.RepositoryPath = path
	if status.ChangedPaths == nil {
		status.ChangedPaths = []string{}
	}
	return status, nil
}
