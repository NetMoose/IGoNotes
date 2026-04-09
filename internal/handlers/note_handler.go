package handlers

import (
	"net/http"

	"IGoNotes/internal/service"
)

// NoteHandler обрабатывает HTTP-запросы, связанные с заметками
type NoteHandler struct {
	NoteService *service.NoteService
}

// NewNoteHandler создает новый экземпляр NoteHandler
func NewNoteHandler(noteService *service.NoteService) *NoteHandler {
	return &NoteHandler{NoteService: noteService}
}

// GetNotes обрабатывает GET /api/notes
func (h *NoteHandler) GetNotes(w http.ResponseWriter, r *http.Request) {
	h.NoteService.GetNotes(w, r)
}

// GetNote обрабатывает GET /api/note
func (h *NoteHandler) GetNote(w http.ResponseWriter, r *http.Request) {
	h.NoteService.GetNote(w, r)
}

// SaveNote обрабатывает POST /api/save
func (h *NoteHandler) SaveNote(w http.ResponseWriter, r *http.Request) {
	h.NoteService.SaveNote(w, r)
}