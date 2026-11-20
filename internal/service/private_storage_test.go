package service

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

func TestPrivateStoragePaths(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	data := filepath.Join(root, "data")
	policy, err := NewPrivateStoragePolicy(config, filepath.Join(data, "metadata.db"), filepath.Join(data, "clipper-staging"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, config, filepath.Join(config, "notes"), data, filepath.Join(data, "clipper-staging"), filepath.Join(data, "clipper-staging", "notes"), filepath.Join(data, "metadata.db"), filepath.Join(data, "metadata.db-wal"), filepath.Join(data, "metadata.db-shm"), filepath.Join(data, "metadata.db-journal"), filepath.Join(data, "metadata.db-mj1234567890")} {
		t.Run(path, func(t *testing.T) {
			if err := policy.ValidateBasePath(path); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("unsafe path accepted: %v", err)
			} else if strings.Contains(err.Error(), root) {
				t.Fatalf("private path leaked: %v", err)
			}
		})
	}
	for _, path := range []string{filepath.Join(data, "bases", "default"), config + "-sibling", filepath.Join(data, "metadata.db-notes")} {
		if err := policy.ValidateBasePath(path); err != nil {
			t.Fatalf("safe sibling %s: %v", path, err)
		}
	}
}

func TestPrivateStorageMissingAncestorsAndRetarget(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(private, alias); err != nil {
		t.Skip(err)
	}
	p, err := NewPrivateStoragePolicy(filepath.Join(alias, "missing", "config"), filepath.Join(root, "data", "metadata.db"), filepath.Join(root, "data", "clipper-staging"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateBasePath(private); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("missing private alias accepted: %v", err)
	}
	safe := filepath.Join(root, "safe")
	if err := os.Mkdir(safe, 0700); err != nil {
		t.Fatal(err)
	}
	baseAlias := filepath.Join(root, "base-alias")
	if err := os.Symlink(safe, baseAlias); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateBasePath(baseAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(baseAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, baseAlias); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateBasePath(baseAlias); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("retarget accepted: %v", err)
	}
}

func TestPrivateStorageStartupAllBasesAndNoWrites(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	configPath := filepath.Join(root, "config", "config.json")
	safe := filepath.Join(root, "notes")
	if err := os.Mkdir(safe, 0700); err != nil {
		t.Fatal(err)
	}
	store := NewConfigService(configPath)
	done := true
	config := model.Config{Bases: []model.Base{{Name: "safe", Path: safe}, {Name: "inactive", Path: data}}, CurrentBase: "safe", SetupCompleted: &done}
	if err := store.Save(&config); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveStartupBase(store, "safe", data); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("inactive unsafe base accepted: %v", err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("config changed")
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("data created: %v", err)
	}
}

func TestPrivateStorageDefaultInitializationRejectsNestedConfigBeforeWrites(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	configPath := filepath.Join(data, "bases", "default", "config", "config.json")
	if _, err := ResolveStartupBase(NewConfigService(configPath), "", data); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("unsafe default accepted: %v", err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("created files before validation: %v", err)
	}
}

type privateSettingsFixture struct {
	settings                                    *SettingsService
	store                                       *ConfigService
	runtime                                     *fakeBaseRuntime
	root, configDir, data, staging, safe, alias string
}

func newPrivateSettingsFixture(t *testing.T, completed bool) privateSettingsFixture {
	t.Helper()
	root := t.TempDir()
	f := privateSettingsFixture{root: root, configDir: filepath.Join(root, "config"), data: filepath.Join(root, "data"), safe: filepath.Join(root, "notes"), alias: filepath.Join(root, "alias")}
	f.staging = filepath.Join(f.data, "clipper-staging")
	for _, path := range []string{f.safe, f.configDir, f.data} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := NewPrivateStoragePolicy(f.configDir, filepath.Join(f.data, "metadata.db"), f.staging)
	if err != nil {
		t.Fatal(err)
	}
	f.store = NewConfigService(filepath.Join(f.configDir, "config.json"), policy)
	config := model.Config{BaseDir: root, Bases: []model.Base{{Name: "safe", Path: f.safe}}, CurrentBase: "safe", SetupCompleted: &completed}
	if err := f.store.Save(&config); err != nil {
		t.Fatal(err)
	}
	f.runtime = &fakeBaseRuntime{path: f.safe}
	f.settings, err = NewSettingsService(f.store, f.runtime, NewBaseOperationCoordinator(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPrivateStorageSettingsRejectWithoutWrites(t *testing.T) {
	for _, operation := range []string{"setup-create", "setup-connect", "add-create", "add-connect", "update", "replace", "switch"} {
		t.Run(operation, func(t *testing.T) {
			f := newPrivateSettingsFixture(t, !strings.HasPrefix(operation, "setup"))
			if operation == "switch" {
				if err := os.Symlink(f.safe, f.alias); err != nil {
					t.Skip(err)
				}
				if _, err := f.settings.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "alias", Path: f.alias}); err != nil {
					t.Fatal(err)
				}
				// Persisted normalized paths are canonical. To exercise an existing
				// configured alias, load it through the real constructor.
				config := f.settings.GetConfig()
				config.Bases[1].Path = f.alias
				if err := f.store.Save(&config); err != nil {
					t.Fatal(err)
				}
				var err error
				f.settings, err = NewSettingsService(f.store, f.runtime, NewBaseOperationCoordinator(), "", nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(f.alias); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.configDir, f.alias); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(f.store.configPath)
			if err != nil {
				t.Fatal(err)
			}
			configBefore := f.settings.GetConfig()
			persistBefore := f.runtime.persistCalls
			switch operation {
			case "setup-create":
				_, err = f.settings.CompleteSetup(model.BaseMutationRequest{Mode: "create", Name: "new", Path: f.configDir})
			case "setup-connect":
				_, err = f.settings.CompleteSetup(model.BaseMutationRequest{Mode: "connect", Name: "new", Path: f.root})
			case "add-create":
				_, err = f.settings.AddBase(model.BaseMutationRequest{Mode: "create", Name: "new", Path: f.configDir})
			case "add-connect":
				_, err = f.settings.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "new", Path: f.data})
			case "update":
				_, err = f.settings.UpdateBase("safe", model.BaseUpdateRequest{Name: "safe", Path: f.configDir})
			case "replace":
				next := f.settings.GetConfig()
				next.Bases = append(next.Bases, model.Base{Name: "bad", Path: f.root})
				_, err = f.settings.ReplaceConfig(next)
			case "switch":
				_, err = f.settings.SwitchBase("alias")
			}
			if !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("unsafe mutation accepted: %v", err)
			}
			var field *FieldError
			if !errors.As(err, &field) || strings.Contains(field.Message, f.root) {
				t.Fatalf("unsafe public error: %v", err)
			}
			after, readErr := os.ReadFile(f.store.configPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(before) != string(after) || !reflect.DeepEqual(configBefore, f.settings.GetConfig()) {
				t.Fatal("configuration changed")
			}
			if f.runtime.persistCalls != persistBefore || len(f.runtime.transactionCalls) != 0 {
				t.Fatal("runtime touched before rejection")
			}
			if _, err := os.Stat(filepath.Join(f.configDir, "new")); !os.IsNotExist(err) {
				t.Fatalf("unsafe directory created: %v", err)
			}
		})
	}
}

func TestPrivateStorageAddChecksInactiveAliasesBeforeMkdir(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	if err := os.Symlink(f.safe, f.alias); err != nil {
		t.Skip(err)
	}
	config := f.settings.GetConfig()
	config.Bases = append(config.Bases, model.Base{Name: "alias", Path: f.alias})
	if err := f.store.Save(&config); err != nil {
		t.Fatal(err)
	}
	settings, err := NewSettingsService(f.store, f.runtime, NewBaseOperationCoordinator(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.configDir, f.alias); err != nil {
		t.Fatal(err)
	}
	_, err = settings.AddBase(model.BaseMutationRequest{Mode: "create", Name: "new", Path: f.safe})
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("retarget accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.safe, "new")); !os.IsNotExist(err) {
		t.Fatalf("mkdir before validation: %v", err)
	}
}

func TestPrivateStorageStagingConcurrentRebind(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	root, err := f.settings.OpenPrivateStagingRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// A pinned upload can continue without holding the global coordinator.
	file, err := root.OpenFile("upload", os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result := make(chan error, 1)
	go func() {
		_, err := f.settings.UpdateBase("safe", model.BaseUpdateRequest{Name: "safe", Path: f.data})
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("rebind accepted: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("staging retained lifecycle lock")
	}
	if _, err := file.WriteString("private upload"); err != nil {
		t.Fatal(err)
	}
	if f.settings.GetConfig().Bases[0].Path != f.safe {
		t.Fatal("root rebound")
	}
	if _, err := f.settings.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "staging", Path: f.staging}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("staging accepted: %v", err)
	}
}

func TestPrivateStorageConstructorRejectsInactiveBeforeMigration(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	config := f.settings.GetConfig()
	config.SetupCompleted = nil
	config.Bases = append(config.Bases, model.Base{Name: "private", Path: f.configDir})
	if err := f.store.Save(&config); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.store.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSettingsService(f.store, f.runtime, NewBaseOperationCoordinator(), "", nil); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("constructor accepted invalid base: %v", err)
	}
	after, err := os.ReadFile(f.store.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("migration wrote invalid configuration")
	}
}

func TestPrivateStorageDestinationRechecksRetargetedPath(t *testing.T) {
	f := newClipperDestinationFixture(t)
	private := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	policy, err := NewPrivateStoragePolicy(private, filepath.Join(t.TempDir(), "metadata.db"), filepath.Join(t.TempDir(), "staging"))
	if err != nil {
		t.Fatal(err)
	}
	f.settings.privateStorage = policy
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(f.target, alias); err != nil {
		t.Skip(err)
	}
	f.settings.config.Bases[1].Path = alias
	if _, err := f.service.Issue("target"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Issue("target"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("private destination issued: %v", err)
	}
}

func TestPrivateStorageConfigStoreAlwaysReservesActualConfigFolder(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	otherPolicy, err := NewPrivateStoragePolicy(t.TempDir(), filepath.Join(f.data, "metadata.db"), f.staging)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []*ConfigService{NewConfigService(f.store.configPath), NewConfigService(f.store.configPath, otherPolicy)} {
		config := f.settings.GetConfig()
		config.Bases = append(config.Bases, model.Base{Name: "bad", Path: f.configDir})
		if err := store.Save(&config); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSettingsService(store, f.runtime, NewBaseOperationCoordinator(), "", nil); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("actual config folder accepted: %v", err)
		}
	}
}

func TestPrivateStorageNativeCaseAndIdentity(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	policy := f.store.PrivateStoragePolicy()
	if runtime.GOOS == "windows" {
		if err := policy.ValidateBasePath(strings.ToUpper(f.configDir) + string(filepath.Separator)); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("case alias accepted: %v", err)
		}
	}
	db := filepath.Join(f.data, "metadata.db")
	if err := os.WriteFile(db, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(f.root, "db-alias")
	if err := os.Link(db, alias); err != nil {
		t.Skip(err)
	}
	if err := policy.ValidateBasePath(alias); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("file identity alias accepted: %v", err)
	}
}

func TestPrivateStorageGitSnapshotsRecheckRetargetedPath(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	if err := os.Symlink(f.safe, f.alias); err != nil {
		t.Skip(err)
	}
	config := f.settings.GetConfig()
	config.Bases = append(config.Bases, model.Base{Name: "git", Path: f.alias, GitURL: "https://example.invalid/notes.git", GitBranch: "main"})
	if err := f.store.Save(&config); err != nil {
		t.Fatal(err)
	}
	settings, err := NewSettingsService(f.store, f.runtime, NewBaseOperationCoordinator(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := settings.GitSnapshot("git"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.configDir, f.alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := settings.GitSnapshot("git"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("unsafe Git snapshot: %v", err)
	}
	if _, err := settings.GitSnapshots(); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("unsafe Git snapshots: %v", err)
	}
}
