package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	gitcmd "IGoNotes/internal/git"
)

var ErrGitOperationTransition = errors.New("invalid Git operation transition")

const operationTimestampLayout = "2006-01-02T15:04:05.000000000Z"

const gitOperationColumns = `
	operation_id, base_name, repo_path, config_fingerprint, remote_fingerprint,
	kind, state, stage, branch, backup_ref, local_oid, candidate_oid, remote_oid,
	push_oid, changed_paths_json, conflict_paths_json, error_code, error_message,
	error_field, error_exit_code, created_at, updated_at`

type GitOperationRepository struct {
	db *sql.DB
}

func NewGitOperationRepository(db *sql.DB) *GitOperationRepository {
	return &GitOperationRepository{db: db}
}

func (r *GitOperationRepository) CreateQueued(ctx context.Context, operation gitcmd.Operation) error {
	changedPaths, err := encodeOperationPaths(operation.ChangedPaths)
	if err != nil {
		return fmt.Errorf("create queued Git operation %q: encode changed paths: %w", operation.ID, err)
	}
	conflictPaths, err := encodeOperationPaths(operation.ConflictPaths)
	if err != nil {
		return fmt.Errorf("create queued Git operation %q: encode conflict paths: %w", operation.ID, err)
	}
	errorCode, errorMessage, errorField, errorExitCode := operationErrorFields(operation.Error)
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO git_operations (`+gitOperationColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		operation.ID, operation.BaseName, operation.RepoPath, operation.ConfigFingerprint,
		operation.RemoteFingerprint, operation.Kind, gitcmd.OperationQueued, gitcmd.StageQueued,
		operation.Branch, operation.BackupRef, operation.LocalOID, operation.CandidateOID,
		operation.RemoteOID, operation.PushOID, changedPaths, conflictPaths, errorCode,
		errorMessage, errorField, errorExitCode, formatOperationTime(operation.CreatedAt),
		formatOperationTime(operation.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create queued Git operation %q: %w", operation.ID, err)
	}
	return nil
}

func (r *GitOperationRepository) Checkpoint(ctx context.Context, id string, checkpoint gitcmd.Checkpoint) error {
	changedPaths, err := encodeOperationPaths(checkpoint.ChangedPaths)
	if err != nil {
		return fmt.Errorf("checkpoint Git operation %q: encode changed paths: %w", id, err)
	}
	conflictPaths, err := encodeOperationPaths(checkpoint.ConflictPaths)
	if err != nil {
		return fmt.Errorf("checkpoint Git operation %q: encode conflict paths: %w", id, err)
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE git_operations SET
			state = ?, stage = ?, backup_ref = ?, local_oid = ?, candidate_oid = ?,
			remote_oid = ?, push_oid = ?, changed_paths_json = ?, conflict_paths_json = ?,
			updated_at = ?
		WHERE operation_id = ? AND state IN ('queued', 'running')
	`, gitcmd.OperationRunning, checkpoint.Stage, checkpoint.BackupRef, checkpoint.LocalOID,
		checkpoint.CandidateOID, checkpoint.RemoteOID, checkpoint.PushOID, changedPaths,
		conflictPaths, formatOperationTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("checkpoint Git operation %q: %w", id, err)
	}
	return requireGitOperationTransition(result, "checkpoint", id)
}

func (r *GitOperationRepository) Finish(ctx context.Context, operation gitcmd.Operation) error {
	if operation.State != gitcmd.OperationSucceeded && operation.State != gitcmd.OperationFailed && operation.State != gitcmd.OperationConflict {
		return fmt.Errorf("finish Git operation %q: %w", operation.ID, ErrGitOperationTransition)
	}
	changedPaths := ""
	changedPathsSpecified := operation.ChangedPaths != nil
	if changedPathsSpecified {
		var err error
		changedPaths, err = encodeOperationPaths(operation.ChangedPaths)
		if err != nil {
			return fmt.Errorf("finish Git operation %q: encode changed paths: %w", operation.ID, err)
		}
	}
	conflictPaths := ""
	conflictPathsSpecified := operation.ConflictPaths != nil
	if conflictPathsSpecified {
		var err error
		conflictPaths, err = encodeOperationPaths(operation.ConflictPaths)
		if err != nil {
			return fmt.Errorf("finish Git operation %q: encode conflict paths: %w", operation.ID, err)
		}
	}
	errorCode, errorMessage, errorField, errorExitCode := operationErrorFields(operation.Error)
	result, err := r.db.ExecContext(ctx, `
		UPDATE git_operations SET
			state = ?, stage = ?,
			backup_ref = CASE WHEN backup_ref = '' THEN ? ELSE backup_ref END,
			local_oid = CASE WHEN local_oid = '' THEN ? ELSE local_oid END,
			candidate_oid = CASE WHEN candidate_oid = '' THEN ? ELSE candidate_oid END,
			remote_oid = CASE WHEN remote_oid = '' THEN ? ELSE remote_oid END,
			push_oid = CASE WHEN push_oid = '' THEN ? ELSE push_oid END,
			changed_paths_json = CASE WHEN ? THEN ? ELSE changed_paths_json END,
			conflict_paths_json = CASE WHEN ? THEN ? ELSE conflict_paths_json END,
			error_code = ?, error_message = ?, error_field = ?, error_exit_code = ?, updated_at = ?
		WHERE operation_id = ? AND state IN ('queued', 'running')
	`, operation.State, operation.Stage, operation.BackupRef, operation.LocalOID,
		operation.CandidateOID, operation.RemoteOID, operation.PushOID,
		changedPathsSpecified, changedPaths, conflictPathsSpecified, conflictPaths,
		errorCode, errorMessage, errorField, errorExitCode,
		formatOperationTime(operation.UpdatedAt), operation.ID)
	if err != nil {
		return fmt.Errorf("finish Git operation %q: %w", operation.ID, err)
	}
	return requireGitOperationTransition(result, "finish", operation.ID)
}

func (r *GitOperationRepository) ActiveByPath(ctx context.Context, path string) (gitcmd.Operation, bool, error) {
	operation, err := scanGitOperation(r.db.QueryRowContext(ctx, `
		SELECT `+gitOperationColumns+`
		FROM git_operations
		WHERE repo_path = ? AND state IN ('queued', 'running')
		ORDER BY updated_at DESC, rowid DESC
		LIMIT 1
	`, path))
	return operationLookupResult(operation, err, fmt.Sprintf("find active Git operation for path %q", path))
}

func (r *GitOperationRepository) LatestByPath(ctx context.Context, path string) (gitcmd.Operation, bool, error) {
	operation, err := scanGitOperation(r.db.QueryRowContext(ctx, `
		SELECT `+gitOperationColumns+`
		FROM git_operations
		WHERE repo_path = ?
		ORDER BY updated_at DESC, rowid DESC
		LIMIT 1
	`, path))
	return operationLookupResult(operation, err, fmt.Sprintf("find latest Git operation for path %q", path))
}

func (r *GitOperationRepository) ListUnfinished(ctx context.Context) ([]gitcmd.Operation, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+gitOperationColumns+`
		FROM git_operations
		WHERE state IN ('queued', 'running')
		ORDER BY created_at ASC, rowid ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list unfinished Git operations: %w", err)
	}
	defer rows.Close()

	operations := make([]gitcmd.Operation, 0)
	for rows.Next() {
		operation, err := scanGitOperation(rows)
		if err != nil {
			return nil, fmt.Errorf("list unfinished Git operations: %w", err)
		}
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unfinished Git operations: %w", err)
	}
	return operations, nil
}

func requireGitOperationTransition(result sql.Result, action, id string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s Git operation %q: inspect transition: %w", action, id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%s Git operation %q: %w", action, id, ErrGitOperationTransition)
	}
	return nil
}

func operationLookupResult(operation gitcmd.Operation, err error, action string) (gitcmd.Operation, bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return gitcmd.Operation{}, false, nil
	}
	if err != nil {
		return gitcmd.Operation{}, false, fmt.Errorf("%s: %w", action, err)
	}
	return operation, true, nil
}

func scanGitOperation(scanner rowScanner) (gitcmd.Operation, error) {
	var operation gitcmd.Operation
	var kind, state, stage string
	var changedPathsJSON, conflictPathsJSON string
	var errorCode, errorMessage, errorField string
	var errorExitCode int
	var createdAt, updatedAt string
	if err := scanner.Scan(
		&operation.ID, &operation.BaseName, &operation.RepoPath, &operation.ConfigFingerprint,
		&operation.RemoteFingerprint, &kind, &state, &stage, &operation.Branch,
		&operation.BackupRef, &operation.LocalOID, &operation.CandidateOID, &operation.RemoteOID,
		&operation.PushOID, &changedPathsJSON, &conflictPathsJSON, &errorCode, &errorMessage,
		&errorField, &errorExitCode, &createdAt, &updatedAt,
	); err != nil {
		return gitcmd.Operation{}, err
	}
	operation.Kind = gitcmd.OperationKind(kind)
	operation.State = gitcmd.OperationState(state)
	operation.Stage = gitcmd.Stage(stage)
	if err := json.Unmarshal([]byte(changedPathsJSON), &operation.ChangedPaths); err != nil {
		return gitcmd.Operation{}, fmt.Errorf("decode changed paths: %w", err)
	}
	if operation.ChangedPaths == nil {
		operation.ChangedPaths = []string{}
	}
	if err := json.Unmarshal([]byte(conflictPathsJSON), &operation.ConflictPaths); err != nil {
		return gitcmd.Operation{}, fmt.Errorf("decode conflict paths: %w", err)
	}
	if operation.ConflictPaths == nil {
		operation.ConflictPaths = []string{}
	}
	if errorCode != "" || errorMessage != "" || errorField != "" || errorExitCode != 0 {
		operation.Error = &gitcmd.SafeError{
			Code: gitcmd.ErrorCode(errorCode), Message: errorMessage, Field: errorField, ExitCode: errorExitCode,
		}
	}
	var err error
	operation.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return gitcmd.Operation{}, fmt.Errorf("parse creation time: %w", err)
	}
	operation.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return gitcmd.Operation{}, fmt.Errorf("parse update time: %w", err)
	}
	return operation, nil
}

func encodeOperationPaths(paths []string) (string, error) {
	normalized := append([]string(nil), paths...)
	sort.Strings(normalized)
	unique := normalized[:0]
	for _, path := range normalized {
		if len(unique) == 0 || unique[len(unique)-1] != path {
			unique = append(unique, path)
		}
	}
	if unique == nil {
		unique = []string{}
	}
	encoded, err := json.Marshal(unique)
	return string(encoded), err
}

func operationErrorFields(operationError *gitcmd.SafeError) (gitcmd.ErrorCode, string, string, int) {
	if operationError == nil {
		return "", "", "", 0
	}
	return operationError.Code, operationError.Message, operationError.Field, operationError.ExitCode
}

func formatOperationTime(value time.Time) string {
	return value.UTC().Format(operationTimestampLayout)
}
