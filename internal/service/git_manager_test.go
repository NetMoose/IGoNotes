package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
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
	"modernc.org/sqlite"
)

const managerOID = "1111111111111111111111111111111111111111"

var managerSQLiteFunctionID atomic.Uint64

type managerSettings struct{ config model.Config }

func (s managerSettings) GetConfig() model.Config { return s.config }

type managerPorcelain struct {
	mu          sync.Mutex
	version     gitcmd.Version
	local       gitcmd.LocalInspection
	remote      gitcmd.RemoteInspection
	history     string
	versionErr  error
	localErr    error
	remoteErr   error
	validateErr error
	calls       []string
}

func (p *managerPorcelain) Version(context.Context, string) (gitcmd.Version, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "version")
	return p.version, p.versionErr
}

func (p *managerPorcelain) ValidateBranch(context.Context, string, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "validate")
	return p.validateErr
}

func (p *managerPorcelain) InspectLocal(context.Context, string) (gitcmd.LocalInspection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "local")
	return p.local, p.localErr
}

func (p *managerPorcelain) InspectRemote(context.Context, string, string) (gitcmd.RemoteInspection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "remote")
	return p.remote, p.remoteErr
}

func (p *managerPorcelain) HistoryRelation(context.Context, string, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "history")
	return p.history, nil
}

type managerRunnerFunc func(context.Context, gitcmd.Command) (gitcmd.Result, error)

func (f managerRunnerFunc) Run(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
	return f(ctx, command)
}

type managerSnapshotStore struct {
	mu     sync.RWMutex
	bases  map[string]gitcmd.ConfiguredBase
	active string
	before func(string)
}

func (s *managerSnapshotStore) get(name string) (gitcmd.ConfiguredBase, bool, error) {
	if s.before != nil {
		s.before(name)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	base, ok := s.bases[name]
	return base, ok && name == s.active, nil
}

func (s *managerSnapshotStore) put(base gitcmd.ConfiguredBase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bases[base.Name] = base
}

type gitManagerFixture struct {
	db          *sql.DB
	statuses    *repository.GitStatusRepository
	operations  *repository.GitOperationRepository
	coordinator *BaseOperationCoordinator
	notes       *NoteService
	snapshots   *managerSnapshotStore
	manager     *GitManager
}

func configuredManagerBase(name, path string) gitcmd.ConfiguredBase {
	return gitcmd.ConfiguredBase{
		Name: name, Path: path, URL: "https://example.invalid/" + name + ".git", Branch: "main",
		IntervalMinutes: 15, CommitTemplate: DefaultGitCommitMessageTemplate,
		Fingerprint: "config-" + name, RemoteFingerprint: "remote-" + name,
	}
}

func permissiveManagerPorcelain(path string) *managerPorcelain {
	return &managerPorcelain{
		version: gitcmd.Version{Major: 2, Minor: 40, Patch: 1, Raw: "git version 2.40.1"},
		local: gitcmd.LocalInspection{
			HasRepository: true, RepositoryRoot: path, GitDir: filepath.Join(path, ".git"),
			CurrentBranch: "main", WorkingTreeClean: true, ExistingOriginURL: "https://example.invalid/work.git",
			IdentityConfigured: true, HasCommits: true,
		},
		remote:  gitcmd.RemoteInspection{Branches: map[string]string{"main": managerOID}},
		history: "shared",
	}
}

func newGitManagerFixture(
	t *testing.T,
	bases []gitcmd.ConfiguredBase,
	active string,
	runner gitcmd.Runner,
	servicePorcelain gitcmd.Porcelain,
	probePorcelain GitPorcelain,
) *gitManagerFixture {
	t.Helper()
	db, err := repository.InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	coordinator := NewBaseOperationCoordinator()
	activePath := ""
	modelBases := make([]model.Base, 0, len(bases))
	baseMap := make(map[string]gitcmd.ConfiguredBase, len(bases))
	for _, base := range bases {
		baseMap[base.Name] = base
		modelBases = append(modelBases, model.Base{Name: base.Name, Path: base.Path, GitURL: base.URL, GitBranch: base.Branch})
		if base.Name == active {
			activePath = base.Path
		}
	}
	if runner == nil {
		runner = managerRunnerFunc(func(context.Context, gitcmd.Command) (gitcmd.Result, error) {
			return gitcmd.Result{}, &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Git command failed"}
		})
	}
	if servicePorcelain == nil {
		servicePorcelain = permissiveManagerPorcelain(firstManagerPath(bases))
	}
	if probePorcelain == nil {
		probePorcelain = permissiveManagerPorcelain(firstManagerPath(bases))
	}
	statuses := repository.NewGitStatusRepository(db)
	operations := repository.NewGitOperationRepository(db)
	notes := NewNoteService(repository.NewNoteRepository(db), activePath, coordinator)
	snapshots := &managerSnapshotStore{bases: baseMap, active: active}
	prober := NewGitProbeService(managerSettings{config: model.Config{Bases: modelBases, CurrentBase: active}}, probePorcelain)
	manager := NewGitManager(gitcmd.NewService(runner, servicePorcelain), statuses, operations, prober, snapshots.get, notes, coordinator)
	fixture := &gitManagerFixture{
		db: db, statuses: statuses, operations: operations, coordinator: coordinator,
		notes: notes, snapshots: snapshots, manager: manager,
	}
	t.Cleanup(func() {
		_ = manager.Close()
		_ = notes.Close()
		_ = db.Close()
	})
	return fixture
}

func firstManagerPath(bases []gitcmd.ConfiguredBase) string {
	if len(bases) == 0 {
		return ""
	}
	return bases[0].Path
}

func seedManagerTrust(t *testing.T, fixture *gitManagerFixture, base gitcmd.ConfiguredBase, oid string) {
	t.Helper()
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	op := gitcmd.Operation{
		ID: strings.Repeat("a", 32), BaseName: base.Name, RepoPath: base.Path,
		ConfigFingerprint: base.Fingerprint, RemoteFingerprint: base.RemoteFingerprint,
		Kind: gitcmd.OperationInitialize, State: gitcmd.OperationQueued, Stage: gitcmd.StageQueued,
		Branch: base.Branch, CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.operations.CreateQueued(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	op.State = gitcmd.OperationSucceeded
	op.Stage = gitcmd.StageCompleted
	op.RemoteOID = oid
	op.PushOID = oid
	if err := fixture.operations.Finish(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if err := fixture.statuses.Upsert(context.Background(), model.GitStatus{
		Base: base.Name, RepositoryPath: base.Path, State: model.GitStateReady,
		RemoteOID: oid, ChangedPaths: []string{},
	}); err != nil {
		t.Fatal(err)
	}
}

func waitManagerTerminal(t *testing.T, manager *GitManager) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	manager.beforeTerminalPublication = func() {
		close(entered)
		<-release
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	<-entered
	close(release)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func requireManagerCode(t *testing.T, err error, code gitcmd.ErrorCode) {
	t.Helper()
	var safeErr *gitcmd.SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != code {
		t.Fatalf("error = %#v, want Git code %q", err, code)
	}
}

func TestGitManagerConstructorAndLifecycle(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
	if err := fixture.manager.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := fixture.manager.Start(); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if err := fixture.manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := fixture.manager.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := fixture.manager.Start(); !errors.Is(err, ErrGitManagerClosed) {
		t.Fatalf("Start() after Close error = %v, want ErrGitManagerClosed", err)
	}
	if _, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base}); !errors.Is(err, ErrGitManagerClosed) {
		t.Fatalf("QueueInitialize() after Close error = %v, want ErrGitManagerClosed", err)
	}
}

func TestGitManagerConstructorPanicsWithDependencyContext(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
	service := gitcmd.NewService(managerRunnerFunc(func(context.Context, gitcmd.Command) (gitcmd.Result, error) { return gitcmd.Result{}, nil }), permissiveManagerPorcelain(base.Path))
	dependencies := []struct {
		name string
		call func()
	}{
		{"GitService", func() {
			NewGitManager(nil, fixture.statuses, fixture.operations, NewGitProbeService(managerSettings{}, permissiveManagerPorcelain(base.Path)), fixture.snapshots.get, fixture.notes, fixture.coordinator)
		}},
		{"GitStatusRepository", func() {
			NewGitManager(service, nil, fixture.operations, NewGitProbeService(managerSettings{}, permissiveManagerPorcelain(base.Path)), fixture.snapshots.get, fixture.notes, fixture.coordinator)
		}},
		{"GitOperationRepository", func() {
			NewGitManager(service, fixture.statuses, nil, NewGitProbeService(managerSettings{}, permissiveManagerPorcelain(base.Path)), fixture.snapshots.get, fixture.notes, fixture.coordinator)
		}},
		{"GitProbeService", func() {
			NewGitManager(service, fixture.statuses, fixture.operations, nil, fixture.snapshots.get, fixture.notes, fixture.coordinator)
		}},
		{"snapshot", func() {
			NewGitManager(service, fixture.statuses, fixture.operations, NewGitProbeService(managerSettings{}, permissiveManagerPorcelain(base.Path)), nil, fixture.notes, fixture.coordinator)
		}},
		{"NoteService", func() {
			NewGitManager(service, fixture.statuses, fixture.operations, NewGitProbeService(managerSettings{}, permissiveManagerPorcelain(base.Path)), fixture.snapshots.get, nil, fixture.coordinator)
		}},
		{"BaseOperationCoordinator", func() {
			NewGitManager(service, fixture.statuses, fixture.operations, NewGitProbeService(managerSettings{}, permissiveManagerPorcelain(base.Path)), fixture.snapshots.get, fixture.notes, nil)
		}},
	}
	for _, test := range dependencies {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				value := recover()
				if value == nil || !strings.Contains(fmt.Sprint(value), test.name) {
					t.Fatalf("panic = %v, want dependency context %q", value, test.name)
				}
			}()
			test.call()
		})
	}
}

func TestGitManagerQueueDurabilityCanonicalDedupeAndIdentity(t *testing.T) {
	realPath := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realPath, alias); err != nil {
		t.Skipf("symlink: %v", err)
	}
	base := configuredManagerBase("work", alias)
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
	fixed := time.Date(2026, 9, 3, 12, 13, 14, 987654321, time.FixedZone("test", 2*60*60))
	fixture.manager.now = func() time.Time { return fixed }

	op, deduplicated, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base})
	if err != nil || deduplicated {
		t.Fatalf("QueueInitialize() = %#v, %v, %v", op, deduplicated, err)
	}
	canonical, err := filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(op.ID) {
		t.Errorf("operation ID = %q, want 32 lowercase hex", op.ID)
	}
	if op.RepoPath != canonical || op.BaseName != base.Name || op.ConfigFingerprint != base.Fingerprint ||
		op.RemoteFingerprint != base.RemoteFingerprint || op.Branch != base.Branch || op.Kind != gitcmd.OperationInitialize {
		t.Errorf("operation identity = %#v", op)
	}
	wantTime := fixed.UTC()
	if !op.CreatedAt.Equal(wantTime) || !op.UpdatedAt.Equal(wantTime) || op.CreatedAt.Nanosecond() != 987654321 || op.CreatedAt.Location() != time.UTC {
		t.Errorf("operation times = %v / %v, want exact UTC %v", op.CreatedAt, op.UpdatedAt, wantTime)
	}
	stored, found, err := fixture.operations.ActiveByPath(context.Background(), canonical)
	if err != nil || !found || !reflect.DeepEqual(stored, op) {
		t.Fatalf("durable operation = %#v, %v, %v; want %#v", stored, found, err, op)
	}
	status, found, err := fixture.statuses.Get(context.Background(), canonical)
	if err != nil || !found || status.State != model.GitStateInitializing || status.OperationID != op.ID || status.Stage != string(gitcmd.StageQueued) {
		t.Fatalf("queued status = %#v, %v, %v", status, found, err)
	}

	canonicalSnapshot := base
	canonicalSnapshot.Path = canonical
	fixture.snapshots.put(canonicalSnapshot)
	deduped, deduplicated, err := fixture.manager.QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: canonicalSnapshot})
	if err != nil || !deduplicated || deduped.ID != op.ID {
		t.Fatalf("init-vs-sync canonical dedupe = %#v, %v, %v; want %q", deduped, deduplicated, err, op.ID)
	}

	changed := canonicalSnapshot
	changed.Fingerprint = "changed-config"
	fixture.snapshots.put(changed)
	if _, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: changed}); !errors.Is(err, ErrGitRepositoryInUse) {
		t.Fatalf("changed fingerprint error = %v, want ErrGitRepositoryInUse", err)
	}
	unchanged, _, err := fixture.operations.ActiveByPath(context.Background(), canonical)
	if err != nil || unchanged.ID != op.ID || unchanged.ConfigFingerprint != base.Fingerprint {
		t.Fatalf("active operation was mutated: %#v, %v", unchanged, err)
	}
}

func TestGitManagerQueueDurableMapGapDedupeAndIdentityRejection(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
	op, deduplicated, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base})
	if err != nil || deduplicated {
		t.Fatalf("initial QueueInitialize() = %#v, %v, %v", op, deduplicated, err)
	}
	newProcessManager := func() *GitManager {
		manager := NewGitManager(
			fixture.manager.gitService, fixture.statuses, fixture.operations, fixture.manager.prober,
			fixture.snapshots.get, fixture.notes, fixture.coordinator,
		)
		t.Cleanup(func() { _ = manager.Close() })
		return manager
	}

	deduped, deduplicated, err := newProcessManager().QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: base})
	if err != nil || !deduplicated || deduped.ID != op.ID {
		t.Fatalf("durable map-gap dedupe = %#v, %v, %v; want %q", deduped, deduplicated, err, op.ID)
	}

	otherName := configuredManagerBase("other", base.Path)
	fixture.snapshots.put(otherName)
	if _, _, err := newProcessManager().QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: otherName}); !errors.Is(err, ErrGitRepositoryInUse) {
		t.Fatalf("durable distinct base-name error = %v, want ErrGitRepositoryInUse", err)
	}

	otherRemote := base
	otherRemote.RemoteFingerprint = "remote-reconfigured"
	fixture.snapshots.put(otherRemote)
	if _, _, err := newProcessManager().QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: otherRemote}); !errors.Is(err, ErrGitRepositoryInUse) {
		t.Fatalf("durable distinct remote-fingerprint error = %v, want ErrGitRepositoryInUse", err)
	}
	stored, found, err := fixture.operations.ActiveByPath(context.Background(), base.Path)
	if err != nil || !found || !reflect.DeepEqual(stored, op) {
		t.Fatalf("durable operation changed = %#v, %v, %v; want %#v", stored, found, err, op)
	}
}

func TestGitManagerQueueRejectsStaleBeforeDurableState(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
	stale := base
	stale.URL = "https://secret@example.invalid/forged.git"
	if _, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: stale}); err == nil {
		t.Fatal("QueueInitialize() stale error = nil")
	} else {
		requireManagerCode(t, err, gitcmd.CodeNeedsReconnect)
	}
	unfinished, err := fixture.operations.ListUnfinished(context.Background())
	if err != nil || len(unfinished) != 0 {
		t.Fatalf("unfinished = %#v, %v, want none", unfinished, err)
	}
	if _, found, err := fixture.statuses.Get(context.Background(), base.Path); err != nil || found {
		t.Fatalf("status found = %v, error = %v, want no write", found, err)
	}
}

func TestGitManagerQueueCoordinatorOrderingAndUnlockedManagerWait(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	for _, kind := range []gitcmd.OperationKind{gitcmd.OperationInitialize, gitcmd.OperationSync} {
		t.Run(string(kind), func(t *testing.T) {
			base := configuredManagerBase("work", t.TempDir())
			fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
			if kind == gitcmd.OperationSync {
				seedManagerTrust(t, fixture, base, managerOID)
			}
			snapshotCalled := make(chan struct{})
			fixture.snapshots.before = func(string) { close(snapshotCalled) }
			fixture.coordinator.Lock()
			coordinatorAttempting := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				close(coordinatorAttempting)
				if kind == gitcmd.OperationInitialize {
					_, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base})
					result <- err
					return
				}
				_, _, err := fixture.manager.QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: base})
				result <- err
			}()
			<-coordinatorAttempting
			runtime.Gosched()
			if !fixture.manager.mu.TryLock() {
				t.Fatal("queue retained manager mutex while waiting for coordinator")
			}
			fixture.manager.mu.Unlock()
			select {
			case <-snapshotCalled:
				t.Fatal("final snapshot ran before coordinator ownership")
			default:
			}
			fixture.coordinator.Unlock()
			if err := <-result; err != nil {
				t.Fatalf("queue error = %v", err)
			}
			<-snapshotCalled
		})
	}
}

func TestGitManagerQueueHoldsCoordinatorThroughJournalStatusAndFIFO(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	snapshotCalled := make(chan struct{})
	fixture.snapshots.before = func(string) { close(snapshotCalled) }
	queued := make(chan error, 1)
	go func() {
		_, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base})
		queued <- err
	}()
	<-snapshotCalled
	coordinatorAcquired := make(chan struct{})
	coordinatorAttempting := make(chan struct{})
	go func() {
		close(coordinatorAttempting)
		fixture.coordinator.Lock()
		close(coordinatorAcquired)
		fixture.coordinator.Unlock()
	}()
	<-coordinatorAttempting
	select {
	case <-coordinatorAcquired:
		t.Fatal("coordinator released while durable admission was blocked")
	default:
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-queued; err != nil {
		t.Fatal(err)
	}
	<-coordinatorAcquired
	fixture.manager.mu.Lock()
	queueLength := len(fixture.manager.queue)
	inFlight := fixture.manager.inFlight[base.Path]
	fixture.manager.mu.Unlock()
	if queueLength != 1 || inFlight.ID == "" {
		t.Fatalf("FIFO/inFlight publication = %d/%#v", queueLength, inFlight)
	}
}

func TestGitManagerQueueRaceRepeatedUsesCoherentImmutableSnapshot(t *testing.T) {
	for _, kind := range []gitcmd.OperationKind{gitcmd.OperationInitialize, gitcmd.OperationSync} {
		t.Run(string(kind), func(t *testing.T) {
			for _, queueFirst := range []bool{true, false} {
				outcome := "reconfigure first"
				if queueFirst {
					outcome = "queue first"
				}
				t.Run(outcome, func(t *testing.T) {
					for iteration := 0; iteration < 10; iteration++ {
						base := configuredManagerBase("work", t.TempDir())
						fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "work", nil, nil, nil)
						if kind == gitcmd.OperationSync {
							seedManagerTrust(t, fixture, base, managerOID)
						}
						beforeStatus, beforeStatusFound, err := fixture.statuses.Get(context.Background(), base.Path)
						if err != nil {
							t.Fatal(err)
						}
						beforeOperations, err := fixture.operations.ListUnfinished(context.Background())
						if err != nil {
							t.Fatal(err)
						}
						changed := base
						changed.URL = "https://example.invalid/changed.git"
						changed.Fingerprint = fmt.Sprintf("changed-%d", iteration)
						type queueResult struct {
							operation gitcmd.Operation
							deduped   bool
							err       error
						}
						queue := func() (gitcmd.Operation, bool, error) {
							if kind == gitcmd.OperationInitialize {
								return fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base})
							}
							return fixture.manager.QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: base})
						}
						queued := make(chan queueResult, 1)

						if queueFirst {
							snapshotEntered := make(chan struct{})
							releaseSnapshot := make(chan struct{})
							fixture.snapshots.before = func(string) {
								close(snapshotEntered)
								<-releaseSnapshot
							}
							go func() {
								op, deduped, err := queue()
								queued <- queueResult{operation: op, deduped: deduped, err: err}
							}()
							<-snapshotEntered
							reconfigureAttempting := make(chan struct{})
							reconfigured := make(chan struct{})
							go func() {
								close(reconfigureAttempting)
								fixture.coordinator.Lock()
								fixture.snapshots.mu.Lock()
								fixture.snapshots.bases[changed.Name] = changed
								fixture.snapshots.active = ""
								fixture.snapshots.mu.Unlock()
								fixture.coordinator.Unlock()
								close(reconfigured)
							}()
							<-reconfigureAttempting
							select {
							case <-reconfigured:
								t.Fatal("reconfiguration crossed queue admission")
							default:
							}
							close(releaseSnapshot)
							result := <-queued
							<-reconfigured
							if result.err != nil || result.deduped || result.operation.ConfigFingerprint != base.Fingerprint ||
								result.operation.RemoteFingerprint != base.RemoteFingerprint || result.operation.RepoPath != base.Path {
								t.Fatalf("iteration %d accepted operation = %#v, %v, %v", iteration, result.operation, result.deduped, result.err)
							}
							stored, found, err := fixture.operations.ActiveByPath(context.Background(), base.Path)
							status, statusFound, statusErr := fixture.statuses.Get(context.Background(), base.Path)
							fixture.manager.mu.Lock()
							fifo := append([]gitManagerJob(nil), fixture.manager.queue...)
							inFlight := fixture.manager.inFlight[base.Path]
							fixture.manager.mu.Unlock()
							if err != nil || !found || statusErr != nil || !statusFound || stored.ID != result.operation.ID ||
								status.OperationID != result.operation.ID || len(fifo) != 1 || fifo[0].operation.ID != result.operation.ID ||
								fifo[0].snapshot != base || inFlight.ID != result.operation.ID {
								t.Fatalf("iteration %d journal/status/FIFO/inFlight = %#v / %#v / %#v / %#v, errors %v/%v", iteration, stored, status, fifo, inFlight, err, statusErr)
							}
							continue
						}

						reconfigureOwned := make(chan struct{})
						releaseReconfigure := make(chan struct{})
						go func() {
							fixture.coordinator.Lock()
							fixture.snapshots.mu.Lock()
							fixture.snapshots.bases[changed.Name] = changed
							fixture.snapshots.active = ""
							fixture.snapshots.mu.Unlock()
							close(reconfigureOwned)
							<-releaseReconfigure
							fixture.coordinator.Unlock()
						}()
						<-reconfigureOwned
						queueAttempting := make(chan struct{})
						snapshotCalled := make(chan struct{})
						fixture.snapshots.before = func(string) { close(snapshotCalled) }
						go func() {
							close(queueAttempting)
							op, deduped, err := queue()
							queued <- queueResult{operation: op, deduped: deduped, err: err}
						}()
						<-queueAttempting
						select {
						case <-snapshotCalled:
							t.Fatal("queue snapshot crossed reconfiguration")
						default:
						}
						close(releaseReconfigure)
						result := <-queued
						requireManagerCode(t, result.err, gitcmd.CodeNeedsReconnect)
						if result.deduped || result.operation.ID != "" {
							t.Fatalf("iteration %d stale request = %#v, deduped %v", iteration, result.operation, result.deduped)
						}
						afterStatus, afterStatusFound, err := fixture.statuses.Get(context.Background(), base.Path)
						if err != nil {
							t.Fatal(err)
						}
						afterOperations, err := fixture.operations.ListUnfinished(context.Background())
						if err != nil {
							t.Fatal(err)
						}
						fixture.manager.mu.Lock()
						queueLength := len(fixture.manager.queue)
						inFlightLength := len(fixture.manager.inFlight)
						fixture.manager.mu.Unlock()
						if afterStatusFound != beforeStatusFound || !reflect.DeepEqual(afterStatus, beforeStatus) ||
							!reflect.DeepEqual(afterOperations, beforeOperations) || queueLength != 0 || inFlightLength != 0 {
							t.Fatalf("iteration %d stale request mutated status/journal/FIFO/inFlight", iteration)
						}
					}
				})
			}
		})
	}
}

func TestGitManagerQueueRejectsConflictAndSyncNeedsReconnect(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
	fixture.coordinator.SetConflict(base.Path, true)
	if _, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base}); !errors.Is(err, ErrGitConflictPending) {
		t.Fatalf("initialize conflict error = %v", err)
	}
	fixture.coordinator.SetConflict(base.Path, false)
	if err := fixture.statuses.Upsert(context.Background(), model.GitStatus{
		Base: base.Name, RepositoryPath: base.Path, State: model.GitStateNeedsReconnect,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.manager.QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: base}); err == nil {
		t.Fatal("sync needs_reconnect error = nil")
	} else {
		requireManagerCode(t, err, gitcmd.CodeNeedsReconnect)
	}
	if _, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base}); err != nil {
		t.Fatalf("initialize may replace needs_reconnect: %v", err)
	}
}

func TestGitManagerAdmissionTrustUsesPushOIDOnlyForProvenTerminalSuccess(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	status := model.GitStatus{
		Base: base.Name, RepositoryPath: base.Path, State: model.GitStateReady,
		RemoteOID: managerOID, ChangedPaths: []string{},
	}
	operation := gitcmd.Operation{
		BaseName: base.Name, RepoPath: base.Path, ConfigFingerprint: base.Fingerprint,
		RemoteFingerprint: base.RemoteFingerprint, State: gitcmd.OperationSucceeded,
		Stage: gitcmd.StageCompleted, PushOID: managerOID,
	}
	trusted, err := managerAdmissionTrust(gitcmd.OperationSync, base, status, true, operation, true)
	if err != nil || trusted != managerOID {
		t.Fatalf("proven terminal success trust = %q, %v", trusted, err)
	}

	for _, test := range []struct {
		name        string
		state       gitcmd.OperationState
		stage       gitcmd.Stage
		statusState model.GitState
	}{
		{name: "failed", state: gitcmd.OperationFailed, stage: gitcmd.StagePushing, statusState: model.GitStateReady},
		{name: "conflict", state: gitcmd.OperationConflict, stage: gitcmd.StageMerging, statusState: model.GitStateReady},
		{name: "incomplete success", state: gitcmd.OperationSucceeded, stage: gitcmd.StagePushing, statusState: model.GitStateReady},
		{name: "non-ready status", state: gitcmd.OperationSucceeded, stage: gitcmd.StageCompleted, statusState: model.GitStateError},
	} {
		t.Run(test.name, func(t *testing.T) {
			operation.State = test.state
			operation.Stage = test.stage
			status.State = test.statusState
			if trusted, err := managerAdmissionTrust(gitcmd.OperationSync, base, status, true, operation, true); err == nil {
				t.Fatalf("unproven PushOID trust = %q, nil error", trusted)
			} else {
				requireManagerCode(t, err, gitcmd.CodeNeedsReconnect)
			}
		})
	}
}

func TestGitManagerQueueStatusPersistenceFailureFinishesWithoutFIFO(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
	if _, err := fixture.db.Exec(`CREATE TRIGGER reject_initializing BEFORE INSERT ON git_status
		WHEN NEW.state = 'initializing' BEGIN SELECT RAISE(ABORT, 'status unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	op, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base})
	if err == nil {
		t.Fatal("QueueInitialize() error = nil")
	}
	requireManagerCode(t, err, gitcmd.CodeCommandFailed)
	stored, found, lookupErr := fixture.operations.LatestByPath(context.Background(), base.Path)
	if lookupErr != nil || !found || stored.ID != op.ID || stored.State != gitcmd.OperationFailed || stored.Error == nil || stored.Error.Code != gitcmd.CodeCommandFailed {
		t.Fatalf("failed admission = %#v, %v, %v", stored, found, lookupErr)
	}
	fixture.manager.mu.Lock()
	defer fixture.manager.mu.Unlock()
	if len(fixture.manager.queue) != 0 || len(fixture.manager.inFlight) != 0 {
		t.Fatalf("failed admission queue/inFlight = %d/%d", len(fixture.manager.queue), len(fixture.manager.inFlight))
	}
}

func TestGitManagerQueueCompensationFailureRetainsActiveOperationAndReportsBothWrites(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
	if _, err := fixture.db.Exec(`CREATE TRIGGER reject_initial_status BEFORE INSERT ON git_status
		WHEN NEW.state = 'initializing' BEGIN SELECT RAISE(ABORT, 'secret initial status failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`CREATE TRIGGER reject_admission_compensation BEFORE UPDATE ON git_operations
		WHEN NEW.state = 'failed' BEGIN SELECT RAISE(ABORT, 'secret compensation failure'); END`); err != nil {
		t.Fatal(err)
	}
	op, deduplicated, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base})
	if err == nil || deduplicated {
		t.Fatalf("QueueInitialize() = %#v, %v, %v; want compensated admission error", op, deduplicated, err)
	}
	message := err.Error()
	if !strings.Contains(message, "Git status persistence failed") || !strings.Contains(message, "Git operation journal persistence failed") || strings.Contains(message, "secret") {
		t.Fatalf("safe combined compensation error = %q", message)
	}
	active, found, lookupErr := fixture.operations.ActiveByPath(context.Background(), base.Path)
	if lookupErr != nil || !found || active.ID != op.ID {
		t.Fatalf("active journal = %#v, %v, %v", active, found, lookupErr)
	}
	fixture.manager.mu.Lock()
	retained := fixture.manager.inFlight[base.Path]
	queueLength := len(fixture.manager.queue)
	fixture.manager.mu.Unlock()
	if retained.ID != op.ID || queueLength != 0 {
		t.Fatalf("retained inFlight/FIFO = %#v/%d", retained, queueLength)
	}
	deduped, deduplicated, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base})
	if err != nil || !deduplicated || deduped.ID != op.ID {
		t.Fatalf("retry dedupe = %#v, %v, %v; want retained active operation", deduped, deduplicated, err)
	}
}

func TestGitManagerQueueCancellationCompensatesWithDetachedBoundedContext(t *testing.T) {
	enteredStatusWrite := make(chan struct{})
	releaseStatusWrite := make(chan struct{})
	functionName := fmt.Sprintf("manager_block_status_%d", managerSQLiteFunctionID.Add(1))
	if err := sqlite.RegisterScalarFunction(functionName, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		close(enteredStatusWrite)
		<-releaseStatusWrite
		return int64(0), nil
	}); err != nil {
		t.Fatal(err)
	}
	base := configuredManagerBase("work", t.TempDir())
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
	if _, err := fixture.db.Exec(fmt.Sprintf(`CREATE TRIGGER block_canceled_admission BEFORE INSERT ON git_status
		WHEN NEW.state = 'initializing' BEGIN SELECT %s(); END`, functionName)); err != nil {
		t.Fatal(err)
	}
	type queueResult struct {
		operation gitcmd.Operation
		err       error
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan queueResult, 1)
	go func() {
		op, _, err := fixture.manager.QueueInitialize(ctx, gitcmd.InitializeRequest{Snapshot: base})
		result <- queueResult{operation: op, err: err}
	}()
	<-enteredStatusWrite
	cancel()
	close(releaseStatusWrite)
	queued := <-result
	if queued.err == nil || !strings.Contains(queued.err.Error(), "Git status persistence failed") ||
		strings.Contains(queued.err.Error(), "Git operation journal persistence failed") {
		t.Fatalf("QueueInitialize() error = %v, want compensated cancellation status failure", queued.err)
	}
	stored, found, err := fixture.operations.LatestByPath(context.Background(), base.Path)
	if err != nil || !found || stored.ID != queued.operation.ID || stored.State != gitcmd.OperationFailed {
		t.Fatalf("compensated canceled admission = %#v, %v, %v", stored, found, err)
	}
	if active, found, err := fixture.operations.ActiveByPath(context.Background(), base.Path); err != nil || found {
		t.Fatalf("active admission after cancellation = %#v, %v, %v", active, found, err)
	}
	fixture.manager.mu.Lock()
	queueLength := len(fixture.manager.queue)
	inFlightLength := len(fixture.manager.inFlight)
	fixture.manager.mu.Unlock()
	if queueLength != 0 || inFlightLength != 0 {
		t.Fatalf("canceled admission FIFO/inFlight = %d/%d", queueLength, inFlightLength)
	}
}

func TestGitManagerInitializeReprobesAndRequiresCurrentConfirmations(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	probe := permissiveManagerPorcelain(base.Path)
	probe.local.HasRepository = false
	probe.local.RepositoryRoot = ""
	probe.local.ExistingOriginURL = ""
	probe.local.HasCommits = false
	probe.remote = gitcmd.RemoteInspection{Empty: true, Branches: map[string]string{}}
	var runnerCalls atomic.Int32
	runner := managerRunnerFunc(func(context.Context, gitcmd.Command) (gitcmd.Result, error) {
		runnerCalls.Add(1)
		return gitcmd.Result{}, errors.New("mutation must not run")
	})
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", runner, permissiveManagerPorcelain(base.Path), probe)
	if _, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{
		Snapshot: base, Confirmations: model.GitConfirmations{CreateBranch: true},
	}); err != nil {
		t.Fatal(err)
	}
	waitManagerTerminal(t, fixture.manager)
	op, _, err := fixture.operations.LatestByPath(context.Background(), base.Path)
	if err != nil || op.State != gitcmd.OperationFailed || op.Error == nil || op.Error.Code != gitcmd.CodeConfirmationRequired {
		t.Fatalf("operation = %#v, error = %v", op, err)
	}
	if runnerCalls.Load() != 0 {
		t.Fatalf("mutating service calls = %d, want zero", runnerCalls.Load())
	}
	probe.mu.Lock()
	calls := append([]string(nil), probe.calls...)
	probe.mu.Unlock()
	if !slices.Contains(calls, "remote") || !slices.Contains(calls, "local") {
		t.Fatalf("current probe calls = %v", calls)
	}
}

func TestGitManagerWorkerFreshnessMismatchOnlyFinishesJournal(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
	op, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base})
	if err != nil {
		t.Fatal(err)
	}
	reconciled := model.GitStatus{Base: "replacement", RepositoryPath: base.Path, State: model.GitStateReady, RemoteOID: managerOID, ChangedPaths: []string{"keep.md"}}
	if err := fixture.statuses.Upsert(context.Background(), reconciled); err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.Fingerprint = "replacement"
	fixture.snapshots.put(changed)
	waitManagerTerminal(t, fixture.manager)
	stored, _, err := fixture.operations.LatestByPath(context.Background(), base.Path)
	if err != nil || stored.ID != op.ID || stored.State != gitcmd.OperationFailed || stored.Error == nil || stored.Error.Code != gitcmd.CodeNeedsReconnect {
		t.Fatalf("stale operation = %#v, %v", stored, err)
	}
	status, _, err := fixture.statuses.Get(context.Background(), base.Path)
	if err != nil || !reflect.DeepEqual(status, reconciled) {
		t.Fatalf("reconciled status overwritten: %#v, %v", status, err)
	}
}

func TestGitManagerWorkerSafeFailurePublication(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	secretURL := "https://user:token@example.invalid/secret.git"
	base.URL = secretURL
	porcelain := permissiveManagerPorcelain(base.Path)
	porcelain.local.ExistingOriginURL = secretURL
	runner := managerRunnerFunc(func(context.Context, gitcmd.Command) (gitcmd.Result, error) {
		return gitcmd.Result{Stderr: secretURL}, &gitcmd.SafeError{Code: gitcmd.CodeAuthentication, Message: "Git authentication failed"}
	})
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", runner, porcelain, porcelain)
	seedManagerTrust(t, fixture, base, managerOID)
	if _, _, err := fixture.manager.QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: base}); err != nil {
		t.Fatal(err)
	}
	waitManagerTerminal(t, fixture.manager)
	op, _, _ := fixture.operations.LatestByPath(context.Background(), base.Path)
	status, _, _ := fixture.statuses.Get(context.Background(), base.Path)
	if op.State != gitcmd.OperationFailed || op.Error == nil || op.Error.Code != gitcmd.CodeAuthentication ||
		status.State != model.GitStateError || status.Error == nil || status.Error.Code != string(gitcmd.CodeAuthentication) {
		t.Fatalf("failure operation/status = %#v / %#v", op, status)
	}
	if strings.Contains(fmt.Sprintf("%#v %#v", op.Error, status.Error), secretURL) || strings.Contains(status.Error.Message, "token") {
		t.Fatalf("unsafe failure publication = %#v / %#v", op.Error, status.Error)
	}
}

func TestGitManagerFailurePreservesHistoricalStatusFields(t *testing.T) {
	base := configuredManagerBase("work", t.TempDir())
	runner := managerRunnerFunc(func(context.Context, gitcmd.Command) (gitcmd.Result, error) {
		return gitcmd.Result{}, &gitcmd.SafeError{Code: gitcmd.CodeAuthentication, Message: "Git authentication failed"}
	})
	porcelain := permissiveManagerPorcelain(base.Path)
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", runner, porcelain, porcelain)
	seedManagerTrust(t, fixture, base, managerOID)
	lastAttempt := time.Date(2026, 9, 2, 7, 0, 0, 0, time.UTC)
	lastSuccess := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	previous := model.GitStatus{
		Base: base.Name, RepositoryPath: base.Path, State: model.GitStateReady,
		OperationID: "previous-operation", Stage: string(gitcmd.StageCompleted), Ahead: 7, Behind: 3,
		ConsecutiveFailures: 4, LastAttempt: &lastAttempt, LastSuccess: &lastSuccess,
		ChangedPaths: []string{"historical.md"}, RemoteOID: managerOID,
	}
	if err := fixture.statuses.Upsert(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	failedAt := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
	fixture.manager.now = func() time.Time { return failedAt }
	queued, _, err := fixture.manager.QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: base})
	if err != nil {
		t.Fatal(err)
	}
	waitManagerTerminal(t, fixture.manager)
	status, found, err := fixture.statuses.Get(context.Background(), base.Path)
	if err != nil || !found {
		t.Fatalf("failure status = %#v, %v, %v", status, found, err)
	}
	if status.State != model.GitStateError || status.OperationID != queued.ID || status.Stage != string(gitcmd.StageQueued) ||
		status.Error == nil || status.Error.Code != string(gitcmd.CodeAuthentication) || status.LastAttempt == nil || !status.LastAttempt.Equal(failedAt) {
		t.Fatalf("failure transition fields = %#v", status)
	}
	if status.LastSuccess == nil || !status.LastSuccess.Equal(lastSuccess) || status.Ahead != previous.Ahead ||
		status.Behind != previous.Behind || status.ConsecutiveFailures != previous.ConsecutiveFailures ||
		!reflect.DeepEqual(status.ChangedPaths, previous.ChangedPaths) || status.RemoteOID != previous.RemoteOID {
		t.Fatalf("failure erased historical status: before %#v, after %#v", previous, status)
	}
}

type mergeConflictRunner struct {
	gitDir string
	oid    string
}

func (r *mergeConflictRunner) Run(_ context.Context, command gitcmd.Command) (gitcmd.Result, error) {
	switch {
	case slices.Equal(command.Args, []string{"rev-parse", "--absolute-git-dir"}):
		return gitcmd.Result{Stdout: r.gitDir + "\n"}, nil
	case slices.Equal(command.Args, []string{"rev-parse", "--verify", "MERGE_HEAD^{commit}"}):
		return gitcmd.Result{Stdout: r.oid + "\n"}, nil
	case slices.Equal(command.Args, []string{"ls-files", "-u", "-z"}):
		line := "100644 " + r.oid + " 2\tz.md\x00" + "100644 " + r.oid + " 1\ta.md\x00" + "100644 " + r.oid + " 3\tz.md\x00"
		return gitcmd.Result{Stdout: line}, nil
	default:
		return gitcmd.Result{}, fmt.Errorf("unexpected Git command: %v", command.Args)
	}
}

func TestGitManagerConflictHandoffActiveAndInactiveBeforePublication(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprintf("active=%v", active), func(t *testing.T) {
			path := t.TempDir()
			gitDir := filepath.Join(path, ".git")
			if err := os.Mkdir(gitDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(managerOID+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			base := configuredManagerBase("work", path)
			porcelain := permissiveManagerPorcelain(path)
			porcelain.local.PendingOperation = "merge"
			porcelain.local.GitDir = gitDir
			activeName := ""
			if active {
				activeName = base.Name
			}
			fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, activeName, &mergeConflictRunner{gitDir: gitDir, oid: managerOID}, porcelain, porcelain)
			seedManagerTrust(t, fixture, base, managerOID)
			if active {
				fixture.notes.scan = func(root *os.Root) ([]model.NoteNode, error) {
					if !errors.Is(fixture.coordinator.CheckMutation(path), ErrGitConflictPending) {
						t.Error("conflict gate was open during active reindex")
					}
					return scanNotes(root)
				}
			}
			publicationChecked := make(chan struct{})
			fixture.manager.beforeTerminalPublication = func() {
				if !errors.Is(fixture.coordinator.CheckMutation(path), ErrGitConflictPending) {
					t.Error("conflict gate was open before terminal publication")
				}
				close(publicationChecked)
			}
			if _, _, err := fixture.manager.QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: base}); err != nil {
				t.Fatal(err)
			}
			if err := fixture.manager.Start(); err != nil {
				t.Fatal(err)
			}
			<-publicationChecked
			if err := fixture.manager.Close(); err != nil {
				t.Fatal(err)
			}
			op, _, _ := fixture.operations.LatestByPath(context.Background(), path)
			status, _, _ := fixture.statuses.Get(context.Background(), path)
			if op.State != gitcmd.OperationConflict || !reflect.DeepEqual(op.ConflictPaths, []string{"a.md", "z.md"}) ||
				status.State != model.GitStateConflict || !reflect.DeepEqual(status.ChangedPaths, []string{"a.md", "z.md"}) {
				t.Fatalf("conflict operation/status = %#v / %#v", op, status)
			}
		})
	}
}

func TestGitManagerActiveConflictGatesExactCallbackIdentityBeforeReindexAndReturn(t *testing.T) {
	physical := t.TempDir()
	aliases := t.TempDir()
	staleQueuedPath := filepath.Join(aliases, "stale-queued")
	currentPath := filepath.Join(aliases, "current")
	if err := os.Symlink(physical, staleQueuedPath); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.Symlink(physical, currentPath); err != nil {
		t.Skipf("symlink: %v", err)
	}
	base := configuredManagerBase("work", currentPath)
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, base.Name, nil, nil, nil)
	callbackSeen := false
	fixture.notes.scan = func(root *os.Root) ([]model.NoteNode, error) {
		if !callbackSeen {
			t.Error("active reindex started before mutation callback")
		}
		if !errors.Is(fixture.coordinator.CheckMutation(physical), ErrGitConflictPending) {
			t.Error("canonical callback identity was not gated before active reindex")
		}
		if errors.Is(fixture.coordinator.CheckMutation(currentPath), ErrGitConflictPending) {
			t.Error("current snapshot path was gated instead of the exact callback identity")
		}
		if errors.Is(fixture.coordinator.CheckMutation(staleQueuedPath), ErrGitConflictPending) {
			t.Error("stale queued path was gated instead of the exact callback identity")
		}
		return scanNotes(root)
	}
	transaction := fixture.manager.worktreeTransaction(base, true)
	wantErr := &gitcmd.ConflictError{Paths: []string{"note.md"}}
	err := transaction(context.Background(), func(callbackPath string) error {
		callbackSeen = true
		if callbackPath != physical {
			t.Fatalf("callback path = %q, want canonical identity %q", callbackPath, physical)
		}
		return fmt.Errorf("wrapped callback conflict: %w", wantErr)
	})
	var conflict *gitcmd.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("transaction error = %v, want wrapped ConflictError", err)
	}
	if !errors.Is(fixture.coordinator.CheckMutation(physical), ErrGitConflictPending) {
		t.Fatal("canonical callback identity gate reopened before transaction return")
	}
}

func TestGitManagerConflictPersistenceFailureNeverClearsGate(t *testing.T) {
	path := t.TempDir()
	gitDir := filepath.Join(path, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(managerOID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := configuredManagerBase("work", path)
	porcelain := permissiveManagerPorcelain(path)
	porcelain.local.PendingOperation = "merge"
	porcelain.local.GitDir = gitDir
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", &mergeConflictRunner{gitDir: gitDir, oid: managerOID}, porcelain, porcelain)
	seedManagerTrust(t, fixture, base, managerOID)
	if _, err := fixture.db.Exec(`CREATE TRIGGER reject_conflict_status BEFORE UPDATE ON git_status
		WHEN NEW.state = 'conflict' BEGIN SELECT RAISE(ABORT, 'status unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.manager.QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: base}); err != nil {
		t.Fatal(err)
	}
	beforeTerminal := make(chan struct{})
	releaseTerminal := make(chan struct{})
	fixture.manager.beforeTerminalPublication = func() {
		close(beforeTerminal)
		<-releaseTerminal
	}
	if err := fixture.manager.Start(); err != nil {
		t.Fatal(err)
	}
	<-beforeTerminal
	close(releaseTerminal)
	if err := fixture.manager.Close(); err == nil || !strings.Contains(err.Error(), "Git status persistence failed") {
		t.Fatalf("Close() error = %v, want safe status persistence failure", err)
	}
	if !errors.Is(fixture.coordinator.CheckMutation(path), ErrGitConflictPending) {
		t.Fatal("conflict persistence failure reopened mutation gate")
	}
	op, _, _ := fixture.operations.LatestByPath(context.Background(), path)
	if op.State != gitcmd.OperationConflict {
		t.Fatalf("durable conflict = %#v", op)
	}
}

func TestGitManagerTerminalWriteFailuresAreSafeReportedAndRecoverable(t *testing.T) {
	for _, test := range []struct {
		name        string
		journalFail bool
		statusFail  bool
	}{
		{name: "journal", journalFail: true},
		{name: "status", statusFail: true},
		{name: "journal and status", journalFail: true, statusFail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := configuredManagerBase("work", t.TempDir())
			probe := permissiveManagerPorcelain(base.Path)
			probe.local.HasRepository = false
			probe.local.RepositoryRoot = ""
			probe.local.ExistingOriginURL = ""
			probe.local.HasCommits = false
			probe.remote = gitcmd.RemoteInspection{Empty: true, Branches: map[string]string{}}
			fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, permissiveManagerPorcelain(base.Path), probe)
			op, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{
				Snapshot: base, Confirmations: model.GitConfirmations{CreateBranch: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.journalFail {
				if _, err := fixture.db.Exec(`CREATE TRIGGER reject_terminal_journal BEFORE UPDATE ON git_operations
					WHEN NEW.state IN ('failed', 'succeeded', 'conflict') BEGIN SELECT RAISE(ABORT, 'secret terminal journal failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if test.statusFail {
				if _, err := fixture.db.Exec(`CREATE TRIGGER reject_terminal_status BEFORE UPDATE ON git_status
					WHEN NEW.state IN ('error', 'ready', 'conflict') BEGIN SELECT RAISE(ABORT, 'secret terminal status failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			beforeTerminal := make(chan struct{})
			releaseTerminal := make(chan struct{})
			fixture.manager.beforeTerminalPublication = func() {
				close(beforeTerminal)
				<-releaseTerminal
			}
			if err := fixture.manager.Start(); err != nil {
				t.Fatal(err)
			}
			<-beforeTerminal
			coordinatorAttempting := make(chan struct{})
			coordinatorAcquired := make(chan struct{})
			go func() {
				close(coordinatorAttempting)
				fixture.coordinator.Lock()
				close(coordinatorAcquired)
				fixture.coordinator.Unlock()
			}()
			<-coordinatorAttempting
			close(releaseTerminal)
			<-coordinatorAcquired

			fixture.manager.mu.Lock()
			retained := fixture.manager.inFlight[base.Path]
			fixture.manager.mu.Unlock()
			latest, _, lookupErr := fixture.operations.LatestByPath(context.Background(), base.Path)
			if lookupErr != nil {
				t.Fatal(lookupErr)
			}
			if test.journalFail {
				if retained.ID != op.ID || latest.State != gitcmd.OperationRunning {
					t.Fatalf("journal failure retained/state = %#v/%q", retained, latest.State)
				}
				deduped, deduplicated, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: base})
				if err != nil || !deduplicated || deduped.ID != op.ID {
					t.Fatalf("failed-terminal retry = %#v, %v, %v", deduped, deduplicated, err)
				}
			} else if retained.ID != "" || latest.State != gitcmd.OperationFailed {
				t.Fatalf("status-only failure retained/state = %#v/%q", retained, latest.State)
			}

			closeErr := fixture.manager.Close()
			if closeErr == nil {
				t.Fatal("Close() error = nil, want terminal persistence report")
			}
			message := closeErr.Error()
			if test.journalFail && !strings.Contains(message, "Git operation journal persistence failed") {
				t.Errorf("Close() error = %q, missing journal failure", message)
			}
			if test.statusFail && !strings.Contains(message, "Git status persistence failed") {
				t.Errorf("Close() error = %q, missing status failure", message)
			}
			if strings.Contains(message, "secret") {
				t.Fatalf("Close() leaked repository diagnostic: %q", message)
			}
			if repeated := fixture.manager.Close(); repeated == nil || repeated.Error() != closeErr.Error() {
				t.Fatalf("second Close() error = %v, want stable %v", repeated, closeErr)
			}
			if test.journalFail {
				if _, err := fixture.db.Exec("DROP TRIGGER reject_terminal_journal"); err != nil {
					t.Fatal(err)
				}
				if test.statusFail {
					if _, err := fixture.db.Exec("DROP TRIGGER reject_terminal_status"); err != nil {
						t.Fatal(err)
					}
				}
				restarted := NewGitManager(
					fixture.manager.gitService, fixture.statuses, fixture.operations, fixture.manager.prober,
					fixture.snapshots.get, fixture.notes, fixture.coordinator,
				)
				t.Cleanup(func() { _ = restarted.Close() })
				if err := restarted.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base}); err != nil {
					t.Fatalf("restart RecoverLocal() error = %v", err)
				}
				if active, found, err := fixture.operations.ActiveByPath(context.Background(), base.Path); err != nil || found {
					t.Fatalf("active journal after restart recovery = %#v, %v, %v", active, found, err)
				}
			}
		})
	}
}

func TestGitManagerShutdownCancelsCurrentAndStartsNoNextJob(t *testing.T) {
	first := configuredManagerBase("first", t.TempDir())
	second := configuredManagerBase("second", t.TempDir())
	entered := make(chan struct{})
	var calls atomic.Int32
	runner := managerRunnerFunc(func(ctx context.Context, _ gitcmd.Command) (gitcmd.Result, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-ctx.Done()
		return gitcmd.Result{}, ctx.Err()
	})
	porcelain := permissiveManagerPorcelain(first.Path)
	porcelain.local.RepositoryRoot = ""
	porcelain.local.HasRepository = false
	porcelain.local.ExistingOriginURL = ""
	porcelain.remote = gitcmd.RemoteInspection{Empty: true, Branches: map[string]string{}}
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{first, second}, "", runner, porcelain, porcelain)
	confirmations := model.GitConfirmations{CreateRepository: true, CreateBranch: true}
	if _, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: first, Confirmations: confirmations}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: second, Confirmations: confirmations}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.Start(); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := fixture.manager.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("Git calls = %d, want current job only", calls.Load())
	}
	firstOp, _, _ := fixture.operations.LatestByPath(context.Background(), first.Path)
	secondOp, _, _ := fixture.operations.LatestByPath(context.Background(), second.Path)
	if firstOp.State != gitcmd.OperationFailed || firstOp.Error == nil || firstOp.Error.Code != gitcmd.CodeCanceled || secondOp.State != gitcmd.OperationQueued {
		t.Fatalf("shutdown operations = %#v / %#v", firstOp, secondOp)
	}
}

func TestGitManagerRunsRepositoriesSequentiallyInGlobalFIFO(t *testing.T) {
	first := configuredManagerBase("first", t.TempDir())
	second := configuredManagerBase("second", t.TempDir())
	probe := permissiveManagerPorcelain(first.Path)
	probe.local.HasRepository = false
	probe.local.RepositoryRoot = ""
	probe.local.ExistingOriginURL = ""
	probe.local.HasCommits = false
	probe.remote = gitcmd.RemoteInspection{Empty: true, Branches: map[string]string{}}
	runner := managerRunnerFunc(func(context.Context, gitcmd.Command) (gitcmd.Result, error) {
		return gitcmd.Result{}, &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Git command failed"}
	})
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{first, second}, "", runner, probe, probe)
	confirmations := model.GitConfirmations{CreateRepository: true, CreateBranch: true}
	if _, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: first, Confirmations: confirmations}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{Snapshot: second, Confirmations: confirmations}); err != nil {
		t.Fatal(err)
	}
	firstTerminal := make(chan struct{})
	secondTerminal := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	var terminalCount atomic.Int32
	fixture.manager.beforeTerminalPublication = func() {
		switch terminalCount.Add(1) {
		case 1:
			close(firstTerminal)
			<-releaseFirst
		case 2:
			close(secondTerminal)
			<-releaseSecond
		}
	}
	if err := fixture.manager.Start(); err != nil {
		t.Fatal(err)
	}
	<-firstTerminal
	select {
	case <-secondTerminal:
		t.Fatal("second repository reached terminal publication while first was in flight")
	default:
	}
	close(releaseFirst)
	<-secondTerminal
	close(releaseSecond)
	if err := fixture.manager.Close(); err != nil {
		t.Fatal(err)
	}
	firstOp, _, _ := fixture.operations.LatestByPath(context.Background(), first.Path)
	secondOp, _, _ := fixture.operations.LatestByPath(context.Background(), second.Path)
	if firstOp.State != gitcmd.OperationFailed || secondOp.State != gitcmd.OperationFailed || terminalCount.Load() != 2 {
		t.Fatalf("FIFO terminal operations = %#v / %#v, count %d", firstOp, secondOp, terminalCount.Load())
	}
}

func TestGitManagerInitializeSuccessPublishesChangedPathsAndExactTimes(t *testing.T) {
	root, remote := newManagerGitPair(t)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := configuredManagerBase("work", root)
	base.URL = remote
	runner := gitcmd.NewCommandRunner()
	client := gitcmd.NewClient(runner)
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", runner, client, client)
	admitted := time.Date(2026, 9, 3, 10, 11, 12, 123456789, time.UTC)
	finished := time.Date(2026, 9, 3, 10, 12, 13, 987000000, time.UTC)
	var clockCalls atomic.Int32
	fixture.manager.now = func() time.Time {
		if clockCalls.Add(1) == 1 {
			return admitted
		}
		return finished
	}
	if _, _, err := fixture.manager.QueueInitialize(context.Background(), gitcmd.InitializeRequest{
		Snapshot: base, Confirmations: model.GitConfirmations{CreateRepository: true, CreateBranch: true},
	}); err != nil {
		t.Fatal(err)
	}
	waitManagerTerminal(t, fixture.manager)
	op, _, _ := fixture.operations.LatestByPath(context.Background(), root)
	status, _, _ := fixture.statuses.Get(context.Background(), root)
	if op.State != gitcmd.OperationSucceeded || op.Stage != gitcmd.StageCompleted || !reflect.DeepEqual(op.ChangedPaths, []string{"note.md"}) {
		t.Fatalf("successful operation = %#v", op)
	}
	if status.State != model.GitStateReady || status.RemoteOID == "" || !reflect.DeepEqual(status.ChangedPaths, []string{"note.md"}) ||
		status.LastAttempt == nil || status.LastSuccess == nil || !status.LastAttempt.Equal(finished) || !status.LastSuccess.Equal(finished) {
		t.Fatalf("successful status = %#v", status)
	}
}

func newManagerGitPair(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	rootDir := t.TempDir()
	home := filepath.Join(rootDir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	runManagerGit(t, rootDir, "config", "--global", "user.name", "IGoNotes Test")
	runManagerGit(t, rootDir, "config", "--global", "user.email", "igonotes@example.invalid")
	remote := filepath.Join(rootDir, "remote.git")
	runManagerGit(t, rootDir, "init", "--bare", "--initial-branch", "main", remote)
	root := filepath.Join(rootDir, "notes")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root, remote
}

func runManagerGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func newManagerRecoveryFixture(t *testing.T) (*gitManagerFixture, gitcmd.ConfiguredBase, string, *atomic.Int32) {
	t.Helper()
	root, remote := newManagerGitPair(t)
	runManagerGit(t, root, "init", "--initial-branch", "main")
	runManagerGit(t, root, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("clean\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, root, "add", "--all")
	runManagerGit(t, root, "commit", "-m", "initial")
	head := runManagerGit(t, root, "rev-parse", "HEAD")
	runManagerGit(t, root, "update-ref", "refs/igonotes/remotes/main", head)
	base := configuredManagerBase("work", root)
	base.URL = remote
	delegate := gitcmd.NewCommandRunner()
	network := &atomic.Int32{}
	runner := managerRunnerFunc(func(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
		if command.Scope == gitcmd.NetworkOperation {
			network.Add(1)
			return gitcmd.Result{}, errors.New("network forbidden")
		}
		return delegate.Run(ctx, command)
	})
	client := gitcmd.NewClient(runner)
	return newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", runner, client, client), base, head, network
}

func TestGitManagerCleanRecoveryPreservesHistoricalStatusFields(t *testing.T) {
	fixture, base, head, network := newManagerRecoveryFixture(t)
	lastAttempt := time.Date(2026, 9, 2, 7, 0, 0, 0, time.UTC)
	lastSuccess := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	previous := model.GitStatus{
		Base: base.Name, RepositoryPath: base.Path, State: model.GitStateError,
		OperationID: "historical-operation", Stage: string(gitcmd.StagePushing), Ahead: 5, Behind: 2,
		ConsecutiveFailures: 3, LastAttempt: &lastAttempt, LastSuccess: &lastSuccess,
		ChangedPaths: []string{"historical.md"}, RemoteOID: strings.Repeat("0", 40),
		Error: &model.APIError{Code: string(gitcmd.CodeAuthentication), Message: "Git authentication failed"},
	}
	if err := fixture.statuses.Upsert(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base}); err != nil {
		t.Fatal(err)
	}
	status, found, err := fixture.statuses.Get(context.Background(), base.Path)
	if err != nil || !found {
		t.Fatalf("clean recovery status = %#v, %v, %v", status, found, err)
	}
	if status.State != model.GitStateReady || status.Error != nil || status.RemoteOID != head {
		t.Fatalf("clean recovery transition fields = %#v", status)
	}
	if status.OperationID != previous.OperationID || status.Stage != previous.Stage || status.Ahead != previous.Ahead ||
		status.Behind != previous.Behind || status.ConsecutiveFailures != previous.ConsecutiveFailures ||
		status.LastAttempt == nil || !status.LastAttempt.Equal(lastAttempt) || status.LastSuccess == nil ||
		!status.LastSuccess.Equal(lastSuccess) || !reflect.DeepEqual(status.ChangedPaths, previous.ChangedPaths) {
		t.Fatalf("clean recovery erased historical status: before %#v, after %#v", previous, status)
	}
	if err := fixture.coordinator.CheckMutation(base.Path); err != nil {
		t.Fatalf("clean recovery mutation gate = %v", err)
	}
	if network.Load() != 0 {
		t.Fatalf("recovery network calls = %d", network.Load())
	}
}

func TestGitManagerRecoveryIgnoresTerminalConflictFromStaleIdentity(t *testing.T) {
	fixture, base, head, network := newManagerRecoveryFixture(t)
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	stale := gitcmd.Operation{
		ID: strings.Repeat("5", 32), BaseName: base.Name, RepoPath: base.Path,
		ConfigFingerprint: "stale-config", RemoteFingerprint: "stale-remote",
		Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageMerging,
		Branch: base.Branch, RemoteOID: head, CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.operations.CreateQueued(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	stale.State = gitcmd.OperationConflict
	stale.Error = &gitcmd.SafeError{Code: gitcmd.CodeGitConflict, Message: "Git merge has conflicts"}
	stale.ConflictPaths = []string{"obsolete.md"}
	if err := fixture.operations.Finish(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base}); err != nil {
		t.Fatal(err)
	}
	status, found, err := fixture.statuses.Get(context.Background(), base.Path)
	if err != nil || !found || status.State != model.GitStateReady || len(status.ChangedPaths) != 0 || status.Error != nil {
		t.Fatalf("stale conflict recovery status = %#v, %v, %v", status, found, err)
	}
	if err := fixture.coordinator.CheckMutation(base.Path); err != nil {
		t.Fatalf("stale conflict closed current mutation gate: %v", err)
	}
	if network.Load() != 0 {
		t.Fatalf("recovery network calls = %d", network.Load())
	}
}

func TestGitManagerRecoveryStatusWriteFailureUsesStatusPersistenceError(t *testing.T) {
	for _, transitionFailure := range []bool{false, true} {
		name := "clean"
		if transitionFailure {
			name = "with journal transition failure"
		}
		t.Run(name, func(t *testing.T) {
			fixture, base, head, _ := newManagerRecoveryFixture(t)
			if transitionFailure {
				now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
				op := gitcmd.Operation{
					ID: strings.Repeat("4", 32), BaseName: base.Name, RepoPath: base.Path,
					ConfigFingerprint: base.Fingerprint, RemoteFingerprint: base.RemoteFingerprint,
					Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageFetching,
					Branch: base.Branch, RemoteOID: head, CreatedAt: now, UpdatedAt: now,
				}
				if err := fixture.operations.CreateQueued(context.Background(), op); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.db.Exec(`CREATE TRIGGER reject_recovery_finish_for_status_error BEFORE UPDATE ON git_operations
					WHEN NEW.state = 'failed' BEGIN SELECT RAISE(ABORT, 'secret finish failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := fixture.db.Exec(`CREATE TRIGGER reject_recovery_status_write BEFORE INSERT ON git_status
				BEGIN SELECT RAISE(ABORT, 'secret status failure'); END`); err != nil {
				t.Fatal(err)
			}
			err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base})
			if err == nil || !strings.Contains(err.Error(), "Git status persistence failed") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("RecoverLocal() error = %v, want safe status persistence failure", err)
			}
			if transitionFailure && !strings.Contains(err.Error(), "Git operation persistence failed") {
				t.Fatalf("RecoverLocal() error = %v, missing journal transition failure", err)
			}
		})
	}
}

func TestGitManagerRecoveryCheckpointFailureImmediatelyFailsClosed(t *testing.T) {
	fixture, base, head, network := newManagerRecoveryFixture(t)
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	op := gitcmd.Operation{
		ID: strings.Repeat("6", 32), BaseName: base.Name, RepoPath: base.Path,
		ConfigFingerprint: base.Fingerprint, RemoteFingerprint: base.RemoteFingerprint,
		Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageFetching,
		Branch: base.Branch, CandidateOID: head, CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.operations.CreateQueued(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, base.Path, "update-ref", "refs/igonotes/fetch/"+op.ID, head)
	if _, err := fixture.db.Exec(`CREATE TRIGGER reject_recovery_checkpoint BEFORE UPDATE ON git_operations
		WHEN NEW.state = 'running' BEGIN SELECT RAISE(ABORT, 'secret recovery checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}

	err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base})
	if err == nil || !strings.Contains(err.Error(), "Git operation persistence failed") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("RecoverLocal() error = %v, want safe checkpoint persistence failure", err)
	}
	active, found, lookupErr := fixture.operations.ActiveByPath(context.Background(), base.Path)
	if lookupErr != nil || !found || active.ID != op.ID || active.State == gitcmd.OperationSucceeded {
		t.Fatalf("active journal immediately after checkpoint failure = %#v, %v, %v", active, found, lookupErr)
	}
	status, statusFound, statusErr := fixture.statuses.Get(context.Background(), base.Path)
	if statusErr != nil || !statusFound || status.State != model.GitStateNeedsReconnect || status.Error == nil {
		t.Fatalf("status immediately after checkpoint failure = %#v, %v, %v", status, statusFound, statusErr)
	}
	if !errors.Is(fixture.coordinator.CheckMutation(base.Path), ErrGitConflictPending) {
		t.Fatal("checkpoint failure reopened mutation gate")
	}
	if network.Load() != 0 {
		t.Fatalf("recovery network calls = %d", network.Load())
	}
}

func TestGitManagerRecoveryFinishFailureImmediatelyFailsClosed(t *testing.T) {
	fixture, base, head, network := newManagerRecoveryFixture(t)
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	op := gitcmd.Operation{
		ID: strings.Repeat("7", 32), BaseName: base.Name, RepoPath: base.Path,
		ConfigFingerprint: base.Fingerprint, RemoteFingerprint: base.RemoteFingerprint,
		Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageFetching,
		Branch: base.Branch, RemoteOID: head, CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.operations.CreateQueued(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`CREATE TRIGGER reject_recovery_finish BEFORE UPDATE ON git_operations
		WHEN NEW.state IN ('failed', 'conflict') BEGIN SELECT RAISE(ABORT, 'secret recovery finish failure'); END`); err != nil {
		t.Fatal(err)
	}

	err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base})
	if err == nil || !strings.Contains(err.Error(), "Git operation persistence failed") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("RecoverLocal() error = %v, want safe finish persistence failure", err)
	}
	active, found, lookupErr := fixture.operations.ActiveByPath(context.Background(), base.Path)
	if lookupErr != nil || !found || active.ID != op.ID || active.State == gitcmd.OperationSucceeded {
		t.Fatalf("active journal immediately after finish failure = %#v, %v, %v", active, found, lookupErr)
	}
	status, statusFound, statusErr := fixture.statuses.Get(context.Background(), base.Path)
	if statusErr != nil || !statusFound || status.State != model.GitStateNeedsReconnect || status.Error == nil {
		t.Fatalf("status immediately after finish failure = %#v, %v, %v", status, statusFound, statusErr)
	}
	if !errors.Is(fixture.coordinator.CheckMutation(base.Path), ErrGitConflictPending) {
		t.Fatal("finish failure reopened mutation gate")
	}
	if network.Load() != 0 {
		t.Fatalf("recovery network calls = %d", network.Load())
	}
}

func TestGitManagerRecoveryTransitionFailurePreservesDurableConflictAcrossRestart(t *testing.T) {
	for _, transition := range []string{"checkpoint", "finish"} {
		t.Run(transition, func(t *testing.T) {
			fixture, base, head, network := newManagerRecoveryFixture(t)
			now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
			op := gitcmd.Operation{
				ID: strings.Repeat("8", 32), BaseName: base.Name, RepoPath: base.Path,
				ConfigFingerprint: base.Fingerprint, RemoteFingerprint: base.RemoteFingerprint,
				Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageMerging,
				Branch: base.Branch, RemoteOID: head, CreatedAt: now, UpdatedAt: now,
			}
			if transition == "checkpoint" {
				op.RemoteOID = ""
				op.CandidateOID = head
				runManagerGit(t, base.Path, "update-ref", "refs/igonotes/fetch/"+op.ID, head)
			}
			if err := fixture.operations.CreateQueued(context.Background(), op); err != nil {
				t.Fatal(err)
			}
			wantPaths := []string{"a.md", "z.md"}
			if err := fixture.statuses.Upsert(context.Background(), model.GitStatus{
				Base: base.Name, RepositoryPath: base.Path, State: model.GitStateConflict,
				OperationID: op.ID, Stage: string(gitcmd.StageMerging), ChangedPaths: wantPaths,
				RemoteOID: head,
				Error:     &model.APIError{Code: string(gitcmd.CodeGitConflict), Message: "Git merge has conflicts"},
			}); err != nil {
				t.Fatal(err)
			}
			trigger := "reject_durable_conflict_" + transition
			condition := "NEW.state = 'running'"
			if transition == "finish" {
				condition = "NEW.state = 'conflict'"
			}
			if _, err := fixture.db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE ON git_operations
				WHEN %s BEGIN SELECT RAISE(ABORT, 'secret durable conflict transition failure'); END`, trigger, condition)); err != nil {
				t.Fatal(err)
			}

			err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base})
			if err == nil || !strings.Contains(err.Error(), "Git operation persistence failed") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("first RecoverLocal() error = %v, want safe %s failure", err, transition)
			}
			status, found, statusErr := fixture.statuses.Get(context.Background(), base.Path)
			if statusErr != nil || !found || status.State != model.GitStateConflict ||
				!reflect.DeepEqual(status.ChangedPaths, wantPaths) || status.Error == nil || status.Error.Code != string(gitcmd.CodeGitConflict) {
				t.Fatalf("status after %s failure = %#v, %v, %v; want durable conflict %v", transition, status, found, statusErr, wantPaths)
			}
			if !errors.Is(fixture.coordinator.CheckMutation(base.Path), ErrGitConflictPending) {
				t.Fatalf("%s failure reopened mutation gate", transition)
			}
			if active, found, err := fixture.operations.ActiveByPath(context.Background(), base.Path); err != nil || !found || active.ID != op.ID {
				t.Fatalf("active journal after %s failure = %#v, %v, %v", transition, active, found, err)
			}

			if _, err := fixture.db.Exec("DROP TRIGGER " + trigger); err != nil {
				t.Fatal(err)
			}
			restartCoordinator := NewBaseOperationCoordinator()
			restarted := NewGitManager(
				fixture.manager.gitService, fixture.statuses, fixture.operations, fixture.manager.prober,
				fixture.snapshots.get, fixture.notes, restartCoordinator,
			)
			t.Cleanup(func() { _ = restarted.Close() })
			if err := restarted.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base}); err != nil {
				t.Fatalf("restart RecoverLocal() error = %v", err)
			}
			status, found, statusErr = fixture.statuses.Get(context.Background(), base.Path)
			if statusErr != nil || !found || status.State != model.GitStateConflict ||
				!reflect.DeepEqual(status.ChangedPaths, wantPaths) || status.Error == nil || status.Error.Code != string(gitcmd.CodeGitConflict) {
				t.Fatalf("status after restart = %#v, %v, %v; want durable conflict %v", status, found, statusErr, wantPaths)
			}
			if !errors.Is(restartCoordinator.CheckMutation(base.Path), ErrGitConflictPending) {
				t.Fatal("restart did not republish durable conflict gate")
			}
			stored, found, lookupErr := fixture.operations.LatestByPath(context.Background(), base.Path)
			if lookupErr != nil || !found || stored.State != gitcmd.OperationConflict || !reflect.DeepEqual(stored.ConflictPaths, wantPaths) {
				t.Fatalf("journal after restart = %#v, %v, %v", stored, found, lookupErr)
			}
			if network.Load() != 0 {
				t.Fatalf("recovery network calls = %d", network.Load())
			}
		})
	}
}

func TestGitManagerRecoverLocalInterruptedConflictFailureGateAndReadyStates(t *testing.T) {
	t.Run("clean interrupted is failed and ready without network", func(t *testing.T) {
		root, remote := newManagerGitPair(t)
		runManagerGit(t, root, "init", "--initial-branch", "main")
		runManagerGit(t, root, "remote", "add", "origin", remote)
		if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("clean\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runManagerGit(t, root, "add", "--all")
		runManagerGit(t, root, "commit", "-m", "initial")
		oid := runManagerGit(t, root, "rev-parse", "HEAD")
		runManagerGit(t, root, "update-ref", "refs/igonotes/remotes/main", oid)
		base := configuredManagerBase("work", root)
		base.URL = remote
		delegate := gitcmd.NewCommandRunner()
		var network atomic.Int32
		runner := managerRunnerFunc(func(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
			if command.Scope == gitcmd.NetworkOperation {
				network.Add(1)
				return gitcmd.Result{}, errors.New("network forbidden")
			}
			return delegate.Run(ctx, command)
		})
		client := gitcmd.NewClient(runner)
		fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", runner, client, client)
		now := time.Date(2026, 9, 3, 9, 0, 0, 123456789, time.UTC)
		op := gitcmd.Operation{ID: strings.Repeat("b", 32), BaseName: base.Name, RepoPath: root,
			ConfigFingerprint: base.Fingerprint, RemoteFingerprint: base.RemoteFingerprint,
			Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageFetching,
			Branch: base.Branch, RemoteOID: oid, CreatedAt: now, UpdatedAt: now}
		if err := fixture.operations.CreateQueued(context.Background(), op); err != nil {
			t.Fatal(err)
		}
		if err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base}); err != nil {
			t.Fatal(err)
		}
		stored, _, _ := fixture.operations.LatestByPath(context.Background(), root)
		status, _, _ := fixture.statuses.Get(context.Background(), root)
		if stored.State != gitcmd.OperationFailed || stored.Error == nil || stored.Error.Code != gitcmd.CodeOperationInterrupted ||
			status.State != model.GitStateReady || status.RemoteOID != oid || network.Load() != 0 {
			t.Fatalf("recovery operation/status/network = %#v / %#v / %d", stored, status, network.Load())
		}
	})

	t.Run("uninitialized exact root needs reconnect", func(t *testing.T) {
		base := configuredManagerBase("work", t.TempDir())
		fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", nil, nil, nil)
		if err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base}); err != nil {
			t.Fatal(err)
		}
		status, _, _ := fixture.statuses.Get(context.Background(), base.Path)
		if status.State != model.GitStateNeedsReconnect {
			t.Fatalf("status = %#v", status)
		}
		if !errors.Is(fixture.coordinator.CheckMutation(base.Path), ErrGitConflictPending) {
			t.Fatal("blocking recovery did not restore mutation gate")
		}
	})
}

func TestGitManagerRecoveryUsesProvenTerminalPostPushCheckpointForSubsequentSync(t *testing.T) {
	root, remote := newManagerGitPair(t)
	runManagerGit(t, root, "init", "--initial-branch", "main")
	runManagerGit(t, root, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, root, "add", "--all")
	runManagerGit(t, root, "commit", "-m", "old remote")
	oldOID := runManagerGit(t, root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("pushed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, root, "commit", "-am", "pushed")
	pushOID := runManagerGit(t, root, "rev-parse", "HEAD")
	if oldOID == pushOID {
		t.Fatal("terminal recovery test requires distinct RemoteOID and PushOID")
	}
	runManagerGit(t, root, "push", "--no-verify", "origin", pushOID+":refs/heads/main")
	runManagerGit(t, root, "update-ref", "refs/igonotes/remotes/main", pushOID)
	base := configuredManagerBase("work", root)
	base.URL = remote
	delegate := gitcmd.NewCommandRunner()
	var network atomic.Int32
	runner := managerRunnerFunc(func(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
		if command.Scope == gitcmd.NetworkOperation {
			network.Add(1)
		}
		return delegate.Run(ctx, command)
	})
	client := gitcmd.NewClient(runner)
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", runner, client, client)
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	op := gitcmd.Operation{ID: strings.Repeat("c", 32), BaseName: base.Name, RepoPath: root,
		ConfigFingerprint: base.Fingerprint, RemoteFingerprint: base.RemoteFingerprint,
		Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageQueued,
		Branch: base.Branch, CreatedAt: now, UpdatedAt: now}
	if err := fixture.operations.CreateQueued(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	op.State = gitcmd.OperationSucceeded
	op.Stage = gitcmd.StageCompleted
	op.RemoteOID = oldOID
	op.PushOID = pushOID
	if err := fixture.operations.Finish(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	fixture.coordinator.SetConflict(root, true)
	if err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base}); err != nil {
		t.Fatal(err)
	}
	status, _, _ := fixture.statuses.Get(context.Background(), root)
	if status.State != model.GitStateReady || status.RemoteOID != pushOID || network.Load() != 0 {
		t.Fatalf("terminal gap status/network = %#v / %d", status, network.Load())
	}
	queued, deduplicated, err := fixture.manager.QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: base})
	if err != nil || deduplicated || queued.RemoteOID != pushOID {
		t.Fatalf("QueueSync() after terminal post-push recovery = %#v, %v, %v", queued, deduplicated, err)
	}
	waitManagerTerminal(t, fixture.manager)
	completed, found, err := fixture.operations.LatestByPath(context.Background(), root)
	if err != nil || !found || completed.ID != queued.ID || completed.State != gitcmd.OperationSucceeded ||
		completed.RemoteOID != pushOID || completed.Error != nil {
		t.Fatalf("subsequent sync operation = %#v, %v, %v", completed, found, err)
	}
	status, found, err = fixture.statuses.Get(context.Background(), root)
	if err != nil || !found || status.State != model.GitStateReady || status.RemoteOID != pushOID ||
		(status.Error != nil && status.Error.Code == string(gitcmd.CodeNeedsReconnect)) {
		t.Fatalf("subsequent sync status = %#v, %v, %v", status, found, err)
	}
	if network.Load() == 0 {
		t.Fatal("subsequent sync did not execute")
	}
	beforeRecovery := network.Load()
	if err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base}); err != nil {
		t.Fatal(err)
	}
	if network.Load() != beforeRecovery {
		t.Fatalf("idempotent recovery network calls changed from %d to %d", beforeRecovery, network.Load())
	}
}

func TestGitManagerRecoveryConflictIsDurableAndIdempotentlyRepublished(t *testing.T) {
	root, remote := newManagerGitPair(t)
	runManagerGit(t, root, "init", "--initial-branch", "main")
	runManagerGit(t, root, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, root, "add", "--all")
	runManagerGit(t, root, "commit", "-m", "base")
	trusted := runManagerGit(t, root, "rev-parse", "HEAD")
	runManagerGit(t, root, "update-ref", "refs/igonotes/remotes/main", trusted)
	runManagerGit(t, root, "switch", "-c", "other")
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, root, "commit", "-am", "other")
	runManagerGit(t, root, "switch", "main")
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, root, "commit", "-am", "main")
	command := exec.Command("git", "merge", "other")
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if err := command.Run(); err == nil {
		t.Fatal("git merge unexpectedly succeeded")
	}

	base := configuredManagerBase("work", root)
	base.URL = remote
	delegate := gitcmd.NewCommandRunner()
	var network atomic.Int32
	runner := managerRunnerFunc(func(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
		if command.Scope == gitcmd.NetworkOperation {
			network.Add(1)
			return gitcmd.Result{}, errors.New("network forbidden")
		}
		return delegate.Run(ctx, command)
	})
	client := gitcmd.NewClient(runner)
	fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", runner, client, client)
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	op := gitcmd.Operation{ID: strings.Repeat("d", 32), BaseName: base.Name, RepoPath: root,
		ConfigFingerprint: base.Fingerprint, RemoteFingerprint: base.RemoteFingerprint,
		Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageMerging,
		Branch: base.Branch, RemoteOID: trusted, CreatedAt: now, UpdatedAt: now}
	if err := fixture.operations.CreateQueued(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		if err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base}); err != nil {
			t.Fatal(err)
		}
		status, _, _ := fixture.statuses.Get(context.Background(), root)
		if status.State != model.GitStateConflict || !reflect.DeepEqual(status.ChangedPaths, []string{"note.md"}) ||
			!errors.Is(fixture.coordinator.CheckMutation(root), ErrGitConflictPending) {
			t.Fatalf("recovery run %d conflict = %#v, gate %v", run, status, fixture.coordinator.CheckMutation(root))
		}
	}
	stored, _, _ := fixture.operations.LatestByPath(context.Background(), root)
	if stored.State != gitcmd.OperationConflict || !reflect.DeepEqual(stored.ConflictPaths, []string{"note.md"}) || network.Load() != 0 {
		t.Fatalf("durable conflict/network = %#v / %d", stored, network.Load())
	}
}

func TestGitManagerRecoveryNeverClearsDurableConflictOnCleanWorktree(t *testing.T) {
	for _, source := range []string{"terminal journal", "public status"} {
		t.Run(source, func(t *testing.T) {
			root, remote := newManagerGitPair(t)
			runManagerGit(t, root, "init", "--initial-branch", "main")
			runManagerGit(t, root, "remote", "add", "origin", remote)
			if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("clean\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runManagerGit(t, root, "add", "--all")
			runManagerGit(t, root, "commit", "-m", "initial")
			oid := runManagerGit(t, root, "rev-parse", "HEAD")
			runManagerGit(t, root, "update-ref", "refs/igonotes/remotes/main", oid)
			base := configuredManagerBase("work", root)
			base.URL = remote
			delegate := gitcmd.NewCommandRunner()
			var network atomic.Int32
			runner := managerRunnerFunc(func(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
				if command.Scope == gitcmd.NetworkOperation {
					network.Add(1)
					return gitcmd.Result{}, errors.New("network forbidden")
				}
				return delegate.Run(ctx, command)
			})
			client := gitcmd.NewClient(runner)
			fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", runner, client, client)
			now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
			op := gitcmd.Operation{ID: strings.Repeat("9", 32), BaseName: base.Name, RepoPath: root,
				ConfigFingerprint: base.Fingerprint, RemoteFingerprint: base.RemoteFingerprint,
				Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageMerging,
				Branch: base.Branch, RemoteOID: oid, CreatedAt: now, UpdatedAt: now}
			if err := fixture.operations.CreateQueued(context.Background(), op); err != nil {
				t.Fatal(err)
			}
			if source == "terminal journal" {
				op.State = gitcmd.OperationConflict
				op.ConflictPaths = []string{"z.md", "a.md", "z.md"}
				op.Error = &gitcmd.SafeError{Code: gitcmd.CodeGitConflict, Message: "Git merge has conflicts"}
				if err := fixture.operations.Finish(context.Background(), op); err != nil {
					t.Fatal(err)
				}
			} else {
				op.State = gitcmd.OperationSucceeded
				op.Stage = gitcmd.StageCompleted
				if err := fixture.operations.Finish(context.Background(), op); err != nil {
					t.Fatal(err)
				}
				if err := fixture.statuses.Upsert(context.Background(), model.GitStatus{
					Base: base.Name, RepositoryPath: root, State: model.GitStateConflict,
					OperationID: op.ID, Stage: string(gitcmd.StageMerging),
					ChangedPaths: []string{"z.md", "a.md", "z.md"}, RemoteOID: oid,
					Error: &model.APIError{Code: string(gitcmd.CodeGitConflict), Message: "Git merge has conflicts"},
				}); err != nil {
					t.Fatal(err)
				}
			}

			if err := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base}); err != nil {
				t.Fatal(err)
			}
			status, _, _ := fixture.statuses.Get(context.Background(), root)
			if status.State != model.GitStateConflict || !reflect.DeepEqual(status.ChangedPaths, []string{"a.md", "z.md"}) {
				t.Fatalf("recovered status = %#v, want preserved conflict", status)
			}
			if !errors.Is(fixture.coordinator.CheckMutation(root), ErrGitConflictPending) {
				t.Fatal("clean local inspection cleared unresolved durable conflict gate")
			}
			stored, _, _ := fixture.operations.LatestByPath(context.Background(), root)
			if source == "terminal journal" && stored.State != gitcmd.OperationConflict {
				t.Fatalf("terminal conflict journal state = %q", stored.State)
			}
			if network.Load() != 0 {
				t.Fatalf("network calls = %d", network.Load())
			}
		})
	}
}

func TestGitManagerRecoveryAcceptsOnlyProvenCandidateAndPostPushTrust(t *testing.T) {
	for _, test := range []struct {
		name           string
		candidate      bool
		postPush       bool
		privateRef     bool
		checkpointFail bool
		wantReady      bool
	}{
		{name: "candidate with managed and private refs", candidate: true, privateRef: true, wantReady: true},
		{name: "candidate checkpoint failure remains recoverable", candidate: true, privateRef: true, checkpointFail: true, wantReady: true},
		{name: "candidate without private ref is ambiguous", candidate: true},
		{name: "post-push managed ref equals HEAD", postPush: true, wantReady: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, remote := newManagerGitPair(t)
			runManagerGit(t, root, "init", "--initial-branch", "main")
			runManagerGit(t, root, "remote", "add", "origin", remote)
			if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("clean\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runManagerGit(t, root, "add", "--all")
			runManagerGit(t, root, "commit", "-m", "initial")
			head := runManagerGit(t, root, "rev-parse", "HEAD")
			base := configuredManagerBase("work", root)
			base.URL = remote
			delegate := gitcmd.NewCommandRunner()
			var network atomic.Int32
			runner := managerRunnerFunc(func(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
				if command.Scope == gitcmd.NetworkOperation {
					network.Add(1)
					return gitcmd.Result{}, errors.New("network forbidden")
				}
				return delegate.Run(ctx, command)
			})
			client := gitcmd.NewClient(runner)
			fixture := newGitManagerFixture(t, []gitcmd.ConfiguredBase{base}, "", runner, client, client)
			now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
			op := gitcmd.Operation{ID: strings.Repeat("e", 32), BaseName: base.Name, RepoPath: root,
				ConfigFingerprint: base.Fingerprint, RemoteFingerprint: base.RemoteFingerprint,
				Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageFetching,
				Branch: base.Branch, CreatedAt: now, UpdatedAt: now}
			if test.candidate {
				op.CandidateOID = head
				runManagerGit(t, root, "update-ref", "refs/igonotes/remotes/main", head)
				if test.privateRef {
					runManagerGit(t, root, "update-ref", "refs/igonotes/fetch/"+op.ID, head)
				}
			}
			if test.postPush {
				op.RemoteOID = strings.Repeat("f", 40)
				op.PushOID = head
				op.Stage = gitcmd.StagePushing
				runManagerGit(t, root, "update-ref", "refs/igonotes/remotes/main", head)
			}
			if err := fixture.operations.CreateQueued(context.Background(), op); err != nil {
				t.Fatal(err)
			}
			if test.checkpointFail {
				if _, err := fixture.db.Exec(`CREATE TRIGGER reject_recovery_checkpoint BEFORE UPDATE ON git_operations
					WHEN NEW.state = 'running' BEGIN SELECT RAISE(ABORT, 'secret recovery checkpoint failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			recoveryErr := fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base})
			if test.checkpointFail {
				if recoveryErr == nil || strings.Contains(recoveryErr.Error(), "secret") {
					t.Fatalf("RecoverLocal() error = %v, want safe checkpoint persistence failure", recoveryErr)
				}
				if active, found, err := fixture.operations.ActiveByPath(context.Background(), root); err != nil || !found || active.ID != op.ID {
					t.Fatalf("active journal after checkpoint failure = %#v, %v, %v", active, found, err)
				}
				if _, err := fixture.db.Exec("DROP TRIGGER reject_recovery_checkpoint"); err != nil {
					t.Fatal(err)
				}
				recoveryErr = fixture.manager.RecoverLocal(context.Background(), []gitcmd.ConfiguredBase{base})
			}
			if recoveryErr != nil {
				t.Fatal(recoveryErr)
			}
			status, _, _ := fixture.statuses.Get(context.Background(), root)
			if test.wantReady && (status.State != model.GitStateReady || status.RemoteOID != head) {
				t.Fatalf("proven trust status = %#v", status)
			}
			stored, _, _ := fixture.operations.LatestByPath(context.Background(), root)
			if test.wantReady && stored.RemoteOID != head {
				t.Fatalf("recovered journal RemoteOID = %q, want service-proven %q", stored.RemoteOID, head)
			}
			if test.wantReady {
				queued, deduplicated, err := fixture.manager.QueueSync(context.Background(), gitcmd.SyncRequest{Snapshot: base})
				if err != nil || deduplicated || queued.RemoteOID != head {
					t.Fatalf("subsequent QueueSync() = %#v, %v, %v; want recovered trust", queued, deduplicated, err)
				}
			}
			if !test.wantReady && (status.State != model.GitStateNeedsReconnect || !errors.Is(fixture.coordinator.CheckMutation(root), ErrGitConflictPending)) {
				t.Fatalf("ambiguous trust status/gate = %#v / %v", status, fixture.coordinator.CheckMutation(root))
			}
			if network.Load() != 0 {
				t.Fatalf("network calls = %d", network.Load())
			}
		})
	}
}
