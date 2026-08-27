package main

import (
	"fmt"
	"path/filepath"
)

func resolveDataDir(userHomeDir func() (string, error)) (string, error) {
	homeDir, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("не удалось определить домашний каталог пользователя: %w", err)
	}
	if homeDir == "" {
		return "", fmt.Errorf("домашний каталог пользователя пуст")
	}
	return filepath.Join(homeDir, ".igonotes"), nil
}
