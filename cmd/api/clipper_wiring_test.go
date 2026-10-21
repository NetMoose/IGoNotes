package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"IGoNotes/internal/handlers"
	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
)

type clipperWiringSetupState bool

func (s clipperWiringSetupState) SetupCompleted() bool { return bool(s) }

func TestClipperManagementWiringCredentialLifecycle(t *testing.T) {
	db, err := repository.InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	connections := service.NewClipperConnectionService(repository.NewClipperConnectionRepository(db))
	state := clipperWiringSetupState(true)
	router := handlers.NewRouter(handlers.NewNoteHandler(nil), handlers.NewSettingsHandler(nil), state, http.NotFoundHandler())
	handlers.RegisterClipperConnectionRoutes(router, handlers.NewClipperConnectionHandler(connections), state)
	request := func(method, path, body, origin, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	const path = "/api/clipper/connections"
	const origin = "http://127.0.0.1:8080"
	w := request(http.MethodGet, path, "", origin, "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"connections":[]}` {
		t.Fatalf("empty list: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodPost, path, `{"label":"Laptop"}`, origin, "")
	var created model.ClipperConnectionCreatedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != 201 || created.Secret == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("issuance status=%d, decode error=%v", w.Code, err)
	}
	if err := connections.Authenticate(context.Background(), created.Secret); err != nil {
		t.Fatal("issued credential did not authenticate")
	}
	// A real, active credential cannot bypass the local UI origin guard.
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		w = request(method, path+"?id="+created.Connection.ID, `{"label":"Another"}`, "chrome-extension://extension-id", created.Secret)
		if w.Code != 403 {
			t.Fatalf("extension management %s status=%d", method, w.Code)
		}
	}
	w = request(http.MethodGet, path, "", origin, "")
	var listed model.ClipperConnectionsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || w.Code != 200 || len(listed.Connections) != 1 || strings.Contains(w.Body.String(), created.Secret) || strings.Contains(w.Body.String(), "digest") || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("metadata response status=%d, decode error=%v", w.Code, err)
	}
	for range 2 {
		w = request(http.MethodDelete, path+"?id="+created.Connection.ID, "", origin, "")
		if w.Code != 204 || w.Body.Len() != 0 {
			t.Fatalf("revoke status=%d", w.Code)
		}
	}
	if err := connections.Authenticate(context.Background(), created.Secret); err == nil {
		t.Fatal("revoked credential still authenticates")
	}
	w = request(http.MethodDelete, path+"?id=unknown", "", origin, "")
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"clipper_invalid_request"`) {
		t.Fatalf("unknown revoke: %d %s", w.Code, w.Body.String())
	}
}

func TestRunServerWiresClipperManagementAfterMigrationBeforeServing(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	remaining := string(source)
	for _, snippet := range []string{
		"repository.InitDB(dbPath)",
		"clipperConnectionRepo := repository.NewClipperConnectionRepository(db)",
		"clipperConnectionService := service.NewClipperConnectionService(clipperConnectionRepo)",
		"clipperConnectionHandler := handlers.NewClipperConnectionHandler(clipperConnectionService)",
		"handlers.RegisterClipperConnectionRoutes(router, clipperConnectionHandler, settingsService)",
		"return serveLocal(ctx, address, newHTTPServer(router)",
	} {
		index := strings.Index(remaining, snippet)
		if index < 0 {
			t.Fatalf("missing production wiring in order: %q", snippet)
		}
		remaining = remaining[index+len(snippet):]
	}
}

func TestRunServerWiresClipperDiscoveryBeforeServing(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	remaining := string(source)
	for _, snippet := range []string{
		"service.NewSettingsServiceWithGit(",
		"clipperDestinationService := service.NewClipperDestinationService(settingsService)",
		"clipperDiscoveryHandler := handlers.NewClipperDiscoveryHandler(clipperDestinationService)",
		"handlers.RegisterClipperDiscoveryRoutes(router, clipperDiscoveryHandler, clipperConnectionService, settingsService)",
		"return serveLocal(ctx, address, newHTTPServer(router)",
	} {
		index := strings.Index(remaining, snippet)
		if index < 0 {
			t.Fatalf("missing production discovery wiring in order: %q", snippet)
		}
		remaining = remaining[index+len(snippet):]
	}
}

func TestClipperDiscoveryWiringBearerAndSetup(t *testing.T) {
	db, err := repository.InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	connections := service.NewClipperConnectionService(repository.NewClipperConnectionRepository(db))
	created, err := connections.Create(t.Context(), "Wiring")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []clipperWiringSetupState{false, true} {
		router := handlers.NewRouter(handlers.NewNoteHandler(nil), handlers.NewSettingsHandler(nil), state, http.NotFoundHandler())
		handlers.RegisterClipperDiscoveryRoutes(router, handlers.NewClipperDiscoveryHandler(nil), connections, state)
		for _, token := range []string{"", created.Secret} {
			r := httptest.NewRequest("GET", "http://127.0.0.1:8080/api/clipper/v1/info", nil)
			r.Header.Set("Origin", "chrome-extension://"+strings.Repeat("a", 32))
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			want := 401
			if token != "" {
				want = 428
				if state {
					want = 200
				}
			}
			if w.Code != want {
				t.Fatalf("state=%v authenticated=%v: %d %s", state, token != "", w.Code, w.Body.String())
			}
		}
	}
}
