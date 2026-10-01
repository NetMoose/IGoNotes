package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"IGoNotes/internal/model"
)

type GitStatusRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewGitStatusRepository(db *sql.DB) *GitStatusRepository {
	return &GitStatusRepository{db: db, now: time.Now}
}

func (r *GitStatusRepository) Upsert(ctx context.Context, status model.GitStatus) error {
	encoded, err := encodeGitStatus(status)
	if err != nil {
		return err
	}

	_, err = r.db.ExecContext(ctx, `
		INSERT INTO git_status (
			repository_path, base_name, state, operation_id, stage,
			ahead, behind, consecutive_failures,
			last_attempt_unix_ms, last_success_unix_ms, changed_paths_json,
			remote_oid, error_code, error_message, error_field, updated_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repository_path) DO UPDATE SET
			base_name = excluded.base_name,
			state = excluded.state,
			operation_id = excluded.operation_id,
			stage = excluded.stage,
			ahead = excluded.ahead,
			behind = excluded.behind,
			consecutive_failures = excluded.consecutive_failures,
			last_attempt_unix_ms = excluded.last_attempt_unix_ms,
			last_success_unix_ms = excluded.last_success_unix_ms,
			changed_paths_json = excluded.changed_paths_json,
			remote_oid = excluded.remote_oid,
			error_code = excluded.error_code,
			error_message = excluded.error_message,
			error_field = excluded.error_field,
			updated_at_unix_ms = excluded.updated_at_unix_ms
	`,
		status.RepositoryPath,
		status.Base,
		status.State,
		status.OperationID,
		status.Stage,
		status.Ahead,
		status.Behind,
		status.ConsecutiveFailures,
		unixMilliseconds(status.LastAttempt),
		unixMilliseconds(status.LastSuccess),
		encoded.changedPathsJSON,
		status.RemoteOID,
		encoded.errorCode,
		encoded.errorMessage,
		encoded.errorField,
		r.now().UnixMilli(),
	)
	return err
}

type encodedGitStatus struct {
	changedPathsJSON string
	errorCode        string
	errorMessage     string
	errorField       string
}

func encodeGitStatus(status model.GitStatus) (encodedGitStatus, error) {
	changedPaths := status.ChangedPaths
	if changedPaths == nil {
		changedPaths = []string{}
	}
	changedPathsJSON, err := json.Marshal(changedPaths)
	if err != nil {
		return encodedGitStatus{}, err
	}
	encoded := encodedGitStatus{changedPathsJSON: string(changedPathsJSON)}
	if status.Error != nil {
		encoded.errorCode = status.Error.Code
		encoded.errorMessage = status.Error.Message
		encoded.errorField = status.Error.Field
	}
	return encoded, nil
}

func (r *GitStatusRepository) Get(ctx context.Context, repositoryPath string) (model.GitStatus, bool, error) {
	status, err := scanGitStatus(r.db.QueryRowContext(ctx, `
		SELECT repository_path, base_name, state, operation_id, stage,
			ahead, behind, consecutive_failures,
			last_attempt_unix_ms, last_success_unix_ms, changed_paths_json,
			remote_oid, error_code, error_message, error_field
		FROM git_status
		WHERE repository_path = ?
	`, repositoryPath))
	if errors.Is(err, sql.ErrNoRows) {
		return model.GitStatus{}, false, nil
	}
	if err != nil {
		return model.GitStatus{}, false, err
	}
	return status, true, nil
}

func (r *GitStatusRepository) List(ctx context.Context) ([]model.GitStatus, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT repository_path, base_name, state, operation_id, stage,
			ahead, behind, consecutive_failures,
			last_attempt_unix_ms, last_success_unix_ms, changed_paths_json,
			remote_oid, error_code, error_message, error_field
		FROM git_status
		ORDER BY base_name, repository_path
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	statuses := make([]model.GitStatus, 0)
	for rows.Next() {
		status, err := scanGitStatus(rows)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return statuses, nil
}

func (r *GitStatusRepository) Delete(ctx context.Context, repositoryPath string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM git_status WHERE repository_path = ?", repositoryPath)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanGitStatus(scanner rowScanner) (model.GitStatus, error) {
	var status model.GitStatus
	var state string
	var lastAttempt, lastSuccess sql.NullInt64
	var changedPathsJSON string
	var errorCode, errorMessage, errorField string
	if err := scanner.Scan(
		&status.RepositoryPath,
		&status.Base,
		&state,
		&status.OperationID,
		&status.Stage,
		&status.Ahead,
		&status.Behind,
		&status.ConsecutiveFailures,
		&lastAttempt,
		&lastSuccess,
		&changedPathsJSON,
		&status.RemoteOID,
		&errorCode,
		&errorMessage,
		&errorField,
	); err != nil {
		return model.GitStatus{}, err
	}

	status.State = model.GitState(state)
	status.LastAttempt = timeFromUnixMilliseconds(lastAttempt)
	status.LastSuccess = timeFromUnixMilliseconds(lastSuccess)
	if err := json.Unmarshal([]byte(changedPathsJSON), &status.ChangedPaths); err != nil {
		return model.GitStatus{}, err
	}
	if status.ChangedPaths == nil {
		status.ChangedPaths = []string{}
	}
	if errorCode != "" || errorMessage != "" || errorField != "" {
		status.Error = &model.APIError{Code: errorCode, Message: errorMessage, Field: errorField}
	}
	return status, nil
}

func unixMilliseconds(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixMilli()
}

func timeFromUnixMilliseconds(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := time.UnixMilli(value.Int64).UTC()
	return &result
}
