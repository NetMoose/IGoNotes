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

func TestRecoverLocalConflictStateTable(t *testing.T) {
	assertState := func(t *testing.T, fixture *connectFixture, options SyncOptions, operation *Operation, want ConflictRecoveryState, blocking bool) {
		t.Helper()
		runner := &interceptRunner{delegate: NewCommandRunner()}
		result, _ := runRecovery(t, fixture, runner, recoveryOptions(options, operation))
		if result.ConflictState != want || result.Blocking != blocking {
			t.Fatalf("RecoverLocal() = %#v, want state %q blocking %t", result, want, blocking)
		}
		assertRecoveryLocalReadOnly(t, runner.commands)
	}

	newCommittedConflict := func(t *testing.T) (*connectFixture, SyncOptions, Operation) {
		t.Helper()
		fixture, options, original := makeSyncConflict(t)
		fixture.write("remote.md", "resolved\n")
		fixture.git(fixture.root, "add", "--", "remote.md")
		fixture.git(fixture.root, "commit", "--no-edit")
		completion := original
		completion.Kind = OperationConflictComplete
		completion.State = OperationRunning
		completion.PushOID = fixture.git(fixture.root, "rev-parse", "HEAD")
		return fixture, options, completion
	}

	t.Run("unresolved merge", func(t *testing.T) {
		fixture, options, operation := makeSyncConflict(t)
		assertState(t, fixture, options, &operation, RecoveryConflict, true)
	})

	t.Run("partially resolved merge", func(t *testing.T) {
		fixture, options := preparedSyncFixture(t)
		fixture.write("remote.md", "local\n")
		fixture.write("second.md", "local\n")
		seedRemoteAdvance(t, fixture, "remote.md", "remote changed\n")
		seedRemoteAdvance(t, fixture, "second.md", "remote\n")
		operation := options.Operation
		_, _ = runSync(t, fixture, nil, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
			applyCheckpoint(&operation, checkpoint)
			return nil
		})
		fixture.write("remote.md", "resolved\n")
		fixture.git(fixture.root, "add", "--", "remote.md")
		assertState(t, fixture, options, &operation, RecoveryConflict, true)
	})

	t.Run("foreign pending operation overrides unmerged index", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			marker string
			dir    bool
		}{
			{name: "rebase", marker: "rebase-merge", dir: true},
			{name: "cherry-pick", marker: "CHERRY_PICK_HEAD"},
			{name: "revert", marker: "REVERT_HEAD"},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture, options, operation := makeSyncConflict(t)
				gitDir := filepath.Join(fixture.root, ".git")
				if err := os.Remove(filepath.Join(gitDir, "MERGE_HEAD")); err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(gitDir, test.marker)
				if test.dir {
					if err := os.Mkdir(marker, 0o700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(marker, []byte(fixture.git(fixture.root, "rev-parse", "HEAD")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				assertState(t, fixture, options, &operation, RecoveryAmbiguous, true)
			})
		}
	})

	t.Run("all staged merge can complete", func(t *testing.T) {
		fixture, options, operation := makeSyncConflict(t)
		fixture.write("remote.md", "resolved\n")
		fixture.git(fixture.root, "add", "--", "remote.md")

		result, err := runRecovery(t, fixture, nil, recoveryOptions(options, &operation))
		if !errorHasSafeCode(err, CodeOperationInterrupted) || !result.Blocking || result.ConflictState != RecoveryCanComplete || len(result.ConflictPaths) != 0 {
			t.Fatalf("RecoverLocal() = %#v, %v", result, err)
		}
	})

	t.Run("committed merge needs reindex", func(t *testing.T) {
		fixture, options, operation := newCommittedConflict(t)
		operation.Stage = StageConflictCommitted
		assertState(t, fixture, options, &operation, RecoveryNeedsReindex, false)
	})

	t.Run("reindexed merge is push pending", func(t *testing.T) {
		fixture, options, operation := newCommittedConflict(t)
		operation.Stage = StageConflictReindexed
		assertState(t, fixture, options, &operation, RecoveryPushPending, false)
	})

	t.Run("pushing merge is push unknown without trusted proof", func(t *testing.T) {
		fixture, options, operation := newCommittedConflict(t)
		operation.Stage = StageConflictPushing
		assertState(t, fixture, options, &operation, RecoveryPushUnknown, false)
	})

	t.Run("pushing merge is pushed only with matching trusted ref", func(t *testing.T) {
		fixture, options, operation := newCommittedConflict(t)
		operation.Stage = StageConflictPushing
		fixture.git(fixture.root, "update-ref", managedRemoteRef(options.Snapshot.Branch), operation.PushOID, operation.RemoteOID)
		result, err := runRecovery(t, fixture, nil, recoveryOptions(options, &operation))
		if err != nil || result.ConflictState != RecoveryPushed || result.PushOID != operation.PushOID || result.RemoteOID != operation.PushOID {
			t.Fatalf("RecoverLocal() = %#v, %v", result, err)
		}
	})

	t.Run("completed abort is recovered only by abort journal", func(t *testing.T) {
		fixture, options, operation := makeSyncConflict(t)
		operation.LocalOID = fixture.git(fixture.root, "rev-parse", "HEAD")
		fixture.git(fixture.root, "merge", "--abort")
		operation.Kind = OperationConflictAbort
		operation.State = OperationRunning
		operation.Stage = StageConflictAborting
		assertState(t, fixture, options, &operation, RecoveryAborted, false)
	})

	t.Run("abort recovery requires an unfinished abort journal", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			state    OperationState
			stage    Stage
			want     ConflictRecoveryState
			blocking bool
		}{
			{name: "queued", state: OperationQueued, stage: StageQueued, want: RecoveryAborted},
			{name: "running", state: OperationRunning, stage: StageConflictAborting, want: RecoveryAborted},
			{name: "failed", state: OperationFailed, stage: StageConflictAborting, want: RecoveryAmbiguous, blocking: true},
			{name: "conflict", state: OperationConflict, stage: StageConflictAborting, want: RecoveryAmbiguous, blocking: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture, options, operation := makeSyncConflict(t)
				operation.LocalOID = fixture.git(fixture.root, "rev-parse", "HEAD")
				fixture.git(fixture.root, "merge", "--abort")
				operation.Kind = OperationConflictAbort
				operation.State = test.state
				operation.Stage = test.stage
				assertState(t, fixture, options, &operation, test.want, test.blocking)
			})
		}
	})

	t.Run("original conflict without merge marker is ambiguous", func(t *testing.T) {
		fixture, options, operation := makeSyncConflict(t)
		fixture.git(fixture.root, "merge", "--abort")
		operation.State = OperationConflict
		assertState(t, fixture, options, &operation, RecoveryAmbiguous, true)
	})

	t.Run("unrelated head is ambiguous", func(t *testing.T) {
		fixture, options, operation := newCommittedConflict(t)
		operation.Stage = StageConflictCommitted
		fixture.write("external.md", "external\n")
		fixture.git(fixture.root, "add", "--", "external.md")
		fixture.git(fixture.root, "commit", "-m", "external commit")
		assertState(t, fixture, options, &operation, RecoveryAmbiguous, true)
	})

	t.Run("lock is reported without deletion", func(t *testing.T) {
		fixture, options := preparedSyncFixture(t)
		lock := filepath.Join(fixture.root, ".git", "index.lock")
		if err := os.WriteFile(lock, []byte("external\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertState(t, fixture, options, nil, RecoveryLocked, true)
		if _, err := os.Stat(lock); err != nil {
			t.Fatalf("index lock was removed: %v", err)
		}
	})

	t.Run("foreign operation is ambiguous", func(t *testing.T) {
		fixture, options := preparedSyncFixture(t)
		head := fixture.git(fixture.root, "rev-parse", "HEAD")
		marker := filepath.Join(fixture.root, ".git", "CHERRY_PICK_HEAD")
		if err := os.WriteFile(marker, []byte(head+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertState(t, fixture, options, nil, RecoveryAmbiguous, true)
	})

	t.Run("clean repository has no conflict decision", func(t *testing.T) {
		fixture, options := preparedSyncFixture(t)
		assertState(t, fixture, options, nil, "", false)
	})
}

func TestRecoverLocalDetectsOrphanedUnmergedIndex(t *testing.T) {
	fixture, options, operation := makeSyncConflict(t)
	if err := os.Remove(filepath.Join(fixture.root, ".git", "MERGE_HEAD")); err != nil {
		t.Fatal(err)
	}
	indexBefore := fixture.git(fixture.root, "ls-files", "--stage")
	statusBefore := fixture.git(fixture.root, "status", "--porcelain=v1")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	result, err := runRecovery(t, fixture, runner, recoveryOptions(options, &operation))
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !result.Blocking || !reflect.DeepEqual(result.ConflictPaths, []string{"remote.md"}) || result.MergeHeadOID != "" {
		t.Fatalf("RecoverLocal() = %#v, %#v", result, err)
	}
	if !errorHasSafeCode(err, CodeOperationInterrupted) {
		t.Fatalf("RecoverLocal() error = %#v, want operation_interrupted context", err)
	}
	for _, command := range runner.commands {
		if command.Scope == NetworkOperation || !command.ReadOnly {
			t.Fatalf("orphan recovery mutated or used network: %#v", command)
		}
	}
	if indexAfter := fixture.git(fixture.root, "ls-files", "--stage"); indexAfter != indexBefore {
		t.Fatalf("index changed:\n%s\nwant:\n%s", indexAfter, indexBefore)
	}
	if statusAfter := fixture.git(fixture.root, "status", "--porcelain=v1"); statusAfter != statusBefore {
		t.Fatalf("status changed: %q -> %q", statusBefore, statusAfter)
	}
}

func TestRecoverLocalPreservesPartiallyResolvedOrphanedIndex(t *testing.T) {
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
	fixture.git(seed, "commit", "-m", "two orphan conflicts")
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
	if err := os.Remove(filepath.Join(fixture.root, ".git", "MERGE_HEAD")); err != nil {
		t.Fatal(err)
	}
	indexBefore := fixture.git(fixture.root, "ls-files", "--stage")
	result, err := runRecovery(t, fixture, nil, recoveryOptions(options, &operation))
	if !errors.As(err, &conflict) || !result.Blocking || !reflect.DeepEqual(result.ConflictPaths, []string{"second.md"}) ||
		!errorHasSafeCode(err, CodeOperationInterrupted) {
		t.Fatalf("RecoverLocal() = %#v, %#v", result, err)
	}
	if indexAfter := fixture.git(fixture.root, "ls-files", "--stage"); indexAfter != indexBefore {
		t.Fatalf("partially resolved orphan index changed:\n%s\nwant:\n%s", indexAfter, indexBefore)
	}
}

func TestRecoverLocalUnmergedInspectionFailureBlocks(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	runner := &interceptRunner{delegate: NewCommandRunner()}
	inspectionErr := errors.New("unmerged inspection unavailable")
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if reflect.DeepEqual(command.Args, []string{"ls-files", "-u", "-z"}) {
			return Result{}, inspectionErr
		}
		return result, err
	}
	result, err := runRecovery(t, fixture, runner, recoveryOptions(options, nil))
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, inspectionErr) || !result.Blocking || !errorHasSafeCode(err, CodeOperationInterrupted) {
		t.Fatalf("RecoverLocal() = %#v, %#v", result, err)
	}
	for _, command := range runner.commands {
		if command.Scope == NetworkOperation || !command.ReadOnly {
			t.Fatalf("failed recovery inspection mutated or used network: %#v", command)
		}
	}
}

func TestRecoverLocalMalformedMergeHeadFailsClosed(t *testing.T) {
	fixture, options, operation := makeSyncConflict(t)
	marker := filepath.Join(fixture.root, ".git", "MERGE_HEAD")
	if err := os.WriteFile(marker, []byte("not-an-oid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &interceptRunner{delegate: NewCommandRunner()}
	result, err := runRecovery(t, fixture, runner, recoveryOptions(options, &operation))
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !result.Blocking || !errorHasSafeCode(err, CodeCommandFailed) ||
		!errorHasSafeCode(err, CodeOperationInterrupted) {
		t.Fatalf("RecoverLocal() = %#v, %#v", result, err)
	}
	contents, readErr := os.ReadFile(marker)
	if readErr != nil || string(contents) != "not-an-oid\n" {
		t.Fatalf("MERGE_HEAD changed: %q, %v", contents, readErr)
	}
	assertRecoveryLocalReadOnly(t, runner.commands)
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

func TestRecoverLocalRejectsManagedAndJournalRemoteMismatch(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	journalRemote := options.LastRemoteOID
	managed := fixture.git(fixture.root, "commit-tree", fixture.git(fixture.root, "rev-parse", "HEAD^{tree}"), "-p", journalRemote, "-m", "managed mismatch")
	fixture.git(fixture.root, "update-ref", managedRemoteRef("main"), managed, journalRemote)
	operation := options.Operation
	operation.RemoteOID = journalRemote
	runner := &interceptRunner{delegate: NewCommandRunner()}

	result, err := runRecovery(t, fixture, runner, recoveryOptions(options, &operation))
	assertSafeCode(t, err, CodeNeedsReconnect)
	if !result.Blocking || result.RemoteOID != "" {
		t.Fatalf("RecoverLocal() = %#v, want blocking without guessed trust", result)
	}
	if got := fixture.git(fixture.root, "rev-parse", managedRemoteRef("main")); got != managed {
		t.Fatalf("managed ref changed from %q to %q", managed, got)
	}
	assertRecoveryLocalReadOnly(t, runner.commands)
}

func TestRecoverLocalTrustMismatchPreservesConflictGate(t *testing.T) {
	fixture, options, operation := makeSyncConflict(t)
	operation.RemoteOID = options.LastRemoteOID
	operation.CandidateOID = ""
	runner := &interceptRunner{delegate: NewCommandRunner()}
	result, err := runRecovery(t, fixture, runner, recoveryOptions(options, &operation))
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !reflect.DeepEqual(conflict.Paths, []string{"remote.md"}) ||
		!errorHasSafeCode(err, CodeNeedsReconnect) || !result.Blocking || result.RemoteOID != "" {
		t.Fatalf("RecoverLocal() = %#v, %#v", result, err)
	}
	assertRecoveryLocalReadOnly(t, runner.commands)
}

func TestRecoverLocalAllowsExplicitJournalTrustGaps(t *testing.T) {
	t.Run("candidate", func(t *testing.T) {
		fixture, options := preparedSyncFixture(t)
		journalRemote := options.LastRemoteOID
		candidate := fixture.git(fixture.root, "commit-tree", fixture.git(fixture.root, "rev-parse", "HEAD^{tree}"), "-p", journalRemote, "-m", "candidate")
		fixture.git(fixture.root, "update-ref", managedRemoteRef("main"), candidate, journalRemote)
		fixture.git(fixture.root, "update-ref", "refs/igonotes/fetch/"+options.Operation.ID, candidate)
		operation := options.Operation
		operation.RemoteOID = journalRemote
		operation.CandidateOID = candidate
		runner := &interceptRunner{delegate: NewCommandRunner()}
		result, err := runRecovery(t, fixture, runner, recoveryOptions(options, &operation))
		if err != nil || result.Blocking || result.RemoteOID != candidate {
			t.Fatalf("candidate recovery = %#v, %v", result, err)
		}
		assertRecoveryLocalReadOnly(t, runner.commands)
	})

	t.Run("confirmed push", func(t *testing.T) {
		fixture, options := preparedSyncFixture(t)
		journalRemote := options.LastRemoteOID
		fixture.write("pushed.md", "pushed\n")
		pushed, err := runSync(t, fixture, nil, options, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		operation := options.Operation
		operation.RemoteOID = journalRemote
		operation.CandidateOID = journalRemote
		operation.PushOID = pushed.PushOID
		runner := &interceptRunner{delegate: NewCommandRunner()}
		result, err := runRecovery(t, fixture, runner, recoveryOptions(options, &operation))
		if err != nil || result.Blocking || result.RemoteOID != pushed.PushOID || result.HeadOID != pushed.PushOID {
			t.Fatalf("push recovery = %#v, %v", result, err)
		}
		assertRecoveryLocalReadOnly(t, runner.commands)
	})
}

func TestRecoverLocalRejectsUnprovenJournalTrustGaps(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, *connectFixture, *SyncOptions, *Operation)
	}{
		{name: "candidate private mismatch", setup: func(t *testing.T, fixture *connectFixture, options *SyncOptions, operation *Operation) {
			journalRemote := options.LastRemoteOID
			candidate := fixture.git(fixture.root, "commit-tree", fixture.git(fixture.root, "rev-parse", "HEAD^{tree}"), "-p", journalRemote, "-m", "candidate")
			other := fixture.git(fixture.root, "commit-tree", fixture.git(fixture.root, "rev-parse", "HEAD^{tree}"), "-p", journalRemote, "-m", "other")
			fixture.git(fixture.root, "update-ref", managedRemoteRef("main"), candidate, journalRemote)
			fixture.git(fixture.root, "update-ref", "refs/igonotes/fetch/"+options.Operation.ID, other)
			operation.RemoteOID = journalRemote
			operation.CandidateOID = candidate
		}},
		{name: "push head mismatch", setup: func(t *testing.T, fixture *connectFixture, options *SyncOptions, operation *Operation) {
			journalRemote := options.LastRemoteOID
			pushOID := fixture.git(fixture.root, "commit-tree", fixture.git(fixture.root, "rev-parse", "HEAD^{tree}"), "-p", journalRemote, "-m", "unproven push")
			fixture.git(fixture.root, "update-ref", managedRemoteRef("main"), pushOID, journalRemote)
			operation.RemoteOID = journalRemote
			operation.PushOID = pushOID
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, options := preparedSyncFixture(t)
			operation := options.Operation
			test.setup(t, fixture, &options, &operation)
			managedBefore := fixture.git(fixture.root, "rev-parse", managedRemoteRef("main"))
			runner := &interceptRunner{delegate: NewCommandRunner()}
			result, err := runRecovery(t, fixture, runner, recoveryOptions(options, &operation))
			assertSafeCode(t, err, CodeNeedsReconnect)
			if !result.Blocking || result.RemoteOID != "" {
				t.Fatalf("RecoverLocal() = %#v, want blocking without guessed trust", result)
			}
			if managedAfter := fixture.git(fixture.root, "rev-parse", managedRemoteRef("main")); managedAfter != managedBefore {
				t.Fatalf("managed ref changed: %q -> %q", managedBefore, managedAfter)
			}
			assertRecoveryLocalReadOnly(t, runner.commands)
		})
	}
}

func assertRecoveryLocalReadOnly(t *testing.T, commands []Command) {
	t.Helper()
	for _, command := range commands {
		if command.Scope == NetworkOperation || !command.ReadOnly {
			t.Fatalf("recovery mutation/network command = %#v", command)
		}
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
