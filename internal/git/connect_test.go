package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type interceptRunner struct {
	delegate Runner
	commands []Command
	before   func(Command) error
	after    func(Command, Result, error) (Result, error)
}

func (r *interceptRunner) Run(ctx context.Context, command Command) (Result, error) {
	r.commands = append(r.commands, command)
	if r.before != nil {
		if err := r.before(command); err != nil {
			return Result{}, err
		}
	}
	result, err := r.delegate.Run(ctx, command)
	if r.after != nil {
		return r.after(command, result, err)
	}
	return result, err
}

func initializeWithRunner(
	t *testing.T,
	fixture *connectFixture,
	runner Runner,
	options InitializeOptions,
	transaction WorktreeTransaction,
	progress Progress,
) (OperationResult, error) {
	t.Helper()
	service := NewService(runner, NewClient(runner))
	if transaction == nil {
		transaction = func(_ context.Context, mutate func(string) error) error { return mutate(fixture.root) }
	}
	if progress == nil {
		progress = func(context.Context, Checkpoint) error { return nil }
	}
	return service.Initialize(context.Background(), options, transaction, progress)
}

func applyCheckpoint(operation *Operation, checkpoint Checkpoint) {
	operation.Stage = checkpoint.Stage
	operation.BackupRef = checkpoint.BackupRef
	operation.LocalOID = checkpoint.LocalOID
	operation.CandidateOID = checkpoint.CandidateOID
	operation.RemoteOID = checkpoint.RemoteOID
	operation.PushOID = checkpoint.PushOID
	operation.ChangedPaths = append([]string(nil), checkpoint.ChangedPaths...)
	operation.ConflictPaths = append([]string(nil), checkpoint.ConflictPaths...)
}

func TestConflictErrorIsSafeSortedAndCloned(t *testing.T) {
	paths := []string{"z.md", "a.md", "z.md"}
	err := newConflictError(paths)
	paths[0] = "changed"
	if err.Error() != "Git merge has conflicts" || !reflect.DeepEqual(err.Paths, []string{"a.md", "z.md"}) {
		t.Fatalf("ConflictError = %#v / %q", err, err.Error())
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != CodeGitConflict || safeErr.Diagnostic() != "" {
		t.Fatalf("unwrapped error = %#v", safeErr)
	}
}

func TestInitializeRetryReusesJournaledBackupRef(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	localOID := fixture.initLocal("local")
	journaled := "refs/igonotes/backups/20260903T081112.123456789Z-7"
	fixture.git(fixture.root, "update-ref", journaled, localOID, strings.Repeat("0", 40))
	options := fixture.options()
	options.Operation.BackupRef = journaled
	options.Operation.LocalOID = localOID

	result, err := fixture.initialize(options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupRef != journaled || fixture.git(fixture.root, "rev-parse", journaled) != localOID {
		t.Fatalf("backup = %q, want journaled ref", result.BackupRef)
	}
}

func TestInitializeBackupRefUsesPersistedCreationTimestamp(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	fixture.initLocal("local")
	result, err := fixture.initialize(fixture.options(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	const want = "refs/igonotes/backups/20260903T081112.123456789Z"
	if result.BackupRef != want {
		t.Fatalf("BackupRef = %q, want %q", result.BackupRef, want)
	}
}

func TestInitializeBackupRefCollisionUsesDeterministicSuffix(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	localOID := fixture.initLocal("local")
	base := "refs/igonotes/backups/20260903T081112.123456789Z"
	fixture.git(fixture.root, "update-ref", base, localOID+"^{tree}", strings.Repeat("0", 40))
	result, err := fixture.initialize(fixture.options(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupRef != base+"-1" {
		t.Fatalf("BackupRef = %q, want deterministic suffix", result.BackupRef)
	}
}

func TestInitializePersistsBackupRefBeforeUpdateRef(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	fixture.initLocal("local")
	var persisted string
	runner := &interceptRunner{delegate: NewCommandRunner()}
	runner.before = func(command Command) error {
		if len(command.Args) >= 2 && command.Args[0] == "update-ref" && command.Args[1] == "--create-reflog" && persisted == "" {
			return errors.New("backup update-ref ran before durable checkpoint")
		}
		return nil
	}
	_, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, func(_ context.Context, checkpoint Checkpoint) error {
		if checkpoint.BackupRef != "" {
			persisted = checkpoint.BackupRef
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInitializePreservesRemoteTrustAcrossTemplateChange(t *testing.T) {
	fixture := newConnectFixture(t)
	oldRemote := fixture.seedRemote()
	fixture.initLocal("local")
	first, err := fixture.initialize(fixture.options(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(filepath.Dir(fixture.root), "seed")
	if err := os.WriteFile(filepath.Join(seed, "later.md"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(seed, "add", "--all", "--", ".")
	fixture.git(seed, "commit", "-m", "later remote")
	// The first connect advanced origin, so advance the seed from the trusted commit before pushing.
	fixture.git(seed, "fetch", "origin", "main")
	fixture.git(seed, "merge", "--no-edit", "origin/main")
	fixture.git(seed, "push", "origin", "HEAD:main")
	options := fixture.options()
	options.Operation.ID = "1123456789abcdef0123456789abcdef"
	options.Operation.RemoteFingerprint = "remote-v1"
	options.Snapshot.Fingerprint = "template-changed"
	options.Operation.ConfigFingerprint = "template-changed"
	options.Snapshot.CommitTemplate = "changed template"
	options.LastRemoteOID = first.RemoteOID
	result, err := fixture.initialize(options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertAncestor(t, fixture, oldRemote, result.RemoteOID)
	if result.RemoteOID != fixture.remoteOID() {
		t.Fatal("trusted remote was not advanced")
	}
}

func TestInitializeRebaselinesManagedRefAfterConfirmedRemoteChange(t *testing.T) {
	fixture := newConnectFixture(t)
	remoteOID := fixture.seedRemote()
	localOID := fixture.initLocal("local")
	managed := "refs/igonotes/remotes/main"
	fixture.git(fixture.root, "update-ref", managed, localOID, strings.Repeat("0", 40))
	options := fixture.options()
	options.Operation.RemoteFingerprint = "new-remote"
	options.Snapshot.RemoteFingerprint = "new-remote"
	options.LastRemoteOID = ""
	result, err := fixture.initialize(options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertAncestor(t, fixture, remoteOID, result.PushOID)
	if got := fixture.git(fixture.root, "rev-parse", managed); got != result.PushOID {
		t.Fatalf("managed ref = %q, want pushed OID %q", got, result.PushOID)
	}
}

func TestInitializePushTimeoutIsRecognizedOnRetry(t *testing.T) {
	fixture := newConnectFixture(t)
	trustedOID := fixture.seedRemote()
	fixture.initLocal("local")
	fixture.git(fixture.root, "fetch", fixture.remote, "refs/heads/main")
	fixture.git(fixture.root, "update-ref", "refs/igonotes/remotes/main", trustedOID, strings.Repeat("0", 40))
	seed := filepath.Join(filepath.Dir(fixture.root), "seed")
	if err := os.WriteFile(filepath.Join(seed, "advanced.md"), []byte("advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(seed, "add", "--all", "--", ".")
	fixture.git(seed, "commit", "-m", "advance remote")
	fixture.git(seed, "push", "origin", "HEAD:main")
	options := fixture.options()
	options.LastRemoteOID = trustedOID
	base := NewCommandRunner()
	timedOut := false
	runner := &interceptRunner{delegate: base}
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if err == nil && len(command.Args) > 0 && command.Args[0] == "push" && !timedOut {
			timedOut = true
			return Result{}, &SafeError{Code: CodeTimedOut, Message: "Git command timed out"}
		}
		return result, err
	}
	_, err := initializeWithRunner(t, fixture, runner, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		applyCheckpoint(&options.Operation, checkpoint)
		return nil
	})
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != CodeTimedOut {
		t.Fatalf("first Initialize() error = %#v, want timeout", err)
	}
	result, err := initializeWithRunner(t, fixture, NewCommandRunner(), options, nil, nil)
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if result.PushOID != fixture.remoteOID() {
		t.Fatal("retry did not recognize the accepted exact-OID push")
	}
}

func TestInitializeRechecksRemoteBeforePush(t *testing.T) {
	fixture := newConnectFixture(t)
	original := fixture.seedRemote()
	changed := ""
	transaction := func(ctx context.Context, mutate func(string) error) error {
		if err := mutate(fixture.root); err != nil {
			return err
		}
		seed := filepath.Join(filepath.Dir(fixture.root), "seed")
		if err := os.WriteFile(filepath.Join(seed, "race.md"), []byte("race\n"), 0o600); err != nil {
			return err
		}
		fixture.git(seed, "add", "--all", "--", ".")
		fixture.git(seed, "commit", "-m", "racing remote")
		fixture.git(seed, "push", "origin", "HEAD:main")
		changed = fixture.remoteOID()
		return nil
	}
	_, err := fixture.initialize(fixture.options(), transaction, nil)
	if err == nil || changed == "" || changed == original || fixture.remoteOID() != changed {
		t.Fatalf("Initialize() error = %v, remote %q; expected changed remote and no push", err, fixture.remoteOID())
	}
}

func TestInitializePushUsesNoVerify(t *testing.T) {
	fixture := newConnectFixture(t)
	runner := &interceptRunner{delegate: NewCommandRunner()}
	if _, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if len(command.Args) > 0 && command.Args[0] == "push" {
			if len(command.Args) != 5 || command.Args[1] != "--no-verify" || command.Args[2] != "--porcelain" || command.Args[3] != "origin" {
				t.Fatalf("push argv = %q", command.Args)
			}
			if command.ReadOnly || command.Scope != NetworkOperation || !reflect.DeepEqual(command.Secrets, []string{fixture.remote}) {
				t.Fatalf("push command metadata = %#v", command)
			}
			return
		}
	}
	t.Fatal("push command was not run")
}

func TestInitializeRetryRecoversBackupWhenCheckpointLags(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	fixture.initLocal("local")
	options := fixture.options()
	checkpointFailure := errors.New("checkpoint unavailable")
	switched := false
	runner := &interceptRunner{delegate: NewCommandRunner()}
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if err == nil && len(command.Args) > 0 && command.Args[0] == "switch" {
			switched = true
		}
		return result, err
	}
	_, err := initializeWithRunner(t, fixture, runner, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		if switched {
			return checkpointFailure
		}
		applyCheckpoint(&options.Operation, checkpoint)
		return nil
	})
	if !errors.Is(err, checkpointFailure) || options.Operation.BackupRef == "" {
		t.Fatalf("first error = %v, checkpoint = %#v", err, options.Operation)
	}
	if _, err := fixture.initialize(options, nil, nil); err != nil {
		t.Fatalf("retry error = %v", err)
	}
}

func TestInitializeRetryRecoversTrustedRefWhenCheckpointLags(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	options := fixture.options()
	failure := errors.New("checkpoint unavailable")
	seenCandidate := false
	_, err := fixture.initialize(options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		if checkpoint.CandidateOID != "" && checkpoint.RemoteOID == "" {
			seenCandidate = true
			applyCheckpoint(&options.Operation, checkpoint)
			return nil
		}
		if seenCandidate && checkpoint.RemoteOID != "" && checkpoint.PushOID == "" {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) {
		t.Fatalf("first error = %v, want checkpoint failure", err)
	}
	if _, err := fixture.initialize(options, nil, nil); err != nil {
		t.Fatalf("retry error = %v", err)
	}
}

func TestInitializeReindexesBeforeWorktreeUnlockAndPush(t *testing.T) {
	fixture := newConnectFixture(t)
	events := []string{}
	runner := &interceptRunner{delegate: NewCommandRunner()}
	runner.before = func(command Command) error {
		if len(command.Args) > 0 && command.Args[0] == "push" {
			events = append(events, "push")
		}
		return nil
	}
	transaction := func(ctx context.Context, mutate func(string) error) error {
		events = append(events, "lock")
		err := mutate(fixture.root)
		events = append(events, "reindex", "unlock")
		return err
	}
	if _, err := initializeWithRunner(t, fixture, runner, fixture.options(), transaction, nil); err != nil {
		t.Fatal(err)
	}
	pushAt := -1
	for index, event := range events {
		if event == "push" {
			pushAt = index
			break
		}
	}
	if pushAt < 2 || !reflect.DeepEqual(events[pushAt-2:pushAt], []string{"reindex", "unlock"}) {
		t.Fatalf("events = %q", events)
	}
}

func TestInitializeValidatesInputsBeforeMutation(t *testing.T) {
	fixture := newConnectFixture(t)
	tests := []struct {
		name   string
		mutate func(*InitializeOptions)
	}{
		{name: "empty path", mutate: func(options *InitializeOptions) { options.Snapshot.Path = "" }},
		{name: "empty URL", mutate: func(options *InitializeOptions) { options.Snapshot.URL = "" }},
		{name: "empty branch", mutate: func(options *InitializeOptions) { options.Snapshot.Branch = "" }},
		{name: "invalid operation ID", mutate: func(options *InitializeOptions) { options.Operation.ID = "../unsafe" }},
		{name: "wrong kind", mutate: func(options *InitializeOptions) { options.Operation.Kind = OperationSync }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := fixture.options()
			test.mutate(&options)
			_, err := fixture.initialize(options, nil, nil)
			if err == nil {
				t.Fatal("Initialize() error = nil")
			}
		})
	}
}

func TestInitializeRejectsSkippedWorktreeCallbackWithoutPush(t *testing.T) {
	fixture := newConnectFixture(t)
	runner := &interceptRunner{delegate: NewCommandRunner()}
	transaction := func(context.Context, func(string) error) error { return nil }
	_, err := initializeWithRunner(t, fixture, runner, fixture.options(), transaction, nil)
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != CodeOperationInterrupted {
		t.Fatalf("Initialize() error = %#v, want interrupted callback contract", err)
	}
	for _, command := range runner.commands {
		if len(command.Args) != 0 && command.Args[0] == "push" {
			t.Fatalf("push was attempted after skipped worktree callback: %q", command.Args)
		}
	}
}
