package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

func TestRouterGuardsNoteRoutesBeforeSetup(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/notes"},
		{method: http.MethodPost, path: "/api/notes"},
		{method: http.MethodGet, path: "/api/note?id=note.md"},
		{method: http.MethodDelete, path: "/api/note?id=note.md"},
		{method: http.MethodPost, path: "/api/sync"},
		{method: http.MethodGet, path: "/api/raw?path=image.png"},
		{method: http.MethodPost, path: "/api/save"},
		{method: http.MethodPut, path: "/api/rename"},
		{method: http.MethodPost, path: "/api/assets"},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))

			assertAPIErrorResponse(t, recorder, http.StatusPreconditionRequired, model.APIError{
				Code:    "setup_required",
				Message: "setup required",
			})
		})
	}
}

func TestRouterLeavesSettingsAndInfoAvailableBeforeSetup(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		path    string
		prepare func(t *testing.T, fixture settingsHandlerFixture)
		body    func(t *testing.T) string
	}{
		{name: "get config", method: http.MethodGet, path: "/api/config"},
		{
			name:   "put config",
			method: http.MethodPut,
			path:   "/api/config",
			body: func(t *testing.T) string {
				basePath := t.TempDir()
				return marshalHandlerJSON(t, model.Config{
					BaseDir:     filepath.Dir(basePath),
					Bases:       []model.Base{{Name: "primary", Path: basePath}},
					CurrentBase: "primary",
				})
			},
		},
		{
			name:   "complete setup",
			method: http.MethodPost,
			path:   "/api/setup",
			body: func(t *testing.T) string {
				return mutationJSON(t, model.BaseMutationRequest{Mode: "connect", Name: "primary", Path: t.TempDir()})
			},
		},
		{
			name:   "add base",
			method: http.MethodPost,
			path:   "/api/bases",
			body: func(t *testing.T) string {
				return mutationJSON(t, model.BaseMutationRequest{Mode: "connect", Name: "primary", Path: t.TempDir()})
			},
		},
		{
			name:   "update base",
			method: http.MethodPut,
			path:   "/api/bases?name=primary",
			prepare: func(t *testing.T, fixture settingsHandlerFixture) {
				addRouterBase(t, fixture, "primary")
			},
			body: func(t *testing.T) string {
				return updateJSON(t, model.BaseUpdateRequest{Name: "renamed", Path: t.TempDir()})
			},
		},
		{
			name:   "forget base",
			method: http.MethodDelete,
			path:   "/api/bases?name=first",
			prepare: func(t *testing.T, fixture settingsHandlerFixture) {
				addRouterBase(t, fixture, "first")
				addRouterBase(t, fixture, "second")
			},
		},
		{
			name:   "switch base",
			method: http.MethodPost,
			path:   "/api/bases/switch",
			prepare: func(t *testing.T, fixture settingsHandlerFixture) {
				addRouterBase(t, fixture, "primary")
			},
			body: func(t *testing.T) string {
				return switchJSON(t, model.BaseSwitchRequest{Name: "primary"})
			},
		},
		{name: "info", method: http.MethodGet, path: "/api/info"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSettingsHandlerFixture(t)
			if test.prepare != nil {
				test.prepare(t, fixture)
			}
			var body string
			if test.body != nil {
				body = test.body(t)
			}
			router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, strings.NewReader(body)))

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %q", recorder.Code, http.StatusOK, recorder.Body.String())
			}
		})
	}
}

func TestRouterRejectsUnsupportedMethodsBeforeSetup(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())
	tests := []struct {
		method string
		path   string
		allow  string
	}{
		{method: http.MethodHead, path: "/api/info", allow: "GET"},
		{method: http.MethodPatch, path: "/api/notes", allow: "GET, POST"},
		{method: http.MethodPost, path: "/api/note", allow: "DELETE, GET"},
		{method: http.MethodGet, path: "/api/sync", allow: "POST"},
		{method: http.MethodPost, path: "/api/raw", allow: "GET"},
		{method: http.MethodGet, path: "/api/save", allow: "POST"},
		{method: http.MethodPost, path: "/api/rename", allow: "PUT"},
		{method: http.MethodGet, path: "/api/assets", allow: "POST"},
		{method: http.MethodOptions, path: "/api/config", allow: "GET, PUT"},
		{method: http.MethodGet, path: "/api/setup", allow: "POST"},
		{method: http.MethodGet, path: "/api/bases", allow: "DELETE, POST, PUT"},
		{method: http.MethodPut, path: "/api/bases/switch", allow: "POST"},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))

			assertAPIErrorResponse(t, recorder, http.StatusMethodNotAllowed, model.APIError{
				Code:    "method_not_allowed",
				Message: "Method not allowed",
			})
			if got := recorder.Header().Get("Allow"); got != test.allow {
				t.Errorf("Allow = %q, want %q", got, test.allow)
			}
		})
	}
}

func TestRouterUsesLiveSetupStateAndPreservesQuery(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	basePath := t.TempDir()
	router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())

	setup := httptest.NewRecorder()
	setupBody := mutationJSON(t, model.BaseMutationRequest{Mode: "connect", Name: "primary", Path: basePath})
	router.ServeHTTP(setup, httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(setupBody)))
	if setup.Code != http.StatusOK {
		t.Fatalf("setup status = %d, want %d; body = %q", setup.Code, http.StatusOK, setup.Body.String())
	}
	if err := os.WriteFile(filepath.Join(basePath, "query.md"), []byte("query content"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	note := httptest.NewRecorder()
	router.ServeHTTP(note, httptest.NewRequest(http.MethodGet, "/api/note?id=query.md&preserved=yes", nil))

	var response map[string]string
	decodeHandlerJSON(t, note, http.StatusOK, &response)
	if response["id"] != "query.md" || response["content"] != "query content" {
		t.Errorf("note response = %#v, want query.md with query content", response)
	}
}

func TestRouterFallsBackToSPAForUnmatchedPaths(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	var requests []string
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		w.WriteHeader(http.StatusTeapot)
	})
	router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, spa)
	if router == http.DefaultServeMux {
		t.Fatal("NewRouter() returned package-global DefaultServeMux")
	}

	for _, path := range []string{"/notes/view?note=one", "/api/not-registered?value=two"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusTeapot {
			t.Errorf("%s status = %d, want %d", path, recorder.Code, http.StatusTeapot)
		}
	}

	want := []string{"/notes/view?note=one", "/api/not-registered?value=two"}
	if len(requests) != len(want) {
		t.Fatalf("SPA requests = %#v, want %#v", requests, want)
	}
	for index := range want {
		if requests[index] != want[index] {
			t.Errorf("SPA request %d = %q, want %q", index, requests[index], want[index])
		}
	}
}

func addRouterBase(t *testing.T, fixture settingsHandlerFixture, name string) {
	t.Helper()
	if _, err := fixture.handler.settings.AddBase(model.BaseMutationRequest{
		Mode: "connect",
		Name: name,
		Path: t.TempDir(),
	}); err != nil {
		t.Fatalf("SettingsService.AddBase() error = %v", err)
	}
}

func TestRouterMethodErrorsAreJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	methods(map[string]http.Handler{
		http.MethodGet: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))

	var response model.APIError
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode method error: %v", err)
	}
	if response.Code != "method_not_allowed" {
		t.Errorf("error code = %q, want method_not_allowed", response.Code)
	}
}
