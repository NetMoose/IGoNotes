package handlers

import (
	"bytes"
	"encoding/json"
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
func (handlerNoteRepository) DeleteNode(string) error                                  { return nil }

func TestNoteHandlerReturnsStructuredErrors(t *testing.T) {
	t.Run("method not allowed", func(t *testing.T) {
		handler := NewNoteHandler(nil)
		recorder := httptest.NewRecorder()
		handler.SyncNotes(recorder, httptest.NewRequest(http.MethodGet, "/api/sync", nil))

		assertAPIErrorResponse(t, recorder, http.StatusMethodNotAllowed, model.APIError{Code: "method_not_allowed", Message: "Method not allowed"})
	})

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
		base := t.TempDir()
		handler := NewNoteHandler(service.NewNoteService(handlerNoteRepository{}, base))
		recorder := httptest.NewRecorder()
		handler.GetNote(recorder, httptest.NewRequest(http.MethodGet, "/api/note?id=.", nil))

		assertAPIErrorResponse(t, recorder, http.StatusInternalServerError, model.APIError{Code: "internal_error", Message: "Internal server error"})
		if strings.Contains(recorder.Body.String(), base) || strings.Contains(recorder.Body.String(), "directory") {
			t.Fatalf("response leaks filesystem error details: %q", recorder.Body.String())
		}
	})
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

	if recorder.Code == http.StatusOK || bytes.Contains(recorder.Body.Bytes(), secret) {
		t.Fatalf("escaping symlink exposed outside file: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var apiErr model.APIError
	if err := json.Unmarshal(recorder.Body.Bytes(), &apiErr); err != nil {
		t.Fatalf("response is not an APIError: %v; body=%q", err, recorder.Body.String())
	}
	if apiErr.Code == "" || strings.Contains(apiErr.Message, outside) {
		t.Fatalf("invalid or leaking APIError: %#v", apiErr)
	}
}

func newFilesystemNoteHandler(t *testing.T) *NoteHandler {
	t.Helper()
	return NewNoteHandler(service.NewNoteService(handlerNoteRepository{}, t.TempDir()))
}
