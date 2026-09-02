package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitcmd "IGoNotes/internal/git"
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
		{name: "runtime path changed", err: fmt.Errorf("secret displaced path /private/base: %w", service.ErrRuntimePathChanged), status: http.StatusConflict, want: model.APIError{Code: "runtime_path_changed", Message: "runtime base path changed"}},
		{name: "invalid config", err: fmt.Errorf("request failed: %w", service.ErrInvalidConfig), status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_config", Message: "invalid config"}},
		{name: "invalid mode", err: fmt.Errorf("request failed: %w", service.ErrInvalidMode), status: http.StatusBadRequest, want: model.APIError{Code: "invalid_mode", Message: "invalid mode"}},
		{name: "invalid name", err: fmt.Errorf("request failed: %w", service.ErrInvalidName), status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_base_name", Message: "invalid name"}},
		{name: "invalid path", err: fmt.Errorf("request failed: %w", service.ErrInvalidPath), status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_base_path", Message: "invalid path"}},
		{name: "base not found", err: fmt.Errorf("request failed: %w", service.ErrBaseNotFound), status: http.StatusNotFound, want: model.APIError{Code: "base_not_found", Message: "base not found"}},
		{name: "base name conflict", err: fmt.Errorf("request failed: %w", service.ErrBaseNameConflict), status: http.StatusConflict, want: model.APIError{Code: "base_name_conflict", Message: "base name conflict"}},
		{name: "base path conflict", err: fmt.Errorf("request failed: %w", service.ErrBasePathConflict), status: http.StatusConflict, want: model.APIError{Code: "base_path_conflict", Message: "base path conflict"}},
		{name: "active base", err: fmt.Errorf("request failed: %w", service.ErrActiveBase), status: http.StatusConflict, want: model.APIError{Code: "active_base", Message: "active base"}},
		{name: "last base", err: fmt.Errorf("request failed: %w", service.ErrLastBase), status: http.StatusConflict, want: model.APIError{Code: "last_base", Message: "last base"}},
		{name: "invalid Git URL", err: &service.FieldError{Kind: service.ErrInvalidGitURL, Field: "git_url", Message: "invalid Git URL"}, status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_git_url", Message: "invalid Git URL", Field: "git_url"}},
		{name: "invalid Git branch", err: &service.FieldError{Kind: service.ErrInvalidGitBranch, Field: "git_branch", Message: "invalid Git branch"}, status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_branch", Message: "invalid Git branch", Field: "git_branch"}},
		{name: "invalid Git interval", err: &service.FieldError{Kind: service.ErrInvalidGitInterval, Field: "auto_sync_interval_minutes", Message: "invalid auto sync interval"}, status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_auto_sync_interval", Message: "invalid auto sync interval", Field: "auto_sync_interval_minutes"}},
		{name: "invalid Git template", err: &service.FieldError{Kind: service.ErrInvalidGitTemplate, Field: "git_commit_message_template", Message: "invalid Git commit message template"}, status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_commit_template", Message: "invalid Git commit message template", Field: "git_commit_message_template"}},
		{name: "Git repository in use", err: fmt.Errorf("configure Git: %w", service.ErrGitRepositoryInUse), status: http.StatusConflict, want: model.APIError{Code: "git_repository_in_use", Message: "git repository in use"}},
		{name: "Git unavailable", err: &gitcmd.SafeError{Code: gitcmd.CodeUnavailable, Message: "Git executable is unavailable"}, status: http.StatusServiceUnavailable, want: model.APIError{Code: "git_unavailable", Message: "Git executable is unavailable"}},
		{name: "Git unsupported version", err: &gitcmd.SafeError{Code: gitcmd.CodeUnsupportedVersion, Message: "Git 2.28 or newer is required"}, status: http.StatusUnprocessableEntity, want: model.APIError{Code: "git_version_unsupported", Message: "Git 2.28 or newer is required"}},
		{name: "Git authentication", err: &gitcmd.SafeError{Code: gitcmd.CodeAuthentication, Message: "Git authentication failed", Field: "git_url"}, status: http.StatusUnauthorized, want: model.APIError{Code: "auth_failed", Message: "Git authentication failed", Field: "git_url"}},
		{name: "Git remote unreachable", err: &gitcmd.SafeError{Code: gitcmd.CodeRemoteUnreachable, Message: "Git remote is unreachable", Field: "git_url"}, status: http.StatusBadGateway, want: model.APIError{Code: "remote_unreachable", Message: "Git remote is unreachable", Field: "git_url"}},
		{name: "Git identity missing", err: &gitcmd.SafeError{Code: gitcmd.CodeIdentityMissing, Message: "Git identity is not configured"}, status: http.StatusUnprocessableEntity, want: model.APIError{Code: "identity_missing", Message: "Git identity is not configured"}},
		{name: "Git invalid branch", err: &gitcmd.SafeError{Code: gitcmd.CodeInvalidBranch, Message: "Invalid Git branch", Field: "git_branch"}, status: http.StatusUnprocessableEntity, want: model.APIError{Code: "invalid_branch", Message: "Invalid Git branch", Field: "git_branch"}},
		{name: "Git repository root", err: &gitcmd.SafeError{Code: gitcmd.CodeRepositoryRoot, Message: "Git repository root does not match the base", Field: "base"}, status: http.StatusConflict, want: model.APIError{Code: "repository_root_mismatch", Message: "Git repository root does not match the base", Field: "base"}},
		{name: "Git repository locked", err: &gitcmd.SafeError{Code: gitcmd.CodeRepositoryLocked, Message: "Git repository is locked"}, status: http.StatusConflict, want: model.APIError{Code: "repository_locked", Message: "Git repository is locked"}},
		{name: "Git timeout", err: &gitcmd.SafeError{Code: gitcmd.CodeTimedOut, Message: "Git command timed out"}, status: http.StatusGatewayTimeout, want: model.APIError{Code: "git_timeout", Message: "Git command timed out"}},
		{name: "Git canceled", err: &gitcmd.SafeError{Code: gitcmd.CodeCanceled, Message: "Git command was canceled"}, status: http.StatusRequestTimeout, want: model.APIError{Code: "git_canceled", Message: "Git command was canceled"}},
		{name: "unknown Git command failure", err: &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "secret command diagnostic"}, status: http.StatusInternalServerError, want: model.APIError{Code: "internal_error", Message: "Internal server error"}},
		{name: "Git not repository remains internal", err: &gitcmd.SafeError{Code: gitcmd.CodeNotRepository, Message: "secret repository path"}, status: http.StatusInternalServerError, want: model.APIError{Code: "internal_error", Message: "Internal server error"}},
		{name: "unknown Git code remains internal", err: &gitcmd.SafeError{Code: gitcmd.ErrorCode("future_git_error"), Message: "secret future detail"}, status: http.StatusInternalServerError, want: model.APIError{Code: "internal_error", Message: "Internal server error"}},
		{name: "rollback failure does not leak joined details", err: errors.Join(service.ErrRollbackFailed, errors.New("secret rollback path /private/base")), status: http.StatusInternalServerError, want: model.APIError{Code: "rollback_failed", Message: "Internal server error"}},
		{name: "rollback failure does not leak field details", err: &service.FieldError{Kind: service.ErrRollbackFailed, Field: "secret_field", Message: "secret database rollback failure"}, status: http.StatusInternalServerError, want: model.APIError{Code: "rollback_failed", Message: "Internal server error"}},
		{name: "rollback failure takes precedence in joined errors", err: errors.Join(&service.FieldError{Kind: service.ErrInvalidPath, Field: "path", Message: "secret invalid path"}, service.ErrRollbackFailed), status: http.StatusInternalServerError, want: model.APIError{Code: "rollback_failed", Message: "Internal server error"}},
		{name: "rollback failure takes precedence over Git error", err: errors.Join(&gitcmd.SafeError{Code: gitcmd.CodeAuthentication, Message: "Git authentication failed", Field: "git_url"}, service.ErrRollbackFailed), status: http.StatusInternalServerError, want: model.APIError{Code: "rollback_failed", Message: "Internal server error"}},
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

func TestWriteServiceErrorUsesOnlySafeErrorPublicFields(t *testing.T) {
	safeErr := &gitcmd.SafeError{
		Code:    gitcmd.CodeAuthentication,
		Message: "Git authentication failed",
		Field:   "git_url",
	}
	wrapped := fmt.Errorf("clone https://user:secret@example.test/private.git: %w", safeErr)
	recorder := httptest.NewRecorder()

	writeServiceError(recorder, wrapped)

	assertAPIErrorResponse(t, recorder, http.StatusUnauthorized, model.APIError{
		Code:    "auth_failed",
		Message: "Git authentication failed",
		Field:   "git_url",
	})
	for _, private := range []string{"user:secret", "example.test", "private.git", "clone"} {
		if strings.Contains(recorder.Body.String(), private) {
			t.Errorf("response leaks private wrapper content %q: %q", private, recorder.Body.String())
		}
	}
}

type gitBranchValidatorFunc func(context.Context, string, string) error

func (f gitBranchValidatorFunc) ValidateBranch(ctx context.Context, dir, branch string) error {
	return f(ctx, dir, branch)
}

func TestWriteServiceErrorPreservesOperationalGitBranchValidationFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    *gitcmd.SafeError
		status int
		want   model.APIError
	}{
		{
			name:   "Git unavailable",
			err:    &gitcmd.SafeError{Code: gitcmd.CodeUnavailable, Message: "Git executable is unavailable"},
			status: http.StatusServiceUnavailable,
			want:   model.APIError{Code: "git_unavailable", Message: "Git executable is unavailable"},
		},
		{
			name:   "Git timeout",
			err:    &gitcmd.SafeError{Code: gitcmd.CodeTimedOut, Message: "Git command timed out"},
			status: http.StatusGatewayTimeout,
			want:   model.APIError{Code: "git_timeout", Message: "Git command timed out"},
		},
		{
			name:   "Git canceled",
			err:    &gitcmd.SafeError{Code: gitcmd.CodeCanceled, Message: "Git command was canceled"},
			status: http.StatusRequestTimeout,
			want:   model.APIError{Code: "git_canceled", Message: "Git command was canceled"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := service.NewGitConfigValidator(gitBranchValidatorFunc(
				func(context.Context, string, string) error { return test.err },
			))
			_, validationErr := validator.Validate(context.Background(), "/private/base", model.GitConfigRequest{
				GitURL:    "https://example.test/private.git",
				GitBranch: "main",
			})
			var safeErr *gitcmd.SafeError
			if !errors.As(validationErr, &safeErr) || safeErr != test.err {
				t.Fatalf("Validate() error = %#v, want operational SafeError %#v", validationErr, test.err)
			}

			recorder := httptest.NewRecorder()
			writeServiceError(recorder, fmt.Errorf("validate https://user:secret@example.test/private.git: %w", validationErr))

			assertAPIErrorResponse(t, recorder, test.status, test.want)
			for _, private := range []string{"user:secret", "example.test", "private.git", "/private/base"} {
				if strings.Contains(recorder.Body.String(), private) {
					t.Errorf("response leaks private validation detail %q: %q", private, recorder.Body.String())
				}
			}
		})
	}
}

func TestWriteServiceErrorSafeErrorWinsOverJoinedInvalidBranch(t *testing.T) {
	err := fmt.Errorf("secret wrapper https://token@example.test/repository.git: %w", errors.Join(
		&service.FieldError{Kind: service.ErrInvalidGitBranch, Field: "git_branch", Message: "invalid Git branch"},
		&gitcmd.SafeError{Code: gitcmd.CodeTimedOut, Message: "Git command timed out"},
	))
	recorder := httptest.NewRecorder()

	writeServiceError(recorder, err)

	assertAPIErrorResponse(t, recorder, http.StatusGatewayTimeout, model.APIError{
		Code:    "git_timeout",
		Message: "Git command timed out",
	})
	for _, private := range []string{"secret wrapper", "token", "example.test", "repository.git", "invalid Git branch"} {
		if strings.Contains(recorder.Body.String(), private) {
			t.Errorf("response leaks lower-precedence error detail %q: %q", private, recorder.Body.String())
		}
	}
}

func TestWriteServiceErrorUnknownSafeErrorFailsClosedBeforeServiceMappings(t *testing.T) {
	err := fmt.Errorf("clone https://user:secret@example.test/private.git: %w", errors.Join(
		&service.FieldError{
			Kind:    service.ErrInvalidGitBranch,
			Field:   "secret_branch_field",
			Message: "secret invalid branch detail",
		},
		&gitcmd.SafeError{
			Code:    gitcmd.CodeCommandFailed,
			Message: "secret Git diagnostic",
			Field:   "secret_git_field",
		},
	))
	recorder := httptest.NewRecorder()

	writeServiceError(recorder, err)

	assertAPIErrorResponse(t, recorder, http.StatusInternalServerError, model.APIError{
		Code:    "internal_error",
		Message: internalErrorMessage,
	})
	for _, private := range []string{
		"user:secret",
		"example.test",
		"private.git",
		"secret_branch_field",
		"secret invalid branch detail",
		"secret Git diagnostic",
		"secret_git_field",
	} {
		if strings.Contains(recorder.Body.String(), private) {
			t.Errorf("response leaks unknown Git error detail %q: %q", private, recorder.Body.String())
		}
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

func TestWriteServiceErrorDoesNotLeakServiceFieldErrorCause(t *testing.T) {
	incomplete := false
	store := &handlerConfigStore{config: model.Config{SetupCompleted: &incomplete}}
	notes := service.NewNoteService(handlerNoteRepository{}, "")
	settings, err := service.NewSettingsService(store, notes, "", nil)
	if err != nil {
		t.Fatalf("NewSettingsService() error = %v", err)
	}
	missingPath := filepath.Join(t.TempDir(), "unique-private-missing-path")
	_, serviceErr := settings.CompleteSetup(model.BaseMutationRequest{
		Mode: "connect",
		Name: "base",
		Path: missingPath,
	})
	if !errors.Is(serviceErr, service.ErrInvalidPath) || !errors.Is(serviceErr, os.ErrNotExist) {
		t.Fatalf("CompleteSetup() error = %v, want ErrInvalidPath and os.ErrNotExist", serviceErr)
	}
	recorder := httptest.NewRecorder()

	writeServiceError(recorder, serviceErr)

	assertAPIErrorResponse(t, recorder, http.StatusUnprocessableEntity, model.APIError{
		Code:    "invalid_base_path",
		Message: "resolve base path symlinks",
		Field:   "path",
	})
	if body := recorder.Body.String(); strings.Contains(body, missingPath) || strings.Contains(body, "lstat") || strings.Contains(body, "no such file") {
		t.Fatalf("response leaks service error cause: %q", body)
	}
}

type handlerConfigStore struct {
	config model.Config
}

func (s *handlerConfigStore) Load() (*model.Config, error) {
	config := s.config
	return &config, nil
}

func (s *handlerConfigStore) Save(config *model.Config) error {
	s.config = *config
	return nil
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
