package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

type NoteHandler struct {
	NoteService *service.NoteService
}

func NewNoteHandler(noteService *service.NoteService) *NoteHandler {
	return &NoteHandler{NoteService: noteService}
}

// GetNotes обрабатывает GET /api/notes
func (h *NoteHandler) GetNotes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.NoteService.GetTree()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if nodes == nil {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(nodes)
}

// GetNote обрабатывает GET /api/note?id=...
func (h *NoteHandler) GetNote(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}

	content, err := h.NoteService.GetNoteContent(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}

	fullPath, err := h.NoteService.GetAbsoluteFilePath(filePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	http.ServeFile(w, r, fullPath)
}

// SaveNote обрабатывает POST /api/save
func (h *NoteHandler) SaveNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req model.SaveNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if req.ID == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	if err := h.NoteService.SaveNoteContent(req.ID, req.Content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "saved"}`))
}

// CreateNote обрабатывает POST /api/notes
func (h *NoteHandler) CreateNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req model.CreateNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if req.Name == "" || (req.Type != "file" && req.Type != "dir") {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	node, err := h.NoteService.CreateNode(req.ParentID, req.Name, req.Type)
	if err != nil {
		if errors.Is(err, service.ErrAlreadyExists) {
			// Если объект уже существует, возвращаем 409 Conflict и данные объекта
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(node)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(node)
}

// DeleteNote обрабатывает DELETE /api/note?id=...
func (h *NoteHandler) DeleteNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	if err := h.NoteService.DeleteNode(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// RenameNote обрабатывает PUT /api/rename
func (h *NoteHandler) RenameNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req model.RenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if req.ID == "" || req.NewName == "" {
		http.Error(w, "missing id or new_name", http.StatusBadRequest)
		return
	}

	if err := h.NoteService.RenameNode(req.ID, req.NewName); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "renamed"}`))
}

// UploadAsset обрабатывает POST /api/assets
func (h *NoteHandler) UploadAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Ограничиваем размер загружаемого файла (например, 10 MB)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "File too large", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "No file provided", http.StatusBadRequest)
		return
	}
	defer file.Close()

	relPath, err := h.NoteService.SaveAsset(file, header.Filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"path": relPath,
	})
}
