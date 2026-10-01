package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

var ErrGitManagerClosed = errors.New("git manager closed")

const (
	managerAdmissionCompensationTimeout = time.Second
	managerResumeCompensationTimeout    = 5 * time.Second
)

type gitManagerJob struct {
	operation     gitcmd.Operation
	snapshot      gitcmd.ConfiguredBase
	confirmations model.GitConfirmations
	synchronous   func()
}

type GitManager struct {
	gitService  *gitcmd.Service
	statuses    *repository.GitStatusRepository
	operations  *repository.GitOperationRepository
	prober      *GitProbeService
	snapshot    func(string) (gitcmd.ConfiguredBase, bool, error)
	notes       *NoteService
	coordinator *BaseOperationCoordinator

	mu               sync.Mutex
	cond             *sync.Cond
	queue            []gitManagerJob
	inFlight         map[string]gitcmd.Operation
	closed           bool
	started          bool
	workerErr        error
	closeOnce        sync.Once
	scheduler        *gitScheduler
	schedulerDone    chan struct{}
	orderedSnapshots GitOrderedSnapshots
	logger           *log.Logger

	lifetimeCtx    context.Context
	lifetimeCancel context.CancelFunc
	workerDone     chan struct{}
	doneOnce       sync.Once

	now                       func() time.Time
	beforeTerminalPublication func()
}

func NewGitManager(
	gitService *gitcmd.Service,
	statuses *repository.GitStatusRepository,
	operations *repository.GitOperationRepository,
	prober *GitProbeService,
	snapshot func(string) (gitcmd.ConfiguredBase, bool, error),
	notes *NoteService,
	coordinator *BaseOperationCoordinator,
) *GitManager {
	if gitService == nil {
		panic("service.NewGitManager: nil GitService")
	}
	if statuses == nil {
		panic("service.NewGitManager: nil GitStatusRepository")
	}
	if operations == nil {
		panic("service.NewGitManager: nil GitOperationRepository")
	}
	if prober == nil {
		panic("service.NewGitManager: nil GitProbeService")
	}
	if snapshot == nil {
		panic("service.NewGitManager: nil snapshot")
	}
	if notes == nil {
		panic("service.NewGitManager: nil NoteService")
	}
	if coordinator == nil {
		panic("service.NewGitManager: nil BaseOperationCoordinator")
	}
	lifetimeCtx, lifetimeCancel := context.WithCancel(context.Background())
	manager := &GitManager{
		gitService:                gitService,
		statuses:                  statuses,
		operations:                operations,
		prober:                    prober,
		snapshot:                  snapshot,
		notes:                     notes,
		coordinator:               coordinator,
		queue:                     make([]gitManagerJob, 0),
		inFlight:                  make(map[string]gitcmd.Operation),
		lifetimeCtx:               lifetimeCtx,
		lifetimeCancel:            lifetimeCancel,
		workerDone:                make(chan struct{}),
		schedulerDone:             make(chan struct{}),
		logger:                    log.Default(),
		now:                       time.Now,
		beforeTerminalPublication: func() {},
	}
	manager.cond = sync.NewCond(&manager.mu)
	return manager
}

// NewGitManagerWithAutosync schedules syncs through the manager's existing FIFO.
func NewGitManagerWithAutosync(
	gitService *gitcmd.Service,
	statuses *repository.GitStatusRepository,
	operations *repository.GitOperationRepository,
	prober *GitProbeService,
	snapshot func(string) (gitcmd.ConfiguredBase, bool, error),
	notes *NoteService,
	coordinator *BaseOperationCoordinator,
	snapshots GitOrderedSnapshots,
	configChanges <-chan struct{},
	logger *log.Logger,
) *GitManager {
	return newGitManagerWithAutosync(gitService, statuses, operations, prober, snapshot, notes, coordinator, snapshots, configChanges, logger, realGitSchedulerClock{})
}

func newGitManagerWithAutosync(
	gitService *gitcmd.Service,
	statuses *repository.GitStatusRepository,
	operations *repository.GitOperationRepository,
	prober *GitProbeService,
	snapshot func(string) (gitcmd.ConfiguredBase, bool, error),
	notes *NoteService,
	coordinator *BaseOperationCoordinator,
	snapshots GitOrderedSnapshots,
	configChanges <-chan struct{},
	logger *log.Logger,
	clock GitResilienceClock,
) *GitManager {
	m := NewGitManager(gitService, statuses, operations, prober, snapshot, notes, coordinator)
	m.scheduler = newGitScheduler(clock, statuses, snapshots, configChanges, m, logger)
	m.orderedSnapshots = snapshots
	m.logger = m.scheduler.logger
	m.now = clock.Now
	return m
}

func (m *GitManager) applyStatus(ctx context.Context, status model.GitStatus, action GitFailureAction) error {
	previous, found, err := m.statuses.Get(ctx, status.RepositoryPath)
	if err != nil {
		return err
	}
	if !found {
		status.ConsecutiveFailures = 0
		if err := m.statuses.Upsert(ctx, status); err != nil {
			return err
		}
	}
	if err := m.statuses.ApplyTransition(ctx, repository.GitStatusTransition{Status: status, Failures: action}); err != nil {
		if !found {
			// Seeding and transition publication are separate writes. Restore the
			// prior absence even when the caller canceled during publication.
			compensationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managerAdmissionCompensationTimeout)
			deleteErr := m.statuses.Delete(compensationCtx, status.RepositoryPath)
			cancel()
			return errors.Join(err, deleteErr)
		}
		return err
	}
	if action == GitFailureIncrement && previous.ConsecutiveFailures == repository.GitStatusPauseFailureThreshold-1 {
		code := ""
		if status.Error != nil {
			code = status.Error.Code
		}
		m.logger.Printf("Git autosync breaker base=%q count=%d code=%s", status.Base, repository.GitStatusPauseFailureThreshold, code)
	}
	if m.scheduler != nil {
		m.scheduler.notifyStatusChanged()
	}
	return nil
}

func (m *GitManager) Resume(ctx context.Context, name string) (gitcmd.Operation, bool, error) {
	requested, _, err := m.snapshot(name)
	if err != nil {
		return gitcmd.Operation{}, false, err
	}
	if requested.Name != name || !fullyConfiguredManagerBase(requested) {
		return gitcmd.Operation{}, false, needsReconnect("Git base directory must be reconnected")
	}
	return m.queueOperationWithResume(ctx, requested, gitcmd.OperationSync, model.GitConfirmations{}, true)
}

func (m *GitManager) QueueInitialize(ctx context.Context, request gitcmd.InitializeRequest) (gitcmd.Operation, bool, error) {
	return m.queueOperation(ctx, request.Snapshot, gitcmd.OperationInitialize, request.Confirmations)
}

func (m *GitManager) QueueSync(ctx context.Context, request gitcmd.SyncRequest) (gitcmd.Operation, bool, error) {
	return m.queueOperation(ctx, request.Snapshot, gitcmd.OperationSync, model.GitConfirmations{})
}

// ListConflicts serializes inspection with all Git mutations without creating a journal row.
func (m *GitManager) ListConflicts(ctx context.Context, base string) (model.GitConflictListResponse, error) {
	if err := ctx.Err(); err != nil {
		return model.GitConflictListResponse{}, err
	}
	results := make(chan struct {
		response model.GitConflictListResponse
		err      error
	}, 1)
	if err := m.enqueueSynchronous(func() {
		response, err := m.listConflicts(ctx, base)
		results <- struct {
			response model.GitConflictListResponse
			err      error
		}{response, err}
	}); err != nil {
		return model.GitConflictListResponse{}, err
	}
	select {
	case result := <-results:
		return result.response, result.err
	case <-ctx.Done():
		return model.GitConflictListResponse{}, ctx.Err()
	}
}

// ResolveConflict serializes one resolution with all Git mutations without creating a journal row.
func (m *GitManager) ResolveConflict(ctx context.Context, request model.GitConflictResolveRequest) (model.GitConflictResolveResponse, error) {
	if err := ctx.Err(); err != nil {
		return model.GitConflictResolveResponse{}, err
	}
	results := make(chan struct {
		response model.GitConflictResolveResponse
		err      error
	}, 1)
	if err := m.enqueueSynchronous(func() {
		response, err := m.resolveConflict(ctx, request)
		results <- struct {
			response model.GitConflictResolveResponse
			err      error
		}{response, err}
	}); err != nil {
		return model.GitConflictResolveResponse{}, err
	}
	select {
	case result := <-results:
		return result.response, result.err
	case <-ctx.Done():
		return model.GitConflictResolveResponse{}, ctx.Err()
	}
}

func (m *GitManager) QueueConflictComplete(ctx context.Context, base string) (gitcmd.Operation, bool, error) {
	return m.queueConflictOperation(ctx, base, gitcmd.OperationConflictComplete)
}

func (m *GitManager) QueueConflictAbort(ctx context.Context, base string) (gitcmd.Operation, bool, error) {
	return m.queueConflictOperation(ctx, base, gitcmd.OperationConflictAbort)
}

func (m *GitManager) queueOperation(
	ctx context.Context,
	requested gitcmd.ConfiguredBase,
	kind gitcmd.OperationKind,
	confirmations model.GitConfirmations,
) (gitcmd.Operation, bool, error) {
	return m.queueOperationWithResume(ctx, requested, kind, confirmations, false)
}

func (m *GitManager) queueOperationWithResume(ctx context.Context, requested gitcmd.ConfiguredBase, kind gitcmd.OperationKind, confirmations model.GitConfirmations, allowPaused bool) (gitcmd.Operation, bool, error) {
	m.coordinator.Lock()
	current, _, err := m.snapshot(requested.Name)
	if err != nil {
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, err
	}
	if current != requested {
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, needsReconnect("Git configuration changed before the operation was queued")
	}
	current, err = canonicalManagerSnapshot(current)
	if err != nil {
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, needsReconnect("Git base directory must be reconnected")
	}
	if err := m.coordinator.CheckMutation(current.Path); err != nil {
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, err
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, ErrGitManagerClosed
	}
	if err := m.refreshQueued(ctx, current); err != nil {
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, err
	}

	status, statusFound, err := m.statuses.Get(ctx, current.Path)
	if err != nil {
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, persistenceError(err)
	}
	if kind == gitcmd.OperationSync && statusFound && status.State == model.GitStateNeedsReconnect {
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, needsReconnect("Git base directory must be reconnected")
	}
	if !allowPaused && statusFound && status.State == model.GitStatePaused {
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, gitcmd.ErrGitPaused
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, ErrGitManagerClosed
	}
	if operation, found := m.inFlight[current.Path]; found {
		if sameManagerIdentity(operation, current) && (!allowPaused || (operation.Kind == gitcmd.OperationSync && status.State == model.GitStateSyncing)) {
			m.mu.Unlock()
			m.coordinator.Unlock()
			return operation, true, nil
		}
		m.mu.Unlock()
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, ErrGitRepositoryInUse
	}
	if allowPaused {
		active, found, err := m.operations.ActiveByPath(ctx, current.Path)
		if err != nil {
			m.mu.Unlock()
			m.coordinator.Unlock()
			return gitcmd.Operation{}, false, persistenceError(err)
		}
		if found {
			active = m.queuedOperationIdentityLocked(active, current)
			if status.State == model.GitStateSyncing && active.Kind == gitcmd.OperationSync && sameManagerIdentity(active, current) {
				m.inFlight[current.Path] = active
				m.mu.Unlock()
				m.coordinator.Unlock()
				return active, true, nil
			}
			m.mu.Unlock()
			m.coordinator.Unlock()
			return gitcmd.Operation{}, false, ErrGitRepositoryInUse
		}
	}
	if allowPaused && (!statusFound || status.State != model.GitStatePaused) {
		m.mu.Unlock()
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, gitcmd.ErrGitNotPaused
	}
	previous := status
	if allowPaused {
		status.State = model.GitStateReady
		status.Error = nil
		if err := m.applyStatus(ctx, status, GitFailureReset); err != nil {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managerResumeCompensationTimeout)
			rollbackErr := m.statuses.Upsert(rollbackCtx, previous)
			cancel()
			m.mu.Unlock()
			m.coordinator.Unlock()
			return gitcmd.Operation{}, false, errors.Join(persistenceError(err), managerStatusError(rollbackErr))
		}
	}
	operation, duplicate, queueErr := m.queueValidatedLocked(ctx, current, kind, confirmations, status, statusFound)
	if allowPaused && queueErr != nil {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managerResumeCompensationTimeout)
		rollbackErr := m.statuses.Upsert(rollbackCtx, previous)
		cancel()
		queueErr = errors.Join(queueErr, managerStatusError(rollbackErr))
		if rollbackErr == nil && m.scheduler != nil {
			m.scheduler.notifyStatusChanged()
		}
	}
	m.mu.Unlock()
	m.coordinator.Unlock()
	return operation, duplicate, queueErr
}

// queueValidatedLocked shares the durable admission path with Resume. Both locks are held.
func (m *GitManager) queueValidatedLocked(ctx context.Context, current gitcmd.ConfiguredBase, kind gitcmd.OperationKind, confirmations model.GitConfirmations, status model.GitStatus, statusFound bool) (gitcmd.Operation, bool, error) {
	active, found, err := m.operations.ActiveByPath(ctx, current.Path)
	if err != nil {
		return gitcmd.Operation{}, false, persistenceError(err)
	}
	if found {
		active = m.queuedOperationIdentityLocked(active, current)
		if sameManagerIdentity(active, current) {
			m.inFlight[current.Path] = active
			return active, true, nil
		}
		return gitcmd.Operation{}, false, ErrGitRepositoryInUse
	}

	latest, latestFound, err := m.operations.LatestByPath(ctx, current.Path)
	if err != nil {
		return gitcmd.Operation{}, false, persistenceError(err)
	}
	trusted, trustErr := managerAdmissionTrust(kind, current, status, statusFound, latest, latestFound)
	if trustErr != nil {
		return gitcmd.Operation{}, false, trustErr
	}
	id, err := newManagerOperationID()
	if err != nil {
		return gitcmd.Operation{}, false, persistenceError(err)
	}
	now := m.now().UTC()
	operation := gitcmd.Operation{
		ID:                id,
		BaseName:          current.Name,
		RepoPath:          current.Path,
		ConfigFingerprint: current.Fingerprint,
		RemoteFingerprint: current.RemoteFingerprint,
		Kind:              kind,
		State:             gitcmd.OperationQueued,
		Stage:             gitcmd.StageQueued,
		Branch:            current.Branch,
		RemoteOID:         trusted,
		ChangedPaths:      []string{},
		ConflictPaths:     []string{},
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := m.operations.CreateQueued(ctx, operation); err != nil {
		return operation, false, persistenceError(err)
	}
	queuedStatus := managerWorkingStatus(operation, status, statusFound)
	if err := m.applyStatus(ctx, queuedStatus, GitFailurePreserve); err != nil {
		statusErr := statusPersistenceError(err)
		operation.State = gitcmd.OperationFailed
		operation.Error = statusErr
		compensationCtx, cancelCompensation := context.WithTimeout(
			context.WithoutCancel(ctx), managerAdmissionCompensationTimeout,
		)
		finishErr := m.operations.Finish(compensationCtx, operation)
		cancelCompensation()
		if finishErr != nil {
			active := operation
			active.State = gitcmd.OperationQueued
			active.Error = nil
			m.inFlight[current.Path] = active
		}
		return operation, false, errors.Join(statusErr, managerJournalError(finishErr))
	}
	m.inFlight[current.Path] = operation
	m.queue = append(m.queue, gitManagerJob{operation: operation, snapshot: current, confirmations: confirmations})
	m.cond.Signal()
	return operation, false, nil
}

func (m *GitManager) queuedOperationIdentityLocked(operation gitcmd.Operation, current gitcmd.ConfiguredBase) gitcmd.Operation {
	for _, job := range m.queue {
		if job.operation.ID == operation.ID && job.snapshot == current {
			operation.BaseName, operation.ConfigFingerprint = current.Name, current.Fingerprint
			break
		}
	}
	return operation
}

func (m *GitManager) enqueueSynchronous(run func()) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrGitManagerClosed
	}
	m.queue = append(m.queue, gitManagerJob{synchronous: run})
	m.cond.Signal()
	return nil
}

func (m *GitManager) listConflicts(ctx context.Context, base string) (model.GitConflictListResponse, error) {
	if err := ctx.Err(); err != nil {
		return model.GitConflictListResponse{}, err
	}
	current, active, err := m.currentConflictSnapshot(base)
	if err != nil {
		return model.GitConflictListResponse{}, err
	}
	operation, _, err := m.currentConflictOperation(ctx, current)
	if err != nil {
		return model.GitConflictListResponse{}, err
	}
	snapshot, err := m.gitService.Conflicts(ctx, current, operation)
	if err != nil {
		return model.GitConflictListResponse{}, err
	}
	_ = active // The transaction is only needed by resolution.
	return conflictListResponse(current.Name, operation.ID, snapshot), nil
}

func (m *GitManager) resolveConflict(ctx context.Context, request model.GitConflictResolveRequest) (model.GitConflictResolveResponse, error) {
	if err := ctx.Err(); err != nil {
		return model.GitConflictResolveResponse{}, err
	}
	current, active, err := m.currentConflictSnapshot(request.Base)
	if err != nil {
		return model.GitConflictResolveResponse{}, err
	}
	operation, status, err := m.currentConflictOperation(ctx, current)
	if err != nil {
		return model.GitConflictResolveResponse{}, err
	}
	if request.OperationID != operation.ID {
		return model.GitConflictResolveResponse{}, gitcmd.ErrConflictStale
	}
	m.coordinator.SetConflict(current.Path, true)
	snapshot, err := m.gitService.ResolveConflict(ctx, current, operation, request, m.worktreeTransaction(current, active))
	if err != nil {
		return model.GitConflictResolveResponse{}, err
	}
	status.Stage = string(gitcmd.StageConflictResolving)
	status.ChangedPaths = conflictSnapshotPaths(snapshot)
	status.Error = nil
	if err := m.applyStatus(ctx, status, GitFailurePreserve); err != nil {
		return model.GitConflictResolveResponse{}, persistenceError(err)
	}
	return model.GitConflictResolveResponse{
		ResolvedPath: request.Path,
		Remaining:    conflictListResponse(current.Name, operation.ID, snapshot),
	}, nil
}

func (m *GitManager) queueConflictOperation(ctx context.Context, base string, kind gitcmd.OperationKind) (gitcmd.Operation, bool, error) {
	m.coordinator.Lock()
	defer m.coordinator.Unlock()
	if err := ctx.Err(); err != nil {
		return gitcmd.Operation{}, false, err
	}
	current, _, err := m.currentConflictSnapshot(base)
	if err != nil {
		return gitcmd.Operation{}, false, err
	}
	original, status, err := m.currentConflictOperation(ctx, current)
	if err != nil {
		return gitcmd.Operation{}, false, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return gitcmd.Operation{}, false, ErrGitManagerClosed
	}
	if active, found := m.inFlight[current.Path]; found {
		if active.Kind == kind && sameManagerIdentity(active, current) {
			m.mu.Unlock()
			return active, true, nil
		}
		m.mu.Unlock()
		return gitcmd.Operation{}, false, ErrGitRepositoryInUse
	}
	m.mu.Unlock()
	active, found, err := m.operations.ActiveByPath(ctx, current.Path)
	if err != nil {
		return gitcmd.Operation{}, false, persistenceError(err)
	}
	if found {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed {
			return gitcmd.Operation{}, false, ErrGitManagerClosed
		}
		if queued, queuedFound := m.inFlight[current.Path]; queuedFound {
			if queued.Kind == kind && sameManagerIdentity(queued, current) {
				return queued, true, nil
			}
			return gitcmd.Operation{}, false, ErrGitRepositoryInUse
		}
		if active.Kind == kind && sameManagerIdentity(active, current) {
			m.inFlight[current.Path] = active
			return active, true, nil
		}
		return gitcmd.Operation{}, false, ErrGitRepositoryInUse
	}

	inspection, err := m.gitService.Conflicts(ctx, current, original)
	if err != nil {
		return gitcmd.Operation{}, false, err
	}
	if kind == gitcmd.OperationConflictComplete && !inspection.CanComplete {
		return gitcmd.Operation{}, false, gitcmd.ErrConflictUnresolved
	}
	changedPaths := append([]string(nil), original.ChangedPaths...)
	if kind == gitcmd.OperationConflictAbort {
		changedPaths, err = managerAbortChangedPaths(ctx, current, original.LocalOID)
		if err != nil {
			return gitcmd.Operation{}, false, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return gitcmd.Operation{}, false, ErrGitManagerClosed
	}
	if active, found := m.inFlight[current.Path]; found {
		if active.Kind == kind && sameManagerIdentity(active, current) {
			return active, true, nil
		}
		return gitcmd.Operation{}, false, ErrGitRepositoryInUse
	}
	id, err := newManagerOperationID()
	if err != nil {
		return gitcmd.Operation{}, false, persistenceError(err)
	}
	now := m.now().UTC()
	operation := gitcmd.Operation{
		ID: id, BaseName: original.BaseName, RepoPath: original.RepoPath,
		ConfigFingerprint: original.ConfigFingerprint, RemoteFingerprint: original.RemoteFingerprint,
		Kind: kind, State: gitcmd.OperationQueued, Stage: gitcmd.StageQueued, Branch: original.Branch,
		BackupRef: original.BackupRef, LocalOID: original.LocalOID, CandidateOID: original.CandidateOID,
		RemoteOID: original.RemoteOID, PushOID: original.PushOID, ChangedPaths: sortedManagerPaths(changedPaths),
		ConflictPaths: conflictSnapshotPaths(inspection), CreatedAt: now, UpdatedAt: now,
	}
	if kind == gitcmd.OperationConflictComplete {
		operation.ConflictPaths = []string{}
	}
	if err := m.operations.CreateQueued(ctx, operation); err != nil {
		return gitcmd.Operation{}, false, persistenceError(err)
	}
	status.OperationID = operation.ID
	status.Stage = string(operation.Stage)
	status.ChangedPaths = operation.ConflictPaths
	status.Error = nil
	if err := m.applyStatus(ctx, status, GitFailurePreserve); err != nil {
		operation.State = gitcmd.OperationFailed
		operation.Error = statusPersistenceError(err)
		compensationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managerAdmissionCompensationTimeout)
		finishErr := m.operations.Finish(compensationCtx, operation)
		cancel()
		if finishErr != nil {
			operation.State = gitcmd.OperationQueued
			operation.Error = nil
			m.inFlight[current.Path] = operation
		}
		return operation, false, errors.Join(statusPersistenceError(err), managerJournalError(finishErr))
	}
	m.inFlight[current.Path] = operation
	m.queue = append(m.queue, gitManagerJob{operation: operation, snapshot: current})
	m.cond.Signal()
	return operation, false, nil
}

func (m *GitManager) currentConflictSnapshot(base string) (gitcmd.ConfiguredBase, bool, error) {
	current, active, err := m.snapshot(base)
	if err != nil {
		return gitcmd.ConfiguredBase{}, false, err
	}
	current, err = canonicalManagerSnapshot(current)
	if err != nil {
		return gitcmd.ConfiguredBase{}, false, needsReconnect("Git base directory must be reconnected")
	}
	return current, active, nil
}

func (m *GitManager) currentConflictOperation(ctx context.Context, current gitcmd.ConfiguredBase) (gitcmd.Operation, model.GitStatus, error) {
	status, found, err := m.statuses.Get(ctx, current.Path)
	if err != nil {
		return gitcmd.Operation{}, model.GitStatus{}, persistenceError(err)
	}
	if !found || status.State != model.GitStateConflict {
		return gitcmd.Operation{}, model.GitStatus{}, gitcmd.ErrRecoveryRequired
	}
	if status.OperationID == "" {
		return gitcmd.Operation{}, model.GitStatus{}, gitcmd.ErrRecoveryRequired
	}
	operation, found, err := m.operations.ByID(ctx, status.OperationID)
	if err != nil {
		return gitcmd.Operation{}, model.GitStatus{}, persistenceError(err)
	}
	if found {
		operation = m.operationForSnapshot(operation, current)
	}
	if found && operation.State == gitcmd.OperationConflict && originalConflictOperation(operation) && sameManagerIdentity(operation, current) {
		return operation, status, nil
	}
	if !found || (operation.State != gitcmd.OperationQueued && operation.State != gitcmd.OperationRunning) ||
		(operation.Kind != gitcmd.OperationConflictComplete && operation.Kind != gitcmd.OperationConflictAbort) {
		return gitcmd.Operation{}, model.GitStatus{}, gitcmd.ErrRecoveryRequired
	}
	operation, found, err = m.operations.LatestConflictByPath(ctx, current.Path)
	if err != nil {
		return gitcmd.Operation{}, model.GitStatus{}, persistenceError(err)
	}
	if found {
		operation = m.operationForSnapshot(operation, current)
	}
	if !found || !originalConflictOperation(operation) || !sameManagerIdentity(operation, current) {
		return gitcmd.Operation{}, model.GitStatus{}, gitcmd.ErrRecoveryRequired
	}
	return operation, status, nil
}

func (m *GitManager) recoveryConflictOperation(ctx context.Context, current gitcmd.ConfiguredBase, status model.GitStatus) (gitcmd.Operation, bool, error) {
	if status.State != model.GitStateConflict {
		return gitcmd.Operation{}, false, nil
	}
	if status.OperationID != "" {
		operation, found, err := m.operations.ByID(ctx, status.OperationID)
		if err != nil {
			return gitcmd.Operation{}, false, persistenceError(err)
		}
		if found {
			operation = m.operationForSnapshot(operation, current)
		}
		if found && originalConflictOperation(operation) && sameManagerIdentity(operation, current) {
			return operation, true, nil
		}
	}
	operation, found, err := m.operations.LatestConflictByPath(ctx, current.Path)
	if err != nil {
		return gitcmd.Operation{}, false, persistenceError(err)
	}
	if found {
		operation = m.operationForSnapshot(operation, current)
	}
	if !found || !originalConflictOperation(operation) || !sameManagerIdentity(operation, current) {
		return gitcmd.Operation{}, false, nil
	}
	return operation, true, nil
}

func originalConflictOperation(operation gitcmd.Operation) bool {
	return operation.Kind == gitcmd.OperationInitialize || operation.Kind == gitcmd.OperationSync
}

func conflictListResponse(base, operationID string, snapshot gitcmd.ConflictSnapshot) model.GitConflictListResponse {
	conflicts := make([]model.GitConflict, 0, len(snapshot.Conflicts))
	for _, conflict := range snapshot.Conflicts {
		conflicts = append(conflicts, model.GitConflict{
			ID: conflict.ID, Kind: model.GitConflictKind(conflict.Kind), ContentKind: model.GitConflictContentKind(conflict.ContentKind),
			Path: conflict.Path, OriginalPath: conflict.OriginalPath, Base: conflictStageDTO(conflict.Base),
			Local: conflictStageDTO(conflict.Local), Remote: conflictStageDTO(conflict.Remote), Actions: conflictActionsDTO(conflict.Actions),
		})
	}
	return model.GitConflictListResponse{
		Base: base, OperationID: operationID, HeadOID: snapshot.HeadOID, MergeHeadOID: snapshot.MergeHeadOID,
		Conflicts: conflicts, CanComplete: snapshot.CanComplete,
	}
}

func conflictStageDTO(stage *gitcmd.ConflictStage) *model.GitConflictStage {
	if stage == nil {
		return nil
	}
	return &model.GitConflictStage{Path: stage.Path, OID: stage.OID, Mode: stage.Mode, Size: stage.Size, Content: stage.Content, PreviewTruncated: stage.PreviewTruncated}
}

func conflictActionsDTO(actions []string) []model.GitConflictAction {
	result := make([]model.GitConflictAction, len(actions))
	for index, action := range actions {
		result[index] = model.GitConflictAction(action)
	}
	return result
}

func conflictSnapshotPaths(snapshot gitcmd.ConflictSnapshot) []string {
	paths := make([]string, 0, len(snapshot.Conflicts))
	for _, conflict := range snapshot.Conflicts {
		paths = append(paths, conflict.Path)
	}
	return sortedManagerPaths(paths)
}

func managerAbortChangedPaths(ctx context.Context, current gitcmd.ConfiguredBase, localOID string) ([]string, error) {
	result, err := gitcmd.NewCommandRunner().Run(ctx, gitcmd.Command{
		Dir: current.Path, Args: []string{"diff", "--name-only", "-z", localOID, "--"}, Scope: gitcmd.LocalOperation, ReadOnly: true,
	})
	if err != nil || result.StdoutTruncated || result.StderrTruncated || !strings.HasSuffix(result.Stdout, "\x00") {
		return nil, gitcmd.ErrRecoveryRequired
	}
	paths := strings.Split(strings.TrimSuffix(result.Stdout, "\x00"), "\x00")
	if len(paths) == 1 && paths[0] == "" {
		return []string{}, nil
	}
	for _, path := range paths {
		if path == "" {
			return nil, gitcmd.ErrRecoveryRequired
		}
	}
	return sortedManagerPaths(paths), nil
}

func (m *GitManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrGitManagerClosed
	}
	if m.started {
		return nil
	}
	m.started = true
	go m.worker()
	if m.scheduler != nil {
		go func() { defer close(m.schedulerDone); m.scheduler.run(m.lifetimeCtx) }()
	} else {
		close(m.schedulerDone)
	}
	return nil
}

func (m *GitManager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.lifetimeCancel()
		m.cond.Broadcast()
		if !m.started {
			m.doneOnce.Do(func() { close(m.workerDone) })
			close(m.schedulerDone)
		}
		m.mu.Unlock()
	})
	<-m.workerDone
	<-m.schedulerDone
	m.mu.Lock()
	err := m.workerErr
	m.mu.Unlock()
	return err
}

func (m *GitManager) worker() {
	defer m.doneOnce.Do(func() { close(m.workerDone) })
	for {
		m.mu.Lock()
		for len(m.queue) == 0 && !m.closed {
			m.cond.Wait()
		}
		if m.closed {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		m.coordinator.Lock()
		m.mu.Lock()
		if m.closed || m.lifetimeCtx.Err() != nil {
			m.mu.Unlock()
			m.coordinator.Unlock()
			return
		}
		if len(m.queue) == 0 {
			m.mu.Unlock()
			m.coordinator.Unlock()
			continue
		}
		job := m.queue[0]
		m.queue[0] = gitManagerJob{}
		m.queue = m.queue[1:]
		m.mu.Unlock()

		if job.synchronous != nil {
			job.synchronous()
		} else {
			m.runJob(job)
		}
		m.coordinator.Unlock()
	}
}

func (m *GitManager) runJob(job gitManagerJob) {
	if m.lifetimeCtx.Err() != nil {
		return
	}
	current, active, snapshotErr := m.freshJobSnapshot(job.snapshot)
	if m.lifetimeCtx.Err() != nil {
		return
	}
	if snapshotErr != nil || (current.Name == job.snapshot.Name && current != job.snapshot) {
		m.beforeTerminalPublication()
		finished, terminalErr := m.finishStale(job.operation)
		m.completeTerminal(job.operation.RepoPath, finished, terminalErr)
		return
	}

	operation, found, err := m.operations.ActiveByPath(m.lifetimeCtx, current.Path)
	if m.lifetimeCtx.Err() != nil {
		return
	}
	if err != nil || !found || operation.ID != job.operation.ID {
		m.beforeTerminalPublication()
		if err == nil {
			err = errors.New("active Git operation is missing")
		}
		finished, terminalErr := m.finishFailure(job.operation, current, gitcmd.OperationResult{}, persistenceError(err), nil)
		m.completeTerminal(current.Path, finished, terminalErr)
		return
	}
	// Admission identity remains immutable in the journal; publication follows a safe rename.
	operation.BaseName = current.Name
	operation.ConfigFingerprint = current.Fingerprint
	status, statusFound, statusErr := m.statuses.Get(m.lifetimeCtx, current.Path)
	if m.lifetimeCtx.Err() != nil {
		return
	}
	if statusErr != nil {
		m.beforeTerminalPublication()
		finished, terminalErr := m.finishFailure(operation, current, gitcmd.OperationResult{}, persistenceError(statusErr), nil)
		m.completeTerminal(current.Path, finished, terminalErr)
		return
	}

	workingState := model.GitStateSyncing
	if operation.Kind == gitcmd.OperationInitialize {
		workingState = model.GitStateInitializing
	}
	if operation.Kind == gitcmd.OperationConflictComplete || operation.Kind == gitcmd.OperationConflictAbort {
		workingState = model.GitStateConflict
	}
	conflictBlockCleared := false
	attemptStarted := false
	progress := func(ctx context.Context, checkpoint gitcmd.Checkpoint) error {
		if operation.Kind == gitcmd.OperationConflictComplete && checkpoint.Stage == gitcmd.StageConflictReindexed {
			if active {
				if err := m.notes.SyncFS(); err != nil {
					return err
				}
			}
		}
		if err := m.operations.Checkpoint(ctx, operation.ID, checkpoint); err != nil {
			return persistenceError(err)
		}
		starting := !attemptStarted
		operation.State = gitcmd.OperationRunning
		operation.Stage = checkpoint.Stage
		operation.BackupRef = checkpoint.BackupRef
		operation.LocalOID = checkpoint.LocalOID
		operation.CandidateOID = checkpoint.CandidateOID
		operation.RemoteOID = checkpoint.RemoteOID
		operation.PushOID = checkpoint.PushOID
		operation.ChangedPaths = sortedManagerPaths(checkpoint.ChangedPaths)
		operation.ConflictPaths = sortedManagerPaths(checkpoint.ConflictPaths)
		published := managerWorkingStatus(operation, status, statusFound)
		if starting {
			now := m.now().UTC()
			published.LastAttempt = &now
		}
		published.State = workingState
		if operation.Kind == gitcmd.OperationConflictComplete && (checkpoint.Stage == gitcmd.StageConflictReindexed || conflictBlockCleared) {
			published.State = model.GitStateSyncing
		}
		if err := m.applyStatus(ctx, published, GitFailurePreserve); err != nil {
			return persistenceError(err)
		}
		status = published
		statusFound = true
		attemptStarted = true
		if operation.Kind == gitcmd.OperationConflictComplete && checkpoint.Stage == gitcmd.StageConflictReindexed {
			m.coordinator.SetConflict(current.Path, false)
			conflictBlockCleared = true
		}
		return nil
	}

	transaction := m.worktreeTransaction(current, active)

	var result gitcmd.OperationResult
	var operationErr error
	abortRecoveredLocally := false
	operationErr = progress(m.lifetimeCtx, gitcmd.Checkpoint{
		Stage: operation.Stage, BackupRef: operation.BackupRef, LocalOID: operation.LocalOID,
		CandidateOID: operation.CandidateOID, RemoteOID: operation.RemoteOID, PushOID: operation.PushOID,
		ChangedPaths: operation.ChangedPaths, ConflictPaths: operation.ConflictPaths,
	})
	if operationErr == nil {
		switch operation.Kind {
		case gitcmd.OperationInitialize:
			if err := progress(m.lifetimeCtx, gitcmd.Checkpoint{Stage: gitcmd.StageProbing, RemoteOID: operation.RemoteOID}); err != nil {
				operationErr = err
				break
			}
			probe, err := m.prober.Probe(m.lifetimeCtx, model.GitProbeRequest{
				Base: current.Name, GitURL: current.URL, GitBranch: current.Branch,
			})
			if err != nil {
				operationErr = managerSafeError(err)
				break
			}
			if probe.BlockingError != nil {
				operationErr = safeErrorFromAPI(probe.BlockingError)
				break
			}
			if !probe.CanConfigure {
				operationErr = &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Git configuration cannot be initialized"}
				break
			}
			if err := requireManagerConfirmations(probe.RequiredMutations, job.confirmations); err != nil {
				operationErr = err
				break
			}
			lastRemoteOID, err := managerRuntimeInitializeTrust(current, status, statusFound, operation)
			if err != nil {
				operationErr = err
				break
			}
			result, operationErr = m.gitService.Initialize(m.lifetimeCtx, gitcmd.InitializeOptions{
				Operation: operation, Snapshot: current, Probe: probe,
				Confirmations: job.confirmations, LastRemoteOID: lastRemoteOID,
			}, transaction, progress)
		case gitcmd.OperationSync:
			lastRemoteOID, err := managerRuntimeSyncTrust(current, status, statusFound, operation)
			if err != nil {
				operationErr = err
				break
			}
			result, operationErr = m.gitService.Sync(m.lifetimeCtx, gitcmd.SyncOptions{
				Operation: operation, Snapshot: current, LastRemoteOID: lastRemoteOID,
			}, transaction, progress)
		case gitcmd.OperationConflictComplete:
			var pushOID string
			pushOID, operationErr = m.gitService.CompleteConflict(m.lifetimeCtx, current, operation, transaction, progress)
			result.PushOID = pushOID
			result.RemoteOID = pushOID
		case gitcmd.OperationConflictAbort:
			operationErr = m.gitService.AbortConflict(m.lifetimeCtx, current, operation, transaction, progress)
			if operationErr == nil {
				abortRecoveredLocally = true
				latest, lookupErr := m.latestOperation(m.lifetimeCtx, operation)
				if lookupErr != nil {
					operationErr = persistenceError(lookupErr)
				} else {
					result.ChangedPaths = latest.ChangedPaths
					if active {
						operationErr = m.notes.SyncFS()
					}
				}
			}
		default:
			operationErr = &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Invalid Git operation"}
		}
	}

	if operationErr != nil && m.lifetimeCtx.Err() != nil {
		var conflict *gitcmd.ConflictError
		if !errors.As(operationErr, &conflict) {
			operationErr = &gitcmd.SafeError{Code: gitcmd.CodeOperationInterrupted, Message: "Git operation was interrupted"}
		}
	}
	m.beforeTerminalPublication()
	if abortRecoveredLocally && operationErr != nil {
		m.coordinator.SetConflict(current.Path, true)
		m.completeTerminal(current.Path, false, managerSafeError(operationErr))
		return
	}
	var conflict *gitcmd.ConflictError
	if errors.As(operationErr, &conflict) {
		m.coordinator.SetConflict(current.Path, true)
	}
	if operationErr == nil && operation.Kind == gitcmd.OperationConflictAbort {
		finished, terminalErr := m.finishAbortSuccess(operation, current, result)
		m.completeTerminal(current.Path, finished, terminalErr)
	} else if operationErr == nil {
		finished, terminalErr := m.finishSuccess(operation, current, result)
		m.completeTerminal(current.Path, finished, terminalErr)
	} else {
		if (operation.Kind == gitcmd.OperationConflictComplete || operation.Kind == gitcmd.OperationConflictAbort) && !conflictBlockCleared {
			m.coordinator.SetConflict(current.Path, true)
		}
		finished, terminalErr := m.finishFailure(operation, current, result, managerSafeError(operationErr), conflict)
		m.completeTerminal(current.Path, finished, terminalErr)
	}
}

// Called under the coordinator, never under the manager mutex: settings callbacks
// may themselves need to inspect manager state.
func (m *GitManager) freshJobSnapshot(old gitcmd.ConfiguredBase) (gitcmd.ConfiguredBase, bool, error) {
	current, active, err := m.snapshot(old.Name)
	if err == nil && current.Name == old.Name && fullyConfiguredManagerBase(current) {
		current, err = canonicalManagerSnapshot(current)
		return current, active, err
	}
	if m.orderedSnapshots == nil {
		return gitcmd.ConfiguredBase{}, false, needsReconnect("Git configuration changed before the operation started")
	}
	bases, err := m.orderedSnapshots.OrderedGitSnapshots()
	if err != nil {
		return gitcmd.ConfiguredBase{}, false, err
	}
	var alias gitcmd.ConfiguredBase
	for _, candidate := range bases {
		if !fullyConfiguredManagerBase(candidate) {
			continue
		}
		candidate, err = canonicalManagerSnapshot(candidate)
		if err != nil || candidate.Path != old.Path || candidate.RemoteFingerprint != old.RemoteFingerprint {
			continue
		}
		if alias.Name != "" {
			return gitcmd.ConfiguredBase{}, false, needsReconnect("Git configuration is ambiguous")
		}
		alias = candidate
	}
	if alias.Name == "" {
		return gitcmd.ConfiguredBase{}, false, needsReconnect("Git configuration changed before the operation started")
	}
	// A rename may change the full fingerprint, but no other configuration may
	// silently ride along on the old queued operation.
	renamed := old
	renamed.Name, renamed.Fingerprint = alias.Name, alias.Fingerprint
	if renamed != alias {
		return gitcmd.ConfiguredBase{}, false, needsReconnect("Git configuration changed before the operation started")
	}
	verified, active, err := m.snapshot(alias.Name)
	if err != nil {
		return gitcmd.ConfiguredBase{}, false, err
	}
	verified, err = canonicalManagerSnapshot(verified)
	if err != nil || verified != alias {
		return gitcmd.ConfiguredBase{}, false, needsReconnect("Git configuration changed before the operation started")
	}
	return alias, active, nil
}

func (m *GitManager) refreshQueued(ctx context.Context, current gitcmd.ConfiguredBase) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	index := -1
	var job gitManagerJob
	for i, queued := range m.queue {
		if queued.synchronous == nil && queued.snapshot.Path == current.Path {
			index, job = i, queued
			break
		}
	}
	m.mu.Unlock()
	if index < 0 {
		return nil
	}
	fresh, _, err := m.freshJobSnapshot(job.snapshot)
	if err == nil && fresh == job.snapshot {
		return nil
	}
	if err == nil && fresh.Name != job.snapshot.Name && fresh == current {
		m.mu.Lock()
		m.queue[index].snapshot = fresh
		m.queue[index].operation.BaseName = fresh.Name
		m.queue[index].operation.ConfigFingerprint = fresh.Fingerprint
		m.inFlight[current.Path] = m.queue[index].operation
		m.mu.Unlock()
		return nil
	}
	finished, finishErr := m.finishStale(job.operation)
	if !finished {
		return finishErr
	}
	m.mu.Lock()
	m.queue = append(m.queue[:index], m.queue[index+1:]...)
	delete(m.inFlight, job.snapshot.Path)
	m.mu.Unlock()
	return finishErr
}

// Journal admission fields are immutable. A unique same-repository rename gets
// an effective identity for conflict inspection and recovery, not a journal edit.
// Like freshJobSnapshot, this must be called outside the manager mutex.
func (m *GitManager) operationForSnapshot(operation gitcmd.Operation, current gitcmd.ConfiguredBase) gitcmd.Operation {
	if sameManagerIdentity(operation, current) || operation.BaseName == current.Name ||
		operation.RepoPath != current.Path || operation.RemoteFingerprint != current.RemoteFingerprint || m.orderedSnapshots == nil {
		return operation
	}
	bases, err := m.orderedSnapshots.OrderedGitSnapshots()
	if err != nil {
		return operation
	}
	var alias gitcmd.ConfiguredBase
	for _, candidate := range bases {
		if candidate.Name == operation.BaseName {
			return operation
		}
		if !fullyConfiguredManagerBase(candidate) {
			continue
		}
		candidate, err = canonicalManagerSnapshot(candidate)
		if err != nil || candidate.Path != operation.RepoPath || candidate.RemoteFingerprint != operation.RemoteFingerprint {
			continue
		}
		if alias.Name != "" {
			return operation
		}
		alias = candidate
	}
	if alias != current {
		return operation
	}
	operation.BaseName, operation.ConfigFingerprint = current.Name, current.Fingerprint
	return operation
}

func (m *GitManager) worktreeTransaction(current gitcmd.ConfiguredBase, active bool) gitcmd.WorktreeTransaction {
	return func(ctx context.Context, mutate func(string) error) error {
		guardedMutate := func(canonicalPath string) error {
			err := mutate(canonicalPath)
			var conflict *gitcmd.ConflictError
			if errors.As(err, &conflict) {
				m.coordinator.SetConflict(canonicalPath, true)
			}
			return err
		}
		if active {
			return m.notes.MutateActiveFilesystem(current.Path, guardedMutate)
		}
		return guardedMutate(current.Path)
	}
}

func (m *GitManager) finishStale(operation gitcmd.Operation) (bool, error) {
	ctx := context.WithoutCancel(m.lifetimeCtx)
	operation.State = gitcmd.OperationFailed
	operation.Error = needsReconnect("Git configuration changed before the operation started")
	operation.UpdatedAt = m.now().UTC()
	err := m.operations.Finish(ctx, operation)
	return err == nil, managerJournalError(err)
}

func (m *GitManager) finishSuccess(operation gitcmd.Operation, current gitcmd.ConfiguredBase, result gitcmd.OperationResult) (bool, error) {
	ctx := context.WithoutCancel(m.lifetimeCtx)
	operation, lookupErr := m.latestOperation(ctx, operation)
	operation.State = gitcmd.OperationSucceeded
	operation.Stage = gitcmd.StageCompleted
	applyManagerResult(&operation, result)
	now := m.now().UTC()
	operation.UpdatedAt = now
	finishErr := m.operations.Finish(ctx, operation)
	changedPaths := result.ChangedPaths
	if changedPaths == nil {
		changedPaths = operation.ChangedPaths
	}
	changed := sortedManagerPaths(changedPaths)
	previous, _, statusLookupErr := m.statuses.Get(ctx, current.Path)
	statusErr := m.applyStatus(ctx, model.GitStatus{
		Base: current.Name, RepositoryPath: current.Path, State: model.GitStateReady,
		OperationID: operation.ID, Stage: string(gitcmd.StageCompleted), Ahead: result.Ahead, Behind: result.Behind,
		LastAttempt: previous.LastAttempt, LastSuccess: &now, ChangedPaths: changed, RemoteOID: result.RemoteOID,
	}, ClassifyGitOutcome(GitTerminalOutcome{Operation: operation.Kind, State: operation.State}).Failures)
	return finishErr == nil, errors.Join(managerJournalError(lookupErr), managerJournalError(finishErr), managerStatusError(statusLookupErr), managerStatusError(statusErr))
}

func (m *GitManager) finishAbortSuccess(operation gitcmd.Operation, current gitcmd.ConfiguredBase, result gitcmd.OperationResult) (bool, error) {
	ctx := context.WithoutCancel(m.lifetimeCtx)
	operation, lookupErr := m.latestOperation(ctx, operation)
	operation.State = gitcmd.OperationSucceeded
	operation.Stage = gitcmd.StageCompleted
	applyManagerResult(&operation, result)
	now := m.now().UTC()
	operation.UpdatedAt = now
	changed := sortedManagerPaths(result.ChangedPaths)
	previous, _, statusLookupErr := m.statuses.Get(ctx, current.Path)
	if statusLookupErr != nil {
		return false, managerStatusError(statusLookupErr)
	}
	statusErr := m.applyStatus(ctx, model.GitStatus{
		Base: current.Name, RepositoryPath: current.Path, State: model.GitStatePaused,
		OperationID: operation.ID, Stage: string(gitcmd.StageCompleted), LastAttempt: previous.LastAttempt, LastSuccess: previous.LastSuccess,
		ChangedPaths: changed, RemoteOID: operation.RemoteOID,
	}, ClassifyGitOutcome(GitTerminalOutcome{Operation: operation.Kind, State: operation.State}).Failures)
	if statusErr != nil {
		return false, errors.Join(managerJournalError(lookupErr), managerStatusError(statusErr))
	}
	finishErr := m.operations.Finish(ctx, operation)
	if finishErr == nil && statusErr == nil {
		m.coordinator.SetConflict(current.Path, false)
	}
	return finishErr == nil, errors.Join(managerJournalError(lookupErr), managerJournalError(finishErr), managerStatusError(statusErr))
}

func (m *GitManager) finishFailure(
	operation gitcmd.Operation,
	current gitcmd.ConfiguredBase,
	result gitcmd.OperationResult,
	safeErr *gitcmd.SafeError,
	conflict *gitcmd.ConflictError,
) (bool, error) {
	ctx := context.WithoutCancel(m.lifetimeCtx)
	operation, lookupErr := m.latestOperation(ctx, operation)
	checkpointChangedPaths := append([]string(nil), operation.ChangedPaths...)
	applyManagerResult(&operation, result)
	if len(operation.ChangedPaths) == 0 && len(checkpointChangedPaths) != 0 {
		operation.ChangedPaths = checkpointChangedPaths
	}
	now := m.now().UTC()
	operation.UpdatedAt = now
	operation.Error = safeErr
	if conflict != nil {
		m.coordinator.SetConflict(current.Path, true)
		operation.State = gitcmd.OperationConflict
		operation.ConflictPaths = sortedManagerPaths(append(operation.ConflictPaths, conflict.Paths...))
	} else {
		operation.State = gitcmd.OperationFailed
	}
	finishErr := m.operations.Finish(ctx, operation)
	status, statusFound, statusLookupErr := m.statuses.Get(ctx, current.Path)
	var statusErr error
	if statusLookupErr == nil {
		status = managerStatusFromPrevious(current, status, statusFound)
		classification := ClassifyGitOutcome(GitTerminalOutcome{Operation: operation.Kind, State: operation.State, ExistingState: status.State, ErrorCode: safeErr.Code})
		status.State = classification.State
		status.OperationID = operation.ID
		status.Stage = string(operation.Stage)
		status.Error = &model.APIError{Code: string(safeErr.Code), Message: safeErr.Message, Field: safeErr.Field}
		if operation.RemoteOID != "" {
			status.RemoteOID = operation.RemoteOID
		}
		if len(operation.ChangedPaths) != 0 {
			status.ChangedPaths = sortedManagerPaths(operation.ChangedPaths)
		}
		if result.RemoteOID != "" {
			status.RemoteOID = result.RemoteOID
		}
		if conflict != nil {
			status.ChangedPaths = append([]string(nil), operation.ConflictPaths...)
		} else if len(result.ChangedPaths) != 0 {
			status.ChangedPaths = sortedManagerPaths(result.ChangedPaths)
		}
		statusErr = m.applyStatus(ctx, status, classification.Failures)
	}
	return finishErr == nil, errors.Join(
		managerJournalError(lookupErr), managerJournalError(finishErr),
		managerStatusError(statusLookupErr), managerStatusError(statusErr),
	)
}

func (m *GitManager) latestOperation(ctx context.Context, fallback gitcmd.Operation) (gitcmd.Operation, error) {
	operation, found, err := m.operations.ActiveByPath(ctx, fallback.RepoPath)
	if err == nil && found && operation.ID == fallback.ID {
		return operation, nil
	}
	return fallback, err
}

func (m *GitManager) completeTerminal(path string, journalFinished bool, terminalErr error) {
	m.mu.Lock()
	if journalFinished {
		delete(m.inFlight, path)
	}
	if terminalErr != nil {
		m.workerErr = errors.Join(m.workerErr, terminalErr)
	}
	m.mu.Unlock()
}

func (m *GitManager) RecoverLocal(ctx context.Context, configuredSnapshots []gitcmd.ConfiguredBase) error {
	unfinished, err := m.operations.ListUnfinished(ctx)
	if err != nil {
		return persistenceError(err)
	}
	unfinishedByPath := make(map[string]gitcmd.Operation, len(unfinished))
	for _, operation := range unfinished {
		unfinishedByPath[operation.RepoPath] = operation
	}
	var recoveryErrors []error
	for _, configured := range configuredSnapshots {
		if !fullyConfiguredManagerBase(configured) {
			continue
		}
		m.coordinator.Lock()
		current, canonicalErr := canonicalManagerSnapshot(configured)
		if canonicalErr != nil {
			current = configured
		}
		latest, latestFound, latestErr := m.operations.LatestByPath(ctx, current.Path)
		if latestErr != nil {
			recoveryErrors = append(recoveryErrors, persistenceError(latestErr))
			m.coordinator.Unlock()
			continue
		}
		existingStatus, statusFound, statusErr := m.statuses.Get(ctx, current.Path)
		if statusErr != nil {
			recoveryErrors = append(recoveryErrors, persistenceError(statusErr))
			m.coordinator.Unlock()
			continue
		}
		latestMatches := latestFound && sameManagerIdentity(latest, current)
		active, activeFound := unfinishedByPath[current.Path]
		if latestFound {
			latest = m.operationForSnapshot(latest, current)
			latestMatches = sameManagerIdentity(latest, current)
		}
		if activeFound {
			active = m.operationForSnapshot(active, current)
			unfinishedByPath[current.Path] = active
		}
		unfinishedTransition := activeFound && sameManagerIdentity(active, current) &&
			(active.Kind == gitcmd.OperationConflictComplete || active.Kind == gitcmd.OperationConflictAbort)
		var recoveryOperation *gitcmd.Operation
		var originalConflict *gitcmd.Operation
		if unfinishedTransition {
			activeCopy := active
			recoveryOperation = &activeCopy
		} else if statusFound && existingStatus.State == model.GitStateConflict {
			conflictOperation, found, conflictErr := m.recoveryConflictOperation(ctx, current, existingStatus)
			if conflictErr != nil {
				recoveryErrors = append(recoveryErrors, conflictErr)
				m.coordinator.SetConflict(current.Path, true)
				m.coordinator.Unlock()
				continue
			}
			if found {
				conflictCopy := conflictOperation
				recoveryOperation = &conflictCopy
				originalConflict = &conflictCopy
			}
		} else if latestMatches && latest.State == gitcmd.OperationConflict && originalConflictOperation(latest) {
			latestCopy := latest
			recoveryOperation = &latestCopy
			originalConflict = &latestCopy
		} else if latestMatches {
			latestCopy := latest
			recoveryOperation = &latestCopy
		}
		if unfinishedTransition {
			conflictOperation, found, conflictErr := m.recoveryConflictOperation(ctx, current, existingStatus)
			if conflictErr == nil && found {
				conflictCopy := conflictOperation
				originalConflict = &conflictCopy
			}
			if originalConflict == nil {
				conflictOperation, found, conflictErr = m.operations.LatestConflictByPath(ctx, current.Path)
				if conflictErr != nil {
					recoveryErrors = append(recoveryErrors, persistenceError(conflictErr))
					m.coordinator.SetConflict(current.Path, true)
					m.coordinator.Unlock()
					continue
				}
				if found && originalConflictOperation(conflictOperation) && sameManagerIdentity(conflictOperation, current) {
					conflictCopy := conflictOperation
					originalConflict = &conflictCopy
				}
			}
		}
		durableConflict := !unfinishedTransition && originalConflict != nil
		durableConflictPaths := []string(nil)
		if originalConflict != nil {
			durableConflictPaths = append(durableConflictPaths, originalConflict.ConflictPaths...)
		}
		if statusFound && existingStatus.State == model.GitStateConflict {
			durableConflictPaths = append(durableConflictPaths, existingStatus.ChangedPaths...)
		}
		result, serviceErr := m.gitService.RecoverLocal(ctx, gitcmd.RecoveryOptions{Snapshot: current, Operation: recoveryOperation})
		status := managerStatusFromPrevious(current, existingStatus, statusFound)
		if result.RemoteOID != "" {
			status.RemoteOID = result.RemoteOID
		}
		var conflict *gitcmd.ConflictError
		isConflict := errors.As(serviceErr, &conflict)
		if durableConflict {
			if !isConflict {
				conflict = &gitcmd.ConflictError{}
			}
			conflict.Paths = sortedManagerPaths(append(conflict.Paths, durableConflictPaths...))
			isConflict = true
		}
		var transitionErr error
		if active, found := unfinishedByPath[current.Path]; found {
			if active.Kind == gitcmd.OperationConflictAbort && isConflict && originalConflict != nil {
				active.State = gitcmd.OperationFailed
				active.Error = &gitcmd.SafeError{Code: gitcmd.CodeOperationInterrupted, Message: "Git conflict abort was interrupted"}
				active.UpdatedAt = m.now().UTC()
				if finishErr := m.operations.Finish(ctx, active); finishErr != nil {
					recoveryErrors = append(recoveryErrors, persistenceError(finishErr))
					m.coordinator.SetConflict(current.Path, true)
					m.coordinator.Unlock()
					continue
				}
				status.State = model.GitStateConflict
				status.OperationID = originalConflict.ID
				status.Stage = string(originalConflict.Stage)
				status.ChangedPaths = sortedManagerPaths(append(result.ConflictPaths, conflict.Paths...))
				status.Error = &model.APIError{Code: string(gitcmd.CodeGitConflict), Message: "Git merge has conflicts"}
				if statusErr := m.applyStatus(ctx, status, GitFailurePreserve); statusErr != nil {
					recoveryErrors = append(recoveryErrors, statusPersistenceError(statusErr))
				}
				m.coordinator.SetConflict(current.Path, true)
				m.coordinator.Unlock()
				continue
			}
			if active.Kind == gitcmd.OperationConflictComplete &&
				(result.ConflictState == gitcmd.RecoveryNeedsReindex || result.ConflictState == gitcmd.RecoveryPushPending || result.ConflictState == gitcmd.RecoveryPushUnknown) {
				if result.ConflictState == gitcmd.RecoveryNeedsReindex {
					if _, activeNow, _ := m.snapshot(current.Name); activeNow {
						if syncErr := m.notes.SyncFS(); syncErr != nil {
							recoveryErrors = append(recoveryErrors, syncErr)
							m.coordinator.SetConflict(current.Path, true)
							m.coordinator.Unlock()
							continue
						}
					}
					checkpoint := gitcmd.Checkpoint{
						Stage: gitcmd.StageConflictReindexed, BackupRef: active.BackupRef, LocalOID: active.LocalOID,
						CandidateOID: active.CandidateOID, RemoteOID: active.RemoteOID, PushOID: active.PushOID,
						ChangedPaths: active.ChangedPaths, ConflictPaths: active.ConflictPaths,
					}
					if checkpointErr := m.operations.Checkpoint(ctx, active.ID, checkpoint); checkpointErr != nil {
						recoveryErrors = append(recoveryErrors, persistenceError(checkpointErr))
						m.coordinator.SetConflict(current.Path, true)
						m.coordinator.Unlock()
						continue
					}
					active.Stage = gitcmd.StageConflictReindexed
				}
				active.State = gitcmd.OperationFailed
				active.Error = &gitcmd.SafeError{Code: gitcmd.CodeOperationInterrupted, Message: "Git operation was interrupted"}
				if result.ConflictState == gitcmd.RecoveryPushUnknown {
					active.Error = &gitcmd.SafeError{Code: gitcmd.CodeRecoveryRequired, Message: "Git push outcome is unknown"}
				}
				active.UpdatedAt = m.now().UTC()
				status.State = model.GitStateError
				status.OperationID = active.ID
				status.Stage = string(active.Stage)
				status.ChangedPaths = sortedManagerPaths(active.ChangedPaths)
				status.Error = &model.APIError{Code: string(active.Error.Code), Message: active.Error.Message}
				if statusErr := m.applyStatus(ctx, status, GitFailurePreserve); statusErr != nil {
					recoveryErrors = append(recoveryErrors, statusPersistenceError(statusErr))
					m.coordinator.SetConflict(current.Path, true)
					m.coordinator.Unlock()
					continue
				}
				if finishErr := m.operations.Finish(ctx, active); finishErr != nil {
					recoveryErrors = append(recoveryErrors, persistenceError(finishErr))
					m.coordinator.SetConflict(current.Path, true)
					m.coordinator.Unlock()
					continue
				}
				m.coordinator.SetConflict(current.Path, false)
				m.coordinator.Unlock()
				continue
			}
			if active.Kind == gitcmd.OperationConflictComplete && result.ConflictState == gitcmd.RecoveryPushed {
				if _, activeNow, _ := m.snapshot(current.Name); activeNow {
					if syncErr := m.notes.SyncFS(); syncErr != nil {
						recoveryErrors = append(recoveryErrors, syncErr)
						m.coordinator.SetConflict(current.Path, true)
						m.coordinator.Unlock()
						continue
					}
				}
				active.State = gitcmd.OperationSucceeded
				active.Stage = gitcmd.StageCompleted
				active.Error = nil
				active.RemoteOID = result.RemoteOID
				active.UpdatedAt = m.now().UTC()
				status.State = model.GitStateReady
				status.OperationID = active.ID
				status.Stage = string(gitcmd.StageCompleted)
				status.ChangedPaths = sortedManagerPaths(active.ChangedPaths)
				status.RemoteOID = result.RemoteOID
				status.Error = nil
				if statusErr := m.applyStatus(ctx, status, GitFailureReset); statusErr != nil {
					recoveryErrors = append(recoveryErrors, statusPersistenceError(statusErr))
					m.coordinator.SetConflict(current.Path, true)
					m.coordinator.Unlock()
					continue
				}
				if finishErr := m.operations.Finish(ctx, active); finishErr != nil {
					recoveryErrors = append(recoveryErrors, persistenceError(finishErr))
					m.coordinator.SetConflict(current.Path, true)
					m.coordinator.Unlock()
					continue
				}
				m.coordinator.SetConflict(current.Path, false)
				m.coordinator.Unlock()
				continue
			}
			if active.Kind == gitcmd.OperationConflictAbort && result.ConflictState == gitcmd.RecoveryAborted {
				active.State = gitcmd.OperationSucceeded
				active.Stage = gitcmd.StageCompleted
				active.Error = nil
				active.UpdatedAt = m.now().UTC()
				status.State = model.GitStatePaused
				status.OperationID = active.ID
				status.Stage = string(gitcmd.StageCompleted)
				status.ChangedPaths = sortedManagerPaths(active.ChangedPaths)
				status.Error = nil
				if statusErr := m.applyStatus(ctx, status, GitFailurePreserve); statusErr != nil {
					recoveryErrors = append(recoveryErrors, statusPersistenceError(statusErr))
					m.coordinator.SetConflict(current.Path, true)
					m.coordinator.Unlock()
					continue
				}
				if finishErr := m.operations.Finish(ctx, active); finishErr != nil {
					recoveryErrors = append(recoveryErrors, persistenceError(finishErr))
					m.coordinator.SetConflict(current.Path, true)
					m.coordinator.Unlock()
					continue
				}
				m.coordinator.SetConflict(current.Path, false)
				m.coordinator.Unlock()
				continue
			}
			if result.RemoteOID != "" && active.RemoteOID != result.RemoteOID {
				checkpoint := gitcmd.Checkpoint{
					Stage: active.Stage, BackupRef: active.BackupRef, LocalOID: active.LocalOID,
					CandidateOID: active.CandidateOID, RemoteOID: result.RemoteOID, PushOID: active.PushOID,
					ChangedPaths: active.ChangedPaths, ConflictPaths: active.ConflictPaths,
				}
				if checkpointErr := m.operations.Checkpoint(ctx, active.ID, checkpoint); checkpointErr != nil {
					transitionErr = persistenceError(checkpointErr)
				} else {
					active.RemoteOID = result.RemoteOID
				}
			}
			active.UpdatedAt = m.now().UTC()
			if isConflict {
				active.State = gitcmd.OperationConflict
				active.Error = &gitcmd.SafeError{Code: gitcmd.CodeGitConflict, Message: "Git merge has conflicts"}
				active.ConflictPaths = sortedManagerPaths(append(result.ConflictPaths, conflict.Paths...))
			} else {
				active.State = gitcmd.OperationFailed
				active.Error = &gitcmd.SafeError{Code: gitcmd.CodeOperationInterrupted, Message: "Git operation was interrupted"}
			}
			if transitionErr == nil {
				if finishErr := m.operations.Finish(ctx, active); finishErr != nil {
					transitionErr = persistenceError(finishErr)
				}
			}
		}
		if _, found := unfinishedByPath[current.Path]; !found && recoveryOperation != nil &&
			recoveryOperation.Kind == gitcmd.OperationConflictComplete &&
			(result.ConflictState == gitcmd.RecoveryNeedsReindex || result.ConflictState == gitcmd.RecoveryPushPending || result.ConflictState == gitcmd.RecoveryPushUnknown) {
			if result.ConflictState == gitcmd.RecoveryNeedsReindex {
				if _, activeNow, _ := m.snapshot(current.Name); activeNow {
					if syncErr := m.notes.SyncFS(); syncErr != nil {
						recoveryErrors = append(recoveryErrors, syncErr)
						m.coordinator.SetConflict(current.Path, true)
						m.coordinator.Unlock()
						continue
					}
				}
			}
			status.State = model.GitStateError
			status.OperationID = recoveryOperation.ID
			status.Stage = string(recoveryOperation.Stage)
			status.ChangedPaths = sortedManagerPaths(recoveryOperation.ChangedPaths)
			status.Error = &model.APIError{Code: string(gitcmd.CodeOperationInterrupted), Message: "Git operation was interrupted"}
			if result.ConflictState == gitcmd.RecoveryPushUnknown {
				status.Error = &model.APIError{Code: string(gitcmd.CodeRecoveryRequired), Message: "Git push outcome is unknown"}
			}
			if statusErr := m.applyStatus(ctx, status, GitFailurePreserve); statusErr != nil {
				recoveryErrors = append(recoveryErrors, statusPersistenceError(statusErr))
				m.coordinator.SetConflict(current.Path, true)
			} else {
				m.coordinator.SetConflict(current.Path, false)
			}
			m.coordinator.Unlock()
			continue
		}
		if transitionErr != nil {
			recoveryErrors = append(recoveryErrors, transitionErr)
			m.coordinator.SetConflict(current.Path, true)
			if isConflict {
				status.State = model.GitStateConflict
				status.ChangedPaths = sortedManagerPaths(append(result.ConflictPaths, conflict.Paths...))
				status.Error = &model.APIError{Code: string(gitcmd.CodeGitConflict), Message: "Git merge has conflicts"}
			} else {
				safeErr := managerSafeError(transitionErr)
				status.State = model.GitStateNeedsReconnect
				status.Error = &model.APIError{Code: string(safeErr.Code), Message: safeErr.Message, Field: safeErr.Field}
			}
			if statusErr := m.applyStatus(ctx, status, GitFailurePreserve); statusErr != nil {
				recoveryErrors = append(recoveryErrors, statusPersistenceError(statusErr))
			}
			m.coordinator.Unlock()
			continue
		}

		switch {
		case isConflict:
			m.coordinator.SetConflict(current.Path, true)
			paths := sortedManagerPaths(append(result.ConflictPaths, conflict.Paths...))
			status.State = model.GitStateConflict
			status.ChangedPaths = paths
			status.Error = &model.APIError{Code: string(gitcmd.CodeGitConflict), Message: "Git merge has conflicts"}
		case serviceErr != nil || result.Blocking || canonicalErr != nil:
			m.coordinator.SetConflict(current.Path, true)
			status.State = model.GitStateNeedsReconnect
			safeErr := managerSafeError(errors.Join(serviceErr, canonicalErr))
			status.Error = &model.APIError{Code: string(safeErr.Code), Message: safeErr.Message, Field: safeErr.Field}
		default:
			m.coordinator.SetConflict(current.Path, false)
			status.State = model.GitStateReady
			if statusFound && existingStatus.State == model.GitStatePaused {
				status.State = model.GitStatePaused
			} else {
				status.Error = nil
			}
		}
		if statusErr := m.applyStatus(ctx, status, GitFailurePreserve); statusErr != nil {
			recoveryErrors = append(recoveryErrors, statusPersistenceError(statusErr))
		}
		m.coordinator.Unlock()
	}
	return errors.Join(recoveryErrors...)
}

func canonicalManagerSnapshot(snapshot gitcmd.ConfiguredBase) (gitcmd.ConfiguredBase, error) {
	path, err := canonicalExistingDirectory(snapshot.Path)
	if err != nil {
		return gitcmd.ConfiguredBase{}, err
	}
	snapshot.Path = path
	return snapshot, nil
}

func sameManagerIdentity(operation gitcmd.Operation, snapshot gitcmd.ConfiguredBase) bool {
	return operation.BaseName == snapshot.Name && operation.RepoPath == snapshot.Path &&
		operation.ConfigFingerprint == snapshot.Fingerprint && operation.RemoteFingerprint == snapshot.RemoteFingerprint
}

func newManagerOperationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func managerWorkingStatus(operation gitcmd.Operation, previous model.GitStatus, found bool) model.GitStatus {
	status := model.GitStatus{ChangedPaths: []string{}}
	if found {
		status = previous
		status.Error = nil
	}
	status.Base = operation.BaseName
	status.RepositoryPath = operation.RepoPath
	status.OperationID = operation.ID
	status.Stage = string(operation.Stage)
	status.RemoteOID = operation.RemoteOID
	if len(operation.ChangedPaths) != 0 {
		status.ChangedPaths = sortedManagerPaths(operation.ChangedPaths)
	}
	if operation.Kind == gitcmd.OperationInitialize {
		status.State = model.GitStateInitializing
	} else {
		status.State = model.GitStateSyncing
	}
	return status
}

func managerStatusFromPrevious(current gitcmd.ConfiguredBase, previous model.GitStatus, found bool) model.GitStatus {
	if !found {
		previous = model.GitStatus{ChangedPaths: []string{}}
	}
	previous.Base = current.Name
	previous.RepositoryPath = current.Path
	if previous.ChangedPaths == nil {
		previous.ChangedPaths = []string{}
	}
	return previous
}

func managerAdmissionTrust(
	kind gitcmd.OperationKind,
	snapshot gitcmd.ConfiguredBase,
	status model.GitStatus,
	statusFound bool,
	latest gitcmd.Operation,
	latestFound bool,
) (string, error) {
	if !latestFound || latest.RemoteFingerprint != snapshot.RemoteFingerprint {
		if kind == gitcmd.OperationSync {
			return "", needsReconnect("Git trusted remote state is missing")
		}
		return "", nil
	}
	journalOID := managerOperationTrustedOID(latest, status, statusFound)
	statusOID := ""
	if statusFound {
		statusOID = status.RemoteOID
	}
	if statusOID != "" && journalOID != "" && statusOID != journalOID {
		return "", needsReconnect("Git trusted remote state does not match")
	}
	if kind == gitcmd.OperationSync && (statusOID == "" || journalOID == "") {
		return "", needsReconnect("Git trusted remote state is missing")
	}
	if statusOID != "" {
		return statusOID, nil
	}
	return journalOID, nil
}

func managerRuntimeInitializeTrust(
	snapshot gitcmd.ConfiguredBase,
	status model.GitStatus,
	statusFound bool,
	operation gitcmd.Operation,
) (string, error) {
	return managerAdmissionTrust(gitcmd.OperationInitialize, snapshot, status, statusFound, operation, true)
}

func managerRuntimeSyncTrust(
	snapshot gitcmd.ConfiguredBase,
	status model.GitStatus,
	statusFound bool,
	operation gitcmd.Operation,
) (string, error) {
	return managerAdmissionTrust(gitcmd.OperationSync, snapshot, status, statusFound, operation, true)
}

func managerOperationTrustedOID(operation gitcmd.Operation, status model.GitStatus, statusFound bool) string {
	if operation.State == gitcmd.OperationSucceeded && operation.Stage == gitcmd.StageCompleted &&
		operation.PushOID != "" && statusFound && status.State == model.GitStateReady && status.RemoteOID == operation.PushOID {
		return operation.PushOID
	}
	return operation.RemoteOID
}

func requireManagerConfirmations(required model.GitRequiredMutations, confirmations model.GitConfirmations) error {
	missing := required.CreateRepository && !confirmations.CreateRepository ||
		required.ReplaceOrigin && !confirmations.ReplaceOrigin ||
		required.CreateBranch && !confirmations.CreateBranch ||
		required.MergeHistories && !confirmations.MergeHistories
	if missing {
		return &gitcmd.SafeError{Code: gitcmd.CodeConfirmationRequired, Message: "Current Git changes require confirmation"}
	}
	return nil
}

func applyManagerResult(operation *gitcmd.Operation, result gitcmd.OperationResult) {
	if result.BackupRef != "" {
		operation.BackupRef = result.BackupRef
	}
	if result.RemoteOID != "" {
		operation.RemoteOID = result.RemoteOID
	}
	if result.PushOID != "" {
		operation.PushOID = result.PushOID
	}
	if result.ChangedPaths != nil {
		operation.ChangedPaths = sortedManagerPaths(result.ChangedPaths)
	}
	if result.ConflictPaths != nil {
		operation.ConflictPaths = sortedManagerPaths(result.ConflictPaths)
	}
}

func sortedManagerPaths(paths []string) []string {
	result := append([]string(nil), paths...)
	sort.Strings(result)
	result = compactManagerPaths(result)
	if result == nil {
		return []string{}
	}
	return result
}

func compactManagerPaths(paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	write := 1
	for read := 1; read < len(paths); read++ {
		if paths[read] != paths[write-1] {
			paths[write] = paths[read]
			write++
		}
	}
	return paths[:write]
}

func fullyConfiguredManagerBase(base gitcmd.ConfiguredBase) bool {
	return base.Name != "" && base.Path != "" && base.URL != "" && base.Branch != "" &&
		base.Fingerprint != "" && base.RemoteFingerprint != ""
}

func needsReconnect(message string) *gitcmd.SafeError {
	return &gitcmd.SafeError{Code: gitcmd.CodeNeedsReconnect, Message: message}
}

func persistenceError(error) *gitcmd.SafeError {
	return &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Git operation persistence failed"}
}

func statusPersistenceError(error) *gitcmd.SafeError {
	return &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Git status persistence failed"}
}

func managerJournalError(err error) error {
	if err == nil {
		return nil
	}
	return &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Git operation journal persistence failed"}
}

func managerStatusError(err error) error {
	if err == nil {
		return nil
	}
	return statusPersistenceError(err)
}

func managerSafeError(err error) *gitcmd.SafeError {
	if err == nil {
		return &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Git command failed"}
	}
	var safeErr *gitcmd.SafeError
	if errors.As(err, &safeErr) {
		return &gitcmd.SafeError{
			Code: safeErr.Code, Message: safeErr.Message, Field: safeErr.Field, ExitCode: safeErr.ExitCode,
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return &gitcmd.SafeError{Code: gitcmd.CodeCanceled, Message: "Git command was canceled"}
	case errors.Is(err, context.DeadlineExceeded):
		return &gitcmd.SafeError{Code: gitcmd.CodeTimedOut, Message: "Git command timed out"}
	default:
		return &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Git command failed"}
	}
}

func safeErrorFromAPI(apiErr *model.APIError) *gitcmd.SafeError {
	if apiErr == nil {
		return &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Git command failed"}
	}
	return &gitcmd.SafeError{Code: gitcmd.ErrorCode(apiErr.Code), Message: apiErr.Message, Field: apiErr.Field}
}
