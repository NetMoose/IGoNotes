package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	"IGoNotes/internal/repository"
)

// All asynchronous boundaries have a buffered result and a five-second bound.
func hardeningReceive[T any](t *testing.T, sink string, results <-chan T) T {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout at %s", sink)
		var zero T
		return zero
	}
}

func hardeningIdle(t *testing.T, manager *GitManager) {
	t.Helper()
	done := make(chan struct{}, 1)
	if err := manager.enqueueSynchronous(func() { done <- struct{}{} }); err != nil {
		t.Fatal("cannot queue worker fence")
	}
	hardeningReceive(t, "worker fence", done)
}

type hardeningBarrier struct {
	stage   string
	release chan struct{}
}

func TestGitManagerRaceManualScheduledResumeSwitchSaveAndClose(t *testing.T) {
	ctx := context.Background()
	root, remote, oid := readyManagerGitPair(t)
	peer := t.TempDir()
	runManagerGit(t, peer, "clone", "--branch", "main", "--", remote, ".")
	runManagerGit(t, peer, "config", "user.name", "Race Test")
	runManagerGit(t, peer, "config", "user.email", "race@example.invalid")
	base := configuredManagerBase("work", root)
	base.URL, base.AutoSync, base.IntervalMinutes = remote, true, 5
	entered := make(chan hardeningBarrier, 3)
	var current, maximum, networks atomic.Int32
	delegate := gitcmd.NewCommandRunner()
	runner := managerRunnerFunc(func(ctx context.Context, c gitcmd.Command) (gitcmd.Result, error) {
		n := current.Add(1)
		defer current.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		stage := ""
		for _, name := range []string{"fetch", "add", "push"} {
			if slices.Contains(c.Args, name) {
				stage = name
			}
		}
		if c.Scope == gitcmd.NetworkOperation {
			networks.Add(1)
		}
		if stage != "" {
			b := hardeningBarrier{stage: stage, release: make(chan struct{}, 1)}
			select {
			case entered <- b:
			case <-ctx.Done():
				return gitcmd.Result{}, ctx.Err()
			}
			select {
			case <-b.release:
			case <-ctx.Done():
				return gitcmd.Result{}, ctx.Err()
			}
		}
		return delegate.Run(ctx, c)
	})
	client := gitcmd.NewClient(runner)
	f := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, base.Name, runner, client, client)
	seedManagerTrust(t, f, base, oid)
	status, _, err := f.statuses.Get(ctx, root)
	if err != nil {
		t.Fatal("cannot read initial failure budget")
	}
	status.ConsecutiveFailures = 3
	if err := f.statuses.Upsert(ctx, status); err != nil {
		t.Fatal("cannot seed initial failure budget")
	}
	clock := newFakeGitSchedulerClock(schedulerTestNow())
	enableManagerAutosyncFixture(t, f, clock)
	f.manager.scheduler.startedAt = clock.Now()
	if err := f.manager.scheduler.reconcile(ctx, true); err != nil {
		t.Fatal("cannot reconcile scheduler")
	}
	clock.Advance(5 * time.Second)
	if !f.manager.scheduler.queueDue(ctx, clock.Now()) {
		t.Fatal("scheduler stopped before admission")
	}
	scheduled, found, err := f.operations.ActiveByPath(ctx, root)
	if err != nil || !found {
		t.Fatal("scheduler did not admit durable work")
	}
	// Admission and Resume take the coordinator too; while a job is running
	// they wait for it. Race queued admissions before starting the worker.
	op, duplicate, err := f.manager.QueueSync(ctx, gitcmd.SyncRequest{Snapshot: base})
	if err != nil || !duplicate || op.ID != scheduled.ID {
		t.Fatal("manual admission did not deduplicate scheduled work")
	}
	for iteration := range 50 {
		if err := f.notes.SwitchBase(root); err != nil {
			t.Fatal("cannot bind active base")
		}
		before, err := f.notes.GetNote("note.md")
		if err != nil {
			t.Fatal("cannot read revision")
		}
		type admission struct {
			op        gitcmd.Operation
			duplicate bool
			err       error
		}
		admissions := make(chan admission, 2)
		poll := make(chan error, 1)
		tick := make(chan struct{}, 1)
		saved, switched := make(chan error, 1), make(chan error, 1)
		start := make(chan struct{})
		go func() {
			<-start
			op, d, e := f.manager.QueueSync(ctx, gitcmd.SyncRequest{Snapshot: base})
			admissions <- admission{op, d, e}
		}()
		go func() { <-start; op, d, e := f.manager.Resume(ctx, base.Name); admissions <- admission{op, d, e} }()
		go func() { <-start; _, _, e := f.statuses.Get(ctx, root); poll <- e }()
		go func() {
			<-start
			clock.Advance(5 * time.Minute)
			f.manager.scheduler.queueDue(ctx, clock.Now())
			tick <- struct{}{}
		}()
		go func() {
			<-start
			_, e := f.notes.SaveNote(model.SaveNoteRequest{ID: "note.md", Content: fmt.Sprintf("editor revision %d\n", iteration), ExpectedRevision: &before.Revision})
			saved <- e
		}()
		go func() { <-start; switched <- f.notes.SwitchBase(root) }()
		close(start)
		for range 2 {
			got := hardeningReceive(t, "same-path admission", admissions)
			if got.err != nil || !got.duplicate || got.op.ID != op.ID {
				t.Fatal("manual/resume lost same-path deduplication")
			}
		}
		if hardeningReceive(t, "status poll", poll) != nil {
			t.Fatal("status poll failed")
		}
		hardeningReceive(t, "scheduled tick", tick)
		if hardeningReceive(t, "queued revision save", saved) != nil || hardeningReceive(t, "queued switch", switched) != nil {
			t.Fatal("queued note mutation failed")
		}
	}
	before, err := f.notes.GetNote("note.md")
	if err != nil {
		t.Fatal("cannot capture pre-transaction revision")
	}
	// Make the next real merge fast-forward by committing the editor's queued
	// bytes first, then changing them from the peer. This isolates stale-save
	// checks from the dirty-snapshot conflict regression.
	runManagerGit(t, root, "add", "--all")
	runManagerGit(t, root, "commit", "-m", "queued editor revisions")
	runManagerGit(t, root, "push", "--no-verify", "origin", "main")
	runManagerGit(t, peer, "pull", "--ff-only")
	want := "incoming revision\n"
	if err := os.WriteFile(filepath.Join(peer, "note.md"), []byte(want), 0o600); err != nil {
		t.Fatal("cannot edit peer")
	}
	runManagerGit(t, peer, "add", "--all")
	runManagerGit(t, peer, "commit", "-m", "incoming revision")
	runManagerGit(t, peer, "push", "--no-verify", "origin", "main")
	if err := f.manager.Start(); err != nil {
		t.Fatal("cannot start worker")
	}
	fetch := hardeningReceive(t, "fetch barrier", entered)
	if fetch.stage != "fetch" {
		t.Fatal("unexpected fetch boundary")
	}
	fetch.release <- struct{}{}
	worktree := hardeningReceive(t, "worktree barrier", entered)
	if worktree.stage != "add" {
		t.Fatal("unexpected worktree boundary")
	}
	saved, switched := make(chan error, 1), make(chan error, 1)
	attempted := make(chan struct{}, 2)
	go func() {
		attempted <- struct{}{}
		_, e := f.notes.SaveNote(model.SaveNoteRequest{ID: "note.md", Content: "stale editor bytes\n", ExpectedRevision: &before.Revision})
		saved <- e
	}()
	go func() { attempted <- struct{}{}; switched <- f.notes.SwitchBase(root) }()
	for range 2 {
		hardeningReceive(t, "mutation callers", attempted)
	}
	select {
	case <-saved:
		t.Fatal("save escaped the worktree coordinator")
	default:
	}
	select {
	case <-switched:
		t.Fatal("switch escaped the worktree coordinator")
	default:
	}
	worktree.release <- struct{}{}
	push := hardeningReceive(t, "push barrier", entered)
	if push.stage != "push" {
		t.Fatal("unexpected push boundary")
	}
	// Both note callers remain behind the whole-operation coordinator until
	// Close cancels push; they must observe the reindexed incoming revision.
	var next atomic.Int32
	if err := f.manager.enqueueSynchronous(func() { next.Add(1) }); err != nil {
		t.Fatal("cannot queue shutdown sentinel")
	}
	closed := make(chan error, 1)
	go func() { closed <- f.manager.Close() }()
	if hardeningReceive(t, "Close cancellation", closed) != nil || next.Load() != 0 {
		t.Fatal("Close started queued work or failed")
	}
	if !errors.Is(hardeningReceive(t, "revision save", saved), ErrNoteChanged) {
		t.Fatal("stale save overwrote incoming bytes")
	}
	if hardeningReceive(t, "switch and reindex", switched) != nil {
		t.Fatal("switch failed after transaction")
	}
	got, err := f.notes.GetNote("note.md")
	if err != nil || got.Content != want {
		t.Fatal("note runtime lost incoming bytes")
	}
	clock.Advance(time.Hour)
	stored, found, err := f.operations.ByID(ctx, op.ID)
	if err != nil || !found || stored.State != gitcmd.OperationFailed || stored.Error == nil || stored.Error.Code != gitcmd.CodeOperationInterrupted {
		t.Fatal("Close did not persist interrupted network operation")
	}
	status, _, err = f.statuses.Get(ctx, root)
	if err != nil || status.ConsecutiveFailures != 3 || status.Error == nil || status.Error.Code != string(gitcmd.CodeOperationInterrupted) {
		t.Fatal("Close changed failure budget or lost cancellation")
	}
	remoteOID := runManagerGit(t, remote, "rev-parse", "refs/heads/main")
	if remoteOID != runManagerGit(t, peer, "rev-parse", "HEAD") {
		t.Fatal("Close performed an unexpected push")
	}
	beforeNetwork := networks.Load()
	hardeningRestart(t, f, runner, client)
	if err := f.manager.RecoverLocal(ctx, []gitcmd.ConfiguredBase{base}); err != nil {
		t.Fatal("cannot recover after Close")
	}
	if networks.Load() != beforeNetwork {
		t.Fatal("local recovery performed network work")
	}
	persisted, _, err := f.operations.ByID(ctx, op.ID)
	if err != nil || persisted.State != gitcmd.OperationFailed || persisted.Error == nil || persisted.Error.Code != gitcmd.CodeOperationInterrupted {
		t.Fatal("restart lost persisted interruption")
	}
	if maximum.Load() != 1 {
		t.Fatal("manager overlapped Git commands")
	}
}

func hardeningRestart(t *testing.T, f *gitManagerFixture, runner gitcmd.Runner, client *gitcmd.Client) {
	t.Helper()
	if err := f.manager.Close(); err != nil {
		t.Fatal("cannot close manager")
	}
	if err := f.notes.Close(); err != nil {
		t.Fatal("cannot close note runtime")
	}
	if err := f.db.Close(); err != nil {
		t.Fatal("cannot close SQLite")
	}
	db, err := repository.InitDB(f.dbPath)
	if err != nil {
		t.Fatal("cannot reopen SQLite")
	}
	db.SetMaxOpenConns(1)
	f.db, f.statuses, f.operations = db, repository.NewGitStatusRepository(db), repository.NewGitOperationRepository(db)
	f.coordinator = NewBaseOperationCoordinator()
	f.notes = NewNoteService(repository.NewNoteRepository(db), firstManagerPathMust(f), f.coordinator)
	f.manager = NewGitManager(gitcmd.NewService(runner, client), f.statuses, f.operations, f.manager.prober, f.snapshots.get, f.notes, f.coordinator)
	manager, notes := f.manager, f.notes
	t.Cleanup(func() { _ = manager.Close(); _ = notes.Close(); _ = db.Close() })
}

func firstManagerPathMust(f *gitManagerFixture) string {
	f.snapshots.mu.RLock()
	defer f.snapshots.mu.RUnlock()
	return f.snapshots.bases[f.snapshots.active].Path
}

func TestGitManagerRaceConflictResolvePollAndRestart(t *testing.T) {
	ctx := context.Background()
	root, remote, oid := readyManagerGitPair(t)
	peer := t.TempDir()
	runManagerGit(t, peer, "clone", "--branch", "main", "--", remote, ".")
	for _, path := range []string{root, peer} {
		runManagerGit(t, path, "config", "user.name", "Conflict Race")
		runManagerGit(t, path, "config", "user.email", "conflict@example.invalid")
	}
	// Precommit the local side: this isolates resolution concurrency from the
	// independently tested dirty-snapshot LocalOID regression.
	for _, path := range []string{peer, root} {
		content := "remote side\n"
		if path == root {
			content = "local side\n"
		}
		if err := os.WriteFile(filepath.Join(path, "note.md"), []byte(content), 0o600); err != nil {
			t.Fatal("cannot edit conflict side")
		}
		for i := range 50 {
			if err := os.WriteFile(filepath.Join(path, fmt.Sprintf("race-%02d.md", i)), []byte(content), 0o600); err != nil {
				t.Fatal("cannot create conflict race files")
			}
		}
		runManagerGit(t, path, "add", "--all")
		runManagerGit(t, path, "commit", "-m", "conflict side")
	}
	runManagerGit(t, peer, "push", "--no-verify", "origin", "main")
	base := configuredManagerBase("work", root)
	base.URL, base.AutoSync = remote, true
	delegate := gitcmd.NewCommandRunner()
	var network, concurrent, maximum atomic.Int32
	runner := managerRunnerFunc(func(ctx context.Context, c gitcmd.Command) (gitcmd.Result, error) {
		n := concurrent.Add(1)
		defer concurrent.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		if c.Scope == gitcmd.NetworkOperation {
			network.Add(1)
		}
		return delegate.Run(ctx, c)
	})
	client := gitcmd.NewClient(runner)
	f := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, base.Name, runner, client, client)
	seedManagerTrust(t, f, base, oid)
	status, _, _ := f.statuses.Get(ctx, root)
	status.ConsecutiveFailures = 3
	if err := f.statuses.Upsert(ctx, status); err != nil {
		t.Fatal("cannot seed failure budget")
	}
	clock := newFakeGitSchedulerClock(schedulerTestNow())
	enableManagerAutosyncFixture(t, f, clock)
	op, _, err := f.manager.QueueSync(ctx, gitcmd.SyncRequest{Snapshot: base})
	if err != nil {
		t.Fatal("cannot admit conflicting sync")
	}
	if err := f.manager.Start(); err != nil {
		t.Fatal("cannot start conflict manager")
	}
	hardeningIdle(t, f.manager)
	list, err := f.manager.ListConflicts(ctx, base.Name)
	if err != nil || len(list.Conflicts) != 51 || list.OperationID != op.ID {
		t.Fatal("real conflict unavailable")
	}
	original := list.Conflicts[0]
	beforeNetwork := network.Load()
	merged := "local side\nremote side\n"
	for iteration := range 50 {
		conflict := list.Conflicts[iteration+1]
		if conflict.Path != fmt.Sprintf("race-%02d.md", iteration) {
			t.Fatal("conflict paths not sorted")
		}
		results := make(chan error, 5)
		start := make(chan struct{})
		go func() { <-start; _, e := f.manager.ListConflicts(ctx, base.Name); results <- e }()
		go func() { <-start; _, _, e := f.statuses.Get(ctx, root); results <- e }()
		go func() {
			<-start
			_, e := f.notes.SaveNote(model.SaveNoteRequest{ID: "note.md", Content: "must not overwrite"})
			if !errors.Is(e, ErrGitConflictPending) {
				results <- errors.New("conflict mutation gate failed")
			} else {
				results <- nil
			}
		}()
		go func() {
			<-start
			_, _, e := f.manager.Resume(ctx, base.Name)
			if e == nil {
				results <- errors.New("conflict admitted resume")
			} else {
				results <- nil
			}
		}()
		go func() {
			<-start
			response, e := f.manager.ResolveConflict(ctx, model.GitConflictResolveRequest{Base: base.Name, OperationID: op.ID, ConflictID: conflict.ID, Path: conflict.Path, Action: model.GitConflictManual, ResultPath: conflict.Path, Content: &merged})
			if e == nil && (response.Remaining.OperationID != op.ID || response.Remaining.CanComplete || len(response.Remaining.Conflicts) != 50-iteration) {
				e = errors.New("resolution lost original conflict identity")
			}
			results <- e
		}()
		close(start)
		for range 5 {
			if hardeningReceive(t, "conflict race", results) != nil {
				t.Fatal("conflict race contract failed")
			}
		}
		clock.Advance(15 * time.Minute)
	}
	hardeningIdle(t, f.manager)
	status, _, err = f.statuses.Get(ctx, root)
	if err != nil || status.State != model.GitStateConflict || status.ConsecutiveFailures != 3 || network.Load() != beforeNetwork {
		t.Fatal("conflict consumed failure budget or performed network work")
	}
	hardeningRestart(t, f, runner, client)
	if err := f.manager.RecoverLocal(ctx, []gitcmd.ConfiguredBase{base}); err != nil {
		t.Fatal("cannot recover resolved conflict")
	}
	if err := f.manager.Start(); err != nil {
		t.Fatal("cannot start recovered manager")
	}
	list, err = f.manager.ListConflicts(ctx, base.Name)
	if err != nil || list.CanComplete || len(list.Conflicts) != 1 || list.Conflicts[0].ID != original.ID || list.OperationID != op.ID || network.Load() != beforeNetwork {
		t.Fatal("restart lost resolution identity")
	}
	resolved, err := f.manager.ResolveConflict(ctx, model.GitConflictResolveRequest{Base: base.Name, OperationID: op.ID, ConflictID: original.ID, Path: original.Path, Action: model.GitConflictManual, ResultPath: original.Path, Content: &merged})
	if err != nil || !resolved.Remaining.CanComplete || resolved.Remaining.OperationID != op.ID {
		t.Fatal("cannot resolve final conflict after restart")
	}
	if runManagerGit(t, root, "show", ":0:note.md") != strings.TrimSpace(merged) {
		t.Fatal("restart lost staged resolution bytes")
	}
	if maximum.Load() != 1 {
		t.Fatal("conflict operations overlapped Git commands")
	}
}

// Sink assertions deliberately never print candidate bytes, secrets or paths.
func hardeningSecretFree(t *testing.T, sink string, data []byte, secrets []string) {
	t.Helper()
	for _, secret := range secrets {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("secret reached %s", sink)
		}
	}
}

func TestGitSecretsNeverReachPublicOrPersistentSinks(t *testing.T) {
	if os.Getenv("IGONOTES_HARDENING_EXTERNAL_GIT") == "1" {
		_, _ = io.WriteString(os.Stderr, os.Getenv("IGONOTES_HARDENING_DIAGNOSTIC"))
		os.Exit(23)
	}
	ctx := context.Background()
	secret := strings.Repeat("IGONOTES_SECRET_32", 7)
	unsafe := "https://user:" + secret + "@example.invalid/repo.git?token=" + secret
	basic := base64.StdEncoding.EncodeToString([]byte("user:" + secret))
	secrets := []string{secret, unsafe, basic}
	root, remote, oid := readyManagerGitPair(t)
	base := configuredManagerBase("work", root)
	base.URL = remote
	delegate := gitcmd.NewCommandRunner()
	var calls atomic.Int32
	var externalErr error
	// Run a native executable under CommandRunner, not a shell or a mock SafeError.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("cannot locate native test executable")
	}
	program, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal("cannot read native test executable")
	}
	bin := t.TempDir()
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), program, 0o700); err != nil {
		t.Fatal("cannot create external Git fixture")
	}
	t.Setenv("IGONOTES_HARDENING_EXTERNAL_GIT", "1")
	t.Setenv("IGONOTES_HARDENING_DIAGNOSTIC", "fatal: Authentication failed for "+unsafe+"\ntoken="+secret+"\nAuthorization: Basic "+basic+"\n")
	originalPATH := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+originalPATH)
	result, externalErr := delegate.Run(ctx, gitcmd.Command{Dir: root, Args: []string{"-test.run=^TestGitSecretsNeverReachPublicOrPersistentSinks$", "--"}, Secrets: secrets})
	if externalErr == nil {
		t.Fatal("external Git diagnostic did not fail")
	}
	var safe *gitcmd.SafeError
	if !errors.As(externalErr, &safe) || safe.Diagnostic() == "" {
		t.Fatal("external Git produced no safe diagnostic")
	}
	for sink, data := range map[string]string{"stderr": result.Stderr, "Error": externalErr.Error(), "Diagnostic": safe.Diagnostic(), "fmt": fmt.Sprintf("%v %+v %#v %q", externalErr, externalErr, externalErr, externalErr)} {
		hardeningSecretFree(t, sink, []byte(data), secrets)
	}
	t.Setenv("PATH", originalPATH)
	t.Setenv("IGONOTES_HARDENING_EXTERNAL_GIT", "")
	runner := managerRunnerFunc(func(ctx context.Context, c gitcmd.Command) (gitcmd.Result, error) {
		calls.Add(1)
		if c.Scope == gitcmd.NetworkOperation {
			return gitcmd.Result{}, externalErr
		}
		return delegate.Run(ctx, c)
	})
	client := gitcmd.NewClient(runner)
	f := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, base.Name, runner, client, client)
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	configPath := filepath.Join(t.TempDir(), "config.json")
	store := NewConfigService(configPath)
	completed := true
	if err := store.Save(&model.Config{SetupCompleted: &completed, CurrentBase: base.Name, Bases: []model.Base{{Name: base.Name, Path: root, GitURL: remote, GitBranch: "main", GitCommitMessageTemplate: DefaultGitCommitMessageTemplate}}}); err != nil {
		t.Fatal("cannot seed config file")
	}
	settings, err := NewSettingsServiceWithGit(store, f.notes, f.coordinator, "", logger, NewGitConfigValidator(client), f.statuses)
	if err != nil {
		t.Fatal("cannot construct real settings")
	}
	base, _, err = settings.GitSnapshot(base.Name)
	if err != nil {
		t.Fatal("cannot snapshot real settings")
	}
	f.snapshots.put(base)
	seedManagerTrust(t, f, base, oid)
	f.manager.logger = logger
	prober := NewGitProbeService(settings, client)
	baseline := calls.Load()
	_, probeErr := prober.Probe(ctx, model.GitProbeRequest{Base: base.Name, GitURL: unsafe, GitBranch: "main"})
	_, configErr := settings.ConfigureGit(ctx, base.Name, model.GitConfigRequest{GitURL: unsafe, GitBranch: "main"})
	candidate := base
	candidate.URL = unsafe
	_, _, queueErr := f.manager.QueueSync(ctx, gitcmd.SyncRequest{Snapshot: candidate})
	if probeErr == nil || configErr == nil || queueErr == nil || calls.Load() != baseline {
		t.Fatal("unsafe candidate reached execution or admission")
	}
	for sink, e := range map[string]error{"probe error": probeErr, "config error": configErr, "queue error": queueErr} {
		hardeningSecretFree(t, sink, []byte(fmt.Sprintf("%v %+v %#v", e, e, e)), secrets)
	}
	var rows int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM git_operations").Scan(&rows); err != nil || rows != 1 {
		t.Fatal("unsafe candidate reached durable queue")
	}
	if err := f.manager.Start(); err != nil {
		t.Fatal("cannot start sink manager")
	}
	for range 5 {
		if _, _, err := f.manager.QueueSync(ctx, gitcmd.SyncRequest{Snapshot: base}); err != nil {
			t.Fatal("cannot queue safe operation")
		}
		hardeningIdle(t, f.manager)
	}
	status, found, err := f.statuses.Get(ctx, root)
	if err != nil || !found || status.State != model.GitStatePaused || status.Error == nil || logs.Len() == 0 {
		t.Fatal("did not exercise real failure publication and breaker log")
	}
	op, found, err := f.operations.LatestByPath(ctx, root)
	if err != nil || !found || op.Error == nil {
		t.Fatal("did not persist failed operation")
	}
	publicStatus, err := NewGitStatusService(settings, f.statuses).Status(ctx, base.Name)
	if err != nil || len(publicStatus.Statuses) != 1 {
		t.Fatal("cannot read public status service")
	}
	probe, err := prober.Probe(ctx, model.GitProbeRequest{Base: base.Name, GitURL: base.URL, GitBranch: base.Branch})
	if err != nil || probe.BlockingError == nil {
		t.Fatal("safe probe did not exercise external failure")
	}
	resumed, duplicate, err := f.manager.Resume(ctx, base.Name)
	if err != nil || duplicate {
		t.Fatal("paused resume did not admit a real operation")
	}
	hardeningIdle(t, f.manager)
	conflicts, conflictErr := f.manager.ListConflicts(ctx, base.Name)
	if conflictErr == nil {
		t.Fatal("non-conflict public inspection unexpectedly succeeded")
	}
	hardeningSecretFree(t, "conflicts error", []byte(fmt.Sprintf("%v %+v %#v", conflictErr, conflictErr, conflictErr)), secrets)
	for sink, value := range map[string]any{"status JSON": publicStatus, "operation JSON": op, "config JSON": settings.GetConfig(), "probe JSON": probe, "resume JSON": resumed, "conflicts JSON": conflicts} {
		data, e := json.Marshal(value)
		if e != nil {
			t.Fatal("cannot serialize public sink")
		}
		hardeningSecretFree(t, sink, data, secrets)
	}
	hardeningSecretFree(t, "logger", logs.Bytes(), secrets)
	if err := f.manager.Close(); err != nil {
		t.Fatal("cannot close sink manager")
	}
	if _, err := f.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal("cannot checkpoint persistent sinks")
	}
	for sink, path := range map[string]string{"config file": configPath, "SQLite": f.dbPath, "SQLite WAL": f.dbPath + "-wal", "SQLite SHM": f.dbPath + "-shm"} {
		data, e := os.ReadFile(path)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			t.Fatal("cannot inspect persistent sink")
		}
		hardeningSecretFree(t, sink, data, secrets)
	}
	bodies, err := delegate.Run(ctx, gitcmd.Command{Dir: root, Args: []string{"log", "--all", "--format=%B"}, ReadOnly: true})
	if err != nil || bodies.StdoutTruncated {
		t.Fatal("cannot inspect commit bodies")
	}
	hardeningSecretFree(t, "commit bodies", []byte(bodies.Stdout), secrets)
}
