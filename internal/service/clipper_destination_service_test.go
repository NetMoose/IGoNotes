package service

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"IGoNotes/internal/model"
)

type clipperDestinationFixture struct {
	settings       *SettingsService
	notes          *NoteService
	store          *fakeConfigStore
	repo           *fakeNoteRepository
	service        *ClipperDestinationService
	active, target string
}

func newClipperDestinationFixture(t *testing.T) clipperDestinationFixture {
	t.Helper()
	active, target := t.TempDir(), t.TempDir()
	writeTestNote(t, active, "active.md", "active")
	writeTestNote(t, target, "target.md", "target")
	coordinator := NewBaseOperationCoordinator()
	repo := &fakeNoteRepository{}
	notes := newTestNoteServiceWithCoordinator(t, repo, active, coordinator)
	if err := notes.SyncFS(); err != nil {
		t.Fatal(err)
	}
	completed := true
	config := model.Config{Bases: []model.Base{{Name: "active", Path: active}, {Name: "target", Path: target}}, CurrentBase: "active", SetupCompleted: &completed}
	store := &fakeConfigStore{config: &config}
	settings, err := NewSettingsService(store, notes, coordinator, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return clipperDestinationFixture{settings, notes, store, repo, NewClipperDestinationService(settings), active, target}
}

func requireClipperDestinationCode(t *testing.T, err error, code model.ClipperErrorCode) {
	t.Helper()
	var clipErr *model.ClipperError
	if !errors.As(err, &clipErr) || clipErr.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func issueClipperDestination(t *testing.T, s *ClipperDestinationService, name string) string {
	t.Helper()
	revision, err := s.Issue(name)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(revision)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("revision is not opaque 256-bit handle: %q", revision)
	}
	return revision
}

func TestClipperDestinationExplicitScopedRoots(t *testing.T) {
	f := newClipperDestinationFixture(t)
	before := f.settings.GetConfig()
	names := f.service.ListBases()
	if !reflect.DeepEqual(names, []string{"active", "target"}) {
		t.Fatalf("names = %v", names)
	}
	names[0] = "changed"
	if f.service.ListBases()[0] != "active" {
		t.Fatal("names alias configuration")
	}
	for _, name := range []string{"active", "target"} {
		revision := issueClipperDestination(t, f.service, name)
		var borrowed *os.Root
		err := f.service.WithDestination(name, revision, func(snapshot ClipperDestinationSnapshot, root *os.Root) error {
			borrowed = root
			if snapshot.Name != name || snapshot.Active != (name == "active") {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			wantPath := f.target
			if name == "active" {
				wantPath = f.active
			}
			canonical, err := canonicalExistingDirectory(wantPath)
			if err != nil {
				t.Fatal(err)
			}
			info, err := root.Stat(".")
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.CanonicalPath != canonical || !os.SameFile(snapshot.RootIdentity, info) {
				t.Fatalf("incorrect pinned snapshot: %+v", snapshot)
			}
			bytes, err := root.ReadFile(name + ".md")
			if err != nil || string(bytes) != name {
				t.Fatalf("wrong root: %q, %v", bytes, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := borrowed.Stat("."); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("scoped root not closed: %v", err)
		}
		if again := issueClipperDestination(t, f.service, name); again != revision {
			t.Fatal("unchanged binding invalidated")
		}
	}
	if !reflect.DeepEqual(before, f.settings.GetConfig()) {
		t.Fatal("discovery changed settings")
	}
	assertRepositoryIDs(t, f.repo, "active.md")
	if f.notes.GetBasePath() != f.active {
		t.Fatal("runtime switched")
	}
	for _, name := range []string{"", "missing"} {
		_, err := f.service.Issue(name)
		requireClipperDestinationCode(t, err, model.ClipperDestinationNotFound)
	}
	revision := issueClipperDestination(t, f.service, "target")
	for _, pair := range [][2]string{{"active", revision}, {"target", ""}, {"missing", revision}, {"target", "unknown"}} {
		err := f.service.WithDestination(pair[0], pair[1], func(ClipperDestinationSnapshot, *os.Root) error { t.Fatal("invalid revision callback"); return nil })
		requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
	}
}

func TestClipperDestinationGitAndActiveSwitchKeepBinding(t *testing.T) {
	f := newClipperDestinationFixture(t)
	validator := &fakeGitConfigValidator{}
	statuses := &fakeGitStatusStore{statuses: make(map[string]model.GitStatus)}
	settings, err := NewSettingsServiceWithGit(f.store, f.notes, f.settings.coordinator, "", nil, validator, statuses)
	if err != nil {
		t.Fatal(err)
	}
	s := NewClipperDestinationService(settings)
	revision := issueClipperDestination(t, s, "target")
	for _, mutate := range []func() error{
		func() error {
			_, err := settings.ConfigureGit(context.Background(), "target", model.GitConfigRequest{GitURL: "https://example.test/repo.git", GitBranch: "main"})
			return err
		},
		func() error { _, err := settings.DisableGit(context.Background(), "target"); return err },
		func() error { _, err := settings.SwitchBase("target"); return err },
	} {
		if err := mutate(); err != nil {
			t.Fatal(err)
		}
		if fresh := issueClipperDestination(t, s, "target"); fresh != revision {
			t.Fatal("unrelated settings invalidated root binding")
		}
		if err := s.WithDestination("target", revision, func(ClipperDestinationSnapshot, *os.Root) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClipperDestinationLifecycleInvalidatesABA(t *testing.T) {
	for _, mutation := range []string{"rename", "path", "forget", "replace rename", "replace path", "replace forget"} {
		t.Run(mutation, func(t *testing.T) {
			f := newClipperDestinationFixture(t)
			old := issueClipperDestination(t, f.service, "target")
			unaffected := issueClipperDestination(t, f.service, "active")
			original := f.settings.GetConfig()
			var err error
			switch mutation {
			case "rename":
				_, err = f.settings.UpdateBase("target", model.BaseUpdateRequest{Name: "renamed", Path: f.target})
			case "path":
				_, err = f.settings.UpdateBase("target", model.BaseUpdateRequest{Name: "target", Path: t.TempDir()})
			case "forget":
				_, err = f.settings.ForgetBase("target")
			default:
				next := cloneConfig(original)
				switch mutation {
				case "replace rename":
					next.Bases[1].Name = "renamed"
				case "replace path":
					next.Bases[1].Path = t.TempDir()
				case "replace forget":
					next.Bases = next.Bases[:1]
				}
				_, err = f.settings.ReplaceConfig(next)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.settings.ReplaceConfig(original); err != nil {
				t.Fatal(err)
			}
			err = f.service.WithDestination("target", old, func(ClipperDestinationSnapshot, *os.Root) error { t.Fatal("ABA accepted"); return nil })
			requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
			if err := f.service.WithDestination("active", unaffected, func(ClipperDestinationSnapshot, *os.Root) error { return nil }); err != nil {
				t.Fatalf("unaffected binding invalidated: %v", err)
			}
			if fresh := issueClipperDestination(t, f.service, "target"); fresh == old {
				t.Fatal("reused invalidated handle")
			}
		})
	}
}

func TestClipperDestinationFailedMutationsKeepBinding(t *testing.T) {
	f := newClipperDestinationFixture(t)
	revision := issueClipperDestination(t, f.service, "target")
	f.store.saveErr = errors.New("save failed")
	if _, err := f.settings.UpdateBase("target", model.BaseUpdateRequest{Name: "renamed", Path: f.target}); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := f.settings.ForgetBase("target"); err == nil {
		t.Fatal("expected failure")
	}
	f.store.saveErr = nil
	if _, err := f.settings.UpdateBase("target", model.BaseUpdateRequest{Name: "active", Path: f.target}); err == nil {
		t.Fatal("expected name conflict")
	}
	if err := f.service.WithDestination("target", revision, func(ClipperDestinationSnapshot, *os.Root) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestClipperDestinationActiveLifecycleAndFailedSwitch(t *testing.T) {
	for _, change := range []string{"rename", "path"} {
		t.Run(change, func(t *testing.T) {
			f := newClipperDestinationFixture(t)
			old := issueClipperDestination(t, f.service, "active")
			original := f.settings.GetConfig()
			request := model.BaseUpdateRequest{Name: "renamed", Path: f.active}
			if change == "path" {
				request = model.BaseUpdateRequest{Name: "active", Path: t.TempDir()}
			}
			f.store.saveErr = errors.New("save failed")
			if _, err := f.settings.UpdateBase("active", request); err == nil {
				t.Fatal("expected persistence failure")
			}
			f.store.saveErr = nil
			if err := f.service.WithDestination("active", old, func(ClipperDestinationSnapshot, *os.Root) error { return nil }); err != nil {
				t.Fatalf("failed active mutation invalidated original binding: %v", err)
			}
			if _, err := f.settings.UpdateBase("active", request); err != nil {
				t.Fatal(err)
			}
			if _, err := f.settings.ReplaceConfig(original); err != nil {
				t.Fatal(err)
			}
			err := f.service.WithDestination("active", old, func(ClipperDestinationSnapshot, *os.Root) error {
				t.Fatal("active ABA accepted")
				return nil
			})
			requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
			if issueClipperDestination(t, f.service, "active") == old {
				t.Fatal("active lifecycle reused revision")
			}
		})
	}
}

func TestClipperDestinationRootReplacement(t *testing.T) {
	for _, name := range []string{"active", "target"} {
		t.Run(name, func(t *testing.T) {
			f := newClipperDestinationFixture(t)
			revision := issueClipperDestination(t, f.service, name)
			path := f.target
			if name == "active" {
				path = f.active
			}
			moved := path + "-original"
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(moved) })
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			err := f.service.WithDestination(name, revision, func(ClipperDestinationSnapshot, *os.Root) error { t.Fatal("replacement accepted"); return nil })
			requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
			if name == "active" {
				_, err = f.service.Issue(name)
				requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
			} else if fresh := issueClipperDestination(t, f.service, name); fresh == revision {
				t.Fatal("replacement kept revision")
			}
		})
	}
}

func TestClipperDestinationRestartRegistry(t *testing.T) {
	f := newClipperDestinationFixture(t)
	old := issueClipperDestination(t, f.service, "target")
	// Even a replacement registry using the same settings cannot accept old handles.
	for _, settings := range []*SettingsService{f.settings, func() *SettingsService {
		coordinator := NewBaseOperationCoordinator()
		notes := newTestNoteServiceWithCoordinator(t, &fakeNoteRepository{}, f.active, coordinator)
		restarted, err := NewSettingsService(f.store, notes, coordinator, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		return restarted
	}()} {
		restarted := NewClipperDestinationService(settings)
		err := restarted.WithDestination("target", old, func(ClipperDestinationSnapshot, *os.Root) error { t.Fatal("old registry handle accepted"); return nil })
		requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
		if issueClipperDestination(t, restarted, "target") == old {
			t.Fatal("restart reused revision")
		}
	}
}

func TestClipperDestinationCoordinatorLockedCallbackAndCleanup(t *testing.T) {
	f := newClipperDestinationFixture(t)
	revision := issueClipperDestination(t, f.service, "target")
	wantErr := errors.New("callback failed")
	var borrowed *os.Root
	f.settings.coordinator.Lock()
	err := f.service.withDestinationCoordinatorLocked("target", revision, func(_ ClipperDestinationSnapshot, root *os.Root) error {
		borrowed = root
		if f.settings.mu.TryLock() {
			f.settings.mu.Unlock()
			t.Fatal("settings snapshot not locked")
		}
		if f.notes.baseMu.TryLock() {
			f.notes.baseMu.Unlock()
			t.Fatal("runtime not locked")
		}
		return wantErr
	})
	f.settings.coordinator.Unlock()
	if !errors.Is(err, wantErr) {
		t.Fatalf("callback error = %v", err)
	}
	if _, err := borrowed.Stat("."); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("callback error leaked root: %v", err)
	}
	if err := f.service.WithDestination("target", revision, nil); err == nil {
		t.Fatal("nil callback accepted")
	}
}

func TestClipperDestinationConcurrentIssueAndLifecycle(t *testing.T) {
	f := newClipperDestinationFixture(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				revision, err := f.service.Issue("target")
				if err != nil {
					t.Error(err)
					return
				}
				err = f.service.WithDestination("target", revision, func(ClipperDestinationSnapshot, *os.Root) error { return nil })
				if err != nil {
					requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
				}
			}
		})
	}
	for range 10 {
		if _, err := f.settings.UpdateBase("target", model.BaseUpdateRequest{Name: "target", Path: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestClipperDestinationSymlinkRetarget(t *testing.T) {
	f := newClipperDestinationFixture(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(f.target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Config loaded at startup may still contain an inactive alias.
	f.settings.mu.Lock()
	f.settings.config.Bases[1].Path = alias
	f.settings.mu.Unlock()
	revision := issueClipperDestination(t, f.service, "target")
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	err := f.service.WithDestination("target", revision, func(ClipperDestinationSnapshot, *os.Root) error { t.Fatal("retarget accepted"); return nil })
	requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
	if strings.Contains(revision, f.target) {
		t.Fatal("revision exposed path")
	}
}
