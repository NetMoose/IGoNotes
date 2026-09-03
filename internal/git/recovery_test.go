package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func recoveryOptions(options SyncOptions, operation *Operation) RecoveryOptions {
	return RecoveryOptions{Snapshot: options.Snapshot, Operation: operation}
}

func runRecovery(t *testing.T, fixture *connectFixture, runner Runner, options RecoveryOptions) (RecoveryResult, error) {
	t.Helper()
	if runner == nil {
		runner = NewCommandRunner()
	}
	return NewService(runner, NewClient(runner)).RecoverLocal(context.Background(), options)
}

func makeSyncConflict(t *testing.T) (*connectFixture, SyncOptions, Operation) {
	t.Helper()
	fixture, options := preparedSyncFixture(t)
	fixture.write("remote.md", "local conflict\n")
	seedRemoteAdvance(t, fixture, "remote.md", "remote conflict\n")
	operation := options.Operation
	_, err := runSync(t, fixture, nil, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		applyCheckpoint(&operation, checkpoint)
		return nil
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("could not create conflict: %v", err)
	}
	return fixture, options, operation
}

func TestRecoverLocalDetectsMergeConflict(t *testing.T) {
	fixture, options, operation := makeSyncConflict(t)
	result, err := runRecovery(t, fixture, nil, recoveryOptions(options, &operation))
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !result.Blocking || !reflect.DeepEqual(result.ConflictPaths, []string{"remote.md"}) || result.MergeHeadOID == "" {
		t.Fatalf("RecoverLocal() = %#v, %#v", result, err)
	}
}

func TestRecoverLocalPreservesPartiallyResolvedIndex(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("remote.md", "local\n")
	fixture.write("second.md", "local\n")
	seed := filepath.Join(filepath.Dir(fixture.root), "seed")
	for _, entry := range []struct{ path, contents string }{{"remote.md", "remote\n"}, {"second.md", "remote\n"}} {
		if err := os.WriteFile(filepath.Join(seed, entry.path), []byte(entry.contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.git(seed, "add", "--all", "--", ".")
	fixture.git(seed, "commit", "-m", "two conflicts")
	fixture.git(seed, "push", "origin", "HEAD:main")
	operation := options.Operation
	_, err := runSync(t, fixture, nil, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		applyCheckpoint(&operation, checkpoint)
		return nil
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatal(err)
	}
	fixture.write("remote.md", "resolved\n")
	fixture.git(fixture.root, "add", "--", "remote.md")
	stagedBefore := fixture.git(fixture.root, "diff", "--cached", "--name-only")
	result, err := runRecovery(t, fixture, nil, recoveryOptions(options, &operation))
	if !errors.As(err, &conflict) || !reflect.DeepEqual(result.ConflictPaths, []string{"second.md"}) {
		t.Fatalf("RecoverLocal() = %#v, %v", result, err)
	}
	if stagedAfter := fixture.git(fixture.root, "diff", "--cached", "--name-only"); stagedAfter != stagedBefore {
		t.Fatalf("staged resolution changed: %q -> %q", stagedBefore, stagedAfter)
	}
}

func TestRecoverLocalDetectsRepositoryLockWithoutDeletingIt(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	lock := filepath.Join(fixture.root, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("owned elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runRecovery(t, fixture, nil, recoveryOptions(options, nil))
	assertSafeCode(t, err, CodeRepositoryLocked)
	if !result.Blocking {
		t.Fatal("repository lock was not blocking")
	}
	if contents, readErr := os.ReadFile(lock); readErr != nil || string(contents) != "owned elsewhere\n" {
		t.Fatalf("index.lock changed: %q, %v", contents, readErr)
	}
}

func TestRecoverLocalRejectsOtherUnfinishedOperation(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	head := fixture.git(fixture.root, "rev-parse", "HEAD")
	marker := filepath.Join(fixture.root, ".git", "CHERRY_PICK_HEAD")
	if err := os.WriteFile(marker, []byte(head+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runRecovery(t, fixture, nil, recoveryOptions(options, nil))
	assertSafeCode(t, err, CodeOperationInterrupted)
	if !result.Blocking {
		t.Fatal("unfinished operation was not blocking")
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("unfinished marker was removed: %v", statErr)
	}
}

func TestRecoverLocalReconcilesTrustedRefOnlyOnThreeWayMatch(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	candidate := options.LastRemoteOID
	private := "refs/igonotes/fetch/" + options.Operation.ID
	fixture.git(fixture.root, "update-ref", private, candidate)
	operation := options.Operation
	operation.CandidateOID = candidate
	operation.RemoteOID = ""
	result, err := runRecovery(t, fixture, nil, recoveryOptions(options, &operation))
	if err != nil || result.RemoteOID != candidate || result.Blocking {
		t.Fatalf("three-way recovery = %#v, %v", result, err)
	}
	other := fixture.git(fixture.root, "commit-tree", fixture.git(fixture.root, "rev-parse", "HEAD^{tree}"), "-p", candidate, "-m", "other")
	fixture.git(fixture.root, "update-ref", private, other)
	result, err = runRecovery(t, fixture, nil, recoveryOptions(options, &operation))
	assertSafeCode(t, err, CodeNeedsReconnect)
	if !result.Blocking || result.RemoteOID != "" {
		t.Fatalf("mismatch recovery = %#v", result)
	}
}

func TestRecoverLocalRecognizesLocallyRecordedSuccessfulPush(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "pushed\n")
	result, err := runSync(t, fixture, nil, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation := options.Operation
	operation.Stage = StagePushing
	operation.CandidateOID = options.LastRemoteOID
	operation.RemoteOID = options.LastRemoteOID
	operation.PushOID = result.PushOID
	recovered, err := runRecovery(t, fixture, nil, recoveryOptions(options, &operation))
	if err != nil || recovered.Blocking || recovered.HeadOID != result.PushOID || recovered.RemoteOID != result.PushOID {
		t.Fatalf("RecoverLocal() = %#v, %v", recovered, err)
	}
}

func TestRecoverLocalRunsNoNetworkCommand(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	runner := &interceptRunner{delegate: NewCommandRunner()}
	if _, err := runRecovery(t, fixture, runner, recoveryOptions(options, nil)); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if command.Scope == NetworkOperation {
			t.Fatalf("recovery network command = %#v", command)
		}
	}
}

func TestRecoverLocalReportsCleanRepository(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	result, err := runRecovery(t, fixture, nil, recoveryOptions(options, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := fixture.git(fixture.root, "rev-parse", "HEAD")
	if result.Blocking || result.HeadOID != want || result.RemoteOID != options.LastRemoteOID || result.MergeHeadOID != "" || len(result.ConflictPaths) != 0 {
		t.Fatalf("RecoverLocal() = %#v", result)
	}
}
