package service

import (
	"context"
	"testing"
	"time"

	gitcmd "IGoNotes/internal/git"
)

func TestGitOutcomeClassification(t *testing.T) {
	tests := []struct {
		name    string
		outcome GitTerminalOutcome
		want    GitFailureAction
	}{
		{"initialize success resets", GitTerminalOutcome{Operation: gitcmd.OperationInitialize, State: gitcmd.OperationSucceeded}, GitFailureReset},
		{"sync success resets", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationSucceeded}, GitFailureReset},
		{"conflict completion success resets", GitTerminalOutcome{Operation: gitcmd.OperationConflictComplete, State: gitcmd.OperationSucceeded}, GitFailureReset},
		{"sync operational failure consumes", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, Failure: GitFailureOperational}, GitFailureConsume},
		{"conflict completion operational failure consumes", GitTerminalOutcome{Operation: gitcmd.OperationConflictComplete, State: gitcmd.OperationFailed, Failure: GitFailureOperational}, GitFailureConsume},
		{"initialize failure preserves", GitTerminalOutcome{Operation: gitcmd.OperationInitialize, State: gitcmd.OperationFailed, Failure: GitFailureOperational}, GitFailurePreserve},
		{"sync conflict preserves", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationConflict, Failure: GitFailureConflict}, GitFailurePreserve},
		{"conflict completion conflict preserves", GitTerminalOutcome{Operation: gitcmd.OperationConflictComplete, State: gitcmd.OperationConflict, Failure: GitFailureConflict}, GitFailurePreserve},
		{"abort success preserves", GitTerminalOutcome{Operation: gitcmd.OperationConflictAbort, State: gitcmd.OperationSucceeded}, GitFailurePreserve},
		{"abort failure preserves", GitTerminalOutcome{Operation: gitcmd.OperationConflictAbort, State: gitcmd.OperationFailed, Failure: GitFailureOperational}, GitFailurePreserve},
		{"configuration failure preserves", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, Failure: GitFailureConfiguration}, GitFailurePreserve},
		{"validation failure preserves", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, Failure: GitFailureValidation}, GitFailurePreserve},
		{"safety failure preserves", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, Failure: GitFailureSafety}, GitFailurePreserve},
		{"shutdown preserves", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, Failure: GitFailureShutdown}, GitFailurePreserve},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyGitOutcome(test.outcome); got != test.want {
				t.Errorf("ClassifyGitOutcome(%+v) = %q, want %q", test.outcome, got, test.want)
			}
		})
	}
}

func TestGitResilienceContracts(t *testing.T) {
	if gitcmd.ErrGitNotPaused.Code != gitcmd.CodeNotPaused || gitcmd.ErrGitNotPaused.Message != "Git synchronization is not paused" || gitcmd.ErrGitNotPaused.Field != "" {
		t.Fatalf("ErrGitNotPaused = %#v, want immutable safe error %q", gitcmd.ErrGitNotPaused, gitcmd.CodeNotPaused)
	}

	scheduler := &fakeGitResilienceScheduler{}
	var contract GitResilienceScheduler = scheduler
	want := GitSyncSchedule{RepositoryPath: "/notes/work", Delay: time.Minute}
	if err := contract.Schedule(context.Background(), want); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	contract.Cancel(want.RepositoryPath)
	if len(scheduler.scheduled) != 1 || scheduler.scheduled[0] != want {
		t.Errorf("scheduled = %#v, want %#v", scheduler.scheduled, want)
	}
	if len(scheduler.canceled) != 1 || scheduler.canceled[0] != want.RepositoryPath {
		t.Errorf("canceled = %#v, want %q", scheduler.canceled, want.RepositoryPath)
	}
}

type fakeGitResilienceScheduler struct {
	scheduled []GitSyncSchedule
	canceled  []string
}

func (s *fakeGitResilienceScheduler) Schedule(_ context.Context, request GitSyncSchedule) error {
	s.scheduled = append(s.scheduled, request)
	return nil
}

func (s *fakeGitResilienceScheduler) Cancel(repositoryPath string) {
	s.canceled = append(s.canceled, repositoryPath)
}
