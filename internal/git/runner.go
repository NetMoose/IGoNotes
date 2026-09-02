package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultLocalTimeout   = 15 * time.Second
	DefaultNetworkTimeout = 60 * time.Second
	DefaultOutputLimit    = 64 * 1024
	AllowedGitProtocols   = "file:http:https:ssh:git"
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

type CommandRunner struct {
	executable     string
	localTimeout   time.Duration
	networkTimeout time.Duration
	outputLimit    int
	command        commandFactory
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

	runErr := cmd.Run()
	diagnostic, diagnosticTruncated := redactAndLimit(stderr.buffer.String(), command.Secrets, r.outputLimit)
	stderrTruncated := stderr.truncated || diagnosticTruncated
	if runErr == nil {
		return Result{
			Stdout:          stdout.buffer.String(),
			Stderr:          diagnostic,
			StdoutTruncated: stdout.truncated,
			StderrTruncated: stderrTruncated,
		}, nil
	}

	if commandContext.Err() != nil {
		runErr = commandContext.Err()
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

	safeErr := classifyFailure(runErr, diagnostic)
	safeErr.diagnostic = diagnostic
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		safeErr.ExitCode = exitErr.ExitCode()
	}
	return Result{}, safeErr
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
	keys := map[string]struct{}{
		"GIT_TERMINAL_PROMPT": {},
		"LC_ALL":              {},
		"GIT_ALLOW_PROTOCOL":  {},
		"GIT_OPTIONAL_LOCKS":  {},
	}
	filtered := make([]string, 0, len(environment)+4)
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if _, replace := keys[key]; found && replace {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"GIT_ALLOW_PROTOCOL="+AllowedGitProtocols,
	)
	if readOnly {
		filtered = append(filtered, "GIT_OPTIONAL_LOCKS=0")
	}
	return filtered
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

func redactAndLimit(value string, secrets []string, limit int) (string, bool) {
	redacted := redact(value, secrets)
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
