package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type helperObservation struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
	Dir  string   `json:"dir"`
}

func TestGitRunnerHelper(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}

	args := os.Args[separator+1:]
	switch args[0] {
	case "success":
		return
	case "inspect":
		dir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(helperObservation{
			Args: args[1:],
			Env:  os.Environ(),
			Dir:  dir,
		}); err != nil {
			t.Fatal(err)
		}
	case "output":
		stdoutSize, err := strconv.Atoi(args[1])
		if err != nil {
			t.Fatal(err)
		}
		stderrSize, err := strconv.Atoi(args[2])
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(os.Stdout, strings.Repeat("o", stdoutSize))
		_, _ = io.WriteString(os.Stderr, strings.Repeat("e", stderrSize))
	case "stderr":
		_, _ = io.WriteString(os.Stderr, args[1])
	case "fail":
		_, _ = io.WriteString(os.Stderr, args[1])
		os.Exit(23)
	case "wait":
		time.Sleep(30 * time.Second)
	default:
		t.Fatalf("unknown helper action %q", args[0])
	}
}

func helperCommand(ctx context.Context, action string, args ...string) *exec.Cmd {
	commandArgs := []string{"-test.run=^TestGitRunnerHelper$", "--", action}
	commandArgs = append(commandArgs, args...)
	return exec.CommandContext(ctx, os.Args[0], commandArgs...)
}

func TestCommandRunnerPassesArgumentsWithoutShell(t *testing.T) {
	var gotName string
	var gotArgs []string
	factory := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return helperCommand(ctx, "success")
	}
	runner := newCommandRunner("git", time.Second, time.Second, 1024, factory)
	argument := `origin; touch /tmp/igonotes-must-not-exist`
	commandArgs := []string{"remote", "get-url", argument}

	_, err := runner.Run(context.Background(), Command{
		Dir: t.TempDir(), Args: commandArgs, Scope: LocalOperation, ReadOnly: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotName != "git" {
		t.Fatalf("command name = %q, want git", gotName)
	}
	want := []string{"remote", "get-url", argument}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
	if !reflect.DeepEqual(commandArgs, want) {
		t.Fatalf("Command.Args mutated to %#v, want %#v", commandArgs, want)
	}
}

func TestCommandRunnerReplacesEnvironment(t *testing.T) {
	tests := []struct {
		name              string
		readOnly          bool
		wantOptionalLocks []string
	}{
		{name: "read only", readOnly: true, wantOptionalLocks: []string{"0"}},
		{name: "mutating", readOnly: false, wantOptionalLocks: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			factory := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				cmd := helperCommand(ctx, "inspect")
				cmd.Env = []string{
					"KEEP=value",
					"GIT_TERMINAL_PROMPT=1", "GIT_TERMINAL_PROMPT=hostile",
					"LC_ALL=hostile", "LC_ALL=also-hostile",
					"GIT_ALLOW_PROTOCOL=ext:file", "GIT_ALLOW_PROTOCOL=ext:ssh",
					"GIT_OPTIONAL_LOCKS=1", "GIT_OPTIONAL_LOCKS=hostile",
				}
				return cmd
			}
			runner := newCommandRunner("git", time.Second, time.Second, 16*1024, factory)

			result, err := runner.Run(context.Background(), Command{
				Dir: t.TempDir(), Args: []string{"status"}, ReadOnly: test.readOnly,
			})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			var observation helperObservation
			if err := json.NewDecoder(strings.NewReader(result.Stdout)).Decode(&observation); err != nil {
				t.Fatalf("decode helper output: %v\noutput: %q", err, result.Stdout)
			}
			assertEnvValues(t, observation.Env, "GIT_TERMINAL_PROMPT", []string{"0"})
			assertEnvValues(t, observation.Env, "LC_ALL", []string{"C"})
			assertEnvValues(t, observation.Env, "GIT_ALLOW_PROTOCOL", []string{AllowedGitProtocols})
			assertEnvValues(t, observation.Env, "GIT_OPTIONAL_LOCKS", test.wantOptionalLocks)
		})
	}
}

func assertEnvValues(t *testing.T, env []string, key string, want []string) {
	t.Helper()
	var got []string
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			got = append(got, strings.TrimPrefix(entry, prefix))
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("environment %s values = %#v, want %#v", key, got, want)
	}
}

func TestCommandRunnerSelectsTimeoutByScope(t *testing.T) {
	const localTimeout = 5 * time.Second
	const networkTimeout = 9 * time.Second
	var remaining []time.Duration
	factory := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("command context has no deadline")
		}
		remaining = append(remaining, time.Until(deadline))
		return helperCommand(ctx, "success")
	}
	runner := newCommandRunner("git", localTimeout, networkTimeout, 1024, factory)
	dir := t.TempDir()

	for _, scope := range []OperationScope{LocalOperation, NetworkOperation} {
		if _, err := runner.Run(context.Background(), Command{Dir: dir, Scope: scope}); err != nil {
			t.Fatalf("Run(scope %d) error = %v", scope, err)
		}
	}

	if len(remaining) != 2 {
		t.Fatalf("factory calls = %d, want 2", len(remaining))
	}
	if remaining[0] < 4*time.Second || remaining[0] > localTimeout {
		t.Errorf("local deadline remaining = %v, want near %v", remaining[0], localTimeout)
	}
	if remaining[1] < 8*time.Second || remaining[1] > networkTimeout {
		t.Errorf("network deadline remaining = %v, want near %v", remaining[1], networkTimeout)
	}
}

func TestCommandRunnerPreservesCancellation(t *testing.T) {
	runner := newCommandRunner("git", time.Second, time.Second, 1024,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return helperCommand(ctx, "wait")
		})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := runner.Run(ctx, Command{Dir: t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if result != (Result{}) {
		t.Fatalf("Run() result = %#v on failure, want zero result", result)
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != CodeCanceled {
		t.Fatalf("Run() error = %#v, want SafeError code %q", err, CodeCanceled)
	}
}

func TestCommandRunnerPreservesDeadline(t *testing.T) {
	runner := newCommandRunner("git", time.Second, time.Second, 1024,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return helperCommand(ctx, "wait")
		})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := runner.Run(ctx, Command{Dir: t.TempDir()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want context.DeadlineExceeded", err)
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.Code != CodeTimedOut {
		t.Fatalf("Run() error = %#v, want SafeError code %q", err, CodeTimedOut)
	}
}

func TestCommandRunnerBoundsOutputWhileConsumingWrites(t *testing.T) {
	const limit = 32
	runner := newCommandRunner("git", time.Second, time.Second, limit,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return helperCommand(ctx, "output", "4096", "4096")
		})

	result, err := runner.Run(context.Background(), Command{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Stdout) != limit || result.Stdout != strings.Repeat("o", limit) || !result.StdoutTruncated {
		t.Errorf("stdout = %q (truncated %t), want %d o bytes and truncated", result.Stdout, result.StdoutTruncated, limit)
	}
	if len(result.Stderr) != limit || result.Stderr != strings.Repeat("e", limit) || !result.StderrTruncated {
		t.Errorf("stderr = %q (truncated %t), want %d e bytes and truncated", result.Stderr, result.StderrTruncated, limit)
	}
}

func TestCommandRunnerRedactsSuccessAndHTTPUserinfo(t *testing.T) {
	const secret = "exact-token-value"
	diagnostic := "token=" + secret + " fetch https://alice:password@example.com/repo.git"
	runner := newCommandRunner("git", time.Second, time.Second, 1024,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return helperCommand(ctx, "stderr", diagnostic)
		})

	result, err := runner.Run(context.Background(), Command{Dir: t.TempDir(), Secrets: []string{secret}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, forbidden := range []string{secret, "alice", "password"} {
		if strings.Contains(result.Stderr, forbidden) {
			t.Errorf("stderr leaks %q: %q", forbidden, result.Stderr)
		}
	}
	if !strings.Contains(result.Stderr, "[REDACTED_REMOTE]") || !strings.Contains(result.Stderr, "https://[REDACTED]@example.com") {
		t.Errorf("stderr = %q, want exact-secret and userinfo markers", result.Stderr)
	}
}

func TestCommandRunnerRedactsBeforeDiagnosticTruncation(t *testing.T) {
	const limit = 64
	const secret = "qZ9-very-secret-token"
	diagnostic := strings.Repeat("x", limit-2) + secret + "tail"
	runner := newCommandRunner("git", time.Second, time.Second, limit,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return helperCommand(ctx, "fail", diagnostic)
		})

	result, err := runner.Run(context.Background(), Command{Dir: t.TempDir(), Secrets: []string{secret}})
	if err == nil {
		t.Fatal("Run() error = nil, want failure")
	}
	if result != (Result{}) {
		t.Fatalf("Run() result = %#v on failure, want zero result", result)
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) {
		t.Fatalf("Run() error type = %T, want *SafeError", err)
	}
	diagnostic = safeErr.Diagnostic()
	if len(diagnostic) > limit {
		t.Fatalf("diagnostic length = %d, want <= %d", len(diagnostic), limit)
	}
	if strings.Contains(diagnostic, secret) {
		t.Fatalf("diagnostic leaks secret: %q", diagnostic)
	}
	for i := 1; i <= len(secret); i++ {
		if strings.HasSuffix(diagnostic, secret[:i]) {
			t.Fatalf("diagnostic ends with secret prefix %q: %q", secret[:i], diagnostic)
		}
	}
}

func TestCommandRunnerMapsExecutableNotFound(t *testing.T) {
	runner := newCommandRunner("igonotes-git-executable-that-does-not-exist", time.Second, time.Second, 1024, exec.CommandContext)

	result, err := runner.Run(context.Background(), Command{Dir: t.TempDir()})
	if result != (Result{}) {
		t.Fatalf("Run() result = %#v on failure, want zero result", result)
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) {
		t.Fatalf("Run() error type = %T, want *SafeError", err)
	}
	if safeErr.Code != CodeUnavailable || safeErr.Message != "Git executable is unavailable" {
		t.Fatalf("Run() error = %#v, want unavailable error", safeErr)
	}
}

func TestCommandRunnerCapturesExitCodeAndHidesFailureOutput(t *testing.T) {
	runner := newCommandRunner("git", time.Second, time.Second, 1024,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return helperCommand(ctx, "fail", "ordinary failure")
		})

	result, err := runner.Run(context.Background(), Command{Dir: t.TempDir()})
	if result != (Result{}) {
		t.Fatalf("Run() result = %#v on failure, want zero result", result)
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) {
		t.Fatalf("Run() error type = %T, want *SafeError", err)
	}
	if safeErr.Code != CodeCommandFailed || safeErr.ExitCode != 23 {
		t.Fatalf("Run() error = %#v, want command failed with exit code 23", safeErr)
	}
	if safeErr.Diagnostic() != "ordinary failure" {
		t.Fatalf("diagnostic = %q, want helper stderr", safeErr.Diagnostic())
	}
}

func TestCommandRunnerCanonicalizesDirectory(t *testing.T) {
	realDir := t.TempDir()
	parent := t.TempDir()
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	var gotCommand *exec.Cmd
	factory := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := helperCommand(ctx, "success")
		gotCommand = cmd
		return cmd
	}
	runner := newCommandRunner("git", time.Second, time.Second, 1024, factory)

	if _, err := runner.Run(context.Background(), Command{Dir: filepath.Join(parent, ".", "linked")}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	canonical, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	if gotCommand.Dir != canonical {
		t.Fatalf("cmd.Dir = %q, want canonical %q", gotCommand.Dir, canonical)
	}
}

func TestCommandRunnerRejectsInvalidDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(file, []byte("note"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		dir  string
	}{
		{name: "missing", dir: filepath.Join(t.TempDir(), "missing")},
		{name: "file", dir: file},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			runner := newCommandRunner("git", time.Second, time.Second, 1024,
				func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					called = true
					return helperCommand(ctx, "success")
				})
			result, err := runner.Run(context.Background(), Command{Dir: test.dir})
			if err == nil {
				t.Fatal("Run() error = nil, want invalid directory error")
			}
			if called {
				t.Fatal("command factory called for invalid directory")
			}
			if result != (Result{}) {
				t.Fatalf("Run() result = %#v, want zero result", result)
			}
			var safeErr *SafeError
			if !errors.As(err, &safeErr) || safeErr.Code != CodeCommandFailed {
				t.Fatalf("Run() error = %#v, want command failed SafeError", err)
			}
			if strings.Contains(safeErr.Error(), test.dir) {
				t.Fatalf("safe error leaks directory %q: %q", test.dir, safeErr.Error())
			}
		})
	}
}

func TestCommandRunnerDefaults(t *testing.T) {
	runner := NewCommandRunner()
	if runner.executable != "git" || runner.localTimeout != DefaultLocalTimeout ||
		runner.networkTimeout != DefaultNetworkTimeout || runner.outputLimit != DefaultOutputLimit || runner.command == nil {
		t.Fatalf("NewCommandRunner() = %#v, want documented defaults", runner)
	}
	if AllowedGitProtocols != "file:http:https:ssh:git" {
		t.Fatalf("AllowedGitProtocols = %q", AllowedGitProtocols)
	}
}

func TestSafeErrorClassification(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		diagnostic  string
		wantCode    ErrorCode
		wantMessage string
	}{
		{name: "deadline has precedence", err: context.DeadlineExceeded, diagnostic: "authentication failed", wantCode: CodeTimedOut, wantMessage: "Git command timed out"},
		{name: "cancellation has precedence", err: context.Canceled, diagnostic: "index.lock", wantCode: CodeCanceled, wantMessage: "Git command was canceled"},
		{name: "authentication", err: errors.New("exit"), diagnostic: "fatal: Authentication failed", wantCode: CodeAuthentication, wantMessage: "Git authentication failed"},
		{name: "public key", err: errors.New("exit"), diagnostic: "Permission denied (publickey)", wantCode: CodeAuthentication, wantMessage: "Git authentication failed"},
		{name: "username", err: errors.New("exit"), diagnostic: "could not read Username", wantCode: CodeAuthentication, wantMessage: "Git authentication failed"},
		{name: "prompt", err: errors.New("exit"), diagnostic: "terminal prompts disabled", wantCode: CodeAuthentication, wantMessage: "Git authentication failed"},
		{name: "auth before unreachable", err: errors.New("exit"), diagnostic: "authentication failed; unable to access", wantCode: CodeAuthentication, wantMessage: "Git authentication failed"},
		{name: "host", err: errors.New("exit"), diagnostic: "Could not resolve host", wantCode: CodeRemoteUnreachable, wantMessage: "Git remote is unreachable"},
		{name: "access", err: errors.New("exit"), diagnostic: "unable to access remote", wantCode: CodeRemoteUnreachable, wantMessage: "Git remote is unreachable"},
		{name: "remote missing", err: errors.New("exit"), diagnostic: "Repository not found", wantCode: CodeRemoteUnreachable, wantMessage: "Git remote is unreachable"},
		{name: "remote not repository", err: errors.New("exit"), diagnostic: "does not appear to be a git repository", wantCode: CodeRemoteUnreachable, wantMessage: "Git remote is unreachable"},
		{name: "lock", err: errors.New("exit"), diagnostic: "fatal: Unable to create '.git/index.lock'", wantCode: CodeRepositoryLocked, wantMessage: "Git repository is locked"},
		{name: "not repository", err: errors.New("exit"), diagnostic: "fatal: not a git repository", wantCode: CodeNotRepository, wantMessage: "Directory is not a Git repository"},
		{name: "default", err: errors.New("exit"), diagnostic: "fatal: unknown failure", wantCode: CodeCommandFailed, wantMessage: "Git command failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyFailure(test.err, test.diagnostic)
			if got.Code != test.wantCode || got.Message != test.wantMessage {
				t.Fatalf("classifyFailure() = %#v, want code %q message %q", got, test.wantCode, test.wantMessage)
			}
			if errors.Is(test.err, context.Canceled) && !errors.Is(got, context.Canceled) {
				t.Fatal("cancellation not preserved through Unwrap")
			}
			if errors.Is(test.err, context.DeadlineExceeded) && !errors.Is(got, context.DeadlineExceeded) {
				t.Fatal("deadline not preserved through Unwrap")
			}
		})
	}
}

func TestSafeErrorNeverLeaksDiagnostic(t *testing.T) {
	const diagnostic = "private diagnostic token"
	const causeText = "private cause token"
	cause := errors.New(causeText)
	err := &SafeError{
		Code: CodeCommandFailed, Message: "Git command failed", Field: "branch", ExitCode: 7,
		diagnostic: diagnostic, cause: cause,
	}

	if err.Error() != "Git command failed" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if err.Diagnostic() != diagnostic {
		t.Fatalf("Diagnostic() = %q, want private diagnostic", err.Diagnostic())
	}
	if !errors.Is(err, cause) {
		t.Fatal("Unwrap() does not preserve cause")
	}
	for _, formatted := range []string{fmt.Sprint(err), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%q", err)} {
		if strings.Contains(formatted, diagnostic) || strings.Contains(formatted, causeText) {
			t.Fatalf("formatted SafeError leaks private data: %q", formatted)
		}
	}
	encoded, encodeErr := json.Marshal(err)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if strings.Contains(string(encoded), diagnostic) || strings.Contains(string(encoded), causeText) {
		t.Fatalf("JSON SafeError leaks private data: %s", encoded)
	}
}

func TestSafeErrorCodes(t *testing.T) {
	want := map[ErrorCode]string{
		CodeUnavailable:        "git_unavailable",
		CodeUnsupportedVersion: "git_version_unsupported",
		CodeAuthentication:     "auth_failed",
		CodeRemoteUnreachable:  "remote_unreachable",
		CodeIdentityMissing:    "identity_missing",
		CodeInvalidBranch:      "invalid_branch",
		CodeRepositoryRoot:     "repository_root_mismatch",
		CodeRepositoryLocked:   "repository_locked",
		CodeCommandFailed:      "git_command_failed",
		CodeTimedOut:           "git_timeout",
		CodeCanceled:           "git_canceled",
		CodeNotRepository:      "not_a_git_repository",
	}
	if len(want) != 12 {
		t.Fatalf("error code coverage = %d, want 12", len(want))
	}
	for code, value := range want {
		if string(code) != value {
			t.Errorf("error code = %q, want %q", code, value)
		}
	}
}
