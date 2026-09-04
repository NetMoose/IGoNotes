package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
)

type gitRouteSetupState struct {
	completed bool
	calls     int
}

func (s *gitRouteSetupState) SetupCompleted() bool {
	s.calls++
	return s.completed
}

func TestGitRoutesRegisterExactMethods(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCall string
	}{
		{name: "probe", method: http.MethodPost, path: "/api/git/probe", body: `{"base":"work","git_url":"url"}`, wantCall: "probe"},
		{name: "configure", method: http.MethodPut, path: "/api/git/config?base=work", body: `{"git_url":"url","git_branch":"main"}`, wantCall: "configure"},
		{name: "disable", method: http.MethodDelete, path: "/api/git/config?base=work", wantCall: "disable"},
		{name: "status", method: http.MethodGet, path: "/api/git/status", wantCall: "status"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prober := &gitProberFake{response: model.GitProbeResponse{Base: "work", Warnings: []string{}}}
			configurer := &gitConfigurerFake{
				configureResponse: model.GitConfigResponse{Base: model.Base{Name: "work"}},
				disableResponse:   model.GitConfigResponse{Base: model.Base{Name: "work"}},
			}
			statuses := &gitStatusReaderFake{response: model.GitStatusResponse{Statuses: []model.GitStatus{}}}
			state := &gitRouteSetupState{completed: true}
			mux := http.NewServeMux()
			RegisterGitRoutes(mux, NewGitHandler(prober, configurer, statuses), state)
			recorder := httptest.NewRecorder()

			mux.ServeHTTP(recorder, newLocalRouterRequest(test.method, test.path, strings.NewReader(test.body)))

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %q", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if state.calls != 1 {
				t.Errorf("SetupCompleted calls = %d, want 1", state.calls)
			}
			calls := map[string]int{
				"probe":     prober.calls,
				"configure": configurer.configureCalls,
				"disable":   configurer.disableCalls,
				"status":    statuses.calls,
			}
			for name, count := range calls {
				want := 0
				if name == test.wantCall {
					want = 1
				}
				if count != want {
					t.Errorf("%s dependency calls = %d, want %d", name, count, want)
				}
			}
		})
	}
}

func TestGitRoutesRegisterManualSync(t *testing.T) {
	configured := &gitOperationConfigurerFake{snapshot: gitcmd.ConfiguredBase{Name: "work", Path: "/notes", URL: "https://example.test/notes.git", Branch: "main"}}
	operations := &gitOperationsFake{syncOperation: gitcmd.Operation{ID: "0123456789abcdef0123456789abcdef", State: gitcmd.OperationQueued}}
	state := &gitRouteSetupState{completed: true}
	mux := http.NewServeMux()
	RegisterGitRoutes(mux, NewGitHandlerWithOperations(&gitProberFake{}, configured, &gitStatusReaderFake{}, operations), state)
	recorder := httptest.NewRecorder()

	mux.ServeHTTP(recorder, newLocalRouterRequest(http.MethodPost, "/api/git/sync?base=work", nil))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %q", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	if operations.syncCalls != 1 || state.calls != 1 {
		t.Fatalf("sync/setup calls = %d/%d, want 1/1", operations.syncCalls, state.calls)
	}
}

func TestGitRoutesRejectUnsupportedMethodsBeforeSetup(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		allow  string
	}{
		{name: "probe", method: http.MethodGet, path: "/api/git/probe", allow: "POST"},
		{name: "config sorted", method: http.MethodPost, path: "/api/git/config", allow: "DELETE, PUT"},
		{name: "status", method: http.MethodPost, path: "/api/git/status", allow: "GET"},
		{name: "sync", method: http.MethodGet, path: "/api/git/sync", allow: "POST"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prober := &gitProberFake{}
			configurer := &gitConfigurerFake{}
			statuses := &gitStatusReaderFake{}
			state := &gitRouteSetupState{}
			mux := http.NewServeMux()
			RegisterGitRoutes(mux, NewGitHandler(prober, configurer, statuses), state)
			recorder := httptest.NewRecorder()

			mux.ServeHTTP(recorder, newLocalRouterRequest(test.method, test.path, nil))

			assertAPIErrorResponse(t, recorder, http.StatusMethodNotAllowed, model.APIError{Code: "method_not_allowed", Message: "Method not allowed"})
			if got := recorder.Header().Get("Allow"); got != test.allow {
				t.Errorf("Allow = %q, want %q", got, test.allow)
			}
			assertGitRouteDependenciesNotCalled(t, prober, configurer, statuses)
			if state.calls != 0 {
				t.Errorf("SetupCompleted calls = %d, want 0", state.calls)
			}
		})
	}
}

func TestGitRoutesRejectCrossOriginBeforeSetupAndHandlers(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/git/probe"},
		{method: http.MethodPut, path: "/api/git/config?base=work"},
		{method: http.MethodDelete, path: "/api/git/config?base=work"},
		{method: http.MethodGet, path: "/api/git/status"},
		{method: http.MethodPost, path: "/api/git/sync?base=work"},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			prober := &gitProberFake{}
			configurer := &gitConfigurerFake{}
			statuses := &gitStatusReaderFake{}
			state := &gitRouteSetupState{}
			mux := http.NewServeMux()
			RegisterGitRoutes(mux, NewGitHandler(prober, configurer, statuses), state)
			request := newLocalRouterRequest(test.method, test.path, strings.NewReader("{"))
			request.Header.Set("Origin", "https://evil.example")
			recorder := httptest.NewRecorder()

			mux.ServeHTTP(recorder, request)

			assertAPIErrorResponse(t, recorder, http.StatusForbidden, model.APIError{Code: "forbidden_origin", Message: "Forbidden request"})
			assertGitRouteDependenciesNotCalled(t, prober, configurer, statuses)
			if state.calls != 0 {
				t.Errorf("SetupCompleted calls = %d, want 0", state.calls)
			}
		})
	}
}

func TestGitRoutesRequireSetupForAllowedMethods(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/git/probe"},
		{method: http.MethodPut, path: "/api/git/config?base=work"},
		{method: http.MethodDelete, path: "/api/git/config?base=work"},
		{method: http.MethodGet, path: "/api/git/status"},
		{method: http.MethodPost, path: "/api/git/sync?base=work"},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			prober := &gitProberFake{}
			configurer := &gitConfigurerFake{}
			statuses := &gitStatusReaderFake{}
			state := &gitRouteSetupState{}
			mux := http.NewServeMux()
			RegisterGitRoutes(mux, NewGitHandler(prober, configurer, statuses), state)
			recorder := httptest.NewRecorder()

			mux.ServeHTTP(recorder, newLocalRouterRequest(test.method, test.path, strings.NewReader("{")))

			assertAPIErrorResponse(t, recorder, http.StatusPreconditionRequired, model.APIError{Code: "setup_required", Message: "setup required"})
			assertGitRouteDependenciesNotCalled(t, prober, configurer, statuses)
			if state.calls != 1 {
				t.Errorf("SetupCompleted calls = %d, want 1", state.calls)
			}
		})
	}
}

func TestGitRoutesDoNotCaptureNearOrTrailingPaths(t *testing.T) {
	prober := &gitProberFake{}
	configurer := &gitConfigurerFake{}
	statuses := &gitStatusReaderFake{}
	state := &gitRouteSetupState{completed: true}
	mux := http.NewServeMux()
	RegisterGitRoutes(mux, NewGitHandler(prober, configurer, statuses), state)

	for _, path := range []string{
		"/api/git/probe/",
		"/api/git/probes",
		"/api/git/config/",
		"/api/git/configuration",
		"/api/git/status/",
		"/api/git/statuses",
	} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()

			mux.ServeHTTP(recorder, newLocalRouterRequest(http.MethodGet, path, nil))

			if recorder.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d; body = %q", recorder.Code, http.StatusNotFound, recorder.Body.String())
			}
		})
	}
	assertGitRouteDependenciesNotCalled(t, prober, configurer, statuses)
	if state.calls != 0 {
		t.Errorf("SetupCompleted calls = %d, want 0", state.calls)
	}
}

func assertGitRouteDependenciesNotCalled(t *testing.T, prober *gitProberFake, configurer *gitConfigurerFake, statuses *gitStatusReaderFake) {
	t.Helper()
	if prober.calls != 0 || configurer.configureCalls != 0 || configurer.disableCalls != 0 || statuses.calls != 0 {
		t.Errorf(
			"dependency calls = probe %d, configure %d, disable %d, status %d; want all 0",
			prober.calls,
			configurer.configureCalls,
			configurer.disableCalls,
			statuses.calls,
		)
	}
}
