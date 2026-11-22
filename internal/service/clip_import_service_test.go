package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

type clipImportFixture struct {
	settings       *SettingsService
	notes          *NoteService
	dest           *ClipperDestinationService
	store          *repository.ClipImportRepository
	index          *repository.NoteRepository
	db             *sql.DB
	dbPath         string
	service        *ClipImportService
	active, target string
}

func newClipImportFixture(t *testing.T) *clipImportFixture {
	t.Helper()
	f := &clipImportFixture{active: t.TempDir(), target: t.TempDir()}
	writeTestNote(t, f.active, "active.md", "active")
	writeTestNote(t, f.target, "target.md", "target")
	f.dbPath = filepath.Join(t.TempDir(), "metadata.db")
	db, err := repository.InitDB(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	f.db = db
	t.Cleanup(func() { db.Close() })
	f.index = repository.NewNoteRepository(db)
	c := NewBaseOperationCoordinator()
	f.notes = NewNoteService(f.index, f.active, c)
	t.Cleanup(func() { f.notes.Close() })
	if err := f.notes.SyncFS(); err != nil {
		t.Fatal(err)
	}
	completed := true
	cfg := model.Config{Bases: []model.Base{{Name: "active", Path: f.active}, {Name: "target", Path: f.target}}, CurrentBase: "active", SetupCompleted: &completed}
	f.settings, err = NewSettingsService(&fakeConfigStore{config: &cfg}, f.notes, c, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.dest = NewClipperDestinationService(f.settings)
	f.store = repository.NewClipImportRepository(db)
	f.service, err = NewClipImportService(f.settings, f.dest, f.notes, f.store)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *clipImportFixture) manifest(t *testing.T, base string) model.ClipperManifest {
	t.Helper()
	m := validClipManifest()
	m.Base = base
	m.DestinationRevision = issueClipperDestination(t, f.dest, base)
	return m
}

func TestClipImportActiveInactiveReceiptReplay(t *testing.T) {
	for _, base := range []string{"active", "target"} {
		t.Run(base, func(t *testing.T) {
			f := newClipImportFixture(t)
			m := f.manifest(t, base)
			before := f.settings.GetConfig()
			status, replay, err := f.service.Import(context.Background(), m)
			if err != nil || replay || status.State != model.ClipperImportSucceeded || status.Result == nil {
				t.Fatalf("Import = %+v, %v, %v", status, replay, err)
			}
			root := f.active
			if base == "target" {
				root = f.target
			}
			data, err := os.ReadFile(filepath.Join(root, m.Filename))
			if err != nil || string(data) != m.Markdown || status.Result.Revision != noteRevision(data) {
				t.Fatalf("content/revision = %q, %+v, %v", data, status, err)
			}
			nodes, err := f.index.GetAllNodes()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, n := range nodes {
				if n.ID == m.Filename {
					found = true
				}
			}
			if found != (base == "active") {
				t.Fatalf("active index changed incorrectly: %+v", nodes)
			}
			if !reflect.DeepEqual(before, f.settings.GetConfig()) || f.notes.GetBasePath() != f.active {
				t.Fatal("runtime/config switched")
			}
			again, replay, err := f.service.Import(context.Background(), m)
			if err != nil || !replay || !reflect.DeepEqual(status, again) {
				t.Fatalf("replay = %+v, %v, %v", again, replay, err)
			}
			public, err := f.service.Status(context.Background(), m.ImportID)
			if err != nil || !reflect.DeepEqual(public, status) {
				t.Fatalf("Status = %+v, %v", public, err)
			}
			encoded, _ := json.Marshal(public)
			if strings.Contains(string(encoded), root) || strings.Contains(string(encoded), "temp_name") {
				t.Fatalf("private status: %s", encoded)
			}
			m.Markdown += "changed"
			_, _, err = f.service.Import(context.Background(), m)
			requireClipperDestinationCode(t, err, model.ClipperRequestChanged)
		})
	}
}

func TestClipImportPreflightGates(t *testing.T) {
	for _, gate := range []string{"missing parent", "existing", "git active", "git inactive", "git identity", "stale", "assets", "invalid"} {
		t.Run(gate, func(t *testing.T) {
			f := newClipImportFixture(t)
			m := f.manifest(t, "target")
			want := model.ClipperDestinationNotFound
			switch gate {
			case "missing parent":
				m.Parent = "missing/child"
			case "existing":
				m.Filename = "target.md"
				want = model.ClipperNoteConflict
			case "git active":
				m = f.manifest(t, "active")
				f.settings.coordinator.SetConflict(f.active, true)
			case "git inactive":
				f.settings.coordinator.SetConflict(f.target, true)
			case "git identity":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(f.target, alias); err != nil {
					t.Skip(err)
				}
				f.settings.coordinator.SetConflict(alias, true)
			case "stale":
				m.DestinationRevision = "stale"
				want = model.ClipperDestinationChanged
			case "assets":
				m.Assets = []model.ClipperAsset{clipAsset("a")}
				want = model.ClipperInvalidRequest
			case "invalid":
				m.Markdown = ""
				want = model.ClipperInvalidRequest
			}
			_, _, err := f.service.Import(context.Background(), m)
			if strings.HasPrefix(gate, "git") {
				if !errors.Is(err, ErrGitConflictPending) {
					t.Fatalf("gate = %v", err)
				}
			} else {
				requireClipperDestinationCode(t, err, want)
			}
			entries, err := os.ReadDir(f.target)
			if err != nil || len(entries) != 1 {
				t.Fatalf("preflight wrote: %v, %v", entries, err)
			}
			_, found, err := f.store.Get(context.Background(), m.ImportID)
			if err != nil || found {
				t.Fatalf("preflight claimed: %v %v", found, err)
			}
		})
	}
}

func TestClipImportConcurrentID(t *testing.T) {
	f := newClipImportFixture(t)
	m := f.manifest(t, "target")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			status, _, err := f.service.Import(context.Background(), m)
			if err != nil || (status.State != model.ClipperImportSucceeded && status.State != model.ClipperImportPending) {
				t.Errorf("concurrent = %+v, %v", status, err)
			}
		})
	}
	wg.Wait()
	entries, _ := os.ReadDir(f.target)
	if len(entries) != 2 {
		t.Fatalf("duplicate artifacts: %v", entries)
	}
}

// The decorator retains the real database for every successful operation. Hooks
// inject failures at persistence boundaries, rather than simulating publication.
type hookedClipImportStore struct {
	ClipImportStore
	claim      func(repository.ClipImportRecord) error
	checkpoint func(json.RawMessage) error
	transition func(repository.ClipImportState) error
}

func (s *hookedClipImportStore) Claim(ctx context.Context, record repository.ClipImportRecord) (repository.ClipImportRecord, bool, error) {
	if s.claim != nil {
		if err := s.claim(record); err != nil {
			return repository.ClipImportRecord{}, false, err
		}
	}
	return s.ClipImportStore.Claim(ctx, record)
}
func (s *hookedClipImportStore) Checkpoint(ctx context.Context, id string, version repository.ClipImportVersion, raw json.RawMessage) error {
	if s.checkpoint != nil {
		if err := s.checkpoint(raw); err != nil {
			return err
		}
	}
	return s.ClipImportStore.Checkpoint(ctx, id, version, raw)
}
func (s *hookedClipImportStore) Transition(ctx context.Context, id string, version repository.ClipImportVersion, state repository.ClipImportState, result *model.ClipperImportResult, public *model.APIError) error {
	if s.transition != nil {
		if err := s.transition(state); err != nil {
			return err
		}
	}
	return s.ClipImportStore.Transition(ctx, id, version, state, result, public)
}

func TestClipImportJournalBeforeEffects(t *testing.T) {
	f := newClipImportFixture(t)
	m := f.manifest(t, "target")
	f.service.store = &hookedClipImportStore{ClipImportStore: f.store, claim: func(record repository.ClipImportRecord) error {
		entries, _ := os.ReadDir(f.target)
		if len(entries) != 1 {
			t.Fatalf("write before claim: %v", entries)
		}
		var plan clipImportPlan
		if err := json.Unmarshal(record.Journal.Plan, &plan); err != nil || plan.Markdown != m.Markdown || plan.Note.TempName == "" || plan.Version != 1 {
			t.Fatalf("nonrecoverable plan: %+v, %v", plan, err)
		}
		return nil
	}}
	f.service.prepare = func(root *os.Root, parent, filename, markdown string) (*clipNoteFile, error) {
		file, err := prepareClipNote(root, parent, filename, markdown)
		if err != nil {
			return nil, err
		}
		file.ops.write = func(handle *os.File, data []byte) (int, error) {
			record, found, err := f.store.Get(context.Background(), m.ImportID)
			var cp clipImportCheckpoint
			_ = json.Unmarshal(record.Journal.Checkpoint, &cp)
			if err != nil || !found || record.State != repository.ClipImportPrepared || cp.Step != "writing" || cp.Note.Identity == (clipFileIdentity{}) {
				t.Fatalf("write without ownership checkpoint: %+v, %v", record, err)
			}
			return handle.Write(data)
		}
		file.ops.rename = func(dir *os.File, old, next string) error {
			record, _, err := f.store.Get(context.Background(), m.ImportID)
			var cp clipImportCheckpoint
			_ = json.Unmarshal(record.Journal.Checkpoint, &cp)
			if err != nil || record.State != repository.ClipImportPublishing || cp.Step != "written" || cp.Note.SHA256 != file.plan.SHA256 {
				t.Fatalf("rename without publishing checkpoint: %+v %v", record, err)
			}
			return renameNoReplace(dir, old, next)
		}
		return file, nil
	}
	status, _, err := f.service.Import(context.Background(), m)
	if err != nil || status.State != model.ClipperImportSucceeded {
		t.Fatalf("Import = %+v %v", status, err)
	}
}

func TestClipImportFailureBoundaries(t *testing.T) {
	for _, failure := range []string{"claim", "created checkpoint", "written checkpoint", "publishing transition", "write", "close", "rename", "rename exists", "directory sync", "published checkpoint", "index", "receipt checkpoint", "receipt", "persistent checkpoint", "cleanup ownership"} {
		t.Run(failure, func(t *testing.T) {
			f := newClipImportFixture(t)
			base := "target"
			if failure == "index" {
				base = "active"
			}
			m := f.manifest(t, base)
			private := errors.New("private /secret/base/path journal body")
			hooks := &hookedClipImportStore{ClipImportStore: f.store}
			f.service.store = hooks
			if failure == "claim" {
				hooks.claim = func(repository.ClipImportRecord) error { return private }
			}
			targetStep := map[string]string{"created checkpoint": "writing", "written checkpoint": "written", "published checkpoint": "published", "receipt checkpoint": "receipt"}[failure]
			failed := false
			hooks.checkpoint = func(raw json.RawMessage) error {
				var cp clipImportCheckpoint
				_ = json.Unmarshal(raw, &cp)
				if failure == "persistent checkpoint" || (!failed && targetStep != "" && cp.Step == targetStep) {
					failed = true
					return private
				}
				return nil
			}
			hooks.transition = func(next repository.ClipImportState) error {
				if failure == "receipt" && next == repository.ClipImportSucceeded || failure == "publishing transition" && next == repository.ClipImportPublishing {
					return private
				}
				return nil
			}
			if failure == "index" {
				f.notes.scan = func(*os.Root) ([]model.NoteNode, error) { return nil, private }
			}
			f.service.prepare = func(root *os.Root, parent, filename, markdown string) (*clipNoteFile, error) {
				file, err := prepareClipNote(root, parent, filename, markdown)
				if err != nil {
					return nil, err
				}
				switch failure {
				case "write":
					file.ops.write = func(*os.File, []byte) (int, error) { return 0, private }
				case "close":
					file.ops.close = func(file *os.File) error { _ = file.Close(); return private }
				case "rename":
					file.ops.rename = func(*os.File, string, string) error { return private }
				case "rename exists":
					file.ops.rename = func(dir *os.File, old, next string) error {
						if err := file.parent.WriteFile(next, []byte("foreign"), 0600); err != nil {
							t.Fatal(err)
						}
						return os.ErrExist
					}
				case "directory sync":
					file.ops.syncParent = func(*os.File) error { return private }
				case "cleanup ownership":
					file.ops.write = func(handle *os.File, data []byte) (int, error) {
						if err := file.parent.Remove(file.plan.TempName); err != nil {
							t.Fatal(err)
						}
						if err := file.parent.WriteFile(file.plan.TempName, []byte("foreign"), 0600); err != nil {
							t.Fatal(err)
						}
						return 0, private
					}
				}
				return file, nil
			}
			status, _, err := f.service.Import(context.Background(), m)
			if failure == "claim" {
				requireClipperDestinationCode(t, err, model.ClipperRecoveryRequired)
				entries, _ := os.ReadDir(f.target)
				if len(entries) != 1 {
					t.Fatalf("claim failure wrote: %v", entries)
				}
				return
			}
			if err != nil {
				t.Fatalf("uncertain operation returned definite error: %v", err)
			}
			want := model.ClipperImportRecoveryRequired
			switch failure {
			case "created checkpoint", "written checkpoint", "publishing transition", "write", "close":
				want = model.ClipperImportRejected
			case "persistent checkpoint":
				want = model.ClipperImportPending
			}
			if status.State != want {
				t.Fatalf("failure %s = %+v, want %s", failure, status, want)
			}
			encoded, _ := json.Marshal(status)
			if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "journal body") {
				t.Fatalf("private error exposed: %s", encoded)
			}
			root := f.target
			if base == "active" {
				root = f.active
			}
			if failure == "directory sync" || failure == "published checkpoint" || failure == "index" || failure == "receipt checkpoint" || failure == "receipt" {
				data, err := os.ReadFile(filepath.Join(root, m.Filename))
				if err != nil || string(data) != m.Markdown {
					t.Fatalf("published file rolled back: %q %v", data, err)
				}
			}
			if failure == "rename exists" {
				data, _ := os.ReadFile(filepath.Join(root, m.Filename))
				if string(data) != "foreign" {
					t.Fatal("foreign file changed")
				}
			}
			if want == model.ClipperImportRejected {
				entries, _ := os.ReadDir(root)
				if len(entries) != 1 {
					t.Fatalf("owned temp leaked: %v", entries)
				}
			}
			again, replay, err := f.service.Import(context.Background(), m)
			if err != nil || !replay || !reflect.DeepEqual(again, status) {
				t.Fatalf("failure replay = %+v %v %v", again, replay, err)
			}
			if want != model.ClipperImportRejected {
				m.ImportID = "550e8400-e29b-41d4-a716-446655440001"
				m.Filename = "other.md"
				_, _, err := f.service.Import(context.Background(), m)
				requireClipperDestinationCode(t, err, model.ClipperRecoveryRequired)
			}
		})
	}
}

func TestClipImportInflightAndSettingsSwitchRace(t *testing.T) {
	f := newClipImportFixture(t)
	m := f.manifest(t, "target")
	entered, release := make(chan struct{}), make(chan struct{})
	f.service.store = &hookedClipImportStore{ClipImportStore: f.store, checkpoint: func(raw json.RawMessage) error {
		var cp clipImportCheckpoint
		_ = json.Unmarshal(raw, &cp)
		if cp.Step == "writing" {
			close(entered)
			<-release
		}
		return nil
	}}
	imported := make(chan error, 1)
	go func() {
		status, _, err := f.service.Import(context.Background(), m)
		if err == nil && status.State != model.ClipperImportSucceeded {
			err = errors.New("import not succeeded")
		}
		imported <- err
	}()
	<-entered
	status, replay, err := f.service.Import(context.Background(), m)
	if err != nil || !replay || status.State != model.ClipperImportPending {
		t.Fatalf("inflight = %+v %v %v", status, replay, err)
	}
	started, switched := make(chan struct{}), make(chan error, 1)
	go func() { close(started); _, err := f.settings.SwitchBase("target"); switched <- err }()
	<-started
	select {
	case err := <-switched:
		t.Fatalf("settings escaped publication locks: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-imported; err != nil {
		t.Fatal(err)
	}
	if err := <-switched; err != nil {
		t.Fatal(err)
	}
	if f.notes.GetBasePath() != f.target {
		t.Fatal("switch lost")
	}
	nodes, err := f.index.GetAllNodes()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, node := range nodes {
		if node.ID == m.Filename {
			found = true
		}
	}
	if !found {
		t.Fatalf("switch indexed stale snapshot: %+v", nodes)
	}
}

func TestClipImportReceiptAfterRegistryRestartRebind(t *testing.T) {
	f := newClipImportFixture(t)
	m := f.manifest(t, "target")
	original, _, err := f.service.Import(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := repository.InitDB(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rebound := t.TempDir()
	if _, err := f.settings.UpdateBase("target", model.BaseUpdateRequest{Name: "target", Path: rebound}); err != nil {
		t.Fatal(err)
	}
	dest := NewClipperDestinationService(f.settings)
	service, err := NewClipImportService(f.settings, dest, f.notes, repository.NewClipImportRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	status, replay, err := service.Import(context.Background(), m)
	if err != nil || !replay || !reflect.DeepEqual(original, status) {
		t.Fatalf("historical replay = %+v %v %v", status, replay, err)
	}
	m.ImportID = "550e8400-e29b-41d4-a716-446655440001"
	_, _, err = service.Import(context.Background(), m)
	requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
	entries, _ := os.ReadDir(rebound)
	if len(entries) != 0 {
		t.Fatal("replay retargeted")
	}
}

func TestClipImportCompetingClaimDigest(t *testing.T) {
	f := newClipImportFixture(t)
	m := f.manifest(t, "target")
	f.service.store = &hookedClipImportStore{ClipImportStore: f.store, claim: func(record repository.ClipImportRecord) error {
		// Another connection/process wins the unique ID after the service lookup.
		record.RequestDigest = strings.Repeat("a", 64)
		if _, _, err := f.store.Claim(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		return nil
	}}
	_, _, err := f.service.Import(context.Background(), m)
	requireClipperDestinationCode(t, err, model.ClipperRequestChanged)
	entries, _ := os.ReadDir(f.target)
	if len(entries) != 1 {
		t.Fatalf("losing claim wrote: %v", entries)
	}
}

func TestClipImportActiveReceiptOrderingAndExactBytes(t *testing.T) {
	f := newClipImportFixture(t)
	if err := os.MkdirAll(filepath.Join(f.active, "research", "articles"), 0700); err != nil {
		t.Fatal(err)
	}
	m := f.manifest(t, "active")
	m.Parent = "research/articles"
	m.Markdown = "---\r\ntitle: Заметка 🦊\r\n---\r\n# Exact bytes\n"
	m.ExternalImages = []model.ClipperExternalImage{{URL: "https://example.test/missing.png", Reason: "Image unavailable"}}
	checked := false
	f.service.store = &hookedClipImportStore{ClipImportStore: f.store, transition: func(next repository.ClipImportState) error {
		if next != repository.ClipImportSucceeded {
			return nil
		}
		checked = true
		nodes, err := f.index.GetAllNodes()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, n := range nodes {
			if n.ID == "research/articles/clip.md" {
				found = true
			}
		}
		if !found {
			t.Fatalf("receipt before index: %+v", nodes)
		}
		record, _, err := f.store.Get(context.Background(), m.ImportID)
		if err != nil {
			t.Fatal(err)
		}
		var cp clipImportCheckpoint
		_ = json.Unmarshal(record.Journal.Checkpoint, &cp)
		if !cp.Indexed || !cp.Note.Published || !cp.Note.Durable || cp.Step != "receipt" {
			t.Fatalf("receipt before durable publication/index checkpoint: %+v", cp)
		}
		return nil
	}}
	status, _, err := f.service.Import(context.Background(), m)
	if err != nil || !checked || status.Result == nil || status.Result.Path != "research/articles/clip.md" || status.Result.Revision != noteRevision([]byte(m.Markdown)) || !reflect.DeepEqual(status.Result.Warnings, []string{"Image unavailable"}) {
		t.Fatalf("result = %+v %v", status, err)
	}
	data, err := os.ReadFile(filepath.Join(f.active, filepath.FromSlash(status.Result.Path)))
	if err != nil || string(data) != m.Markdown {
		t.Fatalf("bytes changed: %q %v", data, err)
	}
}

func TestClipImportCanceledBeforePublication(t *testing.T) {
	f := newClipImportFixture(t)
	m := f.manifest(t, "target")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.service.store = &hookedClipImportStore{ClipImportStore: f.store, checkpoint: func(raw json.RawMessage) error {
		var cp clipImportCheckpoint
		_ = json.Unmarshal(raw, &cp)
		if cp.Step == "writing" {
			cancel()
		}
		return nil
	}}
	status, _, err := f.service.Import(ctx, m)
	if err != nil || status.State != model.ClipperImportRejected {
		t.Fatalf("canceled cleanup = %+v %v", status, err)
	}
	entries, _ := os.ReadDir(f.target)
	if len(entries) != 1 {
		t.Fatalf("canceled import leaked: %v", entries)
	}
	durable, err := f.service.Status(context.Background(), m.ImportID)
	if err != nil || !reflect.DeepEqual(durable, status) {
		t.Fatalf("canceled receipt = %+v %v", durable, err)
	}
}

func TestClipImportDependenciesAndUnknownStatus(t *testing.T) {
	f := newClipImportFixture(t)
	var nilStore *repository.ClipImportRepository
	for _, deps := range []struct {
		settings *SettingsService
		dest     *ClipperDestinationService
		notes    *NoteService
		store    ClipImportStore
	}{
		{nil, f.dest, f.notes, f.store}, {f.settings, nil, f.notes, f.store}, {f.settings, f.dest, nil, f.store},
		{f.settings, f.dest, f.notes, nil}, {f.settings, f.dest, f.notes, nilStore},
		{f.settings, NewClipperDestinationService(&SettingsService{}), f.notes, f.store},
		{f.settings, f.dest, &NoteService{}, f.store},
	} {
		if service, err := NewClipImportService(deps.settings, deps.dest, deps.notes, deps.store); service != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("invalid dependencies accepted: %v %v", service, err)
		}
	}
	_, err := f.service.Status(context.Background(), "invalid")
	requireClipperDestinationCode(t, err, model.ClipperInvalidRequest)
	_, err = f.service.Status(context.Background(), validClipManifest().ImportID)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown status = %v", err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = f.service.Status(context.Background(), validClipManifest().ImportID)
	if err == nil || err.Error() != "Clipper import failed" {
		t.Fatalf("unsafe IO error = %v", err)
	}
}

func TestClipImportExcludedNoteParentsRejectedBeforeClaim(t *testing.T) {
	for _, base := range []string{"active", "target"} {
		for _, parent := range []string{".hidden", "assets", "ASSETS", "nested/.cache/deep", "nested/assets/x", "nested/AsSeTs/x"} {
			t.Run(base+"/"+parent, func(t *testing.T) {
				f := newClipImportFixture(t)
				root := f.target
				if base == "active" {
					root = f.active
				}
				if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(parent)), 0700); err != nil {
					t.Fatal(err)
				}
				m := f.manifest(t, base)
				m.Parent = parent
				// This is a publication policy, not a change to syntax or digest.
				if _, err := ClipperManifestDigest(m); err != nil {
					t.Fatalf("syntax/digest changed: %v", err)
				}
				paths := func() []string {
					var names []string
					if err := filepath.WalkDir(root, func(name string, _ fs.DirEntry, err error) error {
						if err == nil {
							names = append(names, name)
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
					return names
				}
				before := paths()
				f.service.prepare = func(*os.Root, string, string, string) (*clipNoteFile, error) {
					t.Fatal("excluded parent reached filesystem preparation")
					return nil, os.ErrInvalid
				}
				f.service.store = &hookedClipImportStore{ClipImportStore: f.store, claim: func(repository.ClipImportRecord) error { t.Fatal("excluded parent reached Claim"); return nil }}
				_, _, err := f.service.Import(context.Background(), m)
				requireClipperDestinationCode(t, err, model.ClipperInvalidRequest)
				if !reflect.DeepEqual(before, paths()) {
					t.Fatal("excluded import changed filesystem")
				}
				if _, found, err := f.store.Get(context.Background(), m.ImportID); err != nil || found {
					t.Fatalf("excluded import journaled: %v, %v", found, err)
				}
			})
		}
	}
}

func TestClipImportVisibleNestedParentsAppearInTree(t *testing.T) {
	// Test each spelling independently on case-insensitive filesystems too.
	for _, filename := range []string{".hidden.md", "assets.md", "ASSETS.md"} {
		t.Run(filename, func(t *testing.T) {
			f := newClipImportFixture(t)
			for _, dir := range []string{"nested/normal", ".hidden/deep", "assets/images", "nested/.cache/deep", "nested/ASSETS/x"} {
				if err := os.MkdirAll(filepath.Join(f.active, filepath.FromSlash(dir)), 0700); err != nil {
					t.Fatal(err)
				}
			}
			listing, err := f.dest.Folders("active")
			if err != nil || !reflect.DeepEqual(listing.Folders, []string{"", "nested", "nested/normal"}) {
				t.Fatalf("discovery = %+v, %v", listing, err)
			}
			m := f.manifest(t, "active")
			m.Parent, m.Filename = "nested/normal", filename
			status, _, err := f.service.Import(context.Background(), m)
			if err != nil || status.Result == nil || status.Result.Path != "nested/normal/"+filename {
				t.Fatalf("visible nested import = %+v, %v", status, err)
			}
			tree, err := f.notes.GetTree()
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			var walk func([]model.NoteNode)
			walk = func(nodes []model.NoteNode) {
				for _, node := range nodes {
					ids = append(ids, node.ID)
					walk(node.Children)
				}
			}
			walk(tree)
			found := false
			for _, id := range ids {
				if id == "nested/normal/"+filename {
					found = true
				}
			}
			if !found {
				t.Fatalf("note missing from GetTree: %v", ids)
			}
			for _, id := range ids {
				if strings.Contains(id, ".cache") || strings.HasPrefix(id, ".hidden/") || id == ".hidden" || id == "assets" || strings.HasPrefix(id, "nested/ASSETS") {
					t.Fatalf("index visibility expanded: %v", ids)
				}
			}
		})
	}
}

func TestClipImportExcludedParentHistoricalReceiptReplay(t *testing.T) {
	for _, parent := range []string{".hidden", "nested/assets/x"} {
		t.Run(parent, func(t *testing.T) {
			f := newClipImportFixture(t)
			ctx := context.Background()
			original := f.manifest(t, "target")
			if status, _, err := f.service.Import(ctx, original); err != nil || status.State != model.ClipperImportSucceeded {
				t.Fatalf("seed import = %+v, %v", status, err)
			}
			record, _, err := f.store.Get(ctx, original.ImportID)
			if err != nil {
				t.Fatal(err)
			}
			// Seed a coherent historical receipt through the real guarded repository.
			legacy := original
			legacy.ImportID, legacy.Parent = "550e8400-e29b-41d4-a716-446655440001", parent
			digest, err := ClipperManifestDigest(legacy)
			if err != nil {
				t.Fatal(err)
			}
			record.ImportID, record.RequestDigest, record.NotePath = legacy.ImportID, digest, path.Join(parent, legacy.Filename)
			var plan clipImportPlan
			if err := json.Unmarshal(record.Journal.Plan, &plan); err != nil {
				t.Fatal(err)
			}
			plan.Note.Parent = parent
			record.Journal.Plan, _ = json.Marshal(plan)
			record.Journal.Checkpoint = json.RawMessage(`{}`)
			record.State, record.Result, record.Error = repository.ClipImportPrepared, nil, nil
			record, _, err = f.store.Claim(ctx, record)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.Transition(ctx, record.ImportID, record.Version(), repository.ClipImportPublishing, nil, nil); err != nil {
				t.Fatal(err)
			}
			record, _, err = f.store.Get(ctx, record.ImportID)
			if err != nil {
				t.Fatal(err)
			}
			result := &model.ClipperImportResult{ImportID: record.ImportID, Base: record.BaseName, Path: record.NotePath, Revision: record.NoteContentHash, Warnings: []string{}}
			if err := f.store.Transition(ctx, record.ImportID, record.Version(), repository.ClipImportSucceeded, result, nil); err != nil {
				t.Fatal(err)
			}
			status, replay, err := f.service.Import(ctx, legacy)
			if err != nil || !replay || status.State != model.ClipperImportSucceeded || !reflect.DeepEqual(status.Result, result) {
				t.Fatalf("historical receipt rewritten: %+v, %v, %v", status, replay, err)
			}
			entries, err := os.ReadDir(f.target)
			if err != nil || len(entries) != 2 {
				t.Fatalf("replay wrote excluded parent: %v, %v", entries, err)
			}
		})
	}
}

func TestClipImportUnfinishedRootMoveRebind(t *testing.T) {
	for _, state := range []repository.ClipImportState{repository.ClipImportPublishing, repository.ClipImportRecoveryRequired} {
		t.Run(string(state), func(t *testing.T) {
			f := newClipImportFixture(t)
			ctx := context.Background()
			old := f.manifest(t, "target")
			// Retain a real durable unfinished operation, before any file creation.
			f.service.store = &hookedClipImportStore{ClipImportStore: f.store, claim: func(record repository.ClipImportRecord) error {
				stored, _, err := f.store.Claim(ctx, record)
				if err != nil {
					t.Fatal(err)
				}
				var public *model.APIError
				if state == repository.ClipImportRecoveryRequired {
					public = &model.APIError{Code: string(model.ClipperRecoveryRequired), Message: "Clipper import requires recovery"}
				}
				if err := f.store.Transition(ctx, stored.ImportID, stored.Version(), state, nil, public); err != nil {
					t.Fatal(err)
				}
				return errors.New("lost claim acknowledgement")
			}}
			_, _, err := f.service.Import(ctx, old)
			requireClipperDestinationCode(t, err, model.ClipperRecoveryRequired)
			f.service.store = f.store
			stored, found, err := f.store.Get(ctx, old.ImportID)
			if err != nil || !found || stored.State != state {
				t.Fatalf("unfinished = %+v, %v", stored, err)
			}
			moved := filepath.Join(filepath.Dir(f.target), filepath.Base(f.target)+"-moved")
			if err := os.Rename(f.target, moved); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(moved) })
			if _, err := f.settings.UpdateBase("target", model.BaseUpdateRequest{Name: "target", Path: moved}); err != nil {
				t.Fatal(err)
			}
			m := f.manifest(t, "target")
			if m.DestinationRevision == old.DestinationRevision {
				t.Fatal("move retained destination revision")
			}
			m.ImportID = "550e8400-e29b-41d4-a716-446655440001"
			m.Filename = "after-move.md"
			_, _, err = f.service.Import(ctx, m)
			requireClipperDestinationCode(t, err, model.ClipperRecoveryRequired)
			entries, err := os.ReadDir(moved)
			if err != nil || len(entries) != 1 {
				t.Fatalf("blocked root mutated: %v, %v", entries, err)
			}
			if _, found, err := f.store.Get(ctx, m.ImportID); err != nil || found {
				t.Fatalf("blocked import claimed: %v, %v", found, err)
			}
			// A different root remains usable; the unfinished operation is not a
			// global publication gate when its identity is valid and unrelated.
			other := f.manifest(t, "active")
			other.ImportID = "550e8400-e29b-41d4-a716-446655440002"
			status, _, err := f.service.Import(ctx, other)
			if err != nil || status.State != model.ClipperImportSucceeded {
				t.Fatalf("unrelated root blocked: %+v, %v", status, err)
			}
			// Reusing the base name at a distinct path/identity is also unrelated.
			unrelated := t.TempDir()
			if _, err := f.settings.UpdateBase("target", model.BaseUpdateRequest{Name: "target", Path: unrelated}); err != nil {
				t.Fatal(err)
			}
			other = f.manifest(t, "target")
			other.ImportID = "550e8400-e29b-41d4-a716-446655440003"
			status, _, err = f.service.Import(ctx, other)
			if err != nil || status.State != model.ClipperImportSucceeded {
				t.Fatalf("same base name at unrelated root blocked: %+v, %v", status, err)
			}
			// Canonical-path matching remains conservative even when that path
			// has been replaced by a physically different directory.
			if err := os.Mkdir(f.target, 0700); err != nil {
				t.Fatal(err)
			}
			movedInfo, err := os.Stat(moved)
			if err != nil {
				t.Fatal(err)
			}
			replacementInfo, err := os.Stat(f.target)
			if err != nil || os.SameFile(movedInfo, replacementInfo) {
				t.Fatalf("replacement identity: %v", err)
			}
			if _, err := f.settings.UpdateBase("target", model.BaseUpdateRequest{Name: "target", Path: f.target}); err != nil {
				t.Fatal(err)
			}
			other = f.manifest(t, "target")
			other.ImportID = "550e8400-e29b-41d4-a716-446655440004"
			_, _, err = f.service.Import(ctx, other)
			requireClipperDestinationCode(t, err, model.ClipperRecoveryRequired)
			entries, err = os.ReadDir(f.target)
			if err != nil || len(entries) != 0 {
				t.Fatalf("historical path failed open: %v, %v", entries, err)
			}
		})
	}
}

func TestClipImportUnfinishedInvalidRootIdentityFailsClosed(t *testing.T) {
	for _, identity := range []string{" ", "not-json", "null", "{}", `{"device":0,"file":0}`, `{"device":1}`, `{"device":null,"file":1}`, `{"device":"1","file":2}`, `{"device":1,"file":2,"unknown":3}`, `{"device":1,"file":2} {}`, `{"device":1,"device":2,"file":3}`} {
		t.Run(identity, func(t *testing.T) {
			f := newClipImportFixture(t)
			ctx := context.Background()
			m := f.manifest(t, "target")
			f.service.store = &hookedClipImportStore{ClipImportStore: f.store, claim: func(record repository.ClipImportRecord) error {
				if _, _, err := f.store.Claim(ctx, record); err != nil {
					t.Fatal(err)
				}
				return errors.New("interrupted claim")
			}}
			_, _, err := f.service.Import(ctx, m)
			requireClipperDestinationCode(t, err, model.ClipperRecoveryRequired)
			// Simulate corrupt durable metadata. Repository APIs deliberately cannot
			// mutate this immutable field, so inject corruption directly in SQLite.
			if _, err := f.db.ExecContext(ctx, "UPDATE clip_imports SET root_identity=? WHERE import_id=?", identity, m.ImportID); err != nil {
				t.Fatal(err)
			}
			f.service.store = f.store
			other := f.manifest(t, "active")
			other.ImportID = "550e8400-e29b-41d4-a716-446655440001"
			_, _, err = f.service.Import(ctx, other)
			requireClipperDestinationCode(t, err, model.ClipperRecoveryRequired)
			entries, err := os.ReadDir(f.active)
			if err != nil || len(entries) != 1 {
				t.Fatalf("invalid identity failed open: %v, %v", entries, err)
			}
		})
	}
}
