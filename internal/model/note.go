package model

import (
	"time"
)

// Note представляет заметку в системе
type Note struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Path      string    `json:"path"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ParentID  string    `json:"parent_id"`
}

// NoteNode представляет узел в дереве заметок
type NoteNode struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Type     string       `json:"type"` // "dir" или "file"
	Path     string       `json:"path"`
	Children []NoteNode   `json:"children,omitempty"`
	Note     *NoteSummary `json:"note,omitempty"`
}

// NoteSummary краткая информация о заметке
type NoteSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Tags      []string  `json:"tags"`
	UpdatedAt time.Time `json:"updated_at"`
}