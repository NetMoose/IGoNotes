package service

import (
	"context"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

type GitFailureAction string

const (
	GitFailurePreserve GitFailureAction = "preserve"
	GitFailureConsume  GitFailureAction = "consume"
	GitFailureReset    GitFailureAction = "reset"
)

// GitTerminalOutcome is the stable input to autosync breaker accounting.
type GitTerminalOutcome struct {
	Operation gitcmd.OperationKind
	State     gitcmd.OperationState
	ErrorCode gitcmd.ErrorCode
}

type GitOutcomeClassification struct {
	State    model.GitState
	Failures GitFailureAction
}

func ClassifyGitOutcome(outcome GitTerminalOutcome) GitOutcomeClassification {
	if outcome.State == gitcmd.OperationSucceeded {
		switch outcome.Operation {
		case gitcmd.OperationInitialize, gitcmd.OperationSync, gitcmd.OperationConflictComplete:
			return GitOutcomeClassification{State: model.GitStateReady, Failures: GitFailureReset}
		case gitcmd.OperationConflictAbort:
			return GitOutcomeClassification{State: model.GitStatePaused, Failures: GitFailurePreserve}
		}
	}
	if outcome.State == gitcmd.OperationConflict || outcome.ErrorCode == gitcmd.CodeGitConflict {
		return GitOutcomeClassification{State: model.GitStateConflict, Failures: GitFailurePreserve}
	}
	switch outcome.ErrorCode {
	case gitcmd.CodeNeedsReconnect, gitcmd.CodeBranchDeleted, gitcmd.CodeRemoteHistoryRewritten:
		return GitOutcomeClassification{State: model.GitStateNeedsReconnect, Failures: GitFailurePreserve}
	}
	if outcome.State == gitcmd.OperationFailed && gitOperationalFailure(outcome.ErrorCode) {
		switch outcome.Operation {
		case gitcmd.OperationSync, gitcmd.OperationConflictComplete:
			return GitOutcomeClassification{State: model.GitStateError, Failures: GitFailureConsume}
		}
	}
	return GitOutcomeClassification{State: model.GitStateError, Failures: GitFailurePreserve}
}

func gitOperationalFailure(code gitcmd.ErrorCode) bool {
	switch code {
	case gitcmd.CodeGitConflict,
		gitcmd.CodeConflictNotFound,
		gitcmd.CodeConflictStale,
		gitcmd.CodeConflictUnresolved,
		gitcmd.CodeConflictUnsupported,
		gitcmd.CodeMergeNotInProgress,
		gitcmd.CodeRecoveryRequired,
		gitcmd.CodeOperationInterrupted,
		gitcmd.CodeRepositoryRoot,
		gitcmd.CodeBackupMismatch,
		gitcmd.CodeNeedsReconnect,
		gitcmd.CodeBranchDeleted,
		gitcmd.CodeRemoteHistoryRewritten,
		gitcmd.CodeIdentityMissing,
		gitcmd.CodeOriginMismatch,
		gitcmd.CodeNotRepository,
		gitcmd.CodeInvalidBranch,
		gitcmd.CodeConfirmationRequired,
		gitcmd.CodeCanceled,
		gitcmd.CodePaused,
		gitcmd.CodeNotPaused:
		return false
	default:
		return true
	}
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

type GitResilienceClock interface {
	Now() time.Time
	NewTimer(time.Duration) GitResilienceTimer
}

type GitResilienceTimer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(time.Duration) bool
}

type GitOrderedSnapshots interface {
	OrderedGitSnapshots() []gitcmd.ConfiguredBase
}

type GitSyncQueue interface {
	QueueSync(context.Context, gitcmd.SyncRequest) (gitcmd.Operation, bool, error)
}

var _ repository.GitStatusTransitioner = (*repository.GitStatusRepository)(nil)
