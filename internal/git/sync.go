package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const defaultSyncCommitTemplate = "IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)"

type syncCheckpoint struct {
	progress Progress
	value    Checkpoint
}

func newSyncCheckpoint(options SyncOptions, progress Progress) *syncCheckpoint {
	return &syncCheckpoint{
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

func (c *syncCheckpoint) save(ctx context.Context, stage Stage) error {
	c.value.Stage = stage
	c.value.ChangedPaths = sortedUnique(c.value.ChangedPaths)
	c.value.ConflictPaths = sortedUnique(c.value.ConflictPaths)
	checkpoint := c.value
	checkpoint.ChangedPaths = append([]string(nil), c.value.ChangedPaths...)
	checkpoint.ConflictPaths = append([]string(nil), c.value.ConflictPaths...)
	return c.progress(ctx, checkpoint)
}

type syncPass struct {
	candidate    string
	preHead      string
	pushOID      string
	changedPaths []string
	ahead        int
	behind       int
}

func (s *Service) Sync(
	ctx context.Context,
	options SyncOptions,
	worktree WorktreeTransaction,
	progress Progress,
) (OperationResult, error) {
	path, err := s.validateSync(options, worktree, progress)
	if err != nil {
		return OperationResult{}, err
	}
	ctx, err = pinConnectBase(ctx, path)
	if err != nil {
		return OperationResult{}, err
	}
	if err := s.porcelain.ValidateBranch(ctx, path, options.Snapshot.Branch); err != nil {
		return OperationResult{}, err
	}
	if err := s.preflightSync(ctx, path, options); err != nil {
		return OperationResult{}, err
	}

	checkpoint := newSyncCheckpoint(options, progress)
	trusted := options.LastRemoteOID
	var latest OperationResult
	for attempt := 0; attempt < 2; attempt++ {
		pass, err := s.fetchSyncCandidate(ctx, path, options, trusted, checkpoint)
		if err != nil {
			return latest, err
		}
		latest.RemoteOID = pass.candidate

		if err := s.runSyncWorktree(ctx, path, options, worktree, checkpoint, &pass); err != nil {
			latest = syncOperationResult(pass, checkpoint.value, pass.candidate)
			return latest, err
		}
		if err := checkpoint.save(ctx, StageReindexing); err != nil {
			return syncOperationResult(pass, checkpoint.value, pass.candidate), err
		}
		pass.ahead, pass.behind, err = s.syncAheadBehind(ctx, path, pass.candidate)
		latest = syncOperationResult(pass, checkpoint.value, pass.candidate)
		if err != nil {
			return latest, err
		}

		if err := checkpoint.save(ctx, StagePushing); err != nil {
			return latest, err
		}
		remoteOID, err := s.prePushSyncCheck(ctx, path, options.Snapshot, pass.candidate, pass.pushOID)
		if err != nil {
			return latest, err
		}
		if remoteOID != pass.candidate {
			if attempt == 0 {
				trusted = pass.candidate
				continue
			}
			return latest, pushRejected()
		}

		refspec := pass.pushOID + ":refs/heads/" + options.Snapshot.Branch
		_, err = s.connectRunNetwork(ctx, path, options.Snapshot.URL, false,
			"push", "--no-verify", "--porcelain", "origin", refspec)
		if err != nil {
			if attempt == 0 && isPushRejected(err) {
				trusted = pass.candidate
				continue
			}
			return latest, err
		}
		if err := checkpoint.save(ctx, StagePushing); err != nil {
			return latest, err
		}
		if err := s.completeSyncPush(ctx, path, options.Snapshot.Branch, pass, checkpoint); err != nil {
			return latest, err
		}
		latest = syncOperationResult(pass, checkpoint.value, pass.pushOID)
		latest.Ahead = 0
		latest.Behind = 0
		return latest, nil
	}
	return latest, pushRejected()
}

func (s *Service) validateSync(
	options SyncOptions,
	worktree WorktreeTransaction,
	progress Progress,
) (string, error) {
	if s == nil || s.runner == nil || s.porcelain == nil {
		return "", &SafeError{Code: CodeUnavailable, Message: "Git executable is unavailable"}
	}
	if worktree == nil || progress == nil {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Git sync callbacks are required"}
	}
	snapshot := options.Snapshot
	if snapshot.Name == "" || snapshot.Path == "" || snapshot.URL == "" || snapshot.Branch == "" ||
		snapshot.Fingerprint == "" || snapshot.RemoteFingerprint == "" || strings.HasPrefix(snapshot.URL, "-") ||
		containsControlOutputByte(snapshot.URL) || containsControlText(snapshot.Name) {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Invalid Git sync snapshot"}
	}
	if _, err := renderSyncCommitMessage(snapshot.CommitTemplate, snapshot.Name, snapshot.Branch, s.now(), 0); err != nil {
		return "", err
	}
	operation := options.Operation
	if operation.Kind != OperationSync || operation.BaseName != snapshot.Name || operation.RepoPath != snapshot.Path ||
		operation.Branch != snapshot.Branch || operation.ConfigFingerprint != snapshot.Fingerprint ||
		operation.RemoteFingerprint != snapshot.RemoteFingerprint || operation.CreatedAt.IsZero() || !validOperationID(operation.ID) {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Invalid Git sync operation"}
	}
	for _, oid := range []string{operation.LocalOID, operation.CandidateOID, operation.RemoteOID, operation.PushOID, options.LastRemoteOID} {
		if oid != "" && !validObjectID(oid) {
			return "", &SafeError{Code: CodeCommandFailed, Message: "Invalid Git sync operation"}
		}
	}
	path, err := canonicalDirectory(snapshot.Path)
	if err != nil {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Git repository inspection failed", cause: err}
	}
	if path != snapshot.Path || path != operation.RepoPath {
		return "", &SafeError{Code: CodeRepositoryRoot, Message: "Git repository root does not match the base directory"}
	}
	return path, nil
}

func (s *Service) preflightSync(ctx context.Context, path string, options SyncOptions) error {
	local, err := s.inspectSafeLocal(ctx, path)
	if err != nil {
		return err
	}
	if !local.HasRepository {
		return &SafeError{Code: CodeNeedsReconnect, Message: "Git base directory must be reconnected"}
	}
	if local.DetachedHead || local.CurrentBranch != options.Snapshot.Branch {
		return &SafeError{Code: CodeNeedsReconnect, Message: "Configured Git branch is not selected"}
	}
	if err := s.requireOrigin(ctx, path, options.Snapshot.URL); err != nil {
		return err
	}
	return s.requireSyncTrust(ctx, path, options.Snapshot.Branch, options.LastRemoteOID)
}

func (s *Service) fetchSyncCandidate(
	ctx context.Context,
	path string,
	options SyncOptions,
	trusted string,
	checkpoint *syncCheckpoint,
) (syncPass, error) {
	if err := s.requireSyncTrust(ctx, path, options.Snapshot.Branch, trusted); err != nil {
		return syncPass{}, err
	}
	_, err := s.selectedRemoteOID(ctx, path, options.Snapshot.URL, options.Snapshot.Branch)
	if err != nil {
		return syncPass{}, err
	}
	if err := checkpoint.save(ctx, StageFetching); err != nil {
		return syncPass{}, err
	}
	privateRef := "refs/igonotes/fetch/" + options.Operation.ID
	refspec := "+refs/heads/" + options.Snapshot.Branch + ":" + privateRef
	if _, err := s.connectRunNetwork(ctx, path, options.Snapshot.URL, false,
		"fetch", "--no-tags", "--show-forced-updates", "origin", refspec); err != nil {
		return syncPass{}, err
	}
	candidate, err := s.commitOID(ctx, path, privateRef)
	if err != nil {
		return syncPass{}, err
	}
	if trusted != "" {
		if err := s.requireAncestor(ctx, path, trusted, candidate); err != nil {
			return syncPass{}, err
		}
	}
	checkpoint.value.CandidateOID = candidate
	if err := checkpoint.save(ctx, StageFetching); err != nil {
		return syncPass{}, err
	}
	if err := s.advanceSyncTrust(ctx, path, options.Snapshot.Branch, trusted, candidate); err != nil {
		return syncPass{}, err
	}
	checkpoint.value.RemoteOID = candidate
	if err := checkpoint.save(ctx, StageFetching); err != nil {
		return syncPass{}, err
	}
	return syncPass{candidate: candidate}, nil
}

func (s *Service) advanceSyncTrust(ctx context.Context, path, branch, trusted, candidate string) error {
	managedRef := managedRemoteRef(branch)
	current, exists, err := s.optionalRefOID(ctx, path, managedRef)
	if err != nil {
		return err
	}
	expected := zeroObjectIDFor(candidate)
	if trusted != "" {
		expected = trusted
		if !exists || current != trusted {
			return &SafeError{Code: CodeNeedsReconnect, Message: "Git trusted remote state does not match"}
		}
	} else if exists {
		return &SafeError{Code: CodeNeedsReconnect, Message: "Git trusted remote state does not match"}
	}
	if current == candidate {
		return nil
	}
	if _, err := s.connectRunLocal(ctx, path, false, "update-ref", managedRef, candidate, expected); err != nil {
		actual, actualExists, inspectErr := s.optionalRefOID(ctx, path, managedRef)
		if inspectErr != nil {
			return inspectErr
		}
		if !actualExists || actual != candidate {
			return err
		}
	}
	return nil
}

func (s *Service) requireSyncTrust(ctx context.Context, path, branch, trusted string) error {
	managed, exists, err := s.optionalRefOID(ctx, path, managedRemoteRef(branch))
	if err != nil {
		return err
	}
	if trusted == "" {
		if !exists {
			return nil
		}
	} else if exists && managed == trusted {
		return nil
	}
	return &SafeError{Code: CodeNeedsReconnect, Message: "Git trusted remote state does not match"}
}

func (s *Service) runSyncWorktree(
	ctx context.Context,
	path string,
	options SyncOptions,
	worktree WorktreeTransaction,
	checkpoint *syncCheckpoint,
	pass *syncPass,
) error {
	err := runWorktree(ctx, worktree, func(callbackPath string) error {
		if err := requireCallbackPath(path, callbackPath); err != nil {
			return err
		}
		_, mergeExists, unmerged, inspectErr := s.inspectMergeState(ctx, path)
		if inspectErr != nil || mergeExists || len(unmerged) != 0 {
			checkpoint.value.ConflictPaths = unmerged
			conflictErr := newConflictError(unmerged)
			var stateErr error
			if inspectErr != nil || !mergeExists || len(unmerged) == 0 {
				stateErr = interruptedMergeState()
			}
			return errors.Join(conflictErr, stateErr, inspectErr, checkpoint.save(ctx, StageMerging))
		}
		local, err := s.inspectSafeLocal(ctx, path)
		if err != nil {
			return err
		}
		if !local.HasRepository || local.DetachedHead || local.CurrentBranch != options.Snapshot.Branch {
			return &SafeError{Code: CodeNeedsReconnect, Message: "Configured Git branch is not selected"}
		}
		if err := s.requireOrigin(ctx, path, options.Snapshot.URL); err != nil {
			return err
		}
		pass.preHead, _, err = s.optionalCommitOID(ctx, path, "HEAD")
		if err != nil {
			return err
		}
		if checkpoint.value.LocalOID == "" {
			checkpoint.value.LocalOID = pass.preHead
		}
		if err := checkpoint.save(ctx, StageSnapshotting); err != nil {
			return err
		}
		if _, err := s.connectRunLocal(ctx, path, false, "add", "--all", "--", "."); err != nil {
			return err
		}
		staged, err := s.stagedPaths(ctx, path)
		if err != nil {
			return err
		}
		if len(staged) != 0 {
			message, err := renderSyncCommitMessage(options.Snapshot.CommitTemplate, options.Snapshot.Name,
				options.Snapshot.Branch, s.now(), len(staged))
			if err != nil {
				return err
			}
			if err := checkpoint.save(ctx, StageSnapshotting); err != nil {
				return err
			}
			if _, err := s.connectRunLocal(ctx, path, false, "commit", "-m", message); err != nil {
				return err
			}
		}
		if err := s.mergeSyncCandidate(ctx, path, options.Snapshot.Branch, pass.candidate, checkpoint); err != nil {
			return err
		}
		pass.pushOID, err = s.commitOID(ctx, path, "HEAD")
		if err != nil {
			return err
		}
		pass.changedPaths, err = s.syncChangedPaths(ctx, path, pass.preHead, pass.pushOID)
		if err != nil {
			return err
		}
		checkpoint.value.PushOID = pass.pushOID
		checkpoint.value.ChangedPaths = sortedUnique(append(checkpoint.value.ChangedPaths, pass.changedPaths...))
		return checkpoint.save(ctx, StageReindexing)
	})
	if err == nil {
		return nil
	}
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		checkpoint.value.ConflictPaths = append([]string(nil), conflict.Paths...)
	}
	return err
}

func (s *Service) inspectMergeState(ctx context.Context, path string) (string, bool, []string, error) {
	gitDir, gitDirErr := s.syncGitDirectory(ctx, path)
	markerExists := false
	var markerErr error
	if gitDirErr == nil {
		markerExists, markerErr = mergeMarkerExists(gitDir)
	}
	mergeHead, mergeExists, mergeErr := s.optionalCommitOID(ctx, path, "MERGE_HEAD")
	paths, pathsErr := s.unmergedPaths(ctx, path)
	markerStillExists := markerExists
	var markerRecheckErr error
	if gitDirErr == nil {
		markerStillExists, markerRecheckErr = mergeMarkerExists(gitDir)
	}
	if gitDirErr == nil && markerErr == nil && markerRecheckErr == nil &&
		(markerExists != mergeExists || markerStillExists != mergeExists) {
		markerErr = malformedOutputError()
	}
	return mergeHead, mergeExists, paths, errors.Join(gitDirErr, markerErr, markerRecheckErr, mergeErr, pathsErr)
}

func mergeMarkerExists(gitDir string) (bool, error) {
	_, err := os.Lstat(filepath.Join(gitDir, "MERGE_HEAD"))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, &SafeError{Code: CodeCommandFailed, Message: "Git repository inspection failed", cause: err}
	}
}

func (s *Service) syncGitDirectory(ctx context.Context, path string) (string, error) {
	result, err := s.connectRunLocal(ctx, path, true, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	if result.StdoutTruncated || result.StderrTruncated || strings.Contains(result.Stdout, "\n\n") {
		return "", malformedOutputError()
	}
	gitDir := singleLine(result.Stdout)
	if gitDir == "" || !filepath.IsAbs(gitDir) {
		return "", malformedOutputError()
	}
	canonical, err := canonicalDirectory(gitDir)
	if err != nil {
		return "", &SafeError{Code: CodeCommandFailed, Message: "Git repository inspection failed", cause: err}
	}
	return canonical, nil
}

func interruptedMergeState() error {
	return &SafeError{Code: CodeOperationInterrupted, Message: "Git merge state is ambiguous or unfinished"}
}

func (s *Service) mergeSyncCandidate(
	ctx context.Context,
	path, branch, candidate string,
	checkpoint *syncCheckpoint,
) error {
	head, err := s.commitOID(ctx, path, "HEAD")
	if err != nil {
		return err
	}
	ancestor, err := s.isAncestor(ctx, path, candidate, head)
	if err != nil || ancestor {
		return err
	}
	unrelated, err := s.unrelated(ctx, path, head, candidate)
	if err != nil {
		return err
	}
	if unrelated {
		return &SafeError{Code: CodeOperationInterrupted, Message: "Git histories are unrelated"}
	}
	if err := checkpoint.save(ctx, StageMerging); err != nil {
		return err
	}
	_, mergeErr := s.connectRunLocal(ctx, path, false,
		"merge", "--no-edit", "-m", "IGoNotes: merge origin/"+branch, candidate)
	if mergeErr != nil {
		_, mergeExists, paths, inspectErr := s.inspectMergeState(ctx, path)
		checkpoint.value.ConflictPaths = paths
		if inspectErr != nil {
			return errors.Join(newConflictError(paths), interruptedMergeState(), inspectErr, checkpoint.save(ctx, StageMerging))
		}
		if mergeExists || len(paths) != 0 {
			conflictErr := newConflictError(paths)
			var stateErr error
			if !mergeExists || len(paths) == 0 {
				stateErr = interruptedMergeState()
			}
			if progressErr := checkpoint.save(ctx, StageMerging); progressErr != nil {
				return errors.Join(conflictErr, stateErr, progressErr)
			}
			return errors.Join(conflictErr, stateErr)
		}
		return mergeErr
	}
	return checkpoint.save(ctx, StageMerging)
}

func (s *Service) syncChangedPaths(ctx context.Context, path, before, after string) ([]string, error) {
	args := []string{"diff", "--name-only", "-z", before, after, "--"}
	if before == "" {
		args = []string{"diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "-z", after, "--"}
	}
	result, err := s.connectRunLocal(ctx, path, true, args...)
	if err != nil {
		return nil, err
	}
	return parseNULPaths(result)
}

func (s *Service) syncAheadBehind(ctx context.Context, path, candidate string) (int, int, error) {
	result, err := s.connectRunLocal(ctx, path, true,
		"rev-list", "--left-right", "--count", "HEAD..."+candidate)
	if err != nil {
		return 0, 0, err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return 0, 0, malformedOutputError()
	}
	fields := strings.Fields(result.Stdout)
	if len(fields) != 2 {
		return 0, 0, malformedOutputError()
	}
	if !decimalDigits(fields[0]) || !decimalDigits(fields[1]) {
		return 0, 0, malformedOutputError()
	}
	ahead, aheadErr := strconv.Atoi(fields[0])
	behind, behindErr := strconv.Atoi(fields[1])
	if aheadErr != nil || behindErr != nil || ahead < 0 || behind < 0 {
		return 0, 0, malformedOutputError()
	}
	return ahead, behind, nil
}

func (s *Service) prePushSyncCheck(
	ctx context.Context,
	path string,
	snapshot ConfiguredBase,
	candidate string,
	pushOID string,
) (string, error) {
	if err := requireConnectBase(ctx, path); err != nil {
		return "", err
	}
	local, err := s.inspectSafeLocal(ctx, path)
	if err != nil {
		return "", err
	}
	if !local.HasRepository || local.DetachedHead || local.CurrentBranch != snapshot.Branch {
		return "", &SafeError{Code: CodeNeedsReconnect, Message: "Configured Git branch is not selected"}
	}
	if err := s.requireOrigin(ctx, path, snapshot.URL); err != nil {
		return "", err
	}
	head, err := s.commitOID(ctx, path, "HEAD")
	if err != nil {
		return "", err
	}
	if head != pushOID {
		return "", &SafeError{Code: CodeOperationInterrupted, Message: "Git HEAD changed before push"}
	}
	if err := s.requireSyncTrust(ctx, path, snapshot.Branch, candidate); err != nil {
		return "", err
	}
	return s.selectedRemoteOID(ctx, path, snapshot.URL, snapshot.Branch)
}

func (s *Service) completeSyncPush(
	ctx context.Context,
	path, branch string,
	pass syncPass,
	checkpoint *syncCheckpoint,
) error {
	managed := managedRemoteRef(branch)
	if _, err := s.connectRunLocal(ctx, path, false, "update-ref", managed, pass.pushOID, pass.candidate); err != nil {
		current, exists, inspectErr := s.optionalRefOID(ctx, path, managed)
		if inspectErr != nil {
			return inspectErr
		}
		if !exists || current != pass.pushOID {
			return err
		}
	}
	checkpoint.value.RemoteOID = pass.pushOID
	checkpoint.value.PushOID = pass.pushOID
	return checkpoint.save(ctx, StageCompleted)
}

func syncOperationResult(pass syncPass, checkpoint Checkpoint, remoteOID string) OperationResult {
	return OperationResult{
		HeadOID:       pass.pushOID,
		RemoteOID:     remoteOID,
		PushOID:       pass.pushOID,
		ChangedPaths:  append([]string(nil), checkpoint.ChangedPaths...),
		ConflictPaths: append([]string(nil), checkpoint.ConflictPaths...),
		Ahead:         pass.ahead,
		Behind:        pass.behind,
	}
}

func renderSyncCommitMessage(template, base, branch string, now time.Time, count int) (string, error) {
	if template == "" {
		template = defaultSyncCommitTemplate
	}
	if strings.TrimSpace(template) == "" || utf8.RuneCountInString(template) > 200 || containsControlText(template) {
		return "", invalidSyncTemplate()
	}
	remainder := template
	for {
		start := strings.Index(remainder, "{{")
		if start < 0 {
			if strings.Contains(remainder, "}}") {
				return "", invalidSyncTemplate()
			}
			break
		}
		if strings.Contains(remainder[:start], "}}") {
			return "", invalidSyncTemplate()
		}
		end := strings.Index(remainder[start+2:], "}}")
		if end < 0 {
			return "", invalidSyncTemplate()
		}
		token := remainder[start+2 : start+2+end]
		switch token {
		case "base", "branch", "date", "datetime", "count":
		default:
			return "", invalidSyncTemplate()
		}
		remainder = remainder[start+2+end+2:]
	}
	return strings.NewReplacer(
		"{{base}}", base,
		"{{branch}}", branch,
		"{{date}}", now.Format("2006-01-02"),
		"{{datetime}}", now.Format(time.RFC3339),
		"{{count}}", strconv.Itoa(count),
	).Replace(template), nil
}

func containsControlText(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func invalidSyncTemplate() error {
	return &SafeError{Code: CodeCommandFailed, Message: "Invalid Git commit message template"}
}

func isPushRejected(err error) bool {
	var safeErr *SafeError
	return errors.As(err, &safeErr) && safeErr.Code == CodePushRejected
}

func pushRejected() error {
	return &SafeError{Code: CodePushRejected, Message: "Git push was rejected"}
}

func syncIndexLocked(gitDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(gitDir, "index.lock"))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}
