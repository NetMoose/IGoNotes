package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

var ErrGitManagerClosed = errors.New("git manager closed")

type gitManagerJob struct {
	operation     gitcmd.Operation
	snapshot      gitcmd.ConfiguredBase
	confirmations model.GitConfirmations
}

type GitManager struct {
	gitService  *gitcmd.Service
	statuses    *repository.GitStatusRepository
	operations  *repository.GitOperationRepository
	prober      *GitProbeService
	snapshot    func(string) (gitcmd.ConfiguredBase, bool, error)
	notes       *NoteService
	coordinator *BaseOperationCoordinator

	mu        sync.Mutex
	cond      *sync.Cond
	queue     []gitManagerJob
	inFlight  map[string]gitcmd.Operation
	closed    bool
	started   bool
	workerErr error

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
		now:                       time.Now,
		beforeTerminalPublication: func() {},
	}
	manager.cond = sync.NewCond(&manager.mu)
	return manager
}

func (m *GitManager) QueueInitialize(ctx context.Context, request gitcmd.InitializeRequest) (gitcmd.Operation, bool, error) {
	return m.queueOperation(ctx, request.Snapshot, gitcmd.OperationInitialize, request.Confirmations)
}

func (m *GitManager) QueueSync(ctx context.Context, request gitcmd.SyncRequest) (gitcmd.Operation, bool, error) {
	return m.queueOperation(ctx, request.Snapshot, gitcmd.OperationSync, model.GitConfirmations{})
}

func (m *GitManager) queueOperation(
	ctx context.Context,
	requested gitcmd.ConfiguredBase,
	kind gitcmd.OperationKind,
	confirmations model.GitConfirmations,
) (gitcmd.Operation, bool, error) {
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

	status, statusFound, err := m.statuses.Get(ctx, current.Path)
	if err != nil {
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, persistenceError(err)
	}
	if kind == gitcmd.OperationSync && statusFound && status.State == model.GitStateNeedsReconnect {
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, needsReconnect("Git base directory must be reconnected")
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, ErrGitManagerClosed
	}
	if operation, found := m.inFlight[current.Path]; found {
		if sameManagerIdentity(operation, current) {
			m.mu.Unlock()
			m.coordinator.Unlock()
			return operation, true, nil
		}
		m.mu.Unlock()
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, ErrGitRepositoryInUse
	}
	active, found, err := m.operations.ActiveByPath(ctx, current.Path)
	if err != nil {
		m.mu.Unlock()
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, persistenceError(err)
	}
	if found {
		if sameManagerIdentity(active, current) {
			m.inFlight[current.Path] = active
			m.mu.Unlock()
			m.coordinator.Unlock()
			return active, true, nil
		}
		m.mu.Unlock()
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, ErrGitRepositoryInUse
	}

	latest, latestFound, err := m.operations.LatestByPath(ctx, current.Path)
	if err != nil {
		m.mu.Unlock()
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, persistenceError(err)
	}
	trusted, trustErr := managerAdmissionTrust(kind, current, status, statusFound, latest, latestFound)
	if trustErr != nil {
		m.mu.Unlock()
		m.coordinator.Unlock()
		return gitcmd.Operation{}, false, trustErr
	}
	id, err := newManagerOperationID()
	if err != nil {
		m.mu.Unlock()
		m.coordinator.Unlock()
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
		m.mu.Unlock()
		m.coordinator.Unlock()
		return operation, false, persistenceError(err)
	}
	queuedStatus := managerWorkingStatus(operation, status, statusFound)
	if err := m.statuses.Upsert(ctx, queuedStatus); err != nil {
		statusErr := statusPersistenceError(err)
		operation.State = gitcmd.OperationFailed
		operation.Error = statusErr
		finishErr := m.operations.Finish(ctx, operation)
		if finishErr != nil {
			active := operation
			active.State = gitcmd.OperationQueued
			active.Error = nil
			m.inFlight[current.Path] = active
		}
		m.mu.Unlock()
		m.coordinator.Unlock()
		return operation, false, errors.Join(statusErr, managerJournalError(finishErr))
	}
	m.inFlight[current.Path] = operation
	m.queue = append(m.queue, gitManagerJob{operation: operation, snapshot: current, confirmations: confirmations})
	m.cond.Signal()
	m.mu.Unlock()
	m.coordinator.Unlock()
	return operation, false, nil
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
	return nil
}

func (m *GitManager) Close() error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.lifetimeCancel()
		m.cond.Broadcast()
	}
	started := m.started
	if !started {
		m.doneOnce.Do(func() { close(m.workerDone) })
	}
	done := m.workerDone
	m.mu.Unlock()
	<-done
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
		job := m.queue[0]
		m.queue[0] = gitManagerJob{}
		m.queue = m.queue[1:]
		m.mu.Unlock()

		m.coordinator.Lock()
		m.runJob(job)
		m.coordinator.Unlock()
	}
}

func (m *GitManager) runJob(job gitManagerJob) {
	current, active, snapshotErr := m.snapshot(job.snapshot.Name)
	if snapshotErr == nil {
		current, snapshotErr = canonicalManagerSnapshot(current)
	}
	if snapshotErr != nil || current != job.snapshot {
		m.beforeTerminalPublication()
		finished, terminalErr := m.finishStale(job.operation)
		m.completeTerminal(job.operation.RepoPath, finished, terminalErr)
		return
	}

	operation, found, err := m.operations.ActiveByPath(m.lifetimeCtx, current.Path)
	if err != nil || !found || operation.ID != job.operation.ID {
		m.beforeTerminalPublication()
		if err == nil {
			err = errors.New("active Git operation is missing")
		}
		finished, terminalErr := m.finishFailure(job.operation, current, gitcmd.OperationResult{}, persistenceError(err), nil)
		m.completeTerminal(current.Path, finished, terminalErr)
		return
	}
	status, statusFound, statusErr := m.statuses.Get(m.lifetimeCtx, current.Path)
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
	progress := func(ctx context.Context, checkpoint gitcmd.Checkpoint) error {
		if err := m.operations.Checkpoint(ctx, operation.ID, checkpoint); err != nil {
			return persistenceError(err)
		}
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
		published.State = workingState
		if err := m.statuses.Upsert(ctx, published); err != nil {
			return persistenceError(err)
		}
		status = published
		statusFound = true
		return nil
	}

	transaction := m.worktreeTransaction(current, active)

	var result gitcmd.OperationResult
	var operationErr error
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
	default:
		operationErr = &gitcmd.SafeError{Code: gitcmd.CodeCommandFailed, Message: "Invalid Git operation"}
	}

	m.beforeTerminalPublication()
	var conflict *gitcmd.ConflictError
	if errors.As(operationErr, &conflict) {
		m.coordinator.SetConflict(current.Path, true)
	}
	if operationErr == nil {
		finished, terminalErr := m.finishSuccess(operation, current, result)
		m.completeTerminal(current.Path, finished, terminalErr)
	} else {
		finished, terminalErr := m.finishFailure(operation, current, result, managerSafeError(operationErr), conflict)
		m.completeTerminal(current.Path, finished, terminalErr)
	}
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
	changed := sortedManagerPaths(result.ChangedPaths)
	statusErr := m.statuses.Upsert(ctx, model.GitStatus{
		Base: current.Name, RepositoryPath: current.Path, State: model.GitStateReady,
		OperationID: operation.ID, Stage: string(gitcmd.StageCompleted), Ahead: result.Ahead, Behind: result.Behind,
		LastAttempt: &now, LastSuccess: &now, ChangedPaths: changed, RemoteOID: result.RemoteOID,
	})
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
	applyManagerResult(&operation, result)
	now := m.now().UTC()
	operation.UpdatedAt = now
	operation.Error = safeErr
	state := model.GitStateError
	changed := sortedManagerPaths(operation.ChangedPaths)
	if conflict != nil {
		m.coordinator.SetConflict(current.Path, true)
		operation.State = gitcmd.OperationConflict
		operation.ConflictPaths = sortedManagerPaths(append(operation.ConflictPaths, conflict.Paths...))
		changed = append([]string(nil), operation.ConflictPaths...)
		state = model.GitStateConflict
	} else {
		operation.State = gitcmd.OperationFailed
	}
	finishErr := m.operations.Finish(ctx, operation)
	remoteOID := operation.RemoteOID
	if result.RemoteOID != "" {
		remoteOID = result.RemoteOID
	}
	statusErr := m.statuses.Upsert(ctx, model.GitStatus{
		Base: current.Name, RepositoryPath: current.Path, State: state,
		OperationID: operation.ID, Stage: string(operation.Stage), Ahead: result.Ahead, Behind: result.Behind,
		LastAttempt: &now, ChangedPaths: changed, RemoteOID: remoteOID,
		Error: &model.APIError{Code: string(safeErr.Code), Message: safeErr.Message, Field: safeErr.Field},
	})
	return finishErr == nil, errors.Join(managerJournalError(lookupErr), managerJournalError(finishErr), managerStatusError(statusErr))
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
		durableConflict := latestFound && latest.State == gitcmd.OperationConflict ||
			statusFound && existingStatus.State == model.GitStateConflict
		durableConflictPaths := []string(nil)
		if latestFound && latest.State == gitcmd.OperationConflict {
			durableConflictPaths = append(durableConflictPaths, latest.ConflictPaths...)
		}
		if statusFound && existingStatus.State == model.GitStateConflict {
			durableConflictPaths = append(durableConflictPaths, existingStatus.ChangedPaths...)
		}
		var latestPointer *gitcmd.Operation
		if latestFound && sameManagerIdentity(latest, current) {
			latestCopy := latest
			latestPointer = &latestCopy
		}
		result, serviceErr := m.gitService.RecoverLocal(ctx, gitcmd.RecoveryOptions{Snapshot: current, Operation: latestPointer})
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
		if transitionErr != nil {
			recoveryErrors = append(recoveryErrors, transitionErr)
			m.coordinator.SetConflict(current.Path, true)
			status := model.GitStatus{
				Base: current.Name, RepositoryPath: current.Path, ChangedPaths: []string{}, RemoteOID: result.RemoteOID,
			}
			if isConflict {
				status.State = model.GitStateConflict
				status.ChangedPaths = sortedManagerPaths(append(result.ConflictPaths, conflict.Paths...))
				status.Error = &model.APIError{Code: string(gitcmd.CodeGitConflict), Message: "Git merge has conflicts"}
			} else {
				safeErr := managerSafeError(transitionErr)
				status.State = model.GitStateNeedsReconnect
				status.Error = &model.APIError{Code: string(safeErr.Code), Message: safeErr.Message, Field: safeErr.Field}
			}
			if statusErr := m.statuses.Upsert(ctx, status); statusErr != nil {
				recoveryErrors = append(recoveryErrors, persistenceError(statusErr))
			}
			m.coordinator.Unlock()
			continue
		}

		status := model.GitStatus{Base: current.Name, RepositoryPath: current.Path, ChangedPaths: []string{}, RemoteOID: result.RemoteOID}
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
		}
		if statusErr := m.statuses.Upsert(ctx, status); statusErr != nil {
			recoveryErrors = append(recoveryErrors, persistenceError(statusErr))
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
	status.ChangedPaths = sortedManagerPaths(operation.ChangedPaths)
	if operation.Kind == gitcmd.OperationInitialize {
		status.State = model.GitStateInitializing
	} else {
		status.State = model.GitStateSyncing
	}
	return status
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
