package service

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

func boolPointer(value bool) *bool { return &value }

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
				CurrentBase:    "default",
				SetupCompleted: boolPointer(false),
			}
			if !reflect.DeepEqual(gotConfig, wantConfig) {
				t.Errorf("saved config = %#v, want %#v", gotConfig, wantConfig)
			}

			serialized, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("os.ReadFile() error = %v, want nil", err)
			}
			if !bytes.Contains(serialized, []byte(`"setup_completed": false`)) {
				t.Errorf("saved config = %s, want setup_completed false", serialized)
			}
		})
	}
}

func TestResolveStartupBaseStoresAbsoluteDefaultPath(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	configPath := filepath.Join(root, "config", "config.json")
	configService := NewConfigService(configPath)

	gotPath, err := ResolveStartupBase(configService, "", ".igonotes")
	if err != nil {
		t.Fatalf("ResolveStartupBase() error = %v, want nil", err)
	}
	if !filepath.IsAbs(gotPath) {
		t.Errorf("ResolveStartupBase() = %q, want absolute path", gotPath)
	}

	config, err := configService.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !filepath.IsAbs(config.BaseDir) {
		t.Errorf("saved BaseDir = %q, want absolute path", config.BaseDir)
	}
	if len(config.Bases) != 1 {
		t.Fatalf("saved Bases length = %d, want 1", len(config.Bases))
	}
	if !filepath.IsAbs(config.Bases[0].Path) {
		t.Errorf("saved default base path = %q, want absolute path", config.Bases[0].Path)
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

func TestResolveStartupBaseRejectsInvalidConfig(t *testing.T) {
	root := t.TempDir()
	validPath := filepath.Join(root, "valid")
	if err := os.MkdirAll(validPath, 0755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v, want nil", validPath, err)
	}
	filePath := filepath.Join(root, "file")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v, want nil", filePath, err)
	}
	invalidPath := "bad\x00path"
	_, statErr := os.Stat(invalidPath)
	if statErr == nil {
		t.Fatalf("os.Stat(%q) error = nil, want non-nil", invalidPath)
	}
	if os.IsNotExist(statErr) {
		t.Fatalf("os.Stat(%q) error = %v, want error other than not exist", invalidPath, statErr)
	}
	var pathErr *os.PathError
	if !errors.As(statErr, &pathErr) {
		t.Fatalf("os.Stat(%q) error = %T, want *os.PathError", invalidPath, statErr)
	}
	invalidPathCause := pathErr.Err

	tests := []struct {
		name          string
		config        *model.Config
		requestedBase string
		wantErrors    []string
		wantCause     error
	}{
		{
			name: "unknown CLI base",
			config: &model.Config{
				Bases: []model.Base{
					{Name: "personal", Path: validPath},
					{Name: "work", Path: validPath},
				},
				CurrentBase: "personal",
			},
			requestedBase: "missing",
			wantErrors:    []string{"--base", "missing", "personal, work"},
		},
		{
			name: "base names are case-sensitive",
			config: &model.Config{
				Bases:       []model.Base{{Name: "Work", Path: validPath}},
				CurrentBase: "Work",
			},
			requestedBase: "work",
			wantErrors:    []string{"--base", "work", "Work"},
		},
		{
			name: "unknown current base",
			config: &model.Config{
				Bases:       []model.Base{{Name: "personal", Path: validPath}},
				CurrentBase: "missing",
			},
			wantErrors: []string{"current_base", "missing"},
		},
		{
			name: "empty current base",
			config: &model.Config{
				Bases: []model.Base{{Name: "", Path: validPath}},
			},
			wantErrors: []string{"current_base", "пустым"},
		},
		{
			name: "duplicate base name",
			config: &model.Config{
				Bases: []model.Base{
					{Name: "personal", Path: validPath},
					{Name: "personal", Path: validPath},
				},
				CurrentBase: "personal",
			},
			wantErrors: []string{"повторяющееся", "personal"},
		},
		{
			name: "duplicate unselected base name",
			config: &model.Config{
				Bases: []model.Base{
					{Name: "selected", Path: validPath},
					{Name: "duplicate", Path: validPath},
					{Name: "duplicate", Path: validPath},
				},
				CurrentBase: "selected",
			},
			wantErrors: []string{"повторяющееся", "duplicate"},
		},
		{
			name: "empty selected path",
			config: &model.Config{
				Bases:       []model.Base{{Name: "personal"}},
				CurrentBase: "personal",
			},
			wantErrors: []string{"personal", "пустой путь"},
		},
		{
			name: "missing selected path",
			config: &model.Config{
				Bases:       []model.Base{{Name: "personal", Path: filepath.Join(root, "missing")}},
				CurrentBase: "personal",
			},
			wantErrors: []string{"не существует"},
		},
		{
			name: "selected path is a regular file",
			config: &model.Config{
				Bases:       []model.Base{{Name: "personal", Path: filePath}},
				CurrentBase: "personal",
			},
			wantErrors: []string{"не является каталогом"},
		},
		{
			name: "selected path cannot be statted",
			config: &model.Config{
				Bases:       []model.Base{{Name: "personal", Path: invalidPath}},
				CurrentBase: "personal",
			},
			wantErrors: []string{"не удалось проверить"},
			wantCause:  invalidPathCause,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(root, tt.name, "config.json")
			configService := NewConfigService(configPath)
			if err := configService.Save(tt.config); err != nil {
				t.Fatalf("Save() error = %v, want nil", err)
			}
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("os.ReadFile() before ResolveStartupBase error = %v, want nil", err)
			}

			_, err = ResolveStartupBase(configService, tt.requestedBase, filepath.Join(root, "data"))
			if err == nil {
				t.Fatal("ResolveStartupBase() error = nil, want non-nil")
			}
			for _, wantError := range tt.wantErrors {
				if !strings.Contains(err.Error(), wantError) {
					t.Errorf("ResolveStartupBase() error = %q, want substring %q", err, wantError)
				}
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Errorf("ResolveStartupBase() error = %v, want cause %v", err, tt.wantCause)
			}

			after, readErr := os.ReadFile(configPath)
			if readErr != nil {
				t.Fatalf("os.ReadFile() after ResolveStartupBase error = %v, want nil", readErr)
			}
			if !bytes.Equal(after, before) {
				t.Errorf("config changed after ResolveStartupBase()\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func TestResolveStartupBaseDoesNotOverwriteMalformedConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v, want nil", err)
	}
	original := []byte(`{"bases": [`)
	if err := os.WriteFile(configPath, original, 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}

	_, err := ResolveStartupBase(NewConfigService(configPath), "", filepath.Join(root, "data"))
	if err == nil {
		t.Fatal("ResolveStartupBase() error = nil, want non-nil")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("os.ReadFile() after ResolveStartupBase error = %v, want nil", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("config changed after ResolveStartupBase()\nbefore: %s\nafter:  %s", original, after)
	}
}
