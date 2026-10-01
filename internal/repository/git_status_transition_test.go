package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

func TestGitStatusRepositoryApplyTransitionPersistsSuppliedStatus(t *testing.T) {
	repo, db := openTestGitStatusRepository(t)
	defer db.Close()
	ctx := context.Background()
	path := "/notes/work"
	if err := repo.Upsert(ctx, model.GitStatus{
		Base:                "old-base",
		RepositoryPath:      path,
		State:               model.GitStateSyncing,
		OperationID:         "old-operation",
		Stage:               "old-stage",
		Ahead:               1,
		Behind:              2,
		ConsecutiveFailures: 3,
		ChangedPaths:        []string{"old.md"},
		RemoteOID:           "old-oid",
		Error:               &model.APIError{Code: "old", Message: "old error", Field: "old_field"},
	}); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	lastAttempt := time.Date(2026, time.September, 11, 12, 0, 1, 234000000, time.UTC)
	lastSuccess := time.Date(2026, time.September, 11, 11, 59, 59, 987000000, time.UTC)
	transition := GitStatusTransition{
		Status: model.GitStatus{
			Base:           "work",
			RepositoryPath: path,
			State:          model.GitStateError,
			OperationID:    "sync-42",
			Stage:          "push",
			Ahead:          5,
			Behind:         7,
			LastAttempt:    &lastAttempt,
			LastSuccess:    &lastSuccess,
			ChangedPaths:   []string{"README.md", "daily/today.md"},
			RemoteOID:      "new-oid",
			Error:          &model.APIError{Code: "push_failed", Message: "rejected", Field: "remote"},
		},
		Failures: GitFailurePreserve,
	}

	if err := repo.ApplyTransition(ctx, transition); err != nil {
		t.Fatalf("ApplyTransition() error = %v", err)
	}
	got, found, err := repo.Get(ctx, path)
	if err != nil || !found {
		t.Fatalf("Get() = %#v, %v, %v", got, found, err)
	}
	want := transition.Status
	want.ConsecutiveFailures = 3
	want.LastAttempt = millisecondUTC(lastAttempt)
	want.LastSuccess = millisecondUTC(lastSuccess)
	assertGitStatusEqual(t, got, want)
}

func TestGitStatusRepositoryApplyTransitionDerivesFailureBudget(t *testing.T) {
	tests := []struct {
		name      string
		failures  GitFailureAction
		initial   int
		state     model.GitState
		wantCount int
		wantState model.GitState
	}{
		{name: "preserves", failures: GitFailurePreserve, initial: 3, state: model.GitStateConflict, wantCount: 3, wantState: model.GitStateConflict},
		{name: "resets", failures: GitFailureReset, initial: 3, state: model.GitStateReady, wantCount: 0, wantState: model.GitStateReady},
		{name: "increments", failures: GitFailureIncrement, initial: 3, state: model.GitStateError, wantCount: 4, wantState: model.GitStateError},
		{name: "fifth increment pauses", failures: GitFailureIncrement, initial: GitStatusPauseFailureThreshold - 1, state: model.GitStateError, wantCount: GitStatusPauseFailureThreshold, wantState: model.GitStatePaused},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, db := openTestGitStatusRepository(t)
			defer db.Close()
			ctx := context.Background()
			path := "/notes/" + test.name
			if err := repo.Upsert(ctx, model.GitStatus{
				Base:                "work",
				RepositoryPath:      path,
				State:               model.GitStateSyncing,
				ConsecutiveFailures: test.initial,
				ChangedPaths:        []string{},
			}); err != nil {
				t.Fatalf("seed status: %v", err)
			}

			attempt := time.Date(2026, time.September, 11, 12, 0, 0, 0, time.UTC)
			transition := GitStatusTransition{
				Status: model.GitStatus{
					Base:           "work",
					RepositoryPath: path,
					State:          test.state,
					LastAttempt:    &attempt,
					ChangedPaths:   []string{},
					Error:          &model.APIError{Code: "result", Message: test.name},
				},
				Failures: test.failures,
			}
			if err := repo.ApplyTransition(ctx, transition); err != nil {
				t.Fatalf("ApplyTransition() error = %v", err)
			}

			got, found, err := repo.Get(ctx, path)
			if err != nil || !found {
				t.Fatalf("Get() = %#v, %v, %v", got, found, err)
			}
			if got.ConsecutiveFailures != test.wantCount || got.State != test.wantState {
				t.Errorf("derived transition = %#v, want failures %d and state %q", got, test.wantCount, test.wantState)
			}
			if got.Error == nil || got.Error.Message != test.name || got.LastAttempt == nil || !got.LastAttempt.Equal(attempt) {
				t.Errorf("transition persistence fields = %#v, want supplied error and attempt", got)
			}
		})
	}
}

func TestGitStatusRepositoryApplyTransitionConcurrentIncrementsDoNotLoseUpdates(t *testing.T) {
	repo, db := openTestGitStatusRepository(t)
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	const increments = 20
	path := "/notes/concurrent"
	if err := repo.Upsert(ctx, model.GitStatus{
		Base:           "work",
		RepositoryPath: path,
		State:          model.GitStateError,
		ChangedPaths:   []string{},
	}); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	attempt := time.Date(2026, time.September, 11, 12, 0, 0, 123000000, time.UTC)
	transition := GitStatusTransition{
		Status: model.GitStatus{
			Base:           "work",
			RepositoryPath: path,
			State:          model.GitStateError,
			Stage:          "push",
			LastAttempt:    &attempt,
			ChangedPaths:   []string{"README.md"},
			Error:          &model.APIError{Code: "push_failed", Message: "rejected", Field: "remote"},
		},
		Failures: GitFailureIncrement,
	}

	errs := make(chan error, increments)
	var ready sync.WaitGroup
	ready.Add(increments)
	start := make(chan struct{})
	for range increments {
		go func() {
			ready.Done()
			<-start
			errs <- repo.ApplyTransition(ctx, transition)
		}()
	}
	ready.Wait()
	close(start)
	for range increments {
		if err := <-errs; err != nil {
			t.Fatalf("ApplyTransition() error = %v", err)
		}
	}

	got, found, err := repo.Get(ctx, path)
	if err != nil || !found {
		t.Fatalf("Get() = %#v, %v, %v", got, found, err)
	}
	if got.ConsecutiveFailures != increments || got.State != model.GitStatePaused {
		t.Errorf("concurrent transition = %#v, want %d failures and paused", got, increments)
	}
	want := transition.Status
	want.State = model.GitStatePaused
	want.ConsecutiveFailures = increments
	want.LastAttempt = millisecondUTC(attempt)
	assertGitStatusEqual(t, got, want)
}

func assertGitStatusEqual(t *testing.T, got, want model.GitStatus) {
	t.Helper()
	if !equalGitStatus(got, want) {
		t.Errorf("status mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func equalGitStatus(left, right model.GitStatus) bool {
	if left.Base != right.Base ||
		left.RepositoryPath != right.RepositoryPath ||
		left.State != right.State ||
		left.OperationID != right.OperationID ||
		left.Stage != right.Stage ||
		left.Ahead != right.Ahead ||
		left.Behind != right.Behind ||
		left.ConsecutiveFailures != right.ConsecutiveFailures ||
		left.RemoteOID != right.RemoteOID {
		return false
	}
	if (left.LastAttempt == nil) != (right.LastAttempt == nil) || (left.LastAttempt != nil && !left.LastAttempt.Equal(*right.LastAttempt)) {
		return false
	}
	if (left.LastSuccess == nil) != (right.LastSuccess == nil) || (left.LastSuccess != nil && !left.LastSuccess.Equal(*right.LastSuccess)) {
		return false
	}
	if len(left.ChangedPaths) != len(right.ChangedPaths) {
		return false
	}
	for index := range left.ChangedPaths {
		if left.ChangedPaths[index] != right.ChangedPaths[index] {
			return false
		}
	}
	if (left.Error == nil) != (right.Error == nil) {
		return false
	}
	return left.Error == nil || *left.Error == *right.Error
}
