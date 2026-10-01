package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultLocalTimeout   = 15 * time.Second
	DefaultNetworkTimeout = 60 * time.Second
	DefaultOutputLimit    = 64 * 1024
	AllowedGitProtocols   = "file:http:https:ssh:git"
	processWaitDelay      = 5 * time.Second
)

type OperationScope uint8

const (
	LocalOperation OperationScope = iota
	NetworkOperation
)

type Command struct {
	Dir      string
	Args     []string
	Scope    OperationScope
	ReadOnly bool
	Secrets  []string
	Stdin    io.Reader
}

type Result struct {
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
}

type Runner interface {
	Run(context.Context, Command) (Result, error)
}

type commandFactory func(context.Context, string, ...string) *exec.Cmd

// Start owns cleanup and reaping if setup fails after creating the process.
// After a successful Start, the caller owns exactly one Wait and then Close.
type processTree interface {
	Start(*exec.Cmd) error
	Terminate(*exec.Cmd) error
	Close() error
}

type processTreeFactory func() (processTree, error)

type CommandRunner struct {
	executable     string
	localTimeout   time.Duration
	networkTimeout time.Duration
	outputLimit    int
	command        commandFactory
	newProcessTree processTreeFactory
}

func NewCommandRunner() *CommandRunner {
	return newCommandRunner("git", DefaultLocalTimeout, DefaultNetworkTimeout, DefaultOutputLimit, exec.CommandContext)
}

func newCommandRunner(
	executable string,
	localTimeout time.Duration,
	networkTimeout time.Duration,
	outputLimit int,
	command commandFactory,
) *CommandRunner {
	return &CommandRunner{
		executable:     executable,
		localTimeout:   localTimeout,
		networkTimeout: networkTimeout,
		outputLimit:    outputLimit,
		command:        command,
		newProcessTree: newProcessTree,
	}
}

func (r *CommandRunner) Run(ctx context.Context, command Command) (Result, error) {
	dir, err := canonicalDirectory(command.Dir)
	if err != nil {
		return Result{}, &SafeError{
			Code:       CodeCommandFailed,
			Message:    "Git command failed",
			diagnostic: redact(err.Error(), command.Secrets),
			cause:      err,
		}
	}

	timeout := r.localTimeout
	if command.Scope == NetworkOperation {
		timeout = r.networkTimeout
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append([]string(nil), command.Args...)
	cmd := r.command(commandContext, r.executable, args...)
	cmd.Dir = dir
	cmd.Env = gitEnvironment(cmd.Env, command.ReadOnly)

	stdout := &limitedBuffer{limit: r.outputLimit}
	stderr := &limitedBuffer{limit: r.outputLimit + longestString(command.Secrets)}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = command.Stdin

	tree, runErr := r.newProcessTree()
	if runErr != nil {
		runErr = newSafeProcessError("prepare Git process tree", runErr)
	} else {
		defer tree.Close()
		// exec's watcher ends when the parent is reaped, before Wait drains
		// inherited pipes. Share one termination attempt with a full-Wait
		// context callback, and disable it before Close to avoid late PID reuse.
		var terminationMu sync.Mutex
		terminationActive, terminated := true, false
		var terminationErr error
		terminate := func() error {
			terminationMu.Lock()
			defer terminationMu.Unlock()
			if !terminationActive {
				return os.ErrProcessDone
			}
			if !terminated {
				terminationErr = tree.Terminate(cmd)
				terminated = true
			}
			return terminationErr
		}
		cmd.Cancel = terminate
		cmd.WaitDelay = processWaitDelay
		runErr = tree.Start(cmd)
		if runErr == nil {
			// Register only after Start succeeds: failed post-create setup owns
			// its own kill/Wait, and Windows cancellation must obey its launch gate.
			callbackDone := make(chan struct{})
			stopCancellation := context.AfterFunc(commandContext, func() {
				defer close(callbackDone)
				_ = terminate()
			})
			runErr = cmd.Wait()
			terminationMu.Lock()
			terminationActive = false
			terminationMu.Unlock()
			if !stopCancellation() {
				<-callbackDone
			}
		} else {
			terminationMu.Lock()
			terminationActive = false
			terminationMu.Unlock()
		}
	}
	// Cancellation wins even if the process exited successfully at the same time.
	if commandContext.Err() != nil {
		runErr = commandContext.Err()
	}
	diagnostic, diagnosticTruncated := redactAndLimit(
		stderr.buffer.String(), command.Secrets, r.outputLimit, stderr.truncated,
	)
	stderrTruncated := stderr.truncated || diagnosticTruncated
	if runErr == nil {
		return Result{
			Stdout:          stdout.buffer.String(),
			Stderr:          diagnostic,
			StdoutTruncated: stdout.truncated,
			StderrTruncated: stderrTruncated,
		}, nil
	}

	var execErr *exec.Error
	if errors.As(runErr, &execErr) {
		return Result{}, &SafeError{
			Code:       CodeUnavailable,
			Message:    "Git executable is unavailable",
			diagnostic: diagnostic,
			cause:      runErr,
		}
	}
	var processErr *SafeError
	if errors.As(runErr, &processErr) {
		return Result{}, processErr
	}

	safeErr := classifyFailure(runErr, diagnostic)
	safeErr.diagnostic = diagnostic
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		safeErr.ExitCode = exitErr.ExitCode()
	}
	return Result{}, safeErr
}

func newSafeProcessError(action string, err error) *SafeError {
	return &SafeError{
		Code: CodeCommandFailed, Message: "Git command failed",
		cause: fmt.Errorf("%s: %w", action, err),
	}
}

func canonicalDirectory(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", &os.PathError{Op: "open", Path: canonical, Err: errors.New("not a directory")}
	}
	return canonical, nil
}

func gitEnvironment(environment []string, readOnly bool) []string {
	if environment == nil {
		environment = os.Environ()
	}
	filtered := make([]string, 0, len(environment)+6)
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found && (isGitEnvironmentKey(key) ||
			strings.EqualFold(key, "LC_ALL") ||
			strings.EqualFold(key, "SSH_ASKPASS") ||
			strings.EqualFold(key, "SSH_ASKPASS_REQUIRE")) {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"GIT_ALLOW_PROTOCOL="+AllowedGitProtocols,
		"GIT_NO_LAZY_FETCH=1",
		"SSH_ASKPASS_REQUIRE=never",
	)
	if readOnly {
		filtered = append(filtered, "GIT_OPTIONAL_LOCKS=0")
	}
	return filtered
}

func isGitEnvironmentKey(key string) bool {
	return len(key) >= len("GIT_") && strings.EqualFold(key[:len("GIT_")], "GIT_")
}

func longestString(values []string) int {
	longest := 0
	for _, value := range values {
		if len(value) > longest {
			longest = len(value)
		}
	}
	return longest
}

func redactAndLimit(value string, secrets []string, limit int, captureTruncated bool) (string, bool) {
	redacted := redact(value, secrets)
	if captureTruncated {
		redacted = truncatedHTTPAuthorityPattern.ReplaceAllString(redacted, `${1}[REDACTED]`)
	}
	if len(redacted) <= limit {
		return redacted, false
	}
	return redacted[:limit], true
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if len(p) > remaining {
		b.truncated = true
	}
	if remaining > 0 {
		count := len(p)
		if count > remaining {
			count = remaining
		}
		_, _ = b.buffer.Write(p[:count])
	}
	return len(p), nil
}
