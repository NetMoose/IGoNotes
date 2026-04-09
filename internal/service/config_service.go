package service

import (
	"encoding/json"
	"os"
	"path/filepath"

	"IGoNotes/internal/model"
)

// ConfigService предоставляет методы для работы с конфигурацией приложения
type ConfigService struct {
	configPath string
}

// NewConfigService создает новый экземпляр ConfigService
func NewConfigService(configPath string) *ConfigService {
	return &ConfigService{configPath: configPath}
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
	// Создаем директорию, если она не существует
	dir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.configPath, data, 0644)
}

// Exists проверяет, существует ли файл конфигурации
func (s *ConfigService) Exists() bool {
	_, err := os.Stat(s.configPath)
	return !os.IsNotExist(err)
}