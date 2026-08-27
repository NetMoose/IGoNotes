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
	configPath string
	replace    func(string, string) error
}

// NewConfigService создает новый экземпляр ConfigService
func NewConfigService(configPath string) *ConfigService {
	return &ConfigService{configPath: configPath, replace: replaceFile}
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

	if err := temporary.Chmod(0644); err != nil {
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
