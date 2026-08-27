package service

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"IGoNotes/internal/model"
)

type ConfigStore interface {
	Load() (*model.Config, error)
	Save(*model.Config) error
}

type BaseRuntime interface {
	GetBasePath() string
	SwitchBase(string) error
}

type SettingsService struct {
	// Lock ordering: SettingsService.mu precedes BaseRuntime locks. BaseRuntime
	// implementations must not call back into SettingsService while locked.
	mu     sync.RWMutex
	store  ConfigStore
	notes  BaseRuntime
	logger *log.Logger
	config model.Config
}

func NewSettingsService(
	store ConfigStore,
	notes BaseRuntime,
	activeBaseName string,
	logger *log.Logger,
) (*SettingsService, error) {
	if store == nil {
		return nil, fmt.Errorf("config store: %w", ErrInvalidConfig)
	}
	if notes == nil {
		return nil, fmt.Errorf("base runtime: %w", ErrInvalidConfig)
	}

	loadedConfig, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	if loadedConfig == nil {
		return nil, fmt.Errorf("load settings: %w: store returned nil config", ErrInvalidConfig)
	}
	config := cloneConfig(*loadedConfig)

	if config.SetupCompleted == nil {
		completed := config.BaseDir != "" || len(config.Bases) != 0 || config.CurrentBase != ""
		config.SetupCompleted = &completed
		if err := store.Save(&config); err != nil {
			return nil, fmt.Errorf("migrate setup state: %w", err)
		}
	}

	runtimePath := notes.GetBasePath()
	structurallyEmpty := config.BaseDir == "" && len(config.Bases) == 0 && config.CurrentBase == ""
	setupIncomplete := config.SetupCompleted != nil && !*config.SetupCompleted
	if !(structurallyEmpty && setupIncomplete && runtimePath == "" && activeBaseName == "") {
		effectiveBaseName := config.CurrentBase
		if activeBaseName != "" {
			effectiveBaseName = activeBaseName
		}
		index := baseIndex(config.Bases, effectiveBaseName)
		if index < 0 {
			return nil, fmt.Errorf("current base %q: %w", effectiveBaseName, ErrBaseNotFound)
		}
		if filepath.Clean(config.Bases[index].Path) != filepath.Clean(runtimePath) {
			return nil, &FieldError{
				Kind:    ErrInvalidConfig,
				Field:   "current_base",
				Message: fmt.Sprintf("current base %q does not match the runtime base path", effectiveBaseName),
			}
		}
	}

	if activeBaseName != "" {
		config.CurrentBase = activeBaseName
	}

	if logger == nil {
		logger = log.Default()
	}

	return &SettingsService{
		store:  store,
		notes:  notes,
		logger: logger,
		config: cloneConfig(config),
	}, nil
}

func (s *SettingsService) GetConfig() model.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.config)
}

func (s *SettingsService) SetupCompleted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.SetupCompleted != nil && *s.config.SetupCompleted
}

func (s *SettingsService) responseLocked() model.SettingsResponse {
	return model.SettingsResponse{
		Config:   cloneConfig(s.config),
		BasePath: s.notes.GetBasePath(),
	}
}

type preparedBase struct {
	base   model.Base
	create bool
}

func prepareBase(request model.BaseMutationRequest) (preparedBase, error) {
	mode := request.Mode
	name := strings.TrimSpace(request.Name)
	path := strings.TrimSpace(request.Path)
	if mode != "create" && mode != "connect" {
		return preparedBase{}, fieldError(ErrInvalidMode, "mode", "mode must be create or connect")
	}
	if name == "" {
		return preparedBase{}, fieldError(ErrInvalidName, "name", "base name is required")
	}
	if mode == "create" && (name == "." || name == ".." || strings.ContainsAny(name, `/\`)) {
		return preparedBase{}, fieldError(ErrInvalidName, "name", "base name cannot contain path separators")
	}
	if path == "" {
		return preparedBase{}, fieldError(ErrInvalidPath, "path", "base path is required")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return preparedBase{}, fieldError(ErrInvalidPath, "path", fmt.Sprintf("resolve base path: %v", err))
	}
	absPath = filepath.Clean(absPath)
	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		return preparedBase{}, fieldError(ErrInvalidPath, "path", "base path must be an existing directory")
	}
	if mode == "connect" {
		return preparedBase{base: model.Base{Name: name, Path: absPath}}, nil
	}

	targetPath := filepath.Join(absPath, name)
	_, err = os.Stat(targetPath)
	switch {
	case err == nil:
		return preparedBase{}, fieldError(ErrBasePathConflict, "path", "base path already exists")
	case !errors.Is(err, os.ErrNotExist):
		return preparedBase{}, fieldError(ErrInvalidPath, "path", fmt.Sprintf("inspect base path: %v", err))
	default:
		return preparedBase{base: model.Base{Name: name, Path: targetPath}, create: true}, nil
	}
}

func (s *SettingsService) applyConfigLocked(next model.Config, targetPath string) error {
	oldPath := s.notes.GetBasePath()
	switched := targetPath != "" && filepath.Clean(targetPath) != filepath.Clean(oldPath)
	if switched {
		if err := s.notes.SwitchBase(targetPath); err != nil {
			return fmt.Errorf("switch runtime base: %w", err)
		}
	}

	if err := s.store.Save(&next); err != nil {
		saveErr := fmt.Errorf("save settings: %w", err)
		if !switched {
			return saveErr
		}
		if rollbackErr := s.notes.SwitchBase(oldPath); rollbackErr != nil {
			s.logger.Printf("settings runtime rollback failed: %v", rollbackErr)
			return errors.Join(
				ErrRollbackFailed,
				saveErr,
				fmt.Errorf("restore runtime base: %w", rollbackErr),
			)
		}
		return saveErr
	}

	s.config = cloneConfig(next)
	return nil
}

func (s *SettingsService) CompleteSetup(request model.BaseMutationRequest) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.config.SetupCompleted != nil && *s.config.SetupCompleted {
		return model.SettingsResponse{}, ErrSetupAlreadyCompleted
	}
	prepared, err := prepareBase(request)
	if err != nil {
		return model.SettingsResponse{}, err
	}

	created := false
	if prepared.create {
		if err := os.Mkdir(prepared.base.Path, 0o755); err != nil {
			if errors.Is(err, os.ErrExist) {
				return model.SettingsResponse{}, fieldError(ErrBasePathConflict, "path", "base path already exists")
			}
			return model.SettingsResponse{}, fieldError(ErrInvalidPath, "path", fmt.Sprintf("create base path: %v", err))
		}
		created = true
	}

	completed := true
	next := model.Config{
		BaseDir:        filepath.Dir(prepared.base.Path),
		Bases:          []model.Base{prepared.base},
		CurrentBase:    prepared.base.Name,
		SetupCompleted: &completed,
	}
	if err := s.applyConfigLocked(next, prepared.base.Path); err != nil {
		if created && filepath.Clean(s.notes.GetBasePath()) != filepath.Clean(prepared.base.Path) {
			if cleanupErr := os.Remove(prepared.base.Path); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
				return model.SettingsResponse{}, errors.Join(err, fmt.Errorf("remove created base path: %w", cleanupErr))
			}
		}
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func ensureUniqueName(config model.Config, name, except string) error {
	for _, base := range config.Bases {
		if base.Name == name && base.Name != except {
			return fieldError(ErrBaseNameConflict, "name", "base name already exists")
		}
	}
	return nil
}

func (s *SettingsService) AddBase(request model.BaseMutationRequest) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	prepared, err := prepareBase(request)
	if err != nil {
		return model.SettingsResponse{}, err
	}
	if err := ensureUniqueName(s.config, prepared.base.Name, ""); err != nil {
		return model.SettingsResponse{}, err
	}

	created := false
	if prepared.create {
		if err := os.Mkdir(prepared.base.Path, 0o755); err != nil {
			if errors.Is(err, os.ErrExist) {
				return model.SettingsResponse{}, fieldError(ErrBasePathConflict, "path", "base path already exists")
			}
			return model.SettingsResponse{}, fieldError(ErrInvalidPath, "path", fmt.Sprintf("create base path: %v", err))
		}
		created = true
	}

	next := cloneConfig(s.config)
	next.Bases = append(next.Bases, prepared.base)
	if err := s.applyConfigLocked(next, ""); err != nil {
		if created {
			if cleanupErr := os.Remove(prepared.base.Path); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
				return model.SettingsResponse{}, errors.Join(err, fmt.Errorf("remove created base path: %w", cleanupErr))
			}
		}
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func fieldError(kind error, field, message string) error {
	return &FieldError{Kind: kind, Field: field, Message: message}
}

func cloneConfig(config model.Config) model.Config {
	cloned := config
	cloned.Bases = append([]model.Base(nil), config.Bases...)
	if config.SetupCompleted != nil {
		completed := *config.SetupCompleted
		cloned.SetupCompleted = &completed
	}
	return cloned
}

func baseIndex(bases []model.Base, name string) int {
	for index := range bases {
		if bases[index].Name == name {
			return index
		}
	}
	return -1
}
