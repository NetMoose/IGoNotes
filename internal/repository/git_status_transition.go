package repository

import (
	"context"
	"errors"

	"IGoNotes/internal/model"
)

const GitStatusPauseFailureThreshold = 5

const (
	GitFailurePreserve GitFailureAction = iota
	GitFailureReset
	GitFailureIncrement
)

type GitFailureAction uint8

// GitStatusTransition applies a complete status snapshot and derives its failure budget atomically.
type GitStatusTransition struct {
	Status   model.GitStatus
	Failures GitFailureAction
}

type GitStatusTransitioner interface {
	ApplyTransition(context.Context, GitStatusTransition) error
}

func (r *GitStatusRepository) ApplyTransition(ctx context.Context, transition GitStatusTransition) error {
	if transition.Status.RepositoryPath == "" || transition.Status.State == "" {
		return errors.New("Git status transition requires repository path and state")
	}
	if transition.Failures != GitFailurePreserve && transition.Failures != GitFailureReset && transition.Failures != GitFailureIncrement {
		return errors.New("invalid Git status failure action")
	}

	encoded, err := encodeGitStatus(transition.Status)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		UPDATE git_status
		SET base_name = ?,
			state = CASE
				WHEN ? = ? AND consecutive_failures + 1 >= ? THEN 'paused'
				ELSE ?
			END,
			operation_id = ?,
			stage = ?,
			ahead = ?,
			behind = ?,
			consecutive_failures = CASE ?
				WHEN ? THEN consecutive_failures + 1
				WHEN ? THEN 0
				ELSE consecutive_failures
			END,
			last_attempt_unix_ms = ?,
			last_success_unix_ms = ?,
			changed_paths_json = ?,
			remote_oid = ?,
			error_code = ?,
			error_message = ?,
			error_field = ?,
			updated_at_unix_ms = ?
		WHERE repository_path = ?
	`,
		transition.Status.Base,
		transition.Failures, GitFailureIncrement, GitStatusPauseFailureThreshold, transition.Status.State,
		transition.Status.OperationID,
		transition.Status.Stage,
		transition.Status.Ahead,
		transition.Status.Behind,
		transition.Failures, GitFailureIncrement, GitFailureReset,
		unixMilliseconds(transition.Status.LastAttempt),
		unixMilliseconds(transition.Status.LastSuccess),
		encoded.changedPathsJSON,
		transition.Status.RemoteOID,
		encoded.errorCode,
		encoded.errorMessage,
		encoded.errorField,
		r.now().UnixMilli(),
		transition.Status.RepositoryPath,
	)
	return err
}
