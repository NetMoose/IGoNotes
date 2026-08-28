package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

type directorySelectorFunc func(context.Context) (string, error)

func (f directorySelectorFunc) SelectDirectory(ctx context.Context) (string, error) {
	return f(ctx)
}

func TestSystemHandlerSelectDirectoryRejectsNonPOSTMethods(t *testing.T) {
	for _, method := range []string{
		http.MethodGet,
		http.MethodPut,
		http.MethodDelete,
		http.MethodPatch,
		http.MethodOptions,
	} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			handler := NewSystemHandler(directorySelectorFunc(func(context.Context) (string, error) {
				calls++
				return "/should/not/be/used", nil
			}))
			recorder := httptest.NewRecorder()

			handler.SelectDirectory(recorder, httptest.NewRequest(method, "/api/system/select-directory", nil))

			assertAPIErrorResponse(t, recorder, http.StatusMethodNotAllowed, model.APIError{
				Code:    "method_not_allowed",
				Message: "Method not allowed",
			})
			if got := recorder.Header().Get("Allow"); got != http.MethodPost {
				t.Errorf("Allow = %q, want %q", got, http.MethodPost)
			}
			if calls != 0 {
				t.Errorf("selector calls = %d, want 0", calls)
			}
		})
	}
}

func TestSystemHandlerSelectDirectoryReturnsSelectedPath(t *testing.T) {
	wantPath := `C:\Папка с пробелами\研究`
	calls := 0
	handler := NewSystemHandler(directorySelectorFunc(func(context.Context) (string, error) {
		calls++
		return wantPath, nil
	}))
	recorder := httptest.NewRecorder()

	handler.SelectDirectory(recorder, httptest.NewRequest(http.MethodPost, "/api/system/select-directory", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %q", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not valid JSON: %v; body = %q", err, recorder.Body.String())
	}
	if len(response) != 1 {
		t.Fatalf("response fields = %v, want only path", response)
	}
	var gotPath string
	if err := json.Unmarshal(response["path"], &gotPath); err != nil {
		t.Fatalf("path is not a JSON string: %v", err)
	}
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if calls != 1 {
		t.Errorf("selector calls = %d, want 1", calls)
	}
}

func TestSystemHandlerSelectDirectoryReturnsNoContentWhenCanceled(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "sentinel", err: service.ErrDirectorySelectionCanceled},
		{name: "wrapped sentinel", err: fmt.Errorf("picker canceled: %w", service.ErrDirectorySelectionCanceled)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := NewSystemHandler(directorySelectorFunc(func(context.Context) (string, error) {
				calls++
				return "", test.err
			}))
			recorder := httptest.NewRecorder()

			handler.SelectDirectory(recorder, httptest.NewRequest(http.MethodPost, "/api/system/select-directory", nil))

			if recorder.Code != http.StatusNoContent {
				t.Errorf("status = %d, want %d", recorder.Code, http.StatusNoContent)
			}
			if recorder.Body.Len() != 0 {
				t.Errorf("body = %q, want empty", recorder.Body.String())
			}
			if _, ok := recorder.Header()["Content-Type"]; ok {
				t.Errorf("Content-Type metadata = %q, want absent", recorder.Header().Values("Content-Type"))
			}
			if calls != 1 {
				t.Errorf("selector calls = %d, want 1", calls)
			}
		})
	}
}

func TestSystemHandlerSelectDirectoryMapsErrors(t *testing.T) {
	tests := []struct {
		name     string
		selector DirectorySelector
		want     model.APIError
		status   int
	}{
		{
			name: "unavailable",
			selector: directorySelectorFunc(func(context.Context) (string, error) {
				return "", service.ErrDirectoryPickerUnavailable
			}),
			status: http.StatusNotImplemented,
			want: model.APIError{
				Code:    "directory_picker_unavailable",
				Message: "Directory picker is unavailable",
			},
		},
		{
			name: "wrapped unavailable",
			selector: directorySelectorFunc(func(context.Context) (string, error) {
				return "", fmt.Errorf("missing zenity: %w", service.ErrDirectoryPickerUnavailable)
			}),
			status: http.StatusNotImplemented,
			want: model.APIError{
				Code:    "directory_picker_unavailable",
				Message: "Directory picker is unavailable",
			},
		},
		{
			name: "diagnostic failure is sanitized",
			selector: directorySelectorFunc(func(context.Context) (string, error) {
				return `/private/returned/path`, errors.New("zenity stderr: secret-token at /private/base")
			}),
			status: http.StatusInternalServerError,
			want: model.APIError{
				Code:    "directory_picker_failed",
				Message: "Failed to select directory",
			},
		},
		{
			name: "context cancellation is sanitized",
			selector: directorySelectorFunc(func(context.Context) (string, error) {
				return "", fmt.Errorf("platform command: %w", context.Canceled)
			}),
			status: http.StatusInternalServerError,
			want: model.APIError{
				Code:    "directory_picker_failed",
				Message: "Failed to select directory",
			},
		},
		{
			name: "deadline is sanitized",
			selector: directorySelectorFunc(func(context.Context) (string, error) {
				return "", fmt.Errorf("platform command: %w", context.DeadlineExceeded)
			}),
			status: http.StatusInternalServerError,
			want: model.APIError{
				Code:    "directory_picker_failed",
				Message: "Failed to select directory",
			},
		},
		{
			name:     "nil selector",
			selector: nil,
			status:   http.StatusInternalServerError,
			want: model.APIError{
				Code:    "directory_picker_failed",
				Message: "Failed to select directory",
			},
		},
		{
			name: "empty successful path",
			selector: directorySelectorFunc(func(context.Context) (string, error) {
				return "", nil
			}),
			status: http.StatusInternalServerError,
			want: model.APIError{
				Code:    "directory_picker_failed",
				Message: "Failed to select directory",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			selector := test.selector
			if selector != nil {
				original := selector
				selector = directorySelectorFunc(func(ctx context.Context) (string, error) {
					calls++
					return original.SelectDirectory(ctx)
				})
			}
			handler := NewSystemHandler(selector)
			recorder := httptest.NewRecorder()

			handler.SelectDirectory(recorder, httptest.NewRequest(http.MethodPost, "/api/system/select-directory", nil))

			assertAPIErrorResponse(t, recorder, test.status, test.want)
			wantCalls := 1
			if test.selector == nil {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Errorf("selector calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestSystemHandlerSelectDirectoryPassesRequestContextExactly(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "request marker"))
	cancel()

	var gotContext context.Context
	calls := 0
	handler := NewSystemHandler(directorySelectorFunc(func(selectorContext context.Context) (string, error) {
		calls++
		gotContext = selectorContext
		return "", selectorContext.Err()
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/system/select-directory", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	handler.SelectDirectory(recorder, request)

	assertAPIErrorResponse(t, recorder, http.StatusInternalServerError, model.APIError{
		Code:    "directory_picker_failed",
		Message: "Failed to select directory",
	})
	if gotContext != request.Context() {
		t.Fatal("selector did not receive the exact request context")
	}
	if gotContext.Err() != context.Canceled {
		t.Errorf("selector context error = %v, want context.Canceled", gotContext.Err())
	}
	if got := gotContext.Value(contextKey{}); got != "request marker" {
		t.Errorf("selector context value = %v, want request marker", got)
	}
	if calls != 1 {
		t.Errorf("selector calls = %d, want 1", calls)
	}
}
