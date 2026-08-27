package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"IGoNotes/internal/model"
)

const defaultBaseName = "default"

// ResolveStartupBase initializes configuration when needed and returns the selected base path.
func ResolveStartupBase(configService *ConfigService, requestedBase, dataDir string) (string, error) {
	needsInitialization, err := configService.NeedsInitialization()
	if err != nil {
		return "", fmt.Errorf("не удалось проверить конфигурацию: %w", err)
	}

	var config *model.Config
	if needsInitialization {
		config, err = initializeDefaultConfig(configService, dataDir)
		if err != nil {
			return "", err
		}
	} else {
		config, err = configService.Load()
		if err != nil {
			return "", fmt.Errorf("не удалось загрузить конфигурацию: %w", err)
		}
	}

	structurallyEmpty := config.BaseDir == "" && len(config.Bases) == 0 && config.CurrentBase == ""
	setupIncomplete := config.SetupCompleted == nil || !*config.SetupCompleted
	if structurallyEmpty && setupIncomplete && requestedBase == "" {
		return "", nil
	}

	return selectConfiguredBase(config, requestedBase)
}

func initializeDefaultConfig(configService *ConfigService, dataDir string) (*model.Config, error) {
	baseRoot, err := filepath.Abs(filepath.Join(dataDir, "bases"))
	if err != nil {
		return nil, fmt.Errorf("не удалось определить абсолютный путь каталога баз: %w", err)
	}
	basePath := filepath.Join(baseRoot, defaultBaseName)
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("не удалось создать базу по умолчанию %q: %w", basePath, err)
	}

	setupCompleted := false
	config := &model.Config{
		BaseDir: filepath.Clean(baseRoot),
		Bases: []model.Base{{
			Name:     defaultBaseName,
			Path:     filepath.Clean(basePath),
			AutoSync: false,
		}},
		CurrentBase:    defaultBaseName,
		SetupCompleted: &setupCompleted,
	}
	if err := configService.Save(config); err != nil {
		return nil, fmt.Errorf("не удалось сохранить первоначальную конфигурацию: %w", err)
	}

	return config, nil
}

func selectConfiguredBase(config *model.Config, requestedBase string) (string, error) {
	basesByName := make(map[string]model.Base, len(config.Bases))
	availableNames := make([]string, 0, len(config.Bases))
	for _, base := range config.Bases {
		if _, exists := basesByName[base.Name]; exists {
			return "", fmt.Errorf("конфигурация содержит повторяющееся имя базы %q", base.Name)
		}
		basesByName[base.Name] = base
		availableNames = append(availableNames, base.Name)
	}

	selectedName := requestedBase
	source := "--base"
	if selectedName == "" {
		selectedName = config.CurrentBase
		source = "current_base"
	}
	if selectedName == "" {
		return "", fmt.Errorf("поле %s не может быть пустым", source)
	}

	base, exists := basesByName[selectedName]
	if !exists {
		available := strings.Join(availableNames, ", ")
		if available == "" {
			available = "нет настроенных баз"
		}
		return "", fmt.Errorf("%s %q не соответствует настроенной базе; доступны: %s", source, selectedName, available)
	}
	if base.Path == "" {
		return "", fmt.Errorf("у базы %q пустой путь", selectedName)
	}
	info, err := os.Stat(base.Path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("путь базы %q не существует", base.Path)
	}
	if err != nil {
		return "", fmt.Errorf("не удалось проверить путь базы %q: %w", base.Path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("путь базы %q не является каталогом", base.Path)
	}
	return base.Path, nil
}
