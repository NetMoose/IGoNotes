package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
)

func TestGitRecoveryCompletesBeforeInitialIndexAndServe(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	runServerSource := sourceFunction(t, string(source), "func runServer(", "func runMain(")

	ordered := []string{
		"gitOperations := repository.NewGitOperationRepository(db)",
		"gitService := gitcmd.NewService(gitRunner, gitClient)",
		"gitManager := service.NewGitManagerWithAutosync(",
		"gitManager.RecoverLocal(ctx, configuredSnapshots)",
		"gitManager.Start()",
		"stopGitClose := watchGitShutdown(ctx, gitManager)",
		"go func() {",
		"noteService.SyncFS()",
		"router := handlers.NewRouter(noteHandler, settingsHandler, settingsService, spaHandler)",
		"return serveLocal(ctx, address, newHTTPServer(router)",
	}
	remaining := runServerSource
	for _, snippet := range ordered {
		index := strings.Index(remaining, snippet)
		if index < 0 {
			t.Fatalf("runServer does not contain %q in startup order", snippet)
		}
		remaining = remaining[index+len(snippet):]
	}
}

func TestGitManagerClosesDuringShutdown(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	runServerSource := sourceFunction(t, string(source), "func runServer(", "func runMain(")
	manager := strings.Index(runServerSource, "gitManager := service.NewGitManagerWithAutosync(")
	deferClose := strings.Index(runServerSource, "gitManager.Close()")
	databaseClose := strings.Index(runServerSource, "db.Close()")
	if manager < 0 || deferClose < manager || databaseClose < 0 || deferClose < databaseClose {
		t.Fatalf("Git manager shutdown ordering is missing or closes after DB: manager=%d close=%d db=%d", manager, deferClose, databaseClose)
	}
}

type runtimeGitRunner func(context.Context, gitcmd.Command) (gitcmd.Result, error)

func (r runtimeGitRunner) Run(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
	return r(ctx, command)
}

func runtimeGitCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func runtimeGitBase(t *testing.T, name string) model.Base {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "Runtime Test"}, {"config", "user.email", "runtime@example.invalid"}, {"commit", "--allow-empty", "-m", "initial"}, {"remote", "add", "origin", "https://example.invalid/notes.git"}} {
		runtimeGitCommand(t, root, args...)
	}
	return model.Base{Name: name, Path: root, GitURL: "https://example.invalid/notes.git", GitBranch: "main", AutoSync: true, AutoSyncIntervalMinutes: 5, GitCommitMessageTemplate: "notes {datetime}"}
}

func runtimeGitFixture(t *testing.T, network runtimeGitRunner, configured bool, extraBases ...model.Base) (*service.GitManager, *service.NoteService, func() error, *repository.GitStatusRepository, []gitcmd.ConfiguredBase) {
	t.Helper()
	root := t.TempDir()
	base := model.Base{Name: "work", Path: root}
	if configured {
		base = runtimeGitBase(t, "work")
		root = base.Path
	}
	db, err := repository.InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Match the manager integration fixtures: serialize SQLite connections so
	// these lifecycle tests do not depend on concurrent-reader busy handling.
	db.SetMaxOpenConns(1)
	coordinator := service.NewBaseOperationCoordinator()
	notes := service.NewNoteService(repository.NewNoteRepository(db), root, coordinator)
	completed := true
	store := &gitWiringConfigStore{config: model.Config{Bases: append([]model.Base{base}, extraBases...), CurrentBase: base.Name, SetupCompleted: &completed}}
	delegate := gitcmd.NewCommandRunner()
	runner := runtimeGitRunner(func(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
		if command.Scope == gitcmd.NetworkOperation {
			return network(ctx, command)
		}
		if !configured {
			t.Error("unconfigured runtime executed Git")
		}
		return delegate.Run(ctx, command)
	})
	client := gitcmd.NewClient(runner)
	statuses := repository.NewGitStatusRepository(db)
	settings, err := service.NewSettingsServiceWithGit(store, notes, coordinator, "", log.Default(), service.NewGitConfigValidator(client), statuses)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := configuredGitSnapshots(settings)
	if err != nil {
		t.Fatal(err)
	}
	operations := repository.NewGitOperationRepository(db)
	for i, snapshot := range snapshots {
		oid := runtimeGitCommand(t, snapshot.Path, "rev-parse", "HEAD")
		runtimeGitCommand(t, snapshot.Path, "update-ref", "refs/igonotes/remotes/main", oid)
		now := time.Now().UTC()
		op := gitcmd.Operation{ID: fmt.Sprintf("%032x", i+1), BaseName: snapshot.Name, RepoPath: snapshot.Path, ConfigFingerprint: snapshot.Fingerprint, RemoteFingerprint: snapshot.RemoteFingerprint, Kind: gitcmd.OperationInitialize, State: gitcmd.OperationQueued, Stage: gitcmd.StageQueued, Branch: snapshot.Branch, CreatedAt: now, UpdatedAt: now}
		if err := operations.CreateQueued(context.Background(), op); err != nil {
			t.Fatal(err)
		}
		op.State, op.Stage, op.RemoteOID, op.PushOID = gitcmd.OperationSucceeded, gitcmd.StageCompleted, oid, oid
		if err := operations.Finish(context.Background(), op); err != nil {
			t.Fatal(err)
		}
		if err := statuses.Upsert(context.Background(), model.GitStatus{Base: snapshot.Name, RepositoryPath: snapshot.Path, State: model.GitStateReady, RemoteOID: oid, ConsecutiveFailures: 3}); err != nil {
			t.Fatal(err)
		}
	}
	manager := service.NewGitManagerWithAutosync(gitcmd.NewService(runner, client), statuses, operations, service.NewGitProbeService(settings, client), settings.GitSnapshot, notes, coordinator, gitSnapshotSource(settings.GitSnapshots), settings.GitConfigChanges(), log.Default())
	t.Cleanup(func() { _ = manager.Close(); _ = notes.Close(); _ = db.Close() })
	return manager, notes, db.Close, statuses, snapshots
}

func awaitRuntime(t *testing.T, channel <-chan struct{}, boundary string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out at %s", boundary)
	}
}

func TestGitShutdownCancelsBeforeHTTPDrainAndWaitsBeforeDependencies(t *testing.T) {
	entered, canceled, releaseGit := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var networkCalls atomic.Int32
	manager, notes, closeDB, statuses, snapshots := runtimeGitFixture(t, func(ctx context.Context, _ gitcmd.Command) (gitcmd.Result, error) {
		if networkCalls.Add(1) == 1 {
			close(entered)
		}
		<-ctx.Done()
		close(canceled)
		<-releaseGit
		return gitcmd.Result{}, context.Canceled
	}, true, runtimeGitBase(t, "queued"))
	heads := make(map[string]string, len(snapshots))
	for _, snapshot := range snapshots {
		heads[snapshot.Path] = runtimeGitCommand(t, snapshot.Path, "rev-parse", "HEAD")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drain, releaseHTTP, dependenciesClosed, runtimeDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	httpReturned := make(chan struct{})
	defer func() {
		cancel()
		select {
		case <-releaseGit:
		default:
			close(releaseGit)
		}
		select {
		case <-releaseHTTP:
		default:
			close(releaseHTTP)
		}
		awaitRuntime(t, runtimeDone, "cleanup after deferred-boundary test")
	}()
	go func() {
		defer close(runtimeDone)
		defer func() {
			if err := closeDB(); err != nil {
				t.Error(err)
			}
			close(dependenciesClosed)
		}()
		defer func() {
			status, found, err := statuses.Get(context.Background(), snapshots[0].Path)
			if err != nil || !found || status.State != model.GitStateError || status.ConsecutiveFailures != 3 || status.Error == nil || status.Error.Code != string(gitcmd.CodeOperationInterrupted) {
				t.Errorf("worker must publish interruption before dependencies close: status=%+v found=%v err=%v", status, found, err)
			}
			if err := notes.SyncFS(); err != nil {
				t.Errorf("notes unavailable at manager completion: %v", err)
			}
			queued, found, err := statuses.Get(context.Background(), snapshots[1].Path)
			if err != nil || !found || queued.State != model.GitStateSyncing || queued.LastAttempt != nil || queued.ConsecutiveFailures != 3 {
				t.Errorf("queued sync started during shutdown: %+v found=%v err=%v", queued, found, err)
			}
			if err := notes.Close(); err != nil {
				t.Error(err)
			}
		}()
		defer func() {
			if err := manager.Close(); err != nil {
				t.Error(err)
			}
		}()
		if err := manager.RecoverLocal(ctx, snapshots); err != nil {
			t.Error(err)
			return
		}
		for _, snapshot := range snapshots {
			if _, _, err := manager.QueueSync(ctx, gitcmd.SyncRequest{Snapshot: snapshot}); err != nil {
				t.Error(err)
				return
			}
		}
		if err := manager.Start(); err != nil {
			t.Error(err)
			return
		}
		stop := watchGitShutdown(ctx, manager)
		defer stop()
		server := &fakeHTTPServerLifecycle{
			serve:    func(net.Listener) error { <-releaseHTTP; return http.ErrServerClosed },
			shutdown: func(context.Context) error { close(drain); <-releaseHTTP; return nil },
			close:    func() error { return nil },
		}
		if err := serveLocal(ctx, "127.0.0.1:0", server, func() {}, time.Minute); err != nil {
			t.Error(err)
		}
		close(httpReturned)
	}()
	awaitRuntime(t, entered, "running network command")
	cancel()
	awaitRuntime(t, drain, "pending HTTP drain")
	awaitRuntime(t, canceled, "Git cancellation during HTTP drain")
	close(releaseHTTP)
	awaitRuntime(t, httpReturned, "HTTP returned while Git still blocked")
	select {
	case <-dependenciesClosed:
		t.Fatal("dependencies closed before Git worker returned")
	default:
	}
	close(releaseGit)
	awaitRuntime(t, runtimeDone, "runtime deferred cleanup")
	if networkCalls.Load() != 1 {
		t.Fatalf("network calls = %d, want 1 (no final sync)", networkCalls.Load())
	}
	for _, snapshot := range snapshots {
		if got := runtimeGitCommand(t, snapshot.Path, "rev-parse", "HEAD"); got != heads[snapshot.Path] {
			t.Fatalf("shutdown created a hidden commit for %s: %s != %s", snapshot.Name, got, heads[snapshot.Path])
		}
	}
	if _, _, err := manager.QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: snapshots[0]}); !errors.Is(err, service.ErrGitManagerClosed) {
		t.Fatalf("closed manager admission: %v", err)
	}
}

func TestGitAutosyncRecoversInactiveBasesAndStaggersInConfigOrder(t *testing.T) {
	inactive := runtimeGitBase(t, "inactive")
	manual := runtimeGitBase(t, "manual")
	manual.AutoSync = false
	type attempt struct {
		path string
		at   time.Time
	}
	attempts := make(chan attempt, 4)
	manager, notes, _, statuses, snapshots := runtimeGitFixture(t, func(_ context.Context, command gitcmd.Command) (gitcmd.Result, error) {
		attempts <- attempt{path: command.Dir, at: time.Now()}
		return gitcmd.Result{}, &gitcmd.SafeError{Code: gitcmd.CodeRemoteUnreachable, Message: "Git remote is unreachable"}
	}, true, manual, inactive)
	// A stale public row must be locally reconciled even for inactive/manual bases.
	for _, snapshot := range snapshots {
		status, _, err := statuses.Get(context.Background(), snapshot.Path)
		if err != nil {
			t.Fatal(err)
		}
		status.State = model.GitStateSyncing
		if err := statuses.Upsert(context.Background(), status); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.RecoverLocal(context.Background(), snapshots); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range snapshots {
		status, found, err := statuses.Get(context.Background(), snapshot.Path)
		if err != nil || !found || status.State != model.GitStateReady {
			t.Fatalf("unrecovered base %s: %+v found=%v err=%v", snapshot.Name, status, found, err)
		}
	}
	select {
	case got := <-attempts:
		t.Fatalf("network before manager start: %+v", got)
	default:
	}
	started := time.Now()
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := watchGitShutdown(ctx, manager)
	defer stop()
	if err := notes.SyncFS(); err != nil {
		t.Fatal(err)
	}
	for i, path := range []string{snapshots[0].Path, inactive.Path} {
		select {
		case got := <-attempts:
			if got.path != path || got.at.Sub(started) < time.Duration(i+1)*5*time.Second {
				t.Fatalf("startup slot %d: %+v elapsed=%s, want %s after %s", i+1, got, got.at.Sub(started), path, time.Duration(i+1)*5*time.Second)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("autosync did not reach startup slot")
		}
	}
	cancel()
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-attempts:
		t.Fatalf("unexpected manual/final sync: %+v", got)
	default:
	}
}

func TestGitUnconfiguredRuntimeStartsAndStopsWithoutGit(t *testing.T) {
	var calls atomic.Int32
	manager, _, _, _, snapshots := runtimeGitFixture(t, func(context.Context, gitcmd.Command) (gitcmd.Result, error) {
		calls.Add(1)
		return gitcmd.Result{}, errors.New("unexpected Git")
	}, false)
	if len(snapshots) != 0 {
		t.Fatalf("unconfigured snapshots = %v", snapshots)
	}
	if err := manager.RecoverLocal(context.Background(), snapshots); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := watchGitShutdown(ctx, manager)
	defer stop()
	cancel()
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("Git calls = %d", calls.Load())
	}
}

func TestGitShutdownLifecycleErrorsJoinWithoutDiagnostics(t *testing.T) {
	first := errors.New("https://user:password@example.invalid/private.git diagnostic")
	second := context.Canceled
	err := errors.Join(gitLifecycleError("Git recovery failed", first), gitLifecycleError("Git shutdown failed", second))
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("lost lifecycle causes: %v", err)
	}
	if err.Error() != "Git recovery failed\nGit shutdown failed" {
		t.Fatalf("unsafe lifecycle error: %v", err)
	}
}

func TestGitRecoveryRejectsIncompleteSnapshotsInsteadOfSkippingBase(t *testing.T) {
	completed := true
	store := &gitWiringConfigStore{config: model.Config{SetupCompleted: &completed, Bases: []model.Base{{Name: "missing", Path: filepath.Join(t.TempDir(), "missing"), GitURL: "https://example.invalid/repo.git", GitBranch: "main"}}}}
	coordinator := service.NewBaseOperationCoordinator()
	notes := service.NewNoteService(gitWiringNoteRepository{}, "", coordinator)
	defer notes.Close()
	settings, err := service.NewSettingsService(store, notes, coordinator, "", log.Default())
	if err != nil {
		t.Fatal(err)
	}
	if snapshots, err := configuredGitSnapshots(settings); err == nil || snapshots != nil {
		t.Fatalf("incomplete recovery snapshot silently accepted: snapshots=%v err=%v", snapshots, err)
	}
}

func TestGitRecoveryUsesNoNetworkCommand(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	runServerSource := sourceFunction(t, string(source), "func runServer(", "func runMain(")
	recovery := strings.Index(runServerSource, "gitManager.RecoverLocal(")
	if recovery < 0 {
		t.Fatal("runServer does not recover Git repositories")
	}
	if strings.Contains(runServerSource[:recovery], ".Sync(") || strings.Contains(runServerSource[:recovery], ".Initialize(") {
		t.Fatal("startup runs network Git work before local recovery")
	}
}

func TestConflictRecoveryReusesLocalRecoveryBeforeWorkerAndInitialIndex(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	runServerSource := sourceFunction(t, string(source), "func runServer(", "func runMain(")

	if got := strings.Count(runServerSource, "gitManager.RecoverLocal("); got != 1 {
		t.Fatalf("RecoverLocal calls = %d, want exactly 1", got)
	}
	for _, forbidden := range []string{".Sync(", ".Initialize(", ".Probe("} {
		if strings.Contains(runServerSource[:strings.Index(runServerSource, "gitManager.RecoverLocal(")], forbidden) {
			t.Fatalf("startup runs network Git work before local recovery: %s", forbidden)
		}
	}
	for _, required := range []string{
		"gitManager.RecoverLocal(ctx, configuredSnapshots)",
		"gitManager.Start()",
		"noteService.SyncFS()",
		"gitConflictHandler := handlers.NewGitConflictHandler(gitManager)",
		"handlers.RegisterGitConflictRoutes(router, gitConflictHandler, settingsService)",
	} {
		if strings.Index(runServerSource, required) < 0 {
			t.Fatalf("runServer does not contain %q", required)
		}
	}
}
