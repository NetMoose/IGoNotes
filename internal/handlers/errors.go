package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

const internalErrorMessage = "Internal server error"

type serviceErrorMapping struct {
	kind    error
	status  int
	code    string
	message string
}

var serviceErrorMappings = []serviceErrorMapping{
	{service.ErrSetupRequired, http.StatusPreconditionRequired, "setup_required", service.ErrSetupRequired.Error()},
	{service.ErrSetupAlreadyCompleted, http.StatusConflict, "setup_already_completed", service.ErrSetupAlreadyCompleted.Error()},
	{service.ErrSetupCannotReopen, http.StatusConflict, "setup_cannot_reopen", service.ErrSetupCannotReopen.Error()},
	{service.ErrRuntimePathChanged, http.StatusConflict, "runtime_path_changed", service.ErrRuntimePathChanged.Error()},
	{service.ErrInvalidConfig, http.StatusUnprocessableEntity, "invalid_config", service.ErrInvalidConfig.Error()},
	{service.ErrInvalidMode, http.StatusBadRequest, "invalid_mode", service.ErrInvalidMode.Error()},
	{service.ErrInvalidName, http.StatusUnprocessableEntity, "invalid_base_name", service.ErrInvalidName.Error()},
	{service.ErrInvalidPath, http.StatusUnprocessableEntity, "invalid_base_path", service.ErrInvalidPath.Error()},
	{service.ErrBaseNotFound, http.StatusNotFound, "base_not_found", service.ErrBaseNotFound.Error()},
	{service.ErrBaseNameConflict, http.StatusConflict, "base_name_conflict", service.ErrBaseNameConflict.Error()},
	{service.ErrBasePathConflict, http.StatusConflict, "base_path_conflict", service.ErrBasePathConflict.Error()},
	{service.ErrActiveBase, http.StatusConflict, "active_base", service.ErrActiveBase.Error()},
	{service.ErrLastBase, http.StatusConflict, "last_base", service.ErrLastBase.Error()},
	{service.ErrInvalidGitURL, http.StatusUnprocessableEntity, "invalid_git_url", service.ErrInvalidGitURL.Error()},
	{service.ErrInvalidGitBranch, http.StatusUnprocessableEntity, "invalid_branch", service.ErrInvalidGitBranch.Error()},
	{service.ErrInvalidGitInterval, http.StatusUnprocessableEntity, "invalid_auto_sync_interval", service.ErrInvalidGitInterval.Error()},
	{service.ErrInvalidGitTemplate, http.StatusUnprocessableEntity, "invalid_commit_template", service.ErrInvalidGitTemplate.Error()},
	{service.ErrGitRepositoryInUse, http.StatusConflict, "git_repository_in_use", service.ErrGitRepositoryInUse.Error()},
}

func WriteAPIError(w http.ResponseWriter, status int, code, message, field string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.APIError{Code: code, Message: message, Field: field})
}

func writeServiceError(w http.ResponseWriter, err error) {
	if errors.Is(err, service.ErrRollbackFailed) {
		WriteAPIError(w, http.StatusInternalServerError, "rollback_failed", internalErrorMessage, "")
		return
	}

	var safeErr *gitcmd.SafeError
	if errors.As(err, &safeErr) && safeErr != nil {
		status := 0
		switch safeErr.Code {
		case gitcmd.CodeUnavailable:
			status = http.StatusServiceUnavailable
		case gitcmd.CodeUnsupportedVersion, gitcmd.CodeIdentityMissing, gitcmd.CodeInvalidBranch:
			status = http.StatusUnprocessableEntity
		case gitcmd.CodeAuthentication:
			status = http.StatusUnauthorized
		case gitcmd.CodeRemoteUnreachable:
			status = http.StatusBadGateway
		case gitcmd.CodeRepositoryRoot, gitcmd.CodeRepositoryLocked:
			status = http.StatusConflict
		case gitcmd.CodeTimedOut:
			status = http.StatusGatewayTimeout
		case gitcmd.CodeCanceled:
			status = http.StatusRequestTimeout
		}
		if status == 0 {
			WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
			return
		}
		WriteAPIError(w, status, string(safeErr.Code), safeErr.Message, safeErr.Field)
		return
	}

	for _, mapping := range serviceErrorMappings {
		if !errors.Is(err, mapping.kind) {
			continue
		}

		message := mapping.message
		field := ""
		var fieldErr *service.FieldError
		if mapping.status < http.StatusInternalServerError && errors.As(err, &fieldErr) && errors.Is(fieldErr.Kind, mapping.kind) {
			message = fieldErr.Message
			field = fieldErr.Field
		}
		WriteAPIError(w, mapping.status, mapping.code, message, field)
		return
	}

	WriteAPIError(w, http.StatusInternalServerError, "internal_error", internalErrorMessage, "")
}
