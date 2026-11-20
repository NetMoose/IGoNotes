package service

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"IGoNotes/internal/model"
)

func TestPrivateStorageAccessorCannotDisableExistingOrNewSettings(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	// This assignment uses only exported types/APIs and is legal outside service.
	*f.store.PrivateStoragePolicy() = PrivateStoragePolicy{}
	if err := f.settings.privateStorage.ValidateBasePath(f.data); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("existing settings policy disabled: %v", err)
	}
	settings, err := NewSettingsService(f.store, f.runtime, NewBaseOperationCoordinator(), "", nil)
	if err != nil {
		t.Fatalf("accessor mutation corrupted constructor: %v", err)
	}
	if err := settings.privateStorage.ValidateBasePath(f.data); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("new settings policy disabled: %v", err)
	}
	// A nonzero replacement must not silently redirect the reservations either.
	other, err := NewPrivateStoragePolicy(t.TempDir(), filepath.Join(t.TempDir(), "metadata.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	*f.store.PrivateStoragePolicy() = *other
	if err := f.store.PrivateStoragePolicy().ValidateBasePath(f.staging); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("store reservations redirected: %v", err)
	}
}

type livePrivatePolicyStore struct {
	*ConfigService
	policy *PrivateStoragePolicy
}

func (s *livePrivatePolicyStore) PrivateStoragePolicy() *PrivateStoragePolicy { return s.policy }

func TestPrivateStorageSettingsSnapshotsProviderPolicy(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	p := *f.store.PrivateStoragePolicy()
	provider := &livePrivatePolicyStore{ConfigService: f.store, policy: &p}
	settings, err := NewSettingsService(provider, f.runtime, NewBaseOperationCoordinator(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewPrivateStoragePolicy(t.TempDir(), filepath.Join(t.TempDir(), "metadata.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	*provider.policy = *other
	if err := settings.privateStorage.ValidateBasePath(f.staging); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("provider mutation changed settings policy: %v", err)
	}
}

func TestPrivateStorageZeroValueFailsClosed(t *testing.T) {
	p := &PrivateStoragePolicy{}
	if err := p.ValidateBasePath(t.TempDir()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("zero policy accepted path: %v", err)
	}
	if err := p.validateConfig(model.Config{}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("zero policy accepted empty startup snapshot: %v", err)
	}
	// Nil still represents an absent provider on pathless ConfigStore fakes;
	// actual ConfigService constructors always supply a nonzero config boundary.
	if err := (*PrivateStoragePolicy)(nil).validateConfig(model.Config{}); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateStorageAccessorWritesConcurrentWithValidation(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			*f.store.PrivateStoragePolicy() = PrivateStoragePolicy{}
		}
	}()
	defer wg.Wait()
	for i := 0; i < 100; i++ {
		if err := f.settings.privateStorage.ValidateBasePath(f.staging); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("settings validation disabled: %v", err)
		}
		if err := f.store.PrivateStoragePolicy().ValidateBasePath(f.staging); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("store validation disabled: %v", err)
		}
	}
}

func TestPrivateStorageConstructorArgumentRemainsDetached(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	arg := f.store.PrivateStoragePolicy()
	store := NewConfigService(f.store.configPath, arg)
	*arg = PrivateStoragePolicy{}
	if err := store.PrivateStoragePolicy().ValidateBasePath(f.staging); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("constructor argument changed store: %v", err)
	}
}

func TestPrivateStorageZeroProviderRejectsBeforeConstructorMigration(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	if err := f.store.Save(&model.Config{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.store.configPath)
	if err != nil {
		t.Fatal(err)
	}
	provider := &livePrivatePolicyStore{ConfigService: f.store, policy: &PrivateStoragePolicy{}}
	if _, err := NewSettingsService(provider, f.runtime, NewBaseOperationCoordinator(), "", nil); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("zero provider constructor = %v", err)
	}
	after, err := os.ReadFile(f.store.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || f.runtime.persistCalls != 0 {
		t.Fatal("invalid provider reached migration")
	}
}

func TestPrivateStorageFullLayoutConstructorRequiresAllDestinations(t *testing.T) {
	root := t.TempDir()
	for missing := 0; missing < 3; missing++ {
		paths := []string{filepath.Join(root, "config"), filepath.Join(root, "metadata.db"), filepath.Join(root, "staging")}
		paths[missing] = ""
		if _, err := NewPrivateStoragePolicy(paths[0], paths[1], paths[2]); err == nil {
			t.Fatalf("missing destination %d accepted", missing)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("layout construction created paths")
	}
}
