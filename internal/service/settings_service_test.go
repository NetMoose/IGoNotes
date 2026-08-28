package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"IGoNotes/internal/model"
)

type fakeConfigStore struct {
	config      *model.Config
	loadErr     error
	saveErr     error
	saveCalls   int
	saveStarted chan struct{}
	saveRelease <-chan struct{}
	events      *orderedEvents
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
	if f.events != nil {
		f.events.record("save")
	}
	if f.saveStarted != nil {
		close(f.saveStarted)
	}
	if f.saveRelease != nil {
		<-f.saveRelease
	}
	if f.saveErr != nil {
		return f.saveErr
	}
	cloned := cloneConfig(*config)
	f.config = &cloned
	return nil
}

type fakeBaseRuntime struct {
	path             string
	pathCalls        int
	switchCalls      []string
	persistCalls     int
	persistMatches   *bool
	transactionCalls []string
	switchErr        error
	switchErrs       []error
	persistErr       error
	matchErr         error
	events           *orderedEvents
	commitErr        error
}

func (f *fakeBaseRuntime) GetBasePath() string {
	f.pathCalls++
	return f.path
}

func (f *fakeBaseRuntime) SwitchBase(path string) error {
	f.switchCalls = append(f.switchCalls, path)
	if f.events != nil {
		f.events.record("switch:" + path)
	}
	if len(f.switchErrs) != 0 {
		err := f.switchErrs[0]
		f.switchErrs = f.switchErrs[1:]
		if err != nil {
			return err
		}
	} else if f.switchErr != nil {
		return f.switchErr
	}
	f.path = path
	return nil
}

func (f *fakeBaseRuntime) baseMatches(expectedPath string) (bool, error) {
	f.pathCalls++
	if f.matchErr != nil {
		return false, f.matchErr
	}
	matches := filepath.Clean(f.path) == filepath.Clean(expectedPath)
	if f.persistMatches != nil {
		matches = *f.persistMatches
	}
	return matches, nil
}

func (f *fakeBaseRuntime) persistConfig(expectedPath string, store ConfigStore, next *model.Config) (bool, error) {
	f.persistCalls++
	if f.persistErr != nil {
		return false, f.persistErr
	}
	matches := filepath.Clean(f.path) == filepath.Clean(expectedPath)
	if f.persistMatches != nil {
		matches = *f.persistMatches
	}
	if !matches {
		return false, nil
	}
	if err := store.Save(next); err != nil {
		return true, err
	}
	f.path = expectedPath
	return true, nil
}

func (f *fakeBaseRuntime) switchBaseTransaction(path string, store ConfigStore, next *model.Config) (error, error) {
	f.transactionCalls = append(f.transactionCalls, path)
	if f.events != nil {
		f.events.record("prepare:" + path)
	}
	if err := f.nextSwitchError(); err != nil {
		return fmt.Errorf("switch runtime base: %w", err), nil
	}
	if err := store.Save(next); err != nil {
		if f.events != nil {
			f.events.record("rollback:" + path)
		}
		return fmt.Errorf("save settings: %w", err), f.nextSwitchError()
	}
	if f.commitErr != nil {
		if f.events != nil {
			f.events.record("commit:" + path)
			f.events.record("rollback:" + path)
		}
		return fmt.Errorf("commit note index: %w", f.commitErr), commitOutcomeError(f.nextSwitchError())
	}
	f.path = path
	if f.events != nil {
		f.events.record("commit:" + path)
	}
	return nil, nil
}

func (f *fakeBaseRuntime) nextSwitchError() error {
	if len(f.switchErrs) != 0 {
		err := f.switchErrs[0]
		f.switchErrs = f.switchErrs[1:]
		return err
	}
	return f.switchErr
}

type orderedEvents struct {
	mu     sync.Mutex
	values []string
}

func (e *orderedEvents) record(value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.values = append(e.values, value)
}

func (e *orderedEvents) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.values...)
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
	notes := newTestNoteService(t, &fakeNoteRepository{}, "")

	service, err := NewSettingsService(store, notes, "", nil)
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

func TestNewSettingsServiceMigrationRejectsClosedRuntimeWithoutSave(t *testing.T) {
	basePath := t.TempDir()
	original := model.Config{
		Bases:       []model.Base{{Name: "active", Path: basePath}},
		CurrentBase: "active",
	}
	store := &fakeConfigStore{config: &original}
	notes := NewNoteService(&fakeNoteRepository{}, basePath)
	if err := notes.Close(); err != nil {
		t.Fatalf("NoteService.Close() error = %v", err)
	}

	service, err := NewSettingsService(store, notes, "", nil)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("NewSettingsService() error = %v, want os.ErrClosed", err)
	}
	if !strings.HasPrefix(err.Error(), "migrate setup state: ") {
		t.Errorf("NewSettingsService() error = %q, want migration context", err)
	}
	if store.saveCalls != 0 || !reflect.DeepEqual(*store.config, original) {
		t.Errorf("rejected migration changed store: saves %d config %#v", store.saveCalls, *store.config)
	}
}

func TestSettingsServiceEmptyHealthyRuntimePersistsConfigOnlyMutation(t *testing.T) {
	completed := false
	config := model.Config{SetupCompleted: &completed}
	store := &fakeConfigStore{config: &config}
	notes := newTestNoteService(t, &fakeNoteRepository{}, "")
	service, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	basePath := t.TempDir()

	response, err := service.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "first", Path: basePath})
	if err != nil {
		t.Fatalf("AddBase() error = %v", err)
	}
	if store.saveCalls != 1 {
		t.Errorf("Save calls = %d, want 1", store.saveCalls)
	}
	if response.BasePath != "" || notes.GetBasePath() != "" {
		t.Errorf("runtime path changed: response %q service %q", response.BasePath, notes.GetBasePath())
	}
	if len(response.Config.Bases) != 1 || response.Config.Bases[0].Path != basePath {
		t.Errorf("persisted config = %#v, want added base %q", response.Config, basePath)
	}
}

func TestNewSettingsServiceRejectsCLIBaseForStructurallyEmptyConfig(t *testing.T) {
	store := &fakeConfigStore{config: &model.Config{}}

	service, err := NewSettingsService(store, &fakeBaseRuntime{}, "missing", nil)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, ErrBaseNotFound) {
		t.Fatalf("NewSettingsService() error = %v, want ErrBaseNotFound", err)
	}
	if store.saveCalls != 0 {
		t.Errorf("Save calls = %d, want none before CLI base validation", store.saveCalls)
	}
	if store.config.SetupCompleted != nil {
		t.Errorf("persisted SetupCompleted = %v, want unchanged nil", store.config.SetupCompleted)
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
	if len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 {
		t.Errorf("runtime switch calls = %v / %v, want none", runtime.switchCalls, runtime.transactionCalls)
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

func TestSettingsServiceCompleteSetupCreatesSelectedBase(t *testing.T) {
	parent := t.TempDir()
	service, store, runtime, oldConfig := newIncompleteSettingsService(t)

	response, err := service.CompleteSetup(model.BaseMutationRequest{
		Mode: "create",
		Name: "  work  ",
		Path: "  " + parent + "  ",
	})
	if err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}
	target := filepath.Join(parent, "work")
	assertDirectory(t, target)
	want := model.Config{
		BaseDir:        parent,
		Bases:          []model.Base{{Name: "work", Path: target, AutoSync: false}},
		CurrentBase:    "work",
		SetupCompleted: settingsBoolPointer(true),
	}
	if !reflect.DeepEqual(service.GetConfig(), want) {
		t.Errorf("service config = %#v, want %#v", service.GetConfig(), want)
	}
	if !reflect.DeepEqual(*store.config, want) {
		t.Errorf("stored config = %#v, want %#v", *store.config, want)
	}
	if !reflect.DeepEqual(response.Config, want) {
		t.Errorf("response config = %#v, want %#v", response.Config, want)
	}
	if response.BasePath != target || runtime.path != target {
		t.Errorf("base paths = response %q, runtime %q; want %q", response.BasePath, runtime.path, target)
	}
	if !reflect.DeepEqual(runtime.transactionCalls, []string{target}) {
		t.Errorf("switchBaseTransaction calls = %v, want [%q]", runtime.transactionCalls, target)
	}
	if store.saveCalls != 1 {
		t.Errorf("Save calls = %d, want 1", store.saveCalls)
	}
	if reflect.DeepEqual(service.GetConfig(), oldConfig) {
		t.Error("service config was not replaced")
	}

	response.Config.Bases[0].Name = "changed"
	*response.Config.SetupCompleted = false
	if !reflect.DeepEqual(service.GetConfig(), want) {
		t.Errorf("response aliases service config: got %#v, want %#v", service.GetConfig(), want)
	}
}

func TestSettingsServiceCompleteSetupConnectsSelectedBase(t *testing.T) {
	basePath := t.TempDir()
	service, store, runtime, _ := newIncompleteSettingsService(t)

	response, err := service.CompleteSetup(model.BaseMutationRequest{
		Mode: "connect",
		Name: "  team/shared  ",
		Path: "  " + basePath + "  ",
	})
	if err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}
	absPath, err := filepath.Abs(basePath)
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	want := model.Config{
		BaseDir:        filepath.Dir(absPath),
		Bases:          []model.Base{{Name: "team/shared", Path: absPath}},
		CurrentBase:    "team/shared",
		SetupCompleted: settingsBoolPointer(true),
	}
	if !reflect.DeepEqual(response.Config, want) || !reflect.DeepEqual(*store.config, want) {
		t.Errorf("saved response/config = %#v / %#v, want %#v", response.Config, *store.config, want)
	}
	if response.BasePath != absPath || runtime.path != absPath {
		t.Errorf("base paths = response %q, runtime %q; want %q", response.BasePath, runtime.path, absPath)
	}
	if !reflect.DeepEqual(runtime.transactionCalls, []string{absPath}) {
		t.Errorf("switchBaseTransaction calls = %v, want [%q]", runtime.transactionCalls, absPath)
	}
}

func TestSettingsServiceCompleteSetupRejectsRepeatedSetupBeforeValidation(t *testing.T) {
	completed := true
	basePath := t.TempDir()
	config := model.Config{
		BaseDir:        filepath.Dir(basePath),
		Bases:          []model.Base{{Name: "work", Path: basePath}},
		CurrentBase:    "work",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config}
	runtime := &fakeBaseRuntime{path: basePath}
	service, err := NewSettingsService(store, runtime, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	_, err = service.CompleteSetup(model.BaseMutationRequest{})
	if !errors.Is(err, ErrSetupAlreadyCompleted) {
		t.Fatalf("CompleteSetup() error = %v, want ErrSetupAlreadyCompleted", err)
	}
	assertSettingsUnchanged(t, service, store, runtime, config, basePath, 0)
}

func TestSettingsServiceCompleteSetupRejectsInvalidRequestsWithoutMutation(t *testing.T) {
	regularFile := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(regularFile, []byte("notes"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	existingParent := t.TempDir()
	existingTarget := filepath.Join(existingParent, "work")
	if err := os.Mkdir(existingTarget, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	tests := []struct {
		name    string
		request model.BaseMutationRequest
		kind    error
		field   string
		cause   error
	}{
		{name: "invalid mode", request: model.BaseMutationRequest{Mode: "CREATE", Name: "work", Path: existingParent}, kind: ErrInvalidMode, field: "mode"},
		{name: "mode is not trimmed", request: model.BaseMutationRequest{Mode: " create ", Name: "work", Path: t.TempDir()}, kind: ErrInvalidMode, field: "mode"},
		{name: "whitespace name", request: model.BaseMutationRequest{Mode: "create", Name: " \t", Path: existingParent}, kind: ErrInvalidName, field: "name"},
		{name: "dot name", request: model.BaseMutationRequest{Mode: "create", Name: ".", Path: existingParent}, kind: ErrInvalidName, field: "name"},
		{name: "dot dot name", request: model.BaseMutationRequest{Mode: "create", Name: "..", Path: existingParent}, kind: ErrInvalidName, field: "name"},
		{name: "slash name", request: model.BaseMutationRequest{Mode: "create", Name: "a/b", Path: existingParent}, kind: ErrInvalidName, field: "name"},
		{name: "backslash name", request: model.BaseMutationRequest{Mode: "create", Name: `a\b`, Path: existingParent}, kind: ErrInvalidName, field: "name"},
		{name: "empty path", request: model.BaseMutationRequest{Mode: "create", Name: "work", Path: " \t"}, kind: ErrInvalidPath, field: "path"},
		{name: "missing create parent", request: model.BaseMutationRequest{Mode: "create", Name: "work", Path: missing}, kind: ErrInvalidPath, field: "path", cause: os.ErrNotExist},
		{name: "regular file create parent", request: model.BaseMutationRequest{Mode: "create", Name: "work", Path: regularFile}, kind: ErrInvalidPath, field: "path"},
		{name: "existing create target", request: model.BaseMutationRequest{Mode: "create", Name: "work", Path: existingParent}, kind: ErrBasePathConflict, field: "path"},
		{name: "missing connect base", request: model.BaseMutationRequest{Mode: "connect", Name: "work", Path: missing}, kind: ErrInvalidPath, field: "path", cause: os.ErrNotExist},
		{name: "regular file connect base", request: model.BaseMutationRequest{Mode: "connect", Name: "work", Path: regularFile}, kind: ErrInvalidPath, field: "path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, store, runtime, original := newIncompleteSettingsService(t)
			oldPath := runtime.path

			_, err := service.CompleteSetup(tt.request)
			if !errors.Is(err, tt.kind) {
				t.Fatalf("CompleteSetup() error = %v, want %v", err, tt.kind)
			}
			if tt.cause != nil && !errors.Is(err, tt.cause) {
				t.Errorf("CompleteSetup() error = %v, want underlying %v", err, tt.cause)
			}
			assertFieldError(t, err, tt.field)
			assertSettingsUnchanged(t, service, store, runtime, original, oldPath, 0)
		})
	}
}

func TestSettingsServiceCompleteSetupPreservesCreatedBaseAfterSwitchFailure(t *testing.T) {
	parent := t.TempDir()
	service, store, runtime, original := newIncompleteSettingsService(t)
	oldPath := runtime.path
	switchErr := errors.New("cannot open metadata")
	runtime.switchErr = switchErr

	_, err := service.CompleteSetup(model.BaseMutationRequest{Mode: "create", Name: "work", Path: parent})
	if !errors.Is(err, switchErr) {
		t.Fatalf("CompleteSetup() error = %v, want wrapped %v", err, switchErr)
	}
	assertDirectory(t, filepath.Join(parent, "work"))
	assertSettingsUnchanged(t, service, store, runtime, original, oldPath, 0)
}

func TestSettingsServiceCompleteSetupRollsBackRuntimeAndPreservesCreatedBaseAfterSaveFailure(t *testing.T) {
	parent := t.TempDir()
	service, store, runtime, original := newIncompleteSettingsService(t)
	oldPath := runtime.path
	saveErr := errors.New("disk full")
	store.saveErr = saveErr

	_, err := service.CompleteSetup(model.BaseMutationRequest{Mode: "create", Name: "work", Path: parent})
	if !errors.Is(err, saveErr) {
		t.Fatalf("CompleteSetup() error = %v, want wrapped %v", err, saveErr)
	}
	target := filepath.Join(parent, "work")
	if !reflect.DeepEqual(runtime.transactionCalls, []string{target}) {
		t.Errorf("switchBaseTransaction calls = %v, want [%q]", runtime.transactionCalls, target)
	}
	assertDirectory(t, target)
	assertDirectory(t, oldPath)
	assertSettingsUnchanged(t, service, store, runtime, original, oldPath, 1)
}

func TestSettingsServiceCompleteSetupRetainsOldRuntimeWhenIndexRollbackFails(t *testing.T) {
	parent := t.TempDir()
	service, store, runtime, original := newIncompleteSettingsService(t)
	oldPath := runtime.path
	saveErr := errors.New("disk full")
	rollbackErr := errors.New("cannot restore metadata")
	store.saveErr = saveErr
	runtime.switchErrs = []error{nil, rollbackErr}

	_, err := service.CompleteSetup(model.BaseMutationRequest{Mode: "create", Name: "work", Path: parent})
	if !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("CompleteSetup() error = %v, want ErrRollbackFailed", err)
	}
	if !strings.Contains(err.Error(), saveErr.Error()) || !strings.Contains(err.Error(), rollbackErr.Error()) {
		t.Errorf("CompleteSetup() error = %q, want save and rollback details", err)
	}
	target := filepath.Join(parent, "work")
	if runtime.path != oldPath {
		t.Errorf("runtime path = %q, want retained old path %q", runtime.path, oldPath)
	}
	assertDirectory(t, target)
	assertDirectory(t, oldPath)
	if !reflect.DeepEqual(service.GetConfig(), original) || !reflect.DeepEqual(*store.config, original) {
		t.Errorf("config changed after rollback failure: service %#v store %#v want %#v", service.GetConfig(), *store.config, original)
	}
}

func TestSettingsServiceCompleteSetupRollbackFailureBlocksLaterMutation(t *testing.T) {
	parent := t.TempDir()
	service, store, runtime, original := newIncompleteSettingsService(t)
	store.saveErr = errors.New("disk full")
	runtime.switchErrs = []error{nil, errors.New("cannot restore metadata")}

	_, firstErr := service.CompleteSetup(model.BaseMutationRequest{Mode: "create", Name: "work", Path: parent})
	if !errors.Is(firstErr, ErrRollbackFailed) {
		t.Fatalf("first CompleteSetup() error = %v, want ErrRollbackFailed", firstErr)
	}
	saveCalls := store.saveCalls
	transactionCalls := append([]string(nil), runtime.transactionCalls...)
	pathCalls := runtime.pathCalls
	laterPath := t.TempDir()

	_, err := service.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "later", Path: laterPath})
	if !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("AddBase() error = %v, want latched ErrRollbackFailed", err)
	}
	if store.saveCalls != saveCalls || !reflect.DeepEqual(runtime.transactionCalls, transactionCalls) || runtime.pathCalls != pathCalls {
		t.Errorf("degraded mutation made calls: saves %d/%d transactions %v/%v paths %d/%d", store.saveCalls, saveCalls, runtime.transactionCalls, transactionCalls, runtime.pathCalls, pathCalls)
	}
	if !reflect.DeepEqual(service.GetConfig(), original) {
		t.Errorf("service config = %#v, want unchanged %#v", service.GetConfig(), original)
	}
}

func TestSettingsServiceCompleteSetupFromEmptyRuntimeRollsBackAndPreservesCreatedBase(t *testing.T) {
	completed := false
	original := model.Config{SetupCompleted: &completed}
	store := &fakeConfigStore{config: &original, saveErr: errors.New("disk full")}
	runtime := &fakeBaseRuntime{}
	service, err := NewSettingsService(store, runtime, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	parent := t.TempDir()

	_, err = service.CompleteSetup(model.BaseMutationRequest{Mode: "create", Name: "work", Path: parent})
	if !errors.Is(err, store.saveErr) {
		t.Fatalf("CompleteSetup() error = %v, want wrapped %v", err, store.saveErr)
	}
	if errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("CompleteSetup() error = %v, do not want ErrRollbackFailed", err)
	}
	if !reflect.DeepEqual(runtime.transactionCalls, []string{filepath.Join(parent, "work")}) {
		t.Errorf("switchBaseTransaction calls = %v, want target only", runtime.transactionCalls)
	}
	if runtime.path != "" {
		t.Errorf("runtime path = %q, want empty", runtime.path)
	}
	assertDirectory(t, filepath.Join(parent, "work"))
	if !reflect.DeepEqual(service.GetConfig(), original) || !reflect.DeepEqual(*store.config, original) {
		t.Errorf("config changed: service %#v store %#v want %#v", service.GetConfig(), *store.config, original)
	}
}

func TestSettingsServiceCreateUsesCanonicalSymlinkParent(t *testing.T) {
	physicalParent := t.TempDir()
	alternateParent := t.TempDir()
	link := filepath.Join(t.TempDir(), "bases")
	createSymlinkOrSkip(t, physicalParent, link)
	service, store, _, original := newIncompleteSettingsService(t)

	response, err := service.AddBase(model.BaseMutationRequest{Mode: "create", Name: "work", Path: link})
	if err != nil {
		t.Fatalf("AddBase() error = %v", err)
	}
	wantPath := filepath.Join(physicalParent, "work")
	if got := response.Config.Bases[len(original.Bases)].Path; got != wantPath {
		t.Errorf("created base path = %q, want canonical %q", got, wantPath)
	}
	retargetSymlink(t, link, alternateParent)
	if got := store.config.Bases[len(original.Bases)].Path; got != wantPath {
		t.Errorf("stored base path after retarget = %q, want stable %q", got, wantPath)
	}
	assertDirectory(t, wantPath)
}

func TestSettingsServiceConnectUsesCanonicalSymlinkBase(t *testing.T) {
	physicalBase := t.TempDir()
	alternateBase := t.TempDir()
	link := filepath.Join(t.TempDir(), "base")
	createSymlinkOrSkip(t, physicalBase, link)
	service, store, _, original := newIncompleteSettingsService(t)

	response, err := service.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "shared", Path: link})
	if err != nil {
		t.Fatalf("AddBase() error = %v", err)
	}
	if got := response.Config.Bases[len(original.Bases)].Path; got != physicalBase {
		t.Errorf("connected base path = %q, want canonical %q", got, physicalBase)
	}
	retargetSymlink(t, link, alternateBase)
	if got := store.config.Bases[len(original.Bases)].Path; got != physicalBase {
		t.Errorf("stored base path after retarget = %q, want stable %q", got, physicalBase)
	}
}

func TestSettingsServiceAddBaseCreatesAndAppendsWithoutSwitching(t *testing.T) {
	parent := t.TempDir()
	service, store, runtime, original := newIncompleteSettingsService(t)
	oldPath := runtime.path

	response, err := service.AddBase(model.BaseMutationRequest{
		Mode: "create",
		Name: "  work  ",
		Path: "  " + parent + "  ",
	})
	if err != nil {
		t.Fatalf("AddBase() error = %v", err)
	}
	target := filepath.Join(parent, "work")
	assertDirectory(t, target)
	want := cloneConfig(original)
	want.Bases = append(want.Bases, model.Base{Name: "work", Path: target})
	if !reflect.DeepEqual(service.GetConfig(), want) || !reflect.DeepEqual(*store.config, want) {
		t.Errorf("saved service/config = %#v / %#v, want %#v", service.GetConfig(), *store.config, want)
	}
	if !reflect.DeepEqual(response.Config, want) {
		t.Errorf("response config = %#v, want %#v", response.Config, want)
	}
	if response.BasePath != oldPath || runtime.path != oldPath {
		t.Errorf("active paths = response %q, runtime %q; want %q", response.BasePath, runtime.path, oldPath)
	}
	if len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 {
		t.Errorf("runtime switch calls = %v / %v, want none", runtime.switchCalls, runtime.transactionCalls)
	}
	if store.saveCalls != 1 {
		t.Errorf("Save calls = %d, want 1", store.saveCalls)
	}

	response.Config.Bases[0].Name = "changed"
	*response.Config.SetupCompleted = true
	if !reflect.DeepEqual(service.GetConfig(), want) {
		t.Errorf("response aliases service config: got %#v, want %#v", service.GetConfig(), want)
	}
}

func TestSettingsServiceRuntimeHealthErrorDoesNotLatchDegradedState(t *testing.T) {
	service, store, runtime, original := newIncompleteSettingsService(t)
	healthCause := errors.New("runtime index state is uncertain")
	healthErr := errors.Join(ErrRollbackFailed, healthCause)
	runtime.persistErr = healthErr
	request := model.BaseMutationRequest{Mode: "connect", Name: "later", Path: t.TempDir()}

	if _, err := service.AddBase(request); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, healthCause) {
		t.Fatalf("first AddBase() error = %v, want runtime health error", err)
	}
	if store.saveCalls != 0 || !reflect.DeepEqual(service.GetConfig(), original) {
		t.Errorf("failed health check changed state: saves %d config %#v", store.saveCalls, service.GetConfig())
	}
	runtime.persistErr = nil
	if _, err := service.AddBase(request); err != nil {
		t.Fatalf("second AddBase() error = %v", err)
	}
	if runtime.persistCalls != 2 || store.saveCalls != 1 {
		t.Errorf("persistence calls = runtime %d store %d, want 2 and 1", runtime.persistCalls, store.saveCalls)
	}
}

func TestSettingsServiceAddBaseConnectsAndAppendsWithoutSwitching(t *testing.T) {
	connectedPath := t.TempDir()
	filePath := filepath.Join(connectedPath, "existing.md")
	if err := os.WriteFile(filePath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	service, store, runtime, original := newIncompleteSettingsService(t)
	oldPath := runtime.path

	response, err := service.AddBase(model.BaseMutationRequest{
		Mode: "connect",
		Name: "  shared/label  ",
		Path: "  " + connectedPath + "  ",
	})
	if err != nil {
		t.Fatalf("AddBase() error = %v", err)
	}
	absPath, err := filepath.Abs(connectedPath)
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	want := cloneConfig(original)
	want.Bases = append(want.Bases, model.Base{Name: "shared/label", Path: absPath})
	if !reflect.DeepEqual(response.Config, want) || !reflect.DeepEqual(*store.config, want) {
		t.Errorf("saved response/config = %#v / %#v, want %#v", response.Config, *store.config, want)
	}
	if response.BasePath != oldPath || runtime.path != oldPath || len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 {
		t.Errorf("runtime changed: response %q runtime %q switches %v transactions %v", response.BasePath, runtime.path, runtime.switchCalls, runtime.transactionCalls)
	}
	contents, err := os.ReadFile(filePath)
	if err != nil || string(contents) != "keep" {
		t.Errorf("connected file = %q, %v; want preserved keep", contents, err)
	}
}

func TestSettingsServiceAddBaseUsesCaseSensitiveNames(t *testing.T) {
	service, _, runtime, _ := newIncompleteSettingsService(t)
	firstPath := t.TempDir()
	secondPath := t.TempDir()

	if _, err := service.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "work", Path: firstPath}); err != nil {
		t.Fatalf("AddBase(work) error = %v", err)
	}
	if _, err := service.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "Work", Path: secondPath}); err != nil {
		t.Fatalf("AddBase(Work) error = %v", err)
	}
	bases := service.GetConfig().Bases
	if bases[len(bases)-2].Name != "work" || bases[len(bases)-1].Name != "Work" {
		t.Errorf("added names = %q, %q; want work, Work", bases[len(bases)-2].Name, bases[len(bases)-1].Name)
	}
	if len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 {
		t.Errorf("runtime switch calls = %v / %v, want none", runtime.switchCalls, runtime.transactionCalls)
	}
}

func TestSettingsServiceAddBaseRejectsExactDuplicateNameWithoutMutation(t *testing.T) {
	service, store, runtime, original := newIncompleteSettingsService(t)
	oldPath := runtime.path

	_, err := service.AddBase(model.BaseMutationRequest{Mode: "connect", Name: " default ", Path: t.TempDir()})
	if !errors.Is(err, ErrBaseNameConflict) {
		t.Fatalf("AddBase() error = %v, want ErrBaseNameConflict", err)
	}
	assertFieldError(t, err, "name")
	assertSettingsUnchanged(t, service, store, runtime, original, oldPath, 0)
}

func TestSettingsServiceAddBasePreservesCreatedBaseAndDataAfterSaveFailure(t *testing.T) {
	parent := t.TempDir()
	service, store, runtime, original := newIncompleteSettingsService(t)
	oldPath := runtime.path
	saveErr := errors.New("disk full")
	started := make(chan struct{})
	release := make(chan struct{})
	store.saveErr = saveErr
	store.saveStarted = started
	store.saveRelease = release
	done := make(chan error, 1)
	go func() {
		_, err := service.AddBase(model.BaseMutationRequest{Mode: "create", Name: "work", Path: parent})
		done <- err
	}()
	<-started
	target := filepath.Join(parent, "work")
	markerPath := filepath.Join(target, "marker")
	if err := os.WriteFile(markerPath, []byte("user data"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	close(release)

	err := <-done
	if !errors.Is(err, saveErr) {
		t.Fatalf("AddBase() error = %v, want wrapped %v", err, saveErr)
	}
	if strings.Contains(err.Error(), "cleanup") {
		t.Errorf("AddBase() error = %q, do not want cleanup error", err)
	}
	contents, readErr := os.ReadFile(markerPath)
	if readErr != nil || string(contents) != "user data" {
		t.Errorf("created base marker = %q, %v; want preserved user data", contents, readErr)
	}
	assertDirectory(t, oldPath)
	assertSettingsUnchanged(t, service, store, runtime, original, oldPath, 1)
	if len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 {
		t.Errorf("runtime switch calls = %v / %v, want none", runtime.switchCalls, runtime.transactionCalls)
	}
}

func TestSettingsServiceAddBasePreservesConnectedDirectoryAfterSaveFailure(t *testing.T) {
	connectedPath := t.TempDir()
	filePath := filepath.Join(connectedPath, "existing.md")
	if err := os.WriteFile(filePath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	service, store, runtime, original := newIncompleteSettingsService(t)
	oldPath := runtime.path
	saveErr := errors.New("disk full")
	store.saveErr = saveErr

	_, err := service.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "shared", Path: connectedPath})
	if !errors.Is(err, saveErr) {
		t.Fatalf("AddBase() error = %v, want wrapped %v", err, saveErr)
	}
	assertDirectory(t, connectedPath)
	contents, readErr := os.ReadFile(filePath)
	if readErr != nil || string(contents) != "keep" {
		t.Errorf("connected file = %q, %v; want preserved keep", contents, readErr)
	}
	assertSettingsUnchanged(t, service, store, runtime, original, oldPath, 1)
	if len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 {
		t.Errorf("runtime switch calls = %v / %v, want none", runtime.switchCalls, runtime.transactionCalls)
	}
}

func TestSettingsServiceAddBasePreservesCanonicalCreatedBaseAcrossParentSymlinkRetarget(t *testing.T) {
	physicalParent := t.TempDir()
	alternateParent := t.TempDir()
	link := filepath.Join(t.TempDir(), "bases")
	createSymlinkOrSkip(t, physicalParent, link)
	service, store, _, _ := newIncompleteSettingsService(t)
	started := make(chan struct{})
	release := make(chan struct{})
	store.saveStarted = started
	store.saveRelease = release
	store.saveErr = errors.New("disk full")
	done := make(chan error, 1)
	go func() {
		_, err := service.AddBase(model.BaseMutationRequest{Mode: "create", Name: "work", Path: link})
		done <- err
	}()
	<-started
	retargetSymlink(t, link, alternateParent)
	alternateTarget := filepath.Join(alternateParent, "work")
	if err := os.Mkdir(alternateTarget, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(alternateTarget, "marker"), []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	close(release)
	err := <-done
	if !errors.Is(err, store.saveErr) {
		t.Fatalf("AddBase() error = %v, want wrapped %v", err, store.saveErr)
	}
	assertDirectory(t, filepath.Join(physicalParent, "work"))
	contents, readErr := os.ReadFile(filepath.Join(alternateTarget, "marker"))
	if readErr != nil || string(contents) != "keep" {
		t.Errorf("alternate replacement = %q, %v; want preserved", contents, readErr)
	}
}

func TestSettingsServiceUpdateBaseMutatesConfiguredBase(t *testing.T) {
	activePath := t.TempDir()
	inactivePath := t.TempDir()
	replacementPath := t.TempDir()
	marker := filepath.Join(inactivePath, "keep.md")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	service, store, runtime, original := newConfiguredSettingsService(t, activePath, inactivePath)

	response, err := service.UpdateBase("other", model.BaseUpdateRequest{Name: "  renamed  ", Path: "  " + replacementPath + "  "})
	if err != nil {
		t.Fatalf("UpdateBase() error = %v", err)
	}
	want := cloneConfig(original)
	want.Bases[1].Name = "renamed"
	want.Bases[1].Path = replacementPath
	if !reflect.DeepEqual(service.GetConfig(), want) || !reflect.DeepEqual(*store.config, want) || !reflect.DeepEqual(response.Config, want) {
		t.Errorf("configs = service %#v store %#v response %#v, want %#v", service.GetConfig(), *store.config, response.Config, want)
	}
	if response.BasePath != activePath || runtime.path != activePath || len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 {
		t.Errorf("runtime changed: response %q runtime %q switches %v transactions %v", response.BasePath, runtime.path, runtime.switchCalls, runtime.transactionCalls)
	}
	contents, readErr := os.ReadFile(marker)
	if readErr != nil || string(contents) != "keep" {
		t.Errorf("old base marker = %q, %v; want preserved", contents, readErr)
	}
	response.Config.Bases[1].Name = "caller mutation"
	*response.Config.SetupCompleted = false
	if !reflect.DeepEqual(service.GetConfig(), want) {
		t.Errorf("response aliases service config: got %#v, want %#v", service.GetConfig(), want)
	}
}

func TestSettingsServiceUpdateBaseRenamesActiveWithoutSwitchingSameCanonicalPath(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	link := filepath.Join(t.TempDir(), "active-link")
	createSymlinkOrSkip(t, activePath, link)
	service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)

	response, err := service.UpdateBase("active", model.BaseUpdateRequest{Name: "renamed", Path: link})
	if err != nil {
		t.Fatalf("UpdateBase() error = %v", err)
	}
	want := cloneConfig(original)
	want.Bases[0].Name = "renamed"
	want.Bases[0].Path = activePath
	want.CurrentBase = "renamed"
	if !reflect.DeepEqual(response.Config, want) || !reflect.DeepEqual(*store.config, want) {
		t.Errorf("saved response/config = %#v / %#v, want %#v", response.Config, *store.config, want)
	}
	if len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 || response.BasePath != activePath {
		t.Errorf("runtime switches/transactions/path = %v / %v / %q, want none / %q", runtime.switchCalls, runtime.transactionCalls, response.BasePath, activePath)
	}
}

func TestSettingsServiceUpdateBaseSwitchesActivePathTransactionally(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	targetPath := t.TempDir()
	service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)

	response, err := service.UpdateBase("active", model.BaseUpdateRequest{Name: "active", Path: targetPath})
	if err != nil {
		t.Fatalf("UpdateBase() error = %v", err)
	}
	want := cloneConfig(original)
	want.Bases[0].Path = targetPath
	if !reflect.DeepEqual(response.Config, want) || response.BasePath != targetPath {
		t.Errorf("response = %#v path %q, want %#v path %q", response.Config, response.BasePath, want, targetPath)
	}
	if !reflect.DeepEqual(runtime.transactionCalls, []string{targetPath}) || store.saveCalls != 1 {
		t.Errorf("calls = transactions %v saves %d, want [%q] / 1", runtime.transactionCalls, store.saveCalls, targetPath)
	}
}

func TestSettingsServiceUpdateBaseRejectsInvalidRequestsWithoutMutation(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	regularFile := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(regularFile, []byte("notes"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	tests := []struct {
		name    string
		oldName string
		request model.BaseUpdateRequest
		kind    error
		field   string
		cause   error
	}{
		{name: "missing base", oldName: "missing", request: model.BaseUpdateRequest{Name: "new", Path: otherPath}, kind: ErrBaseNotFound},
		{name: "empty name", oldName: "other", request: model.BaseUpdateRequest{Name: " \t", Path: otherPath}, kind: ErrInvalidName, field: "name"},
		{name: "exact duplicate", oldName: "other", request: model.BaseUpdateRequest{Name: " active ", Path: otherPath}, kind: ErrBaseNameConflict, field: "name"},
		{name: "missing path", oldName: "other", request: model.BaseUpdateRequest{Name: "other", Path: missing}, kind: ErrInvalidPath, field: "path", cause: os.ErrNotExist},
		{name: "regular file", oldName: "other", request: model.BaseUpdateRequest{Name: "other", Path: regularFile}, kind: ErrInvalidPath, field: "path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
			_, err := service.UpdateBase(tt.oldName, tt.request)
			if !errors.Is(err, tt.kind) {
				t.Fatalf("UpdateBase() error = %v, want %v", err, tt.kind)
			}
			if tt.field != "" {
				assertFieldError(t, err, tt.field)
			}
			if tt.cause != nil && !errors.Is(err, tt.cause) {
				t.Errorf("UpdateBase() error = %v, want underlying %v", err, tt.cause)
			}
			assertSettingsUnchanged(t, service, store, runtime, original, activePath, 0)
		})
	}
}

func TestSettingsServiceUpdateBaseUsesCaseSensitiveNames(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	service, _, runtime, _ := newConfiguredSettingsService(t, activePath, otherPath)

	response, err := service.UpdateBase("other", model.BaseUpdateRequest{Name: "Active", Path: otherPath})
	if err != nil {
		t.Fatalf("UpdateBase() error = %v", err)
	}
	if got := response.Config.Bases[1].Name; got != "Active" {
		t.Errorf("updated name = %q, want Active", got)
	}
	if len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 {
		t.Errorf("runtime switch calls = %v / %v, want none", runtime.switchCalls, runtime.transactionCalls)
	}
}

func TestSettingsServiceUpdateBasePreservesStateOnSwitchAndSaveFailures(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	targetPath := t.TempDir()
	t.Run("switch failure", func(t *testing.T) {
		service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
		switchErr := errors.New("cannot index target")
		runtime.switchErr = switchErr
		_, err := service.UpdateBase("active", model.BaseUpdateRequest{Name: "renamed", Path: targetPath})
		if !errors.Is(err, switchErr) {
			t.Fatalf("UpdateBase() error = %v, want %v", err, switchErr)
		}
		assertSettingsUnchanged(t, service, store, runtime, original, activePath, 0)
		if !reflect.DeepEqual(runtime.transactionCalls, []string{targetPath}) {
			t.Errorf("switchBaseTransaction calls = %v, want [%q]", runtime.transactionCalls, targetPath)
		}
	})
	t.Run("save failure rolls back", func(t *testing.T) {
		service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
		events := &orderedEvents{}
		store.events = events
		runtime.events = events
		saveErr := errors.New("disk full")
		store.saveErr = saveErr
		_, err := service.UpdateBase("active", model.BaseUpdateRequest{Name: "renamed", Path: targetPath})
		if !errors.Is(err, saveErr) {
			t.Fatalf("UpdateBase() error = %v, want %v", err, saveErr)
		}
		if !reflect.DeepEqual(runtime.transactionCalls, []string{targetPath}) {
			t.Errorf("switchBaseTransaction calls = %v, want target only", runtime.transactionCalls)
		}
		wantEvents := []string{"prepare:" + targetPath, "save", "rollback:" + targetPath}
		if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
			t.Errorf("events = %v, want %v", got, wantEvents)
		}
		assertSettingsUnchanged(t, service, store, runtime, original, activePath, 1)
	})
}

func TestSettingsServiceUpdateBaseCanonicalizesReverseRuntimeSymlinkAlias(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	activeLink := filepath.Join(t.TempDir(), "active-link")
	createSymlinkOrSkip(t, activePath, activeLink)
	service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
	runtime.path = activeLink

	response, err := service.UpdateBase("active", model.BaseUpdateRequest{Name: "renamed", Path: activePath})
	if err != nil {
		t.Fatalf("UpdateBase() error = %v", err)
	}
	want := cloneConfig(original)
	want.Bases[0].Name = "renamed"
	want.CurrentBase = "renamed"
	if !reflect.DeepEqual(response.Config, want) || !reflect.DeepEqual(*store.config, want) {
		t.Errorf("saved response/config = %#v / %#v, want %#v", response.Config, *store.config, want)
	}
	if !reflect.DeepEqual(runtime.transactionCalls, []string{activePath}) || runtime.path != activePath {
		t.Errorf("transaction calls/path = %v / %q, want [%q] / %q", runtime.transactionCalls, runtime.path, activePath, activePath)
	}
}

func TestSettingsServiceUpdateBaseRollbackRetainsOriginalRuntimePath(t *testing.T) {
	oldPath := t.TempDir()
	otherPath := t.TempDir()
	targetPath := t.TempDir()
	retargetPath := t.TempDir()
	oldLink := filepath.Join(t.TempDir(), "old-link")
	createSymlinkOrSkip(t, oldPath, oldLink)
	service, store, runtime, original := newConfiguredSettingsService(t, oldPath, otherPath)
	runtime.path = oldLink
	store.saveErr = errors.New("disk full")
	store.saveStarted = make(chan struct{})
	release := make(chan struct{})
	store.saveRelease = release
	events := &orderedEvents{}
	store.events = events
	runtime.events = events
	done := make(chan error, 1)
	go func() {
		_, err := service.UpdateBase("active", model.BaseUpdateRequest{Name: "active", Path: targetPath})
		done <- err
	}()
	<-store.saveStarted
	retargetSymlink(t, oldLink, retargetPath)
	close(release)

	err := <-done
	if !errors.Is(err, store.saveErr) {
		t.Fatalf("UpdateBase() error = %v, want %v", err, store.saveErr)
	}
	wantEvents := []string{"prepare:" + targetPath, "save", "rollback:" + targetPath}
	if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Errorf("events = %v, want %v", got, wantEvents)
	}
	if runtime.path != oldLink {
		t.Errorf("runtime path = %q, want retained original path %q", runtime.path, oldLink)
	}
	if runtime.path == retargetPath {
		t.Errorf("runtime rolled back through retargeted symlink to %q", retargetPath)
	}
	if !reflect.DeepEqual(service.GetConfig(), original) || !reflect.DeepEqual(*store.config, original) {
		t.Errorf("config changed: service %#v store %#v want %#v", service.GetConfig(), *store.config, original)
	}
}

func TestSettingsServiceSwitchBaseRejectsRuntimePathResolutionFailures(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	t.Run("current runtime", func(t *testing.T) {
		service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
		runtime.path = filepath.Join(t.TempDir(), "missing-current")
		runtime.persistErr = os.ErrNotExist
		_, err := service.SwitchBase("other")
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("SwitchBase() error = %v, want underlying os.ErrNotExist", err)
		}
		assertSettingsUnchanged(t, service, store, runtime, original, runtime.path, 0)
		if len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 {
			t.Errorf("runtime switch calls = %v / %v, want none", runtime.switchCalls, runtime.transactionCalls)
		}
	})
	t.Run("target runtime", func(t *testing.T) {
		missingTarget := filepath.Join(t.TempDir(), "missing-target")
		completed := true
		config := model.Config{
			Bases:          []model.Base{{Name: "active", Path: activePath}, {Name: "other", Path: missingTarget}},
			CurrentBase:    "active",
			SetupCompleted: &completed,
		}
		store := &fakeConfigStore{config: &config}
		runtime := &fakeBaseRuntime{path: activePath}
		service, err := NewSettingsService(store, runtime, "", nil)
		if err != nil {
			t.Fatalf("NewSettingsService() error = %v", err)
		}
		_, err = service.SwitchBase("other")
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("SwitchBase() error = %v, want underlying os.ErrNotExist", err)
		}
		assertSettingsUnchanged(t, service, store, runtime, config, activePath, 0)
		if len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 {
			t.Errorf("runtime switch calls = %v / %v, want none", runtime.switchCalls, runtime.transactionCalls)
		}
	})
}

func TestSettingsServiceForgetBaseValidatesAndPreservesFiles(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	marker := filepath.Join(otherPath, "keep.md")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
	response, err := service.ForgetBase("other")
	if err != nil {
		t.Fatalf("ForgetBase() error = %v", err)
	}
	want := cloneConfig(original)
	want.Bases = want.Bases[:1]
	if !reflect.DeepEqual(response.Config, want) || !reflect.DeepEqual(*store.config, want) {
		t.Errorf("saved response/config = %#v / %#v, want %#v", response.Config, *store.config, want)
	}
	if len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 || response.BasePath != activePath {
		t.Errorf("runtime changed: switches %v transactions %v path %q", runtime.switchCalls, runtime.transactionCalls, response.BasePath)
	}
	contents, readErr := os.ReadFile(marker)
	if readErr != nil || string(contents) != "keep" {
		t.Errorf("forgotten base marker = %q, %v; want preserved", contents, readErr)
	}

	t.Run("save failure preserves config and files", func(t *testing.T) {
		svc, saved, rt, config := newConfiguredSettingsService(t, activePath, otherPath)
		saved.saveErr = errors.New("disk full")
		_, err := svc.ForgetBase("other")
		if !errors.Is(err, saved.saveErr) {
			t.Fatalf("ForgetBase() error = %v, want %v", err, saved.saveErr)
		}
		assertSettingsUnchanged(t, svc, saved, rt, config, activePath, 1)
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("Stat(marker) error = %v, want preserved file", err)
		}
	})

	for _, tc := range []struct {
		name string
		base string
		kind error
	}{
		{name: "missing", base: "missing", kind: ErrBaseNotFound},
		{name: "active", base: "active", kind: ErrActiveBase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, saved, rt, config := newConfiguredSettingsService(t, activePath, otherPath)
			_, err := svc.ForgetBase(tc.base)
			if !errors.Is(err, tc.kind) {
				t.Fatalf("ForgetBase() error = %v, want %v", err, tc.kind)
			}
			assertSettingsUnchanged(t, svc, saved, rt, config, activePath, 0)
		})
	}

	t.Run("last", func(t *testing.T) {
		completed := true
		config := model.Config{Bases: []model.Base{{Name: "active", Path: activePath}}, CurrentBase: "active", SetupCompleted: &completed}
		store := &fakeConfigStore{config: &config}
		runtime := &fakeBaseRuntime{path: activePath}
		svc, err := NewSettingsService(store, runtime, "", nil)
		if err != nil {
			t.Fatalf("NewSettingsService() error = %v", err)
		}
		_, err = svc.ForgetBase("active")
		if !errors.Is(err, ErrLastBase) {
			t.Fatalf("ForgetBase() error = %v, want ErrLastBase", err)
		}
		assertSettingsUnchanged(t, svc, store, runtime, config, activePath, 0)
	})
}

func TestSettingsServiceSwitchBaseIsTransactional(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
	response, err := service.SwitchBase("other")
	if err != nil {
		t.Fatalf("SwitchBase() error = %v", err)
	}
	want := cloneConfig(original)
	want.CurrentBase = "other"
	if !reflect.DeepEqual(response.Config, want) || response.BasePath != otherPath || !reflect.DeepEqual(*store.config, want) {
		t.Errorf("response/store = %#v path %q / %#v, want %#v path %q", response.Config, response.BasePath, *store.config, want, otherPath)
	}
	if !reflect.DeepEqual(runtime.transactionCalls, []string{otherPath}) {
		t.Errorf("switchBaseTransaction calls = %v, want [%q]", runtime.transactionCalls, otherPath)
	}
}

func TestSettingsServiceSwitchBaseRejectsMissingAndPreservesFailures(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	t.Run("missing", func(t *testing.T) {
		service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
		_, err := service.SwitchBase("missing")
		if !errors.Is(err, ErrBaseNotFound) {
			t.Fatalf("SwitchBase() error = %v, want ErrBaseNotFound", err)
		}
		assertSettingsUnchanged(t, service, store, runtime, original, activePath, 0)
	})
	t.Run("runtime failure does not save", func(t *testing.T) {
		service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
		runtimeErr := errors.New("cannot index")
		runtime.switchErr = runtimeErr
		_, err := service.SwitchBase("other")
		if !errors.Is(err, runtimeErr) {
			t.Fatalf("SwitchBase() error = %v, want %v", err, runtimeErr)
		}
		assertSettingsUnchanged(t, service, store, runtime, original, activePath, 0)
	})
	t.Run("save failure rolls runtime back", func(t *testing.T) {
		service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
		events := &orderedEvents{}
		store.events = events
		runtime.events = events
		saveErr := errors.New("disk full")
		store.saveErr = saveErr
		_, err := service.SwitchBase("other")
		if !errors.Is(err, saveErr) {
			t.Fatalf("SwitchBase() error = %v, want %v", err, saveErr)
		}
		if !reflect.DeepEqual(runtime.transactionCalls, []string{otherPath}) {
			t.Errorf("switchBaseTransaction calls = %v, want target only", runtime.transactionCalls)
		}
		wantEvents := []string{"prepare:" + otherPath, "save", "rollback:" + otherPath}
		if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
			t.Errorf("events = %v, want %v", got, wantEvents)
		}
		assertSettingsUnchanged(t, service, store, runtime, original, activePath, 1)
	})
}

func TestSettingsServiceRollbackFailureDegradesAllCRUDMutations(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	targetPath := t.TempDir()
	service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
	store.saveErr = errors.New("disk full")
	runtime.switchErrs = []error{nil, errors.New("rollback failed")}
	_, firstErr := service.SwitchBase("other")
	if !errors.Is(firstErr, ErrRollbackFailed) {
		t.Fatalf("SwitchBase() error = %v, want ErrRollbackFailed", firstErr)
	}
	saves := store.saveCalls
	transactions := append([]string(nil), runtime.transactionCalls...)
	pathCalls := runtime.pathCalls
	mutations := []struct {
		name string
		call func() error
	}{
		{name: "update", call: func() error {
			_, err := service.UpdateBase("active", model.BaseUpdateRequest{Name: "active", Path: targetPath})
			return err
		}},
		{name: "forget", call: func() error { _, err := service.ForgetBase("other"); return err }},
		{name: "switch", call: func() error { _, err := service.SwitchBase("active"); return err }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if err := mutation.call(); !errors.Is(err, ErrRollbackFailed) {
				t.Fatalf("mutation error = %v, want degraded ErrRollbackFailed", err)
			}
		})
	}
	if store.saveCalls != saves || !reflect.DeepEqual(runtime.transactionCalls, transactions) || runtime.pathCalls != pathCalls {
		t.Errorf("degraded mutations made calls: saves %d/%d transactions %v/%v paths %d/%d", store.saveCalls, saves, runtime.transactionCalls, transactions, runtime.pathCalls, pathCalls)
	}
	if !reflect.DeepEqual(service.GetConfig(), original) {
		t.Errorf("service config = %#v, want unchanged %#v", service.GetConfig(), original)
	}
}

func TestNormalizeConfigReturnsIndependentNormalizedSnapshot(t *testing.T) {
	firstPath := t.TempDir()
	secondPath := t.TempDir()
	baseDirInput := filepath.Join(t.TempDir(), "missing", "..", "bases")
	completed := true
	input := model.Config{
		BaseDir: "  " + baseDirInput + "  ",
		Bases: []model.Base{
			{Name: "  active  ", Path: "  " + firstPath + "  ", GitURL: "active.git", AutoSync: true},
			{Name: "Active", Path: secondPath, GitURL: "other.git"},
		},
		CurrentBase:    "  active  ",
		SetupCompleted: &completed,
	}

	got, err := normalizeConfig(input, false)
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	wantBaseDir, err := filepath.Abs(strings.TrimSpace(baseDirInput))
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	want := model.Config{
		BaseDir: filepath.Clean(wantBaseDir),
		Bases: []model.Base{
			{Name: "active", Path: firstPath, GitURL: "active.git", AutoSync: true},
			{Name: "Active", Path: secondPath, GitURL: "other.git"},
		},
		CurrentBase:    "active",
		SetupCompleted: settingsBoolPointer(true),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeConfig() = %#v, want %#v", got, want)
	}
	input.Bases[0].Name = "caller changed input"
	*input.SetupCompleted = false
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalized config aliases input: got %#v, want %#v", got, want)
	}
	got.Bases[0].Name = "caller changed result"
	*got.SetupCompleted = false
	if input.Bases[0].Name != "caller changed input" || *input.SetupCompleted {
		t.Errorf("normalized result aliases input: input %#v", input)
	}
}

func TestNormalizeConfigPreservesEffectiveSetupWhenOmitted(t *testing.T) {
	basePath := t.TempDir()
	for _, currentSetup := range []bool{false, true} {
		t.Run(fmt.Sprintf("current_%t", currentSetup), func(t *testing.T) {
			got, err := normalizeConfig(model.Config{
				Bases:       []model.Base{{Name: "base", Path: basePath}},
				CurrentBase: "base",
			}, currentSetup)
			if err != nil {
				t.Fatalf("normalizeConfig() error = %v", err)
			}
			if got.SetupCompleted == nil || *got.SetupCompleted != currentSetup {
				t.Errorf("SetupCompleted = %v, want %t", got.SetupCompleted, currentSetup)
			}
		})
	}
}

func TestNormalizeConfigBaseDirAbsFailureIsInvalidPath(t *testing.T) {
	basePath := t.TempDir()
	deadCWD := filepath.Join(t.TempDir(), "removed-cwd")
	if err := os.Mkdir(deadCWD, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	t.Chdir(deadCWD)
	if err := os.Remove(deadCWD); err != nil {
		t.Skipf("cannot remove current directory on this platform: %v", err)
	}
	_, absErr := filepath.Abs("relative-base-dir")
	if absErr == nil {
		t.Skip("filepath.Abs does not fail from a removed current directory on this platform")
	}
	underlying := errors.Unwrap(absErr)
	if underlying == nil {
		underlying = absErr
	}

	_, err := normalizeConfig(model.Config{
		BaseDir:     "relative-base-dir",
		Bases:       []model.Base{{Name: "base", Path: basePath}},
		CurrentBase: "base",
	}, false)
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("normalizeConfig() error = %v, want ErrInvalidPath", err)
	}
	if errors.Is(err, ErrInvalidConfig) {
		t.Errorf("normalizeConfig() error = %v, do not want ErrInvalidConfig", err)
	}
	if !errors.Is(err, underlying) {
		t.Errorf("normalizeConfig() error = %v, want underlying %v", err, underlying)
	}
	assertFieldError(t, err, "base_dir")
}

func TestNormalizeConfigRejectsInvalidConfig(t *testing.T) {
	basePath := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	tests := []struct {
		name         string
		input        model.Config
		currentSetup bool
		kind         error
		field        string
		cause        error
	}{
		{name: "completed setup cannot reopen", input: model.Config{Bases: []model.Base{{Name: "base", Path: basePath}}, CurrentBase: "base", SetupCompleted: settingsBoolPointer(false)}, currentSetup: true, kind: ErrSetupCannotReopen, field: "setup_completed"},
		{name: "empty bases", input: model.Config{CurrentBase: "base"}, kind: ErrInvalidConfig, field: "bases"},
		{name: "empty name", input: model.Config{Bases: []model.Base{{Name: " \t", Path: basePath}}, CurrentBase: "base"}, kind: ErrInvalidName, field: "bases[0].name"},
		{name: "duplicate name", input: model.Config{Bases: []model.Base{{Name: "base", Path: basePath}, {Name: " base ", Path: basePath}}, CurrentBase: "base"}, kind: ErrBaseNameConflict, field: "bases[1].name"},
		{name: "missing base path", input: model.Config{Bases: []model.Base{{Name: "base", Path: missing}}, CurrentBase: "base"}, kind: ErrInvalidPath, field: "bases[0].path", cause: os.ErrNotExist},
		{name: "unknown current base", input: model.Config{Bases: []model.Base{{Name: "base", Path: basePath}}, CurrentBase: " missing "}, kind: ErrBaseNotFound, field: "current_base"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeConfig(tt.input, tt.currentSetup)
			if !errors.Is(err, tt.kind) {
				t.Fatalf("normalizeConfig() error = %v, want %v", err, tt.kind)
			}
			assertFieldError(t, err, tt.field)
			if tt.cause != nil && !errors.Is(err, tt.cause) {
				t.Errorf("normalizeConfig() error = %v, want underlying %v", err, tt.cause)
			}
		})
	}
}

func TestNormalizeConfigCanonicalizesEveryBasePath(t *testing.T) {
	physicalPath := t.TempDir()
	alternatePath := t.TempDir()
	link := filepath.Join(t.TempDir(), "base-link")
	createSymlinkOrSkip(t, physicalPath, link)

	got, err := normalizeConfig(model.Config{
		Bases:       []model.Base{{Name: "base", Path: link}, {Name: "other", Path: alternatePath}},
		CurrentBase: "base",
	}, false)
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	if got.Bases[0].Path != physicalPath || got.Bases[1].Path != alternatePath {
		t.Errorf("base paths = %q, %q; want %q, %q", got.Bases[0].Path, got.Bases[1].Path, physicalPath, alternatePath)
	}
}

func TestSettingsServiceReplaceConfigAppliesNormalizedConfigWithoutUnneededSwitch(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	removedPath := t.TempDir()
	activeLink := filepath.Join(t.TempDir(), "active-link")
	createSymlinkOrSkip(t, activePath, activeLink)
	baseDir := filepath.Join(t.TempDir(), "not-created", "..", "bases")
	marker := filepath.Join(removedPath, "keep.md")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	service, store, runtime, _ := newConfiguredSettingsService(t, activePath, removedPath)
	input := model.Config{
		BaseDir: baseDir,
		Bases: []model.Base{
			{Name: "  renamed  ", Path: activeLink, GitURL: "new.git", AutoSync: true},
			{Name: "other", Path: otherPath},
		},
		CurrentBase: " renamed ",
	}

	response, err := service.ReplaceConfig(input)
	if err != nil {
		t.Fatalf("ReplaceConfig() error = %v", err)
	}
	want, err := normalizeConfig(input, true)
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	if !reflect.DeepEqual(response.Config, want) || !reflect.DeepEqual(*store.config, want) || !reflect.DeepEqual(service.GetConfig(), want) {
		t.Errorf("configs = response %#v store %#v service %#v, want %#v", response.Config, *store.config, service.GetConfig(), want)
	}
	if len(runtime.switchCalls) != 0 || len(runtime.transactionCalls) != 0 || response.BasePath != activePath {
		t.Errorf("runtime changed: switches %v transactions %v path %q", runtime.switchCalls, runtime.transactionCalls, response.BasePath)
	}
	contents, readErr := os.ReadFile(marker)
	if readErr != nil || string(contents) != "keep" {
		t.Errorf("removed config base marker = %q, %v; want preserved", contents, readErr)
	}
	if _, statErr := os.Stat(filepath.Clean(baseDir)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("replacement BaseDir Stat() error = %v, want not created", statErr)
	}
	input.Bases[0].Name = "caller input mutation"
	response.Config.Bases[0].Name = "caller response mutation"
	*response.Config.SetupCompleted = false
	if !reflect.DeepEqual(service.GetConfig(), want) || !reflect.DeepEqual(*store.config, want) {
		t.Errorf("caller memory aliases saved config: service %#v store %#v want %#v", service.GetConfig(), *store.config, want)
	}
}

func TestSettingsServiceReplaceConfigSwitchesChangedActivePath(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	targetPath := t.TempDir()
	service, store, runtime, _ := newConfiguredSettingsService(t, activePath, otherPath)
	input := model.Config{
		Bases:       []model.Base{{Name: "new-active", Path: targetPath}, {Name: "other", Path: otherPath}},
		CurrentBase: "new-active",
	}

	response, err := service.ReplaceConfig(input)
	if err != nil {
		t.Fatalf("ReplaceConfig() error = %v", err)
	}
	if response.BasePath != targetPath || !reflect.DeepEqual(runtime.transactionCalls, []string{targetPath}) || store.saveCalls != 1 {
		t.Errorf("runtime/save = path %q transactions %v saves %d, want %q [%q] 1", response.BasePath, runtime.transactionCalls, store.saveCalls, targetPath, targetPath)
	}
}

func TestSettingsServiceReplaceConfigIsTransactional(t *testing.T) {
	activePath := t.TempDir()
	otherPath := t.TempDir()
	targetPath := t.TempDir()
	input := model.Config{Bases: []model.Base{{Name: "target", Path: targetPath}}, CurrentBase: "target"}
	t.Run("switch failure does not save", func(t *testing.T) {
		service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
		switchErr := errors.New("cannot index")
		runtime.switchErr = switchErr
		_, err := service.ReplaceConfig(input)
		if !errors.Is(err, switchErr) {
			t.Fatalf("ReplaceConfig() error = %v, want %v", err, switchErr)
		}
		assertSettingsUnchanged(t, service, store, runtime, original, activePath, 0)
	})
	t.Run("save failure rolls back target then old", func(t *testing.T) {
		service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
		events := &orderedEvents{}
		store.events = events
		runtime.events = events
		saveErr := errors.New("disk full")
		store.saveErr = saveErr
		_, err := service.ReplaceConfig(input)
		if !errors.Is(err, saveErr) {
			t.Fatalf("ReplaceConfig() error = %v, want %v", err, saveErr)
		}
		if !reflect.DeepEqual(runtime.transactionCalls, []string{targetPath}) {
			t.Errorf("switchBaseTransaction calls = %v, want target only", runtime.transactionCalls)
		}
		wantEvents := []string{"prepare:" + targetPath, "save", "rollback:" + targetPath}
		if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
			t.Errorf("events = %v, want %v", got, wantEvents)
		}
		assertSettingsUnchanged(t, service, store, runtime, original, activePath, 1)
	})
	t.Run("rollback failure degrades service with both causes", func(t *testing.T) {
		service, store, runtime, original := newConfiguredSettingsService(t, activePath, otherPath)
		saveErr := errors.New("disk full")
		rollbackErr := errors.New("cannot restore")
		store.saveErr = saveErr
		runtime.switchErrs = []error{nil, rollbackErr}
		_, err := service.ReplaceConfig(input)
		if !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, saveErr) || !errors.Is(err, rollbackErr) {
			t.Fatalf("ReplaceConfig() error = %v, want rollback, save, and restore causes", err)
		}
		if !reflect.DeepEqual(service.GetConfig(), original) {
			t.Errorf("service config = %#v, want unchanged %#v", service.GetConfig(), original)
		}
		saves := store.saveCalls
		transactions := append([]string(nil), runtime.transactionCalls...)
		pathCalls := runtime.pathCalls
		_, laterErr := service.ReplaceConfig(model.Config{})
		if !errors.Is(laterErr, ErrRollbackFailed) {
			t.Fatalf("later ReplaceConfig() error = %v, want degraded ErrRollbackFailed", laterErr)
		}
		if store.saveCalls != saves || !reflect.DeepEqual(runtime.transactionCalls, transactions) || runtime.pathCalls != pathCalls {
			t.Errorf("degraded replacement made calls: saves %d/%d transactions %v/%v paths %d/%d", store.saveCalls, saves, runtime.transactionCalls, transactions, runtime.pathCalls, pathCalls)
		}
	})
}

func TestSettingsServiceConcurrentTask7MutationsAreSerialized(t *testing.T) {
	activePath := t.TempDir()
	targetPath := t.TempDir()
	editablePath := t.TempDir()
	forgottenPath := t.TempDir()
	completed := true
	original := model.Config{
		Bases: []model.Base{
			{Name: "active", Path: activePath},
			{Name: "target", Path: targetPath},
			{Name: "editable", Path: editablePath},
			{Name: "forgotten", Path: forgottenPath},
		},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &original}
	runtime := &fakeBaseRuntime{path: activePath}
	service, err := NewSettingsService(store, runtime, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	calls := []func() error{
		func() error {
			_, err := service.UpdateBase("editable", model.BaseUpdateRequest{Name: "edited", Path: editablePath})
			return err
		},
		func() error {
			_, err := service.ForgetBase("forgotten")
			return err
		},
		func() error {
			_, err := service.SwitchBase("target")
			return err
		},
	}
	for _, call := range calls {
		call := call
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- call()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent mutation error = %v", err)
		}
	}

	want := cloneConfig(original)
	want.Bases = []model.Base{
		{Name: "active", Path: activePath},
		{Name: "target", Path: targetPath},
		{Name: "edited", Path: editablePath},
	}
	want.CurrentBase = "target"
	if !reflect.DeepEqual(service.GetConfig(), want) || !reflect.DeepEqual(*store.config, want) {
		t.Errorf("configs = service %#v store %#v, want %#v", service.GetConfig(), *store.config, want)
	}
	if runtime.path != targetPath || !reflect.DeepEqual(runtime.transactionCalls, []string{targetPath}) {
		t.Errorf("runtime = path %q transactions %v, want %q [%q]", runtime.path, runtime.transactionCalls, targetPath, targetPath)
	}
	if store.saveCalls != len(calls) {
		t.Errorf("Save calls = %d, want %d", store.saveCalls, len(calls))
	}
}

func newIncompleteSettingsService(t *testing.T) (*SettingsService, *fakeConfigStore, *fakeBaseRuntime, model.Config) {
	t.Helper()
	completed := false
	parent := t.TempDir()
	defaultPath := filepath.Join(parent, "default")
	if err := os.Mkdir(defaultPath, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	config := model.Config{
		BaseDir:        parent,
		Bases:          []model.Base{{Name: "default", Path: defaultPath}},
		CurrentBase:    "default",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config}
	runtime := &fakeBaseRuntime{path: defaultPath}
	service, err := NewSettingsService(store, runtime, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	return service, store, runtime, cloneConfig(config)
}

func newConfiguredSettingsService(t *testing.T, activePath, otherPath string) (*SettingsService, *fakeConfigStore, *fakeBaseRuntime, model.Config) {
	t.Helper()
	completed := true
	config := model.Config{
		BaseDir: filepath.Dir(activePath),
		Bases: []model.Base{
			{Name: "active", Path: activePath, GitURL: "active.git", AutoSync: true},
			{Name: "other", Path: otherPath, GitURL: "other.git"},
		},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config}
	runtime := &fakeBaseRuntime{path: activePath}
	service, err := NewSettingsService(store, runtime, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	return service, store, runtime, cloneConfig(config)
}

func settingsBoolPointer(value bool) *bool {
	return &value
}

func assertFieldError(t *testing.T, err error, field string) {
	t.Helper()
	var fieldErr *FieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("error type = %T, want *FieldError", err)
	}
	if fieldErr.Field != field {
		t.Errorf("FieldError.Field = %q, want %q", fieldErr.Field, field)
	}
}

func assertSettingsUnchanged(t *testing.T, service *SettingsService, store *fakeConfigStore, runtime *fakeBaseRuntime, config model.Config, runtimePath string, saveCalls int) {
	t.Helper()
	if !reflect.DeepEqual(service.GetConfig(), config) {
		t.Errorf("service config = %#v, want unchanged %#v", service.GetConfig(), config)
	}
	if store.config == nil || !reflect.DeepEqual(*store.config, config) {
		t.Errorf("stored config = %#v, want unchanged %#v", store.config, config)
	}
	if runtime.path != runtimePath {
		t.Errorf("runtime path = %q, want unchanged %q", runtime.path, runtimePath)
	}
	if store.saveCalls != saveCalls {
		t.Errorf("Save calls = %d, want %d", store.saveCalls, saveCalls)
	}
}

func assertDirectory(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("Stat(%q).IsDir() = false, want true", path)
	}
}

func createSymlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("symlink creation is not supported: %v", err)
		}
		t.Fatalf("Symlink() error = %v", err)
	}
}

func retargetSymlink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Remove(link); err != nil {
		t.Fatalf("Remove symlink error = %v", err)
	}
	createSymlinkOrSkip(t, target, link)
}
