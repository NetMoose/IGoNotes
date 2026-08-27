package service

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

func TestSettingsServiceFailedSavePreservesExactSQLiteIndexTransaction(t *testing.T) {
	db, err := repository.InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO notes (id, title, path, parent_id, type, created_at, updated_at)
		VALUES ('stale.md', 'stale title', 'stale.md', NULL, 'file', '2001-02-03 04:05:06', '2007-08-09 10:11:12');
		INSERT INTO tags (note_id, tag) VALUES ('stale.md', 'keep-tag');
	`); err != nil {
		t.Fatalf("seed metadata error = %v", err)
	}
	wantNotes := queryStringRows(t, db, "SELECT id, title, path, COALESCE(parent_id, ''), type, quote(created_at), quote(updated_at) FROM notes ORDER BY id", 7)
	wantTags := queryStringRows(t, db, "SELECT note_id, tag FROM tags ORDER BY note_id, tag", 2)

	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "disk-only.md", "not in the stale index")
	writeTestNote(t, target, "candidate.md", "candidate")
	notes := NewNoteService(repository.NewNoteRepository(db), oldBase)
	defer notes.Close()
	completed := true
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: oldBase}, {Name: "target", Path: target}},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config, saveErr: errors.New("save failed")}
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	if _, err := settings.SwitchBase("target"); !errors.Is(err, store.saveErr) {
		t.Fatalf("SwitchBase() error = %v, want %v", err, store.saveErr)
	}
	gotNotes := queryStringRows(t, db, "SELECT id, title, path, COALESCE(parent_id, ''), type, quote(created_at), quote(updated_at) FROM notes ORDER BY id", 7)
	gotTags := queryStringRows(t, db, "SELECT note_id, tag FROM tags ORDER BY note_id, tag", 2)
	if !reflect.DeepEqual(gotNotes, wantNotes) {
		t.Errorf("notes after rollback = %#v, want exact snapshot %#v", gotNotes, wantNotes)
	}
	if !reflect.DeepEqual(gotTags, wantTags) {
		t.Errorf("tags after rollback = %#v, want exact snapshot %#v", gotTags, wantTags)
	}
}

func TestSettingsServiceSaveErrRollbackFailedDoesNotLatchDegradedState(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "old.md", "old")
	writeTestNote(t, target, "new.md", "new")
	repo := &fakeNoteRepository{}
	notes := newTestNoteService(t, repo, oldBase)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	completed := true
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: oldBase}, {Name: "target", Path: target}},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	saveCause := errors.New("store-specific rollback marker")
	store := &fakeConfigStore{config: &config, saveErr: errors.Join(ErrRollbackFailed, saveCause)}
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	if _, err := settings.SwitchBase("target"); !errors.Is(err, saveCause) {
		t.Fatalf("first SwitchBase() error = %v, want save cause %v", err, saveCause)
	}
	store.saveErr = nil
	response, err := settings.SwitchBase("target")
	if err != nil {
		t.Fatalf("second SwitchBase() error = %v, want mutation attempted", err)
	}
	if store.saveCalls != 2 {
		t.Errorf("Save calls = %d, want 2", store.saveCalls)
	}
	if response.BasePath != target || response.Config.CurrentBase != "target" {
		t.Errorf("response = path %q current %q, want %q / target", response.BasePath, response.Config.CurrentBase, target)
	}
}

func TestNoteServiceRollbackFailureFailsAllOperationsClosed(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "old.md", "old")
	writeTestNote(t, target, "new.md", "new")
	rollbackErr := errors.New("index rollback failed")
	repo := &fakeNoteRepository{rollbackErr: rollbackErr}
	notes := NewNoteService(repo, oldBase)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	completed := true
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: oldBase}, {Name: "target", Path: target}},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config, saveErr: errors.New("save failed")}
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	if _, err := settings.SwitchBase("target"); !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("SwitchBase() error = %v, want ErrRollbackFailed", err)
	}

	operations := []struct {
		name string
		call func() error
	}{
		{name: "get note", call: func() error { _, err := notes.GetNoteContent("old.md"); return err }},
		{name: "get tree", call: func() error { _, err := notes.GetTree(); return err }},
		{name: "get absolute path", call: func() error { _, err := notes.GetAbsoluteFilePath("old.md"); return err }},
		{name: "open raw", call: func() error {
			file, _, err := notes.OpenRawFile("old.md")
			if file != nil {
				_ = file.Close()
			}
			return err
		}},
		{name: "sync", call: notes.SyncFS},
		{name: "switch", call: func() error { return notes.SwitchBase(target) }},
		{name: "save", call: func() error { return notes.SaveNoteContent("old.md", "changed") }},
		{name: "create", call: func() error { _, err := notes.CreateNode("", "created", "file"); return err }},
		{name: "delete", call: func() error { return notes.DeleteNode("old.md") }},
		{name: "rename", call: func() error { return notes.RenameNode("old.md", "renamed") }},
		{name: "asset", call: func() error { _, err := notes.SaveAsset(nilReader{}, "asset.txt"); return err }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.call(); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, rollbackErr) {
				t.Errorf("operation error = %v, want rollback failure with cause", err)
			}
		})
	}
	if err := notes.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := notes.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestNoteServiceIndexCommitFailureFailsClosedWithExactOrder(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "old.md", "old")
	writeTestNote(t, target, "new.md", "new")
	commitErr := errors.New("index commit failed")
	events := &orderedEvents{}
	repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md"}}, commitErr: commitErr, events: events}
	notes := NewNoteService(repo, oldBase)
	defer notes.Close()
	var candidateRoot *os.Root
	notes.openRoot = func(path string) (*os.Root, error) {
		root, err := os.OpenRoot(path)
		candidateRoot = root
		return root, err
	}
	completed := true
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: oldBase}, {Name: "target", Path: target}},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config, events: events}
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	if _, err := settings.SwitchBase("target"); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, commitErr) {
		t.Fatalf("SwitchBase() error = %v, want degraded commit failure", err)
	}
	wantEvents := []string{"prepare:1", "save", "commit:1", "rollback:1"}
	if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Errorf("events = %v, want %v", got, wantEvents)
	}
	if _, err := notes.GetTree(); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, commitErr) {
		t.Errorf("GetTree() error = %v, want fail-closed commit error", err)
	}
	if _, err := notes.GetNoteContent("old.md"); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, commitErr) {
		t.Errorf("GetNoteContent() error = %v, want fail-closed commit error", err)
	}
	if candidateRoot == nil {
		t.Fatal("candidate root was not opened")
	}
	if _, err := candidateRoot.Stat("."); !errors.Is(err, os.ErrClosed) {
		t.Errorf("candidate root Stat() error = %v, want os.ErrClosed", err)
	}
	assertRepositoryIDs(t, repo, "old.md")
}

func TestSettingsServicePreparationRollbackFailureDegradesWithoutSave(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "old.md", "old")
	writeTestNote(t, target, "new.md", "new")
	operationErr := errors.New("prepare candidate failed")
	rollbackErr := errors.New("prepare rollback failed")
	repo := &fakeNoteRepository{
		nodes:              []model.NoteNode{{ID: "old.md"}},
		prepareErr:         operationErr,
		prepareRollbackErr: rollbackErr,
	}
	notes := newTestNoteService(t, repo, oldBase)
	completed := true
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: oldBase}, {Name: "target", Path: target}},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config}
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	if _, err := settings.SwitchBase("target"); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, operationErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("SwitchBase() error = %v, want operation and rollback failure", err)
	}
	if store.saveCalls != 0 {
		t.Errorf("Save calls = %d, want none", store.saveCalls)
	}
	if _, err := notes.GetNoteContent("old.md"); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, operationErr) || !errors.Is(err, rollbackErr) {
		t.Errorf("GetNoteContent() error = %v, want fail-closed preparation errors", err)
	}
	if _, err := settings.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "later", Path: t.TempDir()}); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, operationErr) || !errors.Is(err, rollbackErr) {
		t.Errorf("AddBase() after degradation error = %v, want latched preparation errors", err)
	}
	if store.saveCalls != 0 {
		t.Errorf("Save calls after degraded mutation = %d, want none", store.saveCalls)
	}
}

func TestSettingsServicePreparationFailureWithSuccessfulRollbackRemainsOperational(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "old.md", "old")
	writeTestNote(t, target, "new.md", "new")
	operationErr := errors.New("prepare candidate failed")
	repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md"}}, prepareErr: operationErr}
	notes := newTestNoteService(t, repo, oldBase)
	completed := true
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: oldBase}, {Name: "target", Path: target}},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config}
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	if _, err := settings.SwitchBase("target"); !errors.Is(err, operationErr) || errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("first SwitchBase() error = %v, want operation error only", err)
	}
	if store.saveCalls != 0 {
		t.Errorf("Save calls = %d, want none", store.saveCalls)
	}
	if content, err := notes.GetNoteContent("old.md"); err != nil || content != "old" {
		t.Errorf("GetNoteContent() = %q, %v; want old, nil", content, err)
	}
	repo.mu.Lock()
	repo.prepareErr = nil
	repo.mu.Unlock()
	response, err := settings.SwitchBase("target")
	if err != nil {
		t.Fatalf("second SwitchBase() error = %v", err)
	}
	if store.saveCalls != 1 || response.BasePath != target {
		t.Errorf("Save calls/path = %d / %q, want 1 / %q", store.saveCalls, response.BasePath, target)
	}
}

func TestSettingsServiceConfigOnlyMutationsRejectFailedRuntimeWithoutSave(t *testing.T) {
	operations := []struct {
		name string
		call func(*SettingsService, model.Config, string) error
	}{
		{name: "add", call: func(settings *SettingsService, _ model.Config, path string) error {
			_, err := settings.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "added", Path: path})
			return err
		}},
		{name: "forget inactive", call: func(settings *SettingsService, _ model.Config, _ string) error {
			_, err := settings.ForgetBase("inactive")
			return err
		}},
		{name: "update inactive", call: func(settings *SettingsService, _ model.Config, path string) error {
			_, err := settings.UpdateBase("inactive", model.BaseUpdateRequest{Name: "renamed", Path: path})
			return err
		}},
		{name: "replace without runtime switch", call: func(settings *SettingsService, config model.Config, _ string) error {
			_, err := settings.ReplaceConfig(config)
			return err
		}},
		{name: "switch current base", call: func(settings *SettingsService, _ model.Config, _ string) error {
			_, err := settings.SwitchBase("active")
			return err
		}},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			activePath := t.TempDir()
			inactivePath := t.TempDir()
			mutationPath := t.TempDir()
			writeTestNote(t, activePath, "old.md", "old")
			operationErr := errors.New("prepare index failed")
			rollbackErr := errors.New("prepare rollback failed")
			repo := &fakeNoteRepository{prepareErr: operationErr, prepareRollbackErr: rollbackErr}
			notes := newTestNoteService(t, repo, activePath)
			completed := true
			config := model.Config{
				Bases: []model.Base{
					{Name: "active", Path: activePath},
					{Name: "inactive", Path: inactivePath},
				},
				CurrentBase:    "active",
				SetupCompleted: &completed,
			}
			store := &fakeConfigStore{config: &config}
			settings, err := NewSettingsService(store, notes, "", nil)
			if err != nil {
				t.Fatalf("NewSettingsService() error = %v", err)
			}
			if err := notes.SyncFS(); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, operationErr) || !errors.Is(err, rollbackErr) {
				t.Fatalf("SyncFS() error = %v, want fail-closed preparation errors", err)
			}

			err = operation.call(settings, cloneConfig(config), mutationPath)
			if !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, operationErr) || !errors.Is(err, rollbackErr) {
				t.Fatalf("mutation error = %v, want runtime preparation and rollback errors", err)
			}
			if store.saveCalls != 0 {
				t.Errorf("Save calls = %d, want none", store.saveCalls)
			}
			if !reflect.DeepEqual(settings.GetConfig(), config) || !reflect.DeepEqual(*store.config, config) {
				t.Errorf("config changed after rejected mutation: service %#v store %#v", settings.GetConfig(), *store.config)
			}
		})
	}
}

func TestSettingsServiceConfigOnlyMutationRejectsClosedRuntimeWithoutSave(t *testing.T) {
	activePath := t.TempDir()
	addedPath := t.TempDir()
	notes := NewNoteService(&fakeNoteRepository{}, activePath)
	completed := true
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: activePath}},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	store := &fakeConfigStore{config: &config}
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	if err := notes.Close(); err != nil {
		t.Fatalf("NoteService.Close() error = %v", err)
	}

	if _, err := settings.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "added", Path: addedPath}); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("AddBase() error = %v, want os.ErrClosed", err)
	}
	if store.saveCalls != 0 || !reflect.DeepEqual(settings.GetConfig(), config) {
		t.Errorf("closed runtime mutation changed state: saves %d config %#v", store.saveCalls, settings.GetConfig())
	}
}

func TestSettingsServiceConfigStoreRollbackSentinelDoesNotPoisonRuntime(t *testing.T) {
	activePath := t.TempDir()
	addedPath := t.TempDir()
	writeTestNote(t, activePath, "note.md", "content")
	repo := &fakeNoteRepository{}
	notes := newTestNoteService(t, repo, activePath)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	originalRoot := notes.baseRoot
	completed := true
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: activePath}},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	storeErr := fmt.Errorf("write config: %w", ErrRollbackFailed)
	store := &fakeConfigStore{config: &config, saveErr: storeErr}
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	request := model.BaseMutationRequest{Mode: "connect", Name: "added", Path: addedPath}

	if _, err := settings.AddBase(request); !errors.Is(err, storeErr) {
		t.Fatalf("first AddBase() error = %v, want %v", err, storeErr)
	}
	store.saveErr = nil
	if _, err := settings.AddBase(request); err != nil {
		t.Fatalf("second AddBase() error = %v", err)
	}
	if store.saveCalls != 2 {
		t.Errorf("Save calls = %d, want 2", store.saveCalls)
	}
	if content, err := notes.GetNoteContent("note.md"); err != nil || content != "content" {
		t.Errorf("GetNoteContent() = %q, %v; want content, nil", content, err)
	}
	if notes.baseRoot != originalRoot || notes.GetBasePath() != activePath {
		t.Errorf("runtime changed after config saves: root %p/%p path %q", notes.baseRoot, originalRoot, notes.GetBasePath())
	}
	repo.mu.Lock()
	prepareCalls, commitCalls := repo.prepareCalls, repo.commitCalls
	repo.mu.Unlock()
	if prepareCalls != 1 || commitCalls != 1 {
		t.Errorf("index transactions = prepare %d commit %d, want initial sync only", prepareCalls, commitCalls)
	}
}

func TestNoteServiceDirectIndexPreparationRollbackFailureFailsClosed(t *testing.T) {
	operations := []struct {
		name string
		call func(*NoteService, string) error
	}{
		{name: "sync", call: func(service *NoteService, _ string) error { return service.SyncFS() }},
		{name: "switch", call: func(service *NoteService, target string) error { return service.SwitchBase(target) }},
		{name: "rename index", call: func(service *NoteService, _ string) error { return service.RenameNode("old.md", "renamed") }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			oldBase := t.TempDir()
			target := t.TempDir()
			writeTestNote(t, oldBase, "old.md", "old")
			writeTestNote(t, target, "new.md", "new")
			operationErr := errors.New("prepare index failed")
			rollbackErr := errors.New("prepare rollback failed")
			repo := &fakeNoteRepository{prepareErr: operationErr, prepareRollbackErr: rollbackErr}
			notes := newTestNoteService(t, repo, oldBase)

			if err := operation.call(notes, target); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, operationErr) || !errors.Is(err, rollbackErr) {
				t.Fatalf("operation error = %v, want fail-closed preparation errors", err)
			}
			if _, err := notes.GetTree(); !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, operationErr) || !errors.Is(err, rollbackErr) {
				t.Errorf("GetTree() error = %v, want released gate and preparation errors", err)
			}
			if _, err := notes.GetNoteContent("old.md"); !errors.Is(err, ErrRollbackFailed) {
				t.Errorf("GetNoteContent() error = %v, want ErrRollbackFailed", err)
			}
		})
	}
}

type nilReader struct{}

func (nilReader) Read([]byte) (int, error) { return 0, io.EOF }

func queryStringRows(t *testing.T, db interface {
	Query(string, ...any) (*sql.Rows, error)
}, query string, columns int) [][]string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	defer rows.Close()
	var result [][]string
	for rows.Next() {
		values := make([]string, columns)
		destinations := make([]any, columns)
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Rows error = %v", err)
	}
	return result
}
