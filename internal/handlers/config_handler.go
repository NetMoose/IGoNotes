package handlers

import (
	"encoding/json"
	"net/http"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

// ConfigHandler обрабатывает HTTP-запросы, связанные с конфигурацией
type ConfigHandler struct {
	ConfigService *service.ConfigService
}

// NewConfigHandler создает новый экземпляр ConfigHandler
func NewConfigHandler(configService *service.ConfigService) *ConfigHandler {
	return &ConfigHandler{ConfigService: configService}
}

// GetConfig обрабатывает GET /api/config
func (h *ConfigHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	config, err := h.ConfigService.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(config)
}

// SaveConfig обрабатывает PUT /api/config
func (h *ConfigHandler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var config model.Config
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.ConfigService.Save(&config); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
}