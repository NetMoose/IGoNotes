package git

import (
	"context"
	"errors"
	"strings"
)

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
		return RecoveryResult{Blocking: true}, &SafeError{Code: CodeCommandFailed, Message: "Git repository inspection failed", cause: err}
	}
	if locked {
		return RecoveryResult{Blocking: true}, &SafeError{Code: CodeRepositoryLocked, Message: "Git repository is locked"}
	}

	head, _, err := s.optionalCommitOID(ctx, path, "HEAD")
	if err != nil {
		return RecoveryResult{Blocking: true}, err
	}
	managed, managedExists, err := s.optionalRefOID(ctx, path, managedRemoteRef(options.Snapshot.Branch))
	if err != nil {
		return RecoveryResult{HeadOID: head, Blocking: true}, err
	}
	result := RecoveryResult{HeadOID: head}
	if managedExists {
		result.RemoteOID = managed
	}
	result.RemoteOID, err = s.recoveryRemoteOID(ctx, path, options.Operation, head, managed, managedExists)
	trustErr := err

	mergeHead, mergeExists, paths, mergeStateErr := s.inspectMergeState(ctx, path)
	result.MergeHeadOID = mergeHead
	result.ConflictPaths = paths
	if mergeStateErr != nil {
		result.Blocking = true
		return result, errors.Join(newConflictError(paths), interruptedMergeState(), mergeStateErr, trustErr)
	}
	if len(paths) != 0 {
		result.Blocking = true
		conflictErr := newConflictError(paths)
		if !mergeExists {
			return result, errors.Join(conflictErr, interruptedMergeState(), trustErr)
		}
		return result, errors.Join(conflictErr, trustErr)
	}
	if local.PendingOperation == "merge" || mergeExists {
		result.Blocking = true
		return result, errors.Join(
			&SafeError{Code: CodeOperationInterrupted, Message: "Git merge is awaiting completion"}, trustErr,
		)
	}
	if local.PendingOperation != "" {
		result.Blocking = true
		return result, errors.Join(
			&SafeError{Code: CodeOperationInterrupted, Message: "Git repository has an unfinished operation"}, trustErr,
		)
	}
	if trustErr != nil {
		result.Blocking = true
		return result, trustErr
	}
	return result, nil
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
