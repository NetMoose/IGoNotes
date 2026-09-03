package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

type fakeConfigStore struct {
	config      *model.Config
	loadErr     error
	saveErr     error
	saveErrs    []error
	saveCalls   int
	saveStarted chan struct{}
	saveStart   sync.Once
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
		f.saveStart.Do(func() { close(f.saveStarted) })
	}
	if f.saveRelease != nil {
		<-f.saveRelease
	}
	if len(f.saveErrs) != 0 {
		err := f.saveErrs[0]
		f.saveErrs = f.saveErrs[1:]
		if err != nil {
			return err
		}
	} else if f.saveErr != nil {
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
	persistStarted   chan struct{}
	persistRelease   <-chan struct{}
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
	if f.persistStarted != nil {
		close(f.persistStarted)
	}
	if f.persistRelease != nil {
		<-f.persistRelease
	}
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

func (f *fakeBaseRuntime) switchBaseTransaction(path string, store ConfigStore, next, previous *model.Config) (error, error) {
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
		rollbackErr := commitOutcomeError(f.nextSwitchError())
		if err := store.Save(previous); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore settings: %w", err))
		}
		return fmt.Errorf("commit note index: %w", f.commitErr), rollbackErr
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

type fakeGitConfigValidator struct {
	calls      int
	dir        string
	request    model.GitConfigRequest
	normalized *model.GitConfigRequest
	err        error
}

func (f *fakeGitConfigValidator) Validate(_ context.Context, dir string, request model.GitConfigRequest) (model.GitConfigRequest, error) {
	f.calls++
	f.dir = dir
	f.request = request
	if f.err != nil {
		return model.GitConfigRequest{}, f.err
	}
	if f.normalized != nil {
		return *f.normalized, nil
	}
	return request, nil
}

type fakeGitStatusStore struct {
	statuses    map[string]model.GitStatus
	getCalls    []string
	listCalls   int
	upsertCalls []model.GitStatus
	deleteCalls []string
	getErr      error
	listErr     error
	upsertErrs  []error
	deleteErrs  []error
	upsertHook  func(context.Context, model.GitStatus) error
	deleteHook  func(context.Context, string) error
}

type exactGitStatusStore struct {
	calls int
}

func (s *exactGitStatusStore) Upsert(context.Context, model.GitStatus) error {
	s.calls++
	return errors.New("must not upsert at startup")
}

func (s *exactGitStatusStore) Get(context.Context, string) (model.GitStatus, bool, error) {
	s.calls++
	return model.GitStatus{}, false, errors.New("must not get at startup")
}

func (s *exactGitStatusStore) List(context.Context) ([]model.GitStatus, error) {
	s.calls++
	return nil, errors.New("must not list at startup")
}

func (s *exactGitStatusStore) Delete(context.Context, string) error {
	s.calls++
	return errors.New("must not delete at startup")
}

var _ GitStatusStore = (*exactGitStatusStore)(nil)

func (f *fakeGitStatusStore) Get(_ context.Context, path string) (model.GitStatus, bool, error) {
	f.getCalls = append(f.getCalls, path)
	if f.getErr != nil {
		return model.GitStatus{}, false, f.getErr
	}
	status, ok := f.statuses[path]
	return cloneGitStatusForTest(status), ok, nil
}

func (f *fakeGitStatusStore) List(_ context.Context) ([]model.GitStatus, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	statuses := make([]model.GitStatus, 0, len(f.statuses))
	for _, status := range f.statuses {
		statuses = append(statuses, cloneGitStatusForTest(status))
	}
	return statuses, nil
}

func (f *fakeGitStatusStore) Upsert(ctx context.Context, status model.GitStatus) error {
	f.upsertCalls = append(f.upsertCalls, cloneGitStatusForTest(status))
	if f.upsertHook != nil {
		if err := f.upsertHook(ctx, status); err != nil {
			return err
		}
	}
	if err := popFakeError(&f.upsertErrs); err != nil {
		return err
	}
	if f.statuses == nil {
		f.statuses = make(map[string]model.GitStatus)
	}
	f.statuses[status.RepositoryPath] = cloneGitStatusForTest(status)
	return nil
}

func (f *fakeGitStatusStore) Delete(ctx context.Context, path string) error {
	f.deleteCalls = append(f.deleteCalls, path)
	if f.deleteHook != nil {
		if err := f.deleteHook(ctx, path); err != nil {
			return err
		}
	}
	if err := popFakeError(&f.deleteErrs); err != nil {
		return err
	}
	delete(f.statuses, path)
	return nil
}

func popFakeError(fakeErrors *[]error) error {
	if len(*fakeErrors) == 0 {
		return nil
	}
	err := (*fakeErrors)[0]
	*fakeErrors = (*fakeErrors)[1:]
	return err
}

func cloneGitStatusForTest(status model.GitStatus) model.GitStatus {
	if status.ChangedPaths != nil {
		status.ChangedPaths = append([]string{}, status.ChangedPaths...)
	}
	if status.Error != nil {
		cloned := *status.Error
		status.Error = &cloned
	}
	return status
}

func TestSettingsServiceMutationsWaitAtCoordinatorBeforeSettingsLock(t *testing.T) {
	mutations := []struct {
		name string
		call func(*SettingsService, model.Config, string, string) error
	}{
		{name: "complete setup", call: func(settings *SettingsService, _ model.Config, _, mutationPath string) error {
			_, err := settings.CompleteSetup(model.BaseMutationRequest{Mode: "connect", Name: "setup", Path: mutationPath})
			return err
		}},
		{name: "add base", call: func(settings *SettingsService, _ model.Config, _, mutationPath string) error {
			_, err := settings.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "added", Path: mutationPath})
			return err
		}},
		{name: "switch base", call: func(settings *SettingsService, _ model.Config, _, _ string) error {
			_, err := settings.SwitchBase("other")
			return err
		}},
		{name: "update base", call: func(settings *SettingsService, _ model.Config, otherPath, _ string) error {
			_, err := settings.UpdateBase("other", model.BaseUpdateRequest{Name: "renamed", Path: otherPath})
			return err
		}},
		{name: "forget base", call: func(settings *SettingsService, _ model.Config, _, _ string) error {
			_, err := settings.ForgetBase("other")
			return err
		}},
		{name: "replace config", call: func(settings *SettingsService, replacement model.Config, _, _ string) error {
			_, err := settings.ReplaceConfig(replacement)
			return err
		}},
		{name: "configure git", call: func(settings *SettingsService, _ model.Config, _, _ string) error {
			_, err := settings.ConfigureGit(context.Background(), "active", model.GitConfigRequest{GitURL: "git@example.test:notes.git", GitBranch: "main"})
			return err
		}},
		{name: "disable git", call: func(settings *SettingsService, _ model.Config, _, _ string) error {
			_, err := settings.DisableGit(context.Background(), "active")
			return err
		}},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			coordinator := NewBaseOperationCoordinator()
			settings, otherPath := newConfiguredSettingsServiceWithCoordinator(t, coordinator)
			replacement := settings.GetConfig()
			mutationPath := t.TempDir()
			coordinator.Lock()
			t.Cleanup(func() {
				if coordinator != nil {
					coordinator.Unlock()
				}
			})

			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				close(started)
				done <- mutation.call(settings, replacement, otherPath, mutationPath)
			}()
			<-started
			for range 10 {
				runtime.Gosched()
			}

			if !settings.mu.TryLock() {
				coordinator.Unlock()
				coordinator = nil
				<-done
				t.Fatal("settings lock was taken while mutation should be waiting at coordinator")
			}
			settings.mu.Unlock()
			select {
			case err := <-done:
				coordinator.Unlock()
				coordinator = nil
				t.Fatalf("mutation completed while coordinator was held: %v", err)
			default:
			}

			coordinator.Unlock()
			coordinator = nil
			if err := <-done; err != nil {
				t.Fatalf("mutation after coordinator release error = %v", err)
			}
		})
	}
}

func newConfiguredSettingsServiceWithCoordinator(t *testing.T, coordinator *BaseOperationCoordinator) (*SettingsService, string) {
	t.Helper()
	activePath := t.TempDir()
	otherPath := t.TempDir()
	completed := false
	config := model.Config{
		Bases: []model.Base{
			{Name: "active", Path: activePath},
			{Name: "other", Path: otherPath},
		},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	settings, err := NewSettingsServiceWithGit(
		&fakeConfigStore{config: &config},
		&fakeBaseRuntime{path: activePath},
		coordinator,
		"",
		nil,
		&fakeGitConfigValidator{},
		&fakeGitStatusStore{statuses: make(map[string]model.GitStatus)},
	)
	if err != nil {
		t.Fatalf("NewSettingsServiceWithGit() error = %v", err)
	}
	return settings, otherPath
}

func TestNewSettingsServiceRejectsNilCoordinator(t *testing.T) {
	settings, err := NewSettingsService(&fakeConfigStore{}, &fakeBaseRuntime{}, nil, "", nil)
	if settings != nil {
		t.Errorf("NewSettingsService() service = %#v, want nil", settings)
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewSettingsService() error = %v, want ErrInvalidConfig", err)
	}
	if !strings.Contains(err.Error(), "coordinator") {
		t.Errorf("NewSettingsService() error = %q, want coordinator context", err)
	}
}

func TestNewSettingsServiceWithGitRejectsNilCoordinator(t *testing.T) {
	settings, err := NewSettingsServiceWithGit(
		&fakeConfigStore{},
		&fakeBaseRuntime{},
		nil,
		"",
		nil,
		&fakeGitConfigValidator{},
		&fakeGitStatusStore{},
	)
	if settings != nil {
		t.Errorf("NewSettingsServiceWithGit() service = %#v, want nil", settings)
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewSettingsServiceWithGit() error = %v, want ErrInvalidConfig", err)
	}
	if !strings.Contains(err.Error(), "coordinator") {
		t.Errorf("NewSettingsServiceWithGit() error = %q, want coordinator context", err)
	}
}

func TestSettingsServiceSuccessfulPathReplacementClearsRemovedConflictOnly(t *testing.T) {
	settings, store, coordinator, activePath, otherPath := newConflictSettingsService(t)
	newPath := t.TempDir()
	coordinator.SetConflict(activePath, true)
	coordinator.SetConflict(otherPath, true)

	if _, err := settings.UpdateBase("other", model.BaseUpdateRequest{Name: "other", Path: newPath}); err != nil {
		t.Fatalf("UpdateBase() error = %v", err)
	}
	if err := coordinator.CheckMutation(otherPath); err != nil {
		t.Errorf("removed path conflict = %v, want nil", err)
	}
	if err := coordinator.CheckMutation(activePath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("retained active path conflict = %v, want ErrGitConflictPending", err)
	}
	if store.saveCalls != 1 {
		t.Errorf("Save calls = %d, want 1", store.saveCalls)
	}
}

func TestSettingsServiceSamePathRenameRetainsConflict(t *testing.T) {
	settings, _, coordinator, _, otherPath := newConflictSettingsService(t)
	coordinator.SetConflict(otherPath, true)

	if _, err := settings.UpdateBase("other", model.BaseUpdateRequest{Name: "renamed", Path: otherPath}); err != nil {
		t.Fatalf("UpdateBase() error = %v", err)
	}
	if err := coordinator.CheckMutation(otherPath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("same-path conflict = %v, want ErrGitConflictPending", err)
	}
}

func TestSettingsServiceForgetBaseClearsRemovedConflict(t *testing.T) {
	settings, _, coordinator, _, otherPath := newConflictSettingsService(t)
	coordinator.SetConflict(otherPath, true)

	if _, err := settings.ForgetBase("other"); err != nil {
		t.Fatalf("ForgetBase() error = %v", err)
	}
	if err := coordinator.CheckMutation(otherPath); err != nil {
		t.Errorf("forgotten path conflict = %v, want nil", err)
	}
}

func TestSettingsServiceFailedPersistenceRetainsConflict(t *testing.T) {
	settings, store, coordinator, _, otherPath := newConflictSettingsService(t)
	saveErr := errors.New("save failed")
	store.saveErr = saveErr
	coordinator.SetConflict(otherPath, true)

	if _, err := settings.ForgetBase("other"); !errors.Is(err, saveErr) {
		t.Fatalf("ForgetBase() error = %v, want %v", err, saveErr)
	}
	if err := coordinator.CheckMutation(otherPath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("conflict after failed persistence = %v, want ErrGitConflictPending", err)
	}
}

func TestSettingsServiceReplaceConfigClearsRemovedConflict(t *testing.T) {
	settings, _, coordinator, _, otherPath := newConflictSettingsService(t)
	coordinator.SetConflict(otherPath, true)
	next := settings.GetConfig()
	next.Bases = next.Bases[:1]

	if _, err := settings.ReplaceConfig(next); err != nil {
		t.Fatalf("ReplaceConfig() error = %v", err)
	}
	if err := coordinator.CheckMutation(otherPath); err != nil {
		t.Errorf("removed replacement path conflict = %v, want nil", err)
	}
}

func TestSettingsServiceForgetBaseClearsCanonicalConflictForLoadedSymlink(t *testing.T) {
	activePath := t.TempDir()
	repositoryPath := t.TempDir()
	aliasPath := filepath.Join(t.TempDir(), "repo-link")
	createSymlinkOrSkip(t, repositoryPath, aliasPath)
	completed := true
	config := model.Config{
		Bases: []model.Base{
			{Name: "active", Path: activePath},
			{Name: "work", Path: aliasPath},
		},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	settings, _, _, validator, statuses := newGitSettingsServiceForConfig(t, config, activePath)
	configureTestBaseGit(t, settings, validator, "work")
	if _, exists := statuses.statuses[repositoryPath]; !exists {
		t.Fatalf("ConfigureGit() did not publish canonical status path %q", repositoryPath)
	}
	settings.coordinator.SetConflict(repositoryPath, true)

	if _, err := settings.ForgetBase("work"); err != nil {
		t.Fatalf("ForgetBase() error = %v", err)
	}
	if err := settings.coordinator.CheckMutation(repositoryPath); err != nil {
		t.Errorf("canonical conflict after forgetting symlinked base = %v, want nil", err)
	}
}

func TestSettingsServiceForgetBaseRetainsCanonicalConflictForLoadedAlias(t *testing.T) {
	repositoryPath := t.TempDir()
	aliasPath := filepath.Join(t.TempDir(), "repo-link")
	createSymlinkOrSkip(t, repositoryPath, aliasPath)
	completed := true
	work := model.Base{Name: "work", Path: repositoryPath}
	setConfiguredGit(&work)
	config := model.Config{
		Bases: []model.Base{
			work,
			{Name: "alias", Path: aliasPath},
		},
		CurrentBase:    "alias",
		SetupCompleted: &completed,
	}
	settings, _, _, _, _ := newGitSettingsServiceForConfig(t, config, aliasPath)
	settings.coordinator.SetConflict(repositoryPath, true)

	if _, err := settings.ForgetBase("work"); err != nil {
		t.Fatalf("ForgetBase() error = %v", err)
	}
	if err := settings.coordinator.CheckMutation(repositoryPath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("conflict retained through physical alias = %v, want ErrGitConflictPending", err)
	}
}

func TestSettingsServiceConflictSnapshotPrecedesPersistenceForRemovedAlias(t *testing.T) {
	activePath := t.TempDir()
	originalPath := t.TempDir()
	retargetedPath := t.TempDir()
	aliasPath := filepath.Join(t.TempDir(), "repo-link")
	createSymlinkOrSkip(t, originalPath, aliasPath)
	completed := true
	config := model.Config{
		Bases: []model.Base{
			{Name: "active", Path: activePath},
			{Name: "work", Path: aliasPath},
		},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	settings, store, _, _, _ := newGitSettingsServiceForConfig(t, config, activePath)
	settings.coordinator.SetConflict(originalPath, true)
	settings.coordinator.SetConflict(retargetedPath, true)
	store.saveStarted = make(chan struct{})
	release := make(chan struct{})
	store.saveRelease = release
	var releaseOnce sync.Once
	releaseSave := func() { releaseOnce.Do(func() { close(release) }) }
	done := make(chan error, 1)
	drained := false
	t.Cleanup(func() {
		releaseSave()
		if !drained {
			<-done
		}
	})
	go func() {
		_, err := settings.ForgetBase("work")
		done <- err
	}()
	<-store.saveStarted
	retargetSymlink(t, aliasPath, retargetedPath)
	releaseSave()
	err := <-done
	drained = true
	if err != nil {
		t.Fatalf("ForgetBase() error = %v", err)
	}
	if err := settings.coordinator.CheckMutation(originalPath); err != nil {
		t.Errorf("original conflict after publication = %v, want nil", err)
	}
	if err := settings.coordinator.CheckMutation(retargetedPath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("retargeted conflict after publication = %v, want ErrGitConflictPending", err)
	}
}

func TestSettingsServiceConflictSnapshotPrecedesPersistenceForRetainedAlias(t *testing.T) {
	originalPath := t.TempDir()
	retargetedPath := t.TempDir()
	aliasPath := filepath.Join(t.TempDir(), "repo-link")
	createSymlinkOrSkip(t, originalPath, aliasPath)
	completed := true
	config := model.Config{
		Bases: []model.Base{
			{Name: "work", Path: originalPath},
			{Name: "alias", Path: aliasPath},
		},
		CurrentBase:    "alias",
		SetupCompleted: &completed,
	}
	settings, store, _, _, _ := newGitSettingsServiceForConfig(t, config, aliasPath)
	settings.coordinator.SetConflict(originalPath, true)
	store.saveStarted = make(chan struct{})
	release := make(chan struct{})
	store.saveRelease = release
	var releaseOnce sync.Once
	releaseSave := func() { releaseOnce.Do(func() { close(release) }) }
	done := make(chan error, 1)
	drained := false
	t.Cleanup(func() {
		releaseSave()
		if !drained {
			<-done
		}
	})
	go func() {
		_, err := settings.ForgetBase("work")
		done <- err
	}()
	<-store.saveStarted
	retargetSymlink(t, aliasPath, retargetedPath)
	releaseSave()
	err := <-done
	drained = true
	if err != nil {
		t.Fatalf("ForgetBase() error = %v", err)
	}
	if err := settings.coordinator.CheckMutation(originalPath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("original conflict retained through pre-save alias = %v, want ErrGitConflictPending", err)
	}
}

func TestSettingsServiceBlockedSavePinsActiveAliasGeneration(t *testing.T) {
	pinnedPath := t.TempDir()
	conflictedPath := t.TempDir()
	writeTestNote(t, pinnedPath, "pinned.md", "pinned")
	aliasPath := filepath.Join(t.TempDir(), "active-link")
	createSymlinkOrSkip(t, pinnedPath, aliasPath)
	completed := true
	config := model.Config{
		Bases: []model.Base{
			{Name: "active", Path: aliasPath},
			{Name: "conflicted", Path: conflictedPath},
		},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	repo := &fakeNoteRepository{}
	notes := newTestNoteService(t, repo, aliasPath)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	storeConfig := cloneConfig(config)
	store := &fakeConfigStore{config: &storeConfig, saveStarted: make(chan struct{})}
	release := make(chan struct{})
	store.saveRelease = release
	settings, err := NewSettingsService(store, notes, notes.coordinator, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	settings.coordinator.SetConflict(conflictedPath, true)

	done := make(chan error, 1)
	go func() {
		_, err := settings.ForgetBase("conflicted")
		done <- err
	}()
	<-store.saveStarted
	retargetSymlink(t, aliasPath, conflictedPath)
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("ForgetBase() error = %v", err)
	}

	published := settings.GetConfig()
	if len(published.Bases) != 1 || published.Bases[0].Path != pinnedPath {
		t.Errorf("published bases = %#v, want active pinned to %q", published.Bases, pinnedPath)
	}
	if store.config == nil || !reflect.DeepEqual(*store.config, published) {
		t.Errorf("persisted config = %#v, want published generation %#v", store.config, published)
	}
	if got := notes.GetBasePath(); got != pinnedPath {
		t.Errorf("runtime path = %q, want pinned %q", got, pinnedPath)
	}
	if content, err := notes.GetNoteContent("pinned.md"); err != nil || content != "pinned" {
		t.Errorf("pinned runtime note = %q, %v; want pinned, nil", content, err)
	}
	if err := settings.coordinator.CheckMutation(conflictedPath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("retargeted conflict = %v, want ErrGitConflictPending", err)
	}

	restartCoordinator := NewBaseOperationCoordinator()
	restartNotes := NewNoteService(&fakeNoteRepository{}, pinnedPath, restartCoordinator)
	defer restartNotes.Close()
	restarted, err := NewSettingsService(store, restartNotes, restartCoordinator, "", nil)
	if err != nil {
		t.Fatalf("restart NewSettingsService() error = %v", err)
	}
	if got := restarted.GetConfig().Bases[0].Path; got != pinnedPath {
		t.Errorf("restart active path = %q, want %q", got, pinnedPath)
	}
}

func TestSettingsServiceBrokenRemovedGitAliasUsesStatusConflictIdentity(t *testing.T) {
	activePath := t.TempDir()
	repositoryPath := t.TempDir()
	aliasPath := filepath.Join(t.TempDir(), "repo-link")
	createSymlinkOrSkip(t, repositoryPath, aliasPath)
	completed := true
	work := model.Base{Name: "work", Path: aliasPath}
	setConfiguredGit(&work)
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: activePath}, work},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	settings, _, _, _, statuses := newGitSettingsServiceForConfig(t, config, activePath)
	statuses.statuses[repositoryPath] = model.GitStatus{Base: "work", RepositoryPath: repositoryPath, State: model.GitStateConflict}
	settings.coordinator.SetConflict(repositoryPath, true)
	if err := os.Remove(aliasPath); err != nil {
		t.Fatalf("Remove symlink error = %v", err)
	}

	if _, err := settings.ForgetBase("work"); err != nil {
		t.Fatalf("ForgetBase() error = %v", err)
	}
	if err := settings.coordinator.CheckMutation(repositoryPath); err != nil {
		t.Errorf("canonical conflict after forgetting broken Git alias = %v, want nil", err)
	}
	if _, exists := statuses.statuses[repositoryPath]; exists {
		t.Error("forgotten Git status remains")
	}
}

func TestSettingsServiceRetargetedRemovedGitAliasClearsStatusConflictIdentity(t *testing.T) {
	activePath := t.TempDir()
	originalPath := t.TempDir()
	retargetedPath := t.TempDir()
	aliasPath := filepath.Join(t.TempDir(), "repo-link")
	createSymlinkOrSkip(t, originalPath, aliasPath)
	completed := true
	work := model.Base{Name: "work", Path: aliasPath}
	setConfiguredGit(&work)
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: activePath}, work},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	settings, _, _, _, statuses := newGitSettingsServiceForConfig(t, config, activePath)
	statuses.statuses[originalPath] = model.GitStatus{Base: "work", RepositoryPath: originalPath, State: model.GitStateConflict}
	settings.coordinator.SetConflict(originalPath, true)
	settings.coordinator.SetConflict(retargetedPath, true)
	retargetSymlink(t, aliasPath, retargetedPath)

	if _, err := settings.ForgetBase("work"); err != nil {
		t.Fatalf("ForgetBase() error = %v", err)
	}
	if err := settings.coordinator.CheckMutation(originalPath); err != nil {
		t.Errorf("original status conflict after forgetting retargeted alias = %v, want nil", err)
	}
	if err := settings.coordinator.CheckMutation(retargetedPath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("retargeted conflict after forgetting alias = %v, want ErrGitConflictPending", err)
	}
}

func TestSettingsServiceBrokenRetainedAliasConservativelyKeepsConflict(t *testing.T) {
	repositoryPath := t.TempDir()
	aliasPath := filepath.Join(t.TempDir(), "repo-link")
	createSymlinkOrSkip(t, repositoryPath, aliasPath)
	completed := true
	work := model.Base{Name: "work", Path: repositoryPath}
	setConfiguredGit(&work)
	config := model.Config{
		Bases:          []model.Base{work, {Name: "alias", Path: aliasPath}},
		CurrentBase:    "alias",
		SetupCompleted: &completed,
	}
	settings, _, _, _, statuses := newGitSettingsServiceForConfig(t, config, aliasPath)
	statuses.statuses[repositoryPath] = model.GitStatus{Base: "work", RepositoryPath: repositoryPath, State: model.GitStateConflict}
	settings.coordinator.SetConflict(repositoryPath, true)
	if err := os.Remove(aliasPath); err != nil {
		t.Fatalf("Remove symlink error = %v", err)
	}

	if _, err := settings.ForgetBase("work"); err != nil {
		t.Fatalf("ForgetBase() error = %v", err)
	}
	if err := settings.coordinator.CheckMutation(repositoryPath); !errors.Is(err, ErrGitConflictPending) {
		t.Errorf("conflict with unknown retained alias = %v, want ErrGitConflictPending", err)
	}
}

func newConflictSettingsService(t *testing.T) (*SettingsService, *fakeConfigStore, *BaseOperationCoordinator, string, string) {
	t.Helper()
	activePath := t.TempDir()
	otherPath := t.TempDir()
	completed := true
	config := model.Config{
		Bases: []model.Base{
			{Name: "active", Path: activePath},
			{Name: "other", Path: otherPath},
		},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config}
	coordinator := NewBaseOperationCoordinator()
	settings, err := NewSettingsService(store, &fakeBaseRuntime{path: activePath}, coordinator, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	return settings, store, coordinator, activePath, otherPath
}

func TestNewSettingsServiceMigratesLegacyConfig(t *testing.T) {
	store := &fakeConfigStore{config: &model.Config{
		BaseDir:     "/notes",
		Bases:       []model.Base{{Name: "personal", Path: "/notes/personal"}},
		CurrentBase: "personal",
	}}

	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/personal"}, NewBaseOperationCoordinator(), "", nil)
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

	service, err := NewSettingsService(store, notes, notes.coordinator, "", nil)
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
	coordinator := NewBaseOperationCoordinator()
	notes := NewNoteService(&fakeNoteRepository{}, basePath, coordinator)
	if err := notes.Close(); err != nil {
		t.Fatalf("NoteService.Close() error = %v", err)
	}

	service, err := NewSettingsService(store, notes, coordinator, "", nil)
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
	service, err := NewSettingsService(store, notes, notes.coordinator, "", nil)
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

	service, err := NewSettingsService(store, &fakeBaseRuntime{}, NewBaseOperationCoordinator(), "missing", nil)
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

			service, err := NewSettingsService(store, &fakeBaseRuntime{path: tt.runtimePath}, NewBaseOperationCoordinator(), "", nil)
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

	service, err := NewSettingsService(store, &fakeBaseRuntime{}, NewBaseOperationCoordinator(), "", nil)
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

	service, err := NewSettingsService(store, runtime, NewBaseOperationCoordinator(), "work", nil)
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

	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/work"}, NewBaseOperationCoordinator(), "work", nil)
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

	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/personal"}, NewBaseOperationCoordinator(), "work", nil)
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
	service, err := NewSettingsService(&fakeConfigStore{}, &fakeBaseRuntime{}, NewBaseOperationCoordinator(), "", nil)
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
		NewBaseOperationCoordinator(),
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
	service, err := NewSettingsService(nil, &fakeBaseRuntime{}, NewBaseOperationCoordinator(), "", nil)
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

	service, err := NewSettingsService(store, nil, NewBaseOperationCoordinator(), "", nil)
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

	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/work"}, NewBaseOperationCoordinator(), "", nil)
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

	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/personal"}, NewBaseOperationCoordinator(), "", nil)
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

	service, err := NewSettingsService(store, runtime, NewBaseOperationCoordinator(), "", nil)
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
	service, err := NewSettingsService(store, &fakeBaseRuntime{path: "/notes/personal"}, NewBaseOperationCoordinator(), "", nil)
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
	service, err := NewSettingsService(store, runtime, NewBaseOperationCoordinator(), "", nil)
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
	service, err := NewSettingsService(store, runtime, NewBaseOperationCoordinator(), "", nil)
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
		service, err := NewSettingsService(store, runtime, NewBaseOperationCoordinator(), "", nil)
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
		svc, err := NewSettingsService(store, runtime, NewBaseOperationCoordinator(), "", nil)
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
			{Name: "  renamed  ", Path: activeLink, GitURL: "active.git", AutoSync: true},
			{Name: "other", Path: otherPath, GitURL: "other.git"},
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
		Bases:       []model.Base{{Name: "new-active", Path: targetPath}, {Name: "other", Path: otherPath, GitURL: "other.git"}},
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
	service, err := NewSettingsService(store, runtime, NewBaseOperationCoordinator(), "", nil)
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

func TestNewSettingsServiceWithGitDoesNotCallGitDependencies(t *testing.T) {
	basePath := t.TempDir()
	completed := true
	config := model.Config{
		Bases:       []model.Base{{Name: "work", Path: basePath, GitURL: "legacy.git"}},
		CurrentBase: "work", SetupCompleted: &completed,
	}
	validator := &fakeGitConfigValidator{err: errors.New("must not validate at startup")}
	statuses := &exactGitStatusStore{}

	service, err := NewSettingsServiceWithGit(&fakeConfigStore{config: &config}, &fakeBaseRuntime{path: basePath}, NewBaseOperationCoordinator(), "", nil, validator, statuses)
	if err != nil {
		t.Fatalf("NewSettingsServiceWithGit() error = %v", err)
	}
	if service == nil || validator.calls != 0 || statuses.calls != 0 {
		t.Fatalf("startup Git calls = validator %d store %d", validator.calls, statuses.calls)
	}
}

func TestSettingsServiceConfigureGitNormalizesAndResetsStatus(t *testing.T) {
	service, store, _, validator, statuses, config := newGitSettingsService(t)
	oldStatus := model.GitStatus{Base: "work", RepositoryPath: config.Bases[0].Path, State: model.GitStateReady, Ahead: 3, ChangedPaths: []string{"note.md"}, RemoteOID: "abc"}
	statuses.statuses[config.Bases[0].Path] = oldStatus
	normalized := model.GitConfigRequest{
		GitURL: "https://example.com/normalized.git", GitBranch: "feature/editor", AutoSync: true,
		AutoSyncIntervalMinutes: 15, GitCommitMessageTemplate: "sync {{count}}",
		Confirmations: model.GitConfirmations{CreateRepository: true},
	}
	validator.normalized = &normalized
	request := model.GitConfigRequest{GitURL: " raw ", GitBranch: " raw ", Confirmations: model.GitConfirmations{ReplaceOrigin: true}}

	response, err := service.ConfigureGit(context.Background(), "work", request)
	if err != nil {
		t.Fatalf("ConfigureGit() error = %v", err)
	}
	wantBase := config.Bases[0]
	wantBase.GitURL = normalized.GitURL
	wantBase.GitBranch = normalized.GitBranch
	wantBase.AutoSync = normalized.AutoSync
	wantBase.AutoSyncIntervalMinutes = normalized.AutoSyncIntervalMinutes
	wantBase.GitCommitMessageTemplate = normalized.GitCommitMessageTemplate
	if !reflect.DeepEqual(response.Base, wantBase) || !reflect.DeepEqual(store.config.Bases[0], wantBase) {
		t.Fatalf("saved base = %#v response %#v, want %#v", store.config.Bases[0], response.Base, wantBase)
	}
	wantStatus := model.GitStatus{Base: "work", RepositoryPath: config.Bases[0].Path, State: model.GitStateNeedsReconnect, ChangedPaths: []string{}}
	if !reflect.DeepEqual(response.Status, wantStatus) || !reflect.DeepEqual(statuses.statuses[config.Bases[0].Path], wantStatus) {
		t.Fatalf("status = %#v stored %#v, want %#v", response.Status, statuses.statuses[config.Bases[0].Path], wantStatus)
	}
	if validator.calls != 1 || validator.dir != config.Bases[0].Path || !reflect.DeepEqual(validator.request, request) {
		t.Errorf("validator calls/args = %d %q %#v", validator.calls, validator.dir, validator.request)
	}
	if response.Base.GitURL == request.GitURL || store.saveCalls != 1 {
		t.Errorf("normalization/save = URL %q saves %d", response.Base.GitURL, store.saveCalls)
	}
}

func TestSettingsServiceConfigureGitRejectsNonLiteralNormalizedBranch(t *testing.T) {
	for _, branch := range []string{"", "HEAD", "@{-1}", "main~1", "refs/heads/main"} {
		t.Run(strings.ReplaceAll(branch, "/", "_"), func(t *testing.T) {
			service, store, _, validator, statuses, original := newGitSettingsService(t)
			normalized := model.GitConfigRequest{GitURL: "https://example.com/notes.git", GitBranch: branch}
			validator.normalized = &normalized
			_, err := service.ConfigureGit(context.Background(), "work", normalized)
			if !errors.Is(err, ErrInvalidGitBranch) {
				t.Fatalf("ConfigureGit(%q) error = %v, want ErrInvalidGitBranch", branch, err)
			}
			assertFieldError(t, err, "git_branch")
			if store.saveCalls != 0 || len(statuses.getCalls)+len(statuses.upsertCalls)+len(statuses.deleteCalls) != 0 || !reflect.DeepEqual(service.GetConfig(), original) {
				t.Fatalf("rejected branch mutated state: saves %d status calls %v/%v/%v", store.saveCalls, statuses.getCalls, statuses.upsertCalls, statuses.deleteCalls)
			}
		})
	}
}

func TestSettingsServiceConfigureGitActivatesLegacyURLOnlyBase(t *testing.T) {
	service, _, _, validator, statuses, config := newGitSettingsService(t)
	legacy := config
	legacy.Bases[0].GitURL = "legacy.example/notes.git"
	legacy.Bases[0].GitBranch = ""
	service.config = legacy
	normalized := model.GitConfigRequest{GitURL: legacy.Bases[0].GitURL, GitBranch: "main"}
	validator.normalized = &normalized

	response, err := service.ConfigureGit(context.Background(), "work", normalized)
	if err != nil {
		t.Fatalf("ConfigureGit() error = %v", err)
	}
	if response.Base.GitBranch != "main" || response.Status.State != model.GitStateNeedsReconnect || len(statuses.upsertCalls) != 1 {
		t.Fatalf("legacy activation response = %#v status calls %#v", response, statuses.upsertCalls)
	}
}

func TestSettingsServiceGitMutationsRequireDependenciesAndExactBase(t *testing.T) {
	basePath := t.TempDir()
	completed := true
	config := model.Config{Bases: []model.Base{{Name: "Work", Path: basePath}}, CurrentBase: "Work", SetupCompleted: &completed}
	store := &fakeConfigStore{config: &config}
	service, err := NewSettingsService(store, &fakeBaseRuntime{path: basePath}, NewBaseOperationCoordinator(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		call func() error
		kind error
	}{
		{name: "configure missing dependencies", call: func() error {
			_, err := service.ConfigureGit(context.Background(), "Work", model.GitConfigRequest{GitBranch: "main"})
			return err
		}, kind: ErrInvalidConfig},
		{name: "disable missing dependencies", call: func() error { _, err := service.DisableGit(context.Background(), "Work"); return err }, kind: ErrInvalidConfig},
		{name: "exact name", call: func() error {
			_, err := service.ConfigureGit(context.Background(), "work", model.GitConfigRequest{})
			return err
		}, kind: ErrBaseNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, test.kind) {
				t.Fatalf("error = %v, want %v", err, test.kind)
			}
		})
	}
	if store.saveCalls != 0 {
		t.Fatalf("Save calls = %d, want 0", store.saveCalls)
	}
}

func TestSettingsServiceConfigureGitRejectsConfiguredCanonicalPathCollision(t *testing.T) {
	service, store, _, validator, statuses, original := newGitSettingsService(t)
	path := original.Bases[0].Path
	link := filepath.Join(t.TempDir(), "alias")
	createSymlinkOrSkip(t, path, link)
	other := model.Base{Name: "other", Path: link, GitURL: "other.git", GitBranch: "main"}
	service.config.Bases = append(service.config.Bases, other)
	store.config.Bases = append(store.config.Bases, other)
	normalized := model.GitConfigRequest{GitURL: "work.git", GitBranch: "main"}
	validator.normalized = &normalized

	_, err := service.ConfigureGit(context.Background(), "work", normalized)
	if !errors.Is(err, ErrGitRepositoryInUse) {
		t.Fatalf("ConfigureGit() error = %v, want ErrGitRepositoryInUse", err)
	}
	if store.saveCalls != 0 || len(statuses.getCalls)+len(statuses.upsertCalls) != 0 {
		t.Fatalf("collision made persistence calls: saves %d get %v upsert %v", store.saveCalls, statuses.getCalls, statuses.upsertCalls)
	}
}

func TestSamePhysicalFileMatchesDirectoryAliases(t *testing.T) {
	physicalPath := t.TempDir()
	aliasPath := filepath.Join(t.TempDir(), "alias")
	createSymlinkOrSkip(t, physicalPath, aliasPath)
	physicalInfo, err := os.Stat(physicalPath)
	if err != nil {
		t.Fatalf("Stat(physical) error = %v", err)
	}
	aliasInfo, err := os.Stat(aliasPath)
	if err != nil {
		t.Fatalf("Stat(alias) error = %v", err)
	}
	if !samePhysicalFile(physicalInfo, aliasInfo) {
		t.Fatal("samePhysicalFile() = false for two names of one directory")
	}
}

func TestSameConflictIdentityPrefersPhysicalIdentity(t *testing.T) {
	firstInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("Stat(first) error = %v", err)
	}
	secondInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("Stat(second) error = %v", err)
	}
	if sameConflictIdentity(
		conflictPathResolution{path: "same", info: firstInfo, stable: true},
		conflictPathResolution{path: "same", info: secondInfo, stable: true},
	) {
		t.Fatal("sameConflictIdentity() used matching strings despite distinct physical identities")
	}
	if !sameConflictIdentity(
		conflictPathResolution{path: "first", info: firstInfo, stable: true},
		conflictPathResolution{path: "second", info: firstInfo, stable: true},
	) {
		t.Fatal("sameConflictIdentity() ignored matching physical identities with distinct strings")
	}
}

func TestValidateUniqueGitRepositoryPathsRejectsPhysicalCaseAlias(t *testing.T) {
	parent := t.TempDir()
	physicalPath := filepath.Join(parent, "CaseAlias")
	if err := os.Mkdir(physicalPath, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	aliasPath := filepath.Join(parent, "casealias")
	physicalInfo, err := os.Stat(physicalPath)
	if err != nil {
		t.Fatalf("Stat(physical) error = %v", err)
	}
	aliasInfo, err := os.Stat(aliasPath)
	if err != nil || !os.SameFile(physicalInfo, aliasInfo) {
		t.Skip("filesystem does not expose case-distinct names for one directory")
	}
	first := model.Base{Name: "first", Path: physicalPath}
	second := model.Base{Name: "second", Path: aliasPath}
	setConfiguredGit(&first)
	setConfiguredGit(&second)
	if err := validateUniqueGitRepositoryPaths([]model.Base{first, second}); !errors.Is(err, ErrGitRepositoryInUse) {
		t.Fatalf("validateUniqueGitRepositoryPaths() error = %v, want ErrGitRepositoryInUse", err)
	}
}

func TestSettingsServiceConfigureAndDisableDoNotMutateRepositoryFiles(t *testing.T) {
	service, _, _, validator, _, config := newGitSettingsService(t)
	gitDir := filepath.Join(config.Bases[0].Path, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(gitDir, "config")
	if err := os.WriteFile(marker, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	normalized := model.GitConfigRequest{GitURL: "work.git", GitBranch: "main"}
	validator.normalized = &normalized
	if _, err := service.ConfigureGit(context.Background(), "work", normalized); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DisableGit(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "before" {
		t.Fatalf("repository marker = %q, %v; want unchanged", contents, err)
	}
}

func TestSettingsServiceDisableGitClearsOnlyGitFieldsAndDeletesStatus(t *testing.T) {
	service, store, _, _, statuses, config := newGitSettingsService(t)
	base := &service.config.Bases[0]
	base.GitURL, base.GitBranch, base.AutoSync = "work.git", "main", true
	base.AutoSyncIntervalMinutes, base.GitCommitMessageTemplate = 30, "sync"
	store.config = ptrConfig(service.config)
	statuses.statuses[config.Bases[0].Path] = model.GitStatus{Base: "work", RepositoryPath: config.Bases[0].Path, State: model.GitStateReady, ChangedPaths: []string{"a.md"}}

	response, err := service.DisableGit(context.Background(), "work")
	if err != nil {
		t.Fatalf("DisableGit() error = %v", err)
	}
	if response.Base.Name != "work" || response.Base.Path != config.Bases[0].Path || response.Base.GitURL != "" || response.Base.GitBranch != "" || response.Base.AutoSync || response.Base.AutoSyncIntervalMinutes != 0 || response.Base.GitCommitMessageTemplate != "" {
		t.Fatalf("disabled base = %#v", response.Base)
	}
	wantStatus := model.GitStatus{Base: "work", RepositoryPath: config.Bases[0].Path, State: model.GitStateUnconfigured, ChangedPaths: []string{}}
	if !reflect.DeepEqual(response.Status, wantStatus) || len(statuses.statuses) != 0 || store.saveCalls != 1 {
		t.Fatalf("disable status/store = %#v %#v saves %d", response.Status, statuses.statuses, store.saveCalls)
	}
}

func TestSettingsServiceDisableGitAfterRepositoryPathDisappears(t *testing.T) {
	activePath := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "base-link")
	createSymlinkOrSkip(t, target, link)
	completed := true
	config := model.Config{
		Bases:       []model.Base{{Name: "active", Path: activePath}, {Name: "work", Path: link}},
		CurrentBase: "active", SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config}
	runtime := &fakeBaseRuntime{path: activePath}
	statuses := &fakeGitStatusStore{statuses: make(map[string]model.GitStatus)}
	validator := &fakeGitConfigValidator{}
	service, err := NewSettingsServiceWithGit(store, runtime, NewBaseOperationCoordinator(), "", nil, validator, statuses)
	if err != nil {
		t.Fatal(err)
	}
	configureTestBaseGit(t, service, validator, "work")
	if _, ok := statuses.statuses[target]; !ok || len(statuses.statuses) != 1 {
		t.Fatalf("ConfigureGit statuses = %#v, want canonical target %q", statuses.statuses, target)
	}
	pathCalls := runtime.pathCalls
	persistCalls := runtime.persistCalls
	if err := os.Remove(link); err != nil {
		t.Fatalf("Remove(link) error = %v", err)
	}

	response, err := service.DisableGit(context.Background(), "work")
	if err != nil {
		t.Fatalf("DisableGit() error = %v", err)
	}
	if response.Base.GitURL != "" || response.Base.GitBranch != "" || response.Base.AutoSync || response.Base.AutoSyncIntervalMinutes != 0 || response.Base.GitCommitMessageTemplate != "" {
		t.Fatalf("disabled base = %#v", response.Base)
	}
	if response.Status.RepositoryPath != target || response.Status.State != model.GitStateUnconfigured || response.Status.ChangedPaths == nil {
		t.Fatalf("disabled status = %#v", response.Status)
	}
	if len(statuses.statuses) != 0 || !reflect.DeepEqual(statuses.deleteCalls, []string{target}) || store.saveCalls != 2 {
		t.Fatalf("disable effects = statuses %#v deletes %v saves %d", statuses.statuses, statuses.deleteCalls, store.saveCalls)
	}
	if _, orphaned := statuses.statuses[filepath.Clean(link)]; orphaned || statuses.listCalls != 1 {
		t.Fatalf("lexical orphan/list calls = %t/%d", orphaned, statuses.listCalls)
	}
	if runtime.persistCalls != persistCalls+1 || runtime.pathCalls != pathCalls || len(runtime.transactionCalls) != 0 {
		t.Fatalf("DisableGit runtime calls = persists %d/%d+1 paths %d/%d transactions %v", runtime.persistCalls, persistCalls, runtime.pathCalls, pathCalls, runtime.transactionCalls)
	}
}

func TestSettingsServiceDisableGitWaitsForRuntimePersistence(t *testing.T) {
	path := t.TempDir()
	config := configuredGitTestConfig(path, t.TempDir())
	service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, path)
	statuses.statuses[path] = model.GitStatus{Base: "active", RepositoryPath: path, State: model.GitStateReady, ChangedPaths: []string{}}
	release := make(chan struct{})
	runtime.persistStarted = make(chan struct{})
	runtime.persistRelease = release
	done := make(chan error, 1)
	go func() {
		_, err := service.DisableGit(context.Background(), "active")
		done <- err
	}()

	select {
	case <-runtime.persistStarted:
	case err := <-done:
		t.Fatalf("DisableGit() bypassed runtime persistence with error %v", err)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("DisableGit() did not enter runtime persistence")
	}
	if store.saveCalls != 0 {
		t.Fatalf("ConfigStore.Save calls while runtime blocked = %d, want 0", store.saveCalls)
	}
	select {
	case err := <-done:
		t.Fatalf("DisableGit() returned while runtime persistence was blocked: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("DisableGit() error = %v", err)
	}
	if runtime.persistCalls != 1 || store.saveCalls != 1 {
		t.Fatalf("DisableGit persistence calls = runtime %d store %d, want 1/1", runtime.persistCalls, store.saveCalls)
	}
}

func TestSettingsServiceDisableGitRuntimeFailureRestoresStatusWithoutDirectSave(t *testing.T) {
	path := t.TempDir()
	config := configuredGitTestConfig(path, t.TempDir())
	service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, path)
	before := model.GitStatus{Base: "active", RepositoryPath: path, State: model.GitStateReady, ChangedPaths: []string{"note.md"}}
	statuses.statuses[path] = before
	runtime.persistErr = errors.New("runtime persistence failed")

	_, err := service.DisableGit(context.Background(), "active")
	if !errors.Is(err, runtime.persistErr) {
		t.Fatalf("DisableGit() error = %v, want %v", err, runtime.persistErr)
	}
	if !reflect.DeepEqual(statuses.statuses[path], before) || !reflect.DeepEqual(service.GetConfig(), config) {
		t.Fatalf("runtime failure state = status %#v config %#v", statuses.statuses[path], service.GetConfig())
	}
	if runtime.persistCalls != 1 || store.saveCalls != 0 {
		t.Fatalf("runtime failure persistence calls = runtime %d store %d, want 1/0", runtime.persistCalls, store.saveCalls)
	}
}

func TestSettingsServiceReplaceConfigProtectsGitFieldsInOrder(t *testing.T) {
	fields := []struct {
		field string
		edit  func(*model.Base)
	}{
		{field: "git_url", edit: func(base *model.Base) { base.GitURL = "changed.git" }},
		{field: "git_branch", edit: func(base *model.Base) { base.GitBranch = "changed" }},
		{field: "auto_sync", edit: func(base *model.Base) { base.AutoSync = false }},
		{field: "auto_sync_interval_minutes", edit: func(base *model.Base) { base.AutoSyncIntervalMinutes = 60 }},
		{field: "git_commit_message_template", edit: func(base *model.Base) { base.GitCommitMessageTemplate = "changed" }},
	}
	for _, test := range fields {
		t.Run(test.field, func(t *testing.T) {
			service, store, _, _, statuses, _ := newGitSettingsService(t)
			base := &service.config.Bases[0]
			base.GitURL, base.GitBranch, base.AutoSync = "work.git", "main", true
			base.AutoSyncIntervalMinutes, base.GitCommitMessageTemplate = 15, "sync"
			store.config = ptrConfig(service.config)
			input := cloneConfig(service.config)
			test.edit(&input.Bases[0])
			_, err := service.ReplaceConfig(input)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("ReplaceConfig() error = %v, want ErrInvalidConfig", err)
			}
			var fieldErr *FieldError
			if !errors.As(err, &fieldErr) || fieldErr.Field != "bases[0]."+test.field || fieldErr.Message != "Git settings must be changed through /api/git/config" {
				t.Fatalf("FieldError = %#v, want field %q and exact message", fieldErr, test.field)
			}
			if store.saveCalls != 0 || len(statuses.getCalls) != 0 {
				t.Fatalf("protected field made persistence calls: saves %d status gets %v", store.saveCalls, statuses.getCalls)
			}
		})
	}
	t.Run("first difference wins", func(t *testing.T) {
		service, store, _, _, _, _ := newGitSettingsService(t)
		setConfiguredGit(&service.config.Bases[0])
		store.config = ptrConfig(service.config)
		input := cloneConfig(service.config)
		for _, field := range fields {
			field.edit(&input.Bases[0])
		}
		_, err := service.ReplaceConfig(input)
		var fieldErr *FieldError
		if !errors.As(err, &fieldErr) || fieldErr.Field != "bases[0].git_url" {
			t.Fatalf("FieldError = %#v, want git_url", fieldErr)
		}
	})
}

func TestSettingsServiceReplaceConfigGitMatchingRules(t *testing.T) {
	t.Run("unchanged exact fields accepted for same name move", func(t *testing.T) {
		service, _, _, _, _, _ := newGitSettingsService(t)
		setConfiguredGit(&service.config.Bases[0])
		target := t.TempDir()
		input := cloneConfig(service.config)
		input.Bases[0].Path = target
		if _, err := service.ReplaceConfig(input); err != nil {
			t.Fatalf("ReplaceConfig() error = %v", err)
		}
	})
	t.Run("unchanged exact fields accepted for unique same path rename", func(t *testing.T) {
		service, _, _, _, _, _ := newGitSettingsService(t)
		setConfiguredGit(&service.config.Bases[0])
		input := cloneConfig(service.config)
		input.Bases[0].Name, input.CurrentBase = "renamed", "renamed"
		if _, err := service.ReplaceConfig(input); err != nil {
			t.Fatalf("ReplaceConfig() error = %v", err)
		}
	})
	t.Run("simultaneous configured rename and move is ambiguous", func(t *testing.T) {
		service, store, _, _, _, _ := newGitSettingsService(t)
		setConfiguredGit(&service.config.Bases[0])
		store.config = ptrConfig(service.config)
		input := cloneConfig(service.config)
		input.Bases[0].Name, input.Bases[0].Path, input.CurrentBase = "renamed", t.TempDir(), "renamed"
		_, err := service.ReplaceConfig(input)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("ReplaceConfig() error = %v, want ErrInvalidConfig", err)
		}
		assertFieldError(t, err, "bases[0].git_url")
	})
	t.Run("new generic base requires zero Git fields", func(t *testing.T) {
		service, _, _, _, _, _ := newGitSettingsService(t)
		input := cloneConfig(service.config)
		input.Bases = append(input.Bases, model.Base{Name: "new", Path: t.TempDir(), GitURL: "new.git"})
		_, err := service.ReplaceConfig(input)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("ReplaceConfig() error = %v, want ErrInvalidConfig", err)
		}
		assertFieldError(t, err, "bases[1].git_url")
	})
}

func TestSettingsServiceRejectsConfiguredPathConvergenceBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure bool
		call      func(*SettingsService, *fakeGitConfigValidator, string) error
	}{
		{name: "configure", configure: false, call: func(service *SettingsService, validator *fakeGitConfigValidator, alias string) error {
			normalized := model.GitConfigRequest{GitURL: "other.git", GitBranch: "different"}
			validator.normalized = &normalized
			_, err := service.ConfigureGit(context.Background(), "other", normalized)
			return err
		}},
		{name: "update", configure: true, call: func(service *SettingsService, _ *fakeGitConfigValidator, alias string) error {
			_, err := service.UpdateBase("other", model.BaseUpdateRequest{Name: "other", Path: alias})
			return err
		}},
		{name: "replace", configure: true, call: func(service *SettingsService, _ *fakeGitConfigValidator, alias string) error {
			input := service.GetConfig()
			input.Bases[1].Path = alias
			_, err := service.ReplaceConfig(input)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			activePath, otherPath := t.TempDir(), t.TempDir()
			alias := filepath.Join(t.TempDir(), "active-alias")
			createSymlinkOrSkip(t, activePath, alias)
			config := configuredGitTestConfig(activePath, otherPath)
			if test.configure {
				setConfiguredGit(&config.Bases[1])
				config.Bases[1].GitURL = "different.git"
				config.Bases[1].GitBranch = "different"
			} else {
				config.Bases[1].Path = alias
			}
			service, store, runtime, validator, statuses := newGitSettingsServiceForConfig(t, config, activePath)
			beforeConfig := service.GetConfig()
			beforePathCalls := runtime.pathCalls

			err := test.call(service, validator, alias)
			if !errors.Is(err, ErrGitRepositoryInUse) {
				t.Fatalf("mutation error = %v, want ErrGitRepositoryInUse", err)
			}
			if !reflect.DeepEqual(service.GetConfig(), beforeConfig) || store.saveCalls != 0 {
				t.Fatalf("rejected convergence changed config: service %#v saves %d", service.GetConfig(), store.saveCalls)
			}
			if statuses.listCalls != 0 || len(statuses.getCalls) != 0 || len(statuses.upsertCalls) != 0 || len(statuses.deleteCalls) != 0 {
				t.Fatalf("rejected convergence status calls = list %d get %v upsert %v delete %v", statuses.listCalls, statuses.getCalls, statuses.upsertCalls, statuses.deleteCalls)
			}
			if runtime.pathCalls != beforePathCalls || runtime.persistCalls != 0 || len(runtime.transactionCalls) != 0 {
				t.Fatalf("rejected convergence runtime calls = paths %d/%d persists %d transactions %v", runtime.pathCalls, beforePathCalls, runtime.persistCalls, runtime.transactionCalls)
			}
		})
	}
}

func TestSettingsServiceReplaceConfigReservesExactNameMatchesBeforePathMatches(t *testing.T) {
	for _, test := range []struct {
		name       string
		aliasFirst bool
	}{
		{name: "path match before exact name", aliasFirst: true},
		{name: "exact name before path match", aliasFirst: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir()
			completed := true
			configured := model.Base{Name: "configured", Path: path}
			setConfiguredGit(&configured)
			config := model.Config{Bases: []model.Base{configured}, CurrentBase: configured.Name, SetupCompleted: &completed}
			service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, path)
			alias := configured
			alias.Name = "alias"
			input := cloneConfig(config)
			if test.aliasFirst {
				input.Bases = []model.Base{alias, configured}
			} else {
				input.Bases = []model.Base{configured, alias}
			}
			beforePathCalls := runtime.pathCalls

			_, err := service.ReplaceConfig(input)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("ReplaceConfig() error = %v, want ErrInvalidConfig", err)
			}
			aliasIndex := 1
			if test.aliasFirst {
				aliasIndex = 0
			}
			assertFieldError(t, err, fmt.Sprintf("bases[%d].git_url", aliasIndex))
			if !reflect.DeepEqual(service.GetConfig(), config) || store.saveCalls != 0 {
				t.Fatalf("rejected replace changed config: config %#v saves %d", service.GetConfig(), store.saveCalls)
			}
			if statuses.listCalls != 0 || len(statuses.getCalls)+len(statuses.upsertCalls)+len(statuses.deleteCalls) != 0 {
				t.Fatalf("rejected replace status calls = list %d get %v upsert %v delete %v", statuses.listCalls, statuses.getCalls, statuses.upsertCalls, statuses.deleteCalls)
			}
			if runtime.pathCalls != beforePathCalls || runtime.persistCalls != 0 || len(runtime.transactionCalls) != 0 {
				t.Fatalf("rejected replace runtime calls = paths %d/%d persists %d transactions %v", runtime.pathCalls, beforePathCalls, runtime.persistCalls, runtime.transactionCalls)
			}
		})
	}
}

func TestSettingsServiceReplaceConfigGitPathMatchingRequiresBidirectionalUniqueness(t *testing.T) {
	for _, test := range []struct {
		name         string
		renamedFirst bool
	}{
		{name: "renamed before alias", renamedFirst: true},
		{name: "alias before renamed", renamedFirst: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir()
			completed := true
			current := model.Base{Name: "old", Path: path}
			setConfiguredGit(&current)
			config := model.Config{Bases: []model.Base{current}, CurrentBase: current.Name, SetupCompleted: &completed}
			service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, path)
			renamed := current
			renamed.Name = "renamed"
			alias := model.Base{Name: "alias", Path: path}
			input := cloneConfig(config)
			if test.renamedFirst {
				input.Bases = []model.Base{renamed, alias}
			} else {
				input.Bases = []model.Base{alias, renamed}
			}
			input.CurrentBase = renamed.Name
			beforePathCalls := runtime.pathCalls

			_, err := service.ReplaceConfig(input)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("ReplaceConfig() error = %v, want ErrInvalidConfig", err)
			}
			renamedIndex := 1
			if test.renamedFirst {
				renamedIndex = 0
			}
			assertFieldError(t, err, fmt.Sprintf("bases[%d].git_url", renamedIndex))
			if !reflect.DeepEqual(service.GetConfig(), config) || store.saveCalls != 0 {
				t.Fatalf("rejected replace changed config: config %#v saves %d", service.GetConfig(), store.saveCalls)
			}
			if statuses.listCalls != 0 || len(statuses.getCalls)+len(statuses.upsertCalls)+len(statuses.deleteCalls) != 0 {
				t.Fatalf("rejected replace status calls = list %d get %v upsert %v delete %v", statuses.listCalls, statuses.getCalls, statuses.upsertCalls, statuses.deleteCalls)
			}
			if runtime.pathCalls != beforePathCalls || runtime.persistCalls != 0 || len(runtime.transactionCalls) != 0 {
				t.Fatalf("rejected replace runtime calls = paths %d/%d persists %d transactions %v", runtime.pathCalls, beforePathCalls, runtime.persistCalls, runtime.transactionCalls)
			}
		})
	}
}

func TestSettingsServiceReplaceConfigGitPathMatchingAllowsAmbiguousZeroFieldAliases(t *testing.T) {
	for _, renamedFirst := range []bool{true, false} {
		name := "renamed before alias"
		if !renamedFirst {
			name = "alias before renamed"
		}
		t.Run(name, func(t *testing.T) {
			path := t.TempDir()
			completed := true
			config := model.Config{Bases: []model.Base{{Name: "old", Path: path}}, CurrentBase: "old", SetupCompleted: &completed}
			service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, path)
			renamed := model.Base{Name: "renamed", Path: path}
			alias := model.Base{Name: "alias", Path: path}
			input := cloneConfig(config)
			if renamedFirst {
				input.Bases = []model.Base{renamed, alias}
			} else {
				input.Bases = []model.Base{alias, renamed}
			}
			input.CurrentBase = renamed.Name

			response, err := service.ReplaceConfig(input)
			if err != nil {
				t.Fatalf("ReplaceConfig() error = %v", err)
			}
			if !reflect.DeepEqual(response.Config.Bases, input.Bases) || response.Config.CurrentBase != renamed.Name {
				t.Fatalf("ReplaceConfig() config = %#v, want %#v", response.Config, input)
			}
			if store.saveCalls != 1 || runtime.persistCalls != 1 || len(runtime.transactionCalls) != 0 {
				t.Fatalf("config/runtime calls = saves %d persists %d transactions %v", store.saveCalls, runtime.persistCalls, runtime.transactionCalls)
			}
			if len(statuses.upsertCalls)+len(statuses.deleteCalls) != 0 {
				t.Fatalf("zero-field aliases mutated statuses: upsert %v delete %v", statuses.upsertCalls, statuses.deleteCalls)
			}
		})
	}
}

func TestSettingsServiceAllowsNonGitDuplicateCanonicalPaths(t *testing.T) {
	path := t.TempDir()
	alias := filepath.Join(t.TempDir(), "base-alias")
	createSymlinkOrSkip(t, path, alias)
	completed := true
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: path}, {Name: "other", Path: t.TempDir()}},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	service, _, _, _, _ := newGitSettingsServiceForConfig(t, config, path)

	response, err := service.UpdateBase("other", model.BaseUpdateRequest{Name: "other", Path: alias})
	if err != nil {
		t.Fatalf("UpdateBase() error = %v", err)
	}
	if response.Config.Bases[1].Path != path {
		t.Fatalf("other path = %q, want %q", response.Config.Bases[1].Path, path)
	}
}

func TestSettingsServiceForeignLexicalGitStatusIsNotOwned(t *testing.T) {
	for _, test := range []struct {
		name     string
		samePath bool
		mutate   func(*SettingsService, string) error
		check    func(*testing.T, model.Config, string)
	}{
		{
			name:     "disable alias",
			samePath: true,
			mutate: func(service *SettingsService, _ string) error {
				_, err := service.DisableGit(context.Background(), "alias")
				return err
			},
			check: func(t *testing.T, config model.Config, sharedPath string) {
				t.Helper()
				if len(config.Bases) != 2 || config.Bases[1] != (model.Base{Name: "alias", Path: sharedPath}) {
					t.Fatalf("disabled alias config = %#v", config)
				}
			},
		},
		{
			name:     "rename alias",
			samePath: true,
			mutate: func(service *SettingsService, sharedPath string) error {
				_, err := service.UpdateBase("alias", model.BaseUpdateRequest{Name: "renamed", Path: sharedPath})
				return err
			},
			check: func(t *testing.T, config model.Config, sharedPath string) {
				t.Helper()
				if len(config.Bases) != 2 || config.Bases[1] != (model.Base{Name: "renamed", Path: sharedPath}) {
					t.Fatalf("renamed alias config = %#v", config)
				}
			},
		},
		{
			name: "move alias",
			mutate: func(service *SettingsService, destination string) error {
				_, err := service.UpdateBase("alias", model.BaseUpdateRequest{Name: "alias", Path: destination})
				return err
			},
			check: func(t *testing.T, config model.Config, destination string) {
				t.Helper()
				if len(config.Bases) != 2 || config.Bases[1] != (model.Base{Name: "alias", Path: destination}) {
					t.Fatalf("moved alias config = %#v", config)
				}
			},
		},
		{
			name: "forget alias",
			mutate: func(service *SettingsService, _ string) error {
				_, err := service.ForgetBase("alias")
				return err
			},
			check: func(t *testing.T, config model.Config, _ string) {
				t.Helper()
				if len(config.Bases) != 1 || config.Bases[0].Name != "owner" {
					t.Fatalf("forgotten alias config = %#v", config)
				}
			},
		},
		{
			name: "replace removes alias",
			mutate: func(service *SettingsService, _ string) error {
				input := service.GetConfig()
				input.Bases = input.Bases[:1]
				_, err := service.ReplaceConfig(input)
				return err
			},
			check: func(t *testing.T, config model.Config, _ string) {
				t.Helper()
				if len(config.Bases) != 1 || config.Bases[0].Name != "owner" {
					t.Fatalf("replace removal config = %#v", config)
				}
			},
		},
		{
			name: "replace moves alias",
			mutate: func(service *SettingsService, destination string) error {
				input := service.GetConfig()
				input.Bases[1].Path = destination
				_, err := service.ReplaceConfig(input)
				return err
			},
			check: func(t *testing.T, config model.Config, destination string) {
				t.Helper()
				if len(config.Bases) != 2 || config.Bases[1] != (model.Base{Name: "alias", Path: destination}) {
					t.Fatalf("replace move config = %#v", config)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sharedPath, destination := t.TempDir(), t.TempDir()
			completed := true
			owner := model.Base{Name: "owner", Path: sharedPath}
			setConfiguredGit(&owner)
			config := model.Config{
				Bases:          []model.Base{owner, {Name: "alias", Path: sharedPath}},
				CurrentBase:    owner.Name,
				SetupCompleted: &completed,
			}
			service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, sharedPath)
			foreign := model.GitStatus{
				Base: "owner", RepositoryPath: sharedPath, State: model.GitStateConflict,
				OperationID: "operation", Stage: "merge", Ahead: 3, Behind: 2,
				ConsecutiveFailures: 4, ChangedPaths: []string{"note.md"}, RemoteOID: "remote",
				Error: &model.APIError{Code: "conflict", Message: "safe"},
			}
			statuses.statuses[sharedPath] = cloneGitStatusForTest(foreign)

			targetPath := destination
			if test.samePath {
				targetPath = sharedPath
			}
			if err := test.mutate(service, targetPath); err != nil {
				t.Fatalf("mutation error = %v", err)
			}
			test.check(t, service.GetConfig(), targetPath)
			if len(statuses.statuses) != 1 || !reflect.DeepEqual(statuses.statuses[sharedPath], foreign) {
				t.Fatalf("foreign status changed = %#v, want %#v", statuses.statuses, foreign)
			}
			if !reflect.DeepEqual(statuses.getCalls, []string{sharedPath}) || len(statuses.upsertCalls) != 0 || len(statuses.deleteCalls) != 0 {
				t.Fatalf("foreign status calls = get %v upsert %v delete %v", statuses.getCalls, statuses.upsertCalls, statuses.deleteCalls)
			}
			if store.saveCalls != 1 || runtime.persistCalls != 1 || len(runtime.transactionCalls) != 0 {
				t.Fatalf("config/runtime calls = saves %d persists %d transactions %v", store.saveCalls, runtime.persistCalls, runtime.transactionCalls)
			}
		})
	}
}

func TestSettingsServiceGitConfiguredMoveWithMissingOwnStatusPreservesForeignLexicalRow(t *testing.T) {
	sharedPath, destination := t.TempDir(), t.TempDir()
	completed := true
	configured := model.Base{Name: "configured", Path: sharedPath}
	setConfiguredGit(&configured)
	config := model.Config{
		Bases:          []model.Base{{Name: "foreign", Path: sharedPath}, configured},
		CurrentBase:    "foreign",
		SetupCompleted: &completed,
	}
	service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, sharedPath)
	foreign := model.GitStatus{Base: "foreign", RepositoryPath: sharedPath, State: model.GitStatePaused, ChangedPaths: []string{"keep.md"}}
	statuses.statuses[sharedPath] = cloneGitStatusForTest(foreign)

	if _, err := service.UpdateBase("configured", model.BaseUpdateRequest{Name: "configured", Path: destination}); err != nil {
		t.Fatalf("UpdateBase() error = %v", err)
	}
	want := map[string]model.GitStatus{
		sharedPath:  foreign,
		destination: needsReconnectGitStatus("configured", destination),
	}
	if !reflect.DeepEqual(statuses.statuses, want) {
		t.Fatalf("moved statuses = %#v, want %#v", statuses.statuses, want)
	}
	if !reflect.DeepEqual(statuses.getCalls, []string{sharedPath, destination}) || len(statuses.deleteCalls) != 0 || len(statuses.upsertCalls) != 1 {
		t.Fatalf("status calls = get %v delete %v upsert %#v", statuses.getCalls, statuses.deleteCalls, statuses.upsertCalls)
	}
	if service.GetConfig().Bases[1].Path != destination || store.saveCalls != 1 || runtime.persistCalls != 1 || len(runtime.transactionCalls) != 0 {
		t.Fatalf("config/runtime state = config %#v saves %d persists %d transactions %v", service.GetConfig(), store.saveCalls, runtime.persistCalls, runtime.transactionCalls)
	}
}

func TestSettingsServiceGitStatusReconciliation(t *testing.T) {
	t.Run("same path rename preserves complete status", func(t *testing.T) {
		service, _, _, _, statuses, config := newGitSettingsService(t)
		status := model.GitStatus{Base: "work", RepositoryPath: config.Bases[0].Path, State: model.GitStateConflict, OperationID: "op", Stage: "merge", Ahead: 2, Behind: 1, ConsecutiveFailures: 4, ChangedPaths: []string{"a.md"}, RemoteOID: "oid", Error: &model.APIError{Code: "conflict", Message: "safe"}}
		statuses.statuses[status.RepositoryPath] = status
		if _, err := service.UpdateBase("work", model.BaseUpdateRequest{Name: "renamed", Path: config.Bases[0].Path}); err != nil {
			t.Fatal(err)
		}
		want := cloneGitStatusForTest(status)
		want.Base = "renamed"
		if !reflect.DeepEqual(statuses.statuses[status.RepositoryPath], want) {
			t.Fatalf("renamed status = %#v, want %#v", statuses.statuses[status.RepositoryPath], want)
		}
	})
	t.Run("configured path move resets status", func(t *testing.T) {
		service, _, _, _, statuses, config := newGitSettingsService(t)
		setConfiguredGit(&service.config.Bases[0])
		oldPath, newPath := config.Bases[0].Path, t.TempDir()
		statuses.statuses[oldPath] = model.GitStatus{Base: "work", RepositoryPath: oldPath, State: model.GitStateReady, ChangedPaths: []string{"old.md"}}
		if _, err := service.UpdateBase("work", model.BaseUpdateRequest{Name: "work", Path: newPath}); err != nil {
			t.Fatal(err)
		}
		want := model.GitStatus{Base: "work", RepositoryPath: newPath, State: model.GitStateNeedsReconnect, ChangedPaths: []string{}}
		if len(statuses.statuses) != 1 || !reflect.DeepEqual(statuses.statuses[newPath], want) {
			t.Fatalf("moved statuses = %#v, want %#v", statuses.statuses, want)
		}
	})
	t.Run("unconfigured move only removes owned stale old row", func(t *testing.T) {
		service, _, _, _, statuses, config := newGitSettingsService(t)
		oldPath, newPath := config.Bases[0].Path, t.TempDir()
		statuses.statuses[oldPath] = model.GitStatus{Base: "work", RepositoryPath: oldPath, State: model.GitStateReady}
		if _, err := service.UpdateBase("work", model.BaseUpdateRequest{Name: "work", Path: newPath}); err != nil {
			t.Fatal(err)
		}
		if len(statuses.statuses) != 0 || len(statuses.upsertCalls) != 0 {
			t.Fatalf("unconfigured move statuses = %#v upserts %#v", statuses.statuses, statuses.upsertCalls)
		}
	})
	t.Run("forget deletes status", func(t *testing.T) {
		service, _, _, _, statuses, _ := newGitSettingsService(t)
		otherPath := t.TempDir()
		service.config.Bases = append(service.config.Bases, model.Base{Name: "other", Path: otherPath})
		statuses.statuses[otherPath] = model.GitStatus{Base: "other", RepositoryPath: otherPath, State: model.GitStateReady}
		if _, err := service.ForgetBase("other"); err != nil {
			t.Fatal(err)
		}
		if _, ok := statuses.statuses[otherPath]; ok {
			t.Fatal("forgotten status remains")
		}
	})
	t.Run("forget cleans status after stored path disappears", func(t *testing.T) {
		activePath := t.TempDir()
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "forgotten-link")
		createSymlinkOrSkip(t, target, link)
		config := configuredGitTestConfig(activePath, link)
		service, store, runtime, validator, statuses := newGitSettingsServiceForConfig(t, config, activePath)
		configureTestBaseGit(t, service, validator, "other")
		if _, ok := statuses.statuses[target]; !ok || len(statuses.statuses) != 1 {
			t.Fatalf("ConfigureGit statuses = %#v, want canonical target %q", statuses.statuses, target)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}

		if _, err := service.ForgetBase("other"); err != nil {
			t.Fatalf("ForgetBase() error = %v", err)
		}
		if len(statuses.statuses) != 0 || store.saveCalls != 2 || runtime.persistCalls != 2 || len(runtime.transactionCalls) != 0 || statuses.listCalls != 1 {
			t.Fatalf("forget effects = statuses %#v saves %d persists %d transactions %v", statuses.statuses, store.saveCalls, runtime.persistCalls, runtime.transactionCalls)
		}
	})
}

func TestSettingsServiceCanonicalGitStatusIdentityAfterSymlinkDisappears(t *testing.T) {
	t.Run("UpdateBase same target rename updates canonical row", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "base-link")
		createSymlinkOrSkip(t, target, link)
		completed := true
		config := model.Config{Bases: []model.Base{{Name: "work", Path: link}}, CurrentBase: "work", SetupCompleted: &completed}
		service, store, runtime, validator, statuses := newGitSettingsServiceForConfig(t, config, link)
		configureTestBaseGit(t, service, validator, "work")
		status := statuses.statuses[target]
		status.State = model.GitStateConflict
		status.OperationID = "op"
		status.ChangedPaths = []string{"note.md"}
		statuses.statuses[target] = status
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}

		if _, err := service.UpdateBase("work", model.BaseUpdateRequest{Name: "renamed", Path: target}); err != nil {
			t.Fatalf("UpdateBase() error = %v", err)
		}
		want := cloneGitStatusForTest(status)
		want.Base = "renamed"
		if len(statuses.statuses) != 1 || !reflect.DeepEqual(statuses.statuses[target], want) {
			t.Fatalf("renamed statuses = %#v, want %#v", statuses.statuses, want)
		}
		if _, orphaned := statuses.statuses[filepath.Clean(link)]; orphaned || statuses.listCalls != 1 || store.saveCalls != 2 || len(runtime.transactionCalls) != 1 {
			t.Fatalf("rename effects = orphan %t lists %d saves %d transactions %v", orphaned, statuses.listCalls, store.saveCalls, runtime.transactionCalls)
		}
	})

	t.Run("ReplaceConfig removal deletes canonical row", func(t *testing.T) {
		activePath := t.TempDir()
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "other-link")
		createSymlinkOrSkip(t, target, link)
		config := configuredGitTestConfig(activePath, link)
		service, store, runtime, validator, statuses := newGitSettingsServiceForConfig(t, config, activePath)
		configureTestBaseGit(t, service, validator, "other")
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		input := service.GetConfig()
		input.Bases = input.Bases[:1]

		if _, err := service.ReplaceConfig(input); err != nil {
			t.Fatalf("ReplaceConfig() error = %v", err)
		}
		if len(statuses.statuses) != 0 || statuses.listCalls != 1 || store.saveCalls != 2 || runtime.persistCalls != 2 || len(runtime.transactionCalls) != 0 {
			t.Fatalf("replace effects = statuses %#v lists %d saves %d persists %d transactions %v", statuses.statuses, statuses.listCalls, store.saveCalls, runtime.persistCalls, runtime.transactionCalls)
		}
		if _, orphaned := statuses.statuses[filepath.Clean(link)]; orphaned {
			t.Fatal("ReplaceConfig left lexical status orphan")
		}
	})
}

func TestSettingsServiceGitStatusMutationsRejectAmbiguousBaseBeforeLexicalRow(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(*SettingsService, string) error
	}{
		{name: "disable", call: func(service *SettingsService, _ string) error {
			_, err := service.DisableGit(context.Background(), "work")
			return err
		}},
		{name: "update", call: func(service *SettingsService, canonicalPath string) error {
			_, err := service.UpdateBase("work", model.BaseUpdateRequest{Name: "renamed", Path: canonicalPath})
			return err
		}},
		{name: "forget", call: func(service *SettingsService, _ string) error {
			_, err := service.ForgetBase("work")
			return err
		}},
		{name: "replace", call: func(service *SettingsService, _ string) error {
			input := service.GetConfig()
			input.Bases = input.Bases[:1]
			_, err := service.ReplaceConfig(input)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			activePath := t.TempDir()
			canonicalPath := t.TempDir()
			lexicalPath := filepath.Join(t.TempDir(), "base-link")
			createSymlinkOrSkip(t, canonicalPath, lexicalPath)
			completed := true
			work := model.Base{Name: "work", Path: lexicalPath, GitURL: "work.git", GitBranch: "main"}
			config := model.Config{
				Bases:          []model.Base{{Name: "active", Path: activePath}, work},
				CurrentBase:    "active",
				SetupCompleted: &completed,
			}
			service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, activePath)
			statuses.statuses[lexicalPath] = model.GitStatus{Base: "work", RepositoryPath: lexicalPath, State: model.GitStatePaused, ChangedPaths: []string{}}
			statuses.statuses[canonicalPath] = model.GitStatus{Base: "work", RepositoryPath: canonicalPath, State: model.GitStateReady, ChangedPaths: []string{}}
			beforeConfig := service.GetConfig()
			beforeStatuses := cloneGitStatusesForTest(statuses.statuses)

			err := test.call(service, canonicalPath)
			if !errors.Is(err, errAmbiguousGitStatusIdentity) {
				t.Fatalf("mutation error = %v, want %v", err, errAmbiguousGitStatusIdentity)
			}
			if !reflect.DeepEqual(service.GetConfig(), beforeConfig) || !reflect.DeepEqual(statuses.statuses, beforeStatuses) {
				t.Fatalf("ambiguous identity mutated state: config %#v statuses %#v", service.GetConfig(), statuses.statuses)
			}
			if store.saveCalls != 0 || runtime.persistCalls != 0 || len(runtime.transactionCalls) != 0 || len(statuses.upsertCalls) != 0 || len(statuses.deleteCalls) != 0 {
				t.Fatalf("ambiguous identity calls = saves %d persists %d transactions %v upserts %v deletes %v", store.saveCalls, runtime.persistCalls, runtime.transactionCalls, statuses.upsertCalls, statuses.deleteCalls)
			}
			if statuses.listCalls != 1 || len(statuses.getCalls) != 0 {
				t.Fatalf("identity reads = list %d get %v, want list first and no get", statuses.listCalls, statuses.getCalls)
			}
		})
	}
}

func TestSettingsServiceGitStatusListFailureAbortsBeforeMutation(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "base-link")
	createSymlinkOrSkip(t, target, link)
	completed := true
	config := model.Config{Bases: []model.Base{{Name: "work", Path: link}}, CurrentBase: "work", SetupCompleted: &completed}
	service, store, runtime, validator, statuses := newGitSettingsServiceForConfig(t, config, link)
	configureTestBaseGit(t, service, validator, "work")
	statuses.listErr = errors.New("list failed with private database path")
	beforeConfig := service.GetConfig()
	beforeStatuses := cloneGitStatusesForTest(statuses.statuses)
	beforeSaves, beforePersists := store.saveCalls, runtime.persistCalls
	beforeGets := len(statuses.getCalls)

	_, err := service.DisableGit(context.Background(), "work")
	if err == nil || !strings.Contains(err.Error(), "list failed with private database path") {
		t.Fatalf("DisableGit() error = %v, want list failure", err)
	}
	if strings.Contains(err.Error(), target) || strings.Contains(err.Error(), link) {
		t.Fatalf("DisableGit() error leaked path: %q", err)
	}
	if !reflect.DeepEqual(service.GetConfig(), beforeConfig) || !reflect.DeepEqual(statuses.statuses, beforeStatuses) || store.saveCalls != beforeSaves || runtime.persistCalls != beforePersists || len(statuses.deleteCalls) != 0 {
		t.Fatalf("list failure mutated state: config %#v statuses %#v saves %d persists %d deletes %v", service.GetConfig(), statuses.statuses, store.saveCalls, runtime.persistCalls, statuses.deleteCalls)
	}
	if statuses.listCalls != 1 || len(statuses.getCalls) != beforeGets {
		t.Fatalf("identity reads = list %d get %v, want list before no additional get", statuses.listCalls, statuses.getCalls)
	}
}

func TestSettingsServiceReplaceConfigReconcilesStatuses(t *testing.T) {
	service, _, _, _, statuses, config := newGitSettingsService(t)
	setConfiguredGit(&service.config.Bases[0])
	renameStatus := model.GitStatus{Base: "work", RepositoryPath: config.Bases[0].Path, State: model.GitStateReady, Ahead: 2, ChangedPaths: []string{"a.md"}}
	statuses.statuses[renameStatus.RepositoryPath] = renameStatus
	removedPath := t.TempDir()
	service.config.Bases = append(service.config.Bases, model.Base{Name: "removed", Path: removedPath})
	statuses.statuses[removedPath] = model.GitStatus{Base: "removed", RepositoryPath: removedPath, State: model.GitStatePaused, ChangedPaths: []string{}}
	input := cloneConfig(service.config)
	input.Bases = input.Bases[:1]
	input.Bases[0].Name, input.CurrentBase = "renamed", "renamed"

	if _, err := service.ReplaceConfig(input); err != nil {
		t.Fatalf("ReplaceConfig() error = %v", err)
	}
	want := renameStatus
	want.Base = "renamed"
	if len(statuses.statuses) != 1 || !reflect.DeepEqual(statuses.statuses[renameStatus.RepositoryPath], want) {
		t.Fatalf("reconciled statuses = %#v, want %#v", statuses.statuses, want)
	}
}

func TestSettingsServiceReplaceConfigFinalGitStatusOwnership(t *testing.T) {
	t.Run("configured move onto removed base path wins deletion", func(t *testing.T) {
		oldPath, destination := t.TempDir(), t.TempDir()
		config := configuredGitTestConfig(oldPath, destination)
		service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, oldPath)
		oldStatus := model.GitStatus{Base: "active", RepositoryPath: oldPath, State: model.GitStateReady, ChangedPaths: []string{"old.md"}}
		destinationStatus := model.GitStatus{Base: "removed", RepositoryPath: destination, State: model.GitStatePaused, ChangedPaths: []string{"removed.md"}}
		statuses.statuses[oldPath], statuses.statuses[destination] = oldStatus, destinationStatus
		input := cloneConfig(config)
		input.Bases = input.Bases[:1]
		input.Bases[0].Path = destination

		if _, err := service.ReplaceConfig(input); err != nil {
			t.Fatalf("ReplaceConfig() error = %v", err)
		}
		want := needsReconnectGitStatus("active", destination)
		if len(statuses.statuses) != 1 || !reflect.DeepEqual(statuses.statuses[destination], want) {
			t.Fatalf("final statuses = %#v, want destination %#v", statuses.statuses, want)
		}
		if store.saveCalls != 1 || runtime.persistCalls != 1 || !reflect.DeepEqual(runtime.transactionCalls, []string{destination}) {
			t.Fatalf("config/runtime calls = saves %d persists %d transactions %v", store.saveCalls, runtime.persistCalls, runtime.transactionCalls)
		}
	})

	t.Run("configured path swap keeps both final destinations", func(t *testing.T) {
		firstPath, secondPath := t.TempDir(), t.TempDir()
		config := configuredGitTestConfig(firstPath, secondPath)
		setConfiguredGit(&config.Bases[1])
		config.Bases[1].GitURL = "other.git"
		service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, firstPath)
		statuses.statuses[firstPath] = model.GitStatus{Base: "active", RepositoryPath: firstPath, State: model.GitStateReady, ChangedPaths: []string{"first.md"}}
		statuses.statuses[secondPath] = model.GitStatus{Base: "other", RepositoryPath: secondPath, State: model.GitStateConflict, ChangedPaths: []string{"second.md"}}
		input := cloneConfig(config)
		input.Bases[0].Path, input.Bases[1].Path = secondPath, firstPath

		if _, err := service.ReplaceConfig(input); err != nil {
			t.Fatalf("ReplaceConfig() error = %v", err)
		}
		want := map[string]model.GitStatus{
			firstPath:  needsReconnectGitStatus("other", firstPath),
			secondPath: needsReconnectGitStatus("active", secondPath),
		}
		if !reflect.DeepEqual(statuses.statuses, want) {
			t.Fatalf("swapped statuses = %#v, want %#v", statuses.statuses, want)
		}
		if store.saveCalls != 1 || runtime.persistCalls != 1 || len(runtime.transactionCalls) != 1 {
			t.Fatalf("config/runtime calls = saves %d persists %d transactions %v", store.saveCalls, runtime.persistCalls, runtime.transactionCalls)
		}
	})
}

func TestSettingsServiceGitStatusFailuresCompensateBeforeImages(t *testing.T) {
	t.Run("forward failure happens before config save", func(t *testing.T) {
		service, store, _, validator, statuses, original := newGitSettingsService(t)
		normalized := model.GitConfigRequest{GitURL: "work.git", GitBranch: "main"}
		validator.normalized = &normalized
		forwardErr := errors.New("status unavailable")
		statuses.upsertErrs = []error{forwardErr}
		_, err := service.ConfigureGit(context.Background(), "work", normalized)
		if !errors.Is(err, forwardErr) {
			t.Fatalf("ConfigureGit() error = %v, want %v", err, forwardErr)
		}
		if store.saveCalls != 0 || !reflect.DeepEqual(service.GetConfig(), original) {
			t.Fatalf("forward status failure saved config: saves %d config %#v", store.saveCalls, service.GetConfig())
		}
	})
	t.Run("config failure restores exact existing status", func(t *testing.T) {
		service, store, _, validator, statuses, _ := newGitSettingsService(t)
		before := model.GitStatus{Base: "old", RepositoryPath: service.config.Bases[0].Path, State: model.GitStateError, OperationID: "op", Ahead: 7, ChangedPaths: []string{"secret.md"}, Error: &model.APIError{Code: "safe", Message: "safe"}}
		statuses.statuses[before.RepositoryPath] = before
		normalized := model.GitConfigRequest{GitURL: "work.git", GitBranch: "main"}
		validator.normalized = &normalized
		store.saveErr = errors.New("disk full")
		_, err := service.ConfigureGit(context.Background(), "work", normalized)
		if !errors.Is(err, store.saveErr) {
			t.Fatalf("ConfigureGit() error = %v, want %v", err, store.saveErr)
		}
		if !reflect.DeepEqual(statuses.statuses[before.RepositoryPath], before) || len(statuses.upsertCalls) != 2 {
			t.Fatalf("restored status = %#v calls %#v, want %#v", statuses.statuses[before.RepositoryPath], statuses.upsertCalls, before)
		}
	})
	t.Run("config failure deletes status absent before", func(t *testing.T) {
		service, store, _, validator, statuses, _ := newGitSettingsService(t)
		normalized := model.GitConfigRequest{GitURL: "work.git", GitBranch: "main"}
		validator.normalized = &normalized
		store.saveErr = errors.New("disk full")
		_, _ = service.ConfigureGit(context.Background(), "work", normalized)
		if len(statuses.statuses) != 0 || len(statuses.deleteCalls) != 1 {
			t.Fatalf("compensated statuses = %#v deletes %v", statuses.statuses, statuses.deleteCalls)
		}
	})
}

func TestSettingsServiceGitStatusCompensationSurvivesRequestCancellation(t *testing.T) {
	oldPath, newPath := t.TempDir(), t.TempDir()
	config := configuredGitTestConfig(oldPath, t.TempDir())
	service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, oldPath)
	before := map[string]model.GitStatus{
		oldPath: {Base: "active", RepositoryPath: oldPath, State: model.GitStateReady, ChangedPaths: []string{"old.md"}},
	}
	statuses.statuses = cloneGitStatusesForTest(before)
	ctx, cancel := context.WithCancel(context.Background())
	restoreContextErrs := make([]error, 0, 2)
	canceled := false
	statuses.deleteHook = func(writeCtx context.Context, path string) error {
		if path == oldPath && !canceled {
			canceled = true
			cancel()
			return nil
		}
		restoreContextErrs = append(restoreContextErrs, writeCtx.Err())
		return writeCtx.Err()
	}
	statuses.upsertHook = func(writeCtx context.Context, status model.GitStatus) error {
		if status.RepositoryPath == newPath {
			return writeCtx.Err()
		}
		restoreContextErrs = append(restoreContextErrs, writeCtx.Err())
		return writeCtx.Err()
	}

	changes, err := service.prepareUpdateBaseGitStatuses(ctx, config.Bases[0], model.Base{
		Name:      "active",
		Path:      newPath,
		GitURL:    config.Bases[0].GitURL,
		GitBranch: config.Bases[0].GitBranch,
	})
	if err != nil {
		t.Fatalf("prepareUpdateBaseGitStatuses() error = %v", err)
	}
	next := cloneConfig(config)
	next.Bases[0].Path = newPath
	err = service.applyConfigWithGitStatusesLocked(ctx, next, newPath, changes)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("applyConfigWithGitStatusesLocked() error = %v, want context cancellation", err)
	}
	if service.degraded != nil {
		t.Fatalf("service degraded = %v", service.degraded)
	}
	if !reflect.DeepEqual(restoreContextErrs, []error{nil, nil}) {
		t.Fatalf("restore context errors = %v, want live contexts", restoreContextErrs)
	}
	assertGitMutationRollback(t, service, store, runtime, statuses, config, before, oldPath, 0, 0, 0)
}

func TestSettingsServiceGitStatusCompensationAcceptsNilContext(t *testing.T) {
	service, _, _, _, statuses, _ := newGitSettingsService(t)
	path := t.TempDir()
	operationErr := errors.New("operation failed")

	err := service.restoreGitStatusesLocked(nil, []gitStatusChange{{path: path}}, operationErr)
	if !errors.Is(err, operationErr) {
		t.Fatalf("restoreGitStatusesLocked() error = %v, want %v", err, operationErr)
	}
	if !reflect.DeepEqual(statuses.deleteCalls, []string{path}) {
		t.Fatalf("Delete calls = %v, want %q", statuses.deleteCalls, path)
	}
}

func TestSettingsServiceGitCompensationAcrossMutators(t *testing.T) {
	t.Run("partial configured move failure restores every status before runtime", func(t *testing.T) {
		oldPath, newPath := t.TempDir(), t.TempDir()
		config := configuredGitTestConfig(oldPath, t.TempDir())
		service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, oldPath)
		before := map[string]model.GitStatus{
			oldPath: {Base: "active", RepositoryPath: oldPath, State: model.GitStateReady, ChangedPaths: []string{"old.md"}},
			newPath: {Base: "destination", RepositoryPath: newPath, State: model.GitStateConflict, ChangedPaths: []string{"destination.md"}},
		}
		statuses.statuses = cloneGitStatusesForTest(before)
		forwardErr := errors.New("destination upsert failed")
		statuses.upsertErrs = []error{forwardErr}

		_, err := service.UpdateBase("active", model.BaseUpdateRequest{Name: "active", Path: newPath})
		if !errors.Is(err, forwardErr) {
			t.Fatalf("UpdateBase() error = %v, want %v", err, forwardErr)
		}
		assertGitMutationRollback(t, service, store, runtime, statuses, config, before, oldPath, 0, 0, 0)
	})

	t.Run("disable save failure restores deleted status", func(t *testing.T) {
		path := t.TempDir()
		config := configuredGitTestConfig(path, t.TempDir())
		service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, path)
		before := map[string]model.GitStatus{path: {Base: "active", RepositoryPath: path, State: model.GitStateReady, ChangedPaths: []string{"note.md"}}}
		statuses.statuses = cloneGitStatusesForTest(before)
		store.saveErr = errors.New("save failed")

		_, err := service.DisableGit(context.Background(), "active")
		if !errors.Is(err, store.saveErr) {
			t.Fatalf("DisableGit() error = %v, want %v", err, store.saveErr)
		}
		assertGitMutationRollback(t, service, store, runtime, statuses, config, before, path, 1, 1, 0)
	})

	t.Run("same path rename save failure restores complete status", func(t *testing.T) {
		path := t.TempDir()
		config := configuredGitTestConfig(path, t.TempDir())
		service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, path)
		before := map[string]model.GitStatus{path: {Base: "active", RepositoryPath: path, State: model.GitStateConflict, OperationID: "op", Ahead: 3, ChangedPaths: []string{"note.md"}}}
		statuses.statuses = cloneGitStatusesForTest(before)
		store.saveErr = errors.New("save failed")

		_, err := service.UpdateBase("active", model.BaseUpdateRequest{Name: "renamed", Path: path})
		if !errors.Is(err, store.saveErr) {
			t.Fatalf("UpdateBase() error = %v, want %v", err, store.saveErr)
		}
		assertGitMutationRollback(t, service, store, runtime, statuses, config, before, path, 1, 1, 0)
	})

	t.Run("configured move runtime failure restores source and destination", func(t *testing.T) {
		oldPath, newPath := t.TempDir(), t.TempDir()
		config := configuredGitTestConfig(oldPath, t.TempDir())
		service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, oldPath)
		before := map[string]model.GitStatus{
			oldPath: {Base: "active", RepositoryPath: oldPath, State: model.GitStateReady, ChangedPaths: []string{"old.md"}},
			newPath: {Base: "destination", RepositoryPath: newPath, State: model.GitStatePaused, ChangedPaths: []string{"keep.md"}},
		}
		statuses.statuses = cloneGitStatusesForTest(before)
		runtime.switchErr = errors.New("runtime switch failed")

		_, err := service.UpdateBase("active", model.BaseUpdateRequest{Name: "active", Path: newPath})
		if !errors.Is(err, runtime.switchErr) {
			t.Fatalf("UpdateBase() error = %v, want %v", err, runtime.switchErr)
		}
		assertGitMutationRollback(t, service, store, runtime, statuses, config, before, oldPath, 0, 1, 1)
	})

	t.Run("forget save failure restores status", func(t *testing.T) {
		activePath, forgottenPath := t.TempDir(), t.TempDir()
		config := configuredGitTestConfig(activePath, forgottenPath)
		service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, activePath)
		before := map[string]model.GitStatus{forgottenPath: {Base: "other", RepositoryPath: forgottenPath, State: model.GitStateError, ChangedPaths: []string{"other.md"}}}
		statuses.statuses = cloneGitStatusesForTest(before)
		store.saveErr = errors.New("save failed")

		_, err := service.ForgetBase("other")
		if !errors.Is(err, store.saveErr) {
			t.Fatalf("ForgetBase() error = %v, want %v", err, store.saveErr)
		}
		assertGitMutationRollback(t, service, store, runtime, statuses, config, before, activePath, 1, 1, 0)
	})

	t.Run("replace runtime failure restores reused destination", func(t *testing.T) {
		oldPath, destination := t.TempDir(), t.TempDir()
		config := configuredGitTestConfig(oldPath, destination)
		service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, oldPath)
		before := map[string]model.GitStatus{
			oldPath:     {Base: "active", RepositoryPath: oldPath, State: model.GitStateReady, ChangedPaths: []string{"old.md"}},
			destination: {Base: "other", RepositoryPath: destination, State: model.GitStatePaused, ChangedPaths: []string{"other.md"}},
		}
		statuses.statuses = cloneGitStatusesForTest(before)
		runtime.switchErr = errors.New("runtime switch failed")
		input := cloneConfig(config)
		input.Bases = input.Bases[:1]
		input.Bases[0].Path = destination

		_, err := service.ReplaceConfig(input)
		if !errors.Is(err, runtime.switchErr) {
			t.Fatalf("ReplaceConfig() error = %v, want %v", err, runtime.switchErr)
		}
		assertGitMutationRollback(t, service, store, runtime, statuses, config, before, oldPath, 0, 1, 1)
	})

	t.Run("replace save failure restores reused destination", func(t *testing.T) {
		oldPath, destination := t.TempDir(), t.TempDir()
		config := configuredGitTestConfig(oldPath, destination)
		service, store, runtime, _, statuses := newGitSettingsServiceForConfig(t, config, oldPath)
		before := map[string]model.GitStatus{
			oldPath:     {Base: "active", RepositoryPath: oldPath, State: model.GitStateReady, ChangedPaths: []string{"old.md"}},
			destination: {Base: "other", RepositoryPath: destination, State: model.GitStatePaused, ChangedPaths: []string{"other.md"}},
		}
		statuses.statuses = cloneGitStatusesForTest(before)
		store.saveErr = errors.New("save failed")
		input := cloneConfig(config)
		input.Bases = input.Bases[:1]
		input.Bases[0].Path = destination

		_, err := service.ReplaceConfig(input)
		if !errors.Is(err, store.saveErr) {
			t.Fatalf("ReplaceConfig() error = %v, want %v", err, store.saveErr)
		}
		assertGitMutationRollback(t, service, store, runtime, statuses, config, before, oldPath, 1, 1, 1)
	})
}

func TestSettingsServiceGitRestoreFailureDegradesWithoutLeakingDetails(t *testing.T) {
	service, store, runtime, validator, statuses, _ := newGitSettingsService(t)
	normalized := model.GitConfigRequest{GitURL: "https://user:secret@example.com/repo.git", GitBranch: "main"}
	validator.normalized = &normalized
	store.saveErr = errors.New("config write failed at /private/config")
	restoreErr := errors.New("restore failed at /private/status")
	statuses.deleteErrs = []error{restoreErr}
	var logs bytes.Buffer
	service.logger.SetOutput(&logs)

	_, err := service.ConfigureGit(context.Background(), "work", normalized)
	if !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, store.saveErr) || !errors.Is(err, restoreErr) {
		t.Fatalf("ConfigureGit() error = %v, want rollback, operation, and restore errors", err)
	}
	if strings.Contains(logs.String(), "private") || strings.Contains(logs.String(), "secret") || logs.String() == "" {
		t.Fatalf("rollback log leaked details or was empty: %q", logs.String())
	}
	validatorCalls := validator.calls
	statusCalls := len(statuses.getCalls) + len(statuses.upsertCalls) + len(statuses.deleteCalls)
	saveCalls := store.saveCalls
	persistCalls := runtime.persistCalls
	transactionCalls := len(runtime.transactionCalls)
	if _, laterErr := service.DisableGit(context.Background(), "work"); !errors.Is(laterErr, ErrRollbackFailed) {
		t.Fatalf("later DisableGit() error = %v, want ErrRollbackFailed", laterErr)
	}
	if validator.calls != validatorCalls || len(statuses.getCalls)+len(statuses.upsertCalls)+len(statuses.deleteCalls) != statusCalls || store.saveCalls != saveCalls || runtime.persistCalls != persistCalls || len(runtime.transactionCalls) != transactionCalls {
		t.Fatal("degraded mutation called dependencies")
	}
}

func configuredGitTestConfig(activePath, otherPath string) model.Config {
	completed := true
	active := model.Base{Name: "active", Path: activePath}
	setConfiguredGit(&active)
	return model.Config{
		Bases:          []model.Base{active, {Name: "other", Path: otherPath}},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
}

func configureTestBaseGit(t *testing.T, service *SettingsService, validator *fakeGitConfigValidator, name string) {
	t.Helper()
	normalized := model.GitConfigRequest{GitURL: name + ".git", GitBranch: "main"}
	validator.normalized = &normalized
	if _, err := service.ConfigureGit(context.Background(), name, normalized); err != nil {
		t.Fatalf("ConfigureGit(%q) error = %v", name, err)
	}
}

func newGitSettingsServiceForConfig(
	t *testing.T,
	config model.Config,
	runtimePath string,
) (*SettingsService, *fakeConfigStore, *fakeBaseRuntime, *fakeGitConfigValidator, *fakeGitStatusStore) {
	t.Helper()
	store := &fakeConfigStore{config: ptrConfig(config)}
	runtime := &fakeBaseRuntime{path: runtimePath}
	validator := &fakeGitConfigValidator{}
	statuses := &fakeGitStatusStore{statuses: make(map[string]model.GitStatus)}
	service, err := NewSettingsServiceWithGit(store, runtime, NewBaseOperationCoordinator(), "", nil, validator, statuses)
	if err != nil {
		t.Fatalf("NewSettingsServiceWithGit() error = %v", err)
	}
	return service, store, runtime, validator, statuses
}

func cloneGitStatusesForTest(statuses map[string]model.GitStatus) map[string]model.GitStatus {
	cloned := make(map[string]model.GitStatus, len(statuses))
	for path, status := range statuses {
		cloned[path] = cloneGitStatusForTest(status)
	}
	return cloned
}

func assertGitMutationRollback(
	t *testing.T,
	service *SettingsService,
	store *fakeConfigStore,
	runtime *fakeBaseRuntime,
	statuses *fakeGitStatusStore,
	config model.Config,
	wantStatuses map[string]model.GitStatus,
	runtimePath string,
	saveCalls int,
	persistCalls int,
	transactionCalls int,
) {
	t.Helper()
	if !reflect.DeepEqual(statuses.statuses, wantStatuses) {
		t.Errorf("statuses = %#v, want restored %#v", statuses.statuses, wantStatuses)
	}
	if !reflect.DeepEqual(service.GetConfig(), config) || store.config == nil || !reflect.DeepEqual(*store.config, config) {
		t.Errorf("configs = service %#v store %#v, want %#v", service.GetConfig(), store.config, config)
	}
	if runtime.path != runtimePath || store.saveCalls != saveCalls || runtime.persistCalls != persistCalls || len(runtime.transactionCalls) != transactionCalls {
		t.Errorf("runtime/config calls = path %q saves %d persists %d transactions %v", runtime.path, store.saveCalls, runtime.persistCalls, runtime.transactionCalls)
	}
}

func newGitSettingsService(t *testing.T) (*SettingsService, *fakeConfigStore, *fakeBaseRuntime, *fakeGitConfigValidator, *fakeGitStatusStore, model.Config) {
	t.Helper()
	completed := true
	basePath := t.TempDir()
	config := model.Config{Bases: []model.Base{{Name: "work", Path: basePath}}, CurrentBase: "work", SetupCompleted: &completed}
	store := &fakeConfigStore{config: &config}
	runtime := &fakeBaseRuntime{path: basePath}
	validator := &fakeGitConfigValidator{}
	statuses := &fakeGitStatusStore{statuses: make(map[string]model.GitStatus)}
	service, err := NewSettingsServiceWithGit(store, runtime, NewBaseOperationCoordinator(), "", nil, validator, statuses)
	if err != nil {
		t.Fatalf("NewSettingsServiceWithGit() error = %v", err)
	}
	return service, store, runtime, validator, statuses, cloneConfig(config)
}

func setConfiguredGit(base *model.Base) {
	base.GitURL = "work.git"
	base.GitBranch = "main"
	base.AutoSync = true
	base.AutoSyncIntervalMinutes = 15
	base.GitCommitMessageTemplate = "sync"
}

func ptrConfig(config model.Config) *model.Config {
	cloned := cloneConfig(config)
	return &cloned
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
	service, err := NewSettingsService(store, runtime, NewBaseOperationCoordinator(), "", nil)
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
	service, err := NewSettingsService(store, runtime, NewBaseOperationCoordinator(), "", nil)
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
