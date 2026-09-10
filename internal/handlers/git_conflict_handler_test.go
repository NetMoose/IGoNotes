package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
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

func TestGitConflictRESTResolutionLifecycle(t *testing.T) {
	fixture := newGitConflictRESTFixture(t)
	fixture.createTwoConflicts(t)

	listed := fixture.list(t)
	if len(listed.Conflicts) != 2 || listed.CanComplete {
		t.Fatalf("initial conflicts = %#v", listed)
	}
	fixture.resolve(t, listed.Conflicts[0], model.GitConflictUseRemote, "first.md")

	fixture.reconstructManager(t)
	listed = fixture.list(t)
	if len(listed.Conflicts) != 1 || listed.Conflicts[0].Path != "second.md" || listed.OperationID == "" {
		t.Fatalf("conflicts after manager reconstruction = %#v", listed)
	}
	merged := "merged second\n"
	fixture.resolveManual(t, listed.Conflicts[0], "second.md", merged)

	ready := fixture.list(t)
	if !ready.CanComplete || len(ready.Conflicts) != 0 {
		t.Fatalf("resolved conflicts = %#v", ready)
	}
	operation := fixture.complete(t)
	status := fixture.waitForState(t, model.GitStateReady)
	if status.OperationID != operation.OperationID || !reflect.DeepEqual(status.ChangedPaths, []string{"first.md", "second.md"}) {
		t.Fatalf("completed status = %#v", status)
	}
	remoteOID := fixture.git(t, fixture.remote, "rev-parse", "refs/heads/main^{commit}")
	trustedOID := fixture.git(t, fixture.local, "rev-parse", "refs/igonotes/remotes/main^{commit}")
	if status.RemoteOID != remoteOID || remoteOID != trustedOID {
		t.Fatalf("remote/trusted/status OIDs = %q/%q/%q", remoteOID, trustedOID, status.RemoteOID)
	}
	if content, err := fixture.notes.GetNoteContent("second.md"); err != nil || content != merged {
		t.Fatalf("rebuilt note index content = %q, %v", content, err)
	}
	if err := fixture.coordinator.CheckMutation(fixture.local); err != nil {
		t.Fatalf("completion left mutations blocked: %v", err)
	}
}

func TestGitConflictRESTAbortLifecycle(t *testing.T) {
	fixture := newGitConflictRESTFixture(t)
	fixture.createTwoConflicts(t)
	remoteBefore := fixture.git(t, fixture.remote, "rev-parse", "refs/heads/main^{commit}")

	listed := fixture.list(t)
	if len(listed.Conflicts) != 2 {
		t.Fatalf("initial conflicts = %#v", listed)
	}
	operation := fixture.abort(t)
	status := fixture.waitForState(t, model.GitStatePaused)
	if status.OperationID != operation.OperationID || !reflect.DeepEqual(status.ChangedPaths, []string{"first.md", "second.md"}) {
		t.Fatalf("paused status = %#v", status)
	}
	if content, err := os.ReadFile(filepath.Join(fixture.local, "first.md")); err != nil || string(content) != "local first.md\n" {
		t.Fatalf("restored first path = %q, %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(fixture.local, "second.md")); err != nil || string(content) != "local second.md\n" {
		t.Fatalf("restored second path = %q, %v", content, err)
	}
	if remoteAfter := fixture.git(t, fixture.remote, "rev-parse", "refs/heads/main^{commit}"); remoteAfter != remoteBefore {
		t.Fatalf("abort changed remote from %q to %q", remoteBefore, remoteAfter)
	}
	fixture.assertAbortRestoredSnapshot(t)
	fixture.waitForMutationsOpen(t)
	fixture.assertPausedSyncRejected(t)
}

func TestGitConflictRESTNegativeLifecycle(t *testing.T) {
	t.Run("text conflict", func(t *testing.T) {
		newGitConflictRESTFixture(t).assertNegativeRESTCases(t)
	})
	t.Run("binary add-add", func(t *testing.T) {
		newGitConflictRESTFixture(t).assertBinaryAddAddRESTErrors(t)
	})
	t.Run("abort without merge", func(t *testing.T) {
		newGitConflictRESTFixture(t).assertAbortWithoutMergeREST(t)
	})
	t.Run("ambiguous restart", func(t *testing.T) {
		newGitConflictRESTFixture(t).assertAmbiguousRestartREST(t)
	})
}

type gitConflictRESTFixture struct {
	db          *sql.DB
	statuses    *repository.GitStatusRepository
	operations  *repository.GitOperationRepository
	coordinator *service.BaseOperationCoordinator
	notes       *service.NoteService
	manager     *service.GitManager
	base        gitcmd.ConfiguredBase
	remote      string
	local       string
	other       string
}

type gitConflictRESTSettings struct{ config model.Config }

func (s gitConflictRESTSettings) GetConfig() model.Config { return s.config }

func newGitConflictRESTFixture(t *testing.T) *gitConflictRESTFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	fixture := &gitConflictRESTFixture{
		remote: filepath.Join(root, "remote.git"),
		local:  filepath.Join(root, "local"),
		other:  filepath.Join(root, "other"),
	}
	fixture.git(t, root, "init", "--bare", "--initial-branch", "main", fixture.remote)
	seed := filepath.Join(root, "seed")
	fixture.git(t, root, "init", "--initial-branch", "main", seed)
	fixture.configureIdentity(t, seed)
	for _, name := range []string{"first.md", "second.md"} {
		if err := os.WriteFile(filepath.Join(seed, name), []byte("base "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.git(t, seed, "add", "--all")
	fixture.git(t, seed, "commit", "-m", "base")
	fixture.git(t, seed, "remote", "add", "origin", fixture.remote)
	fixture.git(t, seed, "push", "--no-verify", "origin", "HEAD:refs/heads/main")
	for _, path := range []string{fixture.local, fixture.other} {
		fixture.git(t, root, "clone", "--branch", "main", fixture.remote, path)
		fixture.configureIdentity(t, path)
	}
	fixture.base = gitcmd.ConfiguredBase{
		Name: "work", Path: fixture.local, URL: fixture.remote, Branch: "main",
		IntervalMinutes: 15, CommitTemplate: service.DefaultGitCommitMessageTemplate,
		Fingerprint: "config-work", RemoteFingerprint: "remote-work",
	}
	db, err := repository.InitDB(filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	fixture.db = db
	fixture.statuses = repository.NewGitStatusRepository(db)
	fixture.operations = repository.NewGitOperationRepository(db)
	fixture.coordinator = service.NewBaseOperationCoordinator()
	fixture.notes = service.NewNoteService(repository.NewNoteRepository(db), fixture.local, fixture.coordinator)
	if err := fixture.notes.SyncFS(); err != nil {
		t.Fatal(err)
	}
	fixture.reconstructManager(t)
	t.Cleanup(func() {
		_ = fixture.manager.Close()
		_ = fixture.notes.Close()
		_ = fixture.db.Close()
	})
	return fixture
}

func (f *gitConflictRESTFixture) reconstructManager(t *testing.T) {
	t.Helper()
	if f.manager != nil {
		if err := f.manager.Close(); err != nil {
			t.Fatal(err)
		}
	}
	runner := gitcmd.NewCommandRunner()
	config := model.Config{Bases: []model.Base{{Name: f.base.Name, Path: f.base.Path, GitURL: f.base.URL, GitBranch: f.base.Branch}}, CurrentBase: f.base.Name}
	prober := service.NewGitProbeService(gitConflictRESTSettings{config: config}, gitcmd.NewClient(runner))
	f.manager = service.NewGitManager(
		gitcmd.NewService(runner, gitcmd.NewClient(runner)), f.statuses, f.operations, prober,
		func(name string) (gitcmd.ConfiguredBase, bool, error) {
			if name != f.base.Name {
				return gitcmd.ConfiguredBase{}, false, service.ErrBaseNotFound
			}
			return f.base, true, nil
		},
		f.notes, f.coordinator,
	)
	if err := f.manager.Start(); err != nil {
		t.Fatal(err)
	}
}

func (f *gitConflictRESTFixture) createTwoConflicts(t *testing.T) {
	t.Helper()
	for _, path := range []string{"first.md", "second.md"} {
		if err := os.WriteFile(filepath.Join(f.local, path), []byte("local "+path+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.git(t, f.local, "commit", "-am", "local conflicts")
	localOID := f.git(t, f.local, "rev-parse", "HEAD^{commit}")
	for _, path := range []string{"first.md", "second.md"} {
		if err := os.WriteFile(filepath.Join(f.other, path), []byte("remote "+path+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.git(t, f.other, "commit", "-am", "remote conflicts")
	f.git(t, f.other, "push", "--no-verify", "origin", "HEAD:refs/heads/main")
	f.git(t, f.local, "fetch", "--no-tags", "origin", "+refs/heads/main:refs/igonotes/remotes/main")
	remoteOID := f.git(t, f.local, "rev-parse", "refs/igonotes/remotes/main^{commit}")
	command := exec.Command("git", "merge", "--no-commit", "--no-ff", remoteOID)
	command.Dir = f.local
	command.Env = f.gitEnvironment()
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", output)
	}
	f.recordConflict(t, localOID, remoteOID, []string{"first.md", "second.md"})
}

func (f *gitConflictRESTFixture) createBinaryAddAddConflict(t *testing.T) {
	t.Helper()
	path := "assets/images/asset.bin"
	if err := os.MkdirAll(filepath.Join(f.local, "assets", "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.local, path), []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(t, f.local, "add", "--", path)
	f.git(t, f.local, "commit", "-m", "local binary add")
	localOID := f.git(t, f.local, "rev-parse", "HEAD^{commit}")
	if err := os.MkdirAll(filepath.Join(f.other, "assets", "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.other, path), []byte{4, 5, 6, 7}, 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(t, f.other, "add", "--", path)
	f.git(t, f.other, "commit", "-m", "remote binary add")
	f.git(t, f.other, "push", "--no-verify", "origin", "HEAD:refs/heads/main")
	f.git(t, f.local, "fetch", "--no-tags", "origin", "+refs/heads/main:refs/igonotes/remotes/main")
	remoteOID := f.git(t, f.local, "rev-parse", "refs/igonotes/remotes/main^{commit}")
	command := exec.Command("git", "merge", "--no-commit", "--no-ff", remoteOID)
	command.Dir = f.local
	command.Env = f.gitEnvironment()
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("binary add/add merge unexpectedly succeeded: %s", output)
	}
	f.recordConflict(t, localOID, remoteOID, []string{path})
}

func (f *gitConflictRESTFixture) recordConflict(t *testing.T, localOID, remoteOID string, paths []string) {
	t.Helper()
	now := time.Now().UTC()
	original := gitcmd.Operation{
		ID: strings.Repeat("a", 32), BaseName: f.base.Name, RepoPath: f.local,
		ConfigFingerprint: f.base.Fingerprint, RemoteFingerprint: f.base.RemoteFingerprint,
		Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageMerging,
		Branch: f.base.Branch, LocalOID: localOID, CandidateOID: remoteOID, RemoteOID: remoteOID,
		ChangedPaths: append([]string(nil), paths...), ConflictPaths: append([]string(nil), paths...), CreatedAt: now, UpdatedAt: now,
	}
	if err := f.operations.CreateQueued(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	original.State = gitcmd.OperationConflict
	if err := f.operations.Finish(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	if err := f.statuses.Upsert(context.Background(), model.GitStatus{
		Base: f.base.Name, RepositoryPath: f.local, State: model.GitStateConflict,
		OperationID: original.ID, Stage: string(original.Stage), ChangedPaths: original.ConflictPaths, RemoteOID: remoteOID,
	}); err != nil {
		t.Fatal(err)
	}
	f.coordinator.SetConflict(f.local, true)
}

func (f *gitConflictRESTFixture) list(t *testing.T) model.GitConflictListResponse {
	t.Helper()
	mux := http.NewServeMux()
	RegisterGitConflictRoutes(mux, NewGitConflictHandler(f.manager), &gitRouteSetupState{completed: true})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, newLocalRouterRequest(http.MethodGet, "/api/git/conflicts?base=work", nil))
	var response model.GitConflictListResponse
	decodeHandlerJSON(t, recorder, http.StatusOK, &response)
	return response
}

func (f *gitConflictRESTFixture) resolve(t *testing.T, conflict model.GitConflict, action model.GitConflictAction, resultPath string) {
	t.Helper()
	request := model.GitConflictResolveRequest{
		Base: "work", OperationID: f.list(t).OperationID, ConflictID: conflict.ID, Path: conflict.Path,
		Action: action, ResultPath: resultPath,
	}
	if action == model.GitConflictUseLocal {
		request.LocalOID = conflict.Local.OID
	} else {
		request.RemoteOID = conflict.Remote.OID
	}
	f.resolveRequest(t, request)
}

func (f *gitConflictRESTFixture) resolveManual(t *testing.T, conflict model.GitConflict, resultPath, content string) {
	t.Helper()
	f.resolveRequest(t, model.GitConflictResolveRequest{
		Base: "work", OperationID: f.list(t).OperationID, ConflictID: conflict.ID, Path: conflict.Path,
		Action: model.GitConflictManual, ResultPath: resultPath, Content: &content,
	})
}

func (f *gitConflictRESTFixture) resolveRequest(t *testing.T, request model.GitConflictResolveRequest) {
	t.Helper()
	mux := http.NewServeMux()
	RegisterGitConflictRoutes(mux, NewGitConflictHandler(f.manager), &gitRouteSetupState{completed: true})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, newLocalRouterRequest(http.MethodPut, "/api/git/conflicts/resolve", strings.NewReader(marshalHandlerJSON(t, request))))
	var response model.GitConflictResolveResponse
	decodeHandlerJSON(t, recorder, http.StatusOK, &response)
}

func (f *gitConflictRESTFixture) complete(t *testing.T) model.GitOperationResponse {
	t.Helper()
	mux := http.NewServeMux()
	RegisterGitConflictRoutes(mux, NewGitConflictHandler(f.manager), &gitRouteSetupState{completed: true})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, newLocalRouterRequest(http.MethodPost, "/api/git/conflicts/complete?base=work", nil))
	var response model.GitOperationResponse
	decodeHandlerJSON(t, recorder, http.StatusAccepted, &response)
	return response
}

func (f *gitConflictRESTFixture) abort(t *testing.T) model.GitOperationResponse {
	t.Helper()
	mux := http.NewServeMux()
	RegisterGitConflictRoutes(mux, NewGitConflictHandler(f.manager), &gitRouteSetupState{completed: true})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, newLocalRouterRequest(http.MethodPost, "/api/git/conflicts/abort?base=work", nil))
	var response model.GitOperationResponse
	decodeHandlerJSON(t, recorder, http.StatusAccepted, &response)
	return response
}

func (f *gitConflictRESTFixture) assertPausedSyncRejected(t *testing.T) {
	t.Helper()
	configured := &gitOperationConfigurerFake{snapshot: f.base}
	handler := NewGitHandlerWithOperations(&gitProberFake{}, configured, &gitStatusReaderFake{}, f.manager)
	recorder := httptest.NewRecorder()
	handler.Sync(recorder, httptest.NewRequest(http.MethodPost, "/api/git/sync?base=work", nil))
	assertAPIErrorResponse(t, recorder, http.StatusConflict, model.APIError{Code: "git_paused", Message: "Git synchronization is paused"})
	latest, found, err := f.operations.LatestByPath(context.Background(), f.local)
	if err != nil || !found || latest.Kind != gitcmd.OperationConflictAbort || latest.State != gitcmd.OperationSucceeded {
		t.Fatalf("manual sync queued despite pause: %#v, %v, %v", latest, found, err)
	}
}

func (f *gitConflictRESTFixture) assertAbortRestoredSnapshot(t *testing.T) {
	t.Helper()
	original, found, err := f.operations.LatestConflictByPath(context.Background(), f.local)
	if err != nil || !found {
		t.Fatalf("original conflict operation = %#v, %v, %v", original, found, err)
	}
	if head := f.git(t, f.local, "rev-parse", "HEAD^{commit}"); head != original.LocalOID {
		t.Fatalf("restored HEAD = %q, want original LocalOID %q", head, original.LocalOID)
	}
	if unmerged := f.git(t, f.local, "ls-files", "--unmerged", "--stage", "--full-name", "-z"); unmerged != "" {
		t.Fatalf("unmerged index snapshot remains: %q", unmerged)
	}
	if status := f.git(t, f.local, "status", "--porcelain=v1", "-z"); status != "" {
		t.Fatalf("staged conflict snapshot remains: %q", status)
	}
}

func (f *gitConflictRESTFixture) assertBinaryAddAddRESTErrors(t *testing.T) {
	t.Helper()
	f.createBinaryAddAddConflict(t)
	listed := f.list(t)
	if len(listed.Conflicts) != 1 || listed.Conflicts[0].Kind != model.GitConflictAddAdd || listed.Conflicts[0].ContentKind != model.GitConflictBinary {
		t.Fatalf("binary add/add conflict = %#v", listed)
	}
	conflict := listed.Conflicts[0]
	content := "manual text"
	f.assertResolveError(t, model.GitConflictResolveRequest{
		Base: "work", OperationID: listed.OperationID, ConflictID: conflict.ID, Path: conflict.Path,
		Action: model.GitConflictManual, ResultPath: conflict.Path, Content: &content,
	}, http.StatusUnprocessableEntity, model.APIError{Code: "git_conflict_unsupported", Message: "Invalid conflict action", Field: "action"})
	f.assertResolveError(t, model.GitConflictResolveRequest{
		Base: "work", OperationID: listed.OperationID, ConflictID: conflict.ID, Path: conflict.Path,
		Action: model.GitConflictKeepBoth, LocalPath: "assets/images/same.bin", RemotePath: "assets/images/same.bin",
		LocalOID: conflict.Local.OID, RemoteOID: conflict.Remote.OID,
	}, http.StatusUnprocessableEntity, model.APIError{Code: "git_conflict_unsupported", Message: "Conflict output paths must differ", Field: "remote_path"})
}

func (f *gitConflictRESTFixture) assertAbortWithoutMergeREST(t *testing.T) {
	t.Helper()
	f.createTwoConflicts(t)
	f.git(t, f.local, "merge", "--abort")
	recorder := f.serveConflict(t, http.MethodPost, "/api/git/conflicts/abort?base=work", nil)
	assertAPIErrorResponse(t, recorder, http.StatusConflict, model.APIError{Code: "git_recovery_required", Message: "Git repository requires recovery"})
}

func (f *gitConflictRESTFixture) assertAmbiguousRestartREST(t *testing.T) {
	t.Helper()
	f.createTwoConflicts(t)
	for _, path := range []string{"first.md", "second.md"} {
		if err := os.WriteFile(filepath.Join(f.local, path), []byte("resolved "+path+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.git(t, f.local, "add", "--all")
	f.git(t, f.local, "commit", "--no-edit")
	if err := os.WriteFile(filepath.Join(f.local, "external.md"), []byte("external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(t, f.local, "add", "--", "external.md")
	f.git(t, f.local, "commit", "-m", "external commit after merge")
	f.reconstructManager(t)
	recorder := f.serveConflict(t, http.MethodGet, "/api/git/conflicts?base=work", nil)
	assertAPIErrorResponse(t, recorder, http.StatusConflict, model.APIError{Code: "git_recovery_required", Message: "Git repository requires recovery"})
}

func (f *gitConflictRESTFixture) assertNegativeRESTCases(t *testing.T) {
	t.Helper()
	f.createTwoConflicts(t)
	conflict := f.list(t).Conflicts[0]
	manual := func(resultPath string) model.GitConflictResolveRequest {
		content := "manual\n"
		return model.GitConflictResolveRequest{
			Base: "work", OperationID: f.list(t).OperationID, ConflictID: conflict.ID, Path: conflict.Path,
			Action: model.GitConflictManual, ResultPath: resultPath, Content: &content,
		}
	}
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "absolute", path: "/outside.md"},
		{name: "traversal", path: "../outside.md"},
		{name: "git directory", path: ".git/config"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.assertResolveError(t, manual(test.path), http.StatusUnprocessableEntity, model.APIError{
				Code: "git_conflict_unsupported", Message: "Invalid conflict path", Field: "result_path",
			})
		})
	}
	staleID := manual("manual.md")
	staleID.ConflictID = "sha256:" + strings.Repeat("0", 64)
	f.assertResolveError(t, staleID, http.StatusConflict, model.APIError{Code: "git_conflict_stale", Message: "Git conflict changed; refresh and try again"})
	staleOID := model.GitConflictResolveRequest{
		Base: "work", OperationID: f.list(t).OperationID, ConflictID: conflict.ID, Path: conflict.Path,
		Action: model.GitConflictUseLocal, ResultPath: "local.md", LocalOID: strings.Repeat("0", 40),
	}
	f.assertResolveError(t, staleOID, http.StatusConflict, model.APIError{Code: "git_conflict_stale", Message: "Git conflict changed; refresh and try again"})
	complete := f.serveConflict(t, http.MethodPost, "/api/git/conflicts/complete?base=work", nil)
	assertAPIErrorResponse(t, complete, http.StatusConflict, model.APIError{Code: "git_conflict_unresolved", Message: "Git conflict still has unresolved paths"})
	lock := filepath.Join(f.local, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("owned by another Git process\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(lock) })
	recorder := f.serveConflict(t, http.MethodGet, "/api/git/conflicts?base=work", nil)
	assertAPIErrorResponse(t, recorder, http.StatusConflict, model.APIError{Code: "git_recovery_required", Message: "Git repository requires recovery"})
	if contents, err := os.ReadFile(lock); err != nil || string(contents) != "owned by another Git process\n" {
		t.Fatalf("repository lock changed: %q, %v", contents, err)
	}
}

func (f *gitConflictRESTFixture) assertResolveError(t *testing.T, request model.GitConflictResolveRequest, status int, want model.APIError) {
	t.Helper()
	recorder := f.serveConflict(t, http.MethodPut, "/api/git/conflicts/resolve", strings.NewReader(marshalHandlerJSON(t, request)))
	assertAPIErrorResponse(t, recorder, status, want)
}

func (f *gitConflictRESTFixture) serveConflict(t *testing.T, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	RegisterGitConflictRoutes(mux, NewGitConflictHandler(f.manager), &gitRouteSetupState{completed: true})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, newLocalRouterRequest(method, path, body))
	return recorder
}

func (f *gitConflictRESTFixture) waitForState(t *testing.T, want model.GitState) model.GitStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, found, err := f.statuses.Get(context.Background(), f.local)
		if err != nil {
			t.Fatal(err)
		}
		if found && status.State == want {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _, _ := f.statuses.Get(context.Background(), f.local)
	t.Fatalf("status = %#v, want state %q", status, want)
	return model.GitStatus{}
}

func (f *gitConflictRESTFixture) waitForMutationsOpen(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := f.coordinator.CheckMutation(f.local); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("abort left mutations blocked: %v", f.coordinator.CheckMutation(f.local))
}

func (f *gitConflictRESTFixture) configureIdentity(t *testing.T, dir string) {
	t.Helper()
	f.git(t, dir, "config", "user.name", "IGoNotes REST Test")
	f.git(t, dir, "config", "user.email", "igonotes-rest@example.invalid")
}

func (f *gitConflictRESTFixture) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = f.gitEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func (f *gitConflictRESTFixture) gitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}
