package service

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestNoteServiceSwitchBaseClearsBaseAndIndex(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "old.md"), []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md", Name: "old", Type: "file", Path: "old.md"}}}
	service := NewNoteService(repo, base)

	if err := service.SwitchBase(""); err != nil {
		t.Fatalf("SwitchBase(empty) error = %v", err)
	}
	if got := service.GetBasePath(); got != "" {
		t.Errorf("GetBasePath() = %q, want empty", got)
	}
	nodes, err := repo.GetAllNodes()
	if err != nil {
		t.Fatalf("GetAllNodes() error = %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("indexed nodes = %#v, want empty", nodes)
	}
	if _, err := service.GetNoteContent("old.md"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("GetNoteContent() error = %v, want os.ErrNotExist", err)
	}
	if tree, err := service.GetTree(); err != nil || len(tree) != 0 {
		t.Errorf("GetTree() = %#v, %v; want empty", tree, err)
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

func TestNoteServiceScanNotesExcludesSymlinks(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	requireSymlink(t, outside, filepath.Join(base, "secret.md"))
	requireSymlink(t, t.TempDir(), filepath.Join(base, "linked-dir"))

	nodes, err := scanNotes(base)
	if err != nil {
		t.Fatalf("scanNotes() error = %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("scanNotes() = %#v, want no symlink nodes", nodes)
	}
}

func TestNoteServiceRejectsSymlinkEscapes(t *testing.T) {
	t.Run("get note content", func(t *testing.T) {
		base := t.TempDir()
		outside := filepath.Join(t.TempDir(), "secret.md")
		if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}
		requireSymlink(t, outside, filepath.Join(base, "secret.md"))

		content, err := NewNoteService(&fakeNoteRepository{}, base).GetNoteContent("secret.md")
		if !errors.Is(err, ErrInvalidNotePath) {
			t.Fatalf("GetNoteContent() error = %v, want ErrInvalidNotePath", err)
		}
		if content != "" {
			t.Errorf("GetNoteContent() content = %q, want empty", content)
		}
	})

	t.Run("save note content", func(t *testing.T) {
		base, outside, marker := symlinkEscapeFixture(t, "linked")
		err := NewNoteService(&fakeNoteRepository{}, base).SaveNoteContent(filepath.Join("linked", "marker.md"), "changed")
		if !errors.Is(err, ErrInvalidNotePath) {
			t.Fatalf("SaveNoteContent() error = %v, want ErrInvalidNotePath", err)
		}
		assertFileContent(t, filepath.Join(outside, "marker.md"), marker)
	})

	for _, nodeType := range []string{"file", "dir"} {
		t.Run("create "+nodeType, func(t *testing.T) {
			base, outside, marker := symlinkEscapeFixture(t, "linked")
			repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "existing.md"}}}
			_, err := NewNoteService(repo, base).CreateNode("linked", "created", nodeType)
			if !errors.Is(err, ErrInvalidNotePath) {
				t.Fatalf("CreateNode() error = %v, want ErrInvalidNotePath", err)
			}
			if _, err := os.Lstat(filepath.Join(outside, "created")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("outside directory was created, error = %v", err)
			}
			if _, err := os.Lstat(filepath.Join(outside, "created.md")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("outside note was created, error = %v", err)
			}
			assertFileContent(t, filepath.Join(outside, "marker.md"), marker)
			assertRepositoryIDs(t, repo, "existing.md")
		})
	}

	t.Run("delete through parent", func(t *testing.T) {
		base, outside, marker := symlinkEscapeFixture(t, "linked")
		repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: filepath.Join("linked", "marker.md")}}}
		err := NewNoteService(repo, base).DeleteNode(filepath.Join("linked", "marker.md"))
		if !errors.Is(err, ErrInvalidNotePath) {
			t.Fatalf("DeleteNode() error = %v, want ErrInvalidNotePath", err)
		}
		assertFileContent(t, filepath.Join(outside, "marker.md"), marker)
		assertRepositoryIDs(t, repo, filepath.Join("linked", "marker.md"))
	})

	t.Run("delete symlink entry only", func(t *testing.T) {
		base, outside, marker := symlinkEscapeFixture(t, "linked")
		repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "linked"}}}
		if err := NewNoteService(repo, base).DeleteNode("linked"); err != nil {
			t.Fatalf("DeleteNode() error = %v, want nil", err)
		}
		if _, err := os.Lstat(filepath.Join(base, "linked")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("symlink entry still exists, error = %v", err)
		}
		assertFileContent(t, filepath.Join(outside, "marker.md"), marker)
		assertRepositoryIDs(t, repo)
	})

	for _, test := range []struct {
		name  string
		setup func(*testing.T, string, string)
		id    string
		to    string
	}{
		{
			name: "parent",
			setup: func(t *testing.T, base, outside string) {
				requireSymlink(t, outside, filepath.Join(base, "linked"))
			},
			id: filepath.Join("linked", "marker.md"), to: "renamed",
		},
		{
			name: "source",
			setup: func(t *testing.T, base, outside string) {
				requireSymlink(t, filepath.Join(outside, "marker.md"), filepath.Join(base, "source.md"))
			},
			id: "source.md", to: "renamed",
		},
		{
			name: "destination",
			setup: func(t *testing.T, base, outside string) {
				if err := os.WriteFile(filepath.Join(base, "source.md"), []byte("local"), 0o600); err != nil {
					t.Fatalf("os.WriteFile() error = %v", err)
				}
				requireSymlink(t, filepath.Join(outside, "marker.md"), filepath.Join(base, "destination.md"))
			},
			id: "source.md", to: "destination",
		},
	} {
		t.Run("rename "+test.name, func(t *testing.T) {
			base := t.TempDir()
			outside := t.TempDir()
			marker := []byte("outside marker")
			if err := os.WriteFile(filepath.Join(outside, "marker.md"), marker, 0o600); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			test.setup(t, base, outside)
			repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: test.id}}}

			err := NewNoteService(repo, base).RenameNode(test.id, test.to)
			if !errors.Is(err, ErrInvalidNotePath) {
				t.Fatalf("RenameNode() error = %v, want ErrInvalidNotePath", err)
			}
			assertFileContent(t, filepath.Join(outside, "marker.md"), marker)
			if _, err := os.Lstat(filepath.Join(outside, "renamed.md")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("outside file was renamed, error = %v", err)
			}
			assertRepositoryIDs(t, repo, test.id)
		})
	}

	for _, link := range []string{"assets", filepath.Join("assets", "images")} {
		t.Run("save asset through "+link, func(t *testing.T) {
			base := t.TempDir()
			outside := t.TempDir()
			linkPath := filepath.Join(base, link)
			if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
				t.Fatalf("os.MkdirAll() error = %v", err)
			}
			requireSymlink(t, outside, linkPath)

			path, err := NewNoteService(&fakeNoteRepository{}, base).SaveAsset(strings.NewReader("outside write"), "upload.png")
			if !errors.Is(err, ErrInvalidNotePath) {
				t.Fatalf("SaveAsset() error = %v, want ErrInvalidNotePath", err)
			}
			if path != "" {
				t.Errorf("SaveAsset() path = %q, want empty", path)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Errorf("outside entries = %#v, %v; want empty", entries, err)
			}
		})
	}

	t.Run("get absolute path", func(t *testing.T) {
		base, _, _ := symlinkEscapeFixture(t, "linked")
		path, err := NewNoteService(&fakeNoteRepository{}, base).GetAbsoluteFilePath(filepath.Join("linked", "marker.md"))
		if !errors.Is(err, ErrInvalidNotePath) {
			t.Fatalf("GetAbsoluteFilePath() error = %v, want ErrInvalidNotePath", err)
		}
		if path != "" {
			t.Errorf("GetAbsoluteFilePath() path = %q, want empty", path)
		}
	})
}

func TestNoteServiceSymlinkReplacementCannotMutateOutside(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	markerPath := filepath.Join(outside, "marker.md")
	marker := []byte("outside marker")
	if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	linkPath := filepath.Join(base, "changing")
	requireSymlink(t, outside, linkPath)
	service := NewNoteService(&fakeNoteRepository{}, base)

	start := make(chan struct{})
	var writers sync.WaitGroup
	for range 4 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			for range 300 {
				_ = service.SaveNoteContent(filepath.Join("changing", "marker.md"), "changed")
			}
		}()
	}
	close(start)
	for range 300 {
		_ = os.RemoveAll(linkPath)
		if err := os.Mkdir(linkPath, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatalf("os.Mkdir() error = %v", err)
		}
		_ = os.RemoveAll(linkPath)
		if err := os.Symlink(outside, linkPath); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatalf("os.Symlink() error = %v", err)
		}
	}
	writers.Wait()

	assertFileContent(t, markerPath, marker)
}

func TestNoteServiceCreateNodeUsesExclusiveCreation(t *testing.T) {
	base := t.TempDir()
	repo := &fakeNoteRepository{}
	service := NewNoteService(repo, base)
	start := make(chan struct{})
	errs := make(chan error, 8)
	for range cap(errs) {
		go func() {
			<-start
			_, err := service.CreateNode("", "race", "file")
			errs <- err
		}()
	}
	close(start)

	created := 0
	conflicts := 0
	for range cap(errs) {
		err := <-errs
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrAlreadyExists):
			conflicts++
		default:
			t.Errorf("CreateNode() error = %v, want nil or ErrAlreadyExists", err)
		}
	}
	if created != 1 || conflicts != cap(errs)-1 {
		t.Errorf("CreateNode() results = %d created, %d conflicts; want 1, %d", created, conflicts, cap(errs)-1)
	}
	assertFileContent(t, filepath.Join(base, "race.md"), []byte("# race\n"))
	assertRepositoryIDs(t, repo, "race.md")
}

func TestNoteServiceRootIOErrorsRemainDistinctFromInvalidPaths(t *testing.T) {
	baseFile := filepath.Join(t.TempDir(), "base-file")
	if err := os.WriteFile(baseFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	service := NewNoteService(&fakeNoteRepository{}, baseFile)
	tests := []struct {
		name string
		call func() error
	}{
		{name: "get", call: func() error { _, err := service.GetNoteContent("note.md"); return err }},
		{name: "absolute", call: func() error { _, err := service.GetAbsoluteFilePath("note.md"); return err }},
		{name: "save", call: func() error { return service.SaveNoteContent("note.md", "content") }},
		{name: "create", call: func() error { _, err := service.CreateNode("", "note", "file"); return err }},
		{name: "delete", call: func() error { return service.DeleteNode("note.md") }},
		{name: "rename", call: func() error { return service.RenameNode("note.md", "renamed") }},
		{name: "asset", call: func() error { _, err := service.SaveAsset(strings.NewReader("image"), "image.png"); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil {
				t.Fatal("operation error = nil, want ordinary I/O error")
			}
			if errors.Is(err, ErrInvalidNotePath) {
				t.Fatalf("operation error = %v, do not want ErrInvalidNotePath", err)
			}
		})
	}
}

func TestNoteServiceGetAbsoluteFilePathValidatesExistence(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "note.md"), []byte("note"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	service := NewNoteService(&fakeNoteRepository{}, base)

	got, err := service.GetAbsoluteFilePath("note.md")
	if err != nil {
		t.Fatalf("GetAbsoluteFilePath() error = %v", err)
	}
	if want := filepath.Join(base, "note.md"); got != want {
		t.Errorf("GetAbsoluteFilePath() = %q, want %q", got, want)
	}
	if _, err := service.GetAbsoluteFilePath("missing.md"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("GetAbsoluteFilePath(missing) error = %v, want os.ErrNotExist", err)
	}
}

func TestNoteServiceSaveAssetPreservesRelativeNamingAndCollisions(t *testing.T) {
	base := t.TempDir()
	service := NewNoteService(&fakeNoteRepository{}, base)

	first, err := service.SaveAsset(strings.NewReader("first"), "upload.png")
	if err != nil {
		t.Fatalf("SaveAsset(first) error = %v", err)
	}
	if want := filepath.Join("assets", "images", "upload.png"); first != want {
		t.Errorf("SaveAsset(first) path = %q, want %q", first, want)
	}
	second, err := service.SaveAsset(strings.NewReader("second"), "upload.png")
	if err != nil {
		t.Fatalf("SaveAsset(second) error = %v", err)
	}
	if second == first || filepath.Dir(second) != filepath.Join("assets", "images") {
		t.Errorf("SaveAsset(second) path = %q, want distinct assets/images path", second)
	}
	assertFileContent(t, filepath.Join(base, first), []byte("first"))
	assertFileContent(t, filepath.Join(base, second), []byte("second"))
}

func requireSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("symlink creation is not permitted: %v", err)
		}
		t.Fatalf("os.Symlink() error = %v", err)
	}
}

func symlinkEscapeFixture(t *testing.T, linkName string) (base, outside string, marker []byte) {
	t.Helper()
	base = t.TempDir()
	outside = t.TempDir()
	marker = []byte("outside marker")
	if err := os.WriteFile(filepath.Join(outside, "marker.md"), marker, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	requireSymlink(t, outside, filepath.Join(base, linkName))
	return base, outside, marker
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("content of %q = %q, want %q", path, got, want)
	}
}

func assertRepositoryIDs(t *testing.T, repo *fakeNoteRepository, want ...string) {
	t.Helper()
	nodes, err := repo.GetAllNodes()
	if err != nil {
		t.Fatalf("GetAllNodes() error = %v", err)
	}
	if len(nodes) != len(want) {
		t.Fatalf("repository nodes = %#v, want IDs %q", nodes, want)
	}
	for i, id := range want {
		if nodes[i].ID != id {
			t.Errorf("repository node %d ID = %q, want %q", i, nodes[i].ID, id)
		}
	}
}

func TestNoteServiceRejectsLexicalTraversalWithoutTouchingOutsideFiles(t *testing.T) {
	tests := []struct {
		name string
		call func(*NoteService) error
	}{
		{name: "get note", call: func(service *NoteService) error {
			_, err := service.GetNoteContent(filepath.Join("..", "outside.md"))
			return err
		}},
		{name: "get absolute path", call: func(service *NoteService) error {
			_, err := service.GetAbsoluteFilePath(filepath.Join("..", "outside.md"))
			return err
		}},
		{name: "open raw file", call: func(service *NoteService) error {
			file, _, err := service.OpenRawFile(filepath.Join("..", "outside.md"))
			if file != nil {
				_ = file.Close()
			}
			return err
		}},
		{name: "save note", call: func(service *NoteService) error {
			return service.SaveNoteContent(filepath.Join("..", "outside.md"), "overwritten")
		}},
		{name: "create note", call: func(service *NoteService) error {
			_, err := service.CreateNode(filepath.Join("..", "outside-parent"), "created", "file")
			return err
		}},
		{name: "delete note", call: func(service *NoteService) error { return service.DeleteNode(filepath.Join("..", "outside.md")) }},
		{name: "rename note", call: func(service *NoteService) error {
			return service.RenameNode(filepath.Join("..", "outside.md"), "renamed")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			base := filepath.Join(root, "base")
			if err := os.Mkdir(base, 0o755); err != nil {
				t.Fatalf("os.Mkdir() error = %v", err)
			}
			outside := filepath.Join(root, "outside.md")
			marker := []byte("outside marker")
			if err := os.WriteFile(outside, marker, 0o600); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			service := NewNoteService(&fakeNoteRepository{}, base)

			err := test.call(service)

			if !errors.Is(err, ErrInvalidNotePath) {
				t.Errorf("operation error = %v, want ErrInvalidNotePath", err)
			}
			got, readErr := os.ReadFile(outside)
			if readErr != nil {
				t.Fatalf("outside marker was removed: %v", readErr)
			}
			if !bytes.Equal(got, marker) {
				t.Errorf("outside marker = %q, want unchanged %q", got, marker)
			}
			if _, statErr := os.Stat(filepath.Join(root, "outside-parent")); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("outside parent was created, stat error = %v", statErr)
			}
		})
	}
}

func TestNoteServiceRejectsInvalidConcretePathsAndAllowsNestedPaths(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join("topic", "note.md")
	if err := os.MkdirAll(filepath.Join(base, "topic"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, nested), []byte("nested"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	service := NewNoteService(&fakeNoteRepository{}, base)

	for _, path := range []string{"", ".", "..", filepath.Join("..", "outside.md"), filepath.Join(base, nested)} {
		t.Run(path, func(t *testing.T) {
			if _, err := service.GetNoteContent(path); !errors.Is(err, ErrInvalidNotePath) {
				t.Errorf("GetNoteContent(%q) error = %v, want ErrInvalidNotePath", path, err)
			}
		})
	}
	content, err := service.GetNoteContent(nested)
	if err != nil {
		t.Fatalf("GetNoteContent(%q) error = %v", nested, err)
	}
	if content != "nested" {
		t.Errorf("GetNoteContent(%q) = %q, want nested", nested, content)
	}
}

func TestCleanRelativeNotePathLocalityAndEmptyHandling(t *testing.T) {
	for _, path := range []string{"", ".", "..", filepath.Join("..", "outside.md"), filepath.Join(string(filepath.Separator), "absolute.md")} {
		t.Run(path, func(t *testing.T) {
			if _, err := cleanRelativeNotePath(path, false); !errors.Is(err, ErrInvalidNotePath) {
				t.Errorf("cleanRelativeNotePath(%q, false) error = %v, want ErrInvalidNotePath", path, err)
			}
		})
	}

	if got, err := cleanRelativeNotePath("", true); err != nil || got != "" {
		t.Errorf("cleanRelativeNotePath(empty, true) = %q, %v; want empty, nil", got, err)
	}
	if _, err := cleanRelativeNotePath(".", true); !errors.Is(err, ErrInvalidNotePath) {
		t.Errorf("cleanRelativeNotePath(dot, true) error = %v, want ErrInvalidNotePath", err)
	}

	path := filepath.Join("topic", "section", "..", "note.md")
	got, err := cleanRelativeNotePath(path, false)
	if err != nil {
		t.Fatalf("cleanRelativeNotePath(%q, false) error = %v", path, err)
	}
	if want := filepath.Clean(path); got != want {
		t.Errorf("cleanRelativeNotePath(%q, false) = %q, want %q", path, got, want)
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
			if !errors.Is(err, ErrInvalidNotePath) {
				t.Fatalf("OpenRawFile(%q) error = %v, want ErrInvalidNotePath", path, err)
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
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "escape.md")); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("symlink creation is not permitted: %v", err)
		}
		t.Fatalf("os.Symlink() error = %v, want nil", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(base, "escape-dir")); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("symlink creation is not permitted: %v", err)
		}
		t.Fatalf("os.Symlink() error = %v, want nil", err)
	}
	service := NewNoteService(&fakeNoteRepository{}, base)

	for _, path := range []string{"escape.md", filepath.Join("escape-dir", "outside.md")} {
		t.Run(path, func(t *testing.T) {
			file, _, err := service.OpenRawFile(path)
			if file != nil {
				_ = file.Close()
				t.Errorf("OpenRawFile() file = %v, want nil", file)
			}
			if !errors.Is(err, ErrInvalidNotePath) {
				t.Fatalf("OpenRawFile() error = %v, want ErrInvalidNotePath", err)
			}
		})
	}
}

func TestNoteServiceOpenRawFileKeepsOrdinaryIOErrorsDistinctFromInvalidPaths(t *testing.T) {
	baseFile := filepath.Join(t.TempDir(), "base-file")
	if err := os.WriteFile(baseFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	service := NewNoteService(&fakeNoteRepository{}, baseFile)

	file, _, err := service.OpenRawFile("asset.txt")
	if file != nil {
		_ = file.Close()
		t.Errorf("OpenRawFile() file = %v, want nil", file)
	}
	if err == nil {
		t.Fatal("OpenRawFile() error = nil, want non-nil")
	}
	if errors.Is(err, ErrInvalidNotePath) {
		t.Fatalf("OpenRawFile() error = %v, do not want ErrInvalidNotePath", err)
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
	repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "stale.md", Name: "stale", Type: "file", Path: "stale.md"}}}
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
	nodes, err := repo.GetAllNodes()
	if err != nil {
		t.Fatalf("GetAllNodes() error = %v, want nil", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "same.md" {
		t.Errorf("indexed nodes = %#v, want only same.md", nodes)
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
