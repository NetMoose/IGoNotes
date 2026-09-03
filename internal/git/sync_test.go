package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const syncOperationID = "fedcba9876543210fedcba9876543210"

func syncOptions(fixture *connectFixture, remoteOID string) SyncOptions {
	return SyncOptions{
		Operation: Operation{
			ID:                syncOperationID,
			BaseName:          "notes",
			RepoPath:          fixture.root,
			ConfigFingerprint: "config-v1",
			RemoteFingerprint: "remote-v1",
			Kind:              OperationSync,
			State:             OperationRunning,
			Stage:             StageQueued,
			Branch:            "main",
			CreatedAt:         time.Date(2026, time.September, 3, 14, 5, 6, 0, time.FixedZone("fixture", 2*60*60)),
		},
		Snapshot: ConfiguredBase{
			Name:              "notes",
			Path:              fixture.root,
			URL:               fixture.remote,
			Branch:            "main",
			CommitTemplate:    "sync {{base}} {{branch}} {{date}} {{datetime}} {{count}}",
			Fingerprint:       "config-v1",
			RemoteFingerprint: "remote-v1",
		},
		LastRemoteOID: remoteOID,
	}
}

func runSync(
	t *testing.T,
	fixture *connectFixture,
	runner Runner,
	options SyncOptions,
	transaction WorktreeTransaction,
	progress Progress,
) (OperationResult, error) {
	t.Helper()
	if runner == nil {
		runner = NewCommandRunner()
	}
	if transaction == nil {
		transaction = func(_ context.Context, mutate func(string) error) error { return mutate(fixture.root) }
	}
	if progress == nil {
		progress = func(context.Context, Checkpoint) error { return nil }
	}
	service := NewService(runner, NewClient(runner))
	service.now = func() time.Time {
		return time.Date(2026, time.September, 3, 14, 5, 6, 0, time.FixedZone("fixture", 2*60*60))
	}
	return service.Sync(context.Background(), options, transaction, progress)
}

func preparedSyncFixture(t *testing.T) (*connectFixture, SyncOptions) {
	t.Helper()
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	connected, err := fixture.initialize(fixture.options(), nil, nil)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return fixture, syncOptions(fixture, connected.RemoteOID)
}

func servicePushes(commands []Command) []Command {
	var pushes []Command
	for _, command := range commands {
		if len(command.Args) > 0 && command.Args[0] == "push" {
			pushes = append(pushes, command)
		}
	}
	return pushes
}

func errorHasSafeCode(err error, code ErrorCode) bool {
	if safeErr, ok := err.(*SafeError); ok && safeErr.Code == code {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if errorHasSafeCode(child, code) {
				return true
			}
		}
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return errorHasSafeCode(wrapped.Unwrap(), code)
	}
	return false
}

func TestSyncRejectsManagedRefAndStatusMismatch(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	other := fixture.git(fixture.root, "commit-tree", fixture.git(fixture.root, "rev-parse", "HEAD^{tree}"), "-p", options.LastRemoteOID, "-m", "other")
	fixture.git(fixture.root, "update-ref", managedRemoteRef("main"), other, options.LastRemoteOID)
	fixture.write("local.md", "local\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}

	_, err := runSync(t, fixture, runner, options, nil, nil)
	assertSafeCode(t, err, CodeNeedsReconnect)
	for _, command := range runner.commands {
		if command.Scope == NetworkOperation {
			t.Fatalf("network command ran after trust mismatch: %#v", command)
		}
	}
}

func TestSyncRejectsManagedRefChangeBeforePush(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "local\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	transaction := func(_ context.Context, mutate func(string) error) error {
		if err := mutate(fixture.root); err != nil {
			return err
		}
		head := fixture.git(fixture.root, "rev-parse", "HEAD")
		other := fixture.git(fixture.root, "commit-tree", fixture.git(fixture.root, "rev-parse", "HEAD^{tree}"), "-p", head, "-m", "other trust")
		fixture.git(fixture.root, "update-ref", managedRemoteRef("main"), other, options.LastRemoteOID)
		return nil
	}
	_, err := runSync(t, fixture, runner, options, transaction, nil)
	assertSafeCode(t, err, CodeNeedsReconnect)
	if len(servicePushes(runner.commands)) != 0 {
		t.Fatal("push ran after managed trust changed")
	}
	if got := fixture.remoteOID(); got != options.LastRemoteOID {
		t.Fatalf("remote advanced to %q, want %q", got, options.LastRemoteOID)
	}
}

func TestSyncPushesCapturedExactOID(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "captured\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	var captured string
	result, err := runSync(t, fixture, runner, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		if checkpoint.PushOID != "" {
			captured = checkpoint.PushOID
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pushes := servicePushes(runner.commands)
	if len(pushes) != 1 || captured == "" || !reflect.DeepEqual(pushes[0].Args,
		[]string{"push", "--no-verify", "--porcelain", "origin", captured + ":refs/heads/main"}) {
		t.Fatalf("pushes = %#v, captured = %q", pushes, captured)
	}
	if result.PushOID != captured {
		t.Fatalf("result PushOID = %q, want %q", result.PushOID, captured)
	}
}

func TestSyncPushUsesNoVerify(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "push\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	if _, err := runSync(t, fixture, runner, options, nil, nil); err != nil {
		t.Fatal(err)
	}
	pushes := servicePushes(runner.commands)
	if len(pushes) != 1 || len(pushes[0].Args) < 3 || pushes[0].Args[1] != "--no-verify" {
		t.Fatalf("push commands = %#v", pushes)
	}
}

func TestSyncCommandsUseExactScopesSecretsAndSafeVerbs(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "commands\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	if _, err := runSync(t, fixture, runner, options, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if command.Scope == NetworkOperation {
			if !reflect.DeepEqual(command.Secrets, []string{fixture.remote}) {
				t.Fatalf("network command secrets = %#v for %q", command.Secrets, command.Args)
			}
			switch command.Args[0] {
			case "ls-remote":
				if !command.ReadOnly {
					t.Fatalf("ls-remote is mutable: %#v", command)
				}
			case "fetch", "push":
				if command.ReadOnly {
					t.Fatalf("network mutation is read-only: %#v", command)
				}
			default:
				t.Fatalf("unexpected network command: %#v", command)
			}
		}
		if len(command.Args) > 0 {
			switch command.Args[0] {
			case "reset", "clean", "checkout", "pull", "rebase", "stash":
				t.Fatalf("forbidden sync command: %#v", command)
			}
		}
	}
}

func TestSyncReportsAheadBehindFromCapturedRemoteOID(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "ahead\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if len(command.Args) > 0 && command.Args[0] == "push" {
			return Result{}, &SafeError{Code: CodeTimedOut, Message: "Git command timed out"}
		}
		return result, err
	}
	result, err := runSync(t, fixture, runner, options, nil, nil)
	assertSafeCode(t, err, CodeTimedOut)
	if result.Ahead != 1 || result.Behind != 0 {
		t.Fatalf("ahead/behind = %d/%d, want 1/0", result.Ahead, result.Behind)
	}
	for _, command := range runner.commands {
		if len(command.Args) > 0 && command.Args[0] == "rev-list" && strings.Contains(strings.Join(command.Args, " "), "origin/") {
			t.Fatalf("ahead/behind used mutable remote ref: %q", command.Args)
		}
	}
}

func TestSyncRejectsNonDecimalAheadBehind(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	runner := &interceptRunner{delegate: NewCommandRunner()}
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if len(command.Args) > 0 && command.Args[0] == "rev-list" {
			return Result{Stdout: "+0 0\n"}, nil
		}
		return result, err
	}
	_, err := runSync(t, fixture, runner, options, nil, nil)
	assertSafeCode(t, err, CodeCommandFailed)
	if len(servicePushes(runner.commands)) != 0 {
		t.Fatal("push ran after malformed ahead/behind output")
	}
}

func TestSyncStopsAfterSecondNonFastForwardPush(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "local\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	pushes := 0
	runner.before = func(command Command) error {
		if len(command.Args) > 0 && command.Args[0] == "push" {
			pushes++
			seedRemoteAdvance(t, fixture, "race.md", "race "+string(rune('0'+pushes))+"\n")
		}
		return nil
	}
	_, err := runSync(t, fixture, runner, options, nil, nil)
	assertSafeCode(t, err, CodePushRejected)
	if pushes != 2 {
		t.Fatalf("push count = %d, want 2", pushes)
	}
	fetches := 0
	for _, command := range runner.commands {
		if len(command.Args) > 0 && command.Args[0] == "fetch" {
			fetches++
		}
	}
	if fetches != 2 {
		t.Fatalf("fetch count = %d, want 2", fetches)
	}
}

func TestSyncRejectsMalformedDirectTemplate(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "local\n")
	options.Snapshot.CommitTemplate = "{{unsupported}}"
	runner := &interceptRunner{delegate: NewCommandRunner()}
	_, err := runSync(t, fixture, runner, options, nil, nil)
	assertSafeCode(t, err, CodeCommandFailed)
	if len(servicePushes(runner.commands)) != 0 {
		t.Fatal("malformed template reached push")
	}
}

func TestSyncConflictInspectionFailurePreservesTypedConflict(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("remote.md", "local conflict\n")
	seedRemoteAdvance(t, fixture, "remote.md", "remote conflict\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	inspectionErr := errors.New("inspection failed")
	mergeFailed := false
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if len(command.Args) > 0 && command.Args[0] == "merge" && err != nil {
			mergeFailed = true
		}
		if mergeFailed && reflect.DeepEqual(command.Args, []string{"ls-files", "-u", "-z"}) {
			return Result{}, inspectionErr
		}
		return result, err
	}
	_, err := runSync(t, fixture, runner, options, nil, nil)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, inspectionErr) {
		t.Fatalf("Sync() error = %#v, want joined typed conflict", err)
	}
}

func TestSyncMergeFailureStateMatrix(t *testing.T) {
	hookErr := errors.New("merge hook failed")
	for _, test := range []struct {
		name             string
		beforeMerge      bool
		afterMerge       func(*connectFixture)
		wantPaths        []string
		wantConflict     bool
		wantInterrupted  bool
		wantOriginalFail bool
	}{
		{name: "merge head and unmerged", wantPaths: []string{"remote.md"}, wantConflict: true},
		{name: "unmerged without merge head", afterMerge: func(f *connectFixture) {
			if err := os.Remove(filepath.Join(f.root, ".git", "MERGE_HEAD")); err != nil {
				t.Fatal(err)
			}
		}, wantPaths: []string{"remote.md"}, wantConflict: true, wantInterrupted: true},
		{name: "merge head without unmerged", afterMerge: func(f *connectFixture) {
			f.git(f.root, "add", "--all", "--", ".")
		}, wantConflict: true, wantInterrupted: true},
		{name: "neither", beforeMerge: true, wantOriginalFail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, options := preparedSyncFixture(t)
			fixture.write("remote.md", "local conflict\n")
			seedRemoteAdvance(t, fixture, "remote.md", "remote conflict\n")
			runner := &interceptRunner{delegate: NewCommandRunner()}
			if test.beforeMerge {
				runner.before = func(command Command) error {
					if len(command.Args) > 0 && command.Args[0] == "merge" {
						return hookErr
					}
					return nil
				}
			}
			if test.afterMerge != nil {
				runner.after = func(command Command, result Result, err error) (Result, error) {
					if len(command.Args) > 0 && command.Args[0] == "merge" && err != nil {
						test.afterMerge(fixture)
					}
					return result, err
				}
			}
			result, err := runSync(t, fixture, runner, options, nil, nil)
			var conflict *ConflictError
			if gotConflict := errors.As(err, &conflict); gotConflict != test.wantConflict {
				t.Fatalf("Sync() error = %#v, conflict = %#v, want conflict %v", err, conflict, test.wantConflict)
			}
			if test.wantConflict && !reflect.DeepEqual(conflict.Paths, test.wantPaths) {
				t.Fatalf("Conflict paths = %#v, want %#v", conflict.Paths, test.wantPaths)
			}
			if errorHasSafeCode(err, CodeOperationInterrupted) != test.wantInterrupted {
				t.Fatalf("Sync() error = %#v, want interrupted %v", err, test.wantInterrupted)
			}
			if test.wantOriginalFail && !errors.Is(err, hookErr) {
				t.Fatalf("Sync() error = %#v, want original merge failure", err)
			}
			if len(servicePushes(runner.commands)) != 0 || result.PushOID != "" {
				t.Fatalf("merge failure pushed: result %#v, pushes %#v", result, servicePushes(runner.commands))
			}
		})
	}
}

func TestSyncMergeFailureInspectionFailuresFailClosed(t *testing.T) {
	inspectionErr := errors.New("merge state inspection failed")
	for _, test := range []struct {
		name   string
		match  func([]string) bool
		result Result
		err    error
	}{
		{name: "merge head command", match: func(args []string) bool {
			return reflect.DeepEqual(args, []string{"rev-parse", "--verify", "MERGE_HEAD^{commit}"})
		}, err: inspectionErr},
		{name: "merge head malformed", match: func(args []string) bool {
			return reflect.DeepEqual(args, []string{"rev-parse", "--verify", "MERGE_HEAD^{commit}"})
		}, result: Result{Stdout: "not-an-oid\n"}},
		{name: "unmerged truncated", match: func(args []string) bool {
			return reflect.DeepEqual(args, []string{"ls-files", "-u", "-z"})
		}, result: Result{StdoutTruncated: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, options := preparedSyncFixture(t)
			fixture.write("remote.md", "local conflict\n")
			seedRemoteAdvance(t, fixture, "remote.md", "remote conflict\n")
			runner := &interceptRunner{delegate: NewCommandRunner()}
			mergeFailed := false
			intercepted := false
			runner.after = func(command Command, result Result, err error) (Result, error) {
				if len(command.Args) > 0 && command.Args[0] == "merge" && err != nil {
					mergeFailed = true
				}
				if mergeFailed && !intercepted && test.match(command.Args) {
					intercepted = true
					return test.result, test.err
				}
				return result, err
			}
			_, err := runSync(t, fixture, runner, options, nil, nil)
			var conflict *ConflictError
			if !intercepted || !errors.As(err, &conflict) || !errorHasSafeCode(err, CodeOperationInterrupted) {
				t.Fatalf("Sync() error = %#v, conflict = %#v, intercepted = %v", err, conflict, intercepted)
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("Sync() error = %#v, want inspection failure", err)
			}
			if len(servicePushes(runner.commands)) != 0 {
				t.Fatal("push ran after merge-state inspection failure")
			}
		})
	}
}

func TestSyncMergeFailureMalformedMergeHeadFailsClosed(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("remote.md", "local conflict\n")
	seedRemoteAdvance(t, fixture, "remote.md", "remote conflict\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	corrupted := false
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if len(command.Args) > 0 && command.Args[0] == "merge" && err != nil {
			if writeErr := os.WriteFile(filepath.Join(fixture.root, ".git", "MERGE_HEAD"), []byte("not-an-oid\n"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			corrupted = true
		}
		return result, err
	}
	_, err := runSync(t, fixture, runner, options, nil, nil)
	var conflict *ConflictError
	if !corrupted || !errors.As(err, &conflict) || !errorHasSafeCode(err, CodeCommandFailed) ||
		!errorHasSafeCode(err, CodeOperationInterrupted) {
		t.Fatalf("Sync() error = %#v, conflict = %#v, corrupted = %v", err, conflict, corrupted)
	}
	if len(servicePushes(runner.commands)) != 0 {
		t.Fatal("push ran after malformed MERGE_HEAD")
	}
}

func TestSyncMergeFailureDetectsMergeHeadRemovalDuringInspection(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("remote.md", "local conflict\n")
	seedRemoteAdvance(t, fixture, "remote.md", "remote conflict\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	mergeFailed := false
	removed := false
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if len(command.Args) > 0 && command.Args[0] == "merge" && err != nil {
			mergeFailed = true
		}
		if mergeFailed && !removed && err == nil &&
			reflect.DeepEqual(command.Args, []string{"rev-parse", "--verify", "MERGE_HEAD^{commit}"}) {
			if removeErr := os.Remove(filepath.Join(fixture.root, ".git", "MERGE_HEAD")); removeErr != nil {
				t.Fatal(removeErr)
			}
			removed = true
		}
		return result, err
	}
	_, err := runSync(t, fixture, runner, options, nil, nil)
	var conflict *ConflictError
	if !removed || !errors.As(err, &conflict) || !errorHasSafeCode(err, CodeOperationInterrupted) {
		t.Fatalf("Sync() error = %#v, conflict = %#v, removed = %v", err, conflict, removed)
	}
	if len(servicePushes(runner.commands)) != 0 {
		t.Fatal("push ran after MERGE_HEAD inspection race")
	}
}

func TestSyncPreAddUnmergedInspectionFailureFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		result Result
		err    error
	}{
		{name: "command failure", err: errors.New("index inspection failed")},
		{name: "truncated", result: Result{StdoutTruncated: true}},
		{name: "malformed", result: Result{Stdout: "not nul terminated"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, options := preparedSyncFixture(t)
			fixture.write("local.md", "must remain uncommitted\n")
			runner := &interceptRunner{delegate: NewCommandRunner()}
			intercepted := false
			runner.after = func(command Command, result Result, err error) (Result, error) {
				if !intercepted && reflect.DeepEqual(command.Args, []string{"ls-files", "-u", "-z"}) {
					intercepted = true
					return test.result, test.err
				}
				return result, err
			}
			_, err := runSync(t, fixture, runner, options, nil, nil)
			var conflict *ConflictError
			if !intercepted || !errors.As(err, &conflict) {
				t.Fatalf("Sync() error = %#v, intercepted = %v", err, intercepted)
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("Sync() error = %#v, want inspection cause", err)
			}
			if len(servicePushes(runner.commands)) != 0 {
				t.Fatal("push ran after failed pre-add inspection")
			}
			for _, command := range runner.commands {
				if len(command.Args) > 0 && (command.Args[0] == "add" || command.Args[0] == "commit") {
					t.Fatalf("local snapshot mutation ran after failed inspection: %#v", command)
				}
			}
		})
	}
}
