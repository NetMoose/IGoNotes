package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

const testExtensionOrigin = "chrome-extension://abcdefghijklmnopabcdefghijklmnop"

type clipperAuthenticatorFunc func(context.Context, string) error

func (f clipperAuthenticatorFunc) Authenticate(ctx context.Context, token string) error {
	return f(ctx, token)
}

func TestClipperGuardHostAndOrigin(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		origins   []string
		forbidden bool
	}{
		{"extension", "localhost:9000", []string{testExtensionOrigin}, false},
		{"native", "localhost", nil, false},
		{"IPv4", "127.0.0.1:9000", nil, false},
		{"loopback range", "127.0.0.2:9000", nil, false},
		{"IPv6", "[::1]:9000", nil, false},
		{"normalized", "LOCALHOST.:09000", nil, false},
		{"remote", "example.com:9000", []string{testExtensionOrigin}, true},
		{"wildcard host", "0.0.0.0:9000", nil, true},
		{"empty host", "", nil, true},
		{"bad port", "localhost:0", nil, true},
		{"malformed host", "localhost:9000/path", nil, true},
		{"web", "localhost", []string{"https://example.com"}, true},
		{"same local web", "localhost", []string{"http://localhost"}, true},
		{"null", "localhost", []string{"null"}, true},
		{"empty origin", "localhost", []string{""}, true},
		{"multiple", "localhost", []string{testExtensionOrigin, testExtensionOrigin}, true},
		{"comma joined", "localhost", []string{testExtensionOrigin + ", " + testExtensionOrigin}, true},
		{"space joined", "localhost", []string{testExtensionOrigin + " " + testExtensionOrigin}, true},
	}
	for _, suffix := range []string{"/", "/path", ":80", "?", "?x", "#", "#x"} {
		tests = append(tests, struct {
			name      string
			host      string
			origins   []string
			forbidden bool
		}{"origin suffix " + suffix, "localhost", []string{testExtensionOrigin + suffix}, true})
	}
	for _, origin := range []string{
		"chrome-extension://user@abcdefghijklmnopabcdefghijklmnop",
		"chrome-extension://" + strings.Repeat("a", 31),
		"chrome-extension://" + strings.Repeat("a", 33),
		"chrome-extension://" + strings.Repeat("q", 32),
		"chrome-extension://" + strings.Repeat("A", 32),
		"CHROME-EXTENSION://abcdefghijklmnopabcdefghijklmnop",
		" " + testExtensionOrigin,
	} {
		tests = append(tests, struct {
			name      string
			host      string
			origins   []string
			forbidden bool
		}{origin, "localhost", []string{origin}, true})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, handled := 0, false
			auth := clipperAuthenticatorFunc(func(_ context.Context, token string) error {
				calls++
				if token != "test-token" {
					t.Fatalf("token = %q", token)
				}
				return nil
			})
			h := RequireClipper(auth, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handled = true
				w.WriteHeader(http.StatusNoContent)
			}), http.MethodGet)
			r := httptest.NewRequest(http.MethodGet, "/api/clipper/v1/info", nil)
			r.Host = tt.host
			r.Header["Origin"] = tt.origins
			r.Header.Set("Authorization", "Bearer test-token")
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			assertClipperCORS(t, w, "")
			if tt.forbidden {
				assertForbiddenOriginResponse(t, w)
				if calls != 0 || handled {
					t.Fatal("forbidden request reached auth or handler")
				}
			} else {
				if w.Code != 204 || calls != 1 || !handled {
					t.Fatalf("status=%d auth=%d handled=%v", w.Code, calls, handled)
				}
				wantOrigin := ""
				if len(tt.origins) == 1 {
					wantOrigin = tt.origins[0]
				}
				if got := w.Header().Get("Access-Control-Allow-Origin"); got != wantOrigin {
					t.Fatalf("origin = %q, want %q", got, wantOrigin)
				}
			}
		})
	}
}

func TestClipperGuardBearer(t *testing.T) {
	for _, tt := range []struct {
		name    string
		headers []string
		err     error
		want    int
		calls   int
	}{
		{"missing", nil, nil, 401, 0},
		{"empty", []string{""}, nil, 401, 0},
		{"empty token", []string{"Bearer "}, nil, 401, 0},
		{"basic", []string{"Basic test-token"}, nil, 401, 0},
		{"two values", []string{"Bearer test-token", "Bearer test-token"}, nil, 401, 0},
		{"comma", []string{"Bearer test-token, Bearer other"}, nil, 401, 0},
		{"tab", []string{"Bearer\ttest-token"}, nil, 401, 0},
		{"extra space", []string{"Bearer  test-token"}, nil, 401, 0},
		{"trailing space", []string{"Bearer test-token "}, nil, 401, 0},
		{"invalid token character", []string{"Bearer test:token"}, nil, 401, 0},
		{"revoked", []string{"Bearer revoked-token"}, fmt.Errorf("private secret: %w", &model.ClipperError{Code: model.ClipperUnauthorized}), 401, 1},
		{"operational", []string{"Bearer test-token"}, errors.New("private database failure"), 500, 1},
		{"valid", []string{"Bearer test-token"}, nil, 204, 1},
		{"scheme case", []string{"bearer test-token"}, nil, 204, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			h := RequireClipper(clipperAuthenticatorFunc(func(_ context.Context, _ string) error { calls++; return tt.err }), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), http.MethodGet)
			r := httptest.NewRequest("GET", "http://localhost/api/clipper/v1/info?token=test-token&access_token=test-token", nil)
			r.Header.Set("Origin", testExtensionOrigin)
			r.Header["Authorization"] = tt.headers
			r.AddCookie(&http.Cookie{Name: "token", Value: "test-token"})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want || calls != tt.calls {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body)
			}
			if tt.want == 401 {
				assertAPIErrorResponse(t, w, 401, model.APIError{Code: "clipper_unauthorized", Message: "Clipper authorization failed"})
			}
			if tt.want == 500 {
				assertAPIErrorResponse(t, w, 500, model.APIError{Code: "internal_error", Message: internalErrorMessage})
			}
			assertClipperCORS(t, w, testExtensionOrigin)
		})
	}
}

func TestClipperGuardPreflight(t *testing.T) {
	for _, tt := range []struct {
		name, host                string
		origins, methods, headers []string
		want                      int
	}{
		{"GET", "localhost:9000", []string{testExtensionOrigin}, []string{"GET"}, []string{"authorization, content-type"}, 204},
		{"POST", "localhost", []string{testExtensionOrigin}, []string{"POST"}, []string{"Content-Type, AUTHORIZATION"}, 204},
		{"no requested headers", "localhost", []string{testExtensionOrigin}, []string{"GET"}, nil, 204},
		{"remote", "remote.example", []string{testExtensionOrigin}, []string{"GET"}, nil, 403},
		{"web", "localhost", []string{"https://example.com"}, []string{"GET"}, nil, 403},
		{"no origin", "localhost", nil, []string{"GET"}, nil, 403},
		{"missing method", "localhost", []string{testExtensionOrigin}, nil, nil, 403},
		{"multiple methods", "localhost", []string{testExtensionOrigin}, []string{"GET", "POST"}, nil, 403},
		{"unsupported method", "localhost", []string{testExtensionOrigin}, []string{"DELETE"}, nil, 403},
		{"unsupported header", "localhost", []string{testExtensionOrigin}, []string{"POST"}, []string{"Authorization, Cookie"}, 403},
		{"empty header item", "localhost", []string{testExtensionOrigin}, []string{"POST"}, []string{"Authorization,"}, 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := RequireClipper(clipperAuthenticatorFunc(func(context.Context, string) error { t.Fatal("preflight authenticated"); return nil }), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("preflight disclosed data") }), "GET", "POST")
			r := httptest.NewRequest("OPTIONS", "/api/clipper/v1/imports", nil)
			r.Host = tt.host
			r.Header["Origin"] = tt.origins
			r.Header["Access-Control-Request-Method"] = tt.methods
			r.Header["Access-Control-Request-Headers"] = tt.headers
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if tt.want == 204 {
				if w.Body.Len() != 0 {
					t.Fatal("preflight body is not empty")
				}
				assertClipperCORS(t, w, testExtensionOrigin)
				if w.Header().Get("Access-Control-Allow-Methods") != tt.methods[0] {
					t.Fatal("preflight allows more than requested method")
				}
				wantHeaders := "Authorization, Content-Type"
				if len(tt.headers) == 0 {
					wantHeaders = ""
				}
				if w.Header().Get("Access-Control-Allow-Headers") != wantHeaders {
					t.Fatal("incorrect allowed headers")
				}
			} else {
				assertForbiddenOriginResponse(t, w)
				assertClipperCORS(t, w, "")
			}
		})
	}
}

func TestClipperGuardPreflightRequestedHeaders(t *testing.T) {
	for _, tt := range []struct {
		name      string
		requested []string
		want      string
	}{
		{"authorization only", []string{"authorization"}, "Authorization"},
		{"content type only", []string{"CONTENT-TYPE"}, "Content-Type"},
		{"both", []string{"content-type, authorization"}, "Authorization, Content-Type"},
		{"separate fields and duplicates", []string{"Content-Type", "authorization, AUTHORIZATION"}, "Authorization, Content-Type"},
		{"none", nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := RequireClipper(clipperAuthenticatorFunc(func(context.Context, string) error {
				t.Fatal("preflight authenticated")
				return nil
			}), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("preflight reached data handler")
			}), "GET")
			r := httptest.NewRequest("OPTIONS", "http://localhost/api/clipper/v1/info", nil)
			r.Header.Set("Origin", testExtensionOrigin)
			r.Header.Set("Access-Control-Request-Method", "GET")
			r.Header["Access-Control-Request-Headers"] = tt.requested
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 204 || w.Body.Len() != 0 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if got := w.Header().Get("Access-Control-Allow-Headers"); got != tt.want {
				t.Errorf("allowed headers=%q, want %q", got, tt.want)
			}
			if _, present := w.Header()["Access-Control-Allow-Headers"]; tt.want == "" && present {
				t.Error("allow-headers must be omitted when none requested")
			}
			vary := strings.Join(w.Header().Values("Vary"), ",")
			if !strings.Contains(vary, "Access-Control-Request-Headers") {
				t.Error("missing Vary Access-Control-Request-Headers")
			}
			assertClipperCORS(t, w, testExtensionOrigin)
		})
	}
}

func TestClipperGuardRouteMethods(t *testing.T) {
	for _, method := range []string{"POST", "HEAD", "DELETE"} {
		for _, preflight := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/preflight=%v", method, preflight), func(t *testing.T) {
				h := RequireClipper(clipperAuthenticatorFunc(func(context.Context, string) error { return nil }), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unsupported method reached handler") }), "GET")
				r := httptest.NewRequest(method, "http://localhost/api/clipper/v1/info", nil)
				r.Header.Set("Origin", testExtensionOrigin)
				r.Header.Set("Authorization", "Bearer test-token")
				want := 405
				if preflight {
					r.Method = "OPTIONS"
					r.Header.Set("Access-Control-Request-Method", method)
					want = 403
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("status=%d want=%d", w.Code, want)
				}
			})
		}
	}
}

func TestClipperBearerDoesNotBypassOrdinaryOriginGuard(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	router := NewRouter(NewNoteHandler(fixture.notes), fixture.handler, fixture.handler.settings, http.NotFoundHandler())
	RegisterGitRoutes(router, &GitHandler{}, fixture.handler.settings)
	for _, path := range []string{"/api/config", "/api/save", "/api/notes", "/api/git/status"} {
		for _, method := range []string{"GET", "POST", "OPTIONS"} {
			r := httptest.NewRequest(method, "http://localhost:8080"+path, nil)
			r.Header.Set("Origin", testExtensionOrigin)
			r.Header.Set("Authorization", "Bearer test-token")
			r.Header.Set("Access-Control-Request-Method", "GET")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			assertForbiddenOriginResponse(t, w)
		}
	}
}

func assertClipperCORS(t *testing.T, w *httptest.ResponseRecorder, origin string) {
	t.Helper()
	if origin != "" && w.Header().Get("Access-Control-Allow-Origin") != origin {
		t.Error("missing exact CORS origin")
	}
	if w.Header().Get("Access-Control-Allow-Origin") == "*" || w.Header().Get("Access-Control-Allow-Headers") == "*" || w.Header().Get("Access-Control-Allow-Methods") == "*" {
		t.Error("wildcard CORS")
	}
	if _, ok := w.Header()["Access-Control-Allow-Credentials"]; ok {
		t.Error("credentials CORS header present")
	}
	if !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Origin") {
		t.Error("missing Vary Origin")
	}
}
