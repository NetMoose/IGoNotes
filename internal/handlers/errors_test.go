package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

func TestWriteServiceError(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		want   model.APIError
	}{
		{name: "setup required", err: fmt.Errorf("request failed: %w", service.ErrSetupRequired), status: http.StatusPreconditionRequired, want: model.APIError{Code: "setup_required", Message: "setup required"}},
		{name: "setup already completed", err: fmt.Errorf("request failed: %w", service.ErrSetupAlreadyCompleted), status: http.StatusConflict, want: model.APIError{Code: "setup_already_completed", Message: "setup already completed"}},
		{name: "setup cannot reopen", err: fmt.Errorf("request failed: %w", service.ErrSetupCannotReopen), status: http.StatusConflict, want: model.APIError{Code: "setup_cannot_reopen", Message: "setup cannot be reopened"}},
		{name: "invalid config", err: fmt.Errorf("request failed: %w", service.ErrInvalidConfig), status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_config", Message: "invalid config"}},
		{name: "invalid mode", err: fmt.Errorf("request failed: %w", service.ErrInvalidMode), status: http.StatusBadRequest, want: model.APIError{Code: "invalid_mode", Message: "invalid mode"}},
		{name: "invalid name", err: fmt.Errorf("request failed: %w", service.ErrInvalidName), status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_base_name", Message: "invalid name"}},
		{name: "invalid path", err: fmt.Errorf("request failed: %w", service.ErrInvalidPath), status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_base_path", Message: "invalid path"}},
		{name: "base not found", err: fmt.Errorf("request failed: %w", service.ErrBaseNotFound), status: http.StatusNotFound, want: model.APIError{Code: "base_not_found", Message: "base not found"}},
		{name: "base name conflict", err: fmt.Errorf("request failed: %w", service.ErrBaseNameConflict), status: http.StatusConflict, want: model.APIError{Code: "base_name_conflict", Message: "base name conflict"}},
		{name: "base path conflict", err: fmt.Errorf("request failed: %w", service.ErrBasePathConflict), status: http.StatusConflict, want: model.APIError{Code: "base_path_conflict", Message: "base path conflict"}},
		{name: "active base", err: fmt.Errorf("request failed: %w", service.ErrActiveBase), status: http.StatusConflict, want: model.APIError{Code: "active_base", Message: "active base"}},
		{name: "last base", err: fmt.Errorf("request failed: %w", service.ErrLastBase), status: http.StatusConflict, want: model.APIError{Code: "last_base", Message: "last base"}},
		{name: "rollback failure does not leak joined details", err: errors.Join(service.ErrRollbackFailed, errors.New("secret rollback path /private/base")), status: http.StatusInternalServerError, want: model.APIError{Code: "rollback_failed", Message: "Internal server error"}},
		{name: "rollback failure does not leak field details", err: &service.FieldError{Kind: service.ErrRollbackFailed, Field: "secret_field", Message: "secret database rollback failure"}, status: http.StatusInternalServerError, want: model.APIError{Code: "rollback_failed", Message: "Internal server error"}},
		{name: "rollback failure takes precedence in joined errors", err: errors.Join(&service.FieldError{Kind: service.ErrInvalidPath, Field: "path", Message: "secret invalid path"}, service.ErrRollbackFailed), status: http.StatusInternalServerError, want: model.APIError{Code: "rollback_failed", Message: "Internal server error"}},
		{name: "unknown failure does not leak wrapped details", err: fmt.Errorf("database password leaked: %w", errors.New("driver failure")), status: http.StatusInternalServerError, want: model.APIError{Code: "internal_error", Message: "Internal server error"}},
		{name: "wrapped field error", err: fmt.Errorf("validation: %w", &service.FieldError{Kind: service.ErrInvalidName, Field: "name", Message: "choose another base name"}), status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_base_name", Message: "choose another base name", Field: "name"}},
		{name: "joined field error", err: errors.Join(errors.New("validation failed"), &service.FieldError{Kind: service.ErrInvalidPath, Field: "path", Message: "select an existing directory"}), status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_base_path", Message: "select an existing directory", Field: "path"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()

			writeServiceError(recorder, test.err)

			assertAPIErrorResponse(t, recorder, test.status, test.want)
			if test.status == http.StatusInternalServerError && strings.Contains(recorder.Body.String(), "secret") {
				t.Fatalf("response leaks internal error details: %q", recorder.Body.String())
			}
		})
	}
}

func TestWriteAPIError(t *testing.T) {
	recorder := httptest.NewRecorder()

	WriteAPIError(recorder, http.StatusBadRequest, "missing_field", "id is required", "id")

	assertAPIErrorResponse(t, recorder, http.StatusBadRequest, model.APIError{
		Code:    "missing_field",
		Message: "id is required",
		Field:   "id",
	})
}

func assertAPIErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, want model.APIError) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d", recorder.Code, wantStatus)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	wantBody := string(wantJSON) + "\n"
	if got := recorder.Body.String(); got != wantBody {
		t.Errorf("body = %q, want %q", got, wantBody)
	}
}
