package service

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	switchErrs  []error
}

func (f *fakeBaseRuntime) GetBasePath() string {
	f.pathCalls++
	return f.path
}

func (f *fakeBaseRuntime) SwitchBase(path string) error {
	f.switchCalls = append(f.switchCalls, path)
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

func TestNewSettingsServiceRejectsCLIBaseForStructurallyEmptyConfig(t *testing.T) {
	store := &fakeConfigStore{config: &model.Config{}}

	service, err := NewSettingsService(store, &fakeBaseRuntime{}, "missing", nil)
	if service != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", service)
	}
	if !errors.Is(err, ErrBaseNotFound) {
		t.Fatalf("NewSettingsService() error = %v, want ErrBaseNotFound", err)
	}
	if store.saveCalls != 1 {
		t.Errorf("Save calls = %d, want 1 setup migration", store.saveCalls)
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
	if !reflect.DeepEqual(runtime.switchCalls, []string{target}) {
		t.Errorf("SwitchBase calls = %v, want [%q]", runtime.switchCalls, target)
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
	if !reflect.DeepEqual(runtime.switchCalls, []string{absPath}) {
		t.Errorf("SwitchBase calls = %v, want [%q]", runtime.switchCalls, absPath)
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
	}{
		{name: "invalid mode", request: model.BaseMutationRequest{Mode: "CREATE", Name: "work", Path: existingParent}, kind: ErrInvalidMode, field: "mode"},
		{name: "mode is not trimmed", request: model.BaseMutationRequest{Mode: " create ", Name: "work", Path: t.TempDir()}, kind: ErrInvalidMode, field: "mode"},
		{name: "whitespace name", request: model.BaseMutationRequest{Mode: "create", Name: " \t", Path: existingParent}, kind: ErrInvalidName, field: "name"},
		{name: "dot name", request: model.BaseMutationRequest{Mode: "create", Name: ".", Path: existingParent}, kind: ErrInvalidName, field: "name"},
		{name: "dot dot name", request: model.BaseMutationRequest{Mode: "create", Name: "..", Path: existingParent}, kind: ErrInvalidName, field: "name"},
		{name: "slash name", request: model.BaseMutationRequest{Mode: "create", Name: "a/b", Path: existingParent}, kind: ErrInvalidName, field: "name"},
		{name: "backslash name", request: model.BaseMutationRequest{Mode: "create", Name: `a\b`, Path: existingParent}, kind: ErrInvalidName, field: "name"},
		{name: "empty path", request: model.BaseMutationRequest{Mode: "create", Name: "work", Path: " \t"}, kind: ErrInvalidPath, field: "path"},
		{name: "missing create parent", request: model.BaseMutationRequest{Mode: "create", Name: "work", Path: missing}, kind: ErrInvalidPath, field: "path"},
		{name: "regular file create parent", request: model.BaseMutationRequest{Mode: "create", Name: "work", Path: regularFile}, kind: ErrInvalidPath, field: "path"},
		{name: "existing create target", request: model.BaseMutationRequest{Mode: "create", Name: "work", Path: existingParent}, kind: ErrBasePathConflict, field: "path"},
		{name: "missing connect base", request: model.BaseMutationRequest{Mode: "connect", Name: "work", Path: missing}, kind: ErrInvalidPath, field: "path"},
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
			assertFieldError(t, err, tt.field)
			assertSettingsUnchanged(t, service, store, runtime, original, oldPath, 0)
		})
	}
}

func TestSettingsServiceCompleteSetupCleansCreatedBaseAfterSwitchFailure(t *testing.T) {
	parent := t.TempDir()
	service, store, runtime, original := newIncompleteSettingsService(t)
	oldPath := runtime.path
	switchErr := errors.New("cannot open metadata")
	runtime.switchErr = switchErr

	_, err := service.CompleteSetup(model.BaseMutationRequest{Mode: "create", Name: "work", Path: parent})
	if !errors.Is(err, switchErr) {
		t.Fatalf("CompleteSetup() error = %v, want wrapped %v", err, switchErr)
	}
	assertNotExist(t, filepath.Join(parent, "work"))
	assertSettingsUnchanged(t, service, store, runtime, original, oldPath, 0)
}

func TestSettingsServiceCompleteSetupRollsBackRuntimeAndCleansCreatedBaseAfterSaveFailure(t *testing.T) {
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
	if !reflect.DeepEqual(runtime.switchCalls, []string{target, oldPath}) {
		t.Errorf("SwitchBase calls = %v, want [%q %q]", runtime.switchCalls, target, oldPath)
	}
	assertNotExist(t, target)
	assertDirectory(t, oldPath)
	assertSettingsUnchanged(t, service, store, runtime, original, oldPath, 1)
}

func TestSettingsServiceCompleteSetupKeepsActiveCreatedBaseWhenRollbackFails(t *testing.T) {
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
	if runtime.path != target {
		t.Errorf("runtime path = %q, want active target %q", runtime.path, target)
	}
	assertDirectory(t, target)
	assertDirectory(t, oldPath)
	if !reflect.DeepEqual(service.GetConfig(), original) || !reflect.DeepEqual(*store.config, original) {
		t.Errorf("config changed after rollback failure: service %#v store %#v want %#v", service.GetConfig(), *store.config, original)
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
	if len(runtime.switchCalls) != 0 {
		t.Errorf("SwitchBase calls = %v, want none", runtime.switchCalls)
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
	if response.BasePath != oldPath || runtime.path != oldPath || len(runtime.switchCalls) != 0 {
		t.Errorf("runtime changed: response %q runtime %q switches %v", response.BasePath, runtime.path, runtime.switchCalls)
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
	if len(runtime.switchCalls) != 0 {
		t.Errorf("SwitchBase calls = %v, want none", runtime.switchCalls)
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

func TestSettingsServiceAddBaseRemovesCreatedBaseAfterSaveFailure(t *testing.T) {
	parent := t.TempDir()
	service, store, runtime, original := newIncompleteSettingsService(t)
	oldPath := runtime.path
	saveErr := errors.New("disk full")
	store.saveErr = saveErr

	_, err := service.AddBase(model.BaseMutationRequest{Mode: "create", Name: "work", Path: parent})
	if !errors.Is(err, saveErr) {
		t.Fatalf("AddBase() error = %v, want wrapped %v", err, saveErr)
	}
	assertNotExist(t, filepath.Join(parent, "work"))
	assertDirectory(t, oldPath)
	assertSettingsUnchanged(t, service, store, runtime, original, oldPath, 1)
	if len(runtime.switchCalls) != 0 {
		t.Errorf("SwitchBase calls = %v, want none", runtime.switchCalls)
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
	if len(runtime.switchCalls) != 0 {
		t.Errorf("SwitchBase calls = %v, want none", runtime.switchCalls)
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

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(%q) error = %v, want not exist", path, err)
	}
}
