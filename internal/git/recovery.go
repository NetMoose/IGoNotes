package git

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

func (s *Service) AbortConflict(
	ctx context.Context,
	base ConfiguredBase,
	operation Operation,
	worktree WorktreeTransaction,
	progress Progress,
) error {
	if worktree == nil || progress == nil {
		return ErrRecoveryRequired
	}
	ctx, err := pinConnectBase(ctx, base.Path)
	if err != nil {
		return err
	}
	if err := s.verifyConflictAbort(ctx, base, operation); err != nil {
		return err
	}
	return runWorktree(ctx, worktree, func(path string) error {
		if path != base.Path || !filepath.IsAbs(path) {
			return ErrRecoveryRequired
		}
		if err := requireConnectBase(ctx, path); err != nil {
			return err
		}
		if err := s.verifyConflictAbort(ctx, base, operation); err != nil {
			return err
		}
		paths, err := s.conflictAbortPaths(ctx, path, operation.LocalOID)
		if err != nil {
			return err
		}
		if err := progress(ctx, Checkpoint{
			BackupRef:     operation.BackupRef,
			Stage:         StageConflictAborting,
			LocalOID:      operation.LocalOID,
			CandidateOID:  operation.CandidateOID,
			RemoteOID:     operation.RemoteOID,
			PushOID:       operation.PushOID,
			ChangedPaths:  paths,
			ConflictPaths: append([]string(nil), operation.ConflictPaths...),
		}); err != nil {
			return err
		}
		if result, err := s.runLocal(ctx, path, false, "merge", "--abort"); err != nil || result.StdoutTruncated || result.StderrTruncated {
			return ErrRecoveryRequired
		}
		head, mergeHead, mergeExists, paths, err := s.abortResult(ctx, path)
		if err != nil || mergeExists || len(paths) != 0 || head != operation.LocalOID || mergeHead != "" {
			return ErrRecoveryRequired
		}
		return nil
	})
}

func (s *Service) verifyConflictAbort(ctx context.Context, base ConfiguredBase, operation Operation) error {
	if s == nil || s.runner == nil || s.porcelain == nil || !validConflictAbortRequest(base, operation) {
		return ErrRecoveryRequired
	}
	local, err := s.porcelain.InspectLocal(ctx, base.Path)
	if err != nil || validConflictInspection(base, local) != nil || conflictMarkersClear(local.GitDir) != nil {
		return ErrRecoveryRequired
	}
	head, err := s.conflictCommit(ctx, local.RepositoryRoot, "HEAD^{commit}")
	if err != nil || head != operation.LocalOID {
		return ErrRecoveryRequired
	}
	mergeHead, err := s.conflictCommit(ctx, local.RepositoryRoot, "MERGE_HEAD^{commit}")
	if err != nil || mergeHead != operation.CandidateOID || mergeHead != operation.RemoteOID {
		return ErrRecoveryRequired
	}
	trusted, err := s.conflictCommit(ctx, local.RepositoryRoot, managedRemoteRef(base.Branch)+"^{commit}")
	if err != nil || trusted != operation.RemoteOID {
		return ErrRecoveryRequired
	}
	return nil
}

func validConflictAbortRequest(base ConfiguredBase, operation Operation) bool {
	return base.Name != "" && filepath.IsAbs(base.Path) && validLiteralBranch(base.Branch) && validOperationID(operation.ID) &&
		operation.Kind == OperationConflictAbort && operation.BaseName == base.Name && operation.RepoPath == base.Path &&
		operation.Branch == base.Branch && validObjectID(operation.LocalOID) && validObjectID(operation.CandidateOID) &&
		validObjectID(operation.RemoteOID)
}

func (s *Service) conflictAbortPaths(ctx context.Context, path, localOID string) ([]string, error) {
	result, err := s.runLocal(ctx, path, true, "diff", "--name-only", "-z", localOID, "--")
	if err != nil {
		return nil, ErrRecoveryRequired
	}
	paths, err := parseNULPaths(result)
	if err != nil {
		return nil, ErrRecoveryRequired
	}
	return paths, nil
}

func (s *Service) abortResult(ctx context.Context, path string) (string, string, bool, []string, error) {
	head, err := s.conflictCommit(ctx, path, "HEAD^{commit}")
	if err != nil {
		return "", "", false, nil, err
	}
	mergeHead, mergeExists, paths, err := s.inspectMergeState(ctx, path)
	if err != nil {
		return "", "", false, nil, err
	}
	return head, mergeHead, mergeExists, paths, nil
}

func (s *Service) RecoverLocal(ctx context.Context, options RecoveryOptions) (RecoveryResult, error) {
	path, err := s.validateRecovery(options)
	if err != nil {
		return RecoveryResult{Blocking: true}, err
	}
	ctx, err = pinConnectBase(ctx, path)
	if err != nil {
		return RecoveryResult{Blocking: true}, err
	}
	if err := s.porcelain.ValidateBranch(ctx, path, options.Snapshot.Branch); err != nil {
		return RecoveryResult{Blocking: true}, err
	}
	if err := requireConnectBase(ctx, path); err != nil {
		return RecoveryResult{Blocking: true}, err
	}
	local, err := s.porcelain.InspectLocal(ctx, path)
	if err != nil {
		return RecoveryResult{Blocking: true}, err
	}
	if !local.HasRepository {
		return RecoveryResult{Blocking: true}, &SafeError{Code: CodeNeedsReconnect, Message: "Git base directory must be reconnected"}
	}
	if local.RepositoryRoot != path {
		return RecoveryResult{Blocking: true}, &SafeError{Code: CodeRepositoryRoot, Message: "Git repository root does not match the base directory"}
	}
	if local.DetachedHead || local.CurrentBranch != options.Snapshot.Branch {
		return RecoveryResult{Blocking: true}, &SafeError{Code: CodeNeedsReconnect, Message: "Configured Git branch is not selected"}
	}
	if err := s.requireOrigin(ctx, path, options.Snapshot.URL); err != nil {
		return RecoveryResult{Blocking: true}, err
	}
	locked, err := syncIndexLocked(local.GitDir)
	if err != nil {
		return RecoveryResult{Blocking: true, ConflictState: RecoveryAmbiguous}, &SafeError{Code: CodeCommandFailed, Message: "Git repository inspection failed", cause: err}
	}
	if locked {
		return RecoveryResult{Blocking: true, ConflictState: RecoveryLocked}, &SafeError{Code: CodeRepositoryLocked, Message: "Git repository is locked"}
	}

	head, _, err := s.optionalCommitOID(ctx, path, "HEAD")
	if err != nil {
		return RecoveryResult{Blocking: true}, err
	}
	managed, managedExists, err := s.optionalRefOID(ctx, path, managedRemoteRef(options.Snapshot.Branch))
	if err != nil {
		return RecoveryResult{HeadOID: head, Blocking: true}, err
	}
	result := RecoveryResult{HeadOID: head, PushOID: recoveryPushOID(options.Operation)}
	if managedExists {
		result.RemoteOID = managed
	}
	result.RemoteOID, err = s.recoveryRemoteOID(ctx, path, options.Operation, head, managed, managedExists)
	trustErr := err

	mergeHead, mergeExists, paths, mergeStateErr := s.inspectMergeState(ctx, path)
	result.MergeHeadOID = mergeHead
	result.ConflictPaths = paths
	if mergeStateErr != nil {
		result.Blocking, result.ConflictState = true, RecoveryAmbiguous
		return result, errors.Join(newConflictError(paths), interruptedMergeState(), mergeStateErr, trustErr)
	}
	if local.PendingOperation != "" && local.PendingOperation != "merge" {
		result.Blocking, result.ConflictState = true, RecoveryAmbiguous
		return result, errors.Join(
			&SafeError{Code: CodeOperationInterrupted, Message: "Git repository has an unfinished operation"}, trustErr,
		)
	}
	if len(paths) != 0 {
		result.Blocking, result.ConflictState = true, RecoveryConflict
		conflictErr := newConflictError(paths)
		if !mergeExists {
			return result, errors.Join(conflictErr, interruptedMergeState(), trustErr)
		}
		return result, errors.Join(conflictErr, trustErr)
	}
	if local.PendingOperation == "merge" || mergeExists {
		result.Blocking, result.ConflictState = true, RecoveryCanComplete
		return result, errors.Join(
			&SafeError{Code: CodeOperationInterrupted, Message: "Git merge is awaiting completion"}, trustErr,
		)
	}
	state := recoveryConflictState(options.Operation, head, managed, managedExists)
	if state == RecoveryAborted || state == RecoveryNeedsReindex || state == RecoveryPushPending || state == RecoveryPushUnknown || state == RecoveryPushed {
		result.ConflictState = state
		if trustErr == nil || state != RecoveryPushed {
			return result, nil
		}
	}
	if trustErr != nil || state == RecoveryAmbiguous {
		result.Blocking, result.ConflictState = true, RecoveryAmbiguous
		if trustErr == nil {
			trustErr = ErrRecoveryRequired
		}
		return result, trustErr
	}
	return result, nil
}

func recoveryPushOID(operation *Operation) string {
	if operation == nil || operation.Kind != OperationConflictComplete || !validObjectID(operation.PushOID) {
		return ""
	}
	switch operation.Stage {
	case StageConflictCommitted, StageConflictReindexed, StageConflictPushing, StageCompleted:
		return operation.PushOID
	default:
		return ""
	}
}

func recoveryConflictState(operation *Operation, head, managed string, managedExists bool) ConflictRecoveryState {
	if operation == nil {
		return ""
	}
	if operation.Kind == OperationConflictAbort {
		if (operation.State == OperationQueued || operation.State == OperationRunning) && head == operation.LocalOID {
			return RecoveryAborted
		}
		return RecoveryAmbiguous
	}
	if operation.Kind == OperationConflictComplete {
		if recoveryPushOID(operation) == "" || head != operation.PushOID {
			return RecoveryAmbiguous
		}
		switch operation.Stage {
		case StageConflictCommitted:
			return RecoveryNeedsReindex
		case StageConflictReindexed:
			return RecoveryPushPending
		case StageConflictPushing, StageCompleted:
			if managedExists && managed == operation.PushOID {
				return RecoveryPushed
			}
			return RecoveryPushUnknown
		}
	}
	if operation.State == OperationConflict {
		return RecoveryAmbiguous
	}
	return ""
}

func (s *Service) recoveryRemoteOID(
	ctx context.Context,
	path string,
	operation *Operation,
	head string,
	managed string,
	managedExists bool,
) (string, error) {
	if operation == nil {
		if managedExists {
			return managed, nil
		}
		return "", nil
	}
	if operation.RemoteOID != "" && managedExists && managed == operation.RemoteOID {
		return operation.RemoteOID, nil
	}
	if operation.PushOID != "" && managedExists && managed == operation.PushOID && head == operation.PushOID {
		return operation.PushOID, nil
	}
	candidateGap, err := s.provenRecoveryCandidate(ctx, path, operation, managed, managedExists)
	if err != nil {
		return "", err
	}
	if candidateGap {
		return operation.CandidateOID, nil
	}
	if operation.RemoteOID != "" || operation.CandidateOID != "" {
		return "", &SafeError{Code: CodeNeedsReconnect, Message: "Git trusted remote recovery is ambiguous"}
	}
	if managedExists {
		return managed, nil
	}
	return "", nil
}

func (s *Service) provenRecoveryCandidate(
	ctx context.Context,
	path string,
	operation *Operation,
	managed string,
	managedExists bool,
) (bool, error) {
	if operation.CandidateOID == "" || !managedExists || managed != operation.CandidateOID {
		return false, nil
	}
	private, privateExists, err := s.optionalRefOID(ctx, path, "refs/igonotes/fetch/"+operation.ID)
	if err != nil {
		return false, err
	}
	return privateExists && private == operation.CandidateOID, nil
}

func (s *Service) validateRecovery(options RecoveryOptions) (string, error) {
	if s == nil || s.runner == nil || s.porcelain == nil {
		return "", &SafeError{Code: CodeUnavailable, Message: "Git executable is unavailable"}
	}
	snapshot := options.Snapshot
	if snapshot.Name == "" || snapshot.Path == "" || snapshot.URL == "" || snapshot.Branch == "" ||
		snapshot.Fingerprint == "" || snapshot.RemoteFingerprint == "" || strings.HasPrefix(snapshot.URL, "-") ||
		containsControlOutputByte(snapshot.URL) {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Invalid Git recovery snapshot"}
	}
	if operation := options.Operation; operation != nil {
		if operation.BaseName != snapshot.Name || operation.RepoPath != snapshot.Path || operation.Branch != snapshot.Branch ||
			operation.ConfigFingerprint != snapshot.Fingerprint || operation.RemoteFingerprint != snapshot.RemoteFingerprint ||
			!validOperationID(operation.ID) {
			return "", &SafeError{Code: CodeCommandFailed, Message: "Invalid Git recovery operation"}
		}
		for _, oid := range []string{operation.LocalOID, operation.CandidateOID, operation.RemoteOID, operation.PushOID} {
			if oid != "" && !validObjectID(oid) {
				return "", &SafeError{Code: CodeCommandFailed, Message: "Invalid Git recovery operation"}
			}
		}
	}
	path, err := canonicalDirectory(snapshot.Path)
	if err != nil {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Git repository inspection failed", cause: err}
	}
	if path != snapshot.Path {
		return "", &SafeError{Code: CodeRepositoryRoot, Message: "Git repository root does not match the base directory"}
	}
	return path, nil
}
