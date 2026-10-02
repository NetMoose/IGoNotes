package git

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const helperTimeout = 10 * time.Second

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
	case "redact-adversarial":
		const size = 512 * 1024
		secret := strings.Repeat("a", size) + "b"
		// Every candidate suffix shares almost its entire prefix, then differs
		// at the final byte. Repeated HasSuffix comparisons become quadratic.
		text := strings.Repeat("a", size) + "c"
		if got := redact(text, []string{secret}); got != text {
			t.Fatal("large nonmatching diagnostic changed")
		}
		if got := redact(strings.Repeat("a", size), []string{secret}); got != "[REDACTED_REMOTE]" {
			t.Fatal("large matching credential prefix was not sanitized")
		}
		_, _ = io.WriteString(os.Stdout, "bounded redaction passed")
	case "redact-multivariant-adversarial":
		size, err := strconv.Atoi(args[1])
		if err != nil {
			t.Fatal("invalid adversarial size")
		}
		shared := strings.Repeat("a", size)
		secrets := []string{shared + "b", shared + "d"}
		near := shared + "c"
		for _, entry := range []struct{ text, want string }{
			{near, near},
			{shared, "[REDACTED_REMOTE]"},
			{secrets[0] + " " + secrets[1], "[REDACTED_REMOTE] [REDACTED_REMOTE]"},
			{near + " " + secrets[0], near + " [REDACTED_REMOTE]"},
		} {
			if got := redact(entry.text, secrets); got != entry.want {
				t.Fatal("large multivariant redaction changed security behavior")
			}
		}
		_, _ = io.WriteString(os.Stdout, "bounded multivariant redaction passed")
	case "fail":
		_, _ = io.WriteString(os.Stderr, args[1])
		os.Exit(23)
	case "wait":
		time.Sleep(30 * time.Second)
	case "tree-parent", "tree-parent-exit", "tree-parent-exit-fail":
		child := helperCommand(context.Background(), "tree-child", args[1]+".child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if args[0] == "tree-parent" {
			defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
		}
		pids, _ := json.Marshal(helperPIDs{Parent: os.Getpid(), Child: child.Process.Pid})
		if err := os.WriteFile(args[1], pids, 0o600); err != nil {
			t.Fatal(err)
		}
		if args[0] == "tree-parent-exit" {
			return
		}
		if args[0] == "tree-parent-exit-fail" {
			os.Exit(23)
		}
		time.Sleep(30 * time.Second)
	case "tree-child":
		if err := os.WriteFile(args[1], []byte("running"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
	case "stdin-env":
		contents, err := io.ReadAll(os.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(struct {
			Env   []string
			Stdin string
		}{os.Environ(), string(contents)}); err != nil {
			t.Fatal(err)
		}
	case "stdin-sha256":
		contents, err := io.ReadAll(os.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(os.Stdout, "%x", sha256.Sum256(contents))
	default:
		t.Fatalf("unknown helper action %q", args[0])
	}
}

type helperPIDs struct {
	Parent int
	Child  int
}

func waitForHelperPIDs(t *testing.T, path string) helperPIDs {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		var pids helperPIDs
		if err == nil && json.Unmarshal(data, &pids) == nil && pids.Parent > 0 && pids.Child > 0 {
			return pids
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper did not publish parent and child PIDs within 5s")
	return helperPIDs{}
}

func TestCommandRunnerStdinEOFAndEnvironment(t *testing.T) {
	for _, payload := range []string{"", "explicit payload"} {
		t.Run(payload, func(t *testing.T) {
			var captured *exec.Cmd
			runner := newCommandRunner("git", helperTimeout, helperTimeout, 64*1024,
				func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					captured = helperCommand(ctx, "stdin-env")
					return captured
				})
			command := Command{Dir: t.TempDir(), ReadOnly: true}
			if payload != "" {
				command.Stdin = strings.NewReader(payload)
			}
			result, err := runner.Run(context.Background(), command)
			if err != nil {
				t.Fatal(err)
			}
			if captured.Stdin != command.Stdin {
				t.Fatal("stdin was replaced")
			}
			var observation struct {
				Env   []string
				Stdin string
			}
			if err := json.NewDecoder(strings.NewReader(result.Stdout)).Decode(&observation); err != nil {
				t.Fatal(err)
			}
			if observation.Stdin != payload {
				t.Fatalf("stdin = %q", observation.Stdin)
			}
			assertEnvValues(t, observation.Env, "GIT_TERMINAL_PROMPT", []string{"0"})
			assertEnvValues(t, observation.Env, "LC_ALL", []string{"C"})
			assertEnvValues(t, observation.Env, "GIT_OPTIONAL_LOCKS", []string{"0"})
		})
	}
}

type testProcessTree struct {
	start     func(*exec.Cmd) error
	terminate func(*exec.Cmd) error
	close     func() error
}

func (tree *testProcessTree) Start(cmd *exec.Cmd) error     { return tree.start(cmd) }
func (tree *testProcessTree) Terminate(cmd *exec.Cmd) error { return tree.terminate(cmd) }
func (tree *testProcessTree) Close() error                  { return tree.close() }

func TestCommandRunnerProcessTreeLifecycle(t *testing.T) {
	var cmd *exec.Cmd
	closed := 0
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			cmd = helperCommand(ctx, "success")
			return cmd
		})
	runner.newProcessTree = func() (processTree, error) {
		return &testProcessTree{
			start: func(cmd *exec.Cmd) error {
				if cmd.Cancel == nil || cmd.WaitDelay != 5*time.Second {
					t.Error("missing tree cancellation or 5s WaitDelay")
				}
				return cmd.Start()
			},
			terminate: func(cmd *exec.Cmd) error { return cmd.Process.Kill() },
			close: func() error {
				closed++
				if cmd.ProcessState == nil {
					t.Error("Close before Wait")
				}
				return nil
			},
		}, nil
	}
	if _, err := runner.Run(context.Background(), Command{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("Close calls = %d", closed)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("runner did not reap command")
	}
}

func TestCommandRunnerContextWinsSuccessfulExitRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd { return helperCommand(ctx, "success") })
	runner.newProcessTree = func() (processTree, error) {
		return &testProcessTree{
			start:     func(cmd *exec.Cmd) error { err := cmd.Start(); cancel(); return err },
			terminate: func(*exec.Cmd) error { return os.ErrProcessDone },
			close:     func() error { return nil },
		}, nil
	}
	if _, err := runner.Run(ctx, Command{Dir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want canceled despite successful exit", err)
	}
}

func TestCommandRunnerCancelsDescendantWhileDrainingPipes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exited parent live child PIDs.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nativeTree, err := newProcessTree()
	if err != nil {
		t.Fatal(err)
	}
	defer nativeTree.Close()
	started := make(chan *exec.Cmd, 1)
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return helperCommand(ctx, "tree-parent-exit", path)
		})
	runner.newProcessTree = func() (processTree, error) {
		return &testProcessTree{
			start: func(cmd *exec.Cmd) error {
				err := nativeTree.Start(cmd)
				if err == nil {
					started <- cmd
				}
				return err
			},
			terminate: nativeTree.Terminate,
			close:     nativeTree.Close,
		}, nil
	}
	done := make(chan error, 1)
	dir := t.TempDir()
	go func() { _, err := runner.Run(ctx, Command{Dir: dir}); done <- err }()
	returned := false
	defer func() {
		cancel()
		// Even the red regression must not leave a live orphan or blocked Run.
		data, err := os.ReadFile(path)
		var pids helperPIDs
		if err == nil && json.Unmarshal(data, &pids) == nil {
			for _, pid := range []int{pids.Child, pids.Parent} {
				if pid > 0 && !helperStopped(pid) {
					process, err := os.FindProcess(pid)
					if err == nil {
						_ = process.Kill()
						_ = process.Release()
					}
				}
			}
		}
		if !returned {
			select {
			case <-done:
			case <-time.After(helperTimeout):
				t.Error("runner did not finish cleanup")
			}
		}
	}()
	var cmd *exec.Cmd
	select {
	case cmd = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not start")
	}
	pids := waitForHelperPIDs(t, path)
	// Process.Signal uses os.Process's synchronized state. Unix Wait marks it
	// done; Windows Wait releases its handle (Signal then returns EINVAL).
	// Neither requires racing on cmd.ProcessState while Wait drains the pipes.
	parentReaped := func() bool {
		err := cmd.Process.Signal(syscall.Signal(0))
		return errors.Is(err, os.ErrProcessDone) || runtime.GOOS == "windows" && errors.Is(err, syscall.EINVAL)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if parentReaped() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !parentReaped() {
		t.Fatal("parent was not reaped")
	}
	if helperStopped(pids.Child) {
		t.Fatal("child stopped before cancellation")
	}
	select {
	case <-done:
		returned = true
		t.Fatal("Run returned before draining descendant pipes")
	default:
	}
	cancel()
	select {
	case err := <-done:
		returned = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation waited for the 5s pipe-drain timeout")
	}
	assertHelpersStopped(t, pids)
}

func TestCommandRunnerProcessTreeCancellationLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan *exec.Cmd, 1)
	var terminated atomic.Int32
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd { return helperCommand(ctx, "wait") })
	runner.newProcessTree = func() (processTree, error) {
		return &testProcessTree{
			start: func(cmd *exec.Cmd) error {
				err := cmd.Start()
				if err == nil {
					started <- cmd
				}
				return err
			},
			terminate: func(cmd *exec.Cmd) error { terminated.Add(1); return cmd.Process.Kill() },
			close: func() error {
				if terminated.Load() != 1 {
					t.Errorf("Terminate calls at Close = %d, want 1", terminated.Load())
				}
				return nil
			},
		}, nil
	}
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() { _, err := runner.Run(ctx, Command{Dir: dir}); done <- err }()
	var cmd *exec.Cmd
	select {
	case cmd = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not start")
	}
	defer func() { _ = cmd.Process.Kill() }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not finish")
	}
	// A retained cancellation function must not signal a reused PID/group after
	// Wait/Close, and overlapping exec/runner cancellation must terminate once.
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("late Cancel = %v", err)
	}
	if terminated.Load() != 1 {
		t.Fatalf("Terminate calls = %d, want 1", terminated.Load())
	}
}

func TestCommandRunnerCleansDescendantsAfterPipeWaitDelay(t *testing.T) {
	for _, action := range []string{"tree-parent-exit", "tree-parent-exit-fail"} {
		t.Run(action, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "exited parent live child PIDs.json")
			t.Cleanup(func() {
				data, err := os.ReadFile(path)
				var pids helperPIDs
				if err == nil && json.Unmarshal(data, &pids) == nil && pids.Child > 0 && !helperStopped(pids.Child) {
					process, err := os.FindProcess(pids.Child)
					if err == nil {
						_ = process.Kill()
						_ = process.Release()
					}
				}
			})
			nativeTree, err := newProcessTree()
			if err != nil {
				t.Fatal(err)
			}
			defer nativeTree.Close()
			var cmd *exec.Cmd
			terminated := 0
			checkedBeforeReturn := false
			runner := newCommandRunner("git", 20*time.Second, 20*time.Second, 1024,
				func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					cmd = helperCommand(ctx, action, path)
					return cmd
				})
			runner.newProcessTree = func() (processTree, error) {
				return &testProcessTree{
					start:     nativeTree.Start,
					terminate: func(cmd *exec.Cmd) error { terminated++; return nativeTree.Terminate(cmd) },
					close: func() error {
						// This runs inside Run, before releasing the controller. Poll
						// only for signal delivery; never kill from the assertion.
						pids := waitForHelperPIDs(t, path)
						deadline := time.Now().Add(time.Second)
						for time.Now().Before(deadline) && !helperStopped(pids.Child) {
							time.Sleep(time.Millisecond)
						}
						if !helperStopped(pids.Child) {
							t.Error("live descendant at controller release, before Run returns")
						}
						if cmd.ProcessState == nil {
							t.Error("main process was not reaped")
						}
						checkedBeforeReturn = true
						return nativeTree.Close()
					},
				}, nil
			}
			begin := time.Now()
			result, err := runner.Run(context.Background(), Command{Dir: t.TempDir()})
			if time.Since(begin) < processWaitDelay {
				t.Error("Run did not wait for pipe WaitDelay")
			}
			var safe *SafeError
			if !errors.As(err, &safe) || safe.Code != CodeCommandFailed || safe.Message != "Git command failed" {
				t.Fatalf("Run error = %v", err)
			}
			if action == "tree-parent-exit-fail" && safe.ExitCode != 23 {
				t.Errorf("ExitCode = %d, want 23", safe.ExitCode)
			}
			if result != (Result{}) {
				t.Errorf("result = %#v, want zero failure result", result)
			}
			if !checkedBeforeReturn {
				t.Fatal("did not check descendant before Run returned")
			}
			if terminated != 1 {
				t.Errorf("Terminate calls = %d, want 1", terminated)
			}
			if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("late Cancel = %v", err)
			}
			if terminated != 1 {
				t.Error("late Cancel signaled a retired process group")
			}
		})
	}
}

func TestCommandRunnerSafeProcessErrors(t *testing.T) {
	sentinel := errors.New("sentinel https://user:password@example.invalid PID 12345")
	for _, stage := range []string{"prepare", "attach", "attach after create"} {
		t.Run(stage, func(t *testing.T) {
			closed := 0
			var cmd *exec.Cmd
			runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
				func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					cmd = helperCommand(ctx, "success")
					return cmd
				})
			runner.newProcessTree = func() (processTree, error) {
				if stage == "prepare" {
					return nil, sentinel
				}
				return &testProcessTree{
					start: func(cmd *exec.Cmd) error {
						if stage == "attach after create" {
							if err := cmd.Start(); err != nil {
								return err
							}
							if err := cmd.Wait(); err != nil {
								return err
							}
						}
						return newSafeProcessError("attach Git process tree", sentinel)
					},
					terminate: func(*exec.Cmd) error { t.Error("Terminate on failed Start"); return nil },
					close:     func() error { closed++; return nil },
				}, nil
			}
			_, err := runner.Run(context.Background(), Command{Dir: t.TempDir()})
			assertSafeProcessError(t, err, sentinel)
			if stage != "prepare" && closed != 1 {
				t.Fatalf("Close calls = %d", closed)
			}
			if stage == "attach after create" {
				if cmd.ProcessState == nil {
					t.Fatal("failed Start did not reap process")
				}
			} else if cmd.ProcessState != nil {
				t.Fatal("unexpected ProcessState after failed Start")
			}
		})
	}
}

func assertSafeProcessError(t *testing.T, err, cause error) {
	t.Helper()
	var safe *SafeError
	if !errors.As(err, &safe) || safe.Code != CodeCommandFailed || safe.Message != "Git command failed" {
		t.Fatalf("error = %v, want fixed command failed SafeError", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("private cause was lost")
	}
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, public := range []string{err.Error(), fmt.Sprintf("%+v", err), string(encoded), safe.Diagnostic()} {
		for _, private := range []string{"sentinel", "https://", "password", "12345"} {
			if strings.Contains(public, private) {
				t.Fatalf("private data leaked: %q", public)
			}
		}
	}
}

func TestCommandRunnerForwardsStdinWithoutExposingIt(t *testing.T) {
	secret := "stdin-secret-marker-5f5d1e8e"
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return helperCommand(ctx, "stdin-sha256")
	})

	result, err := runner.Run(context.Background(), Command{
		Dir: t.TempDir(), Args: []string{"hash-object", "--stdin"}, Scope: LocalOperation, ReadOnly: true,
		Stdin: strings.NewReader(secret),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
	if !strings.HasPrefix(result.Stdout, want) {
		t.Errorf("stdout = %q, want SHA-256 prefix %q", result.Stdout, want)
	}
	if strings.Contains(result.Stdout, secret) || strings.Contains(result.Stderr, secret) {
		t.Fatalf("result exposes stdin secret: %#v", result)
	}
}

func helperCommand(ctx context.Context, action string, args ...string) *exec.Cmd {
	commandArgs := []string{"-test.run=^TestGitRunnerHelper$", "--", action}
	commandArgs = append(commandArgs, args...)
	return exec.CommandContext(ctx, os.Args[0], commandArgs...)
}

func TestRedactAdversarialLargeValuesAreBounded(t *testing.T) {
	// Run in a killable subprocess so a quadratic regression cannot stall the
	// suite. Use the normal helper deadline to include process startup and race
	// detector shutdown on hosted runners, not just the redaction itself.
	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()
	output, err := helperCommand(ctx, "redact-adversarial").Output()
	if ctx.Err() != nil {
		t.Fatalf("adversarial redaction exceeded the %s helper deadline", helperTimeout)
	}
	if err != nil || !strings.HasPrefix(string(output), "bounded redaction passed") {
		t.Fatal("adversarial redaction failed; helper output is intentionally private")
	}
}

func TestRedactMultivariantAdversarialLargeValuesAreBounded(t *testing.T) {
	for _, size := range []int{32 * 1024, 512 * 1024, 1024 * 1024} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			// This bounds the entire subprocess, including race-detector overhead.
			ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
			defer cancel()
			start := time.Now()
			output, err := helperCommand(ctx, "redact-multivariant-adversarial", strconv.Itoa(size)).Output()
			if ctx.Err() != nil {
				t.Fatalf("multivariant adversarial redaction exceeded the %s helper deadline", helperTimeout)
			}
			if err != nil || !strings.HasPrefix(string(output), "bounded multivariant redaction passed") {
				t.Fatal("multivariant redaction failed; helper output is intentionally private")
			}
			t.Logf("two-pattern near-match and security checks: %d bytes in %s", size, time.Since(start))
		})
	}
}

func TestCommandRunnerPassesArgumentsWithoutShell(t *testing.T) {
	var gotName string
	var gotArgs []string
	factory := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return helperCommand(ctx, "success")
	}
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024, factory)
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
					"KEEP=value", "SSH_AUTH_SOCK=/safe/agent.sock",
					"HTTP_PROXY=http://proxy.invalid", "HTTPS_PROXY=https://proxy.invalid",
					"NO_PROXY=localhost", "SSL_CERT_FILE=/safe/ca.pem", "SSL_CERT_DIR=/safe/certs",
					"GIT_TERMINAL_PROMPT=1", "GIT_TERMINAL_PROMPT=hostile",
					"Git_Terminal_Prompt=mixed-hostile",
					"LC_ALL=hostile", "LC_ALL=also-hostile",
					"lc_All=mixed-hostile",
					"GIT_ALLOW_PROTOCOL=ext:file", "GIT_ALLOW_PROTOCOL=ext:ssh",
					"Git_Allow_Protocol=mixed-hostile",
					"GIT_OPTIONAL_LOCKS=1", "GIT_OPTIONAL_LOCKS=hostile",
					"git_optional_locks=mixed-hostile",
					"GIT_NO_LAZY_FETCH=0", "GIT_NO_LAZY_FETCH=hostile",
					"Git_No_Lazy_Fetch=mixed-hostile",
					"GIT_DIR=/hostile/repository", "git_work_tree=/hostile/worktree",
					"Git_Common_Dir=/hostile/common", "GIT_INDEX_FILE=/hostile/index",
					"git_object_directory=/hostile/objects",
					"GIT_ALTERNATE_OBJECT_DIRECTORIES=/hostile/alternates",
					"Git_Config_Count=1", "GIT_CONFIG_KEY_0=core.pager",
					"git_config_value_0=hostile", "GIT_CONFIG_GLOBAL=/hostile/config",
					"Git_Ssh=/hostile/ssh", "GIT_SSH_COMMAND=/hostile/ssh-command",
					"git_askpass=/hostile/git-askpass", "GIT_PROXY_COMMAND=/hostile/proxy",
					"Git_External_Diff=/hostile/diff", "GIT_PAGER=/hostile/pager",
					"Ssh_Askpass=/hostile/ssh-askpass", "SSH_ASKPASS_REQUIRE=force",
				}
				return cmd
			}
			runner := newCommandRunner("git", helperTimeout, helperTimeout, 16*1024, factory)

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
			assertEnvValues(t, observation.Env, "GIT_NO_LAZY_FETCH", []string{"1"})
			assertEnvValues(t, observation.Env, "SSH_ASKPASS", nil)
			assertEnvValues(t, observation.Env, "SSH_ASKPASS_REQUIRE", []string{"never"})
			assertEnvValues(t, observation.Env, "SSH_AUTH_SOCK", []string{"/safe/agent.sock"})
			assertEnvValues(t, observation.Env, "HTTP_PROXY", []string{"http://proxy.invalid"})
			assertEnvValues(t, observation.Env, "HTTPS_PROXY", []string{"https://proxy.invalid"})
			assertEnvValues(t, observation.Env, "NO_PROXY", []string{"localhost"})
			assertEnvValues(t, observation.Env, "SSL_CERT_FILE", []string{"/safe/ca.pem"})
			assertEnvValues(t, observation.Env, "SSL_CERT_DIR", []string{"/safe/certs"})
			assertOnlyCanonicalGitEnvironment(t, observation.Env, test.readOnly)
		})
	}
}

func assertOnlyCanonicalGitEnvironment(t *testing.T, env []string, readOnly bool) {
	t.Helper()
	want := map[string]string{
		"git_terminal_prompt": "0",
		"git_allow_protocol":  AllowedGitProtocols,
		"git_no_lazy_fetch":   "1",
	}
	if readOnly {
		want["git_optional_locks"] = "0"
	}
	seen := make(map[string]int, len(want))
	for _, entry := range env {
		key, value, found := strings.Cut(entry, "=")
		key = strings.ToLower(key)
		if !found || !strings.HasPrefix(key, "git_") {
			continue
		}
		wantValue, allowed := want[key]
		if !allowed {
			t.Errorf("unexpected Git environment key %q", key)
			continue
		}
		seen[key]++
		if value != wantValue {
			t.Errorf("environment %s = %q, want %q", key, value, wantValue)
		}
	}
	for key := range want {
		if seen[key] != 1 {
			t.Errorf("environment %s count = %d, want 1", key, seen[key])
		}
	}
}

func assertEnvValues(t *testing.T, env []string, key string, want []string) {
	t.Helper()
	var got []string
	for _, entry := range env {
		entryKey, value, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(entryKey, key) {
			got = append(got, value)
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
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
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
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
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
	runner := newCommandRunner("git", helperTimeout, helperTimeout, limit,
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
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
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

func assertNoSecretLeak(t testing.TB, public string, secrets []string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(public, secret) {
			t.Fatal("public output contains a complete secret")
		}
		for n := 4; n <= len(secret); n++ {
			if strings.HasSuffix(public, secret[:n]) {
				t.Fatal("public output ends in a secret prefix")
			}
		}
	}
}

func TestCommandRunnerSecretVariantMatrix(t *testing.T) {
	const remote = "https://matrixUser:matrixPassword@example.invalid/private.git"
	variants := []string{remote, "matrixUser:matrixPassword", "matrixUser", "matrixPassword"}
	corpus := []struct{ name, diagnostic, secret string }{
		{"full URL", remote, remote},
		{"authentication trailing slash", "fatal: Authentication failed for '" + remote + "/'", remote},
		{"userinfo token", "token=matrixUser:matrixPassword", remote},
		{"username token", "token=matrixUser", remote},
		{"password token", "token=matrixPassword", remote},
		{"authorization Basic", "Authorization: Basic matrixPassword", remote},
		{"scp path", "git@example.invalid:private/scpSecret.git", "scpSecret"},
	}
	for _, entry := range corpus {
		t.Run(entry.name, func(t *testing.T) {
			for _, action := range []string{"stderr", "fail"} {
				t.Run(action, func(t *testing.T) {
					for _, limit := range []int{64, 4096} {
						t.Run(strconv.Itoa(limit), func(t *testing.T) {
							// Place a complete credential across the diagnostic boundary.
							diagnostic := entry.diagnostic
							if limit == 64 {
								diagnostic = strings.Repeat("~", limit-8) + diagnostic
							}
							runner := newCommandRunner("git", helperTimeout, helperTimeout, limit,
								func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
									return helperCommand(ctx, action, diagnostic)
								})
							result, err := runner.Run(context.Background(), Command{Dir: t.TempDir(), Secrets: []string{entry.secret}})
							resultJSON, marshalErr := json.Marshal(result)
							if marshalErr != nil {
								t.Fatal(marshalErr)
							}
							public := []string{result.Stderr, fmt.Sprintf("%+v", result), fmt.Sprintf("%#v", result), string(resultJSON)}
							if action == "fail" {
								var safe *SafeError
								if !errors.As(err, &safe) || result != (Result{}) || safe.ExitCode != 23 {
									t.Fatal("failure did not preserve SafeError/result contract")
								}
								if entry.name == "authentication trailing slash" && limit == 4096 && safe.Code != CodeAuthentication {
									t.Fatal("authentication classification changed")
								}
								public = append(public, safe.Error(), safe.Diagnostic(), fmt.Sprint(safe), fmt.Sprintf("%v", safe), fmt.Sprintf("%+v", safe), fmt.Sprintf("%#v", safe), fmt.Sprintf("%q", safe))
								encoded, marshalErr := json.Marshal(safe)
								if marshalErr != nil {
									t.Fatal(marshalErr)
								}
								public = append(public, string(encoded))
								if len(safe.Diagnostic()) > limit {
									t.Fatal("diagnostic exceeds limit")
								}
							} else if err != nil || len(result.Stderr) > limit {
								t.Fatal("success stderr contract changed")
							}
							for _, output := range public {
								assertNoSecretLeak(t, output, append(variants, entry.secret, "scpSecret"))
							}
						})
					}
				})
			}
		})
	}
}

type unformattableCause struct{ calls *int }

func (c unformattableCause) Error() string          { *c.calls++; return "private cause" }
func (c unformattableCause) Format(fmt.State, rune) { *c.calls++ }

func TestSafeErrorNeverFormatsCause(t *testing.T) {
	calls := 0
	cause := unformattableCause{calls: &calls}
	safe := &SafeError{Code: CodeCommandFailed, Message: "Git command failed", diagnostic: "private diagnostic", cause: cause}
	for _, verb := range []string{"%s", "%v", "%+v", "%#v", "%q"} {
		output := fmt.Sprintf(verb, safe)
		if strings.Contains(output, "private") {
			t.Fatal("format exposes private state")
		}
	}
	if _, err := json.Marshal(safe); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("public formatting invoked private cause")
	}
	if safe.Unwrap() != cause {
		t.Fatal("cause contract changed")
	}
}

func TestRedactKeepsUnrelatedShortWords(t *testing.T) {
	text := "an at to on user password"
	if got := redact(text, []string{"https://an:to@example.invalid/repo"}); got != text {
		t.Fatal("redaction changed unrelated words")
	}
	if got := redact("https://an:to@example.invalid/repo", nil); strings.Contains(got, "an:to") {
		t.Fatal("HTTP userinfo stripping was lost")
	}
}

func TestCommandRunnerRedactsCaptureTailAfterEarlierReplacement(t *testing.T) {
	const password = "matrixPasswordLongCredential"
	const remote = "https://matrixUser:" + password + "@host/r"
	const limit = 64
	// Earlier replacements shrink the output, bringing a partially captured
	// credential back inside the public diagnostic limit.
	text := remote + " " + remote + " " + password
	for _, action := range []string{"stderr", "fail"} {
		t.Run(action, func(t *testing.T) {
			runner := newCommandRunner("git", helperTimeout, helperTimeout, limit,
				func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					return helperCommand(ctx, action, text)
				})
			result, err := runner.Run(context.Background(), Command{Dir: t.TempDir(), Secrets: []string{remote}})
			output := result.Stderr
			if action == "fail" {
				var safe *SafeError
				if !errors.As(err, &safe) {
					t.Fatal("missing SafeError")
				}
				output = safe.Diagnostic()
			} else if err != nil {
				t.Fatal("unexpected failure")
			}
			assertNoSecretLeak(t, output, []string{remote, "matrixUser:" + password, "matrixUser", password})
		})
	}
}

func TestCommandRunnerVariantMarkerCrossesDiagnosticLimit(t *testing.T) {
	const remote = "https://matrixUser:matrixPassword@example.invalid/private.git"
	for _, variant := range []string{remote, "matrixUser:matrixPassword", "matrixUser", "matrixPassword"} {
		t.Run(variant, func(t *testing.T) {
			for _, action := range []string{"stderr", "fail"} {
				t.Run(action, func(t *testing.T) {
					const limit = 64
					const prefix = limit - 6
					text := strings.Repeat("~", prefix) + variant + " trailing bytes"
					runner := newCommandRunner("git", helperTimeout, helperTimeout, limit,
						func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
							return helperCommand(ctx, action, text)
						})
					result, err := runner.Run(context.Background(), Command{Dir: t.TempDir(), Secrets: []string{remote}})
					output := result.Stderr
					if action == "fail" {
						var safe *SafeError
						if !errors.As(err, &safe) {
							t.Fatal("missing SafeError")
						}
						output = safe.Diagnostic()
					} else if err != nil {
						t.Fatal("unexpected failure")
					}
					want := strings.Repeat("~", prefix) + "[REDAC"
					if output != want {
						t.Fatal("complete variant was not redacted before marker truncation")
					}
					assertNoSecretLeak(t, output, []string{variant})
				})
			}
		})
	}
}

func TestCommandRunnerRedactsBeforeDiagnosticTruncation(t *testing.T) {
	const limit = 64
	const secret = "qZ9-very-secret-token"
	diagnostic := strings.Repeat("x", limit-2) + secret + "tail"
	runner := newCommandRunner("git", helperTimeout, helperTimeout, limit,
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

func TestCommandRunnerRedactsTruncatedHTTPUserinfo(t *testing.T) {
	const limit = 32
	const credentialPrefix = "https://userZQ:pa"
	diagnostic := strings.Repeat("x", limit-len(credentialPrefix)) +
		"https://userZQ:passXY@example.com/repository.git"
	tests := []struct {
		name   string
		action string
	}{
		{name: "successful stderr", action: "stderr"},
		{name: "failure diagnostic", action: "fail"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := newCommandRunner("git", helperTimeout, helperTimeout, limit,
				func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					return helperCommand(ctx, test.action, diagnostic)
				})

			result, err := runner.Run(context.Background(), Command{Dir: t.TempDir()})
			redacted := result.Stderr
			if test.action == "stderr" {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				if !result.StderrTruncated {
					t.Fatal("StderrTruncated = false, want true")
				}
			} else {
				var safeErr *SafeError
				if !errors.As(err, &safeErr) {
					t.Fatalf("Run() error type = %T, want *SafeError", err)
				}
				if result != (Result{}) {
					t.Fatalf("Run() result = %#v on failure, want zero result", result)
				}
				redacted = safeErr.Diagnostic()
			}

			if len(redacted) > limit {
				t.Fatalf("redacted diagnostic length = %d, want <= %d", len(redacted), limit)
			}
			for _, forbidden := range []string{"userZQ", ":pa", credentialPrefix} {
				if strings.Contains(redacted, forbidden) {
					t.Fatalf("redacted diagnostic leaks HTTP credential prefix %q: %q", forbidden, redacted)
				}
			}
		})
	}
}

func TestCommandRunnerMapsExecutableNotFound(t *testing.T) {
	runner := newCommandRunner("igonotes-git-executable-that-does-not-exist", helperTimeout, helperTimeout, 1024, exec.CommandContext)

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
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
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
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024, factory)

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
			runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
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
		runner.networkTimeout != DefaultNetworkTimeout || runner.outputLimit != DefaultOutputLimit || runner.command == nil || runner.newProcessTree == nil {
		t.Fatalf("NewCommandRunner() = %#v, want documented defaults", runner)
	}
	if AllowedGitProtocols != "file:http:https:ssh:git" {
		t.Fatalf("AllowedGitProtocols = %q", AllowedGitProtocols)
	}
}

func TestCommandRunnerProductionEnvironmentIsolatesRepository(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("Git unavailable: %v", err)
	}
	selected := t.TempDir()
	poisoned := t.TempDir()
	runGitSetup(t, gitPath, selected, "init", "--quiet")
	runGitSetup(t, gitPath, poisoned, "init", "--quiet")

	hostileCommand := filepath.Join(t.TempDir(), "must-not-run")
	t.Setenv("GIT_DIR", filepath.Join(poisoned, ".git"))
	t.Setenv("GIT_WORK_TREE", poisoned)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "igonotes.injected")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	t.Setenv("GIT_SSH_COMMAND", hostileCommand)

	runner := NewCommandRunner()
	result, err := runner.Run(context.Background(), Command{
		Dir: selected, Args: []string{"rev-parse", "--show-toplevel"}, ReadOnly: true,
	})
	if err != nil {
		t.Fatalf("rev-parse through production runner: %v", err)
	}
	canonicalSelected, err := filepath.EvalSymlinks(selected)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Clean(strings.TrimSpace(result.Stdout)); got != canonicalSelected {
		t.Fatalf("rev-parse root = %q, want selected base %q", got, canonicalSelected)
	}

	result, err = runner.Run(context.Background(), Command{
		Dir: selected, Args: []string{"config", "--get", "igonotes.injected"}, ReadOnly: true,
	})
	if err == nil {
		t.Fatalf("injected Git config applied with output %q", result.Stdout)
	}
	var safeErr *SafeError
	if !errors.As(err, &safeErr) || safeErr.ExitCode != 1 {
		t.Fatalf("config lookup error = %#v, want Git exit code 1 for missing key", err)
	}
	if _, statErr := os.Stat(hostileCommand); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hostile command path status = %v, want not created", statErr)
	}
}

func TestCommandRunnerProductionEnvironmentBlocksGitSSHCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable marker script is Unix-only")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("Git unavailable: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "git-ssh-command-ran")
	script := filepath.Join(t.TempDir(), "git-ssh-command")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > \"$IGONOTES_GIT_SSH_MARKER\"\nexit 97\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(marker) })
	t.Setenv("PATH", filepath.Dir(script)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_SSH_COMMAND", filepath.Base(script))
	t.Setenv("IGONOTES_GIT_SSH_MARKER", marker)

	remote := "ssh://127.0.0.1:1/repo"
	baselineCtx, cancelBaseline := context.WithTimeout(context.Background(), 5*time.Second)
	baseline := exec.CommandContext(baselineCtx, gitPath, "ls-remote", remote)
	baseline.Dir = t.TempDir()
	baseline.Env = append(cleanGitSetupEnvironment(os.Environ()),
		"GIT_SSH_COMMAND="+filepath.Base(script),
	)
	_ = baseline.Run()
	cancelBaseline()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("unfiltered Git did not execute marker command: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	runCtx, cancelRun := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRun()
	_, err = NewCommandRunner().Run(runCtx, Command{
		Dir: t.TempDir(), Args: []string{"ls-remote", remote}, Scope: NetworkOperation, ReadOnly: true,
	})
	if err == nil {
		t.Fatal("ls-remote to closed loopback port unexpectedly succeeded")
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("production runner executed ambient GIT_SSH_COMMAND: marker status = %v", statErr)
	}
}

func runGitSetup(t *testing.T, gitPath, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = dir
	cmd.Env = cleanGitSetupEnvironment(os.Environ())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Git setup %q failed: %v: %s", args, err, output)
	}
}

func cleanGitSetupEnvironment(env []string) []string {
	clean := make([]string, 0, len(env)+2)
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if found && (strings.HasPrefix(strings.ToUpper(key), "GIT_") ||
			strings.EqualFold(key, "SSH_ASKPASS") || strings.EqualFold(key, "SSH_ASKPASS_REQUIRE")) {
			continue
		}
		clean = append(clean, entry)
	}
	return append(clean, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
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
		CodeUnavailable:            "git_unavailable",
		CodeUnsupportedVersion:     "git_version_unsupported",
		CodeAuthentication:         "auth_failed",
		CodeRemoteUnreachable:      "remote_unreachable",
		CodeIdentityMissing:        "identity_missing",
		CodeInvalidBranch:          "invalid_branch",
		CodeRepositoryRoot:         "repository_root_mismatch",
		CodeRepositoryLocked:       "repository_locked",
		CodeCommandFailed:          "git_command_failed",
		CodeTimedOut:               "git_timeout",
		CodeCanceled:               "git_canceled",
		CodeNotRepository:          "not_a_git_repository",
		CodeOriginMismatch:         "origin_mismatch",
		CodeBranchDeleted:          "branch_deleted",
		CodeRemoteHistoryRewritten: "remote_history_rewritten",
		CodePushRejected:           "push_rejected",
		CodeGitConflict:            "git_conflict",
		CodeNeedsReconnect:         "needs_reconnect",
		CodeConfirmationRequired:   "git_confirmation_required",
		CodeOperationInterrupted:   "operation_interrupted",
		CodeBackupMismatch:         "backup_mismatch",
		CodeConflictNotFound:       "git_conflict_not_found",
		CodeConflictStale:          "git_conflict_stale",
		CodeConflictUnresolved:     "git_conflict_unresolved",
		CodeConflictUnsupported:    "git_conflict_unsupported",
		CodeMergeNotInProgress:     "git_merge_not_in_progress",
		CodeRecoveryRequired:       "git_recovery_required",
		CodePaused:                 "git_paused",
		CodeNotPaused:              "git_not_paused",
	}
	if len(want) != 29 {
		t.Fatalf("error code coverage = %d, want 29", len(want))
	}
	for code, value := range want {
		if string(code) != value {
			t.Errorf("error code = %q, want %q", code, value)
		}
	}
}

func TestClassifyFailureNonFastForward(t *testing.T) {
	tests := []struct {
		name       string
		diagnostic string
		want       ErrorCode
	}{
		{name: "non-fast-forward", diagnostic: "! [rejected] main -> main (non-fast-forward)", want: CodePushRejected},
		{name: "fetch first", diagnostic: "Updates were rejected because the remote contains work that you do not have locally.\nhint: You may want to first integrate the remote changes (e.g., 'git pull ...') before pushing again.\nhint: See the 'Note about fast-forwards' in 'git push --help' for details.", want: CodePushRejected},
		{name: "authentication takes precedence", diagnostic: "Authentication failed; push rejected (non-fast-forward)", want: CodeAuthentication},
		{name: "transport takes precedence", diagnostic: "unable to access remote; Updates were rejected because the remote contains work", want: CodeRemoteUnreachable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyFailure(errors.New("exit"), test.diagnostic)
			if got.Code != test.want {
				t.Fatalf("classifyFailure() code = %q, want %q", got.Code, test.want)
			}
		})
	}
}
