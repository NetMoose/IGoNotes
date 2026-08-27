package service

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"IGoNotes/internal/model"
)

type noteReadResult struct {
	content string
	err     error
}

type baseTransactionResult struct {
	operationErr error
	rollbackErr  error
}

func TestNoteServiceBasePersistenceBlocksReadsUntilFailureRollsBack(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "old.md", "old")
	writeTestNote(t, target, "new.md", "new")
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, oldBase)
	if err := service.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}

	persistStarted := make(chan struct{})
	persistRelease := make(chan struct{})
	persistErr := errors.New("disk full")
	store := &fakeConfigStore{saveErr: persistErr, saveStarted: persistStarted, saveRelease: persistRelease}
	transactionDone := make(chan baseTransactionResult, 1)
	go func() {
		operationErr, rollbackErr := service.switchBaseTransaction(target, store, &model.Config{})
		transactionDone <- baseTransactionResult{operationErr: operationErr, rollbackErr: rollbackErr}
	}()
	<-persistStarted

	readAttempted := make(chan struct{})
	service.beforeReadLock = func() { close(readAttempted) }
	readDone := make(chan noteReadResult, 1)
	go func() {
		content, err := service.GetNoteContent("old.md")
		readDone <- noteReadResult{content: content, err: err}
	}()
	<-readAttempted
	treeDone := make(chan error, 1)
	go func() {
		_, err := service.GetTree()
		treeDone <- err
	}()
	for range 10 {
		runtime.Gosched()
	}
	if service.baseMu.TryRLock() {
		service.baseMu.RUnlock()
		t.Fatal("base read lock available while persistence callback is blocked")
	}
	select {
	case result := <-readDone:
		t.Fatalf("GetNoteContent() completed during persistence: %#v", result)
	default:
	}
	select {
	case err := <-treeDone:
		t.Fatalf("GetTree() completed during persistence: %v", err)
	default:
	}

	close(persistRelease)
	transaction := <-transactionDone
	if !errors.Is(transaction.operationErr, persistErr) || transaction.rollbackErr != nil {
		t.Fatalf("switchBaseTransaction() errors = %v, %v; want persist error, nil", transaction.operationErr, transaction.rollbackErr)
	}
	result := <-readDone
	if result.err != nil || result.content != "old" {
		t.Errorf("GetNoteContent() after rollback = %q, %v; want old, nil", result.content, result.err)
	}
	if err := <-treeDone; err != nil {
		t.Fatalf("GetTree() after rollback error = %v", err)
	}
	assertRepositoryIDs(t, repo, "old.md")
	if got := service.GetBasePath(); got != oldBase {
		t.Errorf("GetBasePath() = %q, want %q", got, oldBase)
	}
}

func TestNoteServiceBasePersistencePublishesOnlyAfterSuccess(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "note.md", "old")
	writeTestNote(t, target, "note.md", "new")
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, oldBase)
	if err := service.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	repo.prepareCalls = 0
	repo.commitCalls = 0
	events := &orderedEvents{}
	repo.events = events

	persistStarted := make(chan struct{})
	persistRelease := make(chan struct{})
	store := &fakeConfigStore{saveStarted: persistStarted, saveRelease: persistRelease, events: events}
	done := make(chan baseTransactionResult, 1)
	go func() {
		operationErr, rollbackErr := service.switchBaseTransaction(target, store, &model.Config{})
		done <- baseTransactionResult{operationErr: operationErr, rollbackErr: rollbackErr}
	}()
	<-persistStarted
	readAttempted := make(chan struct{})
	service.beforeReadLock = func() { close(readAttempted) }
	readDone := make(chan noteReadResult, 1)
	go func() {
		content, err := service.GetNoteContent("note.md")
		readDone <- noteReadResult{content: content, err: err}
	}()
	<-readAttempted
	select {
	case result := <-readDone:
		t.Fatalf("GetNoteContent() completed before persistence success: %#v", result)
	default:
	}

	close(persistRelease)
	transaction := <-done
	if transaction.operationErr != nil || transaction.rollbackErr != nil {
		t.Fatalf("switchBaseTransaction() errors = %v, %v", transaction.operationErr, transaction.rollbackErr)
	}
	result := <-readDone
	if result.err != nil || result.content != "new" {
		t.Errorf("GetNoteContent() after commit = %q, %v; want new, nil", result.content, result.err)
	}
	assertRepositoryIDs(t, repo, "note.md")
	wantEvents := []string{"prepare:1", "save", "commit:1"}
	if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Errorf("events = %v, want %v", got, wantEvents)
	}
}

func TestSettingsServiceConfigOnlyPersistenceBlocksConcurrentSync(t *testing.T) {
	activePath := t.TempDir()
	addedPath := t.TempDir()
	writeTestNote(t, activePath, "note.md", "content")
	repo := &fakeNoteRepository{}
	notes := newTestNoteService(t, repo, activePath)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("initial SyncFS() error = %v", err)
	}
	repo.mu.Lock()
	repo.prepareCalls = 0
	repo.commitCalls = 0
	repo.mu.Unlock()
	completed := true
	config := model.Config{
		Bases:          []model.Base{{Name: "active", Path: activePath}},
		CurrentBase:    "active",
		SetupCompleted: &completed,
	}
	persistStarted := make(chan struct{})
	persistRelease := make(chan struct{})
	store := &fakeConfigStore{config: &config, saveStarted: persistStarted, saveRelease: persistRelease}
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	persistDone := make(chan error, 1)
	go func() {
		_, err := settings.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "added", Path: addedPath})
		persistDone <- err
	}()
	<-persistStarted

	baseLockAvailable := notes.baseMu.TryLock()
	if baseLockAvailable {
		notes.baseMu.Unlock()
	}
	syncStarted := make(chan struct{})
	syncDone := make(chan error, 1)
	go func() {
		close(syncStarted)
		syncDone <- notes.SyncFS()
	}()
	<-syncStarted
	for range 10 {
		runtime.Gosched()
	}
	syncCompletedEarly := false
	select {
	case err := <-syncDone:
		syncCompletedEarly = true
		t.Errorf("SyncFS() completed during config persistence: %v", err)
	default:
	}

	close(persistRelease)
	if err := <-persistDone; err != nil {
		t.Fatalf("AddBase() error = %v", err)
	}
	if !syncCompletedEarly {
		if err := <-syncDone; err != nil {
			t.Fatalf("SyncFS() after persistence error = %v", err)
		}
	}
	if baseLockAvailable {
		t.Error("runtime base lock was available while config persistence was blocked")
	}
	if store.saveCalls != 1 {
		t.Errorf("Save calls = %d, want 1", store.saveCalls)
	}
	repo.mu.Lock()
	prepareCalls, commitCalls := repo.prepareCalls, repo.commitCalls
	repo.mu.Unlock()
	if prepareCalls != 1 || commitCalls != 1 {
		t.Errorf("SyncFS transaction calls = prepare %d commit %d, want 1 and 1", prepareCalls, commitCalls)
	}
}

func TestNoteServiceBasePersistenceRollbackRetainsPinnedOldRoot(t *testing.T) {
	parent := t.TempDir()
	oldPath := filepath.Join(parent, "base")
	if err := os.Mkdir(oldPath, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeTestNote(t, oldPath, "note.md", "original")
	target := t.TempDir()
	writeTestNote(t, target, "note.md", "candidate")
	replacementSource := t.TempDir()
	writeTestNote(t, replacementSource, "note.md", "replacement")
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, oldPath)
	if err := service.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}

	persistStarted := make(chan struct{})
	persistRelease := make(chan struct{})
	persistErr := errors.New("save failed")
	store := &fakeConfigStore{saveErr: persistErr, saveStarted: persistStarted, saveRelease: persistRelease}
	done := make(chan baseTransactionResult, 1)
	go func() {
		operationErr, rollbackErr := service.switchBaseTransaction(target, store, &model.Config{})
		done <- baseTransactionResult{operationErr: operationErr, rollbackErr: rollbackErr}
	}()
	<-persistStarted
	originalPath := filepath.Join(parent, "original")
	if err := os.Rename(oldPath, originalPath); err != nil {
		t.Fatalf("Rename(old base) error = %v", err)
	}
	if err := os.Rename(replacementSource, oldPath); err != nil {
		t.Fatalf("Rename(replacement) error = %v", err)
	}
	close(persistRelease)
	transaction := <-done
	if !errors.Is(transaction.operationErr, persistErr) || transaction.rollbackErr != nil {
		t.Fatalf("switchBaseTransaction() errors = %v, %v; want %v, nil", transaction.operationErr, transaction.rollbackErr, persistErr)
	}

	if got := service.GetBasePath(); got != oldPath {
		t.Errorf("GetBasePath() = %q, want old logical path %q", got, oldPath)
	}
	content, err := service.GetNoteContent("note.md")
	if err != nil || content != "original" {
		t.Errorf("GetNoteContent() = %q, %v; want pinned original, nil", content, err)
	}
	if err := service.SaveNoteContent("created.md", "original only"); err != nil {
		t.Fatalf("SaveNoteContent() error = %v", err)
	}
	assertFileContent(t, filepath.Join(originalPath, "created.md"), []byte("original only"))
	if _, err := os.Stat(filepath.Join(oldPath, "created.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("replacement base was mutated, Stat error = %v", err)
	}
	assertFileContent(t, filepath.Join(oldPath, "note.md"), []byte("replacement"))
}

func TestNoteServiceBasePersistenceFailurePhases(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "old.md", "old")
	writeTestNote(t, target, "new.md", "new")

	t.Run("candidate replace skips persist", func(t *testing.T) {
		replaceErr := errors.New("candidate replace failed")
		repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md"}}, prepareErr: replaceErr}
		service := newTestNoteService(t, repo, oldBase)
		store := &fakeConfigStore{}
		operationErr, rollbackErr := service.switchBaseTransaction(target, store, &model.Config{})
		if !errors.Is(operationErr, replaceErr) || rollbackErr != nil {
			t.Fatalf("switchBaseTransaction() errors = %v, %v; want %v, nil", operationErr, rollbackErr, replaceErr)
		}
		if store.saveCalls != 0 {
			t.Errorf("Save calls = %d, want none", store.saveCalls)
		}
		if got := service.GetBasePath(); got != oldBase {
			t.Errorf("GetBasePath() = %q, want %q", got, oldBase)
		}
		assertRepositoryIDs(t, repo, "old.md")
	})

	t.Run("rollback failure reports both causes and keeps committed index", func(t *testing.T) {
		persistErr := errors.New("save failed")
		rollbackErr := errors.New("rollback replace failed")
		events := &orderedEvents{}
		repo := &fakeNoteRepository{
			nodes:       []model.NoteNode{{ID: "old.md"}},
			rollbackErr: rollbackErr,
			events:      events,
		}
		service := newTestNoteService(t, repo, oldBase)
		store := &fakeConfigStore{saveErr: persistErr, events: events}
		operationErr, gotRollbackErr := service.switchBaseTransaction(target, store, &model.Config{})
		if !errors.Is(operationErr, persistErr) || !errors.Is(gotRollbackErr, rollbackErr) {
			t.Fatalf("switchBaseTransaction() errors = %v, %v; want persist and rollback causes", operationErr, gotRollbackErr)
		}
		if got := service.GetBasePath(); got != oldBase {
			t.Errorf("GetBasePath() = %q, want retained %q", got, oldBase)
		}
		assertRepositoryIDs(t, repo, "old.md")
		wantEvents := []string{"prepare:1", "save", "rollback:1"}
		if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
			t.Errorf("events = %v, want %v", got, wantEvents)
		}
	})
}

func TestNoteServiceBasePersistenceEmptyRuntimeRollback(t *testing.T) {
	repo := &fakeNoteRepository{}
	service := newTestNoteService(t, repo, "")
	target := t.TempDir()
	writeTestNote(t, target, "new.md", "new")
	persistErr := errors.New("save failed")

	store := &fakeConfigStore{saveErr: persistErr}
	operationErr, rollbackErr := service.switchBaseTransaction(target, store, &model.Config{})
	if !errors.Is(operationErr, persistErr) || rollbackErr != nil {
		t.Fatalf("switchBaseTransaction() errors = %v, %v; want %v, nil", operationErr, rollbackErr, persistErr)
	}
	if got := service.GetBasePath(); got != "" {
		t.Errorf("GetBasePath() = %q, want empty", got)
	}
	assertRepositoryIDs(t, repo)
	if _, err := service.GetNoteContent("new.md"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("GetNoteContent() error = %v, want os.ErrNotExist", err)
	}
}

func TestSettingsServiceSwitchingMutationsKeepNoteReadsAtomicWithSave(t *testing.T) {
	mutations := []struct {
		name      string
		completed bool
		call      func(*SettingsService, string) error
	}{
		{name: "switch", completed: true, call: func(s *SettingsService, _ string) error { _, err := s.SwitchBase("target"); return err }},
		{name: "update", completed: true, call: func(s *SettingsService, target string) error {
			_, err := s.UpdateBase("active", model.BaseUpdateRequest{Name: "active", Path: target})
			return err
		}},
		{name: "replace", completed: true, call: func(s *SettingsService, target string) error {
			_, err := s.ReplaceConfig(model.Config{Bases: []model.Base{{Name: "target", Path: target}}, CurrentBase: "target"})
			return err
		}},
		{name: "complete setup", completed: false, call: func(s *SettingsService, target string) error {
			_, err := s.CompleteSetup(model.BaseMutationRequest{Mode: "connect", Name: "target", Path: target})
			return err
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			oldBase := t.TempDir()
			target := t.TempDir()
			writeTestNote(t, oldBase, "note.md", "old")
			writeTestNote(t, oldBase, "old-index.md", "old index")
			writeTestNote(t, target, "note.md", "new")
			writeTestNote(t, target, "candidate-index.md", "candidate index")
			repo := &fakeNoteRepository{}
			notes := newTestNoteService(t, repo, oldBase)
			if err := notes.SyncFS(); err != nil {
				t.Fatalf("SyncFS() error = %v", err)
			}
			completed := mutation.completed
			config := model.Config{
				Bases:          []model.Base{{Name: "active", Path: oldBase}, {Name: "target", Path: target}},
				CurrentBase:    "active",
				SetupCompleted: &completed,
			}
			store := &fakeConfigStore{config: &config, saveErr: errors.New("save failed"), saveStarted: make(chan struct{})}
			release := make(chan struct{})
			store.saveRelease = release
			settings, err := NewSettingsService(store, notes, "", nil)
			if err != nil {
				t.Fatalf("NewSettingsService() error = %v", err)
			}
			done := make(chan error, 1)
			go func() { done <- mutation.call(settings, target) }()
			<-store.saveStarted

			if notes.baseMu.TryRLock() {
				notes.baseMu.RUnlock()
				close(release)
				<-done
				t.Fatal("note reads were not gated while ConfigStore.Save was blocked")
			}
			readDone := make(chan noteReadResult, 1)
			go func() {
				content, err := notes.GetNoteContent("note.md")
				readDone <- noteReadResult{content: content, err: err}
			}()
			treeDone := make(chan error, 1)
			go func() {
				_, err := notes.GetTree()
				treeDone <- err
			}()
			select {
			case err := <-treeDone:
				close(release)
				<-done
				t.Fatalf("GetTree() completed while ConfigStore.Save was blocked: %v", err)
			default:
			}
			close(release)
			if err := <-done; !errors.Is(err, store.saveErr) {
				t.Fatalf("mutation error = %v, want %v", err, store.saveErr)
			}
			result := <-readDone
			if result.err != nil || result.content != "old" {
				t.Errorf("GetNoteContent() after rollback = %q, %v; want old, nil", result.content, result.err)
			}
			if err := <-treeDone; err != nil {
				t.Fatalf("GetTree() after rollback error = %v", err)
			}
			assertRepositoryIDs(t, repo, "note.md", "old-index.md")
		})
	}
}

func TestSettingsServiceSuccessfulBlockedSavePublishesCandidateAfterCommit(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "note.md", "old")
	writeTestNote(t, target, "note.md", "new")
	repo := &fakeNoteRepository{}
	notes := newTestNoteService(t, repo, oldBase)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	completed := true
	config := model.Config{Bases: []model.Base{{Name: "active", Path: oldBase}, {Name: "target", Path: target}}, CurrentBase: "active", SetupCompleted: &completed}
	store := &fakeConfigStore{config: &config, saveStarted: make(chan struct{})}
	release := make(chan struct{})
	store.saveRelease = release
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	switchDone := make(chan error, 1)
	go func() { _, err := settings.SwitchBase("target"); switchDone <- err }()
	<-store.saveStarted
	readAttempted := make(chan struct{})
	notes.beforeReadLock = func() { close(readAttempted) }
	readDone := make(chan noteReadResult, 1)
	go func() {
		content, err := notes.GetNoteContent("note.md")
		readDone <- noteReadResult{content: content, err: err}
	}()
	<-readAttempted
	select {
	case result := <-readDone:
		close(release)
		<-switchDone
		t.Fatalf("GetNoteContent() completed before ConfigStore.Save committed: %#v", result)
	default:
	}

	close(release)
	if err := <-switchDone; err != nil {
		t.Fatalf("SwitchBase() error = %v", err)
	}
	result := <-readDone
	if result.err != nil || result.content != "new" {
		t.Errorf("GetNoteContent() after commit = %q, %v; want new, nil", result.content, result.err)
	}
	if got := notes.GetBasePath(); got != target {
		t.Errorf("GetBasePath() = %q, want %q", got, target)
	}
}

func TestSettingsServiceCandidateIndexFailureSkipsSaveAndPreservesOldRuntime(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "old.md", "old")
	writeTestNote(t, target, "new.md", "new")
	replaceErr := errors.New("candidate index failed")
	repo := &fakeNoteRepository{prepareErrs: []error{nil, replaceErr}}
	notes := newTestNoteService(t, repo, oldBase)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	completed := true
	config := model.Config{Bases: []model.Base{{Name: "active", Path: oldBase}, {Name: "target", Path: target}}, CurrentBase: "active", SetupCompleted: &completed}
	store := &fakeConfigStore{config: &config}
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	if _, err := settings.SwitchBase("target"); !errors.Is(err, replaceErr) {
		t.Fatalf("SwitchBase() error = %v, want %v", err, replaceErr)
	}
	if store.saveCalls != 0 {
		t.Errorf("Save calls = %d, want none", store.saveCalls)
	}
	if got := notes.GetBasePath(); got != oldBase {
		t.Errorf("GetBasePath() = %q, want %q", got, oldBase)
	}
	assertRepositoryIDs(t, repo, "old.md")
}

func TestSettingsServiceRollbackRetainsPinnedRuntimeAcrossOldPathReplacement(t *testing.T) {
	parent := t.TempDir()
	oldPath := filepath.Join(parent, "active")
	if err := os.Mkdir(oldPath, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeTestNote(t, oldPath, "note.md", "original")
	target := t.TempDir()
	writeTestNote(t, target, "note.md", "candidate")
	replacement := t.TempDir()
	writeTestNote(t, replacement, "note.md", "replacement")
	repo := &fakeNoteRepository{}
	notes := newTestNoteService(t, repo, oldPath)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	completed := true
	config := model.Config{Bases: []model.Base{{Name: "active", Path: oldPath}, {Name: "target", Path: target}}, CurrentBase: "active", SetupCompleted: &completed}
	store := &fakeConfigStore{config: &config, saveErr: errors.New("save failed"), saveStarted: make(chan struct{})}
	release := make(chan struct{})
	store.saveRelease = release
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := settings.SwitchBase("target"); done <- err }()
	<-store.saveStarted
	originalPath := filepath.Join(parent, "original")
	if err := os.Rename(oldPath, originalPath); err != nil {
		t.Fatalf("Rename(old path) error = %v", err)
	}
	if err := os.Rename(replacement, oldPath); err != nil {
		t.Fatalf("Rename(replacement) error = %v", err)
	}
	close(release)
	if err := <-done; !errors.Is(err, store.saveErr) {
		t.Fatalf("SwitchBase() error = %v, want %v", err, store.saveErr)
	}
	if got := notes.GetBasePath(); got != oldPath {
		t.Errorf("GetBasePath() = %q, want %q", got, oldPath)
	}
	content, err := notes.GetNoteContent("note.md")
	if err != nil || content != "original" {
		t.Errorf("GetNoteContent() = %q, %v; want original, nil", content, err)
	}
	assertFileContent(t, filepath.Join(oldPath, "note.md"), []byte("replacement"))
	assertFileContent(t, filepath.Join(originalPath, "note.md"), []byte("original"))
}

func TestSettingsServiceCanonicalEquivalentSwitchPublishesCanonicalPathTransactionally(t *testing.T) {
	physical := t.TempDir()
	writeTestNote(t, physical, "note.md", "physical")
	alternate := t.TempDir()
	writeTestNote(t, alternate, "note.md", "alternate")
	alias := filepath.Join(t.TempDir(), "alias")
	createSymlinkOrSkip(t, physical, alias)
	completed := true
	config := model.Config{Bases: []model.Base{{Name: "active", Path: alias}}, CurrentBase: "active", SetupCompleted: &completed}

	t.Run("success publishes canonical descriptor and path", func(t *testing.T) {
		repo := &fakeNoteRepository{}
		notes := newTestNoteService(t, repo, alias)
		if err := notes.SyncFS(); err != nil {
			t.Fatalf("SyncFS() error = %v", err)
		}
		store := &fakeConfigStore{config: &config}
		settings, err := NewSettingsService(store, notes, "", nil)
		if err != nil {
			t.Fatalf("NewSettingsService() error = %v", err)
		}
		if _, err := settings.UpdateBase("active", model.BaseUpdateRequest{Name: "active", Path: physical}); err != nil {
			t.Fatalf("UpdateBase() error = %v", err)
		}
		if got := notes.GetBasePath(); got != physical {
			t.Errorf("GetBasePath() = %q, want canonical %q", got, physical)
		}
		retargetSymlink(t, alias, alternate)
		content, err := notes.GetNoteContent("note.md")
		if err != nil || content != "physical" {
			t.Errorf("GetNoteContent() after alias retarget = %q, %v; want physical, nil", content, err)
		}
	})

	t.Run("save failure retains alias path and pinned descriptor", func(t *testing.T) {
		if target, err := filepath.EvalSymlinks(alias); err == nil && target != physical {
			retargetSymlink(t, alias, physical)
		}
		repo := &fakeNoteRepository{}
		notes := newTestNoteService(t, repo, alias)
		if err := notes.SyncFS(); err != nil {
			t.Fatalf("SyncFS() error = %v", err)
		}
		originalConfig := cloneConfig(config)
		store := &fakeConfigStore{config: &originalConfig, saveErr: errors.New("save failed"), saveStarted: make(chan struct{})}
		release := make(chan struct{})
		store.saveRelease = release
		settings, err := NewSettingsService(store, notes, "", nil)
		if err != nil {
			t.Fatalf("NewSettingsService() error = %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := settings.UpdateBase("active", model.BaseUpdateRequest{Name: "active", Path: physical})
			done <- err
		}()
		<-store.saveStarted
		retargetSymlink(t, alias, alternate)
		close(release)
		if err := <-done; !errors.Is(err, store.saveErr) {
			t.Fatalf("UpdateBase() error = %v, want %v", err, store.saveErr)
		}
		if got := notes.GetBasePath(); got != alias {
			t.Errorf("GetBasePath() = %q, want retained alias %q", got, alias)
		}
		if !reflect.DeepEqual(settings.GetConfig(), originalConfig) || !reflect.DeepEqual(*store.config, originalConfig) {
			t.Errorf("config changed after failed save: service %#v store %#v", settings.GetConfig(), *store.config)
		}
		content, err := notes.GetNoteContent("note.md")
		if err != nil || content != "physical" {
			t.Errorf("GetNoteContent() after failed canonicalization = %q, %v; want physical, nil", content, err)
		}
	})
}

func TestSettingsServiceRealRuntimeRollbackFailureDegradesMutations(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	writeTestNote(t, oldBase, "old.md", "old")
	writeTestNote(t, target, "new.md", "new")
	persistErr := errors.New("save failed")
	rollbackErr := errors.New("repository rollback failed")
	repo := &fakeNoteRepository{rollbackErr: rollbackErr}
	notes := newTestNoteService(t, repo, oldBase)
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("SyncFS() error = %v", err)
	}
	completed := true
	config := model.Config{Bases: []model.Base{{Name: "active", Path: oldBase}, {Name: "target", Path: target}}, CurrentBase: "active", SetupCompleted: &completed}
	store := &fakeConfigStore{config: &config, saveErr: persistErr}
	settings, err := NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}

	_, err = settings.SwitchBase("target")
	if !errors.Is(err, ErrRollbackFailed) || !errors.Is(err, persistErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("SwitchBase() error = %v, want rollback, persist, and repository causes", err)
	}
	if got := notes.GetBasePath(); got != oldBase {
		t.Errorf("runtime path = %q, want retained old path %q", got, oldBase)
	}
	assertRepositoryIDs(t, repo, "old.md")
	saves := store.saveCalls
	if _, laterErr := settings.AddBase(model.BaseMutationRequest{Mode: "connect", Name: "later", Path: t.TempDir()}); !errors.Is(laterErr, ErrRollbackFailed) {
		t.Fatalf("AddBase() after rollback failure error = %v, want ErrRollbackFailed", laterErr)
	}
	if store.saveCalls != saves {
		t.Errorf("Save calls after degraded mutation = %d, want %d", store.saveCalls, saves)
	}
}

func TestSettingsServiceFakeRuntimeTransactionEventOrder(t *testing.T) {
	activePath := t.TempDir()
	targetPath := t.TempDir()
	service, store, runtime, _ := newConfiguredSettingsService(t, activePath, targetPath)
	events := &orderedEvents{}
	store.events = events
	runtime.events = events

	if _, err := service.SwitchBase("other"); err != nil {
		t.Fatalf("SwitchBase() error = %v", err)
	}
	want := []string{"prepare:" + targetPath, "save", "commit:" + targetPath}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func writeTestNote(t *testing.T, base, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(base, name), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", name, err)
	}
}
