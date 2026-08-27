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
	mu       sync.RWMutex
	store    ConfigStore
	notes    BaseRuntime
	logger   *log.Logger
	config   model.Config
	degraded error
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
		return preparedBase{}, fieldErrorWithCause(ErrInvalidPath, err, "path", "resolve base path")
	}
	absPath = filepath.Clean(absPath)
	canonicalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return preparedBase{}, fieldErrorWithCause(ErrInvalidPath, err, "path", "resolve base path symlinks")
	}
	canonicalPath = filepath.Clean(canonicalPath)
	info, err := os.Stat(canonicalPath)
	if err != nil {
		return preparedBase{}, fieldErrorWithCause(ErrInvalidPath, err, "path", "inspect base path")
	}
	if !info.IsDir() {
		return preparedBase{}, fieldError(ErrInvalidPath, "path", "base path must be an existing directory")
	}
	if mode == "connect" {
		return preparedBase{base: model.Base{Name: name, Path: canonicalPath}}, nil
	}

	targetPath := filepath.Join(canonicalPath, name)
	_, err = os.Stat(targetPath)
	switch {
	case err == nil:
		return preparedBase{}, fieldError(ErrBasePathConflict, "path", "base path already exists")
	case !errors.Is(err, os.ErrNotExist):
		return preparedBase{}, fieldErrorWithCause(ErrInvalidPath, err, "path", "inspect base path")
	default:
		return preparedBase{base: model.Base{Name: name, Path: targetPath}, create: true}, nil
	}
}

func createBaseDirectory(prepared preparedBase) error {
	root, err := os.OpenRoot(filepath.Dir(prepared.base.Path))
	if err != nil {
		return fieldErrorWithCause(ErrInvalidPath, err, "path", "open base parent")
	}
	mkdirErr := root.Mkdir(prepared.base.Name, 0o755)
	closeErr := root.Close()
	if mkdirErr != nil {
		cause := errors.Join(mkdirErr, closeErr)
		if errors.Is(mkdirErr, os.ErrExist) {
			return fieldErrorWithCause(ErrBasePathConflict, cause, "path", "base path already exists")
		}
		return fieldErrorWithCause(ErrInvalidPath, cause, "path", "create base path")
	}
	if closeErr != nil {
		return fieldErrorWithCause(ErrInvalidPath, closeErr, "path", "close base parent after creating base")
	}
	return nil
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
			s.degraded = errors.Join(
				ErrRollbackFailed,
				saveErr,
				fmt.Errorf("restore runtime base: %w", rollbackErr),
			)
			return s.degraded
		}
		return saveErr
	}

	s.config = cloneConfig(next)
	return nil
}

func (s *SettingsService) CompleteSetup(request model.BaseMutationRequest) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	if s.config.SetupCompleted != nil && *s.config.SetupCompleted {
		return model.SettingsResponse{}, ErrSetupAlreadyCompleted
	}
	prepared, err := prepareBase(request)
	if err != nil {
		return model.SettingsResponse{}, err
	}

	if prepared.create {
		if err := createBaseDirectory(prepared); err != nil {
			return model.SettingsResponse{}, err
		}
	}

	completed := true
	next := model.Config{
		BaseDir:        filepath.Dir(prepared.base.Path),
		Bases:          []model.Base{prepared.base},
		CurrentBase:    prepared.base.Name,
		SetupCompleted: &completed,
	}
	if err := s.applyConfigLocked(next, prepared.base.Path); err != nil {
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

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	prepared, err := prepareBase(request)
	if err != nil {
		return model.SettingsResponse{}, err
	}
	if err := ensureUniqueName(s.config, prepared.base.Name, ""); err != nil {
		return model.SettingsResponse{}, err
	}

	if prepared.create {
		if err := createBaseDirectory(prepared); err != nil {
			return model.SettingsResponse{}, err
		}
	}

	next := cloneConfig(s.config)
	next.Bases = append(next.Bases, prepared.base)
	if err := s.applyConfigLocked(next, ""); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func (s *SettingsService) UpdateBase(oldName string, request model.BaseUpdateRequest) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	index := baseIndex(s.config.Bases, oldName)
	if index < 0 {
		return model.SettingsResponse{}, ErrBaseNotFound
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return model.SettingsResponse{}, fieldError(ErrInvalidName, "name", "base name is required")
	}
	if err := ensureUniqueName(s.config, name, oldName); err != nil {
		return model.SettingsResponse{}, err
	}
	path, err := normalizeExistingBasePath(request.Path, "path")
	if err != nil {
		return model.SettingsResponse{}, err
	}

	next := cloneConfig(s.config)
	next.Bases[index].Name = name
	next.Bases[index].Path = path
	targetPath := ""
	if s.config.CurrentBase == oldName {
		next.CurrentBase = name
		targetPath = path
	}
	if err := s.applyConfigLocked(next, targetPath); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func (s *SettingsService) ForgetBase(name string) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	index := baseIndex(s.config.Bases, name)
	if index < 0 {
		return model.SettingsResponse{}, ErrBaseNotFound
	}
	if len(s.config.Bases) == 1 {
		return model.SettingsResponse{}, ErrLastBase
	}
	if s.config.CurrentBase == name {
		return model.SettingsResponse{}, ErrActiveBase
	}

	next := cloneConfig(s.config)
	next.Bases = append(next.Bases[:index], next.Bases[index+1:]...)
	if err := s.applyConfigLocked(next, ""); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func (s *SettingsService) SwitchBase(name string) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	index := baseIndex(s.config.Bases, name)
	if index < 0 {
		return model.SettingsResponse{}, ErrBaseNotFound
	}

	next := cloneConfig(s.config)
	next.CurrentBase = name
	if err := s.applyConfigLocked(next, next.Bases[index].Path); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func (s *SettingsService) ReplaceConfig(input model.Config) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	currentSetup := s.config.SetupCompleted != nil && *s.config.SetupCompleted
	next, err := normalizeConfig(input, currentSetup)
	if err != nil {
		return model.SettingsResponse{}, err
	}
	index := baseIndex(next.Bases, next.CurrentBase)
	if err := s.applyConfigLocked(next, next.Bases[index].Path); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func normalizeConfig(input model.Config, currentSetup bool) (model.Config, error) {
	normalized := cloneConfig(input)
	setupCompleted := currentSetup
	if normalized.SetupCompleted != nil {
		if currentSetup && !*normalized.SetupCompleted {
			return model.Config{}, fieldError(ErrSetupCannotReopen, "setup_completed", "completed setup cannot be reopened")
		}
		setupCompleted = *normalized.SetupCompleted
	}
	normalized.SetupCompleted = &setupCompleted

	if len(normalized.Bases) == 0 {
		return model.Config{}, fieldError(ErrInvalidConfig, "bases", "at least one base is required")
	}
	names := make(map[string]struct{}, len(normalized.Bases))
	for index := range normalized.Bases {
		nameField := fmt.Sprintf("bases[%d].name", index)
		name := strings.TrimSpace(normalized.Bases[index].Name)
		if name == "" {
			return model.Config{}, fieldError(ErrInvalidName, nameField, "base name is required")
		}
		if _, exists := names[name]; exists {
			return model.Config{}, fieldError(ErrBaseNameConflict, nameField, "base name already exists")
		}
		names[name] = struct{}{}
		normalized.Bases[index].Name = name

		pathField := fmt.Sprintf("bases[%d].path", index)
		path, err := normalizeExistingBasePath(normalized.Bases[index].Path, pathField)
		if err != nil {
			return model.Config{}, err
		}
		normalized.Bases[index].Path = path
	}

	normalized.CurrentBase = strings.TrimSpace(normalized.CurrentBase)
	if baseIndex(normalized.Bases, normalized.CurrentBase) < 0 {
		return model.Config{}, fieldError(ErrBaseNotFound, "current_base", "current base is not configured")
	}

	normalized.BaseDir = strings.TrimSpace(normalized.BaseDir)
	if normalized.BaseDir != "" {
		baseDir, err := filepath.Abs(normalized.BaseDir)
		if err != nil {
			return model.Config{}, fieldErrorWithCause(ErrInvalidConfig, err, "base_dir", "resolve base directory")
		}
		normalized.BaseDir = filepath.Clean(baseDir)
	}
	return normalized, nil
}

func normalizeExistingBasePath(path, field string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fieldError(ErrInvalidPath, field, "base path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fieldErrorWithCause(ErrInvalidPath, err, field, "resolve base path")
	}
	canonicalPath, err := filepath.EvalSymlinks(filepath.Clean(absPath))
	if err != nil {
		return "", fieldErrorWithCause(ErrInvalidPath, err, field, "resolve base path symlinks")
	}
	canonicalPath = filepath.Clean(canonicalPath)
	info, err := os.Stat(canonicalPath)
	if err != nil {
		return "", fieldErrorWithCause(ErrInvalidPath, err, field, "inspect base path")
	}
	if !info.IsDir() {
		return "", fieldError(ErrInvalidPath, field, "base path must be an existing directory")
	}
	return canonicalPath, nil
}

func fieldError(kind error, field, message string) error {
	return &FieldError{Kind: kind, Field: field, Message: message}
}

func fieldErrorWithCause(kind, cause error, field, message string) error {
	return &FieldError{Kind: errors.Join(kind, cause), Field: field, Message: fmt.Sprintf("%s: %v", message, cause)}
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
