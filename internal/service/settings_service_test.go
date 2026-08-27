package service

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

type fakeConfigStore struct {
	config    *model.Config
	loadErr   error
	saveErr   error
	saveCalls int
}

func (f *fakeConfigStore) Load() (*model.Config, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if f.config == nil {
		return nil, nil
	}
	config := cloneConfig(*f.config)
	return &config, nil
}

func (f *fakeConfigStore) Save(config *model.Config) error {
	f.saveCalls++
	if f.saveErr != nil {
		return f.saveErr
	}
	cloned := cloneConfig(*config)
	f.config = &cloned
	return nil
}

type fakeBaseRuntime struct {
	path        string
	pathCalls   int
	switchCalls []string
	switchErr   error
}

func (f *fakeBaseRuntime) GetBasePath() string {
	f.pathCalls++
	return f.path
}

func (f *fakeBaseRuntime) SwitchBase(path string) error {
	f.switchCalls = append(f.switchCalls, path)
	return f.switchErr
}

func TestNewSettingsServiceMigratesLegacyConfig(t *testing.T) {
	store := &fakeConfigStore{config: &model.Config{
		BaseDir:     "/notes",
		Bases:       []model.Base{{Name: "personal", Path: "/notes/personal"}},
		CurrentBase: "personal",
	}}

	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/personal"}, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	if !service.SetupCompleted() {
		t.Error("SetupCompleted() = false, want true")
	}
	if store.saveCalls != 1 {
		t.Fatalf("Save calls = %d, want 1", store.saveCalls)
	}
	if store.config.SetupCompleted == nil || !*store.config.SetupCompleted {
		t.Errorf("persisted SetupCompleted = %v, want true", store.config.SetupCompleted)
	}
}

func TestNewSettingsServiceMigratesStructurallyEmptyConfig(t *testing.T) {
	store := &fakeConfigStore{config: &model.Config{}}

	service, err := NewSettingsService(store, &fakeBaseRuntime{}, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	if service.SetupCompleted() {
		t.Error("SetupCompleted() = true, want false")
	}
	if store.saveCalls != 1 {
		t.Fatalf("Save calls = %d, want 1", store.saveCalls)
	}
	if store.config.SetupCompleted == nil || *store.config.SetupCompleted {
		t.Errorf("persisted SetupCompleted = %v, want false", store.config.SetupCompleted)
	}
}

func TestNewSettingsServicePreservesExplicitSetupState(t *testing.T) {
	tests := []struct {
		name        string
		completed   bool
		config      model.Config
		runtimePath string
	}{
		{name: "false", completed: false},
		{
			name:      "true",
			completed: true,
			config: model.Config{
				Bases:       []model.Base{{Name: "personal", Path: "/notes/personal"}},
				CurrentBase: "personal",
			},
			runtimePath: "/notes/personal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completed := tt.completed
			config := cloneConfig(tt.config)
			config.SetupCompleted = &completed
			store := &fakeConfigStore{config: &config}

			service, err := NewSettingsService(store, &fakeBaseRuntime{path: tt.runtimePath}, "", nil)
			if err != nil {
				t.Fatalf("NewSettingsService() error = %v", err)
			}
			if got := service.SetupCompleted(); got != tt.completed {
				t.Errorf("SetupCompleted() = %t, want %t", got, tt.completed)
			}
			if store.saveCalls != 0 {
				t.Errorf("Save calls = %d, want 0", store.saveCalls)
			}
		})
	}
}

func TestNewSettingsServiceReturnsMigrationSaveError(t *testing.T) {
	saveErr := errors.New("disk full")
	store := &fakeConfigStore{config: &model.Config{}, saveErr: saveErr}

	service, err := NewSettingsService(store, &fakeBaseRuntime{}, "", nil)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, saveErr) {
		t.Fatalf("NewSettingsService() error = %v, want wrapped %v", err, saveErr)
	}
	if got, want := err.Error(), "migrate setup state: disk full"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestNewSettingsServiceAppliesCLIBaseToSnapshotOnly(t *testing.T) {
	completed := true
	workPath := filepath.Join("notes", "work")
	store := &fakeConfigStore{config: &model.Config{
		Bases: []model.Base{
			{Name: "personal", Path: filepath.Join("notes", "personal")},
			{Name: "work", Path: workPath},
		},
		CurrentBase:    "personal",
		SetupCompleted: &completed,
	}}
	runtime := &fakeBaseRuntime{path: filepath.Join("notes", ".", "work")}

	service, err := NewSettingsService(store, runtime, "work", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	if got := service.GetConfig().CurrentBase; got != "work" {
		t.Errorf("service CurrentBase = %q, want work", got)
	}
	if got := store.config.CurrentBase; got != "personal" {
		t.Errorf("persisted CurrentBase = %q, want personal", got)
	}
	if store.saveCalls != 0 {
		t.Errorf("Save calls = %d, want 0", store.saveCalls)
	}
	if runtime.pathCalls != 1 {
		t.Errorf("GetBasePath calls = %d, want 1", runtime.pathCalls)
	}
	if len(runtime.switchCalls) != 0 {
		t.Errorf("SwitchBase calls = %v, want none", runtime.switchCalls)
	}
}

func TestNewSettingsServiceRejectsUnknownCLIBase(t *testing.T) {
	completed := true
	store := &fakeConfigStore{config: &model.Config{
		Bases:          []model.Base{{Name: "Work", Path: "/notes/work"}},
		CurrentBase:    "Work",
		SetupCompleted: &completed,
	}}

	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/work"}, "work", nil)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, ErrBaseNotFound) {
		t.Fatalf("NewSettingsService() error = %v, want ErrBaseNotFound", err)
	}
}

func TestNewSettingsServiceRejectsMismatchedRuntimePath(t *testing.T) {
	completed := true
	store := &fakeConfigStore{config: &model.Config{
		Bases:          []model.Base{{Name: "work", Path: "/notes/work"}},
		CurrentBase:    "personal",
		SetupCompleted: &completed,
	}}

	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/personal"}, "work", nil)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewSettingsService() error = %v, want ErrInvalidConfig", err)
	}
	var fieldErr *FieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("NewSettingsService() error type = %T, want *FieldError", err)
	}
	if fieldErr.Field != "current_base" {
		t.Errorf("FieldError.Field = %q, want current_base", fieldErr.Field)
	}
}

func TestNewSettingsServiceRejectsNilLoadedConfig(t *testing.T) {
	service, err := NewSettingsService(&fakeConfigStore{}, &fakeBaseRuntime{}, "", nil)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewSettingsService() error = %v, want ErrInvalidConfig", err)
	}
}

func TestNewSettingsServiceReturnsLoadError(t *testing.T) {
	loadErr := errors.New("permission denied")

	service, err := NewSettingsService(
		&fakeConfigStore{loadErr: loadErr},
		&fakeBaseRuntime{},
		"",
		nil,
	)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, loadErr) {
		t.Fatalf("NewSettingsService() error = %v, want wrapped %v", err, loadErr)
	}
	if got, want := err.Error(), "load settings: permission denied"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestNewSettingsServiceRejectsNilConfigStore(t *testing.T) {
	service, err := NewSettingsService(nil, &fakeBaseRuntime{}, "", nil)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewSettingsService() error = %v, want ErrInvalidConfig", err)
	}
	if !strings.Contains(err.Error(), "config store") {
		t.Errorf("NewSettingsService() error = %q, want config store context", err)
	}
}

func TestNewSettingsServiceRejectsNilBaseRuntime(t *testing.T) {
	completed := false
	store := &fakeConfigStore{config: &model.Config{SetupCompleted: &completed}}

	service, err := NewSettingsService(store, nil, "", nil)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewSettingsService() error = %v, want ErrInvalidConfig", err)
	}
	if !strings.Contains(err.Error(), "base runtime") {
		t.Errorf("NewSettingsService() error = %q, want base runtime context", err)
	}
}

func TestNewSettingsServiceRejectsUnknownPersistedCurrentBase(t *testing.T) {
	completed := true
	store := &fakeConfigStore{config: &model.Config{
		Bases:          []model.Base{{Name: "work", Path: "/notes/work"}},
		CurrentBase:    "missing",
		SetupCompleted: &completed,
	}}

	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/work"}, "", nil)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, ErrBaseNotFound) {
		t.Fatalf("NewSettingsService() error = %v, want ErrBaseNotFound", err)
	}
	if store.saveCalls != 0 {
		t.Errorf("Save calls = %d, want 0", store.saveCalls)
	}
}

func TestNewSettingsServiceRejectsMismatchedPersistedCurrentBasePath(t *testing.T) {
	completed := true
	store := &fakeConfigStore{config: &model.Config{
		Bases:          []model.Base{{Name: "work", Path: "/notes/work"}},
		CurrentBase:    "work",
		SetupCompleted: &completed,
	}}

	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/personal"}, "", nil)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewSettingsService() error = %v, want ErrInvalidConfig", err)
	}
	var fieldErr *FieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("NewSettingsService() error type = %T, want *FieldError", err)
	}
	if fieldErr.Field != "current_base" {
		t.Errorf("FieldError.Field = %q, want current_base", fieldErr.Field)
	}
}

func TestNewSettingsServiceAcceptsMatchingPersistedCurrentBase(t *testing.T) {
	completed := true
	store := &fakeConfigStore{config: &model.Config{
		Bases:          []model.Base{{Name: "work", Path: filepath.Join("notes", "work")}},
		CurrentBase:    "work",
		SetupCompleted: &completed,
	}}
	runtime := &fakeBaseRuntime{path: filepath.Join("notes", ".", "work")}

	service, err := NewSettingsService(store, runtime, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	if got := service.GetConfig().CurrentBase; got != "work" {
		t.Errorf("CurrentBase = %q, want work", got)
	}
	if runtime.pathCalls != 1 {
		t.Errorf("GetBasePath calls = %d, want 1", runtime.pathCalls)
	}
	if store.saveCalls != 0 {
		t.Errorf("Save calls = %d, want 0", store.saveCalls)
	}
}

func TestSettingsServiceGetConfigReturnsDeepSnapshot(t *testing.T) {
	completed := true
	store := &fakeConfigStore{config: &model.Config{
		Bases:          []model.Base{{Name: "personal", Path: "/notes/personal"}},
		CurrentBase:    "personal",
		SetupCompleted: &completed,
	}}
	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/personal"}, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	snapshot := service.GetConfig()
	snapshot.Bases[0].Name = "changed"
	*snapshot.SetupCompleted = false

	got := service.GetConfig()
	if got.Bases[0].Name != "personal" {
		t.Errorf("Bases[0].Name = %q, want personal", got.Bases[0].Name)
	}
	if got.SetupCompleted == nil || !*got.SetupCompleted {
		t.Errorf("SetupCompleted = %v, want true", got.SetupCompleted)
	}
}
