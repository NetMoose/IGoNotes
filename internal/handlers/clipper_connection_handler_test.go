package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

type clipperConnectionsStub struct {
	calls     int
	label, id string
	ctx       context.Context
	err       error
}

func (s *clipperConnectionsStub) Create(ctx context.Context, label string) (model.ClipperConnectionCreatedResponse, error) {
	s.calls++
	s.label = label
	s.ctx = ctx
	return model.ClipperConnectionCreatedResponse{Connection: model.ClipperConnection{ID: "connection-id", Label: label}, Secret: "one-time-secret"}, s.err
}
func (s *clipperConnectionsStub) List(ctx context.Context) ([]model.ClipperConnection, error) {
	s.calls++
	s.ctx = ctx
	return []model.ClipperConnection{{ID: "connection-id", Label: "Laptop"}}, s.err
}
func (s *clipperConnectionsStub) Revoke(ctx context.Context, id string) error {
	s.calls++
	s.id = id
	s.ctx = ctx
	return s.err
}

func clipperManagementRouter(s *clipperConnectionsStub, state SetupState) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterClipperConnectionRoutes(mux, NewClipperConnectionHandler(s), state)
	return mux
}

func TestClipperConnectionsMethods(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			s := &clipperConnectionsStub{}
			r := newLocalRouterRequest(method, "/api/clipper/connections?id=connection-id", strings.NewReader(`{"label":"Laptop"}`))
			w := httptest.NewRecorder()
			clipperManagementRouter(s, &gitRouteSetupState{completed: true}).ServeHTTP(w, r)
			want := map[string]int{http.MethodGet: 200, http.MethodPost: 201, http.MethodDelete: 204}[method]
			if w.Code != want || s.calls != 1 || s.ctx != r.Context() {
				t.Fatalf("status/calls/context = %d/%d/%v; body=%s", w.Code, s.calls, s.ctx, w.Body.String())
			}
			switch method {
			case http.MethodGet:
				var response model.ClipperConnectionsResponse
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Connections) != 1 {
					t.Fatalf("list: %s (%v)", w.Body.String(), err)
				}
				if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "digest") {
					t.Fatal("list leaks credentials")
				}
			case http.MethodPost:
				var response model.ClipperConnectionCreatedResponse
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Secret != "one-time-secret" || response.Connection.ID != "connection-id" || s.label != "Laptop" {
					t.Fatalf("create: %s (%v)", w.Body.String(), err)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("secret issuance must not be cached")
				}
			case http.MethodDelete:
				if s.id != "connection-id" || w.Body.Len() != 0 {
					t.Fatalf("revoke id/body = %q/%q", s.id, w.Body.String())
				}
			}
		})
	}
}

func TestClipperConnectionsRejectInvalidCreateJSON(t *testing.T) {
	for _, body := range []string{"", "{", `null`, `[]`, `{"label":"Laptop","secret":"private-input"}`, `{"label":"Laptop"} {}`, `{"label":"Laptop"} trailing`, `{"label":1}`, `{"label":"` + strings.Repeat("x", 65536) + `"}`} {
		t.Run(fmt.Sprintf("body length %d %s", len(body), body[:min(len(body), 30)]), func(t *testing.T) {
			s := &clipperConnectionsStub{}
			w := httptest.NewRecorder()
			clipperManagementRouter(s, &gitRouteSetupState{completed: true}).ServeHTTP(w, newLocalRouterRequest(http.MethodPost, "/api/clipper/connections", strings.NewReader(body)))
			if w.Code != 400 || s.calls != 0 || !strings.Contains(w.Body.String(), `"code":"clipper_invalid_request"`) {
				t.Fatalf("status/calls/body = %d/%d/%s", w.Code, s.calls, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private-input") {
				t.Fatal("echoed input")
			}
		})
	}
}

func TestClipperConnectionsSafeErrors(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, err := range []error{errors.New("private-secret diagnostic"), &model.ClipperError{Code: model.ClipperInvalidRequest}, fmt.Errorf("private-secret: %w", sql.ErrNoRows)} {
			t.Run(method+err.Error(), func(t *testing.T) {
				s := &clipperConnectionsStub{err: err}
				w := httptest.NewRecorder()
				clipperManagementRouter(s, &gitRouteSetupState{completed: true}).ServeHTTP(w, newLocalRouterRequest(method, "/api/clipper/connections?id=unknown", strings.NewReader(`{"label":"Laptop"}`)))
				want := 500
				var invalid *model.ClipperError
				if errors.As(err, &invalid) || method == http.MethodDelete && errors.Is(err, sql.ErrNoRows) {
					want = 400
				}
				if w.Code != want || strings.Contains(w.Body.String(), "private-secret") || strings.Contains(w.Body.String(), "one-time-secret") {
					t.Fatalf("status/body = %d/%s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestClipperConnectionsRequireRevokeID(t *testing.T) {
	for _, query := range []string{"", "?id=", "?id=%20", "?id=a&id=b", "?id=%zz"} {
		s := &clipperConnectionsStub{}
		w := httptest.NewRecorder()
		clipperManagementRouter(s, &gitRouteSetupState{completed: true}).ServeHTTP(w, newLocalRouterRequest(http.MethodDelete, "/api/clipper/connections"+query, nil))
		if w.Code != 400 || s.calls != 0 {
			t.Fatalf("%s: status/calls = %d/%d", query, w.Code, s.calls)
		}
	}
}

func TestClipperConnectionsGuardsAndExactPath(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodPut, http.MethodOptions, http.MethodHead} {
		for _, foreign := range []bool{false, true} {
			for _, completed := range []bool{false, true} {
				s := &clipperConnectionsStub{}
				state := &gitRouteSetupState{completed: completed}
				w := httptest.NewRecorder()
				r := newLocalRouterRequest(method, "/api/clipper/connections?id=id", strings.NewReader(`{"label":"Laptop"}`))
				r.Header.Set("Authorization", "Bearer valid-clipper-secret")
				if foreign {
					r.Header.Set("Origin", "chrome-extension://extension-id")
				}
				clipperManagementRouter(s, state).ServeHTTP(w, r)
				unsupported := method == http.MethodPut || method == http.MethodOptions || method == http.MethodHead
				want := map[string]int{http.MethodGet: 200, http.MethodPost: 201, http.MethodDelete: 204}[method]
				if !completed {
					want = 428
				}
				if unsupported {
					want = 405
				}
				if foreign {
					want = 403
				}
				if w.Code != want {
					t.Fatalf("%s foreign=%v setup=%v: %d %s", method, foreign, completed, w.Code, w.Body.String())
				}
				if foreign && state.calls != 0 {
					t.Fatal("origin must run before setup")
				}
				if unsupported && !foreign && w.Header().Get("Allow") != "DELETE, GET, POST" {
					t.Fatal("missing Allow")
				}
				if want >= 400 && s.calls != 0 {
					t.Fatal("guard bypassed")
				}
			}
		}
	}
	s := &clipperConnectionsStub{}
	for _, path := range []string{"/api/clipper/connections/", "/api/clipper/connections/child", "/api/clipper/connections-nearby", "/api/clipper/v1/info"} {
		w := httptest.NewRecorder()
		clipperManagementRouter(s, &gitRouteSetupState{completed: true}).ServeHTTP(w, newLocalRouterRequest(http.MethodGet, path, nil))
		if w.Code != 404 || s.calls != 0 {
			t.Fatalf("unexpected registration: %s %d", path, w.Code)
		}
	}
}
