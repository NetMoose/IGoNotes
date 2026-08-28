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
	"IGoNotes/internal/repository"
)

func boolPointer(value bool) *bool { return &value }

type countingConfigStore struct {
	*ConfigService
	saveCalls int
}

func (s *countingConfigStore) Save(config *model.Config) error {
	s.saveCalls++
	return s.ConfigService.Save(config)
}

func newProductionNoteService(t *testing.T, basePath string) *NoteService {
	t.Helper()
	db, err := repository.InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("repository.InitDB() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("database Close() error = %v, want nil", err)
		}
	})
	notes := NewNoteService(repository.NewNoteRepository(db), basePath)
	t.Cleanup(func() {
		if err := notes.Close(); err != nil {
			t.Errorf("NoteService.Close() error = %v, want nil", err)
		}
	})
	return notes
}

func TestResolveStartupBaseAllowsStructurallyEmptyConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	original := []byte(`{}`)
	if err := os.WriteFile(configPath, original, 0600); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	dataDir := filepath.Join(root, "data")

	gotPath, err := ResolveStartupBase(NewConfigService(configPath), "", dataDir)
	if err != nil {
		t.Fatalf("ResolveStartupBase() error = %v, want nil", err)
	}
	if gotPath != "" {
		t.Errorf("ResolveStartupBase() = %q, want empty path", gotPath)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v, want nil", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("config changed after ResolveStartupBase()\nbefore: %s\nafter:  %s", original, after)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("os.Stat(%q) error = %v, want os.ErrNotExist", dataDir, err)
	}
}

func TestEmptyConfigStartupCanCompleteSetup(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{name: "connect", mode: "connect"},
		{name: "create", mode: "create"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config.json")
			if err := os.WriteFile(configPath, []byte(`{}`), 0600); err != nil {
				t.Fatalf("os.WriteFile() error = %v, want nil", err)
			}
			configService := NewConfigService(configPath)

			basePath, err := ResolveStartupBase(configService, "", filepath.Join(root, "data"))
			if err != nil {
				t.Fatalf("ResolveStartupBase() error = %v, want nil", err)
			}
			if basePath != "" {
				t.Fatalf("ResolveStartupBase() = %q, want empty path", basePath)
			}

			notes := newProductionNoteService(t, basePath)
			store := &countingConfigStore{ConfigService: configService}
			settings, err := NewSettingsService(store, notes, "", nil)
			if err != nil {
				t.Fatalf("NewSettingsService() error = %v, want nil", err)
			}
			if settings.SetupCompleted() {
				t.Fatal("SetupCompleted() = true, want false")
			}
			if store.saveCalls != 1 {
				t.Fatalf("migration Save calls = %d, want 1", store.saveCalls)
			}
			migrated, err := configService.Load()
			if err != nil {
				t.Fatalf("Load() after migration error = %v, want nil", err)
			}
			if migrated.SetupCompleted == nil || *migrated.SetupCompleted {
				t.Fatalf("migrated SetupCompleted = %v, want false", migrated.SetupCompleted)
			}

			requestPath := filepath.Join(root, "base-parent")
			if err := os.Mkdir(requestPath, 0755); err != nil {
				t.Fatalf("os.Mkdir() error = %v, want nil", err)
			}
			wantPath := requestPath
			if tt.mode == "create" {
				wantPath = filepath.Join(requestPath, "work")
			}
			response, err := settings.CompleteSetup(model.BaseMutationRequest{
				Mode: tt.mode,
				Name: "work",
				Path: requestPath,
			})
			if err != nil {
				t.Fatalf("CompleteSetup() error = %v, want nil", err)
			}
			if !settings.SetupCompleted() {
				t.Error("SetupCompleted() = false, want true")
			}
			if response.BasePath != wantPath || notes.GetBasePath() != wantPath {
				t.Errorf("runtime paths = response %q, service %q; want %q", response.BasePath, notes.GetBasePath(), wantPath)
			}
			if store.saveCalls != 2 {
				t.Errorf("total Save calls = %d, want migration and setup saves", store.saveCalls)
			}
			if _, err := notes.GetTree(); err != nil {
				t.Errorf("GetTree() after setup error = %v, want nil", err)
			}
		})
	}
}

func TestExplicitIncompleteEmptyConfigStartupDoesNotMigrate(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	original := []byte(`{"setup_completed":false}`)
	if err := os.WriteFile(configPath, original, 0600); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	configService := NewConfigService(configPath)

	basePath, err := ResolveStartupBase(configService, "", filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("ResolveStartupBase() error = %v, want nil", err)
	}
	store := &countingConfigStore{ConfigService: configService}
	settings, err := NewSettingsService(store, newProductionNoteService(t, basePath), "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v, want nil", err)
	}
	if settings.SetupCompleted() {
		t.Error("SetupCompleted() = true, want false")
	}
	if store.saveCalls != 0 {
		t.Errorf("migration Save calls = %d, want 0", store.saveCalls)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v, want nil", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("config changed during startup\nbefore: %s\nafter:  %s", original, after)
	}
}

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
			name: "completed structurally empty config",
			config: &model.Config{
				SetupCompleted: boolPointer(true),
			},
			wantErrors: []string{"current_base", "пустым"},
		},
		{
			name:          "CLI base with structurally empty config",
			config:        &model.Config{},
			requestedBase: "missing",
			wantErrors:    []string{"--base", "missing", "нет настроенных баз"},
		},
		{
			name: "base directory without bases",
			config: &model.Config{
				BaseDir: validPath,
			},
			wantErrors: []string{"current_base", "пустым"},
		},
		{
			name: "bases without current base",
			config: &model.Config{
				Bases: []model.Base{{Name: "personal", Path: validPath}},
			},
			wantErrors: []string{"current_base", "пустым"},
		},
		{
			name: "current base without bases",
			config: &model.Config{
				CurrentBase: "personal",
			},
			wantErrors: []string{"current_base", "personal", "нет настроенных баз"},
		},
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
