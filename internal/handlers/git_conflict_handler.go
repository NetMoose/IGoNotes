package handlers

import (
	"context"
	"net/http"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
)

type gitConflictManager interface {
	ListConflicts(context.Context, string) (model.GitConflictListResponse, error)
	ResolveConflict(context.Context, model.GitConflictResolveRequest) (model.GitConflictResolveResponse, error)
	QueueConflictComplete(context.Context, string) (gitcmd.Operation, bool, error)
	QueueConflictAbort(context.Context, string) (gitcmd.Operation, bool, error)
}

type GitConflictHandler struct {
	manager gitConflictManager
}

func NewGitConflictHandler(manager gitConflictManager) *GitConflictHandler {
	return &GitConflictHandler{manager: manager}
}

func (h *GitConflictHandler) List(w http.ResponseWriter, r *http.Request) {
	base, ok := readBaseQuery(w, r, true)
	if !ok {
		return
	}

	response, err := h.manager.ListConflicts(r.Context(), base)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *GitConflictHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var request model.GitConflictResolveRequest
	if err := decodeSingleJSON(r.Body, &request); err != nil {
		writeBadJSON(w)
		return
	}
	if request.Base == "" {
		writeMissingField(w, "base")
		return
	}
	if request.OperationID == "" {
		writeMissingField(w, "operation_id")
		return
	}
	if request.ConflictID == "" {
		writeMissingField(w, "conflict_id")
		return
	}
	if request.Path == "" {
		writeMissingField(w, "path")
		return
	}
	if request.Action == "" {
		writeMissingField(w, "action")
		return
	}
	if !isGitConflictAction(request.Action) {
		WriteAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid request", "action")
		return
	}
	if request.Action == model.GitConflictManual && request.Content == nil {
		writeMissingField(w, "content")
		return
	}
	if request.Action == model.GitConflictKeepBoth {
		if request.LocalPath == "" {
			writeMissingField(w, "local_path")
			return
		}
		if request.RemotePath == "" {
			writeMissingField(w, "remote_path")
			return
		}
	}

	response, err := h.manager.ResolveConflict(r.Context(), request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *GitConflictHandler) Complete(w http.ResponseWriter, r *http.Request) {
	base, ok := readBaseQuery(w, r, true)
	if !ok {
		return
	}

	operation, deduplicated, err := h.manager.QueueConflictComplete(r.Context(), base)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, operationResponse(operation, deduplicated))
}

func (h *GitConflictHandler) Abort(w http.ResponseWriter, r *http.Request) {
	base, ok := readBaseQuery(w, r, true)
	if !ok {
		return
	}

	operation, deduplicated, err := h.manager.QueueConflictAbort(r.Context(), base)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, operationResponse(operation, deduplicated))
}

func isGitConflictAction(action model.GitConflictAction) bool {
	switch action {
	case model.GitConflictUseLocal,
		model.GitConflictUseRemote,
		model.GitConflictManual,
		model.GitConflictDelete,
		model.GitConflictKeepBoth:
		return true
	default:
		return false
	}
}
