package model

type CreateNoteRequest struct {
	ParentID string `json:"parent_id"` // пустая строка для корня
	Name     string `json:"name"`
	Type     string `json:"type"`      // "file" или "dir"
}

type SaveNoteRequest struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

type RenameRequest struct {
	ID      string `json:"id"`
	NewName string `json:"new_name"`
}
