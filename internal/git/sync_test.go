package git

import (
	"context"
	"errors"
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
