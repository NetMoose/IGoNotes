package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

func TestGitOutcomeClassification(t *testing.T) {
	tests := []struct {
		name    string
		outcome GitTerminalOutcome
		want    GitOutcomeClassification
	}{
		{"initialize success resets", GitTerminalOutcome{Operation: gitcmd.OperationInitialize, State: gitcmd.OperationSucceeded}, GitOutcomeClassification{State: model.GitStateReady, Failures: GitFailureReset}},
		{"sync success resets", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationSucceeded}, GitOutcomeClassification{State: model.GitStateReady, Failures: GitFailureReset}},
		{"conflict completion success resets", GitTerminalOutcome{Operation: gitcmd.OperationConflictComplete, State: gitcmd.OperationSucceeded}, GitOutcomeClassification{State: model.GitStateReady, Failures: GitFailureReset}},
		{"sync operational failure increments", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, ErrorCode: gitcmd.CodeCommandFailed}, GitOutcomeClassification{State: model.GitStateError, Failures: GitFailureIncrement}},
		{"conflict completion operational failure increments", GitTerminalOutcome{Operation: gitcmd.OperationConflictComplete, State: gitcmd.OperationFailed, ErrorCode: gitcmd.CodeTimedOut}, GitOutcomeClassification{State: model.GitStateError, Failures: GitFailureIncrement}},
		{"initialize failure preserves", GitTerminalOutcome{Operation: gitcmd.OperationInitialize, State: gitcmd.OperationFailed, ErrorCode: gitcmd.CodeCommandFailed}, GitOutcomeClassification{State: model.GitStateError, Failures: GitFailurePreserve}},
		{"conflict preserves", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationConflict, ErrorCode: gitcmd.CodeGitConflict}, GitOutcomeClassification{State: model.GitStateConflict, Failures: GitFailurePreserve}},
		{"abort success preserves", GitTerminalOutcome{Operation: gitcmd.OperationConflictAbort, State: gitcmd.OperationSucceeded}, GitOutcomeClassification{State: model.GitStatePaused, Failures: GitFailurePreserve}},
		{"reconnect preserves", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, ErrorCode: gitcmd.CodeNeedsReconnect}, GitOutcomeClassification{State: model.GitStateNeedsReconnect, Failures: GitFailurePreserve}},
		{"branch deletion pauses safely", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, ErrorCode: gitcmd.CodeBranchDeleted}, GitOutcomeClassification{State: model.GitStatePaused, Failures: GitFailurePreserve}},
		{"rewritten history pauses safely", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, ErrorCode: gitcmd.CodeRemoteHistoryRewritten}, GitOutcomeClassification{State: model.GitStatePaused, Failures: GitFailurePreserve}},
		{"failed abort preserves conflict", GitTerminalOutcome{Operation: gitcmd.OperationConflictAbort, State: gitcmd.OperationFailed, ExistingState: model.GitStateConflict, ErrorCode: gitcmd.CodeCommandFailed}, GitOutcomeClassification{State: model.GitStateConflict, Failures: GitFailurePreserve}},
		{"failed abort preserves recovery", GitTerminalOutcome{Operation: gitcmd.OperationConflictAbort, State: gitcmd.OperationFailed, ExistingState: model.GitStateNeedsReconnect, ErrorCode: gitcmd.CodeCommandFailed}, GitOutcomeClassification{State: model.GitStateNeedsReconnect, Failures: GitFailurePreserve}},
		{"configuration preserves", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, ErrorCode: gitcmd.CodeIdentityMissing}, GitOutcomeClassification{State: model.GitStateError, Failures: GitFailurePreserve}},
		{"validation preserves", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, ErrorCode: gitcmd.CodeInvalidBranch}, GitOutcomeClassification{State: model.GitStateError, Failures: GitFailurePreserve}},
		{"safety preserves", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, ErrorCode: gitcmd.CodeOperationInterrupted}, GitOutcomeClassification{State: model.GitStateError, Failures: GitFailurePreserve}},
		{"shutdown preserves", GitTerminalOutcome{Operation: gitcmd.OperationSync, State: gitcmd.OperationFailed, ErrorCode: gitcmd.CodeCanceled}, GitOutcomeClassification{State: model.GitStateError, Failures: GitFailurePreserve}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyGitOutcome(test.outcome); got != test.want {
				t.Errorf("ClassifyGitOutcome(%+v) = %q, want %q", test.outcome, got, test.want)
			}
		})
	}
}

func TestGitResilienceFifthOperationalFailurePausesAtomically(t *testing.T) {
	db, err := repository.InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := repository.NewGitStatusRepository(db)
	status := model.GitStatus{RepositoryPath: "/notes/work", Base: "work", State: model.GitStateError, ConsecutiveFailures: 4}
	if err := repo.Upsert(context.Background(), status); err != nil {
		t.Fatal(err)
	}

	if err := repo.ApplyTransition(context.Background(), repository.GitStatusTransition{
		Status: model.GitStatus{
			Base:           status.Base,
			RepositoryPath: status.RepositoryPath,
			State:          model.GitStateError,
			ChangedPaths:   []string{},
		},
		Failures: repository.GitFailureIncrement,
	}); err != nil {
		t.Fatalf("ApplyTransition() error = %v", err)
	}
	got, found, err := repo.Get(context.Background(), status.RepositoryPath)
	if err != nil || !found {
		t.Fatalf("Get() = %#v, %v, %v", got, found, err)
	}
	if got.State != model.GitStatePaused || got.ConsecutiveFailures != 5 {
		t.Errorf("transitioned status = %#v, want paused at five failures", got)
	}
}

func TestGitResilienceContractsAreFakeable(t *testing.T) {
	clock := &fakeGitResilienceClock{now: time.Date(2026, time.September, 11, 12, 0, 0, 0, time.UTC)}
	var clockContract GitResilienceClock = clock
	if got := clockContract.Now(); !got.Equal(clock.now) {
		t.Errorf("Now() = %v, want %v", got, clock.now)
	}
	if timer := clockContract.NewTimer(time.Minute); timer != clock.timer {
		t.Errorf("NewTimer() = %T, want fake timer", timer)
	}
	var _ GitStatusReader = &fakeGitResilienceStatusReader{}
	var _ GitOrderedSnapshots = fakeGitResilienceSnapshots{}
	var _ GitSyncQueue = fakeGitResilienceQueue{}
}

type fakeGitResilienceClock struct {
	now   time.Time
	timer *fakeGitResilienceTimer
}

func (c *fakeGitResilienceClock) Now() time.Time { return c.now }

func (c *fakeGitResilienceClock) NewTimer(time.Duration) GitResilienceTimer {
	if c.timer == nil {
		c.timer = &fakeGitResilienceTimer{events: make(chan time.Time)}
	}
	return c.timer
}

type fakeGitResilienceTimer struct{ events chan time.Time }

func (t *fakeGitResilienceTimer) C() <-chan time.Time { return t.events }
func (t *fakeGitResilienceTimer) Stop() bool          { return true }
func (t *fakeGitResilienceTimer) Reset(time.Duration) bool {
	return true
}

type fakeGitResilienceStatusReader struct{}

func (*fakeGitResilienceStatusReader) Get(context.Context, string) (model.GitStatus, bool, error) {
	return model.GitStatus{}, false, nil
}

func (*fakeGitResilienceStatusReader) List(context.Context) ([]model.GitStatus, error) {
	return nil, nil
}

type fakeGitResilienceSnapshots []gitcmd.ConfiguredBase

func (s fakeGitResilienceSnapshots) OrderedGitSnapshots() ([]gitcmd.ConfiguredBase, error) {
	return s, nil
}

type fakeGitResilienceQueue struct{}

func (fakeGitResilienceQueue) QueueSync(context.Context, gitcmd.SyncRequest) (gitcmd.Operation, bool, error) {
	return gitcmd.Operation{}, false, nil
}
