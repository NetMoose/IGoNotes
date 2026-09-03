package git

import (
	"context"
	"encoding/json"
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

func assertNoServicePush(t *testing.T, commands []Command) {
	t.Helper()
	for _, command := range commands {
		if len(command.Args) != 0 && command.Args[0] == "push" {
			t.Fatalf("unexpected service push: %q", command.Args)
		}
	}
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

func TestInitializeSHA256UsesObjectFormatWidthForZeroCAS(t *testing.T) {
	probe := t.TempDir()
	if output, err := runFixtureGit(probe, "init", "--object-format=sha256", "--initial-branch", "main"); err != nil {
		lower := strings.ToLower(output)
		if strings.Contains(lower, "unknown option") || strings.Contains(lower, "unsupported") || strings.Contains(lower, "not support") {
			t.Skipf("installed Git does not support SHA-256 repositories: %v (%s)", err, output)
		}
		t.Fatalf("could not probe SHA-256 repository support: %v (%s)", err, output)
	}
	zero64 := strings.Repeat("0", 64)

	t.Run("branch and trusted refs", func(t *testing.T) {
		fixture := newConnectFixtureWithObjectFormat(t, "sha256")
		candidate := fixture.seedRemote()
		fixture.git(fixture.root, "init", "--object-format=sha256", "--initial-branch", "main")
		runner := &interceptRunner{delegate: NewCommandRunner()}
		result, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false,
			"update-ref", "refs/heads/main", candidate, zero64))
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false,
			"update-ref", managedRemoteRef("main"), candidate, zero64))
		if result.PushOID != candidate {
			t.Fatalf("PushOID = %q, want %q", result.PushOID, candidate)
		}
	})

	t.Run("backup ref", func(t *testing.T) {
		fixture := newConnectFixtureWithObjectFormat(t, "sha256")
		fixture.seedRemote()
		localOID := fixture.initLocal("local")
		runner := &interceptRunner{delegate: NewCommandRunner()}
		result, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false, "update-ref", "--create-reflog", "-m",
			"IGoNotes initial-connect backup", result.BackupRef, localOID, zero64))
	})

	t.Run("post-push managed ref", func(t *testing.T) {
		fixture := newConnectFixtureWithObjectFormat(t, "sha256")
		localOID := fixture.initLocal("main")
		runner := &interceptRunner{delegate: NewCommandRunner()}
		result, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false,
			"update-ref", managedRemoteRef("main"), localOID, zero64))
		if result.PushOID != localOID || fixture.remoteOID() != localOID {
			t.Fatalf("push result = %#v, local = %q", result, localOID)
		}
	})
}

func TestInitializeRejectsBaseIdentityReplacementBeforeMutation(t *testing.T) {
	for _, mutation := range []string{"add", "commit", "backup-ref", "push", "parent-repository"} {
		t.Run(mutation, func(t *testing.T) {
			fixture := newConnectFixture(t)
			switch mutation {
			case "add", "commit", "parent-repository":
				fixture.write("note.md", "local note\n")
			case "backup-ref":
				fixture.seedRemote()
				fixture.initLocal("local")
			case "push":
				fixture.initLocal("main")
			}
			runner := &interceptRunner{delegate: NewCommandRunner()}
			swapped := false
			swappedAt := 0
			replacementHead := ""
			swap := func(parent bool) {
				if swapped {
					return
				}
				swapped = true
				swappedAt = len(runner.commands)
				original := fixture.root + "-original"
				if err := os.Rename(fixture.root, original); err != nil {
					t.Fatal(err)
				}
				if parent {
					parentPath := filepath.Dir(fixture.root)
					fixture.git(parentPath, "init", "--initial-branch", "parent")
					fixture.git(parentPath, "config", "user.name", "IGoNotes Test")
					fixture.git(parentPath, "config", "user.email", "igonotes@example.invalid")
					if err := os.Mkdir(fixture.root, 0o700); err != nil {
						t.Fatal(err)
					}
					return
				}
				replacement := fixture.root + "-replacement"
				if err := os.Mkdir(replacement, 0o700); err != nil {
					t.Fatal(err)
				}
				fixture.git(replacement, "init", "--initial-branch", "replacement")
				fixture.git(replacement, "config", "user.name", "IGoNotes Test")
				fixture.git(replacement, "config", "user.email", "igonotes@example.invalid")
				if err := os.WriteFile(filepath.Join(replacement, "replacement.md"), []byte("replacement\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				fixture.git(replacement, "add", "--all", "--", ".")
				fixture.git(replacement, "commit", "-m", "replacement")
				replacementHead = fixture.git(replacement, "rev-parse", "HEAD")
				if err := os.Rename(replacement, fixture.root); err != nil {
					t.Fatal(err)
				}
			}
			runner.after = func(command Command, result Result, err error) (Result, error) {
				if mutation == "commit" && reflect.DeepEqual(command.Args, []string{"diff", "--cached", "--name-only", "-z"}) {
					swap(false)
				}
				if mutation == "push" && reflect.DeepEqual(command.Args, []string{"ls-remote", "--symref", "origin"}) {
					swap(false)
				}
				return result, err
			}
			progress := func(_ context.Context, checkpoint Checkpoint) error {
				if mutation == "add" && checkpoint.Stage == StageSnapshotting {
					swap(false)
				}
				if mutation == "parent-repository" && checkpoint.Stage == StageSnapshotting {
					swap(true)
				}
				if mutation == "backup-ref" && checkpoint.Stage == StageBackingUp && checkpoint.BackupRef != "" {
					swap(false)
				}
				return nil
			}

			_, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, progress)
			if !swapped {
				t.Fatal("base path was not replaced")
			}
			var safeErr *SafeError
			if !errors.As(err, &safeErr) || (safeErr.Code != CodeNeedsReconnect && safeErr.Code != CodeRepositoryRoot) {
				t.Fatalf("Initialize() error = %#v, want identity error", err)
			}
			if len(runner.commands) != swappedAt {
				t.Fatalf("commands ran after replacement: %#v", runner.commands[swappedAt:])
			}
			if replacementHead != "" {
				if got := fixture.git(fixture.root, "rev-parse", "HEAD"); got != replacementHead {
					t.Fatalf("replacement HEAD = %q, want %q", got, replacementHead)
				}
				if status := fixture.git(fixture.root, "status", "--porcelain"); status != "" {
					t.Fatalf("replacement worktree changed: %q", status)
				}
			}
		})
	}
}

func TestInitializeConflictCheckpointFailurePreservesTypedConflict(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	fixture.initLocal("local")
	fixture.write("remote.md", "local collision\n")
	checkpointErr := errors.New("checkpoint unavailable")
	runner := &interceptRunner{delegate: NewCommandRunner()}

	_, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, func(_ context.Context, checkpoint Checkpoint) error {
		if len(checkpoint.ConflictPaths) != 0 {
			return checkpointErr
		}
		return nil
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !reflect.DeepEqual(conflict.Paths, []string{"remote.md"}) {
		t.Fatalf("Initialize() error = %#v, want typed conflict", err)
	}
	if !errors.Is(err, checkpointErr) {
		t.Fatalf("Initialize() error = %#v, want joined checkpoint error", err)
	}
	assertNoServicePush(t, runner.commands)
}

func TestInitializeConflictWithNewlineFilenamePreservesTypedGate(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	seed := filepath.Join(filepath.Dir(fixture.root), "seed")
	name := "line\nbreak.md"
	if err := os.WriteFile(filepath.Join(seed, name), []byte("remote secret contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(seed, "add", "--all", "--", ".")
	fixture.git(seed, "commit", "-m", "newline remote")
	fixture.git(seed, "push", "origin", "HEAD:main")
	fixture.initLocal("local")
	fixture.write(name, "local secret contents\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}

	_, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !reflect.DeepEqual(conflict.Paths, []string{name}) {
		t.Fatalf("Initialize() error = %#v, conflict = %#v", err, conflict)
	}
	if strings.Contains(err.Error(), "secret contents") {
		t.Fatalf("conflict error disclosed file contents: %v", err)
	}
	encoded, marshalErr := json.Marshal(conflict)
	if marshalErr != nil || !json.Valid(encoded) || !strings.Contains(string(encoded), `line\nbreak.md`) {
		t.Fatalf("JSON conflict = %q, error %v", encoded, marshalErr)
	}
	assertNoServicePush(t, runner.commands)
}

func TestInitializeConflictInspectionFailurePreservesTypedGate(t *testing.T) {
	for _, test := range []struct {
		name          string
		inspectionErr error
		truncate      bool
	}{
		{name: "command failure", inspectionErr: errors.New("conflict inspection unavailable")},
		{name: "truncated over 64KiB listing", truncate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConnectFixture(t)
			fixture.seedRemote()
			fixture.initLocal("local")
			fixture.write("remote.md", "local collision\n")
			checkpointErr := errors.New("checkpoint unavailable")
			runner := &interceptRunner{delegate: NewCommandRunner()}
			mergeFailed := false
			runner.after = func(command Command, result Result, err error) (Result, error) {
				if len(command.Args) > 0 && command.Args[0] == "merge" && err != nil {
					mergeFailed = true
				}
				if len(command.Args) >= 3 && reflect.DeepEqual(command.Args[:3], []string{"ls-files", "-u", "-z"}) {
					if test.inspectionErr != nil {
						return Result{}, test.inspectionErr
					}
					result.Stdout += strings.Repeat("x", DefaultOutputLimit)
					result.StdoutTruncated = true
				}
				return result, err
			}

			_, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, func(_ context.Context, checkpoint Checkpoint) error {
				if checkpoint.Stage == StageMerging && mergeFailed {
					return checkpointErr
				}
				return nil
			})
			var conflict *ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("Initialize() error = %#v, want typed conflict", err)
			}
			if test.inspectionErr != nil && !errors.Is(err, test.inspectionErr) {
				t.Fatalf("Initialize() error = %#v, want inspection error", err)
			}
			if len(conflict.Paths) != 0 && !reflect.DeepEqual(conflict.Paths, []string{"remote.md"}) {
				t.Fatalf("partial conflict paths = %#v", conflict.Paths)
			}
			if !errors.Is(err, checkpointErr) {
				t.Fatalf("Initialize() error = %#v, want checkpoint error", err)
			}
			assertNoServicePush(t, runner.commands)
		})
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

func TestInitializeTemplateChangeRejectsRewrittenRemoteHistory(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	first, err := fixture.initialize(fixture.options(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	trustedRef := managedRemoteRef("main")
	trustedOID := fixture.git(fixture.root, "rev-parse", "--verify", trustedRef)
	if trustedOID != first.RemoteOID {
		t.Fatalf("initial trusted ref = %q, want %q", trustedOID, first.RemoteOID)
	}
	rewriteRemoteUnrelated(fixture)
	options := fixture.options()
	options.Operation.ID = "2123456789abcdef0123456789abcdef"
	options.Operation.ConfigFingerprint = "template-and-autosync-changed"
	options.Snapshot.Fingerprint = "template-and-autosync-changed"
	options.Snapshot.CommitTemplate = "changed template"
	options.Snapshot.AutoSync = true
	options.LastRemoteOID = trustedOID
	runner := &interceptRunner{delegate: NewCommandRunner()}

	_, err = initializeWithRunner(t, fixture, runner, options, nil, nil)
	assertSafeCode(t, err, CodeRemoteHistoryRewritten)
	if got := fixture.git(fixture.root, "rev-parse", "--verify", trustedRef); got != trustedOID {
		t.Fatalf("trusted ref changed from %q to %q after rewrite", trustedOID, got)
	}
	assertNoServicePush(t, runner.commands)
}

func rewriteRemoteUnrelated(fixture *connectFixture) string {
	fixture.t.Helper()
	rewrite := filepath.Join(filepath.Dir(fixture.root), "rewrite")
	if err := os.Mkdir(rewrite, 0o700); err != nil {
		fixture.t.Fatal(err)
	}
	fixture.git(rewrite, "init", "--initial-branch", "main")
	fixture.git(rewrite, "config", "user.name", "IGoNotes Test")
	fixture.git(rewrite, "config", "user.email", "igonotes@example.invalid")
	if err := os.WriteFile(filepath.Join(rewrite, "rewritten.md"), []byte("rewritten\n"), 0o600); err != nil {
		fixture.t.Fatal(err)
	}
	fixture.git(rewrite, "add", "--all", "--", ".")
	fixture.git(rewrite, "commit", "-m", "unrelated rewrite")
	fixture.git(rewrite, "remote", "add", "origin", fixture.remote)
	fixture.git(rewrite, "push", "--force", "origin", "HEAD:refs/heads/main")
	return fixture.git(rewrite, "rev-parse", "--verify", "HEAD^{commit}")
}

func TestInitializeRebaselinesManagedRefAfterConfirmedRemoteChange(t *testing.T) {
	fixture := newConnectFixture(t)
	remoteOID := fixture.seedRemote()
	localOID := fixture.initLocal("local")
	managed := "refs/igonotes/remotes/main"
	fixture.git(fixture.root, "update-ref", managed, localOID, strings.Repeat("0", 40))
	oldRemote := filepath.Join(filepath.Dir(fixture.root), "old-remote.git")
	fixture.git(filepath.Dir(fixture.root), "init", "--bare", oldRemote)
	fixture.git(fixture.root, "remote", "add", "origin", oldRemote)
	options := fixture.options()
	options.Operation.RemoteFingerprint = "new-remote"
	options.Snapshot.RemoteFingerprint = "new-remote"
	options.LastRemoteOID = ""
	runner := &interceptRunner{delegate: NewCommandRunner()}
	result, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertAncestor(t, fixture, remoteOID, result.PushOID)
	if got := fixture.git(fixture.root, "rev-parse", managed); got != result.PushOID {
		t.Fatalf("managed ref = %q, want pushed OID %q", got, result.PushOID)
	}
	if !hasExactCommand(runner.commands, Command{
		Dir: fixture.root, Args: []string{"remote", "set-url", "origin", fixture.remote}, Scope: LocalOperation,
		ReadOnly: false, Secrets: []string{fixture.remote},
	}) {
		t.Fatal("confirmed URL change did not use exact set-url command")
	}
	if !hasExactCommand(runner.commands, Command{
		Dir: fixture.root, Args: []string{"update-ref", managed, remoteOID, localOID}, Scope: LocalOperation,
		ReadOnly: false,
	}) {
		t.Fatalf("rebaseline did not CAS managed ref from old OID %q to candidate %q", localOID, remoteOID)
	}
}

func hasExactCommand(commands []Command, want Command) bool {
	for _, command := range commands {
		if reflect.DeepEqual(command, want) {
			return true
		}
	}
	return false
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

func TestInitializeRetryRecoversCompletedCheckpointGap(t *testing.T) {
	fixture, options, candidate, pushOID, initialCommands := prepareCompletedCheckpointGap(t)
	commitCount := fixture.git(fixture.root, "rev-list", "--count", "HEAD")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	completed := false
	result, err := initializeWithRunner(t, fixture, runner, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		if checkpoint.Stage == StageCompleted {
			completed = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if !completed || result.PushOID != pushOID || result.RemoteOID != pushOID || fixture.remoteOID() != pushOID {
		t.Fatalf("retry result = %#v completed=%v remote=%q, want accepted push %q", result, completed, fixture.remoteOID(), pushOID)
	}
	if got := fixture.git(fixture.root, "rev-list", "--count", "HEAD"); got != commitCount {
		t.Fatalf("commit count after retry = %s, want unchanged %s", got, commitCount)
	}
	if got := fixture.git(fixture.root, "rev-parse", "--verify", managedRemoteRef("main")); got != pushOID {
		t.Fatalf("managed ref = %q, want pushed OID %q", got, pushOID)
	}
	if options.Operation.Stage != StagePushing || options.Operation.CandidateOID != candidate || options.Operation.PushOID != pushOID {
		t.Fatalf("durable gap operation = %#v", options.Operation)
	}
	assertNoServicePush(t, runner.commands)
	pushes := 0
	for _, command := range initialCommands {
		if len(command.Args) != 0 && command.Args[0] == "push" {
			pushes++
		}
	}
	if pushes != 1 {
		t.Fatalf("initial push count = %d, want 1", pushes)
	}
}

func TestInitializeCompletedCheckpointGapMismatchesFailClosed(t *testing.T) {
	t.Run("managed ref mismatch", func(t *testing.T) {
		fixture, options, _, pushOID, _ := prepareCompletedCheckpointGap(t)
		fixture.git(fixture.root, "update-ref", managedRemoteRef("main"), pushOID+"^{tree}", pushOID)
		runner := &interceptRunner{delegate: NewCommandRunner()}
		_, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
		assertSafeCode(t, err, CodeRemoteHistoryRewritten)
		assertNoServicePush(t, runner.commands)
	})

	t.Run("HEAD mismatch", func(t *testing.T) {
		fixture, options, _, _, _ := prepareCompletedCheckpointGap(t)
		fixture.write("post-gap-drift.md", "drift\n")
		fixture.git(fixture.root, "add", "--all", "--", ".")
		fixture.git(fixture.root, "commit", "-m", "post-gap drift")
		runner := &interceptRunner{delegate: NewCommandRunner()}
		_, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
		assertSafeCode(t, err, CodeRemoteHistoryRewritten)
		assertNoServicePush(t, runner.commands)
	})

	t.Run("remote mismatch", func(t *testing.T) {
		fixture, options, candidate, pushOID, _ := prepareCompletedCheckpointGap(t)
		fixture.git(filepath.Dir(fixture.root), "--git-dir", fixture.remote, "update-ref", "refs/heads/main", candidate, pushOID)
		runner := &interceptRunner{delegate: NewCommandRunner()}
		_, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
		assertSafeCode(t, err, CodeOperationInterrupted)
		assertExactCommand(t, runner.commands, networkCommand(fixture.root, true, fixture.remote,
			"ls-remote", "--symref", fixture.remote))
		assertNoServicePush(t, runner.commands)
	})
}

func prepareCompletedCheckpointGap(t *testing.T) (*connectFixture, InitializeOptions, string, string, []Command) {
	t.Helper()
	fixture := newConnectFixture(t)
	candidate := fixture.seedRemote()
	fixture.initLocal("local")
	fixture.git(fixture.root, "fetch", fixture.remote, "refs/heads/main")
	fixture.git(fixture.root, "update-ref", managedRemoteRef("main"), candidate, strings.Repeat("0", 40))
	options := fixture.options()
	options.LastRemoteOID = candidate
	completedErr := errors.New("final completed checkpoint unavailable")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	_, err := initializeWithRunner(t, fixture, runner, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		if checkpoint.Stage == StageCompleted {
			return completedErr
		}
		applyCheckpoint(&options.Operation, checkpoint)
		return nil
	})
	if !errors.Is(err, completedErr) {
		t.Fatalf("first Initialize() error = %v, want final completed checkpoint failure", err)
	}
	pushOID := options.Operation.PushOID
	if options.Operation.Stage != StagePushing || options.Operation.CandidateOID != candidate ||
		options.Operation.RemoteOID != candidate || pushOID == "" || pushOID == candidate {
		t.Fatalf("durable operation = %#v, want B=%q and distinct C", options.Operation, candidate)
	}
	if got := fixture.git(fixture.root, "rev-parse", "--verify", managedRemoteRef("main")); got != pushOID {
		t.Fatalf("managed ref after CAS = %q, want C=%q", got, pushOID)
	}
	if got := fixture.git(fixture.root, "rev-parse", "--verify", "HEAD^{commit}"); got != pushOID {
		t.Fatalf("HEAD after CAS = %q, want C=%q", got, pushOID)
	}
	if fixture.remoteOID() != pushOID {
		t.Fatalf("remote after confirmed push = %q, want C=%q", fixture.remoteOID(), pushOID)
	}
	return fixture, options, candidate, pushOID, append([]Command(nil), runner.commands...)
}

func TestInitializeRechecksRemoteBeforePush(t *testing.T) {
	fixture := newConnectFixture(t)
	original := fixture.seedRemote()
	changed := ""
	transactions := 0
	transaction := func(ctx context.Context, mutate func(string) error) error {
		transactions++
		if err := mutate(fixture.root); err != nil {
			return err
		}
		if transactions != 2 {
			return nil
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
	runner := &interceptRunner{delegate: NewCommandRunner()}
	_, err := initializeWithRunner(t, fixture, runner, fixture.options(), transaction, nil)
	assertSafeCode(t, err, CodeOperationInterrupted)
	if changed == "" || changed == original || fixture.remoteOID() != changed {
		t.Fatalf("remote race = %q from %q, want changed selected OID", changed, original)
	}
	assertNoServicePush(t, runner.commands)
	fetches := 0
	for _, command := range runner.commands {
		if len(command.Args) != 0 && command.Args[0] == "fetch" {
			fetches++
		}
	}
	if fetches != 1 {
		t.Fatalf("fetch count = %d, want one attempt without retry", fetches)
	}
	if got := fixture.git(fixture.root, "rev-parse", "--verify", managedRemoteRef("main")); got != original {
		t.Fatalf("trusted ref = %q, want unchanged fetched OID %q", got, original)
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

func TestInitializeRetryAfterUnbornCheckoutBeforeBranchRef(t *testing.T) {
	fixture, options, candidate := prepareUnbornPopulationGap(t, "checkout")
	assertUnbornPopulationRetry(t, fixture, options, candidate, true)
}

func TestInitializeRetryAfterTrustedCandidateCheckpointBeforeCheckout(t *testing.T) {
	fixture, options, candidate := preparePreCheckoutPopulationGap(t, "after-trust")
	excludes := filepath.Join(filepath.Dir(fixture.root), "home", "recovery-excludes")
	if err := os.WriteFile(excludes, []byte("ignored-recovery.tmp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(filepath.Dir(fixture.root), "config", "--global", "core.excludesFile", excludes)
	fixture.write("ignored-recovery.tmp", "preserve ignored\n")
	assertPreCheckoutPopulationRetry(t, fixture, options, candidate)
	contents, err := os.ReadFile(filepath.Join(fixture.root, "ignored-recovery.tmp"))
	if err != nil || string(contents) != "preserve ignored\n" {
		t.Fatalf("ignored recovery file changed: contents=%q error=%v", contents, err)
	}
}

func TestInitializeRetryAfterPreCheckoutCheckpointBeforeCheckout(t *testing.T) {
	fixture, options, candidate := preparePreCheckoutPopulationGap(t, "before-checkout")
	assertPreCheckoutPopulationRetry(t, fixture, options, candidate)
}

func TestInitializeRetryAfterUnbornBranchRefBeforeCheckpoint(t *testing.T) {
	fixture, options, candidate := prepareUnbornPopulationGap(t, "branch-ref")
	assertUnbornPopulationRetry(t, fixture, options, candidate, false)
}

func TestInitializeUnbornRecoveryFailsClosedOnMismatch(t *testing.T) {
	t.Run("private candidate mismatch", func(t *testing.T) {
		fixture, options, candidate := prepareUnbornPopulationGap(t, "checkout")
		privateRef := "refs/igonotes/fetch/" + options.Operation.ID
		fixture.git(fixture.root, "update-ref", privateRef, candidate+"^{tree}", candidate)
		runner := &interceptRunner{delegate: NewCommandRunner()}
		_, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
		assertSafeCode(t, err, CodeRemoteHistoryRewritten)
		assertNoServicePush(t, runner.commands)
	})

	t.Run("worktree differs from candidate", func(t *testing.T) {
		fixture, options, _ := prepareUnbornPopulationGap(t, "checkout")
		fixture.write("remote.md", "local drift preserved\n")
		runner := &interceptRunner{delegate: NewCommandRunner()}
		_, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
		assertSafeCode(t, err, CodeOperationInterrupted)
		contents, readErr := os.ReadFile(filepath.Join(fixture.root, "remote.md"))
		if readErr != nil || string(contents) != "local drift preserved\n" {
			t.Fatalf("local drift changed: contents=%q error=%v", contents, readErr)
		}
		for _, command := range runner.commands {
			if len(command.Args) != 0 && (command.Args[0] == "commit" || command.Args[0] == "merge") {
				t.Fatalf("mismatched recovery mutated history: %q", command.Args)
			}
		}
		assertNoServicePush(t, runner.commands)
	})

	t.Run("clean unborn gained untracked file", func(t *testing.T) {
		fixture, options, _ := preparePreCheckoutPopulationGap(t, "after-trust")
		fixture.write("local-untracked.md", "preserve me\n")
		runner := &interceptRunner{delegate: NewCommandRunner()}
		_, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
		assertSafeCode(t, err, CodeOperationInterrupted)
		contents, readErr := os.ReadFile(filepath.Join(fixture.root, "local-untracked.md"))
		if readErr != nil || string(contents) != "preserve me\n" {
			t.Fatalf("untracked file changed: contents=%q error=%v", contents, readErr)
		}
		for _, command := range runner.commands {
			if len(command.Args) != 0 && (command.Args[0] == "add" || command.Args[0] == "commit" || command.Args[0] == "checkout") {
				t.Fatalf("untracked mismatch triggered mutation: %q", command.Args)
			}
		}
		assertNoServicePush(t, runner.commands)
	})
}

func preparePreCheckoutPopulationGap(t *testing.T, crashPoint string) (*connectFixture, InitializeOptions, string) {
	t.Helper()
	fixture := newConnectFixture(t)
	candidate := fixture.seedRemote()
	options := fixture.options()
	interrupted := errors.New("simulated pre-checkout interruption")
	trustedPublished := false
	runner := &interceptRunner{delegate: NewCommandRunner()}
	runner.before = func(command Command) error {
		if crashPoint == "after-trust" && trustedPublished && reflect.DeepEqual(command.Args,
			[]string{"symbolic-ref", "--quiet", "--short", "HEAD"}) {
			return interrupted
		}
		if crashPoint == "before-checkout" && len(command.Args) != 0 && command.Args[0] == "checkout" {
			return interrupted
		}
		return nil
	}
	_, err := initializeWithRunner(t, fixture, runner, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		applyCheckpoint(&options.Operation, checkpoint)
		if checkpoint.Stage == StageFetching && checkpoint.CandidateOID == candidate && checkpoint.RemoteOID == candidate {
			trustedPublished = true
		}
		return nil
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("first Initialize() error = %v, want interruption at %s", err, crashPoint)
	}
	wantStage := StageFetching
	if crashPoint == "before-checkout" {
		wantStage = StageSwitching
	}
	if options.Operation.Stage != wantStage || options.Operation.CandidateOID != candidate ||
		options.Operation.RemoteOID != candidate || options.Operation.LocalOID != "" ||
		options.Operation.BackupRef != "" || options.Operation.PushOID != "" {
		t.Fatalf("durable operation at %s gap = %#v", crashPoint, options.Operation)
	}
	if index := fixture.git(fixture.root, "ls-files", "--cached", "-z"); index != "" {
		t.Fatalf("index at %s gap = %q, want empty", crashPoint, index)
	}
	if untracked := fixture.git(fixture.root, "ls-files", "--others", "--exclude-standard", "-z"); untracked != "" {
		t.Fatalf("untracked files at %s gap = %q, want empty", crashPoint, untracked)
	}
	privateOID := fixture.git(fixture.root, "rev-parse", "--verify", "refs/igonotes/fetch/"+options.Operation.ID)
	managedOID := fixture.git(fixture.root, "rev-parse", "--verify", managedRemoteRef("main"))
	if privateOID != candidate || managedOID != candidate {
		t.Fatalf("candidate refs at %s gap: checkpoint=%q private=%q managed=%q", crashPoint, candidate, privateOID, managedOID)
	}
	assertNoServicePush(t, runner.commands)
	return fixture, options, candidate
}

func assertPreCheckoutPopulationRetry(t *testing.T, fixture *connectFixture, options InitializeOptions, candidate string) {
	t.Helper()
	remoteCount := fixture.git(filepath.Dir(fixture.root), "--git-dir", fixture.remote, "rev-list", "--count", "refs/heads/main")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	result, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if result.HeadOID != candidate || result.RemoteOID != candidate || result.PushOID != candidate || result.BackupRef != "" {
		t.Fatalf("retry result = %#v, want exact candidate %q without backup", result, candidate)
	}
	if got := fixture.git(fixture.root, "rev-parse", "--verify", "HEAD^{commit}"); got != candidate {
		t.Fatalf("HEAD = %q, want candidate %q", got, candidate)
	}
	if count := fixture.git(filepath.Dir(fixture.root), "--git-dir", fixture.remote, "rev-list", "--count", "refs/heads/main"); count != remoteCount {
		t.Fatalf("remote commit count = %s, want unchanged %s", count, remoteCount)
	}
	if refs := fixture.git(fixture.root, "for-each-ref", "--format=%(refname)", "refs/igonotes/backups"); refs != "" {
		t.Fatalf("unexpected backup refs after pre-checkout recovery: %q", refs)
	}
	assertExactCommand(t, runner.commands, localCommand(fixture.root, false, "checkout", candidate, "--", "."))
	assertExactCommand(t, runner.commands, localCommand(fixture.root, false,
		"update-ref", "refs/heads/main", candidate, strings.Repeat("0", 40)))
	for _, command := range runner.commands {
		if len(command.Args) == 0 {
			continue
		}
		if command.Args[0] == "add" || command.Args[0] == "commit" || command.Args[0] == "merge" ||
			(command.Args[0] == "update-ref" && containsArg(command.Args, "--create-reflog")) {
			t.Fatalf("pre-checkout retry misclassified remote population: %q", command.Args)
		}
	}
	assertExactPush(t, runner.commands, fixture, candidate)
}

func prepareUnbornPopulationGap(t *testing.T, crashPoint string) (*connectFixture, InitializeOptions, string) {
	t.Helper()
	fixture := newConnectFixture(t)
	candidate := fixture.seedRemote()
	options := fixture.options()
	interrupted := errors.New("simulated unborn population interruption")
	branchRefCreated := false
	runner := &interceptRunner{delegate: NewCommandRunner()}
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if err != nil {
			return result, err
		}
		if crashPoint == "checkout" && len(command.Args) != 0 && command.Args[0] == "checkout" {
			return Result{}, interrupted
		}
		if crashPoint == "branch-ref" && isSelectedBranchRefUpdate(command, "main") {
			branchRefCreated = true
		}
		return result, nil
	}
	_, err := initializeWithRunner(t, fixture, runner, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		if branchRefCreated {
			return interrupted
		}
		applyCheckpoint(&options.Operation, checkpoint)
		return nil
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("first Initialize() error = %v, want interruption at %s", err, crashPoint)
	}
	if options.Operation.Stage != StageSwitching || options.Operation.CandidateOID != candidate ||
		options.Operation.RemoteOID != candidate || options.Operation.LocalOID != "" || options.Operation.BackupRef != "" {
		t.Fatalf("durable operation at %s gap = %#v", crashPoint, options.Operation)
	}
	privateOID := fixture.git(fixture.root, "rev-parse", "--verify", "refs/igonotes/fetch/"+options.Operation.ID)
	managedOID := fixture.git(fixture.root, "rev-parse", "--verify", managedRemoteRef("main"))
	if privateOID != candidate || managedOID != candidate {
		t.Fatalf("candidate refs at %s gap: checkpoint=%q private=%q managed=%q", crashPoint, candidate, privateOID, managedOID)
	}
	assertNoServicePush(t, runner.commands)
	return fixture, options, candidate
}

func assertUnbornPopulationRetry(
	t *testing.T,
	fixture *connectFixture,
	options InitializeOptions,
	candidate string,
	wantBranchUpdate bool,
) {
	t.Helper()
	remoteCount := fixture.git(filepath.Dir(fixture.root), "--git-dir", fixture.remote, "rev-list", "--count", "refs/heads/main")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	lastStage := Stage("")
	branchUpdateRan := false
	checkpointBeforeBranchUpdate := false
	checkpointAfterBranchUpdate := false
	runner.before = func(command Command) error {
		if isSelectedBranchRefUpdate(command, "main") {
			branchUpdateRan = true
			checkpointBeforeBranchUpdate = lastStage == StageSwitching
		}
		return nil
	}
	result, err := initializeWithRunner(t, fixture, runner, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		lastStage = checkpoint.Stage
		if branchUpdateRan && checkpoint.Stage == StageSwitching {
			checkpointAfterBranchUpdate = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if result.HeadOID != candidate || result.RemoteOID != candidate || result.PushOID != candidate || result.BackupRef != "" {
		t.Fatalf("retry result = %#v, want exact candidate %q without backup", result, candidate)
	}
	if got := fixture.git(fixture.root, "rev-parse", "--verify", "HEAD^{commit}"); got != candidate {
		t.Fatalf("HEAD = %q, want candidate %q", got, candidate)
	}
	if got := fixture.git(filepath.Dir(fixture.root), "--git-dir", fixture.remote, "rev-list", "--count", "refs/heads/main"); got != remoteCount {
		t.Fatalf("remote commit count = %s, want unchanged %s", got, remoteCount)
	}
	if refs := fixture.git(fixture.root, "for-each-ref", "--format=%(refname)", "refs/igonotes/backups"); refs != "" {
		t.Fatalf("unexpected backup refs after recovery: %q", refs)
	}
	assertExactCommand(t, runner.commands, localCommand(fixture.root, true,
		"diff", "--cached", "--quiet", "--exit-code", candidate, "--"))
	assertExactCommand(t, runner.commands, localCommand(fixture.root, true,
		"diff", "--quiet", "--exit-code", candidate, "--"))
	assertExactCommand(t, runner.commands, localCommand(fixture.root, true,
		"ls-files", "--others", "--exclude-standard", "-z"))
	branchUpdates := 0
	for _, command := range runner.commands {
		if len(command.Args) == 0 {
			continue
		}
		if command.Args[0] == "add" || command.Args[0] == "commit" || command.Args[0] == "merge" || command.Args[0] == "checkout" ||
			(command.Args[0] == "update-ref" && containsArg(command.Args, "--create-reflog")) {
			t.Fatalf("retry misclassified remote population: %q", command.Args)
		}
		if isSelectedBranchRefUpdate(command, "main") {
			branchUpdates++
		}
	}
	wantUpdates := 0
	if wantBranchUpdate {
		wantUpdates = 1
	}
	if branchUpdates != wantUpdates {
		t.Fatalf("branch update count = %d, want %d", branchUpdates, wantUpdates)
	}
	if wantBranchUpdate && (!checkpointBeforeBranchUpdate || !checkpointAfterBranchUpdate) {
		t.Fatalf("branch update checkpoints: before=%v after=%v", checkpointBeforeBranchUpdate, checkpointAfterBranchUpdate)
	}
	assertExactPush(t, runner.commands, fixture, candidate)
}

func TestInitializeRetryRecoversTrustedRefWhenCheckpointLags(t *testing.T) {
	fixture, options := prepareTrustedRemoteAdvance(t)
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
	candidate := options.Operation.CandidateOID
	privateRef := "refs/igonotes/fetch/" + options.Operation.ID
	managedRef := managedRemoteRef("main")
	privateOID := fixture.git(fixture.root, "rev-parse", "--verify", privateRef)
	managedOID := fixture.git(fixture.root, "rev-parse", "--verify", managedRef)
	if candidate == "" || candidate != privateOID || candidate != managedOID {
		t.Fatalf("crash-gap refs differ: checkpoint=%q private=%q managed=%q", candidate, privateOID, managedOID)
	}
	runner := &interceptRunner{delegate: NewCommandRunner()}
	result, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if !hasExactCommand(runner.commands, Command{
		Dir: fixture.root, Args: []string{"update-ref", managedRef, result.PushOID, candidate}, Scope: LocalOperation,
		ReadOnly: false,
	}) {
		t.Fatalf("retry did not use candidate as expected-old CAS: candidate=%q push=%q", candidate, result.PushOID)
	}
}

func TestInitializeRetryRejectsTrustedRefCrashGapMismatch(t *testing.T) {
	fixture, options := prepareTrustedRemoteAdvance(t)
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
	candidate := options.Operation.CandidateOID
	privateRef := "refs/igonotes/fetch/" + options.Operation.ID
	managedRef := managedRemoteRef("main")
	if got := fixture.git(fixture.root, "rev-parse", "--verify", privateRef); got != candidate {
		t.Fatalf("private ref = %q, want candidate %q", got, candidate)
	}
	if got := fixture.git(fixture.root, "rev-parse", "--verify", managedRef); got != candidate {
		t.Fatalf("managed ref = %q, want candidate %q", got, candidate)
	}
	fixture.git(fixture.root, "update-ref", privateRef, candidate+"^{tree}", candidate)
	runner := &interceptRunner{delegate: NewCommandRunner()}
	_, err = initializeWithRunner(t, fixture, runner, options, nil, nil)
	assertSafeCode(t, err, CodeRemoteHistoryRewritten)
	if got := fixture.git(fixture.root, "rev-parse", "--verify", managedRef); got != candidate {
		t.Fatalf("managed ref advanced after mismatch: got %q want %q", got, candidate)
	}
	assertNoServicePush(t, runner.commands)
}

func prepareTrustedRemoteAdvance(t *testing.T) (*connectFixture, InitializeOptions) {
	t.Helper()
	fixture := newConnectFixture(t)
	trustedOID := fixture.seedRemote()
	fixture.initLocal("local")
	fixture.git(fixture.root, "fetch", fixture.remote, "refs/heads/main")
	fixture.git(fixture.root, "update-ref", managedRemoteRef("main"), trustedOID, strings.Repeat("0", 40))
	seed := filepath.Join(filepath.Dir(fixture.root), "seed")
	if err := os.WriteFile(filepath.Join(seed, "advanced-for-crash.md"), []byte("advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(seed, "add", "--all", "--", ".")
	fixture.git(seed, "commit", "-m", "advance for crash gap")
	fixture.git(seed, "push", "origin", "HEAD:main")
	options := fixture.options()
	options.LastRemoteOID = trustedOID
	return fixture, options
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

func TestInitializePostPushTrustUpdateRechecksLocalSafety(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutate   func(*connectFixture) error
		wantCode ErrorCode
	}{
		{
			name: "repository replaced by parent discovery",
			mutate: func(fixture *connectFixture) error {
				if err := os.Rename(filepath.Join(fixture.root, ".git"), filepath.Join(fixture.root, ".git.pushed")); err != nil {
					return err
				}
				fixture.git(filepath.Dir(fixture.root), "init", "--initial-branch", "parent")
				return nil
			},
			wantCode: CodeRepositoryRoot,
		},
		{
			name: "pending operation appeared",
			mutate: func(fixture *connectFixture) error {
				head := fixture.git(fixture.root, "rev-parse", "--verify", "HEAD^{commit}")
				return os.WriteFile(filepath.Join(fixture.root, ".git", "MERGE_HEAD"), []byte(head+"\n"), 0o600)
			},
			wantCode: CodeRepositoryLocked,
		},
		{
			name: "repository lock appeared",
			mutate: func(fixture *connectFixture) error {
				return os.WriteFile(filepath.Join(fixture.root, ".git", "index.lock"), []byte("locked\n"), 0o600)
			},
			wantCode: CodeRepositoryLocked,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConnectFixture(t)
			runner := &interceptRunner{delegate: NewCommandRunner()}
			pushed := false
			runner.after = func(command Command, result Result, err error) (Result, error) {
				if err == nil && len(command.Args) != 0 && command.Args[0] == "push" {
					pushed = true
				}
				return result, err
			}
			transactions := 0
			transaction := func(_ context.Context, mutate func(string) error) error {
				transactions++
				if transactions == 3 {
					if !pushed {
						t.Fatal("post-push transaction started before push")
					}
					if err := test.mutate(fixture); err != nil {
						return err
					}
				}
				return mutate(fixture.root)
			}
			var pushedCheckpoint string
			_, err := initializeWithRunner(t, fixture, runner, fixture.options(), transaction, func(_ context.Context, checkpoint Checkpoint) error {
				if checkpoint.Stage == StagePushing && checkpoint.PushOID != "" {
					pushedCheckpoint = checkpoint.PushOID
				}
				return nil
			})
			var safeErr *SafeError
			if !errors.As(err, &safeErr) || safeErr.Code != test.wantCode {
				t.Fatalf("Initialize() error = %#v, want code %q", err, test.wantCode)
			}
			if !pushed || pushedCheckpoint == "" || pushedCheckpoint != fixture.remoteOID() {
				t.Fatalf("confirmed push was not checkpointed: pushed=%v checkpoint=%q remote=%q", pushed, pushedCheckpoint, fixture.remoteOID())
			}
			for _, command := range runner.commands {
				if isManagedRefUpdate(command, "main") {
					t.Fatalf("managed ref update ran after unsafe local change: %q", command.Args)
				}
			}
		})
	}
}

func isManagedRefUpdate(command Command, branch string) bool {
	return len(command.Args) >= 2 && command.Args[0] == "update-ref" && command.Args[1] == managedRemoteRef(branch)
}

func TestInitializeRequiresActualConsequenceConfirmations(t *testing.T) {
	t.Run("create repository", func(t *testing.T) {
		fixture := newConnectFixture(t)
		options := fixture.options()
		options.Confirmations.CreateRepository = false
		runner := &interceptRunner{delegate: NewCommandRunner()}
		_, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
		assertSafeCode(t, err, CodeConfirmationRequired)
		for _, command := range runner.commands {
			if command.Scope == LocalOperation && !command.ReadOnly {
				t.Fatalf("local mutation ran without repository confirmation: %q", command.Args)
			}
		}
	})

	t.Run("create branch on empty remote", func(t *testing.T) {
		fixture := newConnectFixture(t)
		options := fixture.options()
		options.Confirmations.CreateBranch = false
		runner := &interceptRunner{delegate: NewCommandRunner()}
		_, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
		assertSafeCode(t, err, CodeConfirmationRequired)
		for _, command := range runner.commands {
			if len(command.Args) == 0 {
				continue
			}
			if command.Args[0] == "commit" || command.Args[0] == "push" ||
				(command.Args[0] == "switch" && containsArg(command.Args, "-c")) ||
				isSelectedBranchRefUpdate(command, "main") {
				t.Fatalf("branch-creation consequence ran without confirmation: %q", command.Args)
			}
		}
	})

	t.Run("merge unrelated histories", func(t *testing.T) {
		fixture := newConnectFixture(t)
		fixture.seedRemote()
		fixture.write("local.md", "local\n")
		options := fixture.options()
		options.Confirmations.MergeHistories = false
		runner := &interceptRunner{delegate: NewCommandRunner()}
		_, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
		assertSafeCode(t, err, CodeConfirmationRequired)
		for _, command := range runner.commands {
			if len(command.Args) != 0 && command.Args[0] == "merge" && containsArg(command.Args, "--allow-unrelated-histories") {
				t.Fatalf("unrelated merge ran without confirmation: %q", command.Args)
			}
		}
		assertNoServicePush(t, runner.commands)
	})
}

func assertSafeCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != code {
		t.Fatalf("error = %#v, want SafeError code %q", err, code)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func isSelectedBranchRefUpdate(command Command, branch string) bool {
	return len(command.Args) >= 2 && command.Args[0] == "update-ref" && command.Args[1] == "refs/heads/"+branch
}

func TestInitializeExactCommandContract(t *testing.T) {
	t.Run("init commit origin inspections push and CAS", func(t *testing.T) {
		fixture := newConnectFixture(t)
		fixture.write("note.md", "note\n")
		runner := &interceptRunner{delegate: NewCommandRunner()}
		result, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		commands := runner.commands
		assertExactCommand(t, commands, localCommand(fixture.root, false, "init", "--initial-branch", "main"))
		assertExactCommand(t, commands, localSecretCommand(fixture.root, false, fixture.remote, "remote", "add", "origin", fixture.remote))
		assertExactCommand(t, commands, localCommand(fixture.root, true, "rev-parse", "--show-toplevel"))
		assertExactCommand(t, commands, localCommand(fixture.root, true, "rev-parse", "--verify", "HEAD^{commit}"))
		assertExactCommand(t, commands, localCommand(fixture.root, true, "symbolic-ref", "--quiet", "--short", "HEAD"))
		assertExactCommand(t, commands, localCommand(fixture.root, true, "remote", "get-url", "--all", "origin"))
		assertExactCommand(t, commands, networkCommand(fixture.root, true, fixture.remote, "ls-remote", "--symref", fixture.remote))
		assertExactCommand(t, commands, networkCommand(fixture.root, true, fixture.remote, "ls-remote", "--symref", "origin"))
		assertExactCommand(t, commands, localCommand(fixture.root, false, "add", "--all", "--", "."))
		assertExactCommand(t, commands, localCommand(fixture.root, true, "diff", "--cached", "--quiet", "--exit-code"))
		assertExactCommand(t, commands, localCommand(fixture.root, true, "diff", "--cached", "--name-only", "-z"))
		assertExactCommand(t, commands, localCommand(fixture.root, false, "commit", "-m", initialCommitMessage))
		assertExactCommand(t, commands, localCommand(fixture.root, false, "update-ref", managedRemoteRef("main"), result.PushOID, strings.Repeat("0", 40)))
		assertExactPush(t, commands, fixture, result.PushOID)
		assertNoForbiddenServiceCommands(t, commands)
	})

	t.Run("empty bootstrap commit", func(t *testing.T) {
		fixture := newConnectFixture(t)
		runner := &interceptRunner{delegate: NewCommandRunner()}
		if _, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil); err != nil {
			t.Fatal(err)
		}
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false, "commit", "--allow-empty", "-m", initialCommitMessage))
		assertNoForbiddenServiceCommands(t, runner.commands)
	})

	t.Run("fetch and unborn checkout", func(t *testing.T) {
		fixture := newConnectFixture(t)
		remoteOID := fixture.seedRemote()
		runner := &interceptRunner{delegate: NewCommandRunner()}
		result, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		privateRef := "refs/igonotes/fetch/" + testOperationID
		assertExactCommand(t, runner.commands, networkCommand(fixture.root, false, fixture.remote,
			"fetch", "--no-tags", "--show-forced-updates", "origin", "+refs/heads/main:"+privateRef))
		assertExactCommand(t, runner.commands, localCommand(fixture.root, true, "rev-parse", "--verify", privateRef+"^{commit}"))
		assertExactCommand(t, runner.commands, localCommand(fixture.root, true, "rev-parse", "--verify", managedRemoteRef("main")))
		assertExactCommand(t, runner.commands, networkCommand(fixture.root, true, fixture.remote,
			"ls-remote", "--exit-code", "--heads", "origin", "refs/heads/main"))
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false, "checkout", remoteOID, "--", "."))
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false, "update-ref", "refs/heads/main", remoteOID, strings.Repeat("0", 40)))
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false, "update-ref", managedRemoteRef("main"), remoteOID, strings.Repeat("0", 40)))
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false, "update-ref", managedRemoteRef("main"), result.PushOID, remoteOID))
		assertExactPush(t, runner.commands, fixture, result.PushOID)
		assertNoForbiddenServiceCommands(t, runner.commands)
	})

	t.Run("backup create branch and unrelated merge", func(t *testing.T) {
		fixture := newConnectFixture(t)
		remoteOID := fixture.seedRemote()
		fixture.initLocal("local")
		runner := &interceptRunner{delegate: NewCommandRunner()}
		result, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		snapshotOID := fixture.git(fixture.root, "rev-parse", "--verify", result.BackupRef+"^{commit}")
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false,
			"update-ref", "--create-reflog", "-m", "IGoNotes initial-connect backup", result.BackupRef, snapshotOID, strings.Repeat("0", 40)))
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false, "switch", "-c", "main", remoteOID))
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false,
			"merge", "--no-edit", "-m", localMergeMessage, "--allow-unrelated-histories", snapshotOID))
		assertNoForbiddenServiceCommands(t, runner.commands)
	})

	t.Run("switch existing branch and merge remote", func(t *testing.T) {
		fixture := newConnectFixture(t)
		remoteOID := fixture.seedRemote()
		fixture.initLocal("main")
		fixture.git(fixture.root, "switch", "-c", "local")
		fixture.write("local-branch.md", "local branch\n")
		fixture.git(fixture.root, "add", "--all", "--", ".")
		fixture.git(fixture.root, "commit", "-m", "local branch")
		runner := &interceptRunner{delegate: NewCommandRunner()}
		result, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		snapshotOID := fixture.git(fixture.root, "rev-parse", "--verify", result.BackupRef+"^{commit}")
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false, "switch", "main"))
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false,
			"merge", "--no-edit", "-m", localMergeMessage, snapshotOID))
		assertExactCommand(t, runner.commands, localCommand(fixture.root, false,
			"merge", "--no-edit", "-m", "IGoNotes: merge origin/main", "--allow-unrelated-histories", remoteOID))
		assertExactPush(t, runner.commands, fixture, result.PushOID)
		assertNoForbiddenServiceCommands(t, runner.commands)
	})
}

func localCommand(dir string, readOnly bool, args ...string) Command {
	return Command{Dir: dir, Args: args, Scope: LocalOperation, ReadOnly: readOnly}
}

func localSecretCommand(dir string, readOnly bool, secret string, args ...string) Command {
	return Command{Dir: dir, Args: args, Scope: LocalOperation, ReadOnly: readOnly, Secrets: []string{secret}}
}

func networkCommand(dir string, readOnly bool, secret string, args ...string) Command {
	return Command{Dir: dir, Args: args, Scope: NetworkOperation, ReadOnly: readOnly, Secrets: []string{secret}}
}

func assertExactCommand(t *testing.T, commands []Command, want Command) {
	t.Helper()
	if !hasExactCommand(commands, want) {
		t.Fatalf("exact command not executed: %#v\nall commands: %#v", want, commands)
	}
}

func assertExactPush(t *testing.T, commands []Command, fixture *connectFixture, oid string) {
	t.Helper()
	if len(oid) != 40 {
		t.Fatalf("captured push OID = %q, want full SHA-1", oid)
	}
	assertExactCommand(t, commands, networkCommand(fixture.root, false, fixture.remote,
		"push", "--no-verify", "--porcelain", "origin", oid+":refs/heads/main"))
}

func assertNoForbiddenServiceCommands(t *testing.T, commands []Command) {
	t.Helper()
	for _, command := range commands {
		if len(command.Args) == 0 {
			continue
		}
		args := command.Args
		switch args[0] {
		case "reset", "rebase", "clean", "prune":
			t.Fatalf("forbidden service command executed: %q", args)
		case "merge":
			if containsArg(args, "--abort") {
				t.Fatalf("forbidden merge abort executed: %q", args)
			}
		case "branch":
			if containsArg(args, "-d") || containsArg(args, "-D") || containsArg(args, "--delete") {
				t.Fatalf("forbidden branch deletion executed: %q", args)
			}
		case "remote":
			if len(args) > 1 && args[1] == "prune" {
				t.Fatalf("forbidden remote prune executed: %q", args)
			}
		case "push":
			for _, arg := range args[1:] {
				if arg == "--force" || arg == "--force-with-lease" || arg == "--delete" ||
					strings.HasPrefix(arg, "+") || strings.HasPrefix(arg, ":refs/") {
					t.Fatalf("forbidden push argv executed: %q", args)
				}
			}
		}
		for _, arg := range args {
			if arg == "--prune" {
				t.Fatalf("forbidden prune argv executed: %q", args)
			}
			if !strings.HasPrefix(arg, "+") {
				continue
			}
			want := "+refs/heads/main:refs/igonotes/fetch/" + testOperationID
			if args[0] != "fetch" || arg != want {
				t.Fatalf("leading + outside private fetch refspec: %q", args)
			}
		}
		if args[0] == "update-ref" && (containsArg(args, "-d") || containsArg(args, "--delete")) {
			t.Fatalf("forbidden ref deletion executed: %q", args)
		}
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
		{name: "invalid 39-character OID", mutate: func(options *InitializeOptions) { options.Operation.CandidateOID = strings.Repeat("a", 39) }},
		{name: "invalid 64-character nonhex OID", mutate: func(options *InitializeOptions) { options.Operation.CandidateOID = strings.Repeat("a", 63) + "z" }},
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
