package service

import (
	"context"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/repository"
)

type GitFailureAction string

const (
	GitFailurePreserve GitFailureAction = "preserve"
	GitFailureConsume  GitFailureAction = "consume"
	GitFailureReset    GitFailureAction = "reset"
)

type GitFailureClass string

const (
	GitFailureOperational   GitFailureClass = "operational"
	GitFailureConflict      GitFailureClass = "conflict"
	GitFailureConfiguration GitFailureClass = "configuration"
	GitFailureValidation    GitFailureClass = "validation"
	GitFailureSafety        GitFailureClass = "safety"
	GitFailureShutdown      GitFailureClass = "shutdown"
)

// GitTerminalOutcome is the stable input to autosync breaker accounting.
type GitTerminalOutcome struct {
	Operation gitcmd.OperationKind
	State     gitcmd.OperationState
	Failure   GitFailureClass
}

func ClassifyGitOutcome(outcome GitTerminalOutcome) GitFailureAction {
	if outcome.State == gitcmd.OperationSucceeded {
		switch outcome.Operation {
		case gitcmd.OperationInitialize, gitcmd.OperationSync, gitcmd.OperationConflictComplete:
			return GitFailureReset
		}
	}
	if outcome.State == gitcmd.OperationFailed && outcome.Failure == GitFailureOperational {
		switch outcome.Operation {
		case gitcmd.OperationSync, gitcmd.OperationConflictComplete:
			return GitFailureConsume
		}
	}
	return GitFailurePreserve
}

func (a GitFailureAction) StatusFailureTransition() repository.GitStatusFailureTransition {
	switch a {
	case GitFailureConsume:
		return repository.GitStatusFailuresIncrement
	case GitFailureReset:
		return repository.GitStatusFailuresReset
	default:
		return repository.GitStatusFailuresPreserve
	}
}

type GitSyncSchedule struct {
	RepositoryPath string
	Delay          time.Duration
}

type GitResilienceScheduler interface {
	Schedule(context.Context, GitSyncSchedule) error
	Cancel(repositoryPath string)
}

var _ repository.GitStatusTransitioner = (*repository.GitStatusRepository)(nil)
