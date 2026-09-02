package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

type gitProberFake struct {
	calls    int
	ctx      context.Context
	request  model.GitProbeRequest
	response model.GitProbeResponse
	err      error
}

func (f *gitProberFake) Probe(ctx context.Context, request model.GitProbeRequest) (model.GitProbeResponse, error) {
	f.calls++
	f.ctx = ctx
	f.request = request
	return f.response, f.err
}

type gitConfigurerFake struct {
	configureCalls    int
	configureContext  context.Context
	configureBase     string
	configureRequest  model.GitConfigRequest
	configureResponse model.GitConfigResponse
	configureErr      error
	disableCalls      int
	disableContext    context.Context
	disableBase       string
	disableResponse   model.GitConfigResponse
	disableErr        error
}

func (f *gitConfigurerFake) ConfigureGit(ctx context.Context, base string, request model.GitConfigRequest) (model.GitConfigResponse, error) {
	f.configureCalls++
	f.configureContext = ctx
	f.configureBase = base
	f.configureRequest = request
	return f.configureResponse, f.configureErr
}

func (f *gitConfigurerFake) DisableGit(ctx context.Context, base string) (model.GitConfigResponse, error) {
	f.disableCalls++
	f.disableContext = ctx
	f.disableBase = base
	return f.disableResponse, f.disableErr
}

type gitStatusReaderFake struct {
	calls    int
	ctx      context.Context
	base     string
	response model.GitStatusResponse
	err      error
}

func (f *gitStatusReaderFake) Status(ctx context.Context, base string) (model.GitStatusResponse, error) {
	f.calls++
	f.ctx = ctx
	f.base = base
	return f.response, f.err
}

func TestGitHandlerProbeAcceptsBranchlessRequestAndForwardsContext(t *testing.T) {
	response := model.GitProbeResponse{
		Base:         "work",
		GitVersion:   "2.51.0",
		CanConfigure: true,
		Warnings:     []string{},
	}
	prober := &gitProberFake{response: response}
	handler := NewGitHandler(prober, &gitConfigurerFake{}, &gitStatusReaderFake{})
	body := &closeTrackingBody{Reader: strings.NewReader(`{"base":"work","git_url":"https://example.com/notes.git"}`)}
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("request"), "probe")
	request := httptest.NewRequest(http.MethodPost, "/api/git/probe", body).WithContext(ctx)
	recorder := httptest.NewRecorder()

	handler.Probe(recorder, request)

	if prober.calls != 1 {
		t.Fatalf("Probe calls = %d, want 1", prober.calls)
	}
	if prober.ctx != request.Context() {
		t.Fatal("Probe context was not forwarded unchanged")
	}
	wantRequest := model.GitProbeRequest{
		Base:   "work",
		GitURL: "https://example.com/notes.git",
	}
	if !reflect.DeepEqual(prober.request, wantRequest) {
		t.Errorf("Probe request = %#v, want %#v", prober.request, wantRequest)
	}
	if !body.closed {
		t.Fatal("request body was not closed")
	}
	var got model.GitProbeResponse
	decodeHandlerJSON(t, recorder, http.StatusOK, &got)
	if !reflect.DeepEqual(got, response) {
		t.Errorf("response = %#v, want %#v", got, response)
	}
}

func TestGitHandlerBodyEndpointsRejectInvalidJSONAndCloseBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		call func(*GitHandler, http.ResponseWriter, *http.Request)
		path string
	}{
		{name: "probe empty", body: "", call: (*GitHandler).Probe, path: "/api/git/probe"},
		{name: "probe malformed", body: "{", call: (*GitHandler).Probe, path: "/api/git/probe"},
		{name: "probe multiple", body: `{"base":"work","git_url":"url"} {}`, call: (*GitHandler).Probe, path: "/api/git/probe"},
		{name: "configure empty", body: "", call: (*GitHandler).Configure, path: "/api/git/config?base=work"},
		{name: "configure malformed", body: "{", call: (*GitHandler).Configure, path: "/api/git/config?base=work"},
		{name: "configure multiple", body: `{"git_url":"url","git_branch":"main"} {}`, call: (*GitHandler).Configure, path: "/api/git/config?base=work"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prober := &gitProberFake{}
			configurer := &gitConfigurerFake{}
			handler := NewGitHandler(prober, configurer, &gitStatusReaderFake{})
			body := &closeTrackingBody{Reader: strings.NewReader(test.body)}
			request := httptest.NewRequest(http.MethodPost, test.path, body)
			recorder := httptest.NewRecorder()

			test.call(handler, recorder, request)

			assertAPIErrorResponse(t, recorder, http.StatusBadRequest, model.APIError{
				Code:    "bad_json",
				Message: "Invalid JSON",
			})
			if prober.calls != 0 || configurer.configureCalls != 0 {
				t.Fatalf("service calls = probe %d, configure %d; want 0", prober.calls, configurer.configureCalls)
			}
			if !body.closed {
				t.Fatal("request body was not closed")
			}
		})
	}
}

func TestGitHandlerProbeRequiresFieldsInOrder(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{name: "base", body: `{}`, field: "base"},
		{name: "git URL", body: `{"base":"work"}`, field: "git_url"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prober := &gitProberFake{}
			handler := NewGitHandler(prober, &gitConfigurerFake{}, &gitStatusReaderFake{})
			recorder := httptest.NewRecorder()

			handler.Probe(recorder, httptest.NewRequest(http.MethodPost, "/api/git/probe", strings.NewReader(test.body)))

			assertMissingField(t, recorder, test.field)
			if prober.calls != 0 {
				t.Fatalf("Probe calls = %d, want 0", prober.calls)
			}
		})
	}
}

func TestGitHandlerConfigureValidatesQueryBeforeBody(t *testing.T) {
	tests := []struct {
		name string
		path string
		want model.APIError
	}{
		{name: "missing", path: "/api/git/config", want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "base"}},
		{name: "empty", path: "/api/git/config?base=", want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "base"}},
		{name: "duplicate", path: "/api/git/config?base=one&base=two", want: model.APIError{Code: "bad_query", Message: "Invalid query parameters"}},
		{name: "unknown", path: "/api/git/config?base=work&extra=value", want: model.APIError{Code: "bad_query", Message: "Invalid query parameters"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configurer := &gitConfigurerFake{}
			handler := NewGitHandler(&gitProberFake{}, configurer, &gitStatusReaderFake{})
			body := &closeTrackingBody{Reader: strings.NewReader("{")}
			recorder := httptest.NewRecorder()

			handler.Configure(recorder, httptest.NewRequest(http.MethodPut, test.path, body))

			assertAPIErrorResponse(t, recorder, http.StatusBadRequest, test.want)
			if configurer.configureCalls != 0 {
				t.Fatalf("ConfigureGit calls = %d, want 0", configurer.configureCalls)
			}
			if !body.closed {
				t.Fatal("request body was not closed")
			}
		})
	}
}

func TestGitHandlerConfigureRequiresBodyFieldsAndAllowsEmptyTemplate(t *testing.T) {
	t.Run("missing fields", func(t *testing.T) {
		tests := []struct {
			name  string
			body  string
			field string
		}{
			{name: "git URL", body: `{}`, field: "git_url"},
			{name: "git branch", body: `{"git_url":"https://example.com/notes.git"}`, field: "git_branch"},
			{name: "empty git branch", body: `{"git_url":"https://example.com/notes.git","git_branch":""}`, field: "git_branch"},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				configurer := &gitConfigurerFake{}
				handler := NewGitHandler(&gitProberFake{}, configurer, &gitStatusReaderFake{})
				recorder := httptest.NewRecorder()

				handler.Configure(recorder, httptest.NewRequest(http.MethodPut, "/api/git/config?base=work", strings.NewReader(test.body)))

				assertMissingField(t, recorder, test.field)
				if configurer.configureCalls != 0 {
					t.Fatalf("ConfigureGit calls = %d, want 0", configurer.configureCalls)
				}
			})
		}
	})

	t.Run("success", func(t *testing.T) {
		response := model.GitConfigResponse{
			Base: model.Base{Name: "work"},
			Status: model.GitStatus{
				Base:         "work",
				State:        model.GitStateReady,
				ChangedPaths: []string{},
			},
		}
		configurer := &gitConfigurerFake{configureResponse: response}
		handler := NewGitHandler(&gitProberFake{}, configurer, &gitStatusReaderFake{})
		requestBody := model.GitConfigRequest{
			GitURL:                  "https://example.com/notes.git",
			GitBranch:               "main",
			AutoSync:                true,
			AutoSyncIntervalMinutes: 15,
			Confirmations: model.GitConfirmations{
				CreateRepository: true,
			},
		}
		type contextKey string
		ctx := context.WithValue(context.Background(), contextKey("request"), "configure")
		request := httptest.NewRequest(http.MethodPut, "/api/git/config?base=work", strings.NewReader(marshalHandlerJSON(t, requestBody))).WithContext(ctx)
		recorder := httptest.NewRecorder()

		handler.Configure(recorder, request)

		if configurer.configureCalls != 1 {
			t.Fatalf("ConfigureGit calls = %d, want 1", configurer.configureCalls)
		}
		if configurer.configureContext != request.Context() {
			t.Fatal("ConfigureGit context was not forwarded unchanged")
		}
		if configurer.configureBase != "work" || !reflect.DeepEqual(configurer.configureRequest, requestBody) {
			t.Errorf("ConfigureGit arguments = %q, %#v; want work, %#v", configurer.configureBase, configurer.configureRequest, requestBody)
		}
		if configurer.configureRequest.GitCommitMessageTemplate != "" {
			t.Errorf("commit template = %q, want empty", configurer.configureRequest.GitCommitMessageTemplate)
		}
		var got model.GitConfigResponse
		decodeHandlerJSON(t, recorder, http.StatusOK, &got)
		if !reflect.DeepEqual(got, response) {
			t.Errorf("response = %#v, want %#v", got, response)
		}
	})
}

func TestGitHandlerDisableRequiresExactBaseQueryAndForwardsContext(t *testing.T) {
	tests := []struct {
		name string
		path string
		want model.APIError
	}{
		{name: "missing", path: "/api/git/config", want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "base"}},
		{name: "empty", path: "/api/git/config?base=", want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "base"}},
		{name: "duplicate", path: "/api/git/config?base=one&base=two", want: model.APIError{Code: "bad_query", Message: "Invalid query parameters"}},
		{name: "unknown", path: "/api/git/config?base=work&extra=value", want: model.APIError{Code: "bad_query", Message: "Invalid query parameters"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configurer := &gitConfigurerFake{}
			handler := NewGitHandler(&gitProberFake{}, configurer, &gitStatusReaderFake{})
			recorder := httptest.NewRecorder()

			handler.Disable(recorder, httptest.NewRequest(http.MethodDelete, test.path, nil))

			assertAPIErrorResponse(t, recorder, http.StatusBadRequest, test.want)
			if configurer.disableCalls != 0 {
				t.Fatalf("DisableGit calls = %d, want 0", configurer.disableCalls)
			}
		})
	}

	t.Run("success", func(t *testing.T) {
		response := model.GitConfigResponse{Base: model.Base{Name: "work"}}
		configurer := &gitConfigurerFake{disableResponse: response}
		handler := NewGitHandler(&gitProberFake{}, configurer, &gitStatusReaderFake{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		request := httptest.NewRequest(http.MethodDelete, "/api/git/config?base=work", nil).WithContext(ctx)
		recorder := httptest.NewRecorder()

		handler.Disable(recorder, request)

		if configurer.disableCalls != 1 || configurer.disableBase != "work" {
			t.Fatalf("DisableGit calls/base = %d/%q, want 1/work", configurer.disableCalls, configurer.disableBase)
		}
		if configurer.disableContext != request.Context() {
			t.Fatal("DisableGit context was not forwarded unchanged")
		}
		var got model.GitConfigResponse
		decodeHandlerJSON(t, recorder, http.StatusOK, &got)
		if !reflect.DeepEqual(got, response) {
			t.Errorf("response = %#v, want %#v", got, response)
		}
	})
}

func TestGitHandlerStatusAcceptsOptionalExactBaseFilter(t *testing.T) {
	tests := []struct {
		name string
		path string
		base string
	}{
		{name: "all omitted", path: "/api/git/status", base: ""},
		{name: "all explicit", path: "/api/git/status?base=", base: ""},
		{name: "one base", path: "/api/git/status?base=work", base: "work"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := model.GitStatusResponse{Statuses: []model.GitStatus{{Base: test.base, State: model.GitStateReady, ChangedPaths: []string{}}}}
			statuses := &gitStatusReaderFake{response: response}
			handler := NewGitHandler(&gitProberFake{}, &gitConfigurerFake{}, statuses)
			type contextKey string
			ctx := context.WithValue(context.Background(), contextKey("request"), test.name)
			request := httptest.NewRequest(http.MethodGet, test.path, strings.NewReader("ignored")).WithContext(ctx)
			recorder := httptest.NewRecorder()

			handler.Status(recorder, request)

			if statuses.calls != 1 || statuses.base != test.base {
				t.Fatalf("Status calls/base = %d/%q, want 1/%q", statuses.calls, statuses.base, test.base)
			}
			if statuses.ctx != request.Context() {
				t.Fatal("Status context was not forwarded unchanged")
			}
			var got model.GitStatusResponse
			decodeHandlerJSON(t, recorder, http.StatusOK, &got)
			if !reflect.DeepEqual(got, response) {
				t.Errorf("response = %#v, want %#v", got, response)
			}
		})
	}
}

func TestGitHandlerStatusRejectsDuplicateAndUnknownQueryParameters(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "duplicate", path: "/api/git/status?base=one&base=two"},
		{name: "unknown", path: "/api/git/status?extra=value"},
		{name: "base and unknown", path: "/api/git/status?base=work&extra=value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			statuses := &gitStatusReaderFake{}
			handler := NewGitHandler(&gitProberFake{}, &gitConfigurerFake{}, statuses)
			recorder := httptest.NewRecorder()

			handler.Status(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))

			assertAPIErrorResponse(t, recorder, http.StatusBadRequest, model.APIError{Code: "bad_query", Message: "Invalid query parameters"})
			if statuses.calls != 0 {
				t.Fatalf("Status calls = %d, want 0", statuses.calls)
			}
		})
	}
}

func TestGitHandlerDelegatesServiceErrors(t *testing.T) {
	tests := []struct {
		name string
		call func(*GitHandler, http.ResponseWriter, *http.Request)
		path string
		body string
	}{
		{name: "probe", call: (*GitHandler).Probe, path: "/api/git/probe", body: `{"base":"missing","git_url":"url"}`},
		{name: "configure", call: (*GitHandler).Configure, path: "/api/git/config?base=missing", body: `{"git_url":"url","git_branch":"main"}`},
		{name: "disable", call: (*GitHandler).Disable, path: "/api/git/config?base=missing"},
		{name: "status", call: (*GitHandler).Status, path: "/api/git/status?base=missing"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prober := &gitProberFake{err: service.ErrBaseNotFound}
			configurer := &gitConfigurerFake{configureErr: service.ErrBaseNotFound, disableErr: service.ErrBaseNotFound}
			statuses := &gitStatusReaderFake{err: service.ErrBaseNotFound}
			handler := NewGitHandler(prober, configurer, statuses)
			recorder := httptest.NewRecorder()

			test.call(handler, recorder, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body)))

			assertAPIErrorResponse(t, recorder, http.StatusNotFound, model.APIError{Code: "base_not_found", Message: "base not found"})
		})
	}
}
