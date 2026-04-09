package model

// Config представляет конфигурацию приложения
type Config struct {
	BaseDir     string `json:"base_dir"`
	Bases       []Base `json:"bases"`
	CurrentBase string `json:"current_base"`
}

// Base представляет базу заметок
type Base struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	GitURL   string `json:"git_url,omitempty"`
	AutoSync bool   `json:"auto_sync"`
}