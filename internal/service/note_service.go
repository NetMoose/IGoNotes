package service

import (
	"net/http"
)

// NoteService предоставляет методы для работы с заметками
type NoteService struct {}

// NewNoteService создает новый экземпляр NoteService
func NewNoteService() *NoteService {
	return &NoteService{}
}

// GetNotes возвращает дерево заметок
func (s *NoteService) GetNotes(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("[]"))
}

// GetNote возвращает содержимое заметки
func (s *NoteService) GetNote(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("# Заметка\n\nКонтент заметки"))
}

// SaveNote сохраняет заметку
func (s *NoteService) SaveNote(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"status\": \"saved\"}"))
}