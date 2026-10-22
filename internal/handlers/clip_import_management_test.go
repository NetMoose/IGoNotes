package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

type clipRecoveryStub struct {
	calls int
	err   error
	ctx   context.Context
}

func (s *clipRecoveryStub) ListUnfinished(ctx context.Context) ([]model.ClipperImportSummary, error) {
	s.calls++
	s.ctx = ctx
	return []model.ClipperImportSummary{{ImportID: managementImportID, Base: "notes", Path: "folder/note.md", State: model.ClipperImportRecoveryRequired}}, s.err
}
func (s *clipRecoveryStub) Abandon(ctx context.Context, id string) (model.ClipperImportStatusResponse, error) {
	s.calls++
	s.ctx = ctx
	return model.ClipperImportStatusResponse{ImportID: id, State: model.ClipperImportRejected, Error: &model.APIError{Code: "clipper_import_abandoned", Message: "Clipper import abandoned; files may already exist"}}, s.err
}

const managementImportID = "12345678-1234-4123-8123-123456789abc"

func recoveryRouter(s *clipRecoveryStub, complete bool) *http.ServeMux {
	mux := http.NewServeMux()
	state := &gitRouteSetupState{completed: complete}
	RegisterClipperConnectionRoutes(mux, NewClipperConnectionHandler(&clipperConnectionsStub{}), state)
	RegisterClipImportManagementRoutes(mux, NewClipImportManagementHandler(s), state)
	return mux
}
func TestClipRecoveryManagementRoutes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         int
		calls        int
	}{
		{"GET", "/api/clipper/imports", 200, 1},
		{"POST", "/api/clipper/imports/abandon?id=" + managementImportID, 200, 1},
		{"POST", "/api/clipper/imports", 405, 0}, {"GET", "/api/clipper/imports/abandon", 405, 0},
		{"OPTIONS", "/api/clipper/imports", 405, 0}, {"DELETE", "/api/clipper/imports/abandon", 405, 0},
		{"GET", "/api/clipper/imports/unknown", 404, 0},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			s := &clipRecoveryStub{}
			r := newLocalRouterRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			recoveryRouter(s, true).ServeHTTP(w, r)
			if w.Code != tc.want || s.calls != tc.calls {
				t.Fatalf("%d %d %s", w.Code, s.calls, w.Body.String())
			}
			if s.calls > 0 && (s.ctx != r.Context() || w.Header().Get("Cache-Control") != "no-store") {
				t.Fatal("context/cache")
			}
			if tc.want == 200 && !strings.Contains(w.Body.String(), managementImportID) {
				t.Fatal(w.Body.String())
			}
		})
	}
}
func TestClipRecoveryManagementValidation(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/clipper/imports?extra=1", ""}, {"GET", "/api/clipper/imports", "{}"},
		{"POST", "/api/clipper/imports/abandon", ""}, {"POST", "/api/clipper/imports/abandon?id=bad", ""},
		{"POST", "/api/clipper/imports/abandon?id=" + managementImportID + "&id=" + managementImportID, ""},
		{"POST", "/api/clipper/imports/abandon?id=" + managementImportID + "&extra=1", ""},
		{"POST", "/api/clipper/imports/abandon?id=%zz", ""},
		{"POST", "/api/clipper/imports/abandon?id=" + managementImportID, "{}"},
		{"POST", "/api/clipper/imports/abandon?id=" + managementImportID, " "},
	} {
		s := &clipRecoveryStub{}
		w := httptest.NewRecorder()
		recoveryRouter(s, true).ServeHTTP(w, newLocalRouterRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != 400 || s.calls != 0 {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
}
func TestClipRecoveryManagementGuardsAndSafeErrors(t *testing.T) {
	for _, path := range []string{"/api/clipper/imports", "/api/clipper/imports/abandon?id=" + managementImportID} {
		for _, method := range []string{"GET", "POST", "DELETE", "OPTIONS"} {
			for _, origin := range []string{"chrome-extension://abcdefghijklmnopabcdefghijklmnop", "null", "https://evil.test", ""} {
				s := &clipRecoveryStub{}
				w := httptest.NewRecorder()
				r := newLocalRouterRequest(method, path, nil)
				if origin != "" {
					r.Header.Set("Origin", origin)
				} else {
					r.Header.Del("Origin")
				}
				r.Header.Set("Authorization", "Bearer valid-extension-token")
				recoveryRouter(s, false).ServeHTTP(w, r)
				if w.Code != 403 || s.calls != 0 {
					t.Fatalf("guard %s %s %q: %d %s", method, path, origin, w.Code, w.Body.String())
				}
			}
		}
		s := &clipRecoveryStub{}
		w := httptest.NewRecorder()
		method := "GET"
		if strings.Contains(path, "abandon") {
			method = "POST"
		}
		recoveryRouter(s, false).ServeHTTP(w, newLocalRouterRequest(method, path, nil))
		if w.Code != 428 || s.calls != 0 {
			t.Fatal("setup", w.Code)
		}
		for _, err := range []error{errors.New("/private/journal secret"), sql.ErrNoRows, &model.ClipperError{Code: model.ClipperImportNotAbandonable}} {
			s := &clipRecoveryStub{err: err}
			w := httptest.NewRecorder()
			recoveryRouter(s, true).ServeHTTP(w, newLocalRouterRequest(method, path, nil))
			want := 500
			if errors.Is(err, sql.ErrNoRows) {
				want = 400
			}
			if _, ok := err.(*model.ClipperError); ok {
				want = 409
			}
			if w.Code != want || strings.Contains(w.Body.String(), "/private") {
				t.Fatalf("error %d %s", w.Code, w.Body.String())
			}
		}
	}
}
