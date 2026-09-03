package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

const testOperationID = "0123456789abcdef0123456789abcdef"

type connectFixture struct {
	t            *testing.T
	root         string
	remote       string
	objectFormat string
}

func newConnectFixture(t *testing.T) *connectFixture {
	return newConnectFixtureWithObjectFormat(t, "")
}

func newConnectFixtureWithObjectFormat(t *testing.T, objectFormat string) *connectFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	fixture := &connectFixture{t: t, root: filepath.Join(root, "notes"), remote: filepath.Join(root, "remote.git"), objectFormat: objectFormat}
	if err := os.Mkdir(fixture.root, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.git(root, "config", "--global", "user.name", "IGoNotes Test")
	fixture.git(root, "config", "--global", "user.email", "igonotes@example.invalid")
	args := []string{"init", "--bare", "--initial-branch", "main"}
	if objectFormat != "" {
		args = append(args, "--object-format="+objectFormat)
	}
	fixture.git(root, append(args, fixture.remote)...)
	return fixture
}

func (f *connectFixture) git(dir string, args ...string) string {
	f.t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %q in %q: %v\n%s", args, dir, err, output)
	}
	return strings.TrimSpace(string(output))
}

func (f *connectFixture) write(relativePath, contents string) {
	f.t.Helper()
	path := filepath.Join(f.root, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *connectFixture) seedRemote() string {
	f.t.Helper()
	seed := filepath.Join(filepath.Dir(f.root), "seed")
	if err := os.Mkdir(seed, 0o700); err != nil {
		f.t.Fatal(err)
	}
	args := []string{"init", "--initial-branch", "main"}
	if f.objectFormat != "" {
		args = append(args, "--object-format="+f.objectFormat)
	}
	f.git(seed, args...)
	f.git(seed, "config", "user.name", "IGoNotes Test")
	f.git(seed, "config", "user.email", "igonotes@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, "remote.md"), []byte("remote\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.git(seed, "add", "--all", "--", ".")
	f.git(seed, "commit", "-m", "remote commit")
	f.git(seed, "remote", "add", "origin", f.remote)
	f.git(seed, "push", "origin", "HEAD:refs/heads/main")
	return f.git(seed, "rev-parse", "--verify", "HEAD^{commit}")
}

func (f *connectFixture) initLocal(branch string) string {
	f.t.Helper()
	args := []string{"init", "--initial-branch", branch}
	if f.objectFormat != "" {
		args = append(args, "--object-format="+f.objectFormat)
	}
	f.git(f.root, args...)
	f.git(f.root, "config", "user.name", "IGoNotes Test")
	f.git(f.root, "config", "user.email", "igonotes@example.invalid")
	f.write("local.md", "local\n")
	f.git(f.root, "add", "--all", "--", ".")
	f.git(f.root, "commit", "-m", "local commit")
	return f.git(f.root, "rev-parse", "--verify", "HEAD^{commit}")
}

func (f *connectFixture) options() InitializeOptions {
	return InitializeOptions{
		Operation: Operation{
			ID:                testOperationID,
			BaseName:          "notes",
			Kind:              OperationInitialize,
			Branch:            "main",
			RepoPath:          f.root,
			ConfigFingerprint: "config-v1",
			RemoteFingerprint: "remote-v1",
			CreatedAt:         time.Date(2026, time.September, 3, 10, 11, 12, 123456789, time.FixedZone("fixture", 2*60*60)),
		},
		Snapshot: ConfiguredBase{
			Name:              "notes",
			Path:              f.root,
			URL:               f.remote,
			Branch:            "main",
			Fingerprint:       "config-v1",
			RemoteFingerprint: "remote-v1",
		},
		Confirmations: model.GitConfirmations{
			CreateRepository: true,
			ReplaceOrigin:    true,
			CreateBranch:     true,
			MergeHistories:   true,
		},
	}
}

func (f *connectFixture) initialize(options InitializeOptions, transaction WorktreeTransaction, progress Progress) (OperationResult, error) {
	f.t.Helper()
	runner := NewCommandRunner()
	service := NewService(runner, NewClient(runner))
	if transaction == nil {
		transaction = func(_ context.Context, mutate func(string) error) error { return mutate(f.root) }
	}
	if progress == nil {
		progress = func(context.Context, Checkpoint) error { return nil }
	}
	return service.Initialize(context.Background(), options, transaction, progress)
}

func (f *connectFixture) remoteOID() string {
	f.t.Helper()
	return f.git(filepath.Dir(f.root), "--git-dir", f.remote, "rev-parse", "--verify", "refs/heads/main^{commit}")
}

func assertAncestor(t *testing.T, fixture *connectFixture, ancestor, descendant string) {
	t.Helper()
	fixture.git(fixture.root, "merge-base", "--is-ancestor", ancestor, descendant)
}

func TestInitializeNoRepositoryEmptyRemote(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents string
		wantFile bool
	}{
		{name: "empty local"},
		{name: "nonempty local", contents: "# local\n", wantFile: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConnectFixture(t)
			if test.wantFile {
				fixture.write("note.md", test.contents)
			}
			result, err := fixture.initialize(fixture.options(), nil, nil)
			if err != nil {
				t.Fatalf("Initialize() error = %v", err)
			}
			head := fixture.git(fixture.root, "rev-parse", "--verify", "HEAD^{commit}")
			if branch := fixture.git(fixture.root, "symbolic-ref", "--quiet", "--short", "HEAD"); branch != "main" {
				t.Fatalf("branch = %q, want main", branch)
			}
			if result.PushOID != head || result.RemoteOID != head || fixture.remoteOID() != head {
				t.Fatalf("oids = result %#v remote %q, want head %q", result, fixture.remoteOID(), head)
			}
			if count := fixture.git(fixture.root, "rev-list", "--count", "HEAD"); count != "1" {
				t.Fatalf("commit count = %s, want 1", count)
			}
			if test.wantFile && fixture.git(fixture.root, "show", "HEAD:note.md") != strings.TrimSpace(test.contents) {
				t.Fatal("pushed tree does not contain local note")
			}
		})
	}
}

func TestInitializeNoRepositoryExistingRemoteBranch(t *testing.T) {
	for _, localContents := range []string{"", "local\n"} {
		name := "empty local"
		if localContents != "" {
			name = "nonempty local"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newConnectFixture(t)
			remoteOID := fixture.seedRemote()
			if localContents != "" {
				fixture.write("local.md", localContents)
			}
			result, err := fixture.initialize(fixture.options(), nil, nil)
			if err != nil {
				t.Fatalf("Initialize() error = %v", err)
			}
			head := fixture.git(fixture.root, "rev-parse", "--verify", "HEAD^{commit}")
			assertAncestor(t, fixture, remoteOID, head)
			if localContents == "" {
				if head != remoteOID || result.BackupRef != "" {
					t.Fatalf("empty local result = %#v head %q, want exact remote %q without backup", result, head, remoteOID)
				}
			} else {
				if result.BackupRef == "" || fixture.git(fixture.root, "rev-list", "--parents", "-n", "1", "HEAD") == head+" "+remoteOID {
					t.Fatalf("nonempty local did not preserve an unrelated merge and backup: %#v", result)
				}
				localSnapshotOID := fixture.git(fixture.root, "rev-parse", "--verify", result.BackupRef+"^{commit}")
				assertAncestor(t, fixture, localSnapshotOID, head)
				if fixture.git(fixture.root, "show", "HEAD:local.md") != strings.TrimSpace(localContents) {
					t.Fatal("merged tree does not contain local file")
				}
			}
			if fixture.remoteOID() != head || result.PushOID != head {
				t.Fatal("push did not use exact selected HEAD")
			}
		})
	}
}

func TestInitializeExistingRepositoryEmptyRemote(t *testing.T) {
	fixture := newConnectFixture(t)
	localOID := fixture.initLocal("local")
	fixture.write("dirty.md", "captured\n")

	result, err := fixture.initialize(fixture.options(), nil, nil)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	head := fixture.git(fixture.root, "rev-parse", "--verify", "HEAD^{commit}")
	if fixture.git(fixture.root, "symbolic-ref", "--quiet", "--short", "HEAD") != "main" {
		t.Fatal("selected branch was not checked out")
	}
	assertAncestor(t, fixture, localOID, head)
	if result.BackupRef == "" || fixture.git(fixture.root, "rev-parse", "--verify", result.BackupRef+"^{commit}") == "" {
		t.Fatal("local snapshot backup was not retained")
	}
	if fixture.git(fixture.root, "show", "HEAD:dirty.md") != "captured" || fixture.remoteOID() != head {
		t.Fatal("current changes or exact remote head were not preserved")
	}
}

func TestInitializeExistingRepositoryExistingRemoteBranch(t *testing.T) {
	fixture := newConnectFixture(t)
	remoteOID := fixture.seedRemote()
	localOID := fixture.initLocal("local")
	fixture.write("dirty.md", "captured\n")

	result, err := fixture.initialize(fixture.options(), nil, nil)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	head := fixture.git(fixture.root, "rev-parse", "--verify", "HEAD^{commit}")
	assertAncestor(t, fixture, remoteOID, head)
	assertAncestor(t, fixture, localOID, head)
	if result.BackupRef == "" || fixture.git(fixture.root, "rev-parse", "--verify", result.BackupRef+"^{commit}") == "" {
		t.Fatal("snapshot backup was not retained")
	}
	for _, path := range []string{"local.md", "remote.md", "dirty.md"} {
		fixture.git(fixture.root, "cat-file", "-e", "HEAD:"+path)
	}
	if fixture.remoteOID() != head || result.PushOID != head {
		t.Fatal("push did not preserve exact merged HEAD")
	}
}

func TestInitializeExistingRepositoryEmptyRemoteDetachedHEAD(t *testing.T) {
	fixture := newConnectFixture(t)
	localOID := fixture.initLocal("main")
	fixture.git(fixture.root, "switch", "--detach", localOID)
	fixture.write("detached.md", "detached snapshot\n")

	result, err := fixture.initialize(fixture.options(), nil, nil)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	head := fixture.git(fixture.root, "rev-parse", "--verify", "HEAD^{commit}")
	if result.BackupRef == "" {
		t.Fatal("detached snapshot backup is empty")
	}
	backupOID := fixture.git(fixture.root, "rev-parse", "--verify", result.BackupRef+"^{commit}")
	if backupOID == localOID {
		t.Fatalf("detached snapshot backup = %q/%q, want committed detached changes", result.BackupRef, backupOID)
	}
	assertAncestor(t, fixture, localOID, head)
	assertAncestor(t, fixture, backupOID, head)
	if fixture.git(fixture.root, "show", "HEAD:detached.md") != "detached snapshot" || fixture.remoteOID() != head {
		t.Fatal("detached snapshot tree or exact push was not preserved")
	}
}

func TestInitializeRetryDoesNotCreateSecondEmptyCommit(t *testing.T) {
	fixture := newConnectFixture(t)
	options := fixture.options()
	result, err := fixture.initialize(options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Operation.PushOID = result.PushOID
	options.Operation.RemoteOID = result.RemoteOID
	options.LastRemoteOID = result.RemoteOID
	if _, err := fixture.initialize(options, nil, nil); err != nil {
		t.Fatal(err)
	}
	if count := fixture.git(fixture.root, "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("commit count after retry = %s, want 1", count)
	}
}

func TestInitializeFailureKeepsRepositoryRemoteCommitAndBackup(t *testing.T) {
	fixture := newConnectFixture(t)
	remoteOID := fixture.seedRemote()
	fixture.initLocal("local")
	fixture.write("remote.md", "local collision\n")
	var last Checkpoint
	_, err := fixture.initialize(fixture.options(), nil, func(_ context.Context, checkpoint Checkpoint) error {
		last = checkpoint
		return nil
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !reflect.DeepEqual(conflict.Paths, []string{"remote.md"}) {
		t.Fatalf("Initialize() error = %#v, want conflict for remote.md", err)
	}
	if fixture.remoteOID() != remoteOID {
		t.Fatal("remote changed after conflict")
	}
	if last.BackupRef == "" || fixture.git(fixture.root, "rev-parse", "--verify", last.BackupRef+"^{commit}") == "" {
		t.Fatal("backup was not preserved after conflict")
	}
}

func TestInitializeRejectsOriginMismatchWithoutConfirmation(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.initLocal("main")
	other := filepath.Join(filepath.Dir(fixture.root), "other.git")
	fixture.git(filepath.Dir(fixture.root), "init", "--bare", other)
	fixture.git(fixture.root, "remote", "add", "origin", other)
	options := fixture.options()
	options.Confirmations.ReplaceOrigin = false
	_, err := fixture.initialize(options, nil, nil)
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != CodeOriginMismatch {
		t.Fatalf("Initialize() error = %#v, want origin mismatch", err)
	}
	if got := fixture.git(fixture.root, "remote", "get-url", "origin"); got != other {
		t.Fatalf("origin changed to %q", got)
	}
}

func TestInitializeRewritesOriginOnlyWithConfirmation(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.initLocal("main")
	other := filepath.Join(filepath.Dir(fixture.root), "other.git")
	fixture.git(filepath.Dir(fixture.root), "init", "--bare", other)
	fixture.git(fixture.root, "remote", "add", "origin", other)
	if _, err := fixture.initialize(fixture.options(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := fixture.git(fixture.root, "remote", "get-url", "origin"); got != fixture.remote {
		t.Fatalf("origin = %q, want configured remote", got)
	}
}

func TestInitializeRejectsAndNormalizesOriginPushURL(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.initLocal("main")
	malicious := filepath.Join(filepath.Dir(fixture.root), "malicious.git")
	fixture.git(filepath.Dir(fixture.root), "init", "--bare", "--initial-branch", "main", malicious)
	fixture.git(fixture.root, "remote", "add", "origin", fixture.remote)
	fixture.git(fixture.root, "config", "--add", "remote.origin.pushurl", malicious)
	fixture.git(fixture.root, "config", "--add", "remote.origin.pushurl", malicious)
	options := fixture.options()
	options.Confirmations.ReplaceOrigin = false
	runner := &interceptRunner{delegate: NewCommandRunner()}

	_, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
	assertSafeCode(t, err, CodeOriginMismatch)
	if strings.Contains(err.Error(), malicious) {
		t.Fatalf("origin mismatch disclosed unknown destination: %v", err)
	}
	assertNoServicePush(t, runner.commands)
	for _, command := range runner.commands {
		if containsArg(command.Args, malicious) || containsArg(command.Secrets, malicious) {
			t.Fatalf("unknown destination escaped origin inspection: %#v", command)
		}
		if !command.ReadOnly {
			t.Fatalf("origin mutated without confirmation: %q", command.Args)
		}
	}
	if got := fixture.git(fixture.root, "remote", "get-url", "--all", "--push", "origin"); got != malicious+"\n"+malicious {
		t.Fatalf("push URL changed without confirmation: %q", got)
	}

	options.Confirmations.ReplaceOrigin = true
	normalizeRunner := &interceptRunner{delegate: NewCommandRunner()}
	if _, err := initializeWithRunner(t, fixture, normalizeRunner, options, nil, nil); err != nil {
		t.Fatal(err)
	}
	assertExactCommand(t, normalizeRunner.commands, localSecretCommand(fixture.root, false, fixture.remote,
		"remote", "set-url", "origin", fixture.remote))
	assertExactCommand(t, normalizeRunner.commands, localSecretCommand(fixture.root, false, fixture.remote,
		"config", "--replace-all", "remote.origin.url", fixture.remote))
	assertExactCommand(t, normalizeRunner.commands, localCommand(fixture.root, false,
		"config", "--unset-all", "remote.origin.pushurl"))
	assertExactCommand(t, normalizeRunner.commands, localCommand(fixture.root, true,
		"remote", "get-url", "--all", "--push", "origin"))
	if got := fixture.git(fixture.root, "remote", "get-url", "--all", "origin"); got != fixture.remote {
		t.Fatalf("fetch URL = %q, want %q", got, fixture.remote)
	}
	if got := fixture.git(fixture.root, "remote", "get-url", "--all", "--push", "origin"); got != fixture.remote {
		t.Fatalf("effective push URL = %q, want %q", got, fixture.remote)
	}
	if output, err := runFixtureGit(fixture.root, "config", "--get-all", "remote.origin.pushurl"); err == nil || output != "" {
		t.Fatalf("explicit push URL remains: output %q, error %v", output, err)
	}
	fixture.remoteOID()
	if output, err := runFixtureGit(filepath.Dir(fixture.root), "--git-dir", malicious, "show-ref"); err == nil || output != "" {
		t.Fatalf("malicious repository changed: output %q, error %v", output, err)
	}
}

func TestInitializeRejectsLateOriginPushURLBeforePush(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.initLocal("main")
	malicious := filepath.Join(filepath.Dir(fixture.root), "malicious.git")
	fixture.git(filepath.Dir(fixture.root), "init", "--bare", "--initial-branch", "main", malicious)
	options := fixture.options()
	runner := &interceptRunner{delegate: NewCommandRunner()}
	injected := false
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if !injected && len(command.Args) > 0 && command.Args[0] == "ls-remote" &&
			reflect.DeepEqual(command.Args[1:], []string{"--symref", "origin"}) {
			fixture.git(fixture.root, "config", "--add", "remote.origin.pushurl", malicious)
			injected = true
		}
		return result, err
	}

	_, err := initializeWithRunner(t, fixture, runner, options, nil, nil)
	assertSafeCode(t, err, CodeOriginMismatch)
	if !injected {
		t.Fatal("late push URL was not injected")
	}
	assertNoServicePush(t, runner.commands)
}

func runFixtureGit(dir string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func TestInitializeRejectsMissingSelectedBranchOnNonemptyRemote(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	options := fixture.options()
	options.Snapshot.Branch = "missing"
	options.Operation.Branch = "missing"
	_, err := fixture.initialize(options, nil, nil)
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != CodeBranchDeleted {
		t.Fatalf("Initialize() error = %#v, want branch deleted", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.root, ".git")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("repository was mutated before missing branch rejection")
	}
}

func TestInitializeRejectsSelectedBranchMissingOnTagOnlyRemote(t *testing.T) {
	fixture := newConnectFixture(t)
	seed := filepath.Join(filepath.Dir(fixture.root), "tag-seed")
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.git(seed, "init", "--initial-branch", "temporary")
	fixture.git(seed, "config", "user.name", "IGoNotes Test")
	fixture.git(seed, "config", "user.email", "igonotes@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, "tagged.md"), []byte("tag only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(seed, "add", "--all", "--", ".")
	fixture.git(seed, "commit", "-m", "tag only")
	fixture.git(seed, "tag", "v1")
	fixture.git(seed, "remote", "add", "origin", fixture.remote)
	fixture.git(seed, "push", "origin", "refs/tags/v1:refs/tags/v1")
	runner := &interceptRunner{delegate: NewCommandRunner()}

	_, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
	assertSafeCode(t, err, CodeBranchDeleted)
	for _, command := range runner.commands {
		if command.Scope == LocalOperation && !command.ReadOnly {
			t.Fatalf("local mutation ran for tag-only missing branch: %q", command.Args)
		}
	}
	assertNoServicePush(t, runner.commands)
}

func TestInitializeRejectsParentRepositoryRoot(t *testing.T) {
	fixture := newConnectFixture(t)
	parent := filepath.Dir(fixture.root)
	fixture.git(parent, "init", "--initial-branch", "main")
	_, err := fixture.initialize(fixture.options(), nil, nil)
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != CodeRepositoryRoot {
		t.Fatalf("Initialize() error = %#v, want repository root error", err)
	}
}

func TestInitializeRejectsUnfinishedGitOperation(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.initLocal("main")
	marker := filepath.Join(fixture.root, ".git", "MERGE_HEAD")
	if err := os.WriteFile(marker, []byte(strings.Repeat("0", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.initialize(fixture.options(), nil, nil)
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != CodeRepositoryLocked {
		t.Fatalf("Initialize() error = %#v, want repository locked", err)
	}
}

func TestInitializeUnbornCheckoutPreservesIgnoredCollision(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	seed := filepath.Join(filepath.Dir(fixture.root), "seed")
	if err := os.WriteFile(filepath.Join(seed, "ignored.md"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(seed, "add", "--all", "--", ".")
	fixture.git(seed, "commit", "-m", "remote ignored path")
	fixture.git(seed, "push", "origin", "HEAD:main")
	excludes := filepath.Join(filepath.Dir(fixture.root), "home", "excludes")
	if err := os.WriteFile(excludes, []byte("ignored.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(filepath.Dir(fixture.root), "config", "--global", "core.excludesFile", excludes)
	fixture.write("ignored.md", "local\n")

	runner := &interceptRunner{delegate: NewCommandRunner()}
	_, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
	if err == nil {
		t.Fatal("Initialize() error = nil, want checkout collision")
	}
	contents, readErr := os.ReadFile(filepath.Join(fixture.root, "ignored.md"))
	if readErr != nil || string(contents) != "local\n" {
		t.Fatalf("ignored collision changed: contents %q error %v", contents, readErr)
	}
	assertNoCheckout(t, runner.commands)
	assertNoRefMutation(t, runner.commands)
}

func TestInitializeUnbornCheckoutRejectsIgnoredPrefixCollision(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	seed := filepath.Join(filepath.Dir(fixture.root), "seed")
	if err := os.WriteFile(filepath.Join(seed, "dir"), []byte("remote leaf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(seed, "add", "--all", "--", ".")
	fixture.git(seed, "commit", "-m", "remote prefix leaf")
	fixture.git(seed, "push", "origin", "HEAD:main")
	excludes := filepath.Join(filepath.Dir(fixture.root), "home", "excludes")
	if err := os.WriteFile(excludes, []byte("dir/local.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(filepath.Dir(fixture.root), "config", "--global", "core.excludesFile", excludes)
	fixture.write("dir/local.md", "preserve local bytes\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}

	_, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
	assertSafeCode(t, err, CodeOperationInterrupted)
	contents, readErr := os.ReadFile(filepath.Join(fixture.root, "dir", "local.md"))
	if readErr != nil || string(contents) != "preserve local bytes\n" {
		t.Fatalf("ignored descendant changed: contents %q error %v", contents, readErr)
	}
	assertNoCheckout(t, runner.commands)
	assertNoRefMutation(t, runner.commands)
}

func TestInitializeUnbornCheckoutRejectsNoteCreatedDuringFetch(t *testing.T) {
	fixture := newConnectFixture(t)
	fixture.seedRemote()
	runner := &interceptRunner{delegate: NewCommandRunner()}
	created := false
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if err == nil && !created && len(command.Args) > 0 && command.Args[0] == "fetch" {
			fixture.write("remote.md", "created during fetch\n")
			created = true
		}
		return result, err
	}

	_, err := initializeWithRunner(t, fixture, runner, fixture.options(), nil, nil)
	assertSafeCode(t, err, CodeOperationInterrupted)
	contents, readErr := os.ReadFile(filepath.Join(fixture.root, "remote.md"))
	if readErr != nil || string(contents) != "created during fetch\n" {
		t.Fatalf("new note changed: contents %q error %v", contents, readErr)
	}
	assertNoCheckout(t, runner.commands)
	assertNoRefMutation(t, runner.commands)
}

func assertNoCheckout(t *testing.T, commands []Command) {
	t.Helper()
	for _, command := range commands {
		if len(command.Args) > 0 && command.Args[0] == "checkout" {
			t.Fatalf("unexpected checkout command: %q", command.Args)
		}
	}
}

func assertNoRefMutation(t *testing.T, commands []Command) {
	t.Helper()
	for _, command := range commands {
		if len(command.Args) > 0 && command.Args[0] == "update-ref" {
			t.Fatalf("unexpected ref mutation: %q", command.Args)
		}
	}
}
