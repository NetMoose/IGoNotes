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

type runnerStep struct {
	want   Command
	result Result
	err    error
}

type porcelainRunnerFake struct {
	t     *testing.T
	steps []runnerStep
	calls []Command
}

func (f *porcelainRunnerFake) Run(_ context.Context, command Command) (Result, error) {
	f.t.Helper()
	f.calls = append(f.calls, command)
	index := len(f.calls) - 1
	if index >= len(f.steps) {
		f.t.Fatalf("unexpected command: %+v", command)
	}
	step := f.steps[index]
	if !reflect.DeepEqual(command, step.want) {
		f.t.Errorf("command %d = %+v, want %+v", index, command, step.want)
	}
	return step.result, step.err
}

func (f *porcelainRunnerFake) assertDone() {
	f.t.Helper()
	if len(f.calls) != len(f.steps) {
		f.t.Fatalf("runner calls = %d, want %d", len(f.calls), len(f.steps))
	}
}

func readCommand(dir string, args ...string) Command {
	return Command{Dir: dir, Args: args, Scope: LocalOperation, ReadOnly: true}
}

func exitError(code int) error {
	return &SafeError{Code: CodeCommandFailed, Message: "Git command failed", ExitCode: code, diagnostic: "private git diagnostic"}
}

func TestParseGitVersion(t *testing.T) {
	tests := []struct {
		output string
		want   Version
	}{
		{output: "git version 2.28.0\n", want: Version{Major: 2, Minor: 28, Patch: 0, Raw: "git version 2.28.0"}},
		{output: "git version 2.43.0.windows.1\n", want: Version{Major: 2, Minor: 43, Patch: 0, Raw: "git version 2.43.0.windows.1"}},
	}
	for _, test := range tests {
		got, err := parseGitVersion(test.output)
		if err != nil {
			t.Fatalf("parseGitVersion(%q) error = %v", test.output, err)
		}
		if got != test.want {
			t.Fatalf("parseGitVersion(%q) = %+v, want %+v", test.output, got, test.want)
		}
	}

	for _, output := range []string{"", "git version", "git version two.28.0", "git version 2.28"} {
		if _, err := parseGitVersion(output); err == nil {
			t.Errorf("parseGitVersion(%q) error = nil, want malformed output", output)
		}
	}

	if !(Version{Major: 2, Minor: 28}).Supported() || !(Version{Major: 3}).Supported() {
		t.Fatal("Git 2.28+ must be supported")
	}
	if (Version{Major: 2, Minor: 27, Patch: 9}).Supported() || (Version{Major: 1, Minor: 99}).Supported() {
		t.Fatal("Git older than 2.28 must not be supported")
	}
}

func TestClientVersionUsesExactReadOnlyCommand(t *testing.T) {
	dir := t.TempDir()
	runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{
		want: readCommand(dir, "--version"), result: Result{Stdout: "git version 2.43.0.windows.1\n"},
	}}}
	got, err := NewClient(runner).Version(context.Background(), dir)
	if err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if got != (Version{Major: 2, Minor: 43, Patch: 0, Raw: "git version 2.43.0.windows.1"}) {
		t.Fatalf("Version() = %+v", got)
	}
	runner.assertDone()
}

func TestValidateBranchPolicy(t *testing.T) {
	accepted := []string{"main", "feature/editor", "head", "refs-notes"}
	for _, branch := range accepted {
		t.Run("accept_"+branch, func(t *testing.T) {
			dir := t.TempDir()
			runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{
				want: readCommand(dir, "check-ref-format", "refs/heads/"+branch),
			}}}
			if err := NewClient(runner).ValidateBranch(context.Background(), dir, branch); err != nil {
				t.Fatalf("ValidateBranch(%q) error = %v", branch, err)
			}
			runner.assertDone()
		})
	}

	rejected := []string{
		"", " main", "main ", "-main", "refs/heads/main", "@", "HEAD", "FETCH_HEAD", "ORIG_HEAD",
		"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "AUTO_MERGE", "BISECT_HEAD",
		"@{-1}", "@{-2}", "main@{1}", "main..other", "main~1", "main^", "main^{commit}", "main:path",
		"main?other", "main*other", "main[other", `main\other`, "main//other", "main/.other", "main.lock",
		"main other", "main\x00other", "main\nother",
	}
	for _, branch := range rejected {
		t.Run("reject_"+branch, func(t *testing.T) {
			runner := &porcelainRunnerFake{t: t}
			err := NewClient(runner).ValidateBranch(context.Background(), t.TempDir(), branch)
			assertInvalidBranchError(t, err)
			if len(runner.calls) != 0 {
				t.Fatalf("runner calls = %d, want 0", len(runner.calls))
			}
		})
	}
}

func TestValidateBranchConvertsGitRejection(t *testing.T) {
	dir := t.TempDir()
	runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{
		want: readCommand(dir, "check-ref-format", "refs/heads/main"), err: exitError(1),
	}}}
	err := NewClient(runner).ValidateBranch(context.Background(), dir, "main")
	assertInvalidBranchError(t, err)
	runner.assertDone()
}

func assertInvalidBranchError(t *testing.T, err error) {
	t.Helper()
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != CodeInvalidBranch || safeErr.Field != "git_branch" {
		t.Fatalf("error = %#v, want invalid_branch SafeError for git_branch", err)
	}
	if safeErr.Diagnostic() != "" {
		t.Fatalf("invalid branch diagnostic = %q, want empty", safeErr.Diagnostic())
	}
}

func TestClientInspectLocalUsesExactReadOnlyCommands(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte("oid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "CHERRY_PICK_HEAD"), []byte("oid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".")
	runner := &porcelainRunnerFake{t: t, steps: []runnerStep{
		{want: readCommand(dir, "rev-parse", "--show-toplevel"), result: Result{Stdout: root + "\n"}},
		{want: readCommand(dir, "rev-parse", "--absolute-git-dir"), result: Result{Stdout: gitDir + "\n"}},
		{want: readCommand(dir, "symbolic-ref", "--quiet", "--short", "HEAD"), result: Result{Stdout: "main\n"}},
		{want: readCommand(dir, "status", "--porcelain=v1", "-z", "--untracked-files=all"), result: Result{Stdout: "?? new note.md\x00"}},
		{want: readCommand(dir, "remote", "get-url", "origin"), result: Result{Stdout: "https://example.com/notes.git\n"}},
		{want: readCommand(dir, "config", "--get", "user.name"), result: Result{Stdout: "Note Author\n"}},
		{want: readCommand(dir, "config", "--get", "user.email"), result: Result{Stdout: "author@example.com\n"}},
		{want: readCommand(dir, "rev-parse", "--verify", "HEAD"), result: Result{Stdout: strings.Repeat("a", 40) + "\n"}},
	}}

	got, err := NewClient(runner).InspectLocal(context.Background(), dir)
	if err != nil {
		t.Fatalf("InspectLocal() error = %v", err)
	}
	want := LocalInspection{
		HasRepository: true, RepositoryRoot: root, GitDir: gitDir, CurrentBranch: "main",
		WorkingTreeClean: false, ExistingOriginURL: "https://example.com/notes.git",
		PendingOperation: "merge", IdentityConfigured: true, HasCommits: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InspectLocal() = %+v, want %+v", got, want)
	}
	runner.assertDone()
}

func TestClientInspectLocalHandlesExpectedAbsence(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &porcelainRunnerFake{t: t, steps: []runnerStep{
		{want: readCommand(root, "rev-parse", "--show-toplevel"), result: Result{Stdout: root + "\n"}},
		{want: readCommand(root, "rev-parse", "--absolute-git-dir"), result: Result{Stdout: gitDir + "\n"}},
		{want: readCommand(root, "symbolic-ref", "--quiet", "--short", "HEAD"), err: exitError(1)},
		{want: readCommand(root, "status", "--porcelain=v1", "-z", "--untracked-files=all")},
		{want: readCommand(root, "remote", "get-url", "origin"), err: exitError(2)},
		{want: readCommand(root, "config", "--get", "user.name"), result: Result{Stdout: "Author\n"}},
		{want: readCommand(root, "config", "--get", "user.email"), err: exitError(1)},
		{want: readCommand(root, "rev-parse", "--verify", "HEAD"), err: exitError(128)},
	}}
	got, err := NewClient(runner).InspectLocal(context.Background(), root)
	if err != nil {
		t.Fatalf("InspectLocal() error = %v", err)
	}
	if !got.HasRepository || !got.DetachedHead || !got.WorkingTreeClean || got.ExistingOriginURL != "" || got.IdentityConfigured || got.HasCommits {
		t.Fatalf("InspectLocal() = %+v, want detached clean repository with expected values absent", got)
	}
	runner.assertDone()
}

func TestClientInspectLocalReturnsNoRepository(t *testing.T) {
	dir := t.TempDir()
	notRepository := &SafeError{Code: CodeNotRepository, Message: "Directory is not a Git repository", ExitCode: 128}
	runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{
		want: readCommand(dir, "rev-parse", "--show-toplevel"), err: notRepository,
	}}}
	got, err := NewClient(runner).InspectLocal(context.Background(), dir)
	if err != nil {
		t.Fatalf("InspectLocal() error = %v", err)
	}
	if got != (LocalInspection{}) {
		t.Fatalf("InspectLocal() = %+v, want no repository", got)
	}
	runner.assertDone()
}

func TestClientInspectLocalPropagatesUnexpectedOptionalCommandExit(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &porcelainRunnerFake{t: t, steps: []runnerStep{
		{want: readCommand(root, "rev-parse", "--show-toplevel"), result: Result{Stdout: root + "\n"}},
		{want: readCommand(root, "rev-parse", "--absolute-git-dir"), result: Result{Stdout: gitDir + "\n"}},
		{want: readCommand(root, "symbolic-ref", "--quiet", "--short", "HEAD"), result: Result{Stdout: "main\n"}},
		{want: readCommand(root, "status", "--porcelain=v1", "-z", "--untracked-files=all")},
		{want: readCommand(root, "remote", "get-url", "origin"), err: exitError(23)},
	}}
	if _, err := NewClient(runner).InspectLocal(context.Background(), root); err == nil {
		t.Fatal("InspectLocal() error = nil, want unexpected origin failure")
	}
	runner.assertDone()
}

func TestClientInspectRemoteParsesAllRefs(t *testing.T) {
	dir := t.TempDir()
	remote := "https://example.com/notes.git"
	mainOID := strings.Repeat("a", 40)
	devOID := strings.Repeat("b", 40)
	tagOID := strings.Repeat("c", 40)
	output := "ref: refs/heads/main\tHEAD\n" + mainOID + "\tHEAD\n" +
		devOID + "\trefs/heads/dev\n" + mainOID + "\trefs/heads/main\n" + tagOID + "\trefs/tags/v1\n"
	command := readCommand(dir, "ls-remote", "--symref", remote)
	command.Scope = NetworkOperation
	command.Secrets = []string{remote}
	runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{want: command, result: Result{Stdout: output}}}}
	got, err := NewClient(runner).InspectRemote(context.Background(), dir, remote)
	if err != nil {
		t.Fatalf("InspectRemote() error = %v", err)
	}
	want := RemoteInspection{Branches: map[string]string{"dev": devOID, "main": mainOID}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InspectRemote() = %+v, want %+v", got, want)
	}
	runner.assertDone()
}

func TestClientInspectRemoteEmptyMeansNoRefs(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		empty  bool
	}{
		{name: "empty", empty: true},
		{name: "tag only", output: strings.Repeat("a", 40) + "\trefs/tags/v1\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			remote := "origin"
			command := readCommand(dir, "ls-remote", "--symref", remote)
			command.Scope = NetworkOperation
			command.Secrets = []string{remote}
			runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{want: command, result: Result{Stdout: test.output}}}}
			got, err := NewClient(runner).InspectRemote(context.Background(), dir, remote)
			if err != nil {
				t.Fatalf("InspectRemote() error = %v", err)
			}
			if got.Empty != test.empty || got.Branches == nil {
				t.Fatalf("InspectRemote() = %+v, want Empty %v and initialized branches", got, test.empty)
			}
		})
	}
}

func TestClientInspectRemoteRejectsMalformedOutput(t *testing.T) {
	badOID := strings.Repeat("z", 40)
	for _, output := range []string{"missing-tab\n", "\trefs/heads/main\n", badOID + "\trefs/heads/main\n", "ref: \tHEAD\n", "ref: refs/heads/main\t\n"} {
		t.Run(output, func(t *testing.T) {
			dir := t.TempDir()
			command := readCommand(dir, "ls-remote", "--symref", "origin")
			command.Scope = NetworkOperation
			command.Secrets = []string{"origin"}
			runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{want: command, result: Result{Stdout: output}}}}
			if _, err := NewClient(runner).InspectRemote(context.Background(), dir, "origin"); err == nil {
				t.Fatal("InspectRemote() error = nil, want malformed output error")
			}
		})
	}
}

func TestClientHistoryRelationUsesOnlyLocalObjects(t *testing.T) {
	dir := t.TempDir()
	oid := strings.Repeat("a", 40)
	tests := []struct {
		name  string
		steps []runnerStep
		want  string
	}{
		{name: "none", want: "none"},
		{name: "missing object", steps: []runnerStep{{want: readCommand(dir, "cat-file", "-e", oid+"^{commit}"), err: exitError(1)}}, want: "unknown"},
		{name: "remote ancestor", steps: []runnerStep{
			{want: readCommand(dir, "cat-file", "-e", oid+"^{commit}")},
			{want: readCommand(dir, "merge-base", "--is-ancestor", oid, "HEAD")},
		}, want: "shared"},
		{name: "common base", steps: []runnerStep{
			{want: readCommand(dir, "cat-file", "-e", oid+"^{commit}")},
			{want: readCommand(dir, "merge-base", "--is-ancestor", oid, "HEAD"), err: exitError(1)},
			{want: readCommand(dir, "merge-base", "HEAD", oid), result: Result{Stdout: strings.Repeat("b", 40) + "\n"}},
		}, want: "shared"},
		{name: "unrelated", steps: []runnerStep{
			{want: readCommand(dir, "cat-file", "-e", oid+"^{commit}")},
			{want: readCommand(dir, "merge-base", "--is-ancestor", oid, "HEAD"), err: exitError(1)},
			{want: readCommand(dir, "merge-base", "HEAD", oid), err: exitError(1)},
		}, want: "unrelated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &porcelainRunnerFake{t: t, steps: test.steps}
			remoteOID := oid
			if test.name == "none" {
				remoteOID = ""
			}
			got, err := NewClient(runner).HistoryRelation(context.Background(), dir, remoteOID)
			if err != nil {
				t.Fatalf("HistoryRelation() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("HistoryRelation() = %q, want %q", got, test.want)
			}
			runner.assertDone()
		})
	}
}

func TestClientHistoryRelationPropagatesUnexpectedExit(t *testing.T) {
	dir := t.TempDir()
	oid := strings.Repeat("a", 40)
	runner := &porcelainRunnerFake{t: t, steps: []runnerStep{
		{want: readCommand(dir, "cat-file", "-e", oid+"^{commit}")},
		{want: readCommand(dir, "merge-base", "--is-ancestor", oid, "HEAD"), err: exitError(2)},
	}}
	if _, err := NewClient(runner).HistoryRelation(context.Background(), dir, oid); err == nil {
		t.Fatal("HistoryRelation() error = nil, want unexpected Git failure")
	}
	runner.assertDone()
}

func TestClientRejectsTruncatedOutput(t *testing.T) {
	assertTruncated := func(t *testing.T, err error) {
		t.Helper()
		var safeErr *SafeError
		if !errors.As(err, &safeErr) || safeErr.Code != CodeCommandFailed || safeErr.Message != "Git output exceeded the configured limit" {
			t.Fatalf("error = %#v, want truncated-output SafeError", err)
		}
	}

	t.Run("version stdout", func(t *testing.T) {
		dir := t.TempDir()
		runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{want: readCommand(dir, "--version"), result: Result{StdoutTruncated: true}}}}
		_, err := NewClient(runner).Version(context.Background(), dir)
		assertTruncated(t, err)
	})
	t.Run("version stderr", func(t *testing.T) {
		dir := t.TempDir()
		runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{want: readCommand(dir, "--version"), result: Result{StderrTruncated: true}}}}
		_, err := NewClient(runner).Version(context.Background(), dir)
		assertTruncated(t, err)
	})
	t.Run("branch", func(t *testing.T) {
		dir := t.TempDir()
		runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{want: readCommand(dir, "check-ref-format", "refs/heads/main"), result: Result{StdoutTruncated: true}}}}
		err := NewClient(runner).ValidateBranch(context.Background(), dir, "main")
		assertTruncated(t, err)
	})
	t.Run("local status", func(t *testing.T) {
		root := t.TempDir()
		gitDir := filepath.Join(root, ".git")
		if err := os.Mkdir(gitDir, 0o700); err != nil {
			t.Fatal(err)
		}
		runner := &porcelainRunnerFake{t: t, steps: []runnerStep{
			{want: readCommand(root, "rev-parse", "--show-toplevel"), result: Result{Stdout: root + "\n"}},
			{want: readCommand(root, "rev-parse", "--absolute-git-dir"), result: Result{Stdout: gitDir + "\n"}},
			{want: readCommand(root, "symbolic-ref", "--quiet", "--short", "HEAD"), result: Result{Stdout: "main\n"}},
			{want: readCommand(root, "status", "--porcelain=v1", "-z", "--untracked-files=all"), result: Result{StdoutTruncated: true}},
		}}
		_, err := NewClient(runner).InspectLocal(context.Background(), root)
		assertTruncated(t, err)
	})
	t.Run("remote", func(t *testing.T) {
		dir := t.TempDir()
		command := readCommand(dir, "ls-remote", "--symref", "origin")
		command.Scope = NetworkOperation
		command.Secrets = []string{"origin"}
		runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{want: command, result: Result{StdoutTruncated: true}}}}
		_, err := NewClient(runner).InspectRemote(context.Background(), dir, "origin")
		assertTruncated(t, err)
	})
	t.Run("history", func(t *testing.T) {
		dir := t.TempDir()
		oid := strings.Repeat("a", 40)
		runner := &porcelainRunnerFake{t: t, steps: []runnerStep{{want: readCommand(dir, "cat-file", "-e", oid+"^{commit}"), result: Result{StdoutTruncated: true}}}}
		_, err := NewClient(runner).HistoryRelation(context.Background(), dir, oid)
		assertTruncated(t, err)
	})
}

func TestClientSatisfiesPorcelain(t *testing.T) {
	var _ Porcelain = (*Client)(nil)
}
