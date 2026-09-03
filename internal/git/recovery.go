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

	if local.PendingOperation == "merge" {
		mergeHead, exists, mergeErr := s.optionalCommitOID(ctx, path, "MERGE_HEAD")
		if mergeErr != nil {
			return RecoveryResult{HeadOID: head, RemoteOID: result.RemoteOID, Blocking: true}, mergeErr
		}
		if !exists {
			return RecoveryResult{HeadOID: head, RemoteOID: result.RemoteOID, Blocking: true},
				&SafeError{Code: CodeOperationInterrupted, Message: "Git merge state is incomplete"}
		}
		paths, pathsErr := s.unmergedPaths(ctx, path)
		result.MergeHeadOID = mergeHead
		result.ConflictPaths = paths
		result.Blocking = true
		if len(paths) != 0 || pathsErr != nil {
			conflictErr := newConflictError(paths)
			if pathsErr != nil {
				return result, errors.Join(conflictErr, pathsErr)
			}
			return result, conflictErr
		}
		return result, &SafeError{Code: CodeOperationInterrupted, Message: "Git merge is awaiting completion"}
	}
	if local.PendingOperation != "" {
		result.Blocking = true
		return result, &SafeError{Code: CodeOperationInterrupted, Message: "Git repository has an unfinished operation"}
	}

	if operation := options.Operation; operation != nil {
		if operation.PushOID != "" && managedExists && managed == operation.PushOID && head == operation.PushOID {
			result.RemoteOID = operation.PushOID
			return result, nil
		}
		if operation.CandidateOID != "" && operation.RemoteOID != operation.CandidateOID {
			private, privateExists, privateErr := s.optionalRefOID(ctx, path, "refs/igonotes/fetch/"+operation.ID)
			if privateErr != nil {
				result.RemoteOID = ""
				result.Blocking = true
				return result, privateErr
			}
			if !managedExists || managed != operation.CandidateOID || !privateExists || private != operation.CandidateOID {
				result.RemoteOID = ""
				result.Blocking = true
				return result, &SafeError{Code: CodeNeedsReconnect, Message: "Git trusted remote recovery is ambiguous"}
			}
			result.RemoteOID = operation.CandidateOID
		}
	}
	return result, nil
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
