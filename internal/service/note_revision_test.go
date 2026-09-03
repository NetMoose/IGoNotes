package service

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"IGoNotes/internal/model"
)

func TestNoteServiceGetNoteReturnsRequestedIDContentAndByteRevision(t *testing.T) {
	base := t.TempDir()
	content := []byte("# Idea\n")
	writeTestNote(t, base, "idea.md", string(content))
	service := newTestNoteService(t, &fakeNoteRepository{}, base)

	response, err := service.GetNote("folder/../idea.md")
	if err != nil {
		t.Fatalf("GetNote() error = %v, want nil", err)
	}
	if response.ID != "folder/../idea.md" {
		t.Errorf("GetNote() ID = %q, want exact request ID", response.ID)
	}
	if !bytes.Equal([]byte(response.Content), content) {
		t.Errorf("GetNote() content = %q, want exact bytes %q", []byte(response.Content), content)
	}
	const wantRevision = "sha256:25ec1fffebeb4b54346d7df2c71511065bf99f571ac1c615e3fc1d4ce17ca5f9"
	if response.Revision != wantRevision {
		t.Errorf("GetNote() revision = %q, want %q", response.Revision, wantRevision)
	}
}

func TestNoteServiceSaveNoteAcceptsMatchingRevision(t *testing.T) {
	base := t.TempDir()
	writeTestNote(t, base, "note.md", "original")
	service := newTestNoteService(t, &fakeNoteRepository{}, base)
	current, err := service.GetNote("note.md")
	if err != nil {
		t.Fatalf("GetNote() error = %v, want nil", err)
	}

	content := "changed\n"
	response, err := service.SaveNote(model.SaveNoteRequest{
		ID:               "note.md",
		Content:          content,
		ExpectedRevision: &current.Revision,
	})
	if err != nil {
		t.Fatalf("SaveNote() error = %v, want nil", err)
	}
	if response.Status != "saved" {
		t.Errorf("SaveNote() status = %q, want saved", response.Status)
	}
	if response.Revision == "" || response.Revision == current.Revision {
		t.Errorf("SaveNote() revision = %q, want nonempty revision changed from %q", response.Revision, current.Revision)
	}
	assertFileContent(t, filepath.Join(base, "note.md"), []byte(content))
	if got := noteRevision([]byte(content)); response.Revision != got {
		t.Errorf("SaveNote() revision = %q, want written-byte revision %q", response.Revision, got)
	}
}

func TestNoteServiceSaveNoteRejectsStaleRevisionWithoutWriting(t *testing.T) {
	base := t.TempDir()
	writeTestNote(t, base, "note.md", "current")
	service := newTestNoteService(t, &fakeNoteRepository{}, base)
	stale := "sha256:stale"

	response, err := service.SaveNote(model.SaveNoteRequest{
		ID:               "note.md",
		Content:          "changed",
		ExpectedRevision: &stale,
	})
	if !errors.Is(err, ErrNoteChanged) {
		t.Fatalf("SaveNote() error = %v, want ErrNoteChanged", err)
	}
	if response != (model.SaveNoteResponse{}) {
		t.Errorf("SaveNote() response = %#v, want zero response", response)
	}
	assertFileContent(t, filepath.Join(base, "note.md"), []byte("current"))
}

func TestNoteServiceSaveNoteOmittedRevisionIsUnconditional(t *testing.T) {
	base := t.TempDir()
	writeTestNote(t, base, "note.md", "current")
	service := newTestNoteService(t, &fakeNoteRepository{}, base)

	response, err := service.SaveNote(model.SaveNoteRequest{ID: "note.md", Content: "changed"})
	if err != nil {
		t.Fatalf("SaveNote() error = %v, want nil", err)
	}
	if response.Status != "saved" || response.Revision == "" {
		t.Errorf("SaveNote() response = %#v, want saved with revision", response)
	}
	assertFileContent(t, filepath.Join(base, "note.md"), []byte("changed"))
}

func TestNoteServiceSaveNoteExplicitEmptyRevisionIsStale(t *testing.T) {
	base := t.TempDir()
	writeTestNote(t, base, "note.md", "current")
	service := newTestNoteService(t, &fakeNoteRepository{}, base)
	empty := ""

	response, err := service.SaveNote(model.SaveNoteRequest{
		ID:               "note.md",
		Content:          "changed",
		ExpectedRevision: &empty,
	})
	if !errors.Is(err, ErrNoteChanged) {
		t.Fatalf("SaveNote() error = %v, want ErrNoteChanged", err)
	}
	if response != (model.SaveNoteResponse{}) {
		t.Errorf("SaveNote() response = %#v, want zero response", response)
	}
	assertFileContent(t, filepath.Join(base, "note.md"), []byte("current"))
}

func TestNoteServiceSaveNoteChecksConflictBeforeRevision(t *testing.T) {
	base := t.TempDir()
	writeTestNote(t, base, "note.md", "current")
	coordinator := NewBaseOperationCoordinator()
	service := newTestNoteServiceWithCoordinator(t, &fakeNoteRepository{}, base, coordinator)
	coordinator.SetConflict(base, true)
	stale := ""

	response, err := service.SaveNote(model.SaveNoteRequest{
		ID:               "note.md",
		Content:          "changed",
		ExpectedRevision: &stale,
	})
	if !errors.Is(err, ErrGitConflictPending) {
		t.Fatalf("SaveNote() error = %v, want ErrGitConflictPending", err)
	}
	if response != (model.SaveNoteResponse{}) {
		t.Errorf("SaveNote() response = %#v, want zero response", response)
	}
	assertFileContent(t, filepath.Join(base, "note.md"), []byte("current"))
}

func TestNoteServiceSaveNoteRevisionIsSensitiveToExactBytes(t *testing.T) {
	base := t.TempDir()
	lf := []byte("line\n")
	crlf := []byte("line\r\n")
	writeTestNote(t, base, "note.md", string(lf))
	service := newTestNoteService(t, &fakeNoteRepository{}, base)
	current, err := service.GetNote("note.md")
	if err != nil {
		t.Fatalf("GetNote() error = %v, want nil", err)
	}
	if current.Revision == noteRevision(crlf) {
		t.Fatal("line-ending variants unexpectedly have the same revision")
	}
	if err := os.WriteFile(filepath.Join(base, "note.md"), crlf, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}

	response, err := service.SaveNote(model.SaveNoteRequest{
		ID:               "note.md",
		Content:          "replacement",
		ExpectedRevision: &current.Revision,
	})
	if !errors.Is(err, ErrNoteChanged) {
		t.Fatalf("SaveNote() error = %v, want ErrNoteChanged after byte-only change", err)
	}
	if response != (model.SaveNoteResponse{}) {
		t.Errorf("SaveNote() response = %#v, want zero response", response)
	}
	assertFileContent(t, filepath.Join(base, "note.md"), crlf)
}

func TestNoteServiceSaveNoteInvalidPathHasNoSideEffects(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.Mkdir(base, 0o755); err != nil {
		t.Fatalf("os.Mkdir() error = %v, want nil", err)
	}
	outside := filepath.Join(root, "outside.md")
	marker := []byte("outside")
	if err := os.WriteFile(outside, marker, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v, want nil", err)
	}
	service := newTestNoteService(t, &fakeNoteRepository{}, base)
	expected := "sha256:anything"

	response, err := service.SaveNote(model.SaveNoteRequest{
		ID:               "../outside.md",
		Content:          "changed",
		ExpectedRevision: &expected,
	})
	if !errors.Is(err, ErrInvalidNotePath) {
		t.Fatalf("SaveNote() error = %v, want ErrInvalidNotePath", err)
	}
	if response != (model.SaveNoteResponse{}) {
		t.Errorf("SaveNote() response = %#v, want zero response", response)
	}
	assertFileContent(t, outside, marker)
	entries, readErr := os.ReadDir(base)
	if readErr != nil {
		t.Fatalf("os.ReadDir() error = %v, want nil", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("base entries = %v, want none", entries)
	}
}
