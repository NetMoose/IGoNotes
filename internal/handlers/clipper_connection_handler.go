package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"IGoNotes/internal/model"
)

// ClipperConnections manages credentials through the local settings UI only.
// Authentication is deliberately not part of this management interface.
type ClipperConnections interface {
	Create(context.Context, string) (model.ClipperConnectionCreatedResponse, error)
	List(context.Context) ([]model.ClipperConnection, error)
	Revoke(context.Context, string) error
}

type ClipperConnectionHandler struct {
	connections ClipperConnections
}

func NewClipperConnectionHandler(connections ClipperConnections) *ClipperConnectionHandler {
	return &ClipperConnectionHandler{connections: connections}
}

func (h *ClipperConnectionHandler) List(w http.ResponseWriter, r *http.Request) {
	connections, err := h.connections.List(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if connections == nil {
		connections = []model.ClipperConnection{}
	}
	writeJSON(w, http.StatusOK, model.ClipperConnectionsResponse{Connections: connections})
}

func (h *ClipperConnectionHandler) Create(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	// Even failed issuance responses must not be cached.
	w.Header().Set("Cache-Control", "no-store")
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	var request *model.ClipperConnectionRequest
	if err := decoder.Decode(&request); err != nil || request == nil {
		writeServiceError(w, &model.ClipperError{Code: model.ClipperInvalidRequest})
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeServiceError(w, &model.ClipperError{Code: model.ClipperInvalidRequest})
		return
	}
	response, err := h.connections.Create(r.Context(), request.Label)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *ClipperConnectionHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	ids := query["id"]
	if err != nil || len(ids) != 1 || strings.TrimSpace(ids[0]) == "" {
		writeServiceError(w, &model.ClipperError{Code: model.ClipperInvalidRequest})
		return
	}
	if err := h.connections.Revoke(r.Context(), ids[0]); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = &model.ClipperError{Code: model.ClipperInvalidRequest}
		}
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
