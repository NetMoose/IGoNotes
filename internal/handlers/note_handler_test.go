package handlers

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

type handlerNoteRepository struct{}

func (handlerNoteRepository) UpsertNode(string, string, string, *string, string) error { return nil }
func (handlerNoteRepository) GetAllNodes() ([]model.NoteNode, error)                   { return nil, nil }
func (handlerNoteRepository) ReplaceAll([]model.NoteNode) error                        { return nil }
func (handlerNoteRepository) BeginReplaceAll([]model.NoteNode) (func() error, func() error, error, error) {
	return func() error { return nil }, func() error { return nil }, nil, nil
}
func (handlerNoteRepository) DeleteNode(string) error { return nil }

type failingInitialSyncRepository struct {
	nodes []model.NoteNode
	err   error
}

func (r *failingInitialSyncRepository) UpsertNode(string, string, string, *string, string) error {
	return nil
}
func (r *failingInitialSyncRepository) GetAllNodes() ([]model.NoteNode, error) {
	return append([]model.NoteNode(nil), r.nodes...), nil
}
func (r *failingInitialSyncRepository) BeginReplaceAll([]model.NoteNode) (func() error, func() error, error, error) {
	return nil, nil, r.err, nil
}
func (r *failingInitialSyncRepository) DeleteNode(string) error { return nil }

func TestNoteHandlerReturnsStructuredErrors(t *testing.T) {
	t.Run("bad JSON", func(t *testing.T) {
		handler := NewNoteHandler(nil)
		recorder := httptest.NewRecorder()
		handler.SaveNote(recorder, httptest.NewRequest(http.MethodPost, "/api/save", strings.NewReader("{")))

		assertAPIErrorResponse(t, recorder, http.StatusBadRequest, model.APIError{Code: "bad_json", Message: "Invalid JSON"})
	})

	t.Run("missing field", func(t *testing.T) {
		handler := NewNoteHandler(nil)
		recorder := httptest.NewRecorder()
		handler.SaveNote(recorder, httptest.NewRequest(http.MethodPost, "/api/save", strings.NewReader(`{"content":"text"}`)))

		assertAPIErrorResponse(t, recorder, http.StatusBadRequest, model.APIError{Code: "missing_field", Message: "Missing required field", Field: "id"})
	})

	t.Run("invalid request", func(t *testing.T) {
		handler := NewNoteHandler(nil)
		recorder := httptest.NewRecorder()
		handler.CreateNote(recorder, httptest.NewRequest(http.MethodPost, "/api/notes", strings.NewReader(`{"name":"note","type":"link"}`)))

		assertAPIErrorResponse(t, recorder, http.StatusBadRequest, model.APIError{Code: "invalid_request", Message: "Invalid request", Field: "type"})
	})

	t.Run("note not found", func(t *testing.T) {
		handler := newFilesystemNoteHandler(t)
		recorder := httptest.NewRecorder()
		handler.GetNote(recorder, httptest.NewRequest(http.MethodGet, "/api/note?id=missing.md", nil))

		assertAPIErrorResponse(t, recorder, http.StatusNotFound, model.APIError{Code: "note_not_found", Message: "Note not found", Field: "id"})
	})

	t.Run("note conflict", func(t *testing.T) {
		base := t.TempDir()
		if err := os.WriteFile(filepath.Join(base, "existing.md"), []byte("existing"), 0644); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}
		handler := NewNoteHandler(service.NewNoteService(handlerNoteRepository{}, base))
		recorder := httptest.NewRecorder()
		handler.CreateNote(recorder, httptest.NewRequest(http.MethodPost, "/api/notes", strings.NewReader(`{"name":"existing","type":"file"}`)))

		assertAPIErrorResponse(t, recorder, http.StatusConflict, model.APIError{Code: "note_conflict", Message: "Note already exists", Field: "name"})
	})

	t.Run("internal error does not leak filesystem details", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(base, []byte("file"), 0o600); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}
		handler := NewNoteHandler(service.NewNoteService(handlerNoteRepository{}, base))
		recorder := httptest.NewRecorder()
		handler.GetRawFile(recorder, httptest.NewRequest(http.MethodGet, "/api/raw?path=asset.txt", nil))

		assertAPIErrorResponse(t, recorder, http.StatusInternalServerError, model.APIError{Code: "internal_error", Message: "Internal server error"})
		if strings.Contains(recorder.Body.String(), base) || strings.Contains(recorder.Body.String(), "directory") {
			t.Fatalf("response leaks filesystem error details: %q", recorder.Body.String())
		}
	})
}

func TestNoteHandlerGetNotesSanitizesInitialSyncFailure(t *testing.T) {
	wantErr := errors.New("index update failed")
	repo := &failingInitialSyncRepository{
		nodes: []model.NoteNode{{ID: "old.md", Name: "old", Path: "old.md", Type: "file"}},
		err:   wantErr,
	}
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "new.md"), []byte("new"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	notes := service.NewNoteService(repo, base)
	t.Cleanup(func() {
		if err := notes.Close(); err != nil {
			t.Errorf("NoteService.Close() error = %v", err)
		}
	})
	if err := notes.SyncFS(); !errors.Is(err, wantErr) {
		t.Fatalf("SyncFS() error = %v, want %v", err, wantErr)
	}

	recorder := httptest.NewRecorder()
	NewNoteHandler(notes).GetNotes(recorder, httptest.NewRequest(http.MethodGet, "/api/notes", nil))

	assertAPIErrorResponse(t, recorder, http.StatusInternalServerError, model.APIError{Code: "internal_error", Message: "Internal server error"})
	if strings.Contains(recorder.Body.String(), "old.md") || strings.Contains(recorder.Body.String(), wantErr.Error()) {
		t.Fatalf("response exposed stale index or sync error: %q", recorder.Body.String())
	}
}

func TestNoteHandlerMethodNotAllowedSetsAllow(t *testing.T) {
	handler := NewNoteHandler(nil)
	tests := []struct {
		name  string
		allow string
		call  func(http.ResponseWriter, *http.Request)
	}{
		{name: "sync", allow: http.MethodPost, call: handler.SyncNotes},
		{name: "save", allow: http.MethodPost, call: handler.SaveNote},
		{name: "create", allow: http.MethodPost, call: handler.CreateNote},
		{name: "delete", allow: http.MethodDelete, call: handler.DeleteNote},
		{name: "rename", allow: http.MethodPut, call: handler.RenameNote},
		{name: "upload", allow: http.MethodPost, call: handler.UploadAsset},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.call(recorder, httptest.NewRequest(http.MethodPatch, "/api/test", nil))

			assertAPIErrorResponse(t, recorder, http.StatusMethodNotAllowed, model.APIError{Code: "method_not_allowed", Message: "Method not allowed"})
			if got := recorder.Header().Get("Allow"); got != test.allow {
				t.Errorf("Allow = %q, want %q", got, test.allow)
			}
		})
	}
}

func TestNoteHandlerGetRawFileErrors(t *testing.T) {
	handler := newFilesystemNoteHandler(t)
	tests := []struct {
		name   string
		path   string
		status int
		want   model.APIError
	}{
		{name: "missing path", status: http.StatusBadRequest, want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "path"}},
		{name: "traversal", path: "../outside.txt", status: http.StatusBadRequest, want: model.APIError{Code: "invalid_path", Message: "Invalid path", Field: "path"}},
		{name: "missing file", path: "missing.txt", status: http.StatusNotFound, want: model.APIError{Code: "file_not_found", Message: "File not found", Field: "path"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/raw?path="+url.QueryEscape(test.path), nil)

			handler.GetRawFile(recorder, request)

			assertAPIErrorResponse(t, recorder, test.status, test.want)
		})
	}
}

func TestNoteHandlerGetRawFileServesDescriptorAndClosesIt(t *testing.T) {
	base := t.TempDir()
	content := []byte("descriptor content")
	if err := os.WriteFile(filepath.Join(base, "asset.txt"), content, 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	noteService := service.NewNoteService(handlerNoteRepository{}, base)
	handler := NewNoteHandler(noteService)
	recorder := httptest.NewRecorder()

	handler.GetRawFile(recorder, httptest.NewRequest(http.MethodGet, "/api/raw?path=asset.txt", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !bytes.Equal(recorder.Body.Bytes(), content) {
		t.Errorf("body = %q, want %q", recorder.Body.Bytes(), content)
	}
	target := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- noteService.SwitchBase(target) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SwitchBase() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SwitchBase() blocked because the raw file was not closed")
	}
}

func TestNoteHandlerGetRawFileDoesNotFollowEscapingSymlink(t *testing.T) {
	base := t.TempDir()
	secret := []byte("outside secret content")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, secret, 0644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "escape.txt")); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("symlink creation is not permitted: %v", err)
		}
		t.Fatalf("os.Symlink() error = %v", err)
	}
	handler := NewNoteHandler(service.NewNoteService(handlerNoteRepository{}, base))
	recorder := httptest.NewRecorder()

	handler.GetRawFile(recorder, httptest.NewRequest(http.MethodGet, "/api/raw?path=escape.txt", nil))

	assertAPIErrorResponse(t, recorder, http.StatusBadRequest, model.APIError{Code: "invalid_path", Message: "Invalid path", Field: "path"})
	if bytes.Contains(recorder.Body.Bytes(), secret) || strings.Contains(recorder.Body.String(), outside) {
		t.Fatalf("escaping symlink leaked outside details: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func newFilesystemNoteHandler(t *testing.T) *NoteHandler {
	t.Helper()
	return NewNoteHandler(service.NewNoteService(handlerNoteRepository{}, t.TempDir()))
}
