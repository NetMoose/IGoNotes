package service

import (
	"fmt"
	"log"
	"path/filepath"
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

	if activeBaseName != "" {
		index := baseIndex(config.Bases, activeBaseName)
		if index < 0 {
			return nil, fmt.Errorf("active base %q: %w", activeBaseName, ErrBaseNotFound)
		}
		if filepath.Clean(config.Bases[index].Path) != filepath.Clean(notes.GetBasePath()) {
			return nil, &FieldError{
				Kind:    ErrInvalidConfig,
				Field:   "current_base",
				Message: fmt.Sprintf("active base %q does not match the runtime base path", activeBaseName),
			}
		}
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
