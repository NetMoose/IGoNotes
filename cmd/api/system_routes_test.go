package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"IGoNotes/internal/handlers"
)

type systemRouteSelectorFunc func(context.Context) (string, error)

func (f systemRouteSelectorFunc) SelectDirectory(ctx context.Context) (string, error) {
	return f(ctx)
}

type incompleteSetupState struct{}

func (incompleteSetupState) SetupCompleted() bool { return false }

func TestRegisterSystemRoutesSelectsDirectoryBeforeSetup(t *testing.T) {
	calls := 0
	systemHandler := handlers.NewSystemHandler(systemRouteSelectorFunc(func(context.Context) (string, error) {
		calls++
		return "/home/alice/notes", nil
	}))
	router := newSystemRoutesRouter(t, systemHandler, nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, newSystemRouteRequest(http.MethodPost, "/api/system/select-directory"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %q", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v; body = %q", err, recorder.Body.String())
	}
	if response.Path != "/home/alice/notes" {
		t.Errorf("path = %q, want %q", response.Path, "/home/alice/notes")
	}
	if calls != 1 {
		t.Errorf("selector calls = %d, want 1", calls)
	}
}

func TestRegisterSystemRoutesLeavesMethodHandlingToSystemHandler(t *testing.T) {
	calls := 0
	systemHandler := handlers.NewSystemHandler(systemRouteSelectorFunc(func(context.Context) (string, error) {
		calls++
		return "/should/not/be/selected", nil
	}))
	router := newSystemRoutesRouter(t, systemHandler, nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, newSystemRouteRequest(http.MethodGet, "/api/system/select-directory"))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d; body = %q", recorder.Code, http.StatusMethodNotAllowed, recorder.Body.String())
	}
	if got := recorder.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want %q", got, http.MethodPost)
	}
	if calls != 0 {
		t.Errorf("selector calls = %d, want 0", calls)
	}
}

func TestRegisterSystemRoutesRejectsForeignRequestsBeforeSelector(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{
			name: "foreign Host",
			mutate: func(request *http.Request) {
				request.Host = "attacker.example"
			},
		},
		{
			name: "foreign Origin",
			mutate: func(request *http.Request) {
				request.Header.Set("Origin", "http://attacker.example")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			systemHandler := handlers.NewSystemHandler(systemRouteSelectorFunc(func(context.Context) (string, error) {
				calls++
				return "", context.Canceled
			}))
			router := newSystemRoutesRouter(t, systemHandler, nil)
			request := newSystemRouteRequest(http.MethodPost, "/api/system/select-directory")
			test.mutate(request)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d; body = %q", recorder.Code, http.StatusForbidden, recorder.Body.String())
			}
			if calls != 0 {
				t.Errorf("selector calls = %d, want 0", calls)
			}
			if strings.Contains(recorder.Body.String(), context.Canceled.Error()) {
				t.Errorf("response exposed selector diagnostic: %q", recorder.Body.String())
			}
		})
	}
}

func TestRegisterSystemRoutesRegistersOnlyExactPath(t *testing.T) {
	calls := 0
	systemHandler := handlers.NewSystemHandler(systemRouteSelectorFunc(func(context.Context) (string, error) {
		calls++
		return "/should/not/be/selected", nil
	}))
	var fallbackPaths []string
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackPaths = append(fallbackPaths, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	router := newSystemRoutesRouter(t, systemHandler, fallback)

	for _, path := range []string{
		"/api/system/select-directory/",
		"/api/system/select-directory-nearby",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, newSystemRouteRequest(http.MethodPost, path))
		if recorder.Code != http.StatusTeapot {
			t.Errorf("%s status = %d, want %d; body = %q", path, recorder.Code, http.StatusTeapot, recorder.Body.String())
		}
	}

	if calls != 0 {
		t.Errorf("selector calls = %d, want 0", calls)
	}
	wantPaths := []string{"/api/system/select-directory/", "/api/system/select-directory-nearby"}
	if len(fallbackPaths) != len(wantPaths) {
		t.Fatalf("fallback paths = %#v, want %#v", fallbackPaths, wantPaths)
	}
	for index := range wantPaths {
		if fallbackPaths[index] != wantPaths[index] {
			t.Errorf("fallback path %d = %q, want %q", index, fallbackPaths[index], wantPaths[index])
		}
	}
}

func TestRegisterSystemRoutesPassesCanceledRequestContextToSelector(t *testing.T) {
	type contextKey struct{}
	requestContext, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "marker"))
	cancel()

	var selectorContext context.Context
	calls := 0
	systemHandler := handlers.NewSystemHandler(systemRouteSelectorFunc(func(ctx context.Context) (string, error) {
		calls++
		selectorContext = ctx
		return "", ctx.Err()
	}))
	router := newSystemRoutesRouter(t, systemHandler, nil)
	request := newSystemRouteRequest(http.MethodPost, "/api/system/select-directory").WithContext(requestContext)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d; body = %q", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	if calls != 1 {
		t.Errorf("selector calls = %d, want 1", calls)
	}
	if selectorContext != request.Context() {
		t.Fatal("selector did not receive the exact request context")
	}
	if selectorContext.Err() != context.Canceled {
		t.Errorf("selector context error = %v, want %v", selectorContext.Err(), context.Canceled)
	}
	if got := selectorContext.Value(contextKey{}); got != "marker" {
		t.Errorf("selector context value = %v, want marker", got)
	}
}

func TestMainWiresDirectoryPickerRouteBeforeServing(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	orderedSnippets := []string{
		"directoryPicker := service.NewDirectoryPicker(service.ExecCommandRunner{}, runtime.GOOS)",
		"systemHandler := handlers.NewSystemHandler(directoryPicker)",
		"router := handlers.NewRouter(noteHandler, settingsHandler, settingsService, spaHandler)",
		"registerSystemRoutes(router, systemHandler)",
		"if err := serveLocal(address, router",
	}
	remaining := string(source)
	for _, snippet := range orderedSnippets {
		index := strings.Index(remaining, snippet)
		if index < 0 {
			t.Fatalf("main.go does not contain %q in production wiring order", snippet)
		}
		remaining = remaining[index+len(snippet):]
	}
}

func newSystemRoutesRouter(t *testing.T, systemHandler *handlers.SystemHandler, fallback http.Handler) *http.ServeMux {
	t.Helper()
	if fallback == nil {
		fallback = http.NotFoundHandler()
	}
	router := handlers.NewRouter(
		handlers.NewNoteHandler(nil),
		handlers.NewSettingsHandler(nil),
		incompleteSetupState{},
		fallback,
	)
	registerSystemRoutes(router, systemHandler)
	return router
}

func newSystemRouteRequest(method, target string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.Host = "127.0.0.1"
	return request
}
