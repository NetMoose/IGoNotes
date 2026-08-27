package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

const (
	badJSONMessage      = "Invalid JSON"
	missingFieldMessage = "Missing required field"
)

type SettingsHandler struct {
	settings *service.SettingsService
}

func NewSettingsHandler(settings *service.SettingsService) *SettingsHandler {
	return &SettingsHandler{settings: settings}
}

func (h *SettingsHandler) GetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.settings.GetConfig())
}

func (h *SettingsHandler) CompleteSetup(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var request model.BaseMutationRequest
	if err := decodeSingleJSON(r.Body, &request); err != nil {
		writeBadJSON(w)
		return
	}
	if request.Mode == "" {
		writeMissingField(w, "mode")
		return
	}
	if request.Name == "" {
		writeMissingField(w, "name")
		return
	}
	if request.Path == "" {
		writeMissingField(w, "path")
		return
	}

	response, err := h.settings.CompleteSetup(request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *SettingsHandler) AddBase(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var request model.BaseMutationRequest
	if err := decodeSingleJSON(r.Body, &request); err != nil {
		writeBadJSON(w)
		return
	}
	if request.Mode == "" {
		writeMissingField(w, "mode")
		return
	}
	if request.Name == "" {
		writeMissingField(w, "name")
		return
	}
	if request.Path == "" {
		writeMissingField(w, "path")
		return
	}

	response, err := h.settings.AddBase(request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *SettingsHandler) UpdateBase(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	oldName := r.URL.Query().Get("name")
	if oldName == "" {
		writeMissingField(w, "name")
		return
	}

	var request model.BaseUpdateRequest
	if err := decodeSingleJSON(r.Body, &request); err != nil {
		writeBadJSON(w)
		return
	}
	if request.Name == "" {
		writeMissingField(w, "name")
		return
	}
	if request.Path == "" {
		writeMissingField(w, "path")
		return
	}

	response, err := h.settings.UpdateBase(oldName, request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *SettingsHandler) ForgetBase(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	name := r.URL.Query().Get("name")
	if name == "" {
		writeMissingField(w, "name")
		return
	}

	response, err := h.settings.ForgetBase(name)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *SettingsHandler) SwitchBase(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var request model.BaseSwitchRequest
	if err := decodeSingleJSON(r.Body, &request); err != nil {
		writeBadJSON(w)
		return
	}
	if request.Name == "" {
		writeMissingField(w, "name")
		return
	}

	response, err := h.settings.SwitchBase(request.Name)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *SettingsHandler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var config model.Config
	if err := decodeSingleJSON(r.Body, &config); err != nil {
		writeBadJSON(w)
		return
	}

	response, err := h.settings.ReplaceConfig(config)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func decodeSingleJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(target); err != nil {
		return err
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeBadJSON(w http.ResponseWriter) {
	WriteAPIError(w, http.StatusBadRequest, "bad_json", badJSONMessage, "")
}

func writeMissingField(w http.ResponseWriter, field string) {
	WriteAPIError(w, http.StatusBadRequest, "missing_field", missingFieldMessage, field)
}
