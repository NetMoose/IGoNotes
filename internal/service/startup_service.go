package service

import (
	"fmt"
	"os"
	"path/filepath"

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

	return selectConfiguredBase(config, requestedBase)
}

func initializeDefaultConfig(configService *ConfigService, dataDir string) (*model.Config, error) {
	baseRoot := filepath.Join(dataDir, "bases")
	basePath := filepath.Join(baseRoot, defaultBaseName)
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("не удалось создать базу по умолчанию %q: %w", basePath, err)
	}

	config := &model.Config{
		BaseDir: baseRoot,
		Bases: []model.Base{{
			Name:     defaultBaseName,
			Path:     basePath,
			AutoSync: false,
		}},
		CurrentBase: defaultBaseName,
	}
	if err := configService.Save(config); err != nil {
		return nil, fmt.Errorf("не удалось сохранить первоначальную конфигурацию: %w", err)
	}

	return config, nil
}

func selectConfiguredBase(config *model.Config, requestedBase string) (string, error) {
	selectedName := requestedBase
	source := "--base"
	if selectedName == "" {
		selectedName = config.CurrentBase
		source = "current_base"
	}
	if selectedName == "" {
		return "", fmt.Errorf("поле %s не может быть пустым", source)
	}

	for _, base := range config.Bases {
		if base.Name != selectedName {
			continue
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

	return "", fmt.Errorf("%s %q не соответствует настроенной базе", source, selectedName)
}
