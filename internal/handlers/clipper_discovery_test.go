package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"IGoNotes/internal/appinfo"
	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
)

type discoverySetup bool

func (s discoverySetup) SetupCompleted() bool { return bool(s) }

func discoveryRequest(router http.Handler, method, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, nil)
	r.Header.Set("Origin", "chrome-extension://"+strings.Repeat("a", 32))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodOptions {
		r.Header.Set("Access-Control-Request-Method", "GET")
		r.Header.Set("Access-Control-Request-Headers", "Authorization")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestClipperDiscoveryRouterIntegration(t *testing.T) {
	root := t.TempDir()
	active, inactive := filepath.Join(root, "active"), filepath.Join(root, "inactive")
	for _, dir := range []string{active, filepath.Join(inactive, "research", "articles"), filepath.Join(inactive, ".git", "private")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{active, inactive} {
		for _, excluded := range []string{".hidden/deep", "assets/images", "ASSETS/private"} {
			if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(excluded)), 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "private.md"), []byte("PRIVATE_NOTE_CONTENT"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, excluded := range []string{"research/.cache/deep", "research/AsSeTs/x"} {
		if err := os.MkdirAll(filepath.Join(inactive, filepath.FromSlash(excluded)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	db, err := repository.InitDB(filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	config := service.NewConfigService(filepath.Join(root, "config.json"))
	complete := true
	cfg := model.Config{CurrentBase: "active", SetupCompleted: &complete, Bases: []model.Base{
		{Name: "active", Path: active}, {Name: "inactive", Path: inactive, GitURL: "https://user:GIT_SECRET@example.invalid/repo", GitBranch: "private-branch"},
	}}
	if err := config.Save(&cfg); err != nil {
		t.Fatal(err)
	}
	coordinator := service.NewBaseOperationCoordinator()
	notes := service.NewNoteService(repository.NewNoteRepository(db), active, coordinator)
	t.Cleanup(func() { _ = notes.Close() })
	if err := notes.SyncFS(); err != nil {
		t.Fatal(err)
	}
	settings, err := service.NewSettingsService(config, notes, coordinator, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	connections := service.NewClipperConnectionService(repository.NewClipperConnectionRepository(db))
	created, err := connections.Create(t.Context(), "Discovery")
	if err != nil {
		t.Fatal(err)
	}
	destinations := service.NewClipperDestinationService(settings)
	handler := NewClipperDiscoveryHandler(destinations)
	routerFor := func(state SetupState) *http.ServeMux {
		router := NewRouter(NewNoteHandler(notes), NewSettingsHandler(settings), state, clipperTestSPA())
		RegisterClipperDiscoveryRoutes(router, handler, connections, state)
		RegisterClipperConnectionRoutes(router, NewClipperConnectionHandler(connections), state)
		return router
	}
	router := routerFor(settings)
	beforeConfig, _ := os.ReadFile(filepath.Join(root, "config.json"))
	beforeTree, err := notes.GetTree()
	if err != nil {
		t.Fatal(err)
	}
	// SQLite triggers catch even index rewrites that restore identical rows.
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		if _, err := db.Exec("CREATE TRIGGER discovery_no_" + op + " BEFORE " + op + " ON notes BEGIN SELECT RAISE(ABORT, 'discovery index write'); END"); err != nil {
			t.Fatal(err)
		}
	}
	oldVersion := appinfo.Version
	appinfo.Version = "v3.3-test-build"
	t.Cleanup(func() { appinfo.Version = oldVersion })
	for _, path := range []string{"/api/clipper/v1/info", "/api/clipper/v1/bases", "/api/clipper/v1/folders?base=inactive", "/api/clipper/v1/folders?base=active"} {
		w := discoveryRequest(router, "GET", path, created.Secret)
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		for _, private := range []string{root, "PRIVATE_NOTE_CONTENT", "GIT_SECRET", "private-branch", ".git", "private.md", created.Secret, "git_url", "current_base"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("%s leaked %q", path, private)
			}
		}
		switch {
		case strings.HasSuffix(path, "/info"):
			var got model.ClipperInfoResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.ProtocolVersion != model.ClipperProtocolVersion || got.AppName != appinfo.Name || got.AppVersion != appinfo.Version || got.Limits != model.ClipperV1Limits() || !reflect.DeepEqual(got.SupportedImageTypes, []string{model.ClipperImagePNG, model.ClipperImageJPEG, model.ClipperImageGIF, model.ClipperImageWebP}) {
				t.Fatalf("info: %+v", got)
			}
		case strings.HasSuffix(path, "/bases"):
			if strings.TrimSpace(w.Body.String()) != `{"bases":["active","inactive"]}` {
				t.Fatal(w.Body.String())
			}
		default:
			var got model.ClipperFoldersResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := []string{""}
			if got.Base == "inactive" {
				want = append(want, "research", "research/articles")
			}
			if !reflect.DeepEqual(got.Folders, want) || got.DestinationRevision == "" {
				t.Fatalf("folders: %+v", got)
			}
		}
	}
	afterTree, err := notes.GetTree()
	if err != nil {
		t.Fatal(err)
	}
	afterConfig, _ := os.ReadFile(filepath.Join(root, "config.json"))
	if !reflect.DeepEqual(beforeTree, afterTree) || string(beforeConfig) != string(afterConfig) || notes.GetBasePath() != active || settings.GetConfig().CurrentBase != "active" {
		t.Fatal("discovery changed editor/config/index")
	}
	for _, endpoint := range []string{"info", "bases", "folders"} {
		path := "/api/clipper/v1/" + endpoint
		for _, state := range []discoverySetup{false, true} {
			router := routerFor(state)
			w := discoveryRequest(router, "GET", path, "")
			assertDiscoveryError(t, w, 401, "clipper_unauthorized")
			w = discoveryRequest(router, "OPTIONS", path, "")
			if w.Code != 204 {
				t.Fatalf("preflight: %d %s", w.Code, w.Body.String())
			}
			if !state {
				assertDiscoveryError(t, discoveryRequest(router, "GET", path, created.Secret), 428, "setup_required")
			}
		}
		w := discoveryRequest(router, "POST", path, created.Secret)
		assertDiscoveryError(t, w, 405, "method_not_allowed")
		if w.Header().Get("Allow") != "GET, OPTIONS" {
			t.Fatal(w.Header())
		}
		for _, suffix := range []string{"/", "/child"} {
			if w := discoveryRequest(router, "GET", path+suffix, created.Secret); w.Code != 404 {
				t.Fatalf("non-exact path %s: %d", path+suffix, w.Code)
			}
		}
	}
	for _, query := range []string{"", "?base=", "?base=+++", "?base=active&base=inactive", "?base=%zz", "?base=active;other=x", "?base=" + strings.Repeat("x", 1025), "?base=active&x=" + strings.Repeat("x", 8192)} {
		assertDiscoveryError(t, discoveryRequest(router, "GET", "/api/clipper/v1/folders"+query, created.Secret), 400, "clipper_invalid_request")
	}
	assertDiscoveryError(t, discoveryRequest(router, "GET", "/api/clipper/v1/folders?base=unknown", created.Secret), 404, "clipper_destination_not_found")
	if err := os.RemoveAll(inactive); err != nil {
		t.Fatal(err)
	}
	assertDiscoveryError(t, discoveryRequest(router, "GET", "/api/clipper/v1/folders?base=inactive", created.Secret), 409, "clipper_destination_changed")
	if w := discoveryRequest(router, "GET", "/api/clipper/connections", created.Secret); w.Code != 403 {
		t.Fatalf("Bearer management: %d", w.Code)
	}
	if err := connections.Revoke(t.Context(), created.Connection.ID); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"info", "bases", "folders?base=inactive"} {
		assertDiscoveryError(t, discoveryRequest(router, "GET", "/api/clipper/v1/"+endpoint, created.Secret), 401, "clipper_unauthorized")
	}
}

func clipperTestSPA() http.Handler {
	return NewSPAHandler(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>CLIPPER_TEST_APP</html>")}})
}

func TestClipperNamespaceFallbackWithProductionSPA(t *testing.T) {
	db, err := repository.InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	connections := service.NewClipperConnectionService(repository.NewClipperConnectionRepository(db))
	created, err := connections.Create(t.Context(), "Fallback routing")
	if err != nil {
		t.Fatal(err)
	}
	for _, setup := range []discoverySetup{false, true} {
		for _, managementFirst := range []bool{false, true} {
			router := NewRouter(NewNoteHandler(nil), NewSettingsHandler(nil), setup, clipperTestSPA())
			management := func() { RegisterClipperConnectionRoutes(router, NewClipperConnectionHandler(connections), setup) }
			if managementFirst {
				management()
			}
			RegisterClipperDiscoveryRoutes(router, NewClipperDiscoveryHandler(discoveryDestinationsStub{}), connections, setup)
			if !managementFirst {
				management()
			}
			// A later exact import route must take precedence over both fallbacks.
			router.Handle("/api/clipper/v1/imports", RequireClipper(connections, RequireSetup(setup, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusAccepted)
			})), http.MethodGet))
			for _, path := range []string{"/api/clipper/v1", "/api/clipper/v1/", "/api/clipper/v1/info/", "/api/clipper/v1/info/child", "/api/clipper/v1/bases/", "/api/clipper/v1/folders/child", "/api/clipper/v1/unknown"} {
				assertDiscoveryError(t, discoveryRequest(router, "GET", path, ""), 401, "clipper_unauthorized")
				w := discoveryRequest(router, "GET", path, created.Secret)
				want, code := 428, "setup_required"
				if setup {
					want, code = 404, "not_found"
				}
				assertDiscoveryError(t, w, want, code)
				if w := discoveryRequest(router, "OPTIONS", path, ""); w.Code != 204 {
					t.Fatalf("preflight %s: %d %s", path, w.Code, w.Body.String())
				}
				r := httptest.NewRequest("GET", "http://127.0.0.1:8080"+path, nil)
				r.Header.Set("Origin", "https://example.com")
				r.Header.Set("Authorization", "Bearer "+created.Secret)
				w = httptest.NewRecorder()
				router.ServeHTTP(w, r)
				assertDiscoveryError(t, w, 403, "forbidden_origin")
			}
			for _, path := range []string{"/api/clipper", "/api/clipper/", "/api/clipper/connections/", "/api/clipper/connections/child", "/api/clipper/unknown"} {
				assertDiscoveryError(t, discoveryRequest(router, "GET", path, created.Secret), 403, "forbidden_origin")
				r := httptest.NewRequest("GET", "http://127.0.0.1:8080"+path, nil)
				r.Header.Set("Origin", "http://127.0.0.1:8080")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				want, code := 428, "setup_required"
				if setup {
					want, code = 404, "not_found"
				}
				assertDiscoveryError(t, w, want, code)
			}
			for _, path := range []string{"/api/clipper/v1/info", "/api/clipper/v1/imports"} {
				w := discoveryRequest(router, "GET", path, created.Secret)
				want := 428
				if setup {
					want = 200
					if strings.HasSuffix(path, "/imports") {
						want = 202
					}
				}
				if w.Code != want {
					t.Fatalf("exact %s: %d %s", path, w.Code, w.Body.String())
				}
			}
			// Preserve the SPA fallback outside the Clipper namespace.
			for _, path := range []string{"/settings", "/api/preexisting-unknown"} {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8080"+path, nil))
				if w.Code != 200 || !strings.Contains(w.Body.String(), "CLIPPER_TEST_APP") {
					t.Fatalf("SPA %s: %d %s", path, w.Code, w.Body.String())
				}
			}
		}
	}
}

func assertDiscoveryError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != status || w.Header().Get("Content-Type") != "application/json" || string(got["code"]) != `"`+code+`"` || len(got["message"]) == 0 {
		t.Fatalf("error: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	for key := range got {
		if key != "code" && key != "message" && key != "field" {
			t.Fatalf("unexpected error field %s", key)
		}
	}
}

type discoveryDestinationsStub struct{ err error }

func (s discoveryDestinationsStub) ListBases() []string { return nil }
func (s discoveryDestinationsStub) Folders(name string) (model.ClipperFoldersResponse, error) {
	return model.ClipperFoldersResponse{Base: name, DestinationRevision: "opaque"}, s.err
}

func TestClipperDiscoveryEmptyArraysAndPrivateIOErrors(t *testing.T) {
	h := NewClipperDiscoveryHandler(discoveryDestinationsStub{})
	for _, tc := range []struct {
		call       http.HandlerFunc
		path, want string
	}{
		{h.Bases, "/bases", `{"bases":[]}`},
		{h.Folders, "/folders?base=empty", `{"base":"empty","folders":[],"destination_revision":"opaque"}`},
	} {
		w := httptest.NewRecorder()
		tc.call(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != tc.want {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	for _, err := range []error{
		&os.PathError{Op: "open", Path: "/PRIVATE/root/note.md", Err: os.ErrPermission},
		fmt.Errorf("/PRIVATE/root: %w", &model.ClipperError{Code: model.ClipperDestinationChanged}),
	} {
		h := NewClipperDiscoveryHandler(discoveryDestinationsStub{err: err})
		w := httptest.NewRecorder()
		h.Folders(w, httptest.NewRequest("GET", "/folders?base=empty", nil))
		if w.Code != 500 && w.Code != 409 {
			t.Fatalf("unsafe status %d", w.Code)
		}
		if strings.Contains(w.Body.String(), "PRIVATE") || strings.Contains(w.Body.String(), "permission") || strings.Contains(w.Body.String(), "opaque") {
			t.Fatal(w.Body.String())
		}
	}
}
