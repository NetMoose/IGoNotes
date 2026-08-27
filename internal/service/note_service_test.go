package service

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

type fakeNoteRepository struct {
	mu             sync.Mutex
	nodes          []model.NoteNode
	replaceErr     error
	replaceStarted chan struct{}
	replaceRelease <-chan struct{}
	startedOnce    sync.Once
}

func waitForPendingWriter(t *testing.T, mu *sync.RWMutex) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if !mu.TryRLock() {
			return
		}
		mu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("writer did not become pending")
		}
		runtime.Gosched()
	}
}

func (r *fakeNoteRepository) UpsertNode(id, title, path string, parentID *string, nodeType string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	parent := ""
	if parentID != nil {
		parent = *parentID
	}
	r.nodes = append(r.nodes, model.NoteNode{
		ID:       id,
		Name:     title,
		Path:     path,
		ParentID: parent,
		Type:     nodeType,
	})
	return nil
}

func (r *fakeNoteRepository) GetAllNodes() ([]model.NoteNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.NoteNode(nil), r.nodes...), nil
}

func (r *fakeNoteRepository) ReplaceAll(nodes []model.NoteNode) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replaceStarted != nil {
		r.startedOnce.Do(func() { close(r.replaceStarted) })
	}
	if r.replaceRelease != nil {
		<-r.replaceRelease
	}
	if r.replaceErr != nil {
		return r.replaceErr
	}
	r.nodes = append([]model.NoteNode(nil), nodes...)
	return nil
}

func (r *fakeNoteRepository) DeleteNode(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.nodes {
		if r.nodes[i].ID == id {
			r.nodes = append(r.nodes[:i], r.nodes[i+1:]...)
			break
		}
	}
	return nil
}

func TestNoteServiceSwitchBasePublishesIndexedTarget(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "new.md"), []byte("new"), 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md", Name: "old", Type: "file", Path: "old.md"}}}
	service := NewNoteService(repo, oldBase)

	targetWithDot := target + string(filepath.Separator) + "."
	if err := service.SwitchBase(targetWithDot); err != nil {
		t.Fatalf("SwitchBase() error = %v, want nil", err)
	}

	if got := service.GetBasePath(); got != filepath.Clean(target) {
		t.Errorf("GetBasePath() = %q, want %q", got, filepath.Clean(target))
	}
	nodes, err := repo.GetAllNodes()
	if err != nil {
		t.Fatalf("GetAllNodes() error = %v, want nil", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "new.md" {
		t.Errorf("indexed nodes = %#v, want only new.md", nodes)
	}
}

func TestNoteServiceSwitchBaseScanFailurePreservesBaseAndIndex(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	wantErr := errors.New("scan failed")
	wantNodes := []model.NoteNode{{ID: "old.md", Name: "old", Type: "file", Path: "old.md"}}
	repo := &fakeNoteRepository{nodes: append([]model.NoteNode(nil), wantNodes...)}
	service := NewNoteService(repo, oldBase)
	service.scan = func(string) ([]model.NoteNode, error) { return nil, wantErr }

	err := service.SwitchBase(target)
	if !errors.Is(err, wantErr) {
		t.Fatalf("SwitchBase() error = %v, want %v", err, wantErr)
	}
	if got := service.GetBasePath(); got != oldBase {
		t.Errorf("GetBasePath() = %q, want unchanged %q", got, oldBase)
	}
	nodes, getErr := repo.GetAllNodes()
	if getErr != nil {
		t.Fatalf("GetAllNodes() error = %v, want nil", getErr)
	}
	if len(nodes) != 1 || nodes[0].ID != wantNodes[0].ID {
		t.Errorf("indexed nodes = %#v, want unchanged %#v", nodes, wantNodes)
	}
}

func TestNoteServiceSwitchBaseRejectsNonDirectory(t *testing.T) {
	oldBase := t.TempDir()
	target := filepath.Join(t.TempDir(), "base.txt")
	if err := os.WriteFile(target, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	service := NewNoteService(&fakeNoteRepository{}, oldBase)

	if err := service.SwitchBase(target); err == nil {
		t.Fatal("SwitchBase() error = nil, want non-nil")
	}
	if got := service.GetBasePath(); got != oldBase {
		t.Errorf("GetBasePath() = %q, want unchanged %q", got, oldBase)
	}
}

func TestNoteServiceSwitchBaseIndexesSymlinkRootAndPublishesLogicalPath(t *testing.T) {
	oldBase := t.TempDir()
	physicalBase := t.TempDir()
	if err := os.WriteFile(filepath.Join(physicalBase, "linked.md"), []byte("linked"), 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	logicalBase := filepath.Join(t.TempDir(), "selected-base")
	if err := os.Symlink(physicalBase, logicalBase); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("symlink creation is not permitted: %v", err)
		}
		t.Fatalf("os.Symlink() error = %v, want nil", err)
	}
	repo := &fakeNoteRepository{}
	service := NewNoteService(repo, oldBase)

	selectedPath := logicalBase + string(filepath.Separator) + "."
	if err := service.SwitchBase(selectedPath); err != nil {
		t.Fatalf("SwitchBase() error = %v, want nil", err)
	}
	if got := service.GetBasePath(); got != filepath.Clean(selectedPath) {
		t.Errorf("GetBasePath() = %q, want logical path %q", got, filepath.Clean(selectedPath))
	}
	nodes, err := repo.GetAllNodes()
	if err != nil {
		t.Fatalf("GetAllNodes() error = %v, want nil", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "linked.md" {
		t.Errorf("indexed nodes = %#v, want only linked.md", nodes)
	}
}

func TestNoteServiceSwitchBaseReplaceFailurePreservesBaseAndIndex(t *testing.T) {
	oldBase := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "new.md"), []byte("new"), 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	wantErr := errors.New("replace failed")
	wantNodes := []model.NoteNode{{ID: "old.md", Name: "old", Type: "file", Path: "old.md"}}
	repo := &fakeNoteRepository{nodes: append([]model.NoteNode(nil), wantNodes...), replaceErr: wantErr}
	service := NewNoteService(repo, oldBase)

	err := service.SwitchBase(target)
	if !errors.Is(err, wantErr) {
		t.Fatalf("SwitchBase() error = %v, want %v", err, wantErr)
	}
	if got := service.GetBasePath(); got != oldBase {
		t.Errorf("GetBasePath() = %q, want unchanged %q", got, oldBase)
	}
	nodes, getErr := repo.GetAllNodes()
	if getErr != nil {
		t.Fatalf("GetAllNodes() error = %v, want nil", getErr)
	}
	if len(nodes) != 1 || nodes[0].ID != wantNodes[0].ID {
		t.Errorf("indexed nodes = %#v, want unchanged %#v", nodes, wantNodes)
	}
}

func TestNoteServiceScanNotesBuildsPureFilteredIndex(t *testing.T) {
	base := filepath.Join(t.TempDir(), "assets")
	paths := []string{
		filepath.Join(base, "topic", "nested"),
		filepath.Join(base, ".hidden"),
		filepath.Join(base, "assets"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v, want nil", path, err)
		}
	}
	files := map[string]string{
		"root.md":                      "root",
		filepath.Join("topic", "a.MD"): "a",
		filepath.Join("topic", "nested", "note.md"): "nested",
		filepath.Join("topic", "ignored.txt"):       "ignored",
		filepath.Join(".hidden", "secret.md"):       "secret",
		filepath.Join("assets", "image.md"):         "asset",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(base, name), []byte(content), 0644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v, want nil", name, err)
		}
	}

	nodes, err := scanNotes(base)
	if err != nil {
		t.Fatalf("scanNotes() error = %v, want nil", err)
	}
	byID := make(map[string]model.NoteNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	want := map[string]model.NoteNode{
		"root.md": {ID: "root.md", Name: "root", Type: "file", Path: "root.md"},
		"topic":   {ID: "topic", Name: "topic", Type: "dir", Path: "topic"},
		filepath.Join("topic", "a.MD"): {
			ID: filepath.Join("topic", "a.MD"), Name: "a", Type: "file", Path: filepath.Join("topic", "a.MD"), ParentID: "topic",
		},
		filepath.Join("topic", "nested"): {
			ID: filepath.Join("topic", "nested"), Name: "nested", Type: "dir", Path: filepath.Join("topic", "nested"), ParentID: "topic",
		},
		filepath.Join("topic", "nested", "note.md"): {
			ID: filepath.Join("topic", "nested", "note.md"), Name: "note", Type: "file", Path: filepath.Join("topic", "nested", "note.md"), ParentID: filepath.Join("topic", "nested"),
		},
	}
	if len(byID) != len(want) {
		t.Fatalf("scanNotes() returned %d nodes (%#v), want %d", len(byID), byID, len(want))
	}
	for id, wantNode := range want {
		if got := byID[id]; got.ID != wantNode.ID || got.Name != wantNode.Name || got.Type != wantNode.Type || got.Path != wantNode.Path || got.ParentID != wantNode.ParentID {
			t.Errorf("node %q = %#v, want %#v", id, got, wantNode)
		}
	}

	empty, err := scanNotes("")
	if err != nil || empty != nil {
		t.Errorf("scanNotes(empty) = %#v, %v, want nil, nil", empty, err)
	}
}

func TestNoteServiceOpenRawFileValidatesPathsAndUnlocksOnce(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "note.md"), []byte("content"), 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	service := NewNoteService(&fakeNoteRepository{}, base)

	for _, path := range []string{"", ".", filepath.Join("..", "outside"), "..", filepath.Join(base, "note.md")} {
		t.Run(path, func(t *testing.T) {
			file, _, err := service.OpenRawFile(path)
			if !errors.Is(err, os.ErrPermission) {
				t.Fatalf("OpenRawFile(%q) error = %v, want os.ErrPermission", path, err)
			}
			if file != nil {
				t.Errorf("OpenRawFile(%q) file = %v, want nil", path, file)
			}
		})
	}

	file, info, err := service.OpenRawFile("note.md")
	if err != nil {
		t.Fatalf("OpenRawFile(valid) error = %v, want nil", err)
	}
	if info.Name() != "note.md" {
		t.Errorf("OpenRawFile(valid) info.Name() = %q, want note.md", info.Name())
	}
	if err := file.Close(); err != nil {
		t.Fatalf("first Close() error = %v, want nil", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want nil", err)
	}

	emptyBase := NewNoteService(&fakeNoteRepository{}, "")
	if _, _, err := emptyBase.OpenRawFile("note.md"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("OpenRawFile() with empty base error = %v, want os.ErrNotExist", err)
	}

	if _, _, err := service.OpenRawFile("missing.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenRawFile(missing) error = %v, want os.ErrNotExist", err)
	}
	target := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- service.SwitchBase(target) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SwitchBase() after failed open error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SwitchBase() blocked after failed OpenRawFile")
	}
}

func TestNoteServiceOpenRawFileRejectsEscapingSymlink(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	link := filepath.Join(base, "escape.md")
	if err := os.Symlink(outside, link); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("symlink creation is not permitted: %v", err)
		}
		t.Fatalf("os.Symlink() error = %v, want nil", err)
	}
	service := NewNoteService(&fakeNoteRepository{}, base)

	file, _, err := service.OpenRawFile("escape.md")
	if file != nil {
		_ = file.Close()
		t.Errorf("OpenRawFile() file = %v, want nil", file)
	}
	if err == nil {
		t.Fatal("OpenRawFile() error = nil, want symlink escape rejection")
	}
}

func TestNoteServiceOpenRawFilePinsOldBaseDuringSwitch(t *testing.T) {
	oldBase := t.TempDir()
	newBase := t.TempDir()
	for path, content := range map[string]string{
		filepath.Join(oldBase, "note.md"): "old content",
		filepath.Join(newBase, "note.md"): "new content",
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v, want nil", path, err)
		}
	}
	service := NewNoteService(&fakeNoteRepository{}, oldBase)
	file, _, err := service.OpenRawFile("note.md")
	if err != nil {
		t.Fatalf("OpenRawFile() error = %v, want nil", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if service.baseMu.TryLock() {
		service.baseMu.Unlock()
		t.Fatal("OpenRawFile() did not retain the base read lock")
	}

	switchDone := make(chan error, 1)
	go func() { switchDone <- service.SwitchBase(newBase) }()
	waitForPendingWriter(t, &service.baseMu)

	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v, want nil", err)
	}
	if string(content) != "old content" {
		t.Errorf("raw content = %q, want old content", content)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	select {
	case err := <-switchDone:
		if err != nil {
			t.Fatalf("SwitchBase() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SwitchBase() did not finish after raw file closed")
	}

	got, err := service.GetNoteContent("note.md")
	if err != nil {
		t.Fatalf("GetNoteContent() error = %v, want nil", err)
	}
	if got != "new content" {
		t.Errorf("GetNoteContent() = %q, want new content", got)
	}
}

func TestNoteServiceRequestDuringSwitchReadsNewBase(t *testing.T) {
	oldBase := t.TempDir()
	newBase := t.TempDir()
	for path, content := range map[string]string{
		filepath.Join(oldBase, "note.md"): "old content",
		filepath.Join(newBase, "note.md"): "new content",
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v, want nil", path, err)
		}
	}
	replaceStarted := make(chan struct{})
	replaceRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(replaceRelease) }) }
	defer release()
	repo := &fakeNoteRepository{
		replaceStarted: replaceStarted,
		replaceRelease: replaceRelease,
	}
	service := NewNoteService(repo, oldBase)

	switchDone := make(chan error, 1)
	go func() { switchDone <- service.SwitchBase(newBase) }()
	select {
	case <-replaceStarted:
	case <-time.After(time.Second):
		t.Fatal("SwitchBase() did not reach ReplaceAll")
	}
	if service.baseMu.TryRLock() {
		service.baseMu.RUnlock()
		t.Fatal("SwitchBase() did not retain the base write lock during replacement")
	}

	type readResult struct {
		content string
		err     error
	}
	readLockEntered := make(chan struct{})
	service.beforeReadLock = func() { close(readLockEntered) }
	readDone := make(chan readResult, 1)
	go func() {
		content, err := service.GetNoteContent("note.md")
		readDone <- readResult{content: content, err: err}
	}()
	select {
	case <-readLockEntered:
	case <-time.After(time.Second):
		t.Fatal("GetNoteContent() did not reach the base read lock")
	}
	if service.baseMu.TryRLock() {
		service.baseMu.RUnlock()
		t.Fatal("base read lock unexpectedly available while switch is replacing the index")
	}

	release()
	select {
	case err := <-switchDone:
		if err != nil {
			t.Fatalf("SwitchBase() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SwitchBase() did not finish after ReplaceAll was released")
	}
	select {
	case result := <-readDone:
		if result.err != nil {
			t.Fatalf("GetNoteContent() error = %v, want nil", result.err)
		}
		if result.content != "new content" {
			t.Errorf("GetNoteContent() = %q, want new content", result.content)
		}
	case <-time.After(time.Second):
		t.Fatal("GetNoteContent() did not finish after switch")
	}
}

func TestNoteServiceSyncFailureStillReleasesInitialTreeWait(t *testing.T) {
	wantErr := errors.New("scan failed")
	repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md", Name: "old", Type: "file", Path: "old.md"}}}
	service := NewNoteService(repo, t.TempDir())
	service.scan = func(string) ([]model.NoteNode, error) { return nil, wantErr }

	if err := service.SyncFS(); !errors.Is(err, wantErr) {
		t.Fatalf("SyncFS() error = %v, want %v", err, wantErr)
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.GetTree()
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("GetTree() error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("GetTree() remained blocked after failed initial SyncFS")
	}
}

func TestNoteServiceRenameNodeRejectsExistingDestination(t *testing.T) {
	base := t.TempDir()
	for name, content := range map[string]string{"old.md": "old", "existing.md": "existing"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(content), 0644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v, want nil", name, err)
		}
	}
	service := NewNoteService(&fakeNoteRepository{}, base)

	if err := service.RenameNode("old.md", "existing"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("RenameNode() error = %v, want ErrAlreadyExists", err)
	}
	for name, want := range map[string]string{"old.md": "old", "existing.md": "existing"} {
		content, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			t.Fatalf("os.ReadFile(%q) error = %v, want nil", name, err)
		}
		if string(content) != want {
			t.Errorf("content of %q = %q, want %q", name, content, want)
		}
	}
}

func TestNoteServiceRenameNodeExactPathIsNoOp(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "same.md"), []byte("same"), 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "same.md", Name: "same", Type: "file", Path: "same.md"}}}
	service := NewNoteService(repo, base)

	if err := service.RenameNode("same.md", "same.md"); err != nil {
		t.Fatalf("RenameNode() error = %v, want nil", err)
	}
	content, err := os.ReadFile(filepath.Join(base, "same.md"))
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v, want nil", err)
	}
	if string(content) != "same" {
		t.Errorf("content = %q, want same", content)
	}
}

func TestNoteServiceRenameNodeAllowsCaseOnlyRename(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "Case.md"), []byte("case"), 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	service := NewNoteService(&fakeNoteRepository{}, base)

	if err := service.RenameNode("Case.md", "case"); err != nil {
		t.Fatalf("RenameNode() error = %v, want nil", err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("os.ReadDir() error = %v, want nil", err)
	}
	if len(entries) != 1 || entries[0].Name() != "case.md" {
		t.Errorf("directory entries = %#v, want only case.md", entries)
	}
}

func TestNoteServiceRenameNodeReplacesIndex(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "old.md"), []byte("old"), 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md", Name: "old", Type: "file", Path: "old.md"}}}
	service := NewNoteService(repo, base)

	if err := service.RenameNode("old.md", "renamed"); err != nil {
		t.Fatalf("RenameNode() error = %v, want nil", err)
	}
	nodes, err := repo.GetAllNodes()
	if err != nil {
		t.Fatalf("GetAllNodes() error = %v, want nil", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "renamed.md" {
		t.Errorf("indexed nodes = %#v, want only renamed.md", nodes)
	}
}
