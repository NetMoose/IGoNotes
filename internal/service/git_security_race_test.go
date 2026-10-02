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
	"sync"
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

func hardeningWaitForSignal(ctx context.Context, signal <-chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-signal:
		return ctx.Err() // Cancellation wins over a simultaneously opened gate.
	case <-ctx.Done():
		return ctx.Err()
	}
}

type hardeningCallers struct {
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	stopWorker func()
}

func newHardeningCallers(t *testing.T, f *gitManagerFixture) *hardeningCallers {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	callers := &hardeningCallers{ctx: ctx, cancel: cancel, stopWorker: func() { f.manager.lifetimeCancel() }}
	// Register after fixture/manager cleanup so cancellation and caller joins
	// run first. Signalling worker cancellation releases non-context-aware
	// coordinator/worktree locks; Close and SQLite teardown happen only later.
	t.Cleanup(func() { callers.stopAndJoin(t) })
	return callers
}

func (callers *hardeningCallers) goCall(run func()) {
	callers.wg.Add(1)
	go func() { defer callers.wg.Done(); run() }()
}

func (callers *hardeningCallers) join(t *testing.T) {
	t.Helper()
	done := make(chan struct{}, 1)
	go func() { callers.wg.Wait(); done <- struct{}{} }()
	// Cleanup must still join when t.Context and the caller context are already
	// cancelled. This independent bound does not prolong any caller's context.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(callers.ctx), 5*time.Second)
	defer cancel()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("cancelled test callers did not join before resource teardown")
	}
}

func (callers *hardeningCallers) stopAndJoin(t *testing.T) {
	t.Helper()
	callers.cancel()
	callers.stopWorker()
	callers.join(t)
}

func TestGitManagerRaceCancellationBeforeWorktree(t *testing.T) {
	root, remote, _ := readyManagerGitPair(t)
	base := configuredManagerBase("work", root)
	base.URL = remote
	runner := gitcmd.NewCommandRunner()
	client := gitcmd.NewClient(runner)
	f := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, base.Name, runner, client, client)
	callers := newHardeningCallers(t, f)
	ctx := callers.ctx
	worktreeStarted := make(chan struct{})
	entered, result := make(chan struct{}, 1), make(chan error, 1)
	var release sync.Once
	t.Cleanup(func() {
		callers.cancel()
		release.Do(func() { close(worktreeStarted) })
		callers.join(t)
	})
	callers.goCall(func() {
		entered <- struct{}{}
		if err := hardeningWaitForSignal(ctx, worktreeStarted); err != nil {
			result <- err
			return
		}
		_, err := f.notes.SaveNote(model.SaveNoteRequest{ID: "note.md", Content: "late save after failure\n"})
		result <- err
	})
	hardeningReceive(t, "waiting worktree caller", entered)
	callers.cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Error("worktree waiter did not return caller cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Error("cancelled caller remained stranded before worktreeStarted")
		// RED/failure recovery must itself join the old, uncancellable waiter.
		release.Do(func() { close(worktreeStarted) })
		hardeningReceive(t, "failed fixture worktree drain", result)
	}
	callers.join(t)
	data, err := os.ReadFile(filepath.Join(root, "note.md"))
	if err != nil || string(data) != "note\n" {
		t.Error("cancelled worktree waiter performed a late real save")
	}
	release.Do(func() { close(worktreeStarted) })
	if !errors.Is(hardeningWaitForSignal(ctx, worktreeStarted), context.Canceled) {
		t.Error("opened worktree gate defeated prior caller cancellation")
	}
}

func TestGitManagerRaceCancellationJoinsQueuedConflictCallers(t *testing.T) {
	root, remote, oid := readyManagerGitPair(t)
	base := configuredManagerBase("work", root)
	base.URL = remote
	var commands atomic.Int32
	delegate := gitcmd.NewCommandRunner()
	runner := managerRunnerFunc(func(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
		commands.Add(1)
		return delegate.Run(ctx, command)
	})
	client := gitcmd.NewClient(runner)
	f := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, base.Name, runner, client, client)
	seedManagerTrust(t, f, base, oid)
	callers := newHardeningCallers(t, f)
	ctx := callers.ctx
	if _, bounded := ctx.Deadline(); !bounded {
		t.Fatal("queued conflict callers require a bounded shared context")
	}
	results := make(chan error, 2)
	// Leave the worker unstarted: both public calls must really enter its FIFO,
	// and only caller cancellation can return them (Close doesn't deliver their
	// private queued result channels). No synthetic handler/error responses.
	callers.goCall(func() { _, err := f.manager.ListConflicts(ctx, base.Name); results <- err })
	callers.goCall(func() {
		_, err := f.manager.ResolveConflict(ctx, model.GitConflictResolveRequest{Base: base.Name, OperationID: strings.Repeat("a", 32), ConflictID: "queued", Path: "note.md"})
		results <- err
	})
	queuedCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		f.manager.mu.Lock()
		queued := len(f.manager.queue)
		f.manager.mu.Unlock()
		if queued == 2 {
			break
		}
		select {
		case <-queuedCtx.Done():
			t.Fatal("conflict callers did not enter real FIFO")
		default:
			runtime.Gosched()
		}
	}
	callers.cancel()
	// Join before even cancelling the manager lifetime. This specifically proves
	// queued ListConflicts/ResolveConflict do not depend on teardown waking them.
	callers.join(t)
	for range 2 {
		if !errors.Is(hardeningReceive(t, "cancelled queued conflict caller", results), context.Canceled) {
			t.Fatal("queued conflict call did not return caller cancellation")
		}
	}
	if f.manager.lifetimeCtx.Err() != nil || commands.Load() != 0 {
		t.Fatal("queued caller cancellation depended on manager shutdown or Git execution")
	}
	if err := f.db.Ping(); err != nil {
		t.Fatal("SQLite closed before caller join")
	}
	note, err := f.notes.GetNote("note.md")
	if err != nil || note.Content != "note\n" {
		t.Fatal("cancelled queued calls mutated or closed the note runtime")
	}
}

type hardeningBarrier struct {
	repo    string
	stage   string
	release chan struct{}
}

type hardeningSyncQueueFunc func(context.Context, gitcmd.SyncRequest) (gitcmd.Operation, bool, error)

func (queue hardeningSyncQueueFunc) QueueSync(ctx context.Context, request gitcmd.SyncRequest) (gitcmd.Operation, bool, error) {
	return queue(ctx, request)
}

func TestGitManagerRaceScheduledManualAdmissionIdentity(t *testing.T) {
	root, remote, oid := readyManagerGitPair(t)
	base := configuredManagerBase("work", root)
	base.URL, base.AutoSync, base.IntervalMinutes = remote, true, 5
	fetchEntered, fetchRelease := make(chan struct{}, 1), make(chan struct{}, 1)
	var fetches, pushes atomic.Int32
	delegate := gitcmd.NewCommandRunner()
	runner := managerRunnerFunc(func(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
		if slices.Contains(command.Args, "fetch") && fetches.Add(1) == 1 {
			fetchEntered <- struct{}{}
			select {
			case <-fetchRelease:
			case <-ctx.Done():
				return gitcmd.Result{}, ctx.Err()
			}
		}
		if slices.Contains(command.Args, "push") {
			pushes.Add(1)
		}
		return delegate.Run(ctx, command)
	})
	client := gitcmd.NewClient(runner)
	f := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, base.Name, runner, client, client)
	callers := newHardeningCallers(t, f)
	ctx := callers.ctx
	seedManagerTrust(t, f, base, oid)
	clock := newFakeGitSchedulerClock(schedulerTestNow())
	f.manager.now = clock.Now
	workerReady, workerRelease := make(chan struct{}, 1), make(chan struct{}, 1)
	// Park the running FIFO worker immediately before operation dequeue using
	// its existing synchronous-job seam. This is only an admission gate, not a
	// Git/worktree mutation: temporarily release the coordinator so both real
	// public QueueSync calls can run, and restore it before the worker resumes.
	// Lifetime cancellation also opens the gate, keeping failure cleanup bounded.
	if err := f.manager.enqueueSynchronous(func() {
		f.coordinator.Unlock()
		defer f.coordinator.Lock()
		workerReady <- struct{}{}
		select {
		case <-workerRelease:
		case <-ctx.Done():
		case <-f.manager.lifetimeCtx.Done():
		}
	}); err != nil {
		t.Fatal("cannot install worker admission gate")
	}
	if err := f.manager.Start(); err != nil {
		t.Fatal("cannot start admission worker")
	}
	hardeningReceive(t, "running worker admission gate", workerReady)
	type admission struct {
		op        gitcmd.Operation
		duplicate bool
		err       error
	}
	captured, adapterRelease := make(chan admission, 1), make(chan struct{}, 1)
	queue := hardeningSyncQueueFunc(func(ctx context.Context, request gitcmd.SyncRequest) (gitcmd.Operation, bool, error) {
		op, duplicate, err := f.manager.QueueSync(ctx, request)
		// Record the real manager return values before returning to queueDue.
		// The scheduler request stays open while the manual request races it.
		select {
		case captured <- admission{op: op, duplicate: duplicate, err: err}:
		case <-ctx.Done():
			return op, duplicate, ctx.Err()
		}
		if waitErr := hardeningWaitForSignal(ctx, adapterRelease); waitErr != nil {
			return op, duplicate, waitErr
		}
		return op, duplicate, err
	})
	scheduler := newGitScheduler(clock, f.statuses, f.snapshots, make(chan struct{}, 1), queue, log.New(io.Discard, "", 0))
	scheduler.startedAt = clock.Now()
	if err := scheduler.reconcile(ctx, true); err != nil {
		t.Fatal("cannot reconcile admission scheduler")
	}
	clock.Advance(5 * time.Second)
	tick := make(chan bool, 1)
	callers.goCall(func() { tick <- scheduler.queueDue(ctx, clock.Now()) })
	scheduled := hardeningReceive(t, "scheduler queue result", captured)
	if scheduled.err != nil || scheduled.duplicate || scheduled.op.ID == "" {
		t.Fatal("scheduler did not capture a new actual admission")
	}
	active, found, err := f.operations.ActiveByPath(ctx, root)
	if err != nil || !found || active.ID != scheduled.op.ID || active.State != gitcmd.OperationQueued || fetches.Load() != 0 {
		t.Fatal("scheduler result was not captured at an active durable admission boundary")
	}
	manualResult := make(chan admission, 1)
	callers.goCall(func() {
		op, duplicate, err := f.manager.QueueSync(ctx, gitcmd.SyncRequest{Snapshot: base})
		manualResult <- admission{op, duplicate, err}
	})
	manual := hardeningReceive(t, "same-path manual result", manualResult)
	if manual.err != nil || !manual.duplicate || manual.op.ID != scheduled.op.ID {
		t.Fatalf("scheduler/manual admission identity differs: manualDeduplicated=%v sameID=%v", manual.duplicate, manual.op.ID == scheduled.op.ID)
	}
	adapterRelease <- struct{}{}
	if !hardeningReceive(t, "scheduled tick", tick) {
		t.Fatal("scheduler stopped at admission boundary")
	}
	// A later automatic admission while this same operation remains active must
	// also report deduplication, rather than merely serializing another job.
	clock.Advance(5 * time.Minute)
	callers.goCall(func() { tick <- scheduler.queueDue(ctx, clock.Now()) })
	again := hardeningReceive(t, "repeat scheduler queue result", captured)
	if again.err != nil || !again.duplicate || again.op.ID != scheduled.op.ID {
		t.Fatal("repeat scheduled admission lost active operation identity")
	}
	callers.goCall(func() {
		op, duplicate, err := f.manager.QueueSync(ctx, gitcmd.SyncRequest{Snapshot: base})
		manualResult <- admission{op, duplicate, err}
	})
	manual = hardeningReceive(t, "repeat same-path manual result", manualResult)
	if manual.err != nil || !manual.duplicate || manual.op.ID != again.op.ID {
		t.Fatal("repeat scheduler/manual admissions returned different operation IDs")
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM git_operations WHERE repo_path = ? AND kind = ?", root, gitcmd.OperationSync).Scan(&count); err != nil || count != 1 {
		t.Fatal("deduplicated admissions created more than one durable sync")
	}
	f.manager.mu.Lock()
	inFlight, pending := f.manager.inFlight[root], len(f.manager.queue)
	f.manager.mu.Unlock()
	if inFlight.ID != scheduled.op.ID || pending != 1 || fetches.Load() != 0 {
		t.Fatal("admission identity or FIFO cardinality changed before execution")
	}
	adapterRelease <- struct{}{}
	if !hardeningReceive(t, "repeat scheduled tick", tick) {
		t.Fatal("repeat scheduler stopped at admission boundary")
	}
	workerRelease <- struct{}{}
	hardeningReceive(t, "admitted operation fetch", fetchEntered)
	fetchRelease <- struct{}{}
	hardeningIdle(t, f.manager)
	callers.join(t)
	completed, found, err := f.operations.ByID(ctx, scheduled.op.ID)
	if err != nil || !found || completed.State != gitcmd.OperationSucceeded || completed.Stage != gitcmd.StageCompleted || fetches.Load() != 1 || pushes.Load() != 1 {
		t.Fatal("shared scheduler/manual operation did not execute exactly once")
	}
	status, found, err := f.statuses.Get(ctx, root)
	if err != nil || !found || status.State != model.GitStateReady || status.OperationID != scheduled.op.ID || status.RemoteOID != oid {
		t.Fatal("completed operation publication lost admission identity")
	}
}

func TestGitManagerRaceManualScheduledResumeSwitchSaveAndClose(t *testing.T) {
	ctx := t.Context()
	var bases []gitcmd.ConfiguredBase
	peers, contents, heads := map[string]string{}, map[string]string{}, map[string]string{}
	for _, name := range []string{"A", "B"} {
		root, remote, _ := readyManagerGitPair(t)
		base := configuredManagerBase(name, root)
		base.URL, base.AutoSync, base.IntervalMinutes = remote, true, 5
		contents[root] = name + " original\n"
		for file, data := range map[string]string{"note.md": contents[root], name + "-only.md": name + " destination index\n"} {
			if err := os.WriteFile(filepath.Join(root, file), []byte(data), 0o600); err != nil {
				t.Fatal("cannot seed distinct base bytes")
			}
		}
		runManagerGit(t, root, "add", "--all")
		runManagerGit(t, root, "commit", "-m", "distinct base")
		runManagerGit(t, root, "push", "--no-verify", "origin", "main")
		heads[root] = runManagerGit(t, root, "rev-parse", "HEAD")
		runManagerGit(t, root, "update-ref", "refs/igonotes/remotes/main", heads[root])
		peers[root] = t.TempDir()
		runManagerGit(t, peers[root], "clone", "--branch", "main", "--", remote, ".")
		bases = append(bases, base)
	}
	entered := make(chan hardeningBarrier, 1)
	canceled := make(chan string, 1)
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
			b := hardeningBarrier{repo: c.Dir, stage: stage, release: make(chan struct{}, 1)}
			select {
			case entered <- b:
			case <-ctx.Done():
				return gitcmd.Result{}, ctx.Err()
			}
			select {
			case <-b.release:
			case <-ctx.Done():
				canceled <- stage
				return gitcmd.Result{}, ctx.Err()
			}
		}
		return delegate.Run(ctx, c)
	})
	client := gitcmd.NewClient(runner)
	f := newGitManagerFixture(t, bases, "A", runner, client, client)
	completed := true
	store := NewConfigService(filepath.Join(t.TempDir(), "config.json"))
	config := model.Config{SetupCompleted: &completed, CurrentBase: "A"}
	for _, base := range bases {
		config.Bases = append(config.Bases, model.Base{Name: base.Name, Path: base.Path, GitURL: base.URL, GitBranch: base.Branch,
			AutoSync: true, AutoSyncIntervalMinutes: 5, GitCommitMessageTemplate: DefaultGitCommitMessageTemplate})
	}
	if err := store.Save(&config); err != nil {
		t.Fatal("cannot seed two-base settings")
	}
	settings, err := NewSettingsServiceWithGit(store, f.notes, f.coordinator, "", log.New(io.Discard, "", 0), NewGitConfigValidator(client), f.statuses)
	if err != nil {
		t.Fatal("cannot construct two-base settings")
	}
	for i := range bases {
		bases[i], _, err = settings.GitSnapshot(bases[i].Name)
		if err != nil {
			t.Fatal("cannot snapshot configured base")
		}
		f.snapshots.put(bases[i])
		if i == 1 {
			// seedManagerTrust deliberately has one fixed fixture ID.
			if _, err := f.db.Exec("UPDATE git_operations SET operation_id = ?", strings.Repeat("b", 32)); err != nil {
				t.Fatal("cannot distinguish base journals")
			}
		}
		seedManagerTrust(t, f, bases[i], heads[bases[i].Path])
	}
	clock := newFakeGitSchedulerClock(schedulerTestNow())
	f.manager = NewGitManager(gitcmd.NewService(runner, client), f.statuses, f.operations, NewGitProbeService(settings, client), settings.GitSnapshot, f.notes, f.coordinator)
	f.manager.now, f.manager.orderedSnapshots = clock.Now, lifecycleGitSnapshots{settings}
	manager := f.manager
	t.Cleanup(func() { _ = manager.Close() })
	callers := newHardeningCallers(t, f)
	ctx = callers.ctx
	// Exercise the real scheduler's reconciliation/admission with an explicitly
	// driven clock. Only this test goroutine drives scheduler state; the manager's
	// actual worker is running throughout all fifty iterations.
	scheduler := newGitScheduler(clock, f.statuses, lifecycleGitSnapshots{settings}, make(chan struct{}, 1), manager, log.New(io.Discard, "", 0))
	scheduler.startedAt = clock.Now()
	if err := manager.Start(); err != nil {
		t.Fatal("cannot start race worker")
	}
	f.manager.mu.Lock()
	started := f.manager.started
	f.manager.mu.Unlock()
	configured, err := f.snapshots.OrderedGitSnapshots()
	if err != nil || !started || len(configured) != 2 {
		t.Fatal("race fixture requires a running worker and two configured bases")
	}
	phases, visits := map[string]int{}, map[string]int{}
	observe := func(b hardeningBarrier) {
		t.Helper()
		if b.stage != []string{"fetch", "add", "push"}[phases[b.repo]] || current.Load() != 1 {
			t.Fatal("running Git commands overlapped or skipped a barrier")
		}
		phases[b.repo] = (phases[b.repo] + 1) % 3
		if b.stage == "fetch" {
			visits[b.repo]++
		}
	}
	type result struct {
		kind      string
		op        gitcmd.Operation
		duplicate bool
		err       error
	}
	var interrupted, next gitcmd.Operation
	destinationIndex := func(base gitcmd.ConfiguredBase) error {
		nodes, err := repository.NewNoteRepository(f.db).GetAllNodes()
		if err != nil {
			return err
		}
		var ids []string
		for _, node := range nodes {
			ids = append(ids, node.ID)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{base.Name + "-only.md", "note.md"}) {
			return errors.New("destination index retained source nodes")
		}
		return nil
	}
	for iteration := range 50 {
		source, destination := bases[iteration%2], bases[1-iteration%2]
		if settings.GetConfig().CurrentBase != source.Name || f.notes.GetBasePath() != source.Path {
			t.Fatal("iteration did not start on the previous switch destination")
		}
		before, err := f.notes.GetNote("note.md")
		if err != nil || before.Content != contents[source.Path] {
			t.Fatal("cannot capture source byte revision")
		}
		want := fmt.Sprintf("%s incoming revision %d\n", source.Name, iteration)
		if err := os.WriteFile(filepath.Join(peers[source.Path], "note.md"), []byte(want), 0o600); err != nil {
			t.Fatal("cannot edit remote source")
		}
		runManagerGit(t, peers[source.Path], "add", "--all")
		runManagerGit(t, peers[source.Path], "commit", "-m", "incoming revision")
		runManagerGit(t, peers[source.Path], "push", "--no-verify", "origin", "main")
		contents[source.Path] = want
		var op gitcmd.Operation
		if iteration == 49 {
			if err := scheduler.reconcile(ctx, false); err != nil {
				t.Fatal("cannot reconcile shutdown scheduler")
			}
			status, _, err := f.statuses.Get(ctx, source.Path)
			if err != nil {
				t.Fatal("cannot read shutdown budget")
			}
			status.ConsecutiveFailures = 3
			if err := f.statuses.Upsert(ctx, status); err != nil {
				t.Fatal("cannot seed shutdown budget")
			}
			// Atomically admit a real second-base journal/FIFO job behind the last
			// source job. The worker is already running. Use the existing admission
			// seam under its documented two locks, so it cannot dequeue between
			// these two admissions (public admissions are raced below).
			f.coordinator.Lock()
			manager.mu.Lock()
			for _, base := range []gitcmd.ConfiguredBase{source, destination} {
				status, found, lookupErr := f.statuses.Get(ctx, base.Path)
				queued, duplicate, queueErr := manager.queueValidatedLocked(ctx, base, gitcmd.OperationSync, model.GitConfirmations{}, status, found)
				if lookupErr != nil || queueErr != nil || duplicate {
					manager.mu.Unlock()
					f.coordinator.Unlock()
					t.Fatal("cannot admit shutdown pair")
				}
				if base.Name == source.Name {
					op = queued
				} else {
					next = queued
				}
			}
			manager.mu.Unlock()
			f.coordinator.Unlock()
		} else {
			var duplicate bool
			op, duplicate, err = manager.QueueSync(ctx, gitcmd.SyncRequest{Snapshot: source})
			if err != nil || duplicate {
				t.Fatal("cannot admit distinct running iteration")
			}
		}
		fetch := hardeningReceive(t, "running fetch", entered)
		observe(fetch)
		stored, found, err := f.operations.ByID(ctx, op.ID)
		if fetch.repo != source.Path || err != nil || !found || stored.State != gitcmd.OperationRunning || stored.Stage != gitcmd.StageFetching {
			t.Fatal("iteration did not reach a real running fetch checkpoint")
		}
		if iteration != 49 {
			if err := scheduler.reconcile(ctx, iteration == 0); err != nil {
				t.Fatal("cannot reconcile live scheduler")
			}
		}
		results, attempted := make(chan result, 6), make(chan struct{}, 6)
		start := make(chan struct{})
		worktreeStarted := make(chan struct{})
		callers.goCall(func() {
			if err := hardeningWaitForSignal(ctx, start); err != nil {
				results <- result{kind: "manual", err: err}
				return
			}
			attempted <- struct{}{}
			op, duplicate, err := manager.QueueSync(ctx, gitcmd.SyncRequest{Snapshot: destination})
			results <- result{kind: "manual", op: op, duplicate: duplicate, err: err}
		})
		callers.goCall(func() {
			if err := hardeningWaitForSignal(ctx, start); err != nil {
				results <- result{kind: "resume", err: err}
				return
			}
			attempted <- struct{}{}
			op, duplicate, err := manager.Resume(ctx, destination.Name)
			results <- result{kind: "resume", op: op, duplicate: duplicate, err: err}
		})
		callers.goCall(func() {
			if err := hardeningWaitForSignal(ctx, start); err != nil {
				results <- result{kind: "poll", err: err}
				return
			}
			attempted <- struct{}{}
			_, _, err := f.statuses.Get(ctx, source.Path)
			results <- result{kind: "poll", err: err}
		})
		callers.goCall(func() {
			if err := hardeningWaitForSignal(ctx, start); err != nil {
				results <- result{kind: "scheduled", err: err}
				return
			}
			attempted <- struct{}{}
			clock.Advance(5 * time.Minute)
			var err error
			if !scheduler.queueDue(ctx, clock.Now()) {
				err = ErrGitManagerClosed
			}
			results <- result{kind: "scheduled", err: err}
		})
		callers.goCall(func() {
			if err := hardeningWaitForSignal(ctx, start); err != nil {
				results <- result{kind: "save", err: err}
				return
			}
			attempted <- struct{}{}
			// Saves use NoteService's worktree lock, not the coordinator. Start
			// this stale buffer only after the real mutation owns that lock.
			if err := hardeningWaitForSignal(ctx, worktreeStarted); err != nil {
				results <- result{kind: "save", err: err}
				return
			}
			_, err := f.notes.SaveNote(model.SaveNoteRequest{ID: "note.md", Content: "stale editor must not overwrite either base\n", ExpectedRevision: &before.Revision})
			results <- result{kind: "save", err: err}
		})
		callers.goCall(func() {
			if err := hardeningWaitForSignal(ctx, start); err != nil {
				results <- result{kind: "switch", err: err}
				return
			}
			attempted <- struct{}{}
			_, err := settings.SwitchBase(destination.Name)
			if err == nil {
				err = destinationIndex(destination)
			}
			results <- result{kind: "switch", err: err}
		})
		close(start)
		for range 6 {
			hardeningReceive(t, "running concurrent callers", attempted)
		}
		// The poll can return, but switching/admission must stay behind the
		// coordinator. The save must wait through the active worktree mutation;
		// after reindex it may reject the old revision while push is still held.
		remaining := 6
		checkBlocked := func(worktree, allowSave bool) {
			t.Helper()
			if f.coordinator.operations.TryLock() {
				f.coordinator.Unlock()
				t.Fatal("running operation released coordinator at barrier")
			}
			if worktree && f.notes.baseMu.TryRLock() {
				f.notes.baseMu.RUnlock()
				t.Fatal("active worktree transaction did not block note runtime")
			}
			for {
				select {
				case got := <-results:
					if !(got.kind == "poll" && got.err == nil || allowSave && got.kind == "save" && errors.Is(got.err, ErrNoteChanged)) {
						t.Fatal("caller escaped blocked transaction coordination")
					}
					remaining--
				default:
					return
				}
			}
		}
		checkBlocked(false, false)
		fetch.release <- struct{}{}
		worktree := hardeningReceive(t, "running worktree", entered)
		observe(worktree)
		if worktree.repo != source.Path || worktree.stage != "add" {
			t.Fatal("source did not enter active worktree transaction")
		}
		close(worktreeStarted)
		checkBlocked(true, false)
		worktree.release <- struct{}{}
		push := hardeningReceive(t, "running push", entered)
		observe(push)
		if push.repo != source.Path || push.stage != "push" {
			t.Fatal("source did not reach push")
		}
		checkBlocked(false, true)
		if iteration == 49 {
			closed := make(chan error, 1)
			callers.goCall(func() { closed <- manager.Close() })
			if hardeningReceive(t, "Close during final network", closed) != nil || hardeningReceive(t, "network cancellation", canceled) != "push" {
				t.Fatal("Close did not cancel running push")
			}
			interrupted = op
		} else {
			push.release <- struct{}{}
		}
		// Admission also uses the coordinator. Drive any jobs admitted after the
		// primary operation releases it, rather than assuming a queued-only race
		// or depending on mutex wakeup order between worker and API callers.
		var fence <-chan struct{}
		for remaining > 0 || fence != nil {
			select {
			case b := <-entered:
				if iteration == 49 {
					t.Fatal("Close started the next configured base")
				}
				observe(b)
				b.release <- struct{}{}
			case got := <-results:
				remaining--
				switch got.kind {
				case "save":
					if !errors.Is(got.err, ErrNoteChanged) {
						t.Fatal("revision save overwrote a source or destination note")
					}
				case "manual", "resume":
					if iteration == 49 {
						if !errors.Is(got.err, ErrGitManagerClosed) {
							t.Fatal("Close admitted waiting mutation request")
						}
					} else if got.err != nil {
						if got.kind != "resume" || !errors.Is(got.err, gitcmd.ErrGitNotPaused) {
							t.Fatal("live admission failed unexpectedly")
						}
					} else {
						journal, found, err := f.operations.ByID(ctx, got.op.ID)
						if err != nil || !found || journal.RepoPath != destination.Path || got.op.RepoPath != destination.Path || got.kind == "resume" && !got.duplicate {
							t.Fatal("same-path admission lost durable operation identity")
						}
					}
				case "scheduled":
					if iteration == 49 && !errors.Is(got.err, ErrGitManagerClosed) || iteration != 49 && got.err != nil {
						t.Fatal("scheduled admission ignored manager lifetime")
					}
				default:
					if got.err != nil {
						t.Fatal("switch or status poll failed")
					}
				}
				if remaining == 0 && iteration != 49 {
					done := make(chan struct{}, 1)
					if err := manager.enqueueSynchronous(func() { done <- struct{}{} }); err != nil {
						t.Fatal("cannot fence live jobs")
					}
					fence = done
				}
			case <-fence:
				fence = nil
			case <-time.After(5 * time.Second):
				t.Fatal("live race did not finish all callers and jobs")
			}
		}
		if settings.GetConfig().CurrentBase != destination.Name || f.notes.GetBasePath() != destination.Path {
			t.Fatal("switch did not publish distinct destination path")
		}
		if err := destinationIndex(destination); err != nil {
			t.Fatal("switch retained source nodes instead of replacing destination index")
		}
		for _, base := range bases {
			data, err := os.ReadFile(filepath.Join(base.Path, "note.md"))
			if err != nil || string(data) != contents[base.Path] {
				t.Fatal("concurrent save or switch lost exact base bytes")
			}
		}
		f.snapshots.mu.Lock()
		f.snapshots.active = destination.Name
		f.snapshots.mu.Unlock()
		stored, found, err = f.operations.ByID(ctx, op.ID)
		if iteration != 49 && (err != nil || !found || stored.State != gitcmd.OperationSucceeded) {
			t.Fatal("running iteration did not succeed")
		}
	}
	stored, found, err := f.operations.ByID(ctx, interrupted.ID)
	if err != nil || !found || stored.State != gitcmd.OperationFailed || stored.Error == nil || stored.Error.Code != gitcmd.CodeOperationInterrupted {
		t.Fatal("Close did not persist interrupted push")
	}
	queued, found, err := f.operations.ByID(ctx, next.ID)
	if err != nil || !found || queued.State != gitcmd.OperationQueued {
		t.Fatal("Close started queued second-base operation")
	}
	status, _, err := f.statuses.Get(ctx, interrupted.RepoPath)
	if err != nil || status.ConsecutiveFailures != 3 || status.Error == nil || status.Error.Code != string(gitcmd.CodeOperationInterrupted) {
		t.Fatal("Close changed failure budget or lost cancellation")
	}
	beforeNetwork := networks.Load()
	hardeningRestart(t, f, runner, client, callers)
	if err := f.manager.RecoverLocal(ctx, bases); err != nil {
		t.Fatal("cannot recover after Close")
	}
	if networks.Load() != beforeNetwork {
		t.Fatal("local recovery performed network work")
	}
	persisted, _, err := f.operations.ByID(ctx, interrupted.ID)
	if err != nil || persisted.State != gitcmd.OperationFailed || persisted.Error == nil || persisted.Error.Code != gitcmd.CodeOperationInterrupted {
		t.Fatal("restart lost persisted interruption")
	}
	if maximum.Load() != 1 {
		t.Fatal("manager overlapped Git commands")
	}
	for _, base := range bases {
		if phases[base.Path] != 0 || visits[base.Path] < 25 || runManagerGit(t, base.Path, "rev-parse", "HEAD") != runManagerGit(t, peers[base.Path], "rev-parse", "HEAD") {
			t.Fatal("live barrier loop failed to exercise or preserve both base histories")
		}
	}
}

func hardeningRestart(t *testing.T, f *gitManagerFixture, runner gitcmd.Runner, client *gitcmd.Client, callers *hardeningCallers) {
	t.Helper()
	callers.join(t)
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
	t.Cleanup(func() { callers.stopAndJoin(t); _ = manager.Close(); _ = notes.Close(); _ = db.Close() })
}

func firstManagerPathMust(f *gitManagerFixture) string {
	f.snapshots.mu.RLock()
	defer f.snapshots.mu.RUnlock()
	return f.snapshots.bases[f.snapshots.active].Path
}

func TestGitManagerRaceConflictResolvePollAndRestart(t *testing.T) {
	ctx := t.Context()
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
	callers := newHardeningCallers(t, f)
	ctx = callers.ctx
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
		callers.goCall(func() {
			if err := hardeningWaitForSignal(ctx, start); err != nil {
				results <- err
				return
			}
			_, e := f.manager.ListConflicts(ctx, base.Name)
			results <- e
		})
		callers.goCall(func() {
			if err := hardeningWaitForSignal(ctx, start); err != nil {
				results <- err
				return
			}
			_, _, e := f.statuses.Get(ctx, root)
			results <- e
		})
		callers.goCall(func() {
			if err := hardeningWaitForSignal(ctx, start); err != nil {
				results <- err
				return
			}
			_, e := f.notes.SaveNote(model.SaveNoteRequest{ID: "note.md", Content: "must not overwrite"})
			if !errors.Is(e, ErrGitConflictPending) {
				results <- errors.New("conflict mutation gate failed")
			} else {
				results <- nil
			}
		})
		callers.goCall(func() {
			if err := hardeningWaitForSignal(ctx, start); err != nil {
				results <- err
				return
			}
			_, _, e := f.manager.Resume(ctx, base.Name)
			if e == nil {
				results <- errors.New("conflict admitted resume")
			} else {
				results <- nil
			}
		})
		callers.goCall(func() {
			if err := hardeningWaitForSignal(ctx, start); err != nil {
				results <- err
				return
			}
			response, e := f.manager.ResolveConflict(ctx, model.GitConflictResolveRequest{Base: base.Name, OperationID: op.ID, ConflictID: conflict.ID, Path: conflict.Path, Action: model.GitConflictManual, ResultPath: conflict.Path, Content: &merged})
			if e == nil && (response.Remaining.OperationID != op.ID || response.Remaining.CanComplete || len(response.Remaining.Conflicts) != 50-iteration) {
				e = errors.New("resolution lost original conflict identity")
			}
			results <- e
		})
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
	hardeningRestart(t, f, runner, client, callers)
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
	ctx := t.Context()
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
	callers := newHardeningCallers(t, f)
	ctx = callers.ctx
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
	callers.stopAndJoin(t)
	if err := f.manager.Close(); err != nil {
		t.Fatal("cannot close sink manager")
	}
	if _, err := f.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal("cannot checkpoint persistent sinks")
	}
	if err := f.db.Close(); err != nil {
		t.Fatal("cannot close SQLite before scanning persistent sinks")
	}
	if err := f.db.Ping(); err == nil {
		t.Fatal("persistent sink fixture must close SQLite before scanning")
	}
	for sink, path := range map[string]string{"config file": configPath, "SQLite": f.dbPath, "SQLite WAL": f.dbPath + "-wal", "SQLite SHM": f.dbPath + "-shm"} {
		data, e := os.ReadFile(path)
		optional := sink == "SQLite WAL" || sink == "SQLite SHM"
		if e != nil && !(optional && errors.Is(e, os.ErrNotExist)) {
			t.Fatal("cannot inspect persistent sink")
		}
		if !optional && len(data) == 0 {
			t.Fatal("persistent sink fixture must contain durable bytes")
		}
		if sink == "SQLite" && !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
			t.Fatal("persistent sink fixture did not read a closed SQLite database")
		}
		hardeningSecretFree(t, sink, data, secrets)
	}
	auditCtx, auditCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer auditCancel()
	bodies, err := delegate.Run(auditCtx, gitcmd.Command{Dir: root, Args: []string{"log", "--all", "--format=%B%x00"}, ReadOnly: true})
	if err != nil || bodies.StdoutTruncated || bodies.StderrTruncated {
		t.Fatal("cannot inspect commit bodies")
	}
	if !strings.Contains(bodies.Stdout, "\x00") {
		t.Fatal("commit sink fixture requires NUL-delimited bodies")
	}
	// Scan every byte including separators, and every body without trimming its
	// whitespace. Git inserts a newline after each explicit %x00 terminator.
	hardeningSecretFree(t, "commit bodies", []byte(bodies.Stdout), secrets)
	parts := strings.Split(bodies.Stdout, "\x00")
	if parts[len(parts)-1] != "\n" {
		t.Fatal("commit sink fixture has an unterminated record")
	}
	for _, body := range parts[:len(parts)-1] {
		if body == "" {
			t.Fatal("commit sink fixture omitted a body")
		}
		hardeningSecretFree(t, "commit body record", []byte(body), secrets)
	}
}
