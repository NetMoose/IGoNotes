package service

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"IGoNotes/internal/model"
)

func TestResolveStartupBaseInitializesDefaultConfig(t *testing.T) {
	tests := []struct {
		name        string
		emptyConfig bool
	}{
		{name: "missing config"},
		{name: "empty config", emptyConfig: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config", "config.json")
			dataDir := filepath.Join(root, "data")
			if tt.emptyConfig {
				if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
					t.Fatalf("os.MkdirAll() error = %v, want nil", err)
				}
				if err := os.WriteFile(configPath, nil, 0644); err != nil {
					t.Fatalf("os.WriteFile() error = %v, want nil", err)
				}
			}

			gotPath, err := ResolveStartupBase(NewConfigService(configPath), "", dataDir)
			if err != nil {
				t.Fatalf("ResolveStartupBase() error = %v, want nil", err)
			}

			wantPath := filepath.Join(dataDir, "bases", "default")
			if gotPath != wantPath {
				t.Errorf("ResolveStartupBase() = %q, want %q", gotPath, wantPath)
			}
			info, err := os.Stat(wantPath)
			if err != nil {
				t.Fatalf("os.Stat(%q) error = %v, want nil", wantPath, err)
			}
			if !info.IsDir() {
				t.Errorf("initialized base %q is not a directory", wantPath)
			}

			gotConfig, err := NewConfigService(configPath).Load()
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			wantConfig := &model.Config{
				BaseDir: filepath.Join(dataDir, "bases"),
				Bases: []model.Base{{
					Name:     "default",
					Path:     wantPath,
					AutoSync: false,
				}},
				CurrentBase: "default",
			}
			if !reflect.DeepEqual(gotConfig, wantConfig) {
				t.Errorf("saved config = %#v, want %#v", gotConfig, wantConfig)
			}
		})
	}
}

func TestResolveStartupBaseSelectsConfiguredBase(t *testing.T) {
	tests := []struct {
		name          string
		requestedBase string
		wantBase      string
	}{
		{name: "current base", wantBase: "personal"},
		{name: "CLI override", requestedBase: "work", wantBase: "work"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config", "config.json")
			personalPath := filepath.Join(root, "personal")
			workPath := filepath.Join(root, "work")
			for _, path := range []string{personalPath, workPath} {
				if err := os.MkdirAll(path, 0755); err != nil {
					t.Fatalf("os.MkdirAll(%q) error = %v, want nil", path, err)
				}
			}

			configService := NewConfigService(configPath)
			config := &model.Config{
				BaseDir: root,
				Bases: []model.Base{
					{Name: "personal", Path: personalPath},
					{Name: "work", Path: workPath},
				},
				CurrentBase: "personal",
			}
			if err := configService.Save(config); err != nil {
				t.Fatalf("Save() error = %v, want nil", err)
			}
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("os.ReadFile() before ResolveStartupBase error = %v, want nil", err)
			}

			gotPath, err := ResolveStartupBase(configService, tt.requestedBase, filepath.Join(root, "data"))
			if err != nil {
				t.Fatalf("ResolveStartupBase() error = %v, want nil", err)
			}
			wantPath := personalPath
			if tt.wantBase == "work" {
				wantPath = workPath
			}
			if gotPath != wantPath {
				t.Errorf("ResolveStartupBase() = %q, want %q", gotPath, wantPath)
			}

			after, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("os.ReadFile() after ResolveStartupBase error = %v, want nil", err)
			}
			if !bytes.Equal(after, before) {
				t.Errorf("config changed after ResolveStartupBase()\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}
