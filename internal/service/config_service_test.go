package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigServiceNeedsInitialization(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		service := NewConfigService(filepath.Join(t.TempDir(), "config.json"))

		got, err := service.NeedsInitialization()
		if err != nil {
			t.Fatalf("NeedsInitialization() error = %v, want nil", err)
		}
		if got != true {
			t.Errorf("NeedsInitialization() = %v, want true", got)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, nil, 0644); err != nil {
			t.Fatalf("os.WriteFile() error = %v, want nil", err)
		}
		service := NewConfigService(configPath)

		got, err := service.NeedsInitialization()
		if err != nil {
			t.Fatalf("NeedsInitialization() error = %v, want nil", err)
		}
		if got != true {
			t.Errorf("NeedsInitialization() = %v, want true", got)
		}
	})

	t.Run("non-empty file", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
			t.Fatalf("os.WriteFile() error = %v, want nil", err)
		}
		service := NewConfigService(configPath)

		got, err := service.NeedsInitialization()
		if err != nil {
			t.Fatalf("NeedsInitialization() error = %v, want nil", err)
		}
		if got != false {
			t.Errorf("NeedsInitialization() = %v, want false", got)
		}
	})

	t.Run("stat error", func(t *testing.T) {
		dir := t.TempDir()
		notDirectory := filepath.Join(dir, "not-a-directory")
		if err := os.WriteFile(notDirectory, nil, 0644); err != nil {
			t.Fatalf("os.WriteFile() error = %v, want nil", err)
		}
		service := NewConfigService(filepath.Join(notDirectory, "config.json"))

		got, err := service.NeedsInitialization()
		if err == nil {
			t.Fatalf("NeedsInitialization() = (%v, %v), want non-nil error", got, err)
		}
	})
}
