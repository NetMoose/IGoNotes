package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

// The helper is a native executable run by CommandRunner, with a real exit
// cause and private stderr. It does not manufacture a SafeError or use a shell.
func TestGitSecretAcceptanceExternalGit(t *testing.T) {
	if os.Getenv("IGONOTES_HTTP_SECRET_HELPER") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stderr, os.Getenv("IGONOTES_HTTP_SECRET_STDERR"))
	os.Exit(23)
}

type secretAcceptanceRunner func(context.Context, gitcmd.Command) (gitcmd.Result, error)

func (run secretAcceptanceRunner) Run(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
	return run(ctx, command)
}

// Failure output identifies the sink only, never its bytes or filesystem path.
func assertHTTPSecretFree(t *testing.T, sink string, data []byte, forbidden []string) {
	t.Helper()
	for _, value := range forbidden {
		if bytes.Contains(data, []byte(value)) {
			t.Fatalf("private data reached %s", sink)
		}
	}
}

func TestGitSecretAcceptanceRealHTTPResponses(t *testing.T) {
	f := newGitConflictRESTFixture(t)
	if err := f.manager.Close(); err != nil {
		t.Fatal("cannot close initial fixture manager")
	}
	ctx := context.Background()
	secret := strings.Repeat("IGONOTES_SECRET_32", 7)
	unsafe := "https://user:" + secret + "@private-git.example.invalid/repo.git?token=" + secret
	basic := base64.StdEncoding.EncodeToString([]byte("user:" + secret))
	private := "IGONOTES_HTTP_PRIVATE_CAUSE_7d972c"
	forbidden := []string{secret, unsafe, basic, "private-git.example.invalid", private, "exit status 23"}
	delegate := gitcmd.NewCommandRunner()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("cannot locate native helper executable")
	}
	program, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal("cannot read native helper executable")
	}
	bin := t.TempDir()
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), program, 0o700); err != nil {
		t.Fatal("cannot create native external Git fixture")
	}
	path := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+path)
	t.Setenv("IGONOTES_HTTP_SECRET_HELPER", "1")
	t.Setenv("IGONOTES_HTTP_SECRET_STDERR", "fatal: Authentication failed for "+unsafe+"\ntoken="+secret+"\nAuthorization: Basic "+basic+"\n"+private+"\n")
	_, externalErr := delegate.Run(ctx, gitcmd.Command{Dir: f.local,
		Args:    []string{"-test.run=^TestGitSecretAcceptanceExternalGit$", "--"},
		Secrets: []string{unsafe, secret, basic}})
	var safe *gitcmd.SafeError
	if !errors.As(externalErr, &safe) || safe.Code != gitcmd.CodeAuthentication ||
		!strings.Contains(safe.Diagnostic(), private) || safe.ExitCode != 23 {
		t.Fatal("native diagnostic did not exercise private stderr and exit cause")
	}
	// An adapter may attach a private transport cause. Keep that cause visibly
	// poisonous so formatting the whole error instead of selecting the public
	// SafeError would fail the actual HTTP sink assertions below.
	externalErr = errors.Join(externalErr, errors.New(private+" "+unsafe+" Authorization: Basic "+basic))
	if !strings.Contains(externalErr.Error(), secret) || !strings.Contains(externalErr.Error(), private) {
		t.Fatal("private adapter cause was not exercised")
	}
	t.Setenv("PATH", path)
	t.Setenv("IGONOTES_HTTP_SECRET_HELPER", "")
	var calls, failedNetworks atomic.Int32
	var failNetwork, failInspection, failValidation atomic.Bool
	runner := secretAcceptanceRunner(func(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
		calls.Add(1)
		if command.Scope == gitcmd.NetworkOperation && failNetwork.Load() {
			failedNetworks.Add(1)
			return gitcmd.Result{}, externalErr
		}
		if failInspection.Load() && slices.Contains(command.Args, "ls-files") {
			return gitcmd.Result{}, externalErr
		}
		if failValidation.Load() && slices.Contains(command.Args, "check-ref-format") {
			return gitcmd.Result{}, externalErr
		}
		return delegate.Run(ctx, command)
	})
	client := gitcmd.NewClient(runner)
	configPath := filepath.Join(t.TempDir(), "config.json")
	store := service.NewConfigService(configPath)
	completed := true
	if err := store.Save(&model.Config{SetupCompleted: &completed, CurrentBase: "work", Bases: []model.Base{{
		Name: "work", Path: f.local, GitURL: f.remote, GitBranch: "main", AutoSyncIntervalMinutes: 15,
		GitCommitMessageTemplate: service.DefaultGitCommitMessageTemplate,
	}}}); err != nil {
		t.Fatal("cannot seed real configuration")
	}
	settings, err := service.NewSettingsServiceWithGit(store, f.notes, f.coordinator, "", log.New(io.Discard, "", 0), service.NewGitConfigValidator(client), f.statuses)
	if err != nil {
		t.Fatal("cannot construct real settings")
	}
	f.base, _, err = settings.GitSnapshot("work")
	if err != nil {
		t.Fatal("cannot snapshot configured base")
	}
	head := f.git(t, f.local, "rev-parse", "HEAD")
	f.git(t, f.local, "update-ref", "refs/igonotes/remotes/main", head)
	seed := gitcmd.Operation{ID: strings.Repeat("b", 32), BaseName: "work", RepoPath: f.local, Branch: "main",
		ConfigFingerprint: f.base.Fingerprint, RemoteFingerprint: f.base.RemoteFingerprint,
		Kind: gitcmd.OperationInitialize, State: gitcmd.OperationQueued, Stage: gitcmd.StageQueued,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := f.operations.CreateQueued(ctx, seed); err != nil {
		t.Fatal("cannot seed durable trust")
	}
	seed.State, seed.Stage, seed.RemoteOID, seed.PushOID = gitcmd.OperationSucceeded, gitcmd.StageCompleted, head, head
	if err := f.operations.Finish(ctx, seed); err != nil {
		t.Fatal("cannot finish durable trust")
	}
	if err := f.statuses.Upsert(ctx, model.GitStatus{Base: "work", RepositoryPath: f.local, State: model.GitStateReady, RemoteOID: head}); err != nil {
		t.Fatal("cannot seed ready status")
	}
	prober := service.NewGitProbeService(settings, client)
	f.manager = service.NewGitManager(gitcmd.NewService(runner, client), f.statuses, f.operations, prober, settings.GitSnapshot, f.notes, f.coordinator)
	if err := f.manager.Start(); err != nil {
		t.Fatal("cannot start real manager")
	}
	register := func(state SetupState) *http.ServeMux {
		mux := http.NewServeMux()
		RegisterGitRoutes(mux, NewGitHandlerWithOperations(prober, settings, service.NewGitStatusService(settings, f.statuses), f.manager), state)
		RegisterGitConflictRoutes(mux, NewGitConflictHandler(f.manager), state)
		return mux
	}
	mux := register(settings)
	request := func(sink, method, target string, payload any, origin string, handler http.Handler, want int) *httptest.ResponseRecorder {
		t.Helper()
		var body io.Reader
		if payload != nil {
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatal("cannot encode request payload")
			}
			body = bytes.NewReader(data)
		}
		req := newLocalRouterRequest(method, target, body)
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req.WithContext(requestCtx))
		assertHTTPSecretFree(t, sink, recorder.Body.Bytes(), forbidden)
		for key, values := range recorder.Header() {
			assertHTTPSecretFree(t, sink, []byte(key+strings.Join(values, "\n")), forbidden)
		}
		if recorder.Code != want || recorder.Header().Get("Content-Type") != "application/json" || !json.Valid(recorder.Body.Bytes()) {
			t.Fatalf("unexpected HTTP response at %s: status=%d", sink, recorder.Code)
		}
		return recorder
	}
	decode := func(recorder *httptest.ResponseRecorder, value any) {
		t.Helper()
		if err := json.Unmarshal(recorder.Body.Bytes(), value); err != nil {
			t.Fatal("cannot decode route response")
		}
	}
	local := "http://localhost:8080"
	unsafeProbe := model.GitProbeRequest{Base: "work", GitURL: unsafe, GitBranch: "main"}
	unsafeConfig := model.GitConfigRequest{GitURL: unsafe, GitBranch: "main"}
	// Guard checks precede service execution for every requested route.
	guarded := register(&gitRouteSetupState{completed: false})
	for _, route := range []struct {
		method, target string
		payload        any
	}{
		{http.MethodPost, "/api/git/probe", unsafeProbe},
		{http.MethodPut, "/api/git/config?base=work", unsafeConfig},
		{http.MethodPost, "/api/git/sync?base=work", nil},
		{http.MethodGet, "/api/git/status?base=work", nil},
		{http.MethodPost, "/api/git/resume?base=work", nil},
		{http.MethodGet, "/api/git/conflicts?base=work", nil},
	} {
		before := calls.Load()
		var api model.APIError
		decode(request("origin guard", route.method, route.target, route.payload, "https://cross-origin.example.invalid", mux, http.StatusForbidden), &api)
		if api.Code != "forbidden_origin" {
			t.Fatal("origin guard did not reject route")
		}
		decode(request("setup guard", route.method, route.target, route.payload, local, guarded, http.StatusPreconditionRequired), &api)
		if api.Code != "setup_required" || calls.Load() != before {
			t.Fatal("guarded route reached Git execution")
		}
	}
	before := calls.Load()
	var api model.APIError
	decode(request("probe validation", http.MethodPost, "/api/git/probe", unsafeProbe, local, mux, http.StatusUnprocessableEntity), &api)
	if api.Code != "invalid_git_url" {
		t.Fatal("probe did not reject credential-bearing candidate")
	}
	decode(request("config validation", http.MethodPut, "/api/git/config?base=work", unsafeConfig, local, mux, http.StatusUnprocessableEntity), &api)
	if api.Code != "invalid_git_url" || calls.Load() != before {
		t.Fatal("unsafe configuration reached Git execution")
	}
	var rows int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM git_operations").Scan(&rows); err != nil || rows != 1 {
		t.Fatal("unsafe request reached durable queue")
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal("cannot read config sink")
	}
	assertHTTPSecretFree(t, "config file", configBytes, forbidden)
	failValidation.Store(true)
	decode(request("config private validation failure", http.MethodPut, "/api/git/config?base=work", model.GitConfigRequest{GitURL: f.remote, GitBranch: "main"}, local, mux, http.StatusUnauthorized), &api)
	if api.Code != string(gitcmd.CodeAuthentication) || api.Message != "Git authentication failed" {
		t.Fatal("config route exposed private validation failure")
	}
	failValidation.Store(false)
	waitOperation := func(id string, state gitcmd.OperationState) {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			op, found, err := f.operations.ByID(ctx, id)
			status, _, statusErr := f.statuses.Get(ctx, f.local)
			if err != nil || statusErr != nil {
				t.Fatal("cannot read operation publication")
			}
			if found && op.State == state && status.OperationID == id && status.State != model.GitStateInitializing && status.State != model.GitStateSyncing {
				return
			}
			select {
			case <-ticker.C:
			case <-deadline.C:
				t.Fatal("operation publication timed out")
			}
		}
	}
	var configured model.GitConfigResponse
	decode(request("config accepted", http.MethodPut, "/api/git/config?base=work", model.GitConfigRequest{GitURL: f.remote, GitBranch: "main", AutoSyncIntervalMinutes: 15, GitCommitMessageTemplate: service.DefaultGitCommitMessageTemplate}, local, mux, http.StatusAccepted), &configured)
	if configured.Operation == nil || configured.Operation.OperationID == "" || configured.Base.GitURL != f.remote {
		t.Fatal("config route did not enqueue real initialization")
	}
	waitOperation(configured.Operation.OperationID, gitcmd.OperationSucceeded)
	failNetwork.Store(true)
	var probe model.GitProbeResponse
	decode(request("probe external failure", http.MethodPost, "/api/git/probe", model.GitProbeRequest{Base: "work", GitURL: f.remote, GitBranch: "main"}, local, mux, http.StatusOK), &probe)
	if probe.BlockingError == nil || probe.BlockingError.Code != string(gitcmd.CodeAuthentication) || probe.BlockingError.Message != "Git authentication failed" {
		t.Fatal("probe route did not publish safe external failure")
	}
	for i := 1; i <= 5; i++ {
		var operation model.GitOperationResponse
		decode(request("sync accepted", http.MethodPost, "/api/git/sync?base=work", nil, local, mux, http.StatusAccepted), &operation)
		if operation.OperationID == "" || operation.Deduplicated {
			t.Fatal("sync route did not admit distinct operation")
		}
		waitOperation(operation.OperationID, gitcmd.OperationFailed)
		var statuses model.GitStatusResponse
		decode(request("status persisted error", http.MethodGet, "/api/git/status?base=work", nil, local, mux, http.StatusOK), &statuses)
		if len(statuses.Statuses) != 1 {
			t.Fatal("status route omitted base")
		}
		status := statuses.Statuses[0]
		if status.OperationID != operation.OperationID || status.ConsecutiveFailures != i || status.Error == nil || status.Error.Code != string(gitcmd.CodeAuthentication) || status.Error.Message != "Git authentication failed" {
			t.Fatal("status route lost safe persisted failure")
		}
		if i == 5 && status.State != model.GitStatePaused {
			t.Fatal("fifth failed sync did not pause")
		}
	}
	decode(request("sync paused", http.MethodPost, "/api/git/sync?base=work", nil, local, mux, http.StatusConflict), &api)
	if api.Code != string(gitcmd.CodePaused) {
		t.Fatal("paused sync route returned wrong error")
	}
	var resumed model.GitOperationResponse
	decode(request("resume accepted", http.MethodPost, "/api/git/resume?base=work", nil, local, mux, http.StatusAccepted), &resumed)
	if resumed.OperationID == "" || resumed.Deduplicated {
		t.Fatal("resume route did not enqueue work")
	}
	waitOperation(resumed.OperationID, gitcmd.OperationFailed)
	var statuses model.GitStatusResponse
	decode(request("status after resume", http.MethodGet, "/api/git/status", nil, local, mux, http.StatusOK), &statuses)
	if len(statuses.Statuses) != 1 || statuses.Statuses[0].ConsecutiveFailures != 1 || failedNetworks.Load() < 7 {
		t.Fatal("resume did not reset budget and execute real failure path")
	}
	failNetwork.Store(false)
	f.createTwoConflicts(t)
	var conflicts model.GitConflictListResponse
	decode(request("conflicts success", http.MethodGet, "/api/git/conflicts?base=work", nil, local, mux, http.StatusOK), &conflicts)
	if conflicts.OperationID != strings.Repeat("a", 32) || len(conflicts.Conflicts) != 2 || conflicts.CanComplete {
		t.Fatal("conflict route did not return real conflict identity")
	}
	for _, conflict := range conflicts.Conflicts {
		if conflict.ID == "" || conflict.Base == nil || conflict.Local == nil || conflict.Remote == nil || conflict.Local.Content == nil || conflict.Remote.Content == nil {
			t.Fatal("conflict route omitted real stage previews")
		}
	}
	failInspection.Store(true)
	decode(request("conflicts private inspection failure", http.MethodGet, "/api/git/conflicts?base=work", nil, local, mux, http.StatusConflict), &api)
	if api.Code != string(gitcmd.CodeRecoveryRequired) {
		t.Fatal("inspection route did not sanitize private Git failure")
	}
	failInspection.Store(false)
	for _, target := range []string{"/api/git/sync?base=work", "/api/git/resume?base=work"} {
		decode(request("conflict mutation gate", http.MethodPost, target, nil, local, mux, http.StatusConflict), &api)
		if api.Code != "git_conflict_pending" {
			t.Fatal("route bypassed conflict mutation gate")
		}
	}
}
