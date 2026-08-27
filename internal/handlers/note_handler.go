package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

type NoteHandler struct {
	NoteService *service.NoteService
}

// maxAssetRequestSize caps the full multipart HTTP body, including framing.
const maxAssetRequestSize int64 = 10 << 20

func NewNoteHandler(noteService *service.NoteService) *NoteHandler {
	return &NoteHandler{NoteService: noteService}
}

// GetInfo обрабатывает GET /api/info
func (h *NoteHandler) GetInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]string{
		"base_path": h.NoteService.GetBasePath(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// GetNotes обрабатывает GET /api/notes
func (h *NoteHandler) GetNotes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.NoteService.GetTree()
	if err != nil {
		WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if nodes == nil {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(nodes)
}

// SyncNotes обрабатывает POST /api/sync
func (h *NoteHandler) SyncNotes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	if err := h.NoteService.SyncFS(); err != nil {
		WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "ok"}`))
}

// GetNote обрабатывает GET /api/note?id=...
func (h *NoteHandler) GetNote(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		WriteAPIError(w, http.StatusBadRequest, "missing_field", "Missing required field", "id")
		return
	}

	content, err := h.NoteService.GetNoteContent(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			WriteAPIError(w, http.StatusNotFound, "note_not_found", "Note not found", "id")
			return
		}
		if errors.Is(err, service.ErrInvalidNotePath) {
			WriteAPIError(w, http.StatusBadRequest, "invalid_path", "Invalid path", "id")
			return
		}
		WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"id":      id,
		"content": content,
	})
}

// GetRawFile обрабатывает GET /api/raw?path=...
// Отдает сырой файл (например, картинку) из файловой системы.
func (h *NoteHandler) GetRawFile(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		WriteAPIError(w, http.StatusBadRequest, "missing_field", "Missing required field", "path")
		return
	}

	file, info, err := h.NoteService.OpenRawFile(filePath)
	if err != nil {
		if errors.Is(err, service.ErrInvalidNotePath) {
			WriteAPIError(w, http.StatusBadRequest, "invalid_path", "Invalid path", "path")
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			WriteAPIError(w, http.StatusNotFound, "file_not_found", "File not found", "path")
			return
		}
		WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
		return
	}
	defer file.Close()

	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

// SaveNote обрабатывает POST /api/save
func (h *NoteHandler) SaveNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	var req model.SaveNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteAPIError(w, http.StatusBadRequest, "bad_json", "Invalid JSON", "")
		return
	}
	defer r.Body.Close()

	if req.ID == "" {
		WriteAPIError(w, http.StatusBadRequest, "missing_field", "Missing required field", "id")
		return
	}

	if err := h.NoteService.SaveNoteContent(req.ID, req.Content); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			WriteAPIError(w, http.StatusNotFound, "note_not_found", "Note not found", "id")
			return
		}
		if errors.Is(err, service.ErrInvalidNotePath) {
			WriteAPIError(w, http.StatusBadRequest, "invalid_path", "Invalid path", "id")
			return
		}
		WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "saved"}`))
}

// CreateNote обрабатывает POST /api/notes
func (h *NoteHandler) CreateNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	var req model.CreateNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteAPIError(w, http.StatusBadRequest, "bad_json", "Invalid JSON", "")
		return
	}
	defer r.Body.Close()

	if req.Name == "" {
		WriteAPIError(w, http.StatusBadRequest, "missing_field", "Missing required field", "name")
		return
	}
	if req.Type == "" {
		WriteAPIError(w, http.StatusBadRequest, "missing_field", "Missing required field", "type")
		return
	}
	if req.Type != "file" && req.Type != "dir" {
		WriteAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request", "type")
		return
	}

	node, err := h.NoteService.CreateNode(req.ParentID, req.Name, req.Type)
	if err != nil {
		if errors.Is(err, service.ErrAlreadyExists) {
			WriteAPIError(w, http.StatusConflict, "note_conflict", "Note already exists", "name")
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			WriteAPIError(w, http.StatusNotFound, "note_not_found", "Note not found", "parent_id")
			return
		}
		if errors.Is(err, service.ErrInvalidNotePath) {
			WriteAPIError(w, http.StatusBadRequest, "invalid_path", "Invalid path", "parent_id")
			return
		}
		WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(node)
}

// DeleteNote обрабатывает DELETE /api/note?id=...
func (h *NoteHandler) DeleteNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		WriteAPIError(w, http.StatusBadRequest, "missing_field", "Missing required field", "id")
		return
	}

	if err := h.NoteService.DeleteNode(id); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			WriteAPIError(w, http.StatusNotFound, "note_not_found", "Note not found", "id")
			return
		}
		if errors.Is(err, service.ErrInvalidNotePath) {
			WriteAPIError(w, http.StatusBadRequest, "invalid_path", "Invalid path", "id")
			return
		}
		WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
		return
	}

	w.WriteHeader(http.StatusOK)
}

// RenameNote обрабатывает PUT /api/rename
func (h *NoteHandler) RenameNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	var req model.RenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteAPIError(w, http.StatusBadRequest, "bad_json", "Invalid JSON", "")
		return
	}
	defer r.Body.Close()

	if req.ID == "" {
		WriteAPIError(w, http.StatusBadRequest, "missing_field", "Missing required field", "id")
		return
	}
	if req.NewName == "" {
		WriteAPIError(w, http.StatusBadRequest, "missing_field", "Missing required field", "new_name")
		return
	}

	if err := h.NoteService.RenameNode(req.ID, req.NewName); err != nil {
		if errors.Is(err, service.ErrAlreadyExists) {
			WriteAPIError(w, http.StatusConflict, "note_conflict", "Note already exists", "new_name")
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			WriteAPIError(w, http.StatusNotFound, "note_not_found", "Note not found", "id")
			return
		}
		if errors.Is(err, service.ErrInvalidNotePath) {
			WriteAPIError(w, http.StatusBadRequest, "invalid_path", "Invalid path", "id")
			return
		}
		WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "renamed"}`))
}

// UploadAsset обрабатывает POST /api/assets
func (h *NoteHandler) UploadAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAssetRequestSize)
	defer r.Body.Close()
	if err := r.ParseMultipartForm(maxAssetRequestSize); err != nil {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
		WriteAPIError(w, http.StatusBadRequest, "file_too_large", "File too large", "file")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		WriteAPIError(w, http.StatusBadRequest, "missing_file", "No file provided", "file")
		return
	}
	defer file.Close()

	relPath, err := h.NoteService.SaveAsset(file, header.Filename)
	if err != nil {
		WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"path": relPath,
	})
}
