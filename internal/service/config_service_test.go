package service

import (
	"bytes"
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
	replaceErr := errors.New("replace failed")
	svc.replace = func(_, _ string) error { return replaceErr }

	err := svc.Save(&model.Config{CurrentBase: "new"})
	if !errors.Is(err, replaceErr) {
		t.Fatalf("Save() error = %v, want wrapped error %v", err, replaceErr)
	}
	if !strings.Contains(err.Error(), "replace config") {
		t.Errorf("Save() error = %v, want context %q", err, "replace config")
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
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	original := []byte(`{"current_base":"old"}`)
	if err := os.WriteFile(configPath, original, 0600); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	if err := os.Chmod(configPath, 0600); err != nil {
		t.Fatalf("os.Chmod() error = %v, want nil", err)
	}
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

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v, want nil", err)
	}
	if bytes.Equal(data, original) {
		t.Errorf("config contents = %q, want old data replaced", data)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("os.Stat() error = %v, want nil", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0600 {
		t.Errorf("config mode = %04o, want %04o", gotMode, os.FileMode(0600))
	}

	temporaryFiles, err := filepath.Glob(filepath.Join(dir, ".config-*.tmp"))
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v, want nil", err)
	}
	if len(temporaryFiles) != 0 {
		t.Errorf("temporary files = %v, want none", temporaryFiles)
	}
}

func TestConfigServiceSaveCreatesConfigWithPrivatePermissions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	svc := NewConfigService(configPath)

	if err := svc.Save(&model.Config{CurrentBase: "work"}); err != nil {
		t.Fatalf("Save() error = %v, want nil", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("os.Stat() error = %v, want nil", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0600 {
		t.Errorf("config mode = %04o, want %04o", gotMode, os.FileMode(0600))
	}

	temporaryFiles, err := filepath.Glob(filepath.Join(filepath.Dir(configPath), ".config-*.tmp"))
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v, want nil", err)
	}
	if len(temporaryFiles) != 0 {
		t.Errorf("temporary files = %v, want none", temporaryFiles)
	}
}

func TestConfigServiceSaveReportsConfigStatError(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config\x00.json")
	svc := NewConfigService(configPath)

	err := svc.Save(&model.Config{CurrentBase: "work"})
	if err == nil || !strings.Contains(err.Error(), "stat config") {
		t.Fatalf("Save() error = %v, want error containing %q", err, "stat config")
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
