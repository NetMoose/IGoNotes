package model

type CreateNoteRequest struct {
	ParentID string `json:"parent_id"` // пустая строка для корня
	Name     string `json:"name"`
	Type     string `json:"type"` // "file" или "dir"
}

type SaveNoteRequest struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

type RenameRequest struct {
	ID      string `json:"id"`
	NewName string `json:"new_name"`
}

type BaseMutationRequest struct {
	Mode string `json:"mode"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type BaseUpdateRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type BaseSwitchRequest struct {
	Name string `json:"name"`
}

type SettingsResponse struct {
	Config   Config `json:"config"`
	BasePath string `json:"base_path"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}
