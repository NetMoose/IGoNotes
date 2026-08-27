package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

func TestConfigServiceSavePreservesOriginalWhenReplaceFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	original := []byte(`{"current_base":"old"}`)
	if err := os.WriteFile(configPath, original, 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}

	svc := NewConfigService(configPath)
	svc.replace = func(_, _ string) error { return errors.New("replace failed") }

	err := svc.Save(&model.Config{CurrentBase: "new"})
	if err == nil || !strings.Contains(err.Error(), "replace failed") {
		t.Fatalf("Save() error = %v, want error containing %q", err, "replace failed")
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v, want nil", err)
	}
	if string(got) != string(original) {
		t.Errorf("config contents = %q, want %q", got, original)
	}

	temporaryFiles, err := filepath.Glob(filepath.Join(dir, ".config-*.tmp"))
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v, want nil", err)
	}
	if len(temporaryFiles) != 0 {
		t.Errorf("temporary files = %v, want none", temporaryFiles)
	}
}

func TestConfigServiceSaveAtomicallyReplacesConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	setupCompleted := true
	svc := NewConfigService(configPath)

	if err := svc.Save(&model.Config{
		CurrentBase:    "work",
		SetupCompleted: &setupCompleted,
	}); err != nil {
		t.Fatalf("Save() error = %v, want nil", err)
	}

	got, err := svc.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if got.CurrentBase != "work" {
		t.Errorf("CurrentBase = %q, want %q", got.CurrentBase, "work")
	}
	if got.SetupCompleted == nil || !*got.SetupCompleted {
		t.Errorf("SetupCompleted = %v, want true", got.SetupCompleted)
	}
}

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

	t.Run("directory path", func(t *testing.T) {
		service := NewConfigService(t.TempDir())

		got, err := service.NeedsInitialization()
		if err == nil {
			t.Fatalf("NeedsInitialization() = (%v, %v), want non-nil error", got, err)
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
