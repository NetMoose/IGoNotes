package git

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	backupTimestampLayout = "20060102T150405.000000000Z"
	initialCommitMessage  = "IGoNotes: initial snapshot"
	localMergeMessage     = "IGoNotes: merge local snapshot"
	zeroObjectID          = "0000000000000000000000000000000000000000"
)

type ConflictError struct {
	Paths []string
}

func newConflictError(paths []string) *ConflictError {
	return &ConflictError{Paths: sortedUnique(paths)}
}

func (e *ConflictError) Error() string {
	return "Git merge has conflicts"
}

func (e *ConflictError) Unwrap() error {
	return &SafeError{Code: CodeGitConflict, Message: "Git merge has conflicts"}
}

type connectCheckpoint struct {
	progress Progress
	value    Checkpoint
}

func newConnectCheckpoint(options InitializeOptions, progress Progress) *connectCheckpoint {
	return &connectCheckpoint{
		progress: progress,
		value: Checkpoint{
			Stage:         options.Operation.Stage,
			BackupRef:     options.Operation.BackupRef,
			LocalOID:      options.Operation.LocalOID,
			CandidateOID:  options.Operation.CandidateOID,
			RemoteOID:     options.Operation.RemoteOID,
			PushOID:       options.Operation.PushOID,
			ChangedPaths:  append([]string(nil), options.Operation.ChangedPaths...),
			ConflictPaths: append([]string(nil), options.Operation.ConflictPaths...),
		},
	}
}

func (c *connectCheckpoint) save(ctx context.Context, stage Stage) error {
	c.value.Stage = stage
	c.value.ChangedPaths = sortedUnique(c.value.ChangedPaths)
	c.value.ConflictPaths = sortedUnique(c.value.ConflictPaths)
	checkpoint := c.value
	checkpoint.ChangedPaths = append([]string(nil), c.value.ChangedPaths...)
	checkpoint.ConflictPaths = append([]string(nil), c.value.ConflictPaths...)
	return c.progress(ctx, checkpoint)
}

type connectActual struct {
	hadRepository bool
	remoteEmpty   bool
	remoteOID     string
	snapshotOID   string
	fetchedOID    string
	pushOID       string
	backupRef     string
	changedPaths  []string
}

func (s *Service) Initialize(
	ctx context.Context,
	options InitializeOptions,
	worktree WorktreeTransaction,
	progress Progress,
) (OperationResult, error) {
	path, err := s.validateInitialize(ctx, options, worktree, progress)
	if err != nil {
		return OperationResult{}, err
	}
	checkpoint := newConnectCheckpoint(options, progress)
	actual := connectActual{backupRef: options.Operation.BackupRef}

	local, err := s.inspectSafeLocal(ctx, path)
	if err != nil {
		return OperationResult{}, err
	}
	actual.hadRepository = local.HasRepository
	if err := s.preflightInitializeRefs(ctx, path, options, local); err != nil {
		return OperationResult{}, err
	}

	remote, err := s.porcelain.InspectRemote(ctx, path, options.Snapshot.URL)
	if err != nil {
		return OperationResult{}, err
	}
	actual.remoteEmpty = remote.Empty
	actual.remoteOID = remote.Branches[options.Snapshot.Branch]
	if remote.Empty && options.LastRemoteOID != "" {
		return OperationResult{}, &SafeError{Code: CodeBranchDeleted, Message: "Selected Git branch no longer exists"}
	}
	if !remote.Empty && actual.remoteOID == "" {
		return OperationResult{}, &SafeError{Code: CodeBranchDeleted, Message: "Selected Git branch no longer exists"}
	}
	if actual.remoteOID != "" && !validObjectID(actual.remoteOID) {
		return OperationResult{}, malformedOutputError()
	}
	if options.Operation.PushOID != "" && actual.remoteOID == options.Operation.PushOID {
		return s.finishAcceptedInitialize(ctx, path, options, worktree, checkpoint, actual.remoteOID)
	}

	err = runWorktree(ctx, worktree, func(callbackPath string) error {
		if err := requireCallbackPath(path, callbackPath); err != nil {
			return err
		}
		current, err := s.inspectSafeLocal(ctx, path)
		if err != nil {
			return err
		}
		if !current.HasRepository {
			if !options.Confirmations.CreateRepository {
				return confirmationRequired("Creating a Git repository requires confirmation")
			}
			if err := checkpoint.save(ctx, StageProbing); err != nil {
				return err
			}
			if _, err := s.runLocal(ctx, path, false, "init", "--initial-branch", options.Snapshot.Branch); err != nil {
				return err
			}
			current, err = s.inspectSafeLocal(ctx, path)
			if err != nil {
				return err
			}
		}
		if err := s.ensureOrigin(ctx, path, options.Snapshot.URL, options.Confirmations.ReplaceOrigin, checkpoint); err != nil {
			return err
		}

		var snapshotOID string
		var changed []string
		if options.Operation.LocalOID != "" && options.Operation.BackupRef != "" {
			// A durable backup checkpoint identifies the original snapshot even if a
			// switch or merge completed before its following checkpoint was stored.
			snapshotOID, err = s.commitOID(ctx, path, options.Operation.LocalOID)
			if err != nil {
				return err
			}
			if snapshotOID != options.Operation.LocalOID {
				return &SafeError{Code: CodeBackupMismatch, Message: "Git backup reference does not match the local snapshot"}
			}
			changed = append([]string(nil), options.Operation.ChangedPaths...)
		} else {
			allowEmpty := actual.remoteEmpty && !current.HasCommits
			snapshotOID, changed, err = s.snapshotInitialTree(
				ctx, path, allowEmpty, !actual.remoteEmpty || options.Confirmations.CreateBranch, checkpoint,
			)
			if err != nil {
				return err
			}
		}
		actual.snapshotOID = snapshotOID
		actual.changedPaths = changed
		checkpoint.value.LocalOID = snapshotOID
		checkpoint.value.ChangedPaths = changed
		if err := checkpoint.save(ctx, StageSnapshotting); err != nil {
			return err
		}

		needsBackup := snapshotOID != "" && ((!actual.remoteEmpty) ||
			(actual.hadRepository && (current.DetachedHead || current.CurrentBranch != options.Snapshot.Branch)))
		if needsBackup {
			backupRef, err := s.ensureBackup(ctx, path, options.Operation, snapshotOID, checkpoint)
			if err != nil {
				return err
			}
			actual.backupRef = backupRef
		}
		return nil
	})
	if err != nil {
		return OperationResult{}, err
	}

	if !actual.remoteEmpty {
		if err := checkpoint.save(ctx, StageFetching); err != nil {
			return OperationResult{}, err
		}
		fetchRef := "refs/igonotes/fetch/" + options.Operation.ID
		refspec := "+refs/heads/" + options.Snapshot.Branch + ":" + fetchRef
		if _, err := s.runNetwork(ctx, path, options.Snapshot.URL, false,
			"fetch", "--no-tags", "--show-forced-updates", "origin", refspec); err != nil {
			return OperationResult{}, err
		}
		actual.fetchedOID, err = s.commitOID(ctx, path, fetchRef)
		if err != nil {
			return OperationResult{}, err
		}
		if actual.fetchedOID != actual.remoteOID {
			return OperationResult{}, &SafeError{Code: CodeOperationInterrupted, Message: "Git remote changed during initialization"}
		}
		checkpoint.value.CandidateOID = actual.fetchedOID
		if err := checkpoint.save(ctx, StageFetching); err != nil {
			return OperationResult{}, err
		}
	}

	err = runWorktree(ctx, worktree, func(callbackPath string) error {
		if err := requireCallbackPath(path, callbackPath); err != nil {
			return err
		}
		if _, err := s.inspectSafeLocal(ctx, path); err != nil {
			return err
		}
		if !actual.remoteEmpty {
			if err := s.establishRemoteTrust(ctx, path, options, actual.fetchedOID, checkpoint); err != nil {
				return err
			}
		}
		if err := s.selectAndMerge(ctx, path, options, actual, checkpoint); err != nil {
			return err
		}
		actual.pushOID, err = s.commitOID(ctx, path, "HEAD")
		if err != nil {
			return err
		}
		checkpoint.value.PushOID = actual.pushOID
		return checkpoint.save(ctx, StageReindexing)
	})
	if err != nil {
		return OperationResult{}, err
	}
	if err := checkpoint.save(ctx, StageReindexing); err != nil {
		return OperationResult{}, err
	}

	if err := s.requireRemoteUnchanged(ctx, path, options.Snapshot, actual); err != nil {
		return OperationResult{}, err
	}
	if actual.remoteEmpty && !options.Confirmations.CreateBranch {
		return OperationResult{}, confirmationRequired("Creating a remote Git branch requires confirmation")
	}
	if err := checkpoint.save(ctx, StagePushing); err != nil {
		return OperationResult{}, err
	}
	refspec := actual.pushOID + ":refs/heads/" + options.Snapshot.Branch
	if _, err := s.runNetwork(ctx, path, options.Snapshot.URL, false,
		"push", "--no-verify", "--porcelain", "origin", refspec); err != nil {
		return OperationResult{}, err
	}
	if err := checkpoint.save(ctx, StagePushing); err != nil {
		return OperationResult{}, err
	}

	err = runWorktree(ctx, worktree, func(callbackPath string) error {
		if err := requireCallbackPath(path, callbackPath); err != nil {
			return err
		}
		expected := actual.fetchedOID
		if actual.remoteEmpty {
			expected = zeroObjectID
		}
		if err := checkpoint.save(ctx, StagePushing); err != nil {
			return err
		}
		if _, err := s.runLocal(ctx, path, false, "update-ref", managedRemoteRef(options.Snapshot.Branch), actual.pushOID, expected); err != nil {
			current, exists, inspectErr := s.optionalRefOID(ctx, path, managedRemoteRef(options.Snapshot.Branch))
			if inspectErr != nil {
				return inspectErr
			}
			if !exists || current != actual.pushOID {
				return err
			}
		}
		checkpoint.value.CandidateOID = actual.pushOID
		checkpoint.value.RemoteOID = actual.pushOID
		return checkpoint.save(ctx, StageCompleted)
	})
	if err != nil {
		return OperationResult{}, err
	}

	return operationResult(actual, checkpoint.value), nil
}

func (s *Service) validateInitialize(
	ctx context.Context,
	options InitializeOptions,
	worktree WorktreeTransaction,
	progress Progress,
) (string, error) {
	if s == nil || s.runner == nil || s.porcelain == nil {
		return "", &SafeError{Code: CodeUnavailable, Message: "Git executable is unavailable"}
	}
	if worktree == nil || progress == nil {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Git initialization callbacks are required"}
	}
	snapshot := options.Snapshot
	if snapshot.Name == "" || snapshot.Path == "" || snapshot.URL == "" || snapshot.Branch == "" ||
		snapshot.Fingerprint == "" || snapshot.RemoteFingerprint == "" || strings.HasPrefix(snapshot.URL, "-") ||
		containsControlOutputByte(snapshot.URL) {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Invalid Git initialization snapshot"}
	}
	if options.Operation.Kind != OperationInitialize || options.Operation.BaseName != snapshot.Name ||
		options.Operation.RepoPath != snapshot.Path ||
		options.Operation.Branch != snapshot.Branch || options.Operation.ConfigFingerprint != snapshot.Fingerprint ||
		options.Operation.RemoteFingerprint != snapshot.RemoteFingerprint || options.Operation.CreatedAt.IsZero() ||
		!validOperationID(options.Operation.ID) {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Invalid Git initialization operation"}
	}
	for _, oid := range []string{options.Operation.LocalOID, options.Operation.CandidateOID, options.Operation.RemoteOID,
		options.Operation.PushOID, options.LastRemoteOID} {
		if oid != "" && !validObjectID(oid) {
			return "", &SafeError{Code: CodeCommandFailed, Message: "Invalid Git initialization operation"}
		}
	}
	path, err := canonicalDirectory(snapshot.Path)
	if err != nil {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Git repository inspection failed", cause: err}
	}
	if path != snapshot.Path || path != options.Operation.RepoPath {
		return "", &SafeError{Code: CodeRepositoryRoot, Message: "Git repository root does not match the base directory"}
	}
	if err := s.porcelain.ValidateBranch(ctx, path, snapshot.Branch); err != nil {
		return "", err
	}
	if options.Operation.BackupRef != "" && !validBackupRef(options.Operation.BackupRef, options.Operation.CreatedAt) {
		return "", &SafeError{Code: CodeBackupMismatch, Message: "Git backup reference does not match the operation"}
	}
	if options.Operation.BackupRef != "" && options.Operation.LocalOID == "" {
		return "", &SafeError{Code: CodeBackupMismatch, Message: "Git backup reference does not match the operation"}
	}
	return path, nil
}

func validOperationID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func requireCallbackPath(expected, callbackPath string) error {
	canonical, err := canonicalDirectory(callbackPath)
	if err != nil || canonical != expected {
		return &SafeError{Code: CodeRepositoryRoot, Message: "Git repository root does not match the base directory", cause: err}
	}
	return nil
}

func (s *Service) inspectSafeLocal(ctx context.Context, path string) (LocalInspection, error) {
	local, err := s.porcelain.InspectLocal(ctx, path)
	if err != nil {
		return LocalInspection{}, err
	}
	if local.HasRepository && local.RepositoryRoot != path {
		return LocalInspection{}, &SafeError{Code: CodeRepositoryRoot, Message: "Git repository root does not match the base directory"}
	}
	if local.PendingOperation != "" {
		return LocalInspection{}, &SafeError{Code: CodeRepositoryLocked, Message: "Git repository has a pending operation"}
	}
	if !local.IdentityConfigured {
		return LocalInspection{}, &SafeError{Code: CodeIdentityMissing, Message: "Git identity is not configured"}
	}
	return local, nil
}

func (s *Service) preflightInitializeRefs(
	ctx context.Context,
	path string,
	options InitializeOptions,
	local LocalInspection,
) error {
	if !local.HasRepository {
		if options.LastRemoteOID != "" || options.Operation.BackupRef != "" {
			return &SafeError{Code: CodeOperationInterrupted, Message: "Git repository state does not match the initialization journal"}
		}
		return nil
	}
	urls, exists, err := s.originURLs(ctx, path)
	if err != nil {
		return err
	}
	if exists && (len(urls) != 1 || urls[0] != options.Snapshot.URL) && !options.Confirmations.ReplaceOrigin {
		return &SafeError{Code: CodeOriginMismatch, Message: "Configured origin does not match"}
	}
	if _, _, err := s.optionalCommitOID(ctx, path, "refs/heads/"+options.Snapshot.Branch); err != nil {
		return err
	}
	if options.Operation.BackupRef != "" {
		backupOID, backupExists, err := s.optionalRefOID(ctx, path, options.Operation.BackupRef)
		if err != nil {
			return err
		}
		if backupExists && backupOID != options.Operation.LocalOID {
			return &SafeError{Code: CodeBackupMismatch, Message: "Git backup reference does not match the local snapshot"}
		}
	}
	privateOID := ""
	privateExists := false
	if options.Operation.CandidateOID != "" {
		privateOID, privateExists, err = s.optionalRefOID(ctx, path, "refs/igonotes/fetch/"+options.Operation.ID)
		if err != nil {
			return err
		}
		if privateExists && privateOID != options.Operation.CandidateOID {
			return remoteHistoryRewritten()
		}
	}
	managedOID, managedExists, err := s.optionalRefOID(ctx, path, managedRemoteRef(options.Snapshot.Branch))
	if err != nil {
		return err
	}
	if options.LastRemoteOID == "" {
		return nil
	}
	if managedExists && managedOID == options.LastRemoteOID {
		return nil
	}
	if managedExists && managedOID == options.Operation.CandidateOID && privateExists && privateOID == managedOID {
		return nil
	}
	return remoteHistoryRewritten()
}

func (s *Service) ensureOrigin(
	ctx context.Context,
	path, expectedURL string,
	replace bool,
	checkpoint *connectCheckpoint,
) error {
	urls, exists, err := s.originURLs(ctx, path)
	if err != nil {
		return err
	}
	if exists && len(urls) == 1 && urls[0] == expectedURL {
		return nil
	}
	if exists && !replace {
		return &SafeError{Code: CodeOriginMismatch, Message: "Configured origin does not match"}
	}
	if err := checkpoint.save(ctx, StageProbing); err != nil {
		return err
	}
	args := []string{"remote", "add", "origin", expectedURL}
	if exists {
		args = []string{"remote", "set-url", "origin", expectedURL}
	}
	if _, err := s.run(ctx, path, LocalOperation, false, expectedURL, args...); err != nil {
		return err
	}
	urls, exists, err = s.originURLs(ctx, path)
	if err != nil {
		return err
	}
	if !exists || len(urls) != 1 || urls[0] != expectedURL {
		return &SafeError{Code: CodeOriginMismatch, Message: "Configured origin does not match"}
	}
	return checkpoint.save(ctx, StageProbing)
}

func (s *Service) originURLs(ctx context.Context, path string) ([]string, bool, error) {
	result, err := s.runLocal(ctx, path, true, "remote", "get-url", "--all", "origin")
	if err != nil {
		if expectedExit(err, 2) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if result.StdoutTruncated || result.StderrTruncated || result.Stdout == "" || !strings.HasSuffix(result.Stdout, "\n") {
		return nil, false, malformedOutputError()
	}
	lines := strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n")
	for _, line := range lines {
		if line == "" || strings.ContainsAny(line, "\r\x00") {
			return nil, false, malformedOutputError()
		}
	}
	return lines, true, nil
}

func (s *Service) snapshotInitialTree(
	ctx context.Context,
	path string,
	allowEmpty bool,
	allowBranchCreation bool,
	checkpoint *connectCheckpoint,
) (string, []string, error) {
	if _, err := s.inspectSafeLocal(ctx, path); err != nil {
		return "", nil, err
	}
	if err := checkpoint.save(ctx, StageSnapshotting); err != nil {
		return "", nil, err
	}
	if _, err := s.runLocal(ctx, path, false, "add", "--all", "--", "."); err != nil {
		return "", nil, err
	}
	changed, err := s.stagedChanges(ctx, path)
	if err != nil {
		return "", nil, err
	}
	paths := []string(nil)
	if changed {
		paths, err = s.stagedPaths(ctx, path)
		if err != nil {
			return "", nil, err
		}
		_, hasHEAD, err := s.optionalCommitOID(ctx, path, "HEAD")
		if err != nil {
			return "", nil, err
		}
		if !hasHEAD && !allowBranchCreation {
			return "", nil, confirmationRequired("Creating a Git branch requires confirmation")
		}
		if err := checkpoint.save(ctx, StageSnapshotting); err != nil {
			return "", nil, err
		}
		if _, err := s.runLocal(ctx, path, false, "commit", "-m", initialCommitMessage); err != nil {
			return "", nil, err
		}
	} else {
		_, hasHEAD, err := s.optionalCommitOID(ctx, path, "HEAD")
		if err != nil {
			return "", nil, err
		}
		if !hasHEAD {
			if !allowEmpty {
				return "", nil, nil
			}
			if !allowBranchCreation {
				return "", nil, confirmationRequired("Creating a Git branch requires confirmation")
			}
			if err := checkpoint.save(ctx, StageSnapshotting); err != nil {
				return "", nil, err
			}
			if _, err := s.runLocal(ctx, path, false, "commit", "--allow-empty", "-m", initialCommitMessage); err != nil {
				return "", nil, err
			}
		}
	}
	oid, exists, err := s.optionalCommitOID(ctx, path, "HEAD")
	if err != nil {
		return "", nil, err
	}
	if !exists {
		return "", paths, nil
	}
	return oid, paths, nil
}

func (s *Service) stagedChanges(ctx context.Context, path string) (bool, error) {
	result, err := s.runLocal(ctx, path, true, "diff", "--cached", "--quiet", "--exit-code")
	if result.StdoutTruncated || result.StderrTruncated {
		return false, malformedOutputError()
	}
	if err == nil {
		return false, nil
	}
	if expectedExit(err, 1) {
		return true, nil
	}
	return false, err
}

func (s *Service) stagedPaths(ctx context.Context, path string) ([]string, error) {
	result, err := s.runLocal(ctx, path, true, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return nil, err
	}
	return parseNULPaths(result)
}

func backupRefBase(createdAt time.Time) string {
	return "refs/igonotes/backups/" + createdAt.UTC().Format(backupTimestampLayout)
}

func backupRefCandidate(base string, collision int) string {
	if collision == 0 {
		return base
	}
	return base + "-" + strconv.Itoa(collision)
}

func validBackupRef(ref string, createdAt time.Time) bool {
	base := backupRefBase(createdAt)
	if ref == base {
		return true
	}
	suffix, found := strings.CutPrefix(ref, base+"-")
	if !found || suffix == "" || suffix[0] == '0' {
		return false
	}
	for _, character := range suffix {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (s *Service) ensureBackup(
	ctx context.Context,
	path string,
	operation Operation,
	snapshotOID string,
	checkpoint *connectCheckpoint,
) (string, error) {
	base := backupRefBase(operation.CreatedAt)
	candidate := operation.BackupRef
	collision := 0
	if candidate != "" {
		if !validBackupRef(candidate, operation.CreatedAt) {
			return "", &SafeError{Code: CodeBackupMismatch, Message: "Git backup reference does not match the operation"}
		}
		if candidate != base {
			suffix := strings.TrimPrefix(candidate, base+"-")
			collision, _ = strconv.Atoi(suffix)
		}
	} else {
		for {
			candidate = backupRefCandidate(base, collision)
			_, exists, err := s.optionalRefOID(ctx, path, candidate)
			if err != nil {
				return "", err
			}
			if !exists {
				break
			}
			collision++
		}
	}

	for {
		existing, exists, err := s.optionalRefOID(ctx, path, candidate)
		if err != nil {
			return "", err
		}
		if exists {
			if existing == snapshotOID {
				checkpoint.value.BackupRef = candidate
				if err := checkpoint.save(ctx, StageBackingUp); err != nil {
					return "", err
				}
				return candidate, nil
			}
			if operation.BackupRef != "" {
				return "", &SafeError{Code: CodeBackupMismatch, Message: "Git backup reference does not match the local snapshot"}
			}
			collision++
			candidate = backupRefCandidate(base, collision)
			continue
		}

		checkpoint.value.BackupRef = candidate
		if err := checkpoint.save(ctx, StageBackingUp); err != nil {
			return "", err
		}
		if _, err := s.runLocal(ctx, path, false, "update-ref", "--create-reflog", "-m",
			"IGoNotes initial-connect backup", candidate, snapshotOID, zeroObjectID); err != nil {
			existing, exists, inspectErr := s.optionalRefOID(ctx, path, candidate)
			if inspectErr != nil {
				return "", inspectErr
			}
			if exists && existing == snapshotOID {
				// The zero-OID race completed the intended durable ref.
			} else if exists && operation.BackupRef == "" {
				collision++
				candidate = backupRefCandidate(base, collision)
				continue
			} else if exists {
				return "", &SafeError{Code: CodeBackupMismatch, Message: "Git backup reference does not match the local snapshot"}
			} else {
				return "", err
			}
		}
		if err := checkpoint.save(ctx, StageBackingUp); err != nil {
			return "", err
		}
		return candidate, nil
	}
}

func (s *Service) establishRemoteTrust(
	ctx context.Context,
	path string,
	options InitializeOptions,
	candidate string,
	checkpoint *connectCheckpoint,
) error {
	managedRef := managedRemoteRef(options.Snapshot.Branch)
	current, exists, err := s.optionalRefOID(ctx, path, managedRef)
	if err != nil {
		return err
	}
	expected := zeroObjectID
	if exists {
		expected = current
	}
	if options.LastRemoteOID != "" {
		trusted := options.LastRemoteOID
		switch {
		case exists && current == trusted:
			expected = trusted
		case exists && current == candidate && options.Operation.CandidateOID == candidate:
			privateOID, privateExists, privateErr := s.optionalRefOID(ctx, path, "refs/igonotes/fetch/"+options.Operation.ID)
			if privateErr != nil {
				return privateErr
			}
			if !privateExists || privateOID != candidate {
				return remoteHistoryRewritten()
			}
			checkpoint.value.RemoteOID = candidate
			return checkpoint.save(ctx, StageFetching)
		default:
			return remoteHistoryRewritten()
		}
		if trusted != candidate {
			if err := s.requireAncestor(ctx, path, trusted, candidate); err != nil {
				return err
			}
		}
	}
	if current == candidate {
		checkpoint.value.RemoteOID = candidate
		return checkpoint.save(ctx, StageFetching)
	}
	if err := checkpoint.save(ctx, StageFetching); err != nil {
		return err
	}
	if _, err := s.runLocal(ctx, path, false, "update-ref", managedRef, candidate, expected); err != nil {
		current, exists, inspectErr := s.optionalRefOID(ctx, path, managedRef)
		if inspectErr != nil {
			return inspectErr
		}
		if !exists || current != candidate {
			return err
		}
	}
	checkpoint.value.RemoteOID = candidate
	return checkpoint.save(ctx, StageFetching)
}

func (s *Service) selectAndMerge(
	ctx context.Context,
	path string,
	options InitializeOptions,
	actual connectActual,
	checkpoint *connectCheckpoint,
) error {
	branch := options.Snapshot.Branch
	currentBranch, detached, err := s.currentBranch(ctx, path)
	if err != nil {
		return err
	}
	branchOID, branchExists, err := s.optionalCommitOID(ctx, path, "refs/heads/"+branch)
	if err != nil {
		return err
	}

	if !branchExists && !actual.remoteEmpty && actual.snapshotOID == "" && !detached && currentBranch == branch {
		collision, err := s.ignoredCheckoutCollision(ctx, path, actual.fetchedOID)
		if err != nil {
			return err
		}
		if collision {
			return &SafeError{Code: CodeOperationInterrupted, Message: "Git checkout would overwrite ignored local files"}
		}
		if err := checkpoint.save(ctx, StageSwitching); err != nil {
			return err
		}
		if _, err := s.runLocal(ctx, path, false, "checkout", actual.fetchedOID, "--", "."); err != nil {
			return err
		}
		if err := checkpoint.save(ctx, StageSwitching); err != nil {
			return err
		}
		if _, err := s.runLocal(ctx, path, false, "update-ref", "refs/heads/"+branch, actual.fetchedOID, zeroObjectID); err != nil {
			return err
		}
		if err := checkpoint.save(ctx, StageSwitching); err != nil {
			return err
		}
		branchExists = true
		branchOID = actual.fetchedOID
		currentBranch = branch
	} else if branchExists {
		if detached || currentBranch != branch {
			if err := checkpoint.save(ctx, StageSwitching); err != nil {
				return err
			}
			if _, err := s.runLocal(ctx, path, false, "switch", branch); err != nil {
				return err
			}
		}
	} else {
		startOID := actual.snapshotOID
		if !actual.remoteEmpty {
			startOID = actual.fetchedOID
		} else if !options.Confirmations.CreateBranch {
			return confirmationRequired("Creating a Git branch requires confirmation")
		}
		if startOID == "" {
			return &SafeError{Code: CodeCommandFailed, Message: "Git branch cannot be created without a commit"}
		}
		if err := checkpoint.save(ctx, StageSwitching); err != nil {
			return err
		}
		if _, err := s.runLocal(ctx, path, false, "switch", "-c", branch, startOID); err != nil {
			return err
		}
		branchExists = true
		branchOID = startOID
	}
	_ = branchExists
	_ = branchOID
	if err := checkpoint.save(ctx, StageSwitching); err != nil {
		return err
	}

	if actual.snapshotOID != "" {
		if err := s.mergeIfNeeded(ctx, path, actual.snapshotOID, localMergeMessage, options.Confirmations.MergeHistories, checkpoint); err != nil {
			return err
		}
	}
	if !actual.remoteEmpty {
		message := "IGoNotes: merge origin/" + branch
		if err := s.mergeIfNeeded(ctx, path, actual.fetchedOID, message, options.Confirmations.MergeHistories, checkpoint); err != nil {
			return err
		}
	}
	return checkpoint.save(ctx, StageMerging)
}

func (s *Service) mergeIfNeeded(
	ctx context.Context,
	path, oid, message string,
	allowUnrelated bool,
	checkpoint *connectCheckpoint,
) error {
	head, err := s.commitOID(ctx, path, "HEAD")
	if err != nil {
		return err
	}
	ancestor, err := s.isAncestor(ctx, path, oid, head)
	if err != nil || ancestor {
		return err
	}
	unrelated, err := s.unrelated(ctx, path, head, oid)
	if err != nil {
		return err
	}
	if unrelated && !allowUnrelated {
		return confirmationRequired("Merging unrelated Git histories requires confirmation")
	}
	if err := checkpoint.save(ctx, StageMerging); err != nil {
		return err
	}
	args := []string{"merge", "--no-edit", "-m", message}
	if unrelated {
		args = append(args, "--allow-unrelated-histories")
	}
	args = append(args, oid)
	if _, err := s.runLocal(ctx, path, false, args...); err != nil {
		paths, inspectErr := s.unmergedPaths(ctx, path)
		if inspectErr != nil {
			return inspectErr
		}
		if len(paths) != 0 {
			checkpoint.value.ConflictPaths = paths
			if progressErr := checkpoint.save(ctx, StageMerging); progressErr != nil {
				return progressErr
			}
			return newConflictError(paths)
		}
		return err
	}
	return checkpoint.save(ctx, StageMerging)
}

func (s *Service) requireRemoteUnchanged(
	ctx context.Context,
	path string,
	snapshot ConfiguredBase,
	actual connectActual,
) error {
	remote, err := s.inspectOriginRemote(ctx, path, snapshot.URL)
	if err != nil {
		return err
	}
	if actual.remoteEmpty {
		if !remote.Empty {
			return &SafeError{Code: CodeOperationInterrupted, Message: "Git remote changed during initialization"}
		}
		return nil
	}
	if remote.Empty || remote.Branches[snapshot.Branch] != actual.fetchedOID {
		return &SafeError{Code: CodeOperationInterrupted, Message: "Git remote changed during initialization"}
	}
	oid, err := s.selectedRemoteOID(ctx, path, snapshot.URL, snapshot.Branch)
	if err != nil {
		return err
	}
	if oid != actual.fetchedOID {
		return &SafeError{Code: CodeOperationInterrupted, Message: "Git remote changed during initialization"}
	}
	return nil
}

func (s *Service) inspectOriginRemote(ctx context.Context, path, secret string) (RemoteInspection, error) {
	result, err := s.runNetwork(ctx, path, secret, true, "ls-remote", "--symref", "origin")
	if err != nil {
		return RemoteInspection{}, err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return RemoteInspection{}, malformedOutputError()
	}
	return parseRemoteInspection(result.Stdout)
}

func (s *Service) selectedRemoteOID(ctx context.Context, path, secret, branch string) (string, error) {
	ref := "refs/heads/" + branch
	result, err := s.runNetwork(ctx, path, secret, true, "ls-remote", "--exit-code", "--heads", "origin", ref)
	if err != nil {
		if expectedExit(err, 2) {
			return "", &SafeError{Code: CodeBranchDeleted, Message: "Selected Git branch no longer exists"}
		}
		return "", err
	}
	if result.StdoutTruncated || result.StderrTruncated || !strings.HasSuffix(result.Stdout, "\n") {
		return "", malformedOutputError()
	}
	line := strings.TrimSuffix(result.Stdout, "\n")
	oid, gotRef, found := strings.Cut(line, "\t")
	if !found || gotRef != ref || !validObjectID(oid) || strings.Contains(line, "\n") {
		return "", malformedOutputError()
	}
	return oid, nil
}

func (s *Service) finishAcceptedInitialize(
	ctx context.Context,
	path string,
	options InitializeOptions,
	worktree WorktreeTransaction,
	checkpoint *connectCheckpoint,
	acceptedOID string,
) (OperationResult, error) {
	if !validObjectID(acceptedOID) {
		return OperationResult{}, malformedOutputError()
	}
	var backupRef = options.Operation.BackupRef
	err := runWorktree(ctx, worktree, func(callbackPath string) error {
		if err := requireCallbackPath(path, callbackPath); err != nil {
			return err
		}
		local, err := s.inspectSafeLocal(ctx, path)
		if err != nil {
			return err
		}
		if !local.HasRepository {
			return &SafeError{Code: CodeOperationInterrupted, Message: "Git repository is missing after push"}
		}
		urls, exists, err := s.originURLs(ctx, path)
		if err != nil {
			return err
		}
		if !exists || len(urls) != 1 || urls[0] != options.Snapshot.URL {
			return &SafeError{Code: CodeOriginMismatch, Message: "Configured origin does not match"}
		}
		head, err := s.commitOID(ctx, path, "HEAD")
		if err != nil {
			return err
		}
		if head != acceptedOID {
			return &SafeError{Code: CodeOperationInterrupted, Message: "Git HEAD changed after push"}
		}
		branch, detached, err := s.currentBranch(ctx, path)
		if err != nil {
			return err
		}
		if detached || branch != options.Snapshot.Branch {
			return &SafeError{Code: CodeOperationInterrupted, Message: "Git branch changed after push"}
		}
		managed := managedRemoteRef(options.Snapshot.Branch)
		current, exists, err := s.optionalRefOID(ctx, path, managed)
		if err != nil {
			return err
		}
		if !exists || current != acceptedOID {
			if options.LastRemoteOID != "" {
				if !exists {
					return remoteHistoryRewritten()
				}
				trustedForRetry := options.LastRemoteOID
				if current != trustedForRetry {
					trustedForRetry = options.Operation.RemoteOID
					if trustedForRetry == "" {
						trustedForRetry = options.Operation.CandidateOID
					}
					privateOID, privateExists, privateErr := s.optionalRefOID(ctx, path, "refs/igonotes/fetch/"+options.Operation.ID)
					if privateErr != nil {
						return privateErr
					}
					if trustedForRetry == "" || current != trustedForRetry || !privateExists || privateOID != trustedForRetry {
						return remoteHistoryRewritten()
					}
				}
				if err := s.requireAncestor(ctx, path, trustedForRetry, acceptedOID); err != nil {
					return err
				}
			}
			expected := zeroObjectID
			if exists {
				expected = current
			}
			if _, err := s.runLocal(ctx, path, false, "update-ref", managed, acceptedOID, expected); err != nil {
				return err
			}
		}
		checkpoint.value.CandidateOID = acceptedOID
		checkpoint.value.RemoteOID = acceptedOID
		checkpoint.value.PushOID = acceptedOID
		return checkpoint.save(ctx, StageCompleted)
	})
	if err != nil {
		return OperationResult{}, err
	}
	actual := connectActual{pushOID: acceptedOID, backupRef: backupRef}
	return operationResult(actual, checkpoint.value), nil
}

func operationResult(actual connectActual, checkpoint Checkpoint) OperationResult {
	return OperationResult{
		HeadOID:       actual.pushOID,
		RemoteOID:     actual.pushOID,
		PushOID:       actual.pushOID,
		BackupRef:     actual.backupRef,
		ChangedPaths:  append([]string(nil), checkpoint.ChangedPaths...),
		ConflictPaths: append([]string(nil), checkpoint.ConflictPaths...),
	}
}

func runWorktree(ctx context.Context, worktree WorktreeTransaction, mutate func(string) error) error {
	called := false
	err := worktree(ctx, func(path string) error {
		if called {
			return &SafeError{Code: CodeOperationInterrupted, Message: "Git worktree transaction invoked its callback more than once"}
		}
		called = true
		return mutate(path)
	})
	if err != nil {
		return err
	}
	if !called {
		return &SafeError{Code: CodeOperationInterrupted, Message: "Git worktree transaction did not invoke its callback"}
	}
	return nil
}

func managedRemoteRef(branch string) string {
	return "refs/igonotes/remotes/" + branch
}

func (s *Service) currentBranch(ctx context.Context, path string) (string, bool, error) {
	result, err := s.runLocal(ctx, path, true, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if expectedExit(err, 1) {
			return "", true, nil
		}
		return "", false, err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return "", false, malformedOutputError()
	}
	branch := singleLine(result.Stdout)
	if branch == "" || containsInvalidRefOutputByte(branch) || strings.Contains(result.Stdout, "\n\n") {
		return "", false, malformedOutputError()
	}
	return branch, false, nil
}

func (s *Service) commitOID(ctx context.Context, path, revision string) (string, error) {
	oid, exists, err := s.optionalCommitOID(ctx, path, revision)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Required Git commit does not exist"}
	}
	return oid, nil
}

func (s *Service) optionalCommitOID(ctx context.Context, path, revision string) (string, bool, error) {
	result, err := s.runLocal(ctx, path, true, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		if expectedExit(err, 1) || expectedExit(err, 128) {
			return "", false, nil
		}
		return "", false, err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return "", false, malformedOutputError()
	}
	oid := singleLine(result.Stdout)
	if !validObjectID(oid) || strings.Contains(result.Stdout, "\n\n") {
		return "", false, malformedOutputError()
	}
	return oid, true, nil
}

func (s *Service) optionalRefOID(ctx context.Context, path, ref string) (string, bool, error) {
	result, err := s.runLocal(ctx, path, true, "rev-parse", "--verify", ref)
	if err != nil {
		if expectedExit(err, 1) || expectedExit(err, 128) {
			return "", false, nil
		}
		return "", false, err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return "", false, malformedOutputError()
	}
	oid := singleLine(result.Stdout)
	if !validObjectID(oid) || strings.Contains(result.Stdout, "\n\n") {
		return "", false, malformedOutputError()
	}
	return oid, true, nil
}

func (s *Service) requireAncestor(ctx context.Context, path, ancestor, descendant string) error {
	ok, err := s.isAncestor(ctx, path, ancestor, descendant)
	if err != nil {
		return err
	}
	if !ok {
		return remoteHistoryRewritten()
	}
	return nil
}

func (s *Service) isAncestor(ctx context.Context, path, ancestor, descendant string) (bool, error) {
	result, err := s.runLocal(ctx, path, true, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		if result.StdoutTruncated || result.StderrTruncated || result.Stdout != "" {
			return false, malformedOutputError()
		}
		return true, nil
	}
	if expectedExit(err, 1) {
		return false, nil
	}
	return false, err
}

func (s *Service) unrelated(ctx context.Context, path, left, right string) (bool, error) {
	result, err := s.runLocal(ctx, path, true, "merge-base", left, right)
	if err != nil {
		if expectedExit(err, 1) {
			return true, nil
		}
		return false, err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return false, malformedOutputError()
	}
	oid := singleLine(result.Stdout)
	if !validObjectID(oid) {
		return false, malformedOutputError()
	}
	return false, nil
}

func (s *Service) unmergedPaths(ctx context.Context, path string) ([]string, error) {
	result, err := s.runLocal(ctx, path, true, "ls-files", "-u", "-z")
	if err != nil {
		return nil, err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return nil, malformedOutputError()
	}
	if result.Stdout == "" {
		return nil, nil
	}
	if !strings.HasSuffix(result.Stdout, "\x00") {
		return nil, malformedOutputError()
	}
	entries := strings.Split(strings.TrimSuffix(result.Stdout, "\x00"), "\x00")
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		metadata, path, found := strings.Cut(entry, "\t")
		if !found || path == "" || strings.ContainsAny(path, "\x00\r\n") {
			return nil, malformedOutputError()
		}
		fields := strings.Fields(metadata)
		if len(fields) != 3 || !validGitMode(fields[0]) || !validObjectID(fields[1]) ||
			(fields[2] != "1" && fields[2] != "2" && fields[2] != "3") {
			return nil, malformedOutputError()
		}
		paths = append(paths, path)
	}
	return sortedUnique(paths), nil
}

func validGitMode(mode string) bool {
	if len(mode) != 6 {
		return false
	}
	for _, character := range mode {
		if character < '0' || character > '7' {
			return false
		}
	}
	return true
}

func (s *Service) ignoredCheckoutCollision(ctx context.Context, path, oid string) (bool, error) {
	ignoredResult, err := s.runLocal(ctx, path, true, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return false, err
	}
	ignored, err := parseNULPaths(ignoredResult)
	if err != nil || len(ignored) == 0 {
		return false, err
	}
	treeResult, err := s.runLocal(ctx, path, true, "ls-tree", "-r", "--name-only", "-z", oid, "--")
	if err != nil {
		return false, err
	}
	treePaths, err := parseNULPaths(treeResult)
	if err != nil {
		return false, err
	}
	tree := make(map[string]struct{}, len(treePaths))
	for _, treePath := range treePaths {
		tree[treePath] = struct{}{}
	}
	for _, ignoredPath := range ignored {
		if _, exists := tree[ignoredPath]; exists {
			return true, nil
		}
	}
	return false, nil
}

func parseNULPaths(result Result) ([]string, error) {
	if result.StdoutTruncated || result.StderrTruncated {
		return nil, malformedOutputError()
	}
	if result.Stdout == "" {
		return nil, nil
	}
	if !strings.HasSuffix(result.Stdout, "\x00") {
		return nil, malformedOutputError()
	}
	paths := strings.Split(strings.TrimSuffix(result.Stdout, "\x00"), "\x00")
	for _, path := range paths {
		if path == "" || strings.ContainsAny(path, "\x00\r\n") || filepath.IsAbs(path) {
			return nil, malformedOutputError()
		}
	}
	return sortedUnique(paths), nil
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 0
	for _, value := range result {
		if write == 0 || result[write-1] != value {
			result[write] = value
			write++
		}
	}
	return result[:write]
}

func confirmationRequired(message string) error {
	return &SafeError{Code: CodeConfirmationRequired, Message: message}
}

func remoteHistoryRewritten() error {
	return &SafeError{Code: CodeRemoteHistoryRewritten, Message: "Git remote history was rewritten"}
}
