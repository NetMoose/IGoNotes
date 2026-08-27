//go:build !windows

package service

import (
	"os"
	"path/filepath"
	"testing"

	"IGoNotes/internal/model"
)

func TestConfigServiceSavePreservesExistingConfigPermissions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"current_base":"old"}`), 0600); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	if err := os.Chmod(configPath, 0640); err != nil {
		t.Fatalf("os.Chmod() error = %v, want nil", err)
	}

	svc := NewConfigService(configPath)
	if err := svc.Save(&model.Config{CurrentBase: "work"}); err != nil {
		t.Fatalf("Save() error = %v, want nil", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("os.Stat() error = %v, want nil", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0640 {
		t.Errorf("config mode = %04o, want %04o", gotMode, os.FileMode(0640))
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
}
