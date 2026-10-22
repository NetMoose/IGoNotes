package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"IGoNotes/internal/model"
)

// ConfigService предоставляет методы для работы с конфигурацией приложения
type ConfigService struct {
	configPath     string
	replace        func(string, string) error
	privateStorage *PrivateStoragePolicy
}

// NewConfigService создает новый экземпляр ConfigService
func NewConfigService(configPath string, policy ...*PrivateStoragePolicy) *ConfigService {
	// Even callers without a full application layout reserve the config folder.
	configDir := filepath.Dir(configPath)
	if absolute, err := filepath.Abs(configDir); err == nil {
		configDir = absolute
	}
	p := &PrivateStoragePolicy{configDir: configDir}
	if len(policy) > 0 && policy[0] != nil {
		copied := *policy[0]
		// An explicit layout can add reservations but cannot exempt this store's
		// actual config folder. Copy rather than modifying the caller's policy.
		copied.configDir = configDir
		p = &copied
	}
	return &ConfigService{configPath: configPath, replace: replaceFile, privateStorage: p}
}

// PrivateStoragePolicy returns a detached snapshot, never the store's live policy.
func (s *ConfigService) PrivateStoragePolicy() *PrivateStoragePolicy {
	return s.privateStorage.clone()
}

// Load загружает конфигурацию из файла
func (s *ConfigService) Load() (*model.Config, error) {
	config := &model.Config{}

	// Проверяем существование файла
	if _, err := os.Stat(s.configPath); os.IsNotExist(err) {
		return config, nil // Возвращаем пустую конфигурацию для нового приложения
	}

	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return config, nil
	}

	err = json.Unmarshal(data, config)
	return config, err
}

// Save сохраняет конфигурацию в файл
func (s *ConfigService) Save(config *model.Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	mode := os.FileMode(0600)
	info, err := os.Stat(s.configPath)
	if err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat config: %w", err)
	}

	temporary, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()

	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	temporary = nil

	if err := s.replace(temporaryPath, s.configPath); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// NeedsInitialization сообщает, отсутствует ли файл конфигурации или является пустым.
func (s *ConfigService) NeedsInitialization() (bool, error) {
	info, err := os.Stat(s.configPath)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("путь конфигурации %q не является обычным файлом", s.configPath)
	}
	return info.Size() == 0, nil
}
