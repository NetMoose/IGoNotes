package repository

import (
	"context"
	"errors"

	"IGoNotes/internal/model"
)

type GitStatusFailureTransition string

const (
	GitStatusFailuresPreserve  GitStatusFailureTransition = "preserve"
	GitStatusFailuresIncrement GitStatusFailureTransition = "increment"
	GitStatusFailuresReset     GitStatusFailureTransition = "reset"
)

// GitStatusTransition updates state and the failure budget together when the current state matches.
type GitStatusTransition struct {
	RepositoryPath string
	FromState      model.GitState
	ToState        model.GitState
	Failures       GitStatusFailureTransition
}

type GitStatusTransitioner interface {
	Transition(context.Context, GitStatusTransition) (bool, error)
}

func (r *GitStatusRepository) Transition(ctx context.Context, transition GitStatusTransition) (bool, error) {
	if transition.RepositoryPath == "" || transition.FromState == "" || transition.ToState == "" {
		return false, errors.New("Git status transition requires repository path and states")
	}
	if transition.Failures == "" {
		transition.Failures = GitStatusFailuresPreserve
	}
	if transition.Failures != GitStatusFailuresPreserve && transition.Failures != GitStatusFailuresIncrement && transition.Failures != GitStatusFailuresReset {
		return false, errors.New("invalid Git status failure transition")
	}

	result, err := r.db.ExecContext(ctx, `
		UPDATE git_status
		SET state = ?,
			consecutive_failures = CASE ?
				WHEN 'increment' THEN consecutive_failures + 1
				WHEN 'reset' THEN 0
				ELSE consecutive_failures
			END,
			updated_at_unix_ms = ?
		WHERE repository_path = ? AND state = ?
	`, transition.ToState, transition.Failures, r.now().UnixMilli(), transition.RepositoryPath, transition.FromState)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}
