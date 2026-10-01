package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
)

type resumeRESTSettings struct{ config model.Config }

func (s resumeRESTSettings) ReadConfigSnapshot(read func(model.Config) error) error {
	return read(s.config)
}

func TestGitResumeRESTLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newGitConflictRESTFixture(t)
	if err := f.manager.Close(); err != nil {
		t.Fatal(err)
	}
	oid := f.git(t, f.local, "rev-parse", "HEAD")
	f.git(t, f.local, "update-ref", "refs/igonotes/remotes/main", oid)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	op := gitcmd.Operation{ID: strings.Repeat("a", 32), BaseName: f.base.Name, RepoPath: f.base.Path, ConfigFingerprint: f.base.Fingerprint, RemoteFingerprint: f.base.RemoteFingerprint, Branch: "main", Kind: gitcmd.OperationInitialize, State: gitcmd.OperationQueued, Stage: gitcmd.StageQueued, CreatedAt: now, UpdatedAt: now}
	if err := f.operations.CreateQueued(ctx, op); err != nil {
		t.Fatal(err)
	}
	op.State, op.Stage, op.RemoteOID, op.PushOID = gitcmd.OperationSucceeded, gitcmd.StageCompleted, oid, oid
	if err := f.operations.Finish(ctx, op); err != nil {
		t.Fatal(err)
	}
	paused := model.GitStatus{Base: "work", RepositoryPath: f.local, State: model.GitStatePaused, OperationID: op.ID, Stage: "completed", ConsecutiveFailures: 5, LastAttempt: &now, LastSuccess: &now, ChangedPaths: []string{}, RemoteOID: oid, Error: &model.APIError{Code: "remote_unreachable", Message: "Git remote is unreachable"}}
	if err := f.statuses.Upsert(ctx, paused); err != nil {
		t.Fatal(err)
	}
	if err := f.notes.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.db, err = repository.InitDB(filepath.Join(filepath.Dir(f.remote), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	f.db.SetMaxOpenConns(1)
	f.statuses, f.operations = repository.NewGitStatusRepository(f.db), repository.NewGitOperationRepository(f.db)
	f.coordinator = service.NewBaseOperationCoordinator()
	f.notes = service.NewNoteService(repository.NewNoteRepository(f.db), f.local, f.coordinator)
	config := model.Config{Bases: []model.Base{{Name: "work", Path: f.local, GitURL: f.remote, GitBranch: "main"}}, CurrentBase: "work"}
	runner := gitcmd.NewCommandRunner()
	client := gitcmd.NewClient(runner)
	prober := service.NewGitProbeService(gitConflictRESTSettings{config}, client)
	f.manager = service.NewGitManager(gitcmd.NewService(runner, client), f.statuses, f.operations, prober, func(name string) (gitcmd.ConfiguredBase, bool, error) { return f.base, true, nil }, f.notes, f.coordinator)
	if err := f.manager.RecoverLocal(ctx, []gitcmd.ConfiguredBase{f.base}); err != nil {
		t.Fatal(err)
	}
	handler := NewGitHandlerWithOperations(prober, concurrentGitOperationConfigurer{snapshots: map[string]gitcmd.ConfiguredBase{"work": f.base}}, service.NewGitStatusService(resumeRESTSettings{config}, f.statuses), f.manager)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/git/status", handler.Status)
	mux.HandleFunc("POST /api/git/sync", handler.Sync)
	mux.HandleFunc("POST /api/git/resume", handler.Resume)
	request := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, httptest.NewRequest(method, path, nil))
		return r
	}
	var statuses model.GitStatusResponse
	decodeHandlerJSON(t, request(http.MethodGet, "/api/git/status?base=work"), http.StatusOK, &statuses)
	if len(statuses.Statuses) != 1 || !reflect.DeepEqual(statuses.Statuses[0], paused) {
		t.Fatalf("reopened HTTP pause=%+v", statuses)
	}
	assertAPIErrorResponse(t, request(http.MethodPost, "/api/git/sync?base=work"), http.StatusConflict, model.APIError{Code: "git_paused", Message: "Git synchronization is paused"})
	var accepted, duplicate model.GitOperationResponse
	decodeHandlerJSON(t, request(http.MethodPost, "/api/git/resume?base=work"), http.StatusAccepted, &accepted)
	decodeHandlerJSON(t, request(http.MethodPost, "/api/git/resume?base=work"), http.StatusAccepted, &duplicate)
	if accepted.OperationID == "" || accepted.Status != "queued" || accepted.Deduplicated || !duplicate.Deduplicated || duplicate.OperationID != accepted.OperationID {
		t.Fatalf("resume=%+v duplicate=%+v", accepted, duplicate)
	}
	statuses = model.GitStatusResponse{}
	decodeHandlerJSON(t, request(http.MethodGet, "/api/git/status?base=work"), http.StatusOK, &statuses)
	if got := statuses.Statuses[0]; got.State != model.GitStateSyncing || got.ConsecutiveFailures != 0 || got.OperationID != accepted.OperationID || got.Error != nil {
		t.Fatalf("admitted resume=%+v", got)
	}
	if err := f.manager.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		status, _, err := f.statuses.Get(ctx, f.local)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == model.GitStateReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resume did not complete: %+v", status)
		}
		runtime.Gosched()
	}
	statuses = model.GitStatusResponse{}
	decodeHandlerJSON(t, request(http.MethodGet, "/api/git/status?base=work"), http.StatusOK, &statuses)
	if got := statuses.Statuses[0]; got.State != model.GitStateReady || got.ConsecutiveFailures != 0 || got.Error != nil || got.LastSuccess == nil {
		t.Fatalf("successful resume=%+v", got)
	}
	assertAPIErrorResponse(t, request(http.MethodPost, "/api/git/resume?base=work"), http.StatusConflict, model.APIError{Code: "git_not_paused", Message: "Git synchronization is not paused"})
}

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

type gitOperationConfigurerFake struct {
	gitConfigurerFake
	events             []string
	initializeResponse model.GitConfigResponse
	initializeSnapshot gitcmd.ConfiguredBase
	initializeErr      error
	initializeCalls    int
	initializeContext  context.Context
	initializeBase     string
	initializeRequest  model.GitConfigRequest
	snapshot           gitcmd.ConfiguredBase
	snapshotActive     bool
	snapshotErr        error
	snapshotCalls      int
	snapshotBase       string
}

func (f *gitOperationConfigurerFake) ConfigureGitForInitialize(ctx context.Context, base string, request model.GitConfigRequest) (model.GitConfigResponse, gitcmd.ConfiguredBase, error) {
	f.events = append(f.events, "configure")
	f.initializeCalls++
	f.initializeContext = ctx
	f.initializeBase = base
	f.initializeRequest = request
	return f.initializeResponse, f.initializeSnapshot, f.initializeErr
}

func (f *gitOperationConfigurerFake) GitSnapshot(base string) (gitcmd.ConfiguredBase, bool, error) {
	f.events = append(f.events, "snapshot")
	f.snapshotCalls++
	f.snapshotBase = base
	return f.snapshot, f.snapshotActive, f.snapshotErr
}

type gitOperationsFake struct {
	events              *[]string
	initializeOperation gitcmd.Operation
	initializeDuplicate bool
	initializeErr       error
	initializeCalls     int
	initializeRequest   gitcmd.InitializeRequest
	syncOperation       gitcmd.Operation
	syncDuplicate       bool
	syncErr             error
	syncCalls           int
	syncRequest         gitcmd.SyncRequest
	resumeOperation     gitcmd.Operation
	resumeDuplicate     bool
	resumeErr           error
	resumeCalls         int
	resumeContext       context.Context
	resumeBase          string
}

func (f *gitOperationsFake) Resume(ctx context.Context, base string) (gitcmd.Operation, bool, error) {
	f.resumeCalls++
	f.resumeContext = ctx
	f.resumeBase = base
	return f.resumeOperation, f.resumeDuplicate, f.resumeErr
}

func TestGitHandlerResumeReturnsExactAcceptedOperationAndForwardsTrimmedBase(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprintf("deduplicated=%v", duplicate), func(t *testing.T) {
			operations := &gitOperationsFake{
				resumeOperation: gitcmd.Operation{ID: "0123456789abcdef0123456789abcdef", State: gitcmd.OperationQueued},
				resumeDuplicate: duplicate,
			}
			handler := NewGitHandlerWithOperations(&gitProberFake{}, &gitOperationConfigurerFake{}, &gitStatusReaderFake{}, operations)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := httptest.NewRequest(http.MethodPost, "/api/git/resume?base=%20work%20", nil).WithContext(ctx)
			recorder := httptest.NewRecorder()

			handler.Resume(recorder, request)

			var got map[string]any
			decodeHandlerJSON(t, recorder, http.StatusAccepted, &got)
			want := map[string]any{"operation_id": operations.resumeOperation.ID, "status": "queued", "deduplicated": duplicate}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("response = %#v, want %#v", got, want)
			}
			if operations.resumeCalls != 1 || operations.resumeBase != "work" || operations.resumeContext != ctx {
				t.Fatalf("Resume calls/base/context = %d/%q/%v", operations.resumeCalls, operations.resumeBase, operations.resumeContext)
			}
			if operations.syncCalls != 0 || operations.initializeCalls != 0 {
				t.Fatal("resume must not queue through sync or initialize")
			}
		})
	}
}

func TestGitHandlerResumeValidatesBaseBeforeCallingOperations(t *testing.T) {
	for _, query := range []string{"", "?base=", "?base=%20%09%20"} {
		t.Run(query, func(t *testing.T) {
			operations := &gitOperationsFake{}
			handler := NewGitHandlerWithOperations(&gitProberFake{}, &gitOperationConfigurerFake{}, &gitStatusReaderFake{}, operations)
			recorder := httptest.NewRecorder()
			handler.Resume(recorder, httptest.NewRequest(http.MethodPost, "/api/git/resume"+query, nil))
			assertMissingField(t, recorder, "base")
			if operations.resumeCalls != 0 {
				t.Fatalf("Resume calls = %d, want 0", operations.resumeCalls)
			}
		})
	}
}

func TestGitHandlerResumeDoesNotLeakErrorDetails(t *testing.T) {
	operations := &gitOperationsFake{resumeErr: fmt.Errorf("resume https://user:secret@example.test/private.git: %w", errors.Join(
		&gitcmd.SafeError{Code: gitcmd.CodeNotPaused, Message: "private diagnostic", Field: "private_field"},
		errors.New("private cause"),
	))}
	handler := NewGitHandlerWithOperations(&gitProberFake{}, &gitOperationConfigurerFake{}, &gitStatusReaderFake{}, operations)
	recorder := httptest.NewRecorder()
	handler.Resume(recorder, httptest.NewRequest(http.MethodPost, "/api/git/resume?base=work", nil))
	assertAPIErrorResponse(t, recorder, http.StatusConflict, model.APIError{Code: "git_not_paused", Message: "Git synchronization is not paused"})
}

type concurrentGitOperationConfigurer struct {
	snapshots map[string]gitcmd.ConfiguredBase
}

func (f concurrentGitOperationConfigurer) ConfigureGit(context.Context, string, model.GitConfigRequest) (model.GitConfigResponse, error) {
	return model.GitConfigResponse{}, nil
}

func (f concurrentGitOperationConfigurer) DisableGit(context.Context, string) (model.GitConfigResponse, error) {
	return model.GitConfigResponse{}, nil
}

func (f concurrentGitOperationConfigurer) ConfigureGitForInitialize(context.Context, string, model.GitConfigRequest) (model.GitConfigResponse, gitcmd.ConfiguredBase, error) {
	return model.GitConfigResponse{}, gitcmd.ConfiguredBase{}, nil
}

func (f concurrentGitOperationConfigurer) GitSnapshot(base string) (gitcmd.ConfiguredBase, bool, error) {
	snapshot, found := f.snapshots[base]
	if !found {
		return gitcmd.ConfiguredBase{}, false, service.ErrBaseNotFound
	}
	return snapshot, false, nil
}

type concurrentGitOperations struct {
	mu         sync.Mutex
	operations map[string]gitcmd.Operation
	calls      map[string]int
}

func (f *concurrentGitOperations) QueueInitialize(context.Context, gitcmd.InitializeRequest) (gitcmd.Operation, bool, error) {
	return gitcmd.Operation{}, false, errors.New("initialize is not expected")
}

func (f *concurrentGitOperations) Resume(context.Context, string) (gitcmd.Operation, bool, error) {
	return gitcmd.Operation{}, false, errors.New("resume is not expected")
}

func (f *concurrentGitOperations) QueueSync(_ context.Context, request gitcmd.SyncRequest) (gitcmd.Operation, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[request.Snapshot.Path]++
	operation := f.operations[request.Snapshot.Path]
	return operation, f.calls[request.Snapshot.Path] > 1, nil
}

func (f *gitOperationsFake) QueueInitialize(_ context.Context, request gitcmd.InitializeRequest) (gitcmd.Operation, bool, error) {
	if f.events != nil {
		*f.events = append(*f.events, "queue_initialize")
	}
	f.initializeCalls++
	f.initializeRequest = request
	return f.initializeOperation, f.initializeDuplicate, f.initializeErr
}

func (f *gitOperationsFake) QueueSync(_ context.Context, request gitcmd.SyncRequest) (gitcmd.Operation, bool, error) {
	if f.events != nil {
		*f.events = append(*f.events, "queue_sync")
	}
	f.syncCalls++
	f.syncRequest = request
	return f.syncOperation, f.syncDuplicate, f.syncErr
}

func TestGitHandlerConfigureReturnsAcceptedAndQueuesSavedSnapshot(t *testing.T) {
	operation := gitcmd.Operation{ID: "0123456789abcdef0123456789abcdef", State: gitcmd.OperationQueued}
	configured := &gitOperationConfigurerFake{
		initializeResponse: model.GitConfigResponse{Base: model.Base{Name: "work"}},
		initializeSnapshot: gitcmd.ConfiguredBase{Name: "work", Path: "/notes", URL: "https://example.test/notes.git", Branch: "main"},
	}
	operations := &gitOperationsFake{events: &configured.events, initializeOperation: operation}
	statuses := &gitStatusReaderFake{response: model.GitStatusResponse{Statuses: []model.GitStatus{{Base: "work", State: model.GitStateInitializing, OperationID: operation.ID, Stage: "queued", ChangedPaths: []string{}}}}}
	handler := NewGitHandlerWithOperations(&gitProberFake{}, configured, statuses, operations)
	requestBody := model.GitConfigRequest{GitURL: "https://example.test/notes.git", GitBranch: "main", Confirmations: model.GitConfirmations{CreateRepository: true}}
	recorder := httptest.NewRecorder()

	handler.Configure(recorder, httptest.NewRequest(http.MethodPut, "/api/git/config?base=work", strings.NewReader(marshalHandlerJSON(t, requestBody))))

	if !reflect.DeepEqual(configured.events, []string{"configure", "queue_initialize"}) {
		t.Fatalf("events = %v, want configure then queue_initialize", configured.events)
	}
	if operations.initializeCalls != 1 || operations.initializeRequest.Snapshot != configured.initializeSnapshot || !reflect.DeepEqual(operations.initializeRequest.Confirmations, requestBody.Confirmations) {
		t.Fatalf("QueueInitialize request = %#v, want saved snapshot and confirmations", operations.initializeRequest)
	}
	if statuses.calls != 1 || statuses.base != "work" {
		t.Fatalf("Status calls/base = %d/%q, want 1/work", statuses.calls, statuses.base)
	}
	var got model.GitConfigResponse
	decodeHandlerJSON(t, recorder, http.StatusAccepted, &got)
	if got.Operation == nil || got.Operation.OperationID != operation.ID || got.Operation.Status != "queued" || got.Operation.Deduplicated {
		t.Fatalf("operation = %#v, want queued operation", got.Operation)
	}
	if !reflect.DeepEqual(got.Status, statuses.response.Statuses[0]) {
		t.Errorf("status = %#v, want %#v", got.Status, statuses.response.Statuses[0])
	}
}

func TestGitHandlerConfigureKeepsSavedConfigWhenQueueFails(t *testing.T) {
	configured := &gitOperationConfigurerFake{
		initializeResponse: model.GitConfigResponse{Base: model.Base{Name: "work"}},
		initializeSnapshot: gitcmd.ConfiguredBase{Name: "work", Path: "/notes", URL: "https://example.test/notes.git", Branch: "main"},
	}
	operations := &gitOperationsFake{events: &configured.events, initializeErr: &gitcmd.SafeError{Code: gitcmd.CodeNeedsReconnect, Message: "Git configuration changed; reconnect is required"}}
	handler := NewGitHandlerWithOperations(&gitProberFake{}, configured, &gitStatusReaderFake{}, operations)
	recorder := httptest.NewRecorder()

	handler.Configure(recorder, httptest.NewRequest(http.MethodPut, "/api/git/config?base=work", strings.NewReader(`{"git_url":"https://example.test/notes.git","git_branch":"main"}`)))

	assertAPIErrorResponse(t, recorder, http.StatusConflict, model.APIError{Code: "needs_reconnect", Message: "Git configuration changed; reconnect is required"})
	if !reflect.DeepEqual(configured.events, []string{"configure", "queue_initialize"}) {
		t.Fatalf("events = %v, want persisted config followed by failed queue", configured.events)
	}
}

func TestGitHandlerManualSyncReturnsAccepted(t *testing.T) {
	operation := gitcmd.Operation{ID: "0123456789abcdef0123456789abcdef", State: gitcmd.OperationQueued}
	snapshot := gitcmd.ConfiguredBase{Name: "work", Path: "/notes", URL: "https://example.test/notes.git", Branch: "main"}
	configured := &gitOperationConfigurerFake{snapshot: snapshot}
	operations := &gitOperationsFake{events: &configured.events, syncOperation: operation}
	handler := NewGitHandlerWithOperations(&gitProberFake{}, configured, &gitStatusReaderFake{}, operations)
	recorder := httptest.NewRecorder()

	handler.Sync(recorder, httptest.NewRequest(http.MethodPost, "/api/git/sync?base=work", nil))

	if !reflect.DeepEqual(configured.events, []string{"snapshot", "queue_sync"}) || operations.syncCalls != 1 || operations.syncRequest.Snapshot != snapshot {
		t.Fatalf("sync events/request = %v/%#v", configured.events, operations.syncRequest)
	}
	var got model.GitOperationResponse
	decodeHandlerJSON(t, recorder, http.StatusAccepted, &got)
	if got.OperationID != operation.ID || got.Status != "queued" || got.Deduplicated {
		t.Errorf("response = %#v, want queued operation", got)
	}
}

func TestGitHandlerOperationResponsesReportDeduplication(t *testing.T) {
	operation := gitcmd.Operation{ID: "0123456789abcdef0123456789abcdef", State: gitcmd.OperationQueued}
	snapshot := gitcmd.ConfiguredBase{Name: "work", Path: "/notes", URL: "https://example.test/notes.git", Branch: "main"}
	configured := &gitOperationConfigurerFake{
		initializeResponse: model.GitConfigResponse{Base: model.Base{Name: "work"}},
		initializeSnapshot: snapshot,
		snapshot:           snapshot,
	}
	operations := &gitOperationsFake{events: &configured.events, initializeOperation: operation, initializeDuplicate: true, syncOperation: operation, syncDuplicate: true}
	statuses := &gitStatusReaderFake{response: model.GitStatusResponse{Statuses: []model.GitStatus{{Base: "work", State: model.GitStateInitializing, OperationID: operation.ID, Stage: "queued", ChangedPaths: []string{}}}}}
	handler := NewGitHandlerWithOperations(&gitProberFake{}, configured, statuses, operations)

	configureRecorder := httptest.NewRecorder()
	handler.Configure(configureRecorder, httptest.NewRequest(http.MethodPut, "/api/git/config?base=work", strings.NewReader(`{"git_url":"https://example.test/notes.git","git_branch":"main"}`)))
	var configureResponse model.GitConfigResponse
	decodeHandlerJSON(t, configureRecorder, http.StatusAccepted, &configureResponse)
	if configureResponse.Operation == nil || !configureResponse.Operation.Deduplicated || configureResponse.Operation.OperationID != operation.ID {
		t.Fatalf("configure operation = %#v, want deduplicated %q", configureResponse.Operation, operation.ID)
	}

	syncRecorder := httptest.NewRecorder()
	handler.Sync(syncRecorder, httptest.NewRequest(http.MethodPost, "/api/git/sync?base=work", nil))
	var syncResponse model.GitOperationResponse
	decodeHandlerJSON(t, syncRecorder, http.StatusAccepted, &syncResponse)
	if !syncResponse.Deduplicated || syncResponse.OperationID != operation.ID {
		t.Errorf("sync operation = %#v, want deduplicated %q", syncResponse, operation.ID)
	}
}

func TestGitHandlerOperationEndpointsRequireBaseAndDoNotLeakSafeErrorContext(t *testing.T) {
	configured := &gitOperationConfigurerFake{
		initializeResponse: model.GitConfigResponse{Base: model.Base{Name: "work"}},
		initializeSnapshot: gitcmd.ConfiguredBase{Name: "work", Path: "/notes", URL: "https://example.test/notes.git", Branch: "main"},
	}
	operations := &gitOperationsFake{events: &configured.events, initializeErr: fmt.Errorf("push https://user:secret@example.test/private.git: %w", &gitcmd.SafeError{Code: gitcmd.CodePushRejected, Message: "Git push was rejected"})}
	handler := NewGitHandlerWithOperations(&gitProberFake{}, configured, &gitStatusReaderFake{}, operations)

	missingConfigure := httptest.NewRecorder()
	handler.Configure(missingConfigure, httptest.NewRequest(http.MethodPut, "/api/git/config", strings.NewReader(`{"git_url":"https://example.test/notes.git","git_branch":"main"}`)))
	assertMissingField(t, missingConfigure, "base")
	missingSync := httptest.NewRecorder()
	handler.Sync(missingSync, httptest.NewRequest(http.MethodPost, "/api/git/sync", nil))
	assertMissingField(t, missingSync, "base")

	recorder := httptest.NewRecorder()
	handler.Configure(recorder, httptest.NewRequest(http.MethodPut, "/api/git/config?base=work", strings.NewReader(`{"git_url":"https://example.test/notes.git","git_branch":"main"}`)))
	assertAPIErrorResponse(t, recorder, http.StatusConflict, model.APIError{Code: "push_rejected", Message: "Git push was rejected"})
	for _, private := range []string{"user:secret", "example.test", "private.git"} {
		if strings.Contains(recorder.Body.String(), private) {
			t.Errorf("response leaks private queue error %q: %q", private, recorder.Body.String())
		}
	}
}

func TestGitHandlerManualSyncConcurrentRequestsShareOperationsByBase(t *testing.T) {
	first := gitcmd.ConfiguredBase{Name: "first", Path: "/notes/first", URL: "https://example.test/first.git", Branch: "main"}
	second := gitcmd.ConfiguredBase{Name: "second", Path: "/notes/second", URL: "https://example.test/second.git", Branch: "main"}
	operations := &concurrentGitOperations{
		operations: map[string]gitcmd.Operation{
			first.Path:  {ID: "11111111111111111111111111111111", State: gitcmd.OperationQueued},
			second.Path: {ID: "22222222222222222222222222222222", State: gitcmd.OperationQueued},
		},
		calls: make(map[string]int),
	}
	handler := NewGitHandlerWithOperations(
		&gitProberFake{},
		concurrentGitOperationConfigurer{snapshots: map[string]gitcmd.ConfiguredBase{"first": first, "second": second}},
		&gitStatusReaderFake{},
		operations,
	)

	type response struct {
		base string
		body model.GitOperationResponse
		err  error
	}
	responses := make(chan response, 3)
	var group sync.WaitGroup
	for _, base := range []string{"first", "first", "second"} {
		group.Add(1)
		go func(base string) {
			defer group.Done()
			recorder := httptest.NewRecorder()
			handler.Sync(recorder, httptest.NewRequest(http.MethodPost, "/api/git/sync?base="+base, nil))
			if recorder.Code != http.StatusAccepted {
				responses <- response{base: base, err: fmt.Errorf("status = %d: %s", recorder.Code, recorder.Body.String())}
				return
			}
			var body model.GitOperationResponse
			responses <- response{base: base, body: body, err: json.Unmarshal(recorder.Body.Bytes(), &body)}
		}(base)
	}
	group.Wait()
	close(responses)

	ids := make(map[string][]string)
	for result := range responses {
		if result.err != nil {
			t.Fatal(result.err)
		}
		ids[result.base] = append(ids[result.base], result.body.OperationID)
	}
	if !reflect.DeepEqual(ids["first"], []string{"11111111111111111111111111111111", "11111111111111111111111111111111"}) {
		t.Errorf("first IDs = %v, want one shared operation ID", ids["first"])
	}
	if !reflect.DeepEqual(ids["second"], []string{"22222222222222222222222222222222"}) {
		t.Errorf("second IDs = %v, want its own operation ID", ids["second"])
	}
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
