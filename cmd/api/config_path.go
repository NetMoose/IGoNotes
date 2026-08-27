package main

import (
	"fmt"
	"path/filepath"
)

func resolveConfigDir(explicitDir string, userConfigDir func() (string, error)) (string, error) {
	if explicitDir != "" {
		return explicitDir, nil
	}
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("не удалось определить пользовательский каталог конфигурации: %w", err)
	}
	return filepath.Join(dir, "igonotes"), nil
}
