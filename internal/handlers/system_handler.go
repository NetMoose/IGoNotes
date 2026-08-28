package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"IGoNotes/internal/service"
)

const (
	directoryPickerUnavailableMessage = "Directory picker is unavailable"
	directoryPickerFailedMessage      = "Failed to select directory"
)

type DirectorySelector interface {
	SelectDirectory(context.Context) (string, error)
}

type SystemHandler struct {
	selector DirectorySelector
}

func NewSystemHandler(selector DirectorySelector) *SystemHandler {
	return &SystemHandler{selector: selector}
}

func (h *SystemHandler) SelectDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
		return
	}

	if h.selector == nil {
		WriteAPIError(w, http.StatusInternalServerError, "directory_picker_failed", directoryPickerFailedMessage, "")
		return
	}

	path, err := h.selector.SelectDirectory(r.Context())
	if errors.Is(err, service.ErrDirectorySelectionCanceled) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if errors.Is(err, service.ErrDirectoryPickerUnavailable) {
		WriteAPIError(w, http.StatusNotImplemented, "directory_picker_unavailable", directoryPickerUnavailableMessage, "")
		return
	}
	if err != nil || path == "" {
		WriteAPIError(w, http.StatusInternalServerError, "directory_picker_failed", directoryPickerFailedMessage, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		Path string `json:"path"`
	}{Path: path})
}
