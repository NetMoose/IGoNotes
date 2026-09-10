package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
)

type gitConflictManagerFake struct {
	listCalls    int
	listContext  context.Context
	listBase     string
	listResponse model.GitConflictListResponse
	listErr      error

	resolveCalls    int
	resolveContext  context.Context
	resolveRequest  model.GitConflictResolveRequest
	resolveResponse model.GitConflictResolveResponse
	resolveErr      error

	completeCalls     int
	completeContext   context.Context
	completeBase      string
	completeOperation gitcmd.Operation
	completeDuplicate bool
	completeErr       error

	abortCalls     int
	abortContext   context.Context
	abortBase      string
	abortOperation gitcmd.Operation
	abortDuplicate bool
	abortErr       error
}

func (f *gitConflictManagerFake) ListConflicts(ctx context.Context, base string) (model.GitConflictListResponse, error) {
	f.listCalls++
	f.listContext = ctx
	f.listBase = base
	return f.listResponse, f.listErr
}

func (f *gitConflictManagerFake) ResolveConflict(ctx context.Context, request model.GitConflictResolveRequest) (model.GitConflictResolveResponse, error) {
	f.resolveCalls++
	f.resolveContext = ctx
	f.resolveRequest = request
	return f.resolveResponse, f.resolveErr
}

func (f *gitConflictManagerFake) QueueConflictComplete(ctx context.Context, base string) (gitcmd.Operation, bool, error) {
	f.completeCalls++
	f.completeContext = ctx
	f.completeBase = base
	return f.completeOperation, f.completeDuplicate, f.completeErr
}

func (f *gitConflictManagerFake) QueueConflictAbort(ctx context.Context, base string) (gitcmd.Operation, bool, error) {
	f.abortCalls++
	f.abortContext = ctx
	f.abortBase = base
	return f.abortOperation, f.abortDuplicate, f.abortErr
}

func TestGitConflictHandlerListRequiresBase(t *testing.T) {
	manager := &gitConflictManagerFake{}
	handler := NewGitConflictHandler(manager)
	recorder := httptest.NewRecorder()

	handler.List(recorder, httptest.NewRequest(http.MethodGet, "/api/git/conflicts", nil))

	assertMissingField(t, recorder, "base")
	if manager.listCalls != 0 {
		t.Fatalf("ListConflicts calls = %d, want 0", manager.listCalls)
	}
}

func TestGitConflictHandlerListReturnsConflicts(t *testing.T) {
	response := model.GitConflictListResponse{
		Base: "work", OperationID: "operation-1", HeadOID: "head", MergeHeadOID: "merge",
		Conflicts: []model.GitConflict{{ID: "sha256:abc", Actions: []model.GitConflictAction{}}},
	}
	manager := &gitConflictManagerFake{listResponse: response}
	handler := NewGitConflictHandler(manager)
	type contextKey string
	request := httptest.NewRequest(http.MethodGet, "/api/git/conflicts?base=work", nil).WithContext(context.WithValue(context.Background(), contextKey("request"), "list"))
	recorder := httptest.NewRecorder()

	handler.List(recorder, request)

	if manager.listCalls != 1 || manager.listBase != "work" || manager.listContext != request.Context() {
		t.Fatalf("ListConflicts calls/base/context = %d/%q/%v", manager.listCalls, manager.listBase, manager.listContext)
	}
	var got model.GitConflictListResponse
	decodeHandlerJSON(t, recorder, http.StatusOK, &got)
	if !reflect.DeepEqual(got, response) {
		t.Errorf("response = %#v, want %#v", got, response)
	}
}

func TestGitConflictHandlerResolveRejectsInvalidJSON(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "absent", body: ""},
		{name: "malformed", body: "{"},
		{name: "multiple", body: `{"base":"work"} {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &gitConflictManagerFake{}
			handler := NewGitConflictHandler(manager)
			body := &closeTrackingBody{Reader: strings.NewReader(test.body)}
			recorder := httptest.NewRecorder()

			handler.Resolve(recorder, httptest.NewRequest(http.MethodPut, "/api/git/conflicts/resolve", body))

			assertAPIErrorResponse(t, recorder, http.StatusBadRequest, model.APIError{Code: "bad_json", Message: "Invalid JSON"})
			if manager.resolveCalls != 0 || !body.closed {
				t.Fatalf("ResolveConflict calls/body closed = %d/%t, want 0/true", manager.resolveCalls, body.closed)
			}
		})
	}
}

func TestGitConflictHandlerResolveValidatesRequest(t *testing.T) {
	valid := `{"base":"work","operation_id":"operation-1","conflict_id":"sha256:abc","path":"notes/idea.md","action":"local"}`
	for _, test := range []struct {
		name string
		body string
		want model.APIError
	}{
		{name: "base", body: `{}`, want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "base"}},
		{name: "operation", body: `{"base":"work"}`, want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "operation_id"}},
		{name: "conflict", body: `{"base":"work","operation_id":"operation-1"}`, want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "conflict_id"}},
		{name: "path", body: `{"base":"work","operation_id":"operation-1","conflict_id":"sha256:abc"}`, want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "path"}},
		{name: "action", body: `{"base":"work","operation_id":"operation-1","conflict_id":"sha256:abc","path":"notes/idea.md"}`, want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "action"}},
		{name: "unknown action", body: strings.Replace(valid, `"local"`, `"invalid"`, 1), want: model.APIError{Code: "invalid_request", Message: "Invalid request", Field: "action"}},
		{name: "manual content", body: strings.Replace(valid, `"local"`, `"manual"`, 1), want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "content"}},
		{name: "keep both paths", body: strings.Replace(valid, `"local"`, `"keep_both"`, 1), want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "local_path"}},
		{name: "keep both remote path", body: strings.Replace(valid, `"local"`, `"keep_both","local_path":"local.md"`, 1), want: model.APIError{Code: "missing_field", Message: "Missing required field", Field: "remote_path"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &gitConflictManagerFake{}
			handler := NewGitConflictHandler(manager)
			recorder := httptest.NewRecorder()

			handler.Resolve(recorder, httptest.NewRequest(http.MethodPut, "/api/git/conflicts/resolve", strings.NewReader(test.body)))

			assertAPIErrorResponse(t, recorder, http.StatusBadRequest, test.want)
			if manager.resolveCalls != 0 {
				t.Fatalf("ResolveConflict calls = %d, want 0", manager.resolveCalls)
			}
		})
	}
}

func TestGitConflictHandlerResolveForwardsLeadingDashes(t *testing.T) {
	content := "manual"
	want := model.GitConflictResolveRequest{
		Base: "-work", OperationID: "-operation", ConflictID: "-conflict", Path: "-note.md", Action: model.GitConflictManual,
		ResultPath: "-result.md", Content: &content, LocalPath: "-local.md", RemotePath: "-remote.md", LocalOID: "-local-oid", RemoteOID: "-remote-oid",
	}
	manager := &gitConflictManagerFake{resolveResponse: model.GitConflictResolveResponse{ResolvedPath: want.Path, Remaining: model.GitConflictListResponse{Conflicts: []model.GitConflict{}}}}
	handler := NewGitConflictHandler(manager)
	recorder := httptest.NewRecorder()

	handler.Resolve(recorder, httptest.NewRequest(http.MethodPut, "/api/git/conflicts/resolve", strings.NewReader(marshalHandlerJSON(t, want))))

	if manager.resolveCalls != 1 || !reflect.DeepEqual(manager.resolveRequest, want) {
		t.Fatalf("ResolveConflict request = %#v, want %#v", manager.resolveRequest, want)
	}
	var got model.GitConflictResolveResponse
	decodeHandlerJSON(t, recorder, http.StatusOK, &got)
	if !reflect.DeepEqual(got, manager.resolveResponse) {
		t.Errorf("response = %#v, want %#v", got, manager.resolveResponse)
	}
}

func TestGitConflictHandlerDelegatesRedactedServiceErrors(t *testing.T) {
	private := "https://user:secret@example.test/private.git"
	manager := &gitConflictManagerFake{resolveErr: fmt.Errorf("resolve %s: %w", private, &gitcmd.SafeError{Code: gitcmd.CodeConflictStale, Message: "Git conflict changed; refresh and try again"})}
	handler := NewGitConflictHandler(manager)
	recorder := httptest.NewRecorder()

	handler.Resolve(recorder, httptest.NewRequest(http.MethodPut, "/api/git/conflicts/resolve", strings.NewReader(`{"base":"work","operation_id":"operation-1","conflict_id":"sha256:abc","path":"notes/idea.md","action":"local"}`)))

	assertAPIErrorResponse(t, recorder, http.StatusConflict, model.APIError{Code: "git_conflict_stale", Message: "Git conflict changed; refresh and try again"})
	if strings.Contains(recorder.Body.String(), private) || manager.resolveCalls != 1 {
		t.Fatalf("response leaks private data or did not call manager: %q", recorder.Body.String())
	}
}

func TestGitConflictHandlerCompleteAndAbortQueueOperations(t *testing.T) {
	operation := gitcmd.Operation{ID: "0123456789abcdef0123456789abcdef", State: gitcmd.OperationQueued}
	for _, test := range []struct {
		name      string
		call      func(*GitConflictHandler, http.ResponseWriter, *http.Request)
		duplicate bool
		calls     func(*gitConflictManagerFake) int
		base      func(*gitConflictManagerFake) string
	}{
		{name: "complete", call: (*GitConflictHandler).Complete, calls: func(f *gitConflictManagerFake) int { return f.completeCalls }, base: func(f *gitConflictManagerFake) string { return f.completeBase }},
		{name: "abort", call: (*GitConflictHandler).Abort, duplicate: true, calls: func(f *gitConflictManagerFake) int { return f.abortCalls }, base: func(f *gitConflictManagerFake) string { return f.abortBase }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &gitConflictManagerFake{completeOperation: operation, abortOperation: operation, completeDuplicate: test.duplicate, abortDuplicate: test.duplicate}
			handler := NewGitConflictHandler(manager)
			recorder := httptest.NewRecorder()

			test.call(handler, recorder, httptest.NewRequest(http.MethodPost, "/api/git/conflicts/"+test.name+"?base=work", nil))

			if test.calls(manager) != 1 || test.base(manager) != "work" {
				t.Fatalf("queue calls/base = %d/%q, want 1/work", test.calls(manager), test.base(manager))
			}
			var got model.GitOperationResponse
			decodeHandlerJSON(t, recorder, http.StatusAccepted, &got)
			if got.OperationID != operation.ID || got.Status != "queued" || got.Deduplicated != test.duplicate {
				t.Errorf("response = %#v", got)
			}
		})
	}
}

func TestGitConflictHandlerCompleteAndAbortRequireBase(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(*GitConflictHandler, http.ResponseWriter, *http.Request)
	}{
		{name: "complete", call: (*GitConflictHandler).Complete},
		{name: "abort", call: (*GitConflictHandler).Abort},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &gitConflictManagerFake{}
			handler := NewGitConflictHandler(manager)
			recorder := httptest.NewRecorder()

			test.call(handler, recorder, httptest.NewRequest(http.MethodPost, "/api/git/conflicts/"+test.name, nil))

			assertMissingField(t, recorder, "base")
			if manager.completeCalls != 0 || manager.abortCalls != 0 {
				t.Fatalf("queue calls = complete %d, abort %d; want 0/0", manager.completeCalls, manager.abortCalls)
			}
		})
	}
}
