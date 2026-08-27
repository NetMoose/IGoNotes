package handlers

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

func TestRouterRejectsAttackerHostOnEveryAPIRoute(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	targetPath := t.TempDir()
	configBody := marshalHandlerJSON(t, model.Config{
		BaseDir:     filepath.Dir(targetPath),
		Bases:       []model.Base{{Name: "attacker", Path: targetPath}},
		CurrentBase: "attacker",
	})
	mutationBody := mutationJSON(t, model.BaseMutationRequest{Mode: "connect", Name: "attacker", Path: targetPath})
	router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())
	before := fixture.handler.settings.GetConfig()
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/info"},
		{method: http.MethodGet, path: "/api/notes"},
		{method: http.MethodPost, path: "/api/notes", body: `{"name":"created","type":"file"}`},
		{method: http.MethodGet, path: "/api/note?id=note.md"},
		{method: http.MethodDelete, path: "/api/note?id=note.md"},
		{method: http.MethodPost, path: "/api/sync"},
		{method: http.MethodGet, path: "/api/raw?path=image.png"},
		{method: http.MethodPost, path: "/api/save", body: `{"id":"note.md","content":"attacker"}`},
		{method: http.MethodPut, path: "/api/rename", body: `{"id":"note.md","new_name":"attacker.md"}`},
		{method: http.MethodPost, path: "/api/assets"},
		{method: http.MethodGet, path: "/api/config"},
		{method: http.MethodPut, path: "/api/config", body: configBody},
		{method: http.MethodPost, path: "/api/setup", body: mutationBody},
		{method: http.MethodPost, path: "/api/bases", body: mutationBody},
		{method: http.MethodPut, path: "/api/bases?name=attacker", body: `{"name":"renamed","path":"` + targetPath + `"}`},
		{method: http.MethodDelete, path: "/api/bases?name=attacker"},
		{method: http.MethodPost, path: "/api/bases/switch", body: `{"name":"attacker"}`},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Host = "attacker.example"
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertForbiddenOriginResponse(t, recorder)
		})
	}

	if after := fixture.handler.settings.GetConfig(); !reflect.DeepEqual(after, before) {
		t.Errorf("config changed after rejected requests: got %#v, want %#v", after, before)
	}
	if fixture.handler.settings.SetupCompleted() {
		t.Fatal("setup completed after rejected requests")
	}
	if got := fixture.notes.GetBasePath(); got != "" {
		t.Errorf("runtime base path = %q, want empty", got)
	}
}

func TestRouterEnforcesOriginAcrossRepresentativeEndpoints(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	completeRouterSetup(t, fixture)
	router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		contentType string
		wantStatus  int
	}{
		{name: "info GET", method: http.MethodGet, path: "/api/info", wantStatus: http.StatusOK},
		{name: "raw GET", method: http.MethodGet, path: "/api/raw", wantStatus: http.StatusBadRequest},
		{name: "config GET", method: http.MethodGet, path: "/api/config", wantStatus: http.StatusOK},
		{name: "sync POST", method: http.MethodPost, path: "/api/sync", wantStatus: http.StatusOK},
		{name: "save text plain POST", method: http.MethodPost, path: "/api/save", body: `{}`, contentType: "text/plain", wantStatus: http.StatusBadRequest},
		{name: "assets multipart POST", method: http.MethodPost, path: "/api/assets", body: "malformed", contentType: "multipart/form-data; boundary=test", wantStatus: http.StatusBadRequest},
		{name: "setup JSON POST", method: http.MethodPost, path: "/api/setup", body: `{}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name+" rejects foreign", func(t *testing.T) {
			request := newLocalRouterRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Origin", "http://attacker.example")
			request.Header.Set("Content-Type", test.contentType)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertForbiddenOriginResponse(t, recorder)
		})

		t.Run(test.name+" allows matching", func(t *testing.T) {
			request := newLocalRouterRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Origin", "http://localhost:8080")
			request.Header.Set("Content-Type", test.contentType)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Errorf("status = %d, want %d; body = %q", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("Access-Control-Allow-Origin = %q, want empty", got)
			}
		})
	}
}

func TestRouterValidatesLocalHostAndStrictOrigin(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		origin    string
		fetchSite string
		tls       bool
		forbidden bool
	}{
		{name: "localhost without origin", host: "localhost:8080"},
		{name: "localhost case and trailing dot", host: "LOCALHOST.:8080", origin: "http://localhost:8080"},
		{name: "IPv4 loopback", host: "127.0.0.1:8080", origin: "http://127.0.0.1:8080"},
		{name: "IPv6 loopback", host: "[::1]:8080", origin: "http://[::1]:8080"},
		{name: "default HTTP port", host: "localhost", origin: "http://localhost:80"},
		{name: "default HTTPS port", host: "localhost", origin: "https://localhost", tls: true},
		{name: "same origin fetch", host: "localhost:8080", origin: "http://localhost:8080", fetchSite: "same-origin"},
		{name: "native fetch", host: "localhost:8080", fetchSite: "none"},
		{name: "empty host", host: "", forbidden: true},
		{name: "attacker domain", host: "attacker.example", forbidden: true},
		{name: "non-loopback IPv4", host: "192.0.2.1:8080", forbidden: true},
		{name: "non-loopback IPv6", host: "[2001:db8::1]:8080", forbidden: true},
		{name: "IPv4 wildcard", host: "0.0.0.0:8080", forbidden: true},
		{name: "IPv6 wildcard", host: "[::]:8080", forbidden: true},
		{name: "malformed port", host: "localhost:bad", forbidden: true},
		{name: "unbracketed IPv6", host: "::1", forbidden: true},
		{name: "bracketed localhost", host: "[localhost]:8080", forbidden: true},
		{name: "bracketed IPv4", host: "[127.0.0.1]:8080", forbidden: true},
		{name: "wildcard host", host: "*", forbidden: true},
		{name: "null origin", host: "localhost:8080", origin: "null", forbidden: true},
		{name: "foreign origin", host: "localhost:8080", origin: "http://attacker.example", forbidden: true},
		{name: "different loopback name", host: "localhost:8080", origin: "http://127.0.0.1:8080", forbidden: true},
		{name: "wrong port", host: "localhost:8080", origin: "http://localhost:8081", forbidden: true},
		{name: "wrong scheme", host: "localhost:8080", origin: "https://localhost:8080", forbidden: true},
		{name: "userinfo", host: "localhost:8080", origin: "http://user@localhost:8080", forbidden: true},
		{name: "path", host: "localhost:8080", origin: "http://localhost:8080/path", forbidden: true},
		{name: "root path", host: "localhost:8080", origin: "http://localhost:8080/", forbidden: true},
		{name: "query", host: "localhost:8080", origin: "http://localhost:8080?query", forbidden: true},
		{name: "fragment", host: "localhost:8080", origin: "http://localhost:8080#fragment", forbidden: true},
		{name: "empty fragment", host: "localhost:8080", origin: "http://localhost:8080#", forbidden: true},
		{name: "cross-site fetch", host: "localhost:8080", origin: "http://localhost:8080", fetchSite: "cross-site", forbidden: true},
		{name: "cross-site fetch case insensitive", host: "localhost:8080", fetchSite: "CROSS-SITE", forbidden: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSettingsHandlerFixture(t)
			router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())
			request := httptest.NewRequest(http.MethodGet, "/api/info", nil)
			request.Host = test.host
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			if test.tls {
				request.TLS = &tls.ConnectionState{}
			}
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			if test.forbidden {
				assertForbiddenOriginResponse(t, recorder)
				return
			}
			if recorder.Code != http.StatusOK {
				t.Errorf("status = %d, want %d; body = %q", recorder.Code, http.StatusOK, recorder.Body.String())
			}
		})
	}
}

func TestRouterChecksOriginBeforeMethodAndSetup(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, "/api/notes", nil)
		request.Host = "attacker.example"

		router.ServeHTTP(recorder, request)

		assertForbiddenOriginResponse(t, recorder)
	}
}

func TestRouterRejectsHostileTextPlainMutationsWithoutSideEffects(t *testing.T) {
	t.Run("create note", func(t *testing.T) {
		fixture := newSettingsHandlerFixture(t)
		basePath := completeRouterSetup(t, fixture)
		router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())
		request := newLocalRouterRequest(http.MethodPost, "/api/notes", strings.NewReader(`{"name":"created","type":"file"}`))
		request.Header.Set("Content-Type", "text/plain")
		request.Header.Set("Origin", "http://attacker.example")
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertForbiddenOriginResponse(t, recorder)
		if _, err := os.Stat(filepath.Join(basePath, "created.md")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("rejected request created note, stat error = %v", err)
		}
	})

	t.Run("save note", func(t *testing.T) {
		fixture := newSettingsHandlerFixture(t)
		basePath := completeRouterSetup(t, fixture)
		notePath := filepath.Join(basePath, "save.md")
		if err := os.WriteFile(notePath, []byte("original"), 0o600); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}
		router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())
		request := newLocalRouterRequest(http.MethodPost, "/api/save", strings.NewReader(`{"id":"save.md","content":"attacker"}`))
		request.Header.Set("Content-Type", "text/plain")
		request.Header.Set("Origin", "http://attacker.example")
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertForbiddenOriginResponse(t, recorder)
		content, err := os.ReadFile(notePath)
		if err != nil {
			t.Fatalf("os.ReadFile() error = %v", err)
		}
		if got := string(content); got != "original" {
			t.Errorf("note content = %q, want original", got)
		}
	})

	t.Run("complete setup", func(t *testing.T) {
		fixture := newSettingsHandlerFixture(t)
		before := fixture.handler.settings.GetConfig()
		targetPath := t.TempDir()
		router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())
		body := mutationJSON(t, model.BaseMutationRequest{Mode: "connect", Name: "attacker", Path: targetPath})
		request := newLocalRouterRequest(http.MethodPost, "/api/setup", strings.NewReader(body))
		request.Header.Set("Content-Type", "text/plain")
		request.Header.Set("Origin", "http://attacker.example")
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertForbiddenOriginResponse(t, recorder)
		if after := fixture.handler.settings.GetConfig(); !reflect.DeepEqual(after, before) {
			t.Errorf("config changed: got %#v, want %#v", after, before)
		}
		if got := fixture.notes.GetBasePath(); got != "" {
			t.Errorf("runtime base path = %q, want empty", got)
		}
	})
}

func completeRouterSetup(t *testing.T, fixture settingsHandlerFixture) string {
	t.Helper()
	basePath := t.TempDir()
	if _, err := fixture.handler.settings.CompleteSetup(model.BaseMutationRequest{
		Mode: "connect",
		Name: "primary",
		Path: basePath,
	}); err != nil {
		t.Fatalf("SettingsService.CompleteSetup() error = %v", err)
	}
	return basePath
}

func assertForbiddenOriginResponse(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	assertAPIErrorResponse(t, recorder, http.StatusForbidden, model.APIError{
		Code:    "forbidden_origin",
		Message: "Forbidden request",
	})
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}
